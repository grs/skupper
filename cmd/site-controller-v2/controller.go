package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/skupperproject/skupper/api/types"
	"github.com/skupperproject/skupper/client"
	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/kube"
	skupperclient "github.com/skupperproject/skupper/pkg/generated/client/clientset/versioned"
	skupperv1alpha1 "github.com/skupperproject/skupper/pkg/apis/skupper/v1alpha1"
	"github.com/skupperproject/skupper/pkg/version"
)

type SiteController struct {
	vanClient             *client.VanClient
	skupperClient         skupperclient.Interface
	controller            *kube.Controller
	stopCh                <-chan struct{}
	siteWatcher           *kube.SiteWatcher
	ingressBindingWatcher *kube.RequiredServiceWatcher
	egressBindingWatcher  *kube.ProvidedServiceWatcher
	tokenWatcher          *kube.SecretWatcher
	serverSecretWatcher   *kube.SecretWatcher
	sites                 map[string]*kube.SiteManager
	defaultSiteOptions    kube.DefaultSiteOptions
}

func NewSiteController() (*SiteController, error) {
	namespace := os.Getenv("NAMESPACE")
	kubeconfig := os.Getenv("KUBECONFIG")
	// todo, get context from env?
	cli, err := client.NewClient(namespace, "", kubeconfig)
	if err != nil {
		log.Fatal("Error getting van client ", err.Error())
	}
	skupperCli, err := skupperclient.NewForConfig(cli.RestConfig)
	if err != nil {
		return nil, err
	}

	var watchNamespace string

	// Startup message
	if os.Getenv("WATCH_NAMESPACE") != "" {
		watchNamespace = os.Getenv("WATCH_NAMESPACE")
		log.Printf("Skupper site controller watching namespace %q", watchNamespace)
	} else {
		watchNamespace = metav1.NamespaceAll
		log.Println("Skupper site controller watching all namespaces")
	}
	log.Printf("Version: %s", version.Version)

	controller := &SiteController{
		vanClient:            cli,
		skupperClient:        skupperCli,
		controller:           kube.NewController("SiteController", cli.KubeClient, skupperCli),
		sites:                map[string]*kube.SiteManager{},
	}
	controller.siteWatcher = controller.controller.WatchSites(watchNamespace, controller.checkSite)
	controller.ingressBindingWatcher = controller.controller.WatchRequiredServices(watchNamespace, controller.checkRequiredService)
	controller.egressBindingWatcher = controller.controller.WatchProvidedServices(watchNamespace, controller.checkProvidedService)
	controller.tokenWatcher = controller.controller.WatchSecrets(kube.ListByLabelSelector(types.TypeTokenQualifier), watchNamespace, controller.checkLink)
	controller.serverSecretWatcher = controller.controller.WatchSecrets(kube.ListByName("skupper-site-server"), watchNamespace, controller.checkServerSecret)

	controller.defaultSiteOptions.IngressMode = kube.GetIngressModeFromString(os.Getenv("SKUPPER_DEFAULT_INGRESS_MODE"))
	controller.defaultSiteOptions.IngressHostSuffix = os.Getenv("SKUPPER_DEFAULT_INGRESS_HOST_SUFFIX")

	return controller, nil
}

func (c *SiteController) Run(stopCh <-chan struct{}) error {
	log.Println("Starting the Skupper site controller informers")
	event.StartDefaultEventStore(stopCh)
	c.siteWatcher.Start(stopCh)
	c.tokenWatcher.Start(stopCh)
	c.serverSecretWatcher.Start(stopCh)
	c.ingressBindingWatcher.Start(stopCh)
	c.egressBindingWatcher.Start(stopCh)
	c.stopCh = stopCh

	log.Println("Waiting for informer caches to sync")
	if ok := c.siteWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for site watcher to sync")
	}
	if ok := c.tokenWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for token watcher to sync")
	}
	if ok := c.serverSecretWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for server secret watcher to sync")
	}
	if ok := c.ingressBindingWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for ingress binding watcher to sync")
	}
	if ok := c.egressBindingWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for ingress binding watcher to sync")
	}
	log.Printf("Checking if sites need updates (%s)", version.Version)
	c.updateChecks()
	log.Println("Starting event loop")
	c.controller.Start(stopCh)
	<-stopCh
	log.Println("Shutting down")
	return nil
}

func (c *SiteController) siteMgr(namespace string) *kube.SiteManager {
	if mgr, ok := c.sites[namespace]; ok {
		return mgr
	}
	mgr := kube.NewSiteManager(c.vanClient.KubeClient, c.skupperClient, c.vanClient.DynamicClient, &c.defaultSiteOptions)
	c.sites[namespace] = mgr
	return mgr
}

func (c *SiteController) checkSite(key string, site *skupperv1alpha1.Site) error {
	if site == nil {
		return nil
	}
	namespace, _, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Printf("Error resolving namespace for %q: ", key, err)
		return err
	}

	siteMgr := c.siteMgr(namespace)
	addressUpToDate, err := siteMgr.Reconcile(site)
	if err != nil {
		log.Printf("Error reconciling site %q: %s", site.ObjectMeta.Name, err)
		return err
	}
	if !addressUpToDate {
		log.Printf("Address not ready for %q", namespace)
		c.controller.CallbackAfter(time.Second, c.checkAddress, namespace)
		return nil
	}
	log.Printf("Updated address for %q", namespace)
	return nil
}

func (c *SiteController) checkAddress(namespace string) error {
	siteMgr := c.siteMgr(namespace)
	addressUpToDate, err := siteMgr.UpdateAddress()
	if err != nil {
		log.Printf("Error updating address for %q: %s", namespace, err)
		return err
	}
	if !addressUpToDate {
		log.Printf("Address not ready for %q", namespace)
		c.controller.CallbackAfter(time.Second, c.checkAddress, namespace)
		return nil
	}
	log.Printf("Updated address for %q", namespace)
	return nil
}

func (c *SiteController) checkRequiredService(key string, binding *skupperv1alpha1.RequiredService) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Printf("Error resolving namespace for %q: %s", key, err)
		return err
	}
	log.Printf("Ingress binding event for %q", key)

	siteMgr := c.siteMgr(namespace)
	if binding == nil {
		return siteMgr.RemoveIngressBinding(name)
	} else {
		return siteMgr.EnsureIngressBinding(binding)
	}
}

func (c *SiteController) checkProvidedService(key string, binding *skupperv1alpha1.ProvidedService) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Printf("Error resolving namespace for %q: %s", key, err)
		return err
	}
	log.Printf("Egress binding event for %q", key)

	siteMgr := c.siteMgr(namespace)
	if binding == nil {
		return siteMgr.RemoveEgressBinding(name)
	} else {
		return siteMgr.EnsureEgressBinding(binding)
	}
}

func (c *SiteController) checkServerSecret(key string, secret *corev1.Secret) error {
	log.Printf("Checking server secret for %s", key)
	namespace, _, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Printf("Error resolving namespace for %q: ", key, err)
		return err
	}

	siteMgr := c.siteMgr(namespace)
	err = siteMgr.EnableListeners()
	if err != nil {
		log.Printf("Error configuring listeners for %q: ", key, err)
		return err
	}
	return nil
}

func (c *SiteController) checkLink(key string, secret *corev1.Secret) error {
	log.Printf("Handling link request for %s", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Printf("Error resolving namespace for %q: ", key, err)
		return err
	}

	siteMgr := c.siteMgr(namespace)
	if secret == nil {
		return siteMgr.RemoveLink(name)
	} else {
		return siteMgr.EnsureLink(secret)
	}
}

func getTokenCost(token *corev1.Secret) (int32, bool) {
	if token.ObjectMeta.Annotations == nil {
		return 0, false
	}
	if costString, ok := token.ObjectMeta.Annotations[types.TokenCost]; ok {
		cost, err := strconv.Atoi(costString)
		if err != nil {
			log.Printf("Ignoring invalid cost annotation %q", costString)
			return 0, false
		}
		return int32(cost), true
	}
	return 0, false
}

func (c *SiteController) connect(token *corev1.Secret, namespace string) error {
	log.Printf("Connecting site in %s using token %s", namespace, token.ObjectMeta.Name)
	var options types.ConnectorCreateOptions
	options.Name = token.ObjectMeta.Name
	options.SkupperNamespace = namespace
	if cost, ok := getTokenCost(token); ok {
		options.Cost = cost
	}
	return c.vanClient.ConnectorCreate(context.Background(), token, options)
}

func (c *SiteController) disconnect(name string, namespace string) error {
	log.Printf("Disconnecting connector %s from site in %s", name, namespace)
	var options types.ConnectorRemoveOptions
	options.Name = name
	options.SkupperNamespace = namespace
	// Secret has already been deleted so force update to current active secrets
	options.ForceCurrent = true
	return c.vanClient.ConnectorRemove(context.Background(), options)
}

func (c *SiteController) updateChecks() {
	sites := c.siteWatcher.List()
	for _, site := range sites {
		updated, err := c.vanClient.RouterUpdateVersionInNamespace(context.Background(), false, site.ObjectMeta.Namespace)
		if err != nil {
			log.Printf("Version update check failed for namespace %q: %s", site.ObjectMeta.Namespace, err)
		} else if updated {
			log.Printf("Updated version for namespace %q", site.ObjectMeta.Namespace)
		} else {
			log.Printf("Version update not required for namespace %q", site.ObjectMeta.Namespace)
		}
	}
}
