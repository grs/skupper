package main

import (
	"fmt"
	"log"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/skupperproject/skupper/client"
	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/kube"
	skupperclient "github.com/skupperproject/skupper/pkg/generated/client/clientset/versioned"
	"github.com/skupperproject/skupper/pkg/version"
)

type Syncer struct {
	local                kube.SyncContext
	controller           *kube.Controller
	syncers              map[string]*kube.Syncer
	pullSecretWatcher    *kube.SecretWatcher
	pushSecretWatcher    *kube.SecretWatcher
}

func NewSyncer() (*Syncer, error) {
	namespace := os.Getenv("NAMESPACE")
	kubeconfig := os.Getenv("KUBECONFIG")
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
		log.Println("Skupper syncer watching current namespace ", watchNamespace)
	} else {
		watchNamespace = metav1.NamespaceAll
		log.Println("Skupper syncer watching all namespaces")
	}
	log.Printf("Version: %s", version.Version)

	controller := &Syncer{
		local: kube.SyncContext{
			KubeClient:     cli.KubeClient,
			SkupperClient:  skupperCli,
			Namespace:      watchNamespace,
		},
		controller: kube.NewController("Syncer", cli.KubeClient, skupperCli),
		syncers:    map[string]*kube.Syncer{},
	}
	controller.pullSecretWatcher = controller.controller.WatchSecrets(kube.ListByLabelSelector("skupper.io/type=pull-secret"), watchNamespace, controller.pullSecretEvent)
	controller.pushSecretWatcher = controller.controller.WatchSecrets(kube.ListByLabelSelector("skupper.io/type=push-secret"), watchNamespace, controller.pushSecretEvent)

	return controller, nil
}

func (c *Syncer) Run(stopCh <-chan struct{}) error {
	log.Println("Starting the Skupper site syncer informers")
	event.StartDefaultEventStore(stopCh)
	c.pullSecretWatcher.Start(stopCh)
	c.pushSecretWatcher.Start(stopCh)

	log.Println("Waiting for informer caches to sync")
	if ok := c.pullSecretWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for pull secret watcher to sync")
	}
	if ok := c.pushSecretWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for push secret watcher to sync")
	}
	log.Println("Starting event loop")
	c.controller.Start(stopCh)
	<-stopCh
	log.Println("Shutting down")
	return nil
}

func (c *Syncer) pullSecretEvent(key string, secret *corev1.Secret) error {
	return c.syncerEvent(key, secret, false)
}

func (c *Syncer) pushSecretEvent(key string, secret *corev1.Secret) error {
	log.Printf("push secret event for %q", key)
	return c.syncerEvent(key, secret, true)
}

func (c *Syncer) syncerEvent(key string, secret *corev1.Secret, push bool) error {
	syncer, ok := c.syncers[key]
	if secret == nil {
		if ok {
			syncer.Stop()
			delete(c.syncers, key)
		}
		return nil
	}
	if ok {
		//update the syncer client?
		return nil
	}
	remote, err := getSyncContext(secret)
	if err != nil {
		return err
	}
	if push {
		//source is local, target is remote
		syncer = kube.NewSyncer(secret.ObjectMeta.Name, c.local, *remote)
	} else {
		//source is remote, target is local
		syncer = kube.NewSyncer(secret.ObjectMeta.Name, *remote, c.local)
	}
	c.syncers[key] = syncer
	return syncer.Start()
}


func getSyncContext(secret *corev1.Secret) (*kube.SyncContext, error) {
	kubeconfig, err := clientcmd.NewClientConfigFromBytes(secret.Data["kubeconfig"])
	if err != nil {
		return nil, err
	}
	restconfig, err := kubeconfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	restconfig.ContentConfig.GroupVersion = &schema.GroupVersion{Version: "v1"}
	restconfig.APIPath = "/api"
	restconfig.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: scheme.Codecs}
	kubeClient, err := kubernetes.NewForConfig(restconfig)
	if err != nil {
		return nil, err
	}
	skupperClient, err := skupperclient.NewForConfig(restconfig)
	if err != nil {
		return nil, err
	}
	return &kube.SyncContext{
		KubeClient:    kubeClient,
		SkupperClient: skupperClient,
		Namespace:     metav1.NamespaceAll, //TODO: make configurable
	}, nil
}
