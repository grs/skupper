/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kube

import (
	"fmt"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1informer "k8s.io/client-go/informers/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers/internalinterfaces"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/skupperproject/skupper/pkg/event"
)

type ResourceChange struct {
	Handler ResourceChangeHandler
	Key     string
}

type ResourceChangeHandler interface {
	Handle(event ResourceChange) error
	Describe(event ResourceChange) string
}

func ListByNameOptions(name string) internalinterfaces.TweakListOptionsFunc {
	return func(options *metav1.ListOptions) {
		options.FieldSelector = "metadata.name=" + name
	}
}

func ListByLabelSelector(selector string) internalinterfaces.TweakListOptionsFunc {
	return func(options *metav1.ListOptions) {
		options.LabelSelector = selector
	}
}

type Controller struct {
	eventKey string
	errorKey string
	client   kubernetes.Interface
	queue    workqueue.RateLimitingInterface
}

func NewController(name string, client kubernetes.Interface) *Controller {
	return &Controller{
		eventKey: name + "Event",
		errorKey: name + "Error",
		client: client,
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), name),
	}
}

func (c *Controller) NewWatchers(client kubernetes.Interface) Watchers {
	return &Controller{
		eventKey: c.eventKey,
		errorKey: c.errorKey,
		client: client,
		queue: c.queue,
	}
}

func (c *Controller) Start(stopCh <-chan struct{}) {
	go wait.Until(c.run, time.Second, stopCh)
}

func (c *Controller) run() {
	for c.process() {
	}
}

func (c *Controller) process() bool {
	obj, shutdown := c.queue.Get()

	if shutdown {
		return false
	}

	retry := false
	defer c.queue.Done(obj)
	if evt, ok := obj.(ResourceChange); ok {
		event.Recordf(c.eventKey, evt.Handler.Describe(evt))
		err := evt.Handler.Handle(evt)
		if err != nil {
			retry = true
			event.Recordf(c.errorKey, "Error handling %s: %s", evt.Handler.Describe(evt), err)
			log.Printf("Error handling %q %s: %s", c.errorKey, evt.Handler.Describe(evt), err)
		}
	} else {
		event.Recordf(c.errorKey, "Invalid object on event queue: %#v", obj)
		log.Printf("Invalid object on event queue for %q: %#v", c.errorKey, obj)
	}
	c.queue.Forget(obj)

	if retry && c.queue.NumRequeues(obj) < 5 {
		c.queue.AddRateLimited(obj)
	}

	return true
}


func (c *Controller) Stop() {
	c.queue.ShutDown()
}

func (c *Controller) AddEvent(o interface{}) {
	c.queue.Add(o)
}

func (c *Controller) Empty() bool {
	return c.queue.Len() == 0
}

func (c *Controller) newEventHandler(handler ResourceChangeHandler) *cache.ResourceEventHandlerFuncs {
	evt := ResourceChange {
		Handler: handler,
	}
	return &cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err != nil {
				utilruntime.HandleError(err)
			} else {
				evt.Key = key
				c.queue.Add(evt)
			}
		},
		UpdateFunc: func(old, new interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(new)
			if err != nil {
				utilruntime.HandleError(err)
			} else {
				evt.Key = key
				c.queue.Add(evt)
			}
		},
		DeleteFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err != nil {
				utilruntime.HandleError(err)
			} else {
				evt.Key = key
				c.queue.Add(evt)
			}
		},
	}
}

type Watchers interface{
	WatchConfigMaps(options internalinterfaces.TweakListOptionsFunc, namespace string, handler ConfigMapHandler) *ConfigMapWatcher
	WatchSecrets(options internalinterfaces.TweakListOptionsFunc, namespace string, handler SecretHandler) *SecretWatcher
}

func (c *Controller) WatchConfigMaps(options internalinterfaces.TweakListOptionsFunc, namespace string, handler ConfigMapHandler) *ConfigMapWatcher {
	watcher := &ConfigMapWatcher{
		handler:   handler,
		informer:  corev1informer.NewFilteredConfigMapInformer(
			c.client,
			namespace,
			time.Second*30,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			options),
		namespace: namespace,
	}

	watcher.informer.AddEventHandler(c.newEventHandler(watcher))
	return watcher
}

type ConfigMapHandler func(string, *corev1.ConfigMap) error

type ConfigMapWatcher struct {
	handler   ConfigMapHandler
	informer  cache.SharedIndexInformer
	namespace string
}

func (w *ConfigMapWatcher) Handle(event ResourceChange) error {
	obj, err := w.Get(event.Key)
	if err != nil {
		return err
	}
	return w.handler(event.Key, obj)
}

func (w *ConfigMapWatcher) Describe(event ResourceChange) string {
	return fmt.Sprintf("ConfigMap %s", event.Key)
}

func (w *ConfigMapWatcher) Start(stopCh <-chan struct{}) {
	go w.informer.Run(stopCh)
}

func (w *ConfigMapWatcher) Sync(stopCh <-chan struct{}) bool {
	return cache.WaitForCacheSync(stopCh, w.informer.HasSynced)
}

func (w *ConfigMapWatcher) Get(key string) (*corev1.ConfigMap, error) {
	entity, exists, err := w.informer.GetStore().GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return entity.(*corev1.ConfigMap), nil
}

func (w *ConfigMapWatcher) List() []*corev1.ConfigMap {
	list := w.informer.GetStore().List()
	results := []*corev1.ConfigMap{}
	for _, o := range list {
		results = append(results, o.(*corev1.ConfigMap))
	}
	return results
}

func (c *Controller) WatchSecrets(options internalinterfaces.TweakListOptionsFunc, namespace string, handler SecretHandler) *SecretWatcher {
	watcher := &SecretWatcher{
		handler:   handler,
		informer:  corev1informer.NewFilteredSecretInformer(
			c.client,
			namespace,
			time.Second*30,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			options),
		namespace: namespace,
	}

	watcher.informer.AddEventHandler(c.newEventHandler(watcher))
	return watcher
}

type SecretHandler func(string, *corev1.Secret) error

type SecretWatcher struct {
	handler   SecretHandler
	informer  cache.SharedIndexInformer
	namespace string
}

func (w *SecretWatcher) Handle(event ResourceChange) error {
	obj, err := w.Get(event.Key)
	if err != nil {
		return err
	}
	return w.handler(event.Key, obj)
}

func (w *SecretWatcher) Describe(event ResourceChange) string {
	return fmt.Sprintf("Secret %s", event.Key)
}

func (w *SecretWatcher) Start(stopCh <-chan struct{}) {
	go w.informer.Run(stopCh)
}

func (w *SecretWatcher) Sync(stopCh <-chan struct{}) bool {
	return cache.WaitForCacheSync(stopCh, w.informer.HasSynced)
}

func (w *SecretWatcher) Get(key string) (*corev1.Secret, error) {
	entity, exists, err := w.informer.GetStore().GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return entity.(*corev1.Secret), nil
}

func (w *SecretWatcher) List() []*corev1.Secret {
	list := w.informer.GetStore().List()
	results := []*corev1.Secret{}
	for _, o := range list {
		results = append(results, o.(*corev1.Secret))
	}
	return results
}

type ServiceHandler func(string, *corev1.Service) error

func (c *Controller) WatchServices(namespace string, handler ServiceHandler) *ServiceWatcher {
	watcher := &ServiceWatcher{
		client:  c.client,
		handler: handler,
		informer: corev1informer.NewServiceInformer(
			c.client,
			namespace,
			time.Second*30,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		),
		namespace: namespace,
	}

	watcher.informer.AddEventHandler(c.newEventHandler(watcher))
	return watcher
}

type ServiceWatcher struct {
	client    kubernetes.Interface
	handler   ServiceHandler
	informer  cache.SharedIndexInformer
	namespace string
}

func (w *ServiceWatcher) Handle(event ResourceChange) error {
	obj, err := w.Get(event.Key)
	if err != nil {
		return err
	}
	return w.handler(event.Key, obj)
}

func (w *ServiceWatcher) Describe(event ResourceChange) string {
	return fmt.Sprintf("Service %s", event.Key)
}

func (w *ServiceWatcher) Start(stopCh <-chan struct{}) {
	go w.informer.Run(stopCh)
}

func (w *ServiceWatcher) Sync(stopCh <-chan struct{}) bool {
	return cache.WaitForCacheSync(stopCh, w.informer.HasSynced)
}

func (w *ServiceWatcher) Get(key string) (*corev1.Service, error) {
	entity, exists, err := w.informer.GetStore().GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return entity.(*corev1.Service), nil
}

func (w *ServiceWatcher) CreateService(svc *corev1.Service) (*corev1.Service, error) {
	//TODO: set owner ref
	return w.client.CoreV1().Services(w.namespace).Create(svc)
}

func (w *ServiceWatcher) UpdateService(svc *corev1.Service) (*corev1.Service, error) {
	return w.client.CoreV1().Services(w.namespace).Update(svc)
}

func (w *ServiceWatcher) GetService(name string) (*corev1.Service, error) {
	return w.Get(name)
}

type PodHandler func(string, *corev1.Pod) error

func (c *Controller) WatchPods(selector string, namespace string, handler PodHandler) *PodWatcher {
	watcher := &PodWatcher{
		handler: handler,
		informer: corev1informer.NewFilteredPodInformer(
			c.client,
			namespace,
			time.Second*30,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			internalinterfaces.TweakListOptionsFunc(func(options *metav1.ListOptions) {
				options.LabelSelector = selector
			})),
		namespace: namespace,
	}

	watcher.informer.AddEventHandler(c.newEventHandler(watcher))
	return watcher
}

type PodWatcher struct {
	handler   PodHandler
	informer  cache.SharedIndexInformer
	namespace string
}

func (w *PodWatcher) Handle(event ResourceChange) error {
	obj, err := w.Get(event.Key)
	if err != nil {
		return err
	}
	return w.handler(event.Key, obj)
}

func (w *PodWatcher) Describe(event ResourceChange) string {
	return fmt.Sprintf("Pod %s", event.Key)
}

func (w *PodWatcher) Start(stopCh <-chan struct{}) {
	go w.informer.Run(stopCh)
}

func (w *PodWatcher) Sync(stopCh <-chan struct{}) bool {
	return cache.WaitForCacheSync(stopCh, w.informer.HasSynced)
}

func (w *PodWatcher) Get(key string) (*corev1.Pod, error) {
	entity, exists, err := w.informer.GetStore().GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return entity.(*corev1.Pod), nil
}

func (w *PodWatcher) List() []*corev1.Pod {
	list := w.informer.GetStore().List()
	pods := []*corev1.Pod{}
	for _, p := range list {
		pods = append(pods, p.(*corev1.Pod))
	}
	return pods
}
