package kube

import (
	"fmt"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1informer "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/informers/internalinterfaces"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/skupperproject/skupper/pkg/event"
)

type ConfigMapHandler func(string, *corev1.ConfigMap) error

type ConfigMapController struct {
	name     string
	handler  ConfigMapHandler
	informer cache.SharedIndexInformer
	queue    workqueue.RateLimitingInterface
}

func (c *ConfigMapController) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err == nil {
		c.queue.Add(key)
	} else {
		event.Recordf(c.name, "Error retrieving key: %s", err)
	}
}

func (c *ConfigMapController) OnAdd(obj interface{}) {
	c.enqueue(obj)
}

func (c *ConfigMapController) OnUpdate(a, b interface{}) {
	c.enqueue(b)
}

func (c *ConfigMapController) OnDelete(obj interface{}) {
	c.enqueue(obj)
}

func (c *ConfigMapController) start(stopCh <-chan struct{}) error {
	go c.informer.Run(stopCh)
	if ok := cache.WaitForCacheSync(stopCh, c.informer.HasSynced); !ok {
		return fmt.Errorf("Failed to wait for caches to sync")
	}
	go wait.Until(c.run, time.Second, stopCh)
	return nil
}

func (c *ConfigMapController) stop() {
	c.queue.ShutDown()
}

func (c *ConfigMapController) run() {
	for c.process() {
	}
	log.Println("STOPPED PROCESSING!")
}

func (c *ConfigMapController) process() bool {
	log.Println("WAITING FOR SITE CONFIG CHANGE")
	obj, shutdown := c.queue.Get()
	log.Println("GOT SITE CONFIG CHANGE")

	if shutdown {
		event.Record(c.name, "Shutdown")
		log.Println("SHUTDOWN")
		return false
	}

	defer c.queue.Done(obj)
	retry := false
	if key, ok := obj.(string); ok {
		entity, exists, err := c.informer.GetStore().GetByKey(key)
		if err != nil {
			event.Recordf(c.name, "Error retrieving configmap %q: %s", key, err)
		}
		if exists {
			if configmap, ok := entity.(*corev1.ConfigMap); ok {
				event.Recordf(c.name, "%q updated", key)
				err := c.handler(key, configmap)
				if err != nil {
					retry = true
					event.Recordf(c.name, "Error handling %q: %s", key, err)
				}
			} else {
				event.Recordf(c.name, "Expected configmap, got %#v", entity)
			}
		} else {
			err := c.handler(key, nil)
			event.Recordf(c.name, "%q deleted", key)
			if err != nil {
				retry = true
				event.Recordf(c.name, "Error handling %q: %s", key, err)
			}
		}
	} else {
		event.Recordf(c.name, "Expected key to be string, was %#v", key)
	}
	c.queue.Forget(obj)

	if retry {
		c.queue.AddRateLimited(obj)
	}
	log.Println("SITE CONFIG CHANGE HANDLED")
	return true
}

func NewConfigMapController(name string, configmap string, client kubernetes.Interface, namespace string, handler ConfigMapHandler) *ConfigMapController {
	informer := corev1informer.NewFilteredConfigMapInformer(
		client,
		namespace,
		time.Second*30,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		internalinterfaces.TweakListOptionsFunc(func(options *metav1.ListOptions) {
			options.FieldSelector = "metadata.name=" + configmap
		}))
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), name)

	controller := &ConfigMapController{
		name:     name,
		handler:  handler,
		informer: informer,
		queue:    queue,
	}

	informer.AddEventHandler(controller)
	return controller
}
