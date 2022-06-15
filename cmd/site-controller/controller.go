package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers/internalinterfaces"
	"k8s.io/client-go/tools/cache"

	"github.com/skupperproject/skupper/api/types"
	"github.com/skupperproject/skupper/client"
	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/kube"
)

type SiteController struct {
	vanClient            *client.VanClient
	controller           *kube.Controller
	stopCh               <-chan struct{}
	siteWatcher          *kube.ConfigMapWatcher
	tokenRequestWatcher  *kube.SecretWatcher
	pullSecrets          *kube.SecretWatcher
	pushSecrets          *kube.SecretWatcher
	kcpSyncConfig        *kube.SecretWatcher
	networkManagers      map[string]*kube.NetworkManager
}

func siteWatcherOptions() internalinterfaces.TweakListOptionsFunc {
	return func(options *metav1.ListOptions) {
		options.FieldSelector = "metadata.name=skupper-site"
		options.LabelSelector = "!" + types.SiteControllerIgnore
	}
}

const (
	KcpSyncerConfigName     string = "kcp-syncer-config"
	KcpSyncerDeploymentName string = "kcp-syncer"
	KcpSyncerContainerName  string = "kcp-syncer"
	WorkloadClusterArgName  string = "--workload-cluster-name="
)

func NewSiteController(cli *client.VanClient) (*SiteController, error) {
	var watchNamespace string

	// Startup message
	if os.Getenv("WATCH_NAMESPACE") != "" {
		watchNamespace = os.Getenv("WATCH_NAMESPACE")
		log.Println("Skupper site controller watching current namespace ", watchNamespace)
	} else {
		watchNamespace = metav1.NamespaceAll
		log.Println("Skupper site controller watching all namespaces")
	}
	log.Printf("Version: %s", client.Version)

	controller := &SiteController{
		vanClient:            cli,
		controller:           kube.NewController("SiteController", cli.KubeClient),
	}
	controller.siteWatcher = controller.controller.WatchConfigMaps(siteWatcherOptions(), watchNamespace, controller.checkSite)
	controller.tokenRequestWatcher = controller.controller.WatchSecrets(kube.ListByLabelSelector(types.TypeTokenRequestQualifier), watchNamespace, controller.checkTokenRequest)
	controller.pullSecrets = controller.controller.WatchSecrets(kube.ListByLabelSelector("skupper.io/type=pull-secret"), watchNamespace, controller.pullSecretsChanged)
	controller.kcpSyncConfig = controller.controller.WatchSecrets(kube.ListByName(KcpSyncerConfigName), watchNamespace, controller.pullSecretsChanged)
	controller.pushSecrets = controller.controller.WatchSecrets(kube.ListByLabelSelector("skupper.io/type=push-secret"), watchNamespace, controller.pushSecretsChanged)
	controller.networkManagers = map[string]*kube.NetworkManager{}

	return controller, nil
}

func (c *SiteController) Run(stopCh <-chan struct{}) error {
	log.Println("Starting the Skupper site controller informers")
	event.StartDefaultEventStore(stopCh)
	c.siteWatcher.Start(stopCh)
	c.tokenRequestWatcher.Start(stopCh)
	c.pullSecrets.Start(stopCh)
	c.kcpSyncConfig.Start(stopCh)
	c.pushSecrets.Start(stopCh)
	c.stopCh = stopCh

	log.Println("Waiting for informer caches to sync")
	if ok := c.siteWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for caches to sync")
	}
	log.Printf("Checking if sites need updates (%s)", client.Version)
	c.updateChecks()
	log.Println("Starting event loop")
	c.controller.Start(stopCh)
	<-stopCh
	log.Println("Shutting down")
	return nil
}

func (c *SiteController) checkAllForSite() {
	// Now need to check whether there are any token requests already in place
	log.Println("Checking token requests...")
	c.checkAllTokenRequests()
	log.Println("Done.")
}

func (c *SiteController) checkSite(key string, configmap *corev1.ConfigMap) error {
	if configmap != nil {
		siteNamespace, _, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			log.Println("Error checking skupper-site namespace: ", err)
			return err
		}
		_, err = c.vanClient.RouterInspectNamespace(context.Background(), configmap.ObjectMeta.Namespace)
		if err == nil {
			log.Println("Skupper site exists", key)
			updatedLogging, err := c.vanClient.RouterUpdateLogging(context.Background(), configmap, false)
			if err != nil {
				log.Println("Error checking router logging configuration:", err)
			}
			updatedDebugMode, err := c.vanClient.RouterUpdateDebugMode(context.Background(), configmap)
			if err != nil {
				log.Println("Error updating router debug mode:", err)
			}
			if updatedLogging {
				if updatedDebugMode {
					log.Println("Updated router logging and debug mode for", key)
				} else {
					err = c.vanClient.RouterRestart(context.Background(), configmap.ObjectMeta.Namespace)
					if err != nil {
						log.Println("Error restarting router:", err)
					} else {
						log.Println("Updated router logging for", key)
					}
				}
			} else if updatedDebugMode {
				log.Println("Updated debug mode for", key)
			}
			updatedAnnotations, err := c.vanClient.RouterUpdateAnnotations(context.Background(), configmap)
			if err != nil {
				log.Println("Error checking annotations:", err)
			} else if updatedAnnotations {
				log.Println("Updated annotations for", key)
			}

			c.checkAllForSite()
		} else if errors.IsNotFound(err) {
			log.Println("Initialising skupper site ...")
			siteConfig, _ := c.vanClient.SiteConfigInspect(context.Background(), configmap)
			siteConfig.Spec.SkupperNamespace = siteNamespace
			err = c.vanClient.RouterCreate(context.Background(), *siteConfig)
			if err != nil {
				log.Println("Error initialising skupper: ", err)
				return err
			} else {
				log.Println("Skupper site initialised")
				c.checkAllForSite()
			}
		} else {
			log.Println("Error inspecting VAN router: ", err)
			return err
		}
	}
	return nil
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

func (c *SiteController) generate(token *corev1.Secret) error {
	log.Printf("Generating token for request %s...", token.ObjectMeta.Name)
	generated, _, err := c.vanClient.ConnectorTokenCreate(context.Background(), token.ObjectMeta.Name, token.ObjectMeta.Namespace)
	if err == nil {
		token.Data = generated.Data
		if token.ObjectMeta.Annotations == nil {
			token.ObjectMeta.Annotations = make(map[string]string)
		}
		for key, value := range generated.ObjectMeta.Annotations {
			token.ObjectMeta.Annotations[key] = value
		}
		token.ObjectMeta.Labels[types.SkupperTypeQualifier] = types.TypeToken
		siteId := c.getSiteIdForNamespace(token.ObjectMeta.Namespace)
		if siteId != "" {
			token.ObjectMeta.Annotations[types.TokenGeneratedBy] = siteId
		}
		_, err = c.vanClient.KubeClient.CoreV1().Secrets(token.ObjectMeta.Namespace).Update(token)
		return err
	} else {
		log.Printf("Failed to generate token for request %s: %s", token.ObjectMeta.Name, err)
		return err
	}
}

func (c *SiteController) checkAllTokenRequests() {
	//can we rely on the cache here?
	tokens := c.tokenRequestWatcher.List()
	for _, t := range tokens {
		// service from workqueue
		c.controller.AddEvent(t)
	}
}


func (c *SiteController) checkTokenRequest(key string, token *corev1.Secret) error {
	log.Printf("Handling token request for %s", key)
	if token != nil {
		if !c.isTokenRequestValidInSite(token) {
			log.Println("Cannot handle token request, as site not yet initialised")
			return nil
		}
		return c.generate(token)
	}
	return nil
}

func (c *SiteController) getSiteIdForNamespace(namespace string) string {
	cm, err := c.vanClient.KubeClient.CoreV1().ConfigMaps(namespace).Get(types.SiteConfigMapName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Printf("Could not obtain siteid for namespace %q, assuming not yet initialised", namespace)
		} else {
			log.Printf("Error checking siteid for namespace %q: %s", namespace, err)
		}
		return ""
	}
	return string(cm.ObjectMeta.UID)
}

func (c *SiteController) isTokenValidInSite(token *corev1.Secret) bool {
	siteId := c.getSiteIdForNamespace(token.ObjectMeta.Namespace)
	if author, ok := token.ObjectMeta.Annotations[types.TokenGeneratedBy]; ok && author == siteId {
		//token was generated by this site so should not be applied
		return false
	} else {
		return true
	}
}

func (c *SiteController) isTokenRequestValidInSite(token *corev1.Secret) bool {
	siteId := c.getSiteIdForNamespace(token.ObjectMeta.Namespace)
	if siteId == "" {
		return false
	}
	return true
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

func (c *SiteController) generateToken(name string, namespace string) (*corev1.Secret, error) {
	token, _, err := c.vanClient.ConnectorTokenCreate(context.Background(), name, namespace)
	return token, err
}

func getClusterId(deployment *appsv1.Deployment) (string, error) {
	container := kube.GetContainerByName(deployment.Spec.Template.Spec.Containers, KcpSyncerContainerName)
	if container == nil {
		return "", fmt.Errorf("Cannot determine cluster id for deployment %s, container %q not found", KcpSyncerDeploymentName, KcpSyncerContainerName)
	}
	for _, a := range container.Args {
		if strings.HasPrefix(a, WorkloadClusterArgName) {
			return strings.TrimPrefix(a, WorkloadClusterArgName), nil
		}
	}
	return "", fmt.Errorf("Cannot determine cluster id for deployment %s, argument %q not found", KcpSyncerDeploymentName, WorkloadClusterArgName)
}

func (c *SiteController) pullSecretsChanged(key string, token *corev1.Secret) error {
	var namespace string
	var cluster string

	if token.ObjectMeta.Name == KcpSyncerConfigName {
		log.Printf("KCP syncer config event for %s", key)
		//lookup cluster id from deployment args
		dep, err := c.vanClient.KubeClient.AppsV1().Deployments(token.ObjectMeta.Namespace).Get(KcpSyncerDeploymentName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("Cannot determine cluster id for deployment %s: %s", KcpSyncerDeploymentName, err)
		}
		id, err := getClusterId(dep)
		if err != nil {
			return err
		}
		cluster = id
		namespace = "kcp-network"
	} else if token.ObjectMeta.Annotations != nil && token.ObjectMeta.Annotations["skupper.io/type"] == "pull-secret" {
		log.Printf("PullSecret event for %s", key)
		cluster = token.ObjectMeta.Annotations["skupper.io/cluster"]
		namespace = token.ObjectMeta.Annotations["skupper.io/namespace"]
	} else {
		log.Printf("Ignoring secret event for %s", key)
		return nil
	}

	if mgr, ok := c.networkManagers[key]; ok {
		if !mgr.HasChanged(token) {
			return nil
		}
		mgr.Stop()
	}
	mgr, err := kube.NewNetworkManager(c.generateToken, c.vanClient.KubeClient, c.controller, c.stopCh, token.Data["kubeconfig"], namespace, cluster)
	if err != nil {
		return err
	}
	c.networkManagers[key] = mgr
	return nil
}

func (c *SiteController) pushSecretsChanged(key string, token *corev1.Secret) error {
	log.Printf("PushSecret event for %s", key)
	if token != nil {
		//create syncer for the site
		//
		// get kubernetes client interface from context held in secret
	} else {
		//delete syncer for that site
	}
	return nil
}
