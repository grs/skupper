package vault

import (
	"context"
	"fmt"
	"log"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers/internalinterfaces"
	"k8s.io/client-go/tools/cache"

	"github.com/skupperproject/skupper/api/types"
	"github.com/skupperproject/skupper/pkg/certs"
	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/kube"
	"github.com/skupperproject/skupper/pkg/kube/resolver"
	"github.com/skupperproject/skupper/pkg/qdr"
	sitepkg "github.com/skupperproject/skupper/pkg/site"
	"github.com/skupperproject/skupper/pkg/vault"
	"github.com/skupperproject/skupper/pkg/version"
)

type Site struct {
	namespace string
	network   string
	siteId    string
	ready     bool
	ca        *corev1.Secret
	token     *corev1.Secret
	config    *types.SiteConfig
}

type Controller struct {
	controller        *kube.Controller
	stopCh            <-chan struct{}
	nsWatcher         *kube.NamespaceWatcher
	siteWatcher       *kube.ConfigMapWatcher
	siteCaWatcher     *kube.SecretWatcher
	linkConfigWatcher *kube.SecretWatcher
	sites             map[string]*Site
	vault             vault.Client
	reachability      sitepkg.Reachability
}

func siteWatcherOptions() internalinterfaces.TweakListOptionsFunc {
	return func(options *metav1.ListOptions) {
		options.FieldSelector = "metadata.name=skupper-site"
		options.LabelSelector = "!" + types.SiteControllerIgnore
	}
}

func siteCaWatcherOptions() internalinterfaces.TweakListOptionsFunc {
	return func(options *metav1.ListOptions) {
		options.FieldSelector = "metadata.name=skupper-site-ca"
	}
}

func skupperTypeWatcherOptions(skupperType string) internalinterfaces.TweakListOptionsFunc {
	return func(options *metav1.ListOptions) {
		options.LabelSelector = "skupper.io/type=" + skupperType
	}
}

func nsWatcherOptions() internalinterfaces.TweakListOptionsFunc {
	return func(options *metav1.ListOptions) {
		options.LabelSelector = "skupper.io/network"
	}
}

func NewController(clients kube.Clients) (*Controller, error) {
	var watchNamespace string

	// Startup message
	if os.Getenv("WATCH_NAMESPACE") != "" {
		watchNamespace = os.Getenv("WATCH_NAMESPACE")
		log.Println("Skupper vault controller watching current namespace ", watchNamespace)
	} else {
		watchNamespace = metav1.NamespaceAll
		log.Println("Skupper vault controller watching all namespaces")
	}
	log.Printf("[vault]: Version: %s", version.Version)

	controller := &Controller{
		controller:    kube.NewController("VaultController", clients),
		sites:         map[string]*Site{},
		vault:         vault.NewClient(),
	}
	controller.reachability.ReadFromEnv()
	controller.nsWatcher = controller.controller.WatchNamespaces(nsWatcherOptions(), controller.checkNamespace)
	controller.siteWatcher = controller.controller.WatchConfigMaps(siteWatcherOptions(), watchNamespace, controller.checkSite)
	controller.linkConfigWatcher = controller.controller.WatchSecrets(skupperTypeWatcherOptions("connection-token"), watchNamespace, controller.checkLinkConfig)
	controller.siteCaWatcher = controller.controller.WatchSecrets(siteCaWatcherOptions(), watchNamespace, controller.checkSiteCa)

	return controller, nil
}

func (c *Controller) Run(stopCh <-chan struct{}) error {
	log.Println("Starting informers")
	event.StartDefaultEventStore(stopCh)
	c.nsWatcher.Start(stopCh)
	c.siteWatcher.Start(stopCh)
	c.linkConfigWatcher.Start(stopCh)
	c.siteCaWatcher.Start(stopCh)
	c.stopCh = stopCh

	log.Println("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.nsWatcher.HasSynced(), c.siteWatcher.HasSynced(), c.linkConfigWatcher.HasSynced(), c.siteCaWatcher.HasSynced()); !ok {
		return fmt.Errorf("Failed to wait for caches to sync")
	}

	for _, value := range c.nsWatcher.List() {
		c.checkNamespace(value.ObjectMeta.Name, value)
	}
	for _, value := range c.siteWatcher.List() {
		c.checkSite(fmt.Sprintf("%s/%s", value.ObjectMeta.Namespace, value.ObjectMeta.Name), value)
	}
	c.callback("")

	log.Println("Starting event loop")
	c.controller.Start(stopCh)
	<-stopCh
	log.Println("Shutting down")
	return nil
}

func (c *Controller) getSite(namespace string) *Site {
	if existing, ok := c.sites[namespace]; ok {
		return existing
	}
	site := &Site{
		namespace: namespace,
	}
	c.sites[namespace] = site
	return site
}

func (c *Controller) checkSite(key string, configmap *corev1.ConfigMap) error {
	if configmap != nil {
		if site, ok := c.sites[configmap.ObjectMeta.Namespace]; ok {
			if site.siteId != string(configmap.ObjectMeta.UID) {
				site.siteId = string(configmap.ObjectMeta.UID)
			}
			config, err := sitepkg.ReadSiteConfig(configmap, c.getDefaultIngress())
			if err != nil {
				return err
			}
			site.config = config
		}
	} else {
		namespace, _, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			return err
		}
		if site, ok := c.sites[namespace]; ok {
			if site.ready {
				c.vault.DeleteToken(context.Background(), site.network, site.siteId)
				delete(c.sites, namespace)
			}
		}
	}
	return nil
}

func (c *Controller) checkLinkConfig(key string, secret *corev1.Secret) error {
	return nil
}

func (c *Controller) callback(ignored string) error {
	log.Printf("[vault]: callback")
	for key, site := range c.sites {
		if site.ready {
			log.Printf("[vault]: checking links for %s (%s)", site.namespace, key)
			c.checkLinksFor(site)
		} else {
			log.Printf("[vault]: site not ready %s (%s)", site.namespace, key)
		}
	}
	c.controller.CallbackAfter(time.Second * 30, c.callback, "")
	return nil
}

func (site *Site) linkTo(local *sitepkg.Reachability, token *corev1.Secret) bool {
	remote := sitepkg.GetReachabilityFrom(token)
	if local.CanReach(remote) {
		if remote.CanReach(local) {
			// in order to prevent linking in both directions, link alphabetically
			return strings.Compare(site.siteId, token.ObjectMeta.Name) < 0
		} else {
			return true
		}
	} else {
		return false
	}
}

func (c *Controller) checkLinksFor(site *Site) {
	tokens, err := c.vault.GetTokens(context.Background(), site.network, site.siteId)
	if err != nil {
		log.Printf("[vault]: Error getting tokens from vault: %s", err)
	}
	for _, token := range tokens {
		//TODO: check if link has already been applied 
		if site.linkTo(&c.reachability, token) {
			err = c.applyToken(site.namespace, token)
			if err != nil {
				log.Printf("[vault]: Error applying token %s/%s from vault: %s", site.namespace, token.ObjectMeta.Name, err)
			} else {
				log.Printf("[vault]: Applied token %s/%s from vault", site.namespace, token.ObjectMeta.Name)
			}
		} else {
			log.Printf("[vault]: Skipping token %s from vault for %s", token.ObjectMeta.Name, site.namespace)
		}
	}
}

func (c *Controller) getRouterConfigFor(site *Site) (*qdr.RouterConfig, error) {
	configmap, err := kube.GetConfigMap(types.TransportConfigMapName, site.namespace, c.controller.GetKubeClient())
	if err != nil {
		return nil, err
	}
	return qdr.GetRouterConfigFromConfigMap(configmap)
}

func (c *Controller) getDefaultIngress() string {
	if c.controller.GetRouteClient() == nil {
		return types.IngressLoadBalancerString
	}
	return types.IngressRouteString
}

func (c *Controller) generateTokenFor(site *Site) (*corev1.Secret, error) {
	config, err := c.getRouterConfigFor(site)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, fmt.Errorf("No router config available for %s", site.namespace)
	}
	if site.config == nil {
		return nil, fmt.Errorf("No site config held yet for %s", site.namespace)
	}
	if config.IsEdge() {
		return nil, nil
	}
	token := certs.GenerateSecret(site.siteId, site.siteId, "", site.ca)
	rslvr, err := resolver.NewResolver(c.controller, site.namespace, &site.config.Spec)
	if err != nil {
		return nil, err
	}
	if rslvr.IsLocalAccessOnly() {
		return nil, nil
	}
	interRouterHostPort, err := rslvr.GetHostPortForInterRouter()
	if err != nil {
		return nil, err
	}
	edgeHostPort, err := rslvr.GetHostPortForEdge()
	if err != nil {
		return nil, err
	}

	certs.AnnotateConnectionToken(&token, "inter-router", interRouterHostPort.Host, strconv.Itoa(int(interRouterHostPort.Port)))
	certs.AnnotateConnectionToken(&token, "edge", edgeHostPort.Host, strconv.Itoa(int(edgeHostPort.Port)))
	token.Annotations[types.SiteVersion] = config.GetSiteMetadata().Version
	if token.ObjectMeta.Labels == nil {
		token.ObjectMeta.Labels = map[string]string{}
	}
	token.ObjectMeta.Labels[types.SkupperTypeQualifier] = types.TypeToken
	c.reachability.WriteToToken(&token)
	return &token, nil
}

func (c *Controller) checkSiteCa(key string, secret *corev1.Secret) error {
	log.Printf("[vault]: checkSiteCa(%s)", key)
	if secret != nil {
		if site, ok := c.sites[secret.ObjectMeta.Namespace]; ok && site != nil {
			if !site.ready {
				log.Printf("[vault]: site %s is ready", secret.ObjectMeta.Namespace)
				site.ready = true
			}
			if site.ca == nil {
				site.ca = secret
			} //TODO: else check ca key is still the same
			if site.token == nil { //TODO: check that if token exists it is issued by the current CA
				token, err := c.generateTokenFor(site)
				if err != nil || token == nil {
					return err
				}
				log.Printf("[vault]: Publishing token for %s", site.namespace)
				err = c.vault.PublishToken(context.Background(), site.network, site.siteId, token)
				if err != nil {
					return err
				}
				site.token = token

				return nil
			}
		} else {
			log.Printf("[vault]: No site being tracked for %s", key)
		}
	}
	return nil
}

func (c *Controller) checkNamespace(key string, namespace *corev1.Namespace) error {
	if namespace != nil {
		log.Printf("[vault]: Namespace %s updated", key)
		if network, ok := getLabel(&namespace.ObjectMeta, "skupper.io/network"); ok {
			site := c.getSite(key)
			if site.network == "" {
				site.network = network
				cm := &corev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1",
						Kind:       "ConfigMap",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "skupper-site",
					},
				}
				_, err := c.controller.GetKubeClient().CoreV1().ConfigMaps(key).Create(context.Background(), cm, metav1.CreateOptions{})
				if err != nil && !errors.IsAlreadyExists(err) {
					return err
				}
			} else if site.network != network {
				site.network = network
				//site has been moved to a different network
				//TODO: handle this
			}
		} else {
			log.Printf("[vault]: Ignoring namespace %s as it has no network label")
		}
	} else {
		log.Printf("[vault]: Namespace %s deleted", key)
	}
	return nil
}

func (c *Controller) applyToken(namespace string, token *corev1.Secret) error {
	key := fmt.Sprintf("%s/%s", namespace, token.ObjectMeta.Name)
	current, err := c.linkConfigWatcher.Get(key)
	if err != nil {
		return err
	}
	if current == nil {
		log.Printf("[vault]: Creating link config secret for %s", key)
		_, err = c.controller.GetKubeClient().CoreV1().Secrets(namespace).Create(context.Background(), token, metav1.CreateOptions{})
		return err
	}
	annotationsChanged := !reflect.DeepEqual(token.Annotations, current.Annotations)
	dataChanged := !reflect.DeepEqual(token.Data, current.Data)
	if annotationsChanged || dataChanged {
		log.Printf("[vault]: Updating link config secret for %s (%t %t)", key, annotationsChanged, dataChanged)
		_, err = c.controller.GetKubeClient().CoreV1().Secrets(namespace).Update(context.Background(), token, metav1.UpdateOptions{})
		return err
	}
	return nil
}

func getLabel(object *metav1.ObjectMeta, key string) (string, bool) {
	if object.Labels == nil {
		return "", false
	}
	value, ok := object.Labels[key]
	return value, ok
}
