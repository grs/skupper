package main

import (
	"fmt"
	"log"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/skupperproject/skupper/client"
	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/kube"
	skupperclient "github.com/skupperproject/skupper/pkg/generated/client/clientset/versioned"
	skupperv1alpha1 "github.com/skupperproject/skupper/pkg/apis/skupper/v1alpha1"
	"github.com/skupperproject/skupper/pkg/version"
)

type NetworkController struct {
	kubeClient           kubernetes.Interface
	skupperClient        skupperclient.Interface
	controller           *kube.Controller
	stopCh               <-chan struct{}
	siteWatcher          *kube.SiteWatcher
	serverSecretWatcher  *kube.SecretWatcher
	ingressBindingWatcher *kube.RequiredServiceWatcher
	egressBindingWatcher  *kube.ProvidedServiceWatcher
	networkManager       *kube.NetworkManager
}

func NewNetworkController() (*NetworkController, error) {
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
		log.Println("Skupper network controller watching current namespace ", watchNamespace)
	} else {
		watchNamespace = metav1.NamespaceAll
		log.Println("Skupper network controller watching all namespaces")
	}
	log.Printf("Version: %s", version.Version)

	controller := &NetworkController{
		kubeClient:           cli.KubeClient,
		skupperClient:        skupperCli,
		controller:           kube.NewController("NetworkController", cli.KubeClient, skupperCli),
	}
	controller.siteWatcher = controller.controller.WatchSites(watchNamespace, controller.siteEvent)
	controller.serverSecretWatcher = controller.controller.WatchSecrets(kube.ListByName("skupper-site-server"), watchNamespace, controller.checkServerSecret)
	controller.ingressBindingWatcher = controller.controller.WatchRequiredServices(watchNamespace, controller.ingressBindingEvent)
	controller.egressBindingWatcher = controller.controller.WatchProvidedServices(watchNamespace, controller.egressBindingEvent)
	controller.networkManager = kube.NewNetworkManager(controller.kubeClient, controller.skupperClient, "skupper-network-ca", "skupper-network-controller")

	return controller, nil
}

func (c *NetworkController) Run(stopCh <-chan struct{}) error {
	log.Println("Starting the Skupper network controller informers")
	event.StartDefaultEventStore(stopCh)
	c.siteWatcher.Start(stopCh)
	c.serverSecretWatcher.Start(stopCh)
	c.ingressBindingWatcher.Start(stopCh)
	c.egressBindingWatcher.Start(stopCh)
	c.stopCh = stopCh
	err := c.networkManager.ReadOrGenerateCA()
	if err != nil {
		return err
	}

	log.Println("Waiting for informer caches to sync")
	if ok := c.siteWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for caches to sync")
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
	log.Println("Starting event loop")
	c.controller.Start(stopCh)
	<-stopCh
	log.Println("Shutting down")
	return nil
}

func (c *NetworkController) siteEvent(key string, site *skupperv1alpha1.Site) error {
	if site == nil {
		return nil
	}
	ns := c.networkManager.GetSite(key, site)
	if !ns.HasServerSecret() {
		err := ns.GenerateServerSecret()
		if err != nil {
			return err
		}
	}
	err := c.networkManager.Link(ns)
	if err != nil {
		return err
	}
	return nil
}

func (c *NetworkController) checkServerSecret(key string, secret *corev1.Secret) error {
	if secret == nil {
		//cert has been deleted, do we need to regenerate?
	}
	//TODO: if it has been modified, should it be saved again?

	//how do we determine the site for this secret?
	return nil
}

func (c *NetworkController) ingressBindingEvent(key string, binding *skupperv1alpha1.RequiredService) error {
	if binding == nil {
		return nil
	}
	return c.ensureSiteFor(key, )
}

func (c *NetworkController) egressBindingEvent(key string, binding *skupperv1alpha1.ProvidedService) error {
	if binding == nil {
		return nil
	}
	return c.ensureSiteFor(key)
}

func (c *NetworkController) ensureSiteFor(key string) error {
	namespace, _, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Printf("Error resolving namespace for %q: %s", key, err)
		return err
	}
	items, err := c.skupperClient.SkupperV1alpha1().Sites(namespace).List(metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(items.Items) > 0 {
		return nil
	}
	site := &skupperv1alpha1.Site{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "skupper.io/v1alpha1",
			Kind:       "Site",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	_, err = c.skupperClient.SkupperV1alpha1().Sites(namespace).Create(site)
	return err
}
