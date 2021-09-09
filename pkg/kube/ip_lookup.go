package kube

import (
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1informer "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/skupperproject/skupper/pkg/event"
)

type IpLookup struct {
	informer cache.SharedIndexInformer
	queue   workqueue.RateLimitingInterface
	lookup   map[string]string
	reverse  map[string]string
	lock     sync.RWMutex
}

func NewIpLookup(cli kubernetes.Interface, namespace string) *IpLookup {
	informer := corev1informer.NewPodInformer(
		cli,
		namespace,
		time.Second*30,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "skupper-ip-lookup")

	iplookup := &IpLookup{
		informer: informer,
		queue:   queue,
		lookup:   map[string]string{},
		reverse:  map[string]string{},
	}

	informer.AddEventHandler(iplookup)

	return iplookup
}

func (i *IpLookup) getPodName(ip string) string {
	i.lock.RLock()
	defer i.lock.RUnlock()
	return i.lookup[ip]
}

//support data.NameMapping interface
func (i *IpLookup) Lookup(ip string) string {
	name := i.getPodName(ip)
	if name == "" {
		return ip
	} else {
		return name
	}
}

func (i *IpLookup) translateKeys(ips map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	i.lock.RLock()
	defer i.lock.RUnlock()
	for key, value := range ips {
		if changed, ok := i.lookup[key]; ok {
			out[changed] = value
		} else {
			out[key] = value
		}
	}
	return out
}

const (
	IpMappingEvent string = "IpMappingEvent"
)

func (i *IpLookup) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err == nil {
		i.queue.Add(key)
	} else {
		event.Recordf(IpMappingEvent, "Error retrieving key: %s", err)
	}
}

func (i *IpLookup) OnAdd(obj interface{}) {
	i.enqueue(obj)
}

func (i *IpLookup) OnUpdate(a, b interface{}) {
	aa := a.(*corev1.Pod)
	bb := b.(*corev1.Pod)
	if aa.ResourceVersion != bb.ResourceVersion {
		i.enqueue(b)
	}
}

func (i *IpLookup) OnDelete(obj interface{}) {
	i.enqueue(obj)
}

func (i *IpLookup) updateLookup(name string, key string, ip string) {
	event.Recordf(IpMappingEvent, "%s mapped to %s", ip, name)
	i.lock.Lock()
	defer i.lock.Unlock()
	i.lookup[ip] = name
	i.reverse[key] = ip
}

func (i *IpLookup) deleteLookup(key string) string {
	i.lock.Lock()
	defer i.lock.Unlock()
	ip, ok := i.reverse[key]
	if ok {
		delete(i.lookup, ip)
		delete(i.reverse, key)
		return ip
	}
	return ""
}

func (i *IpLookup) Start(stopCh <-chan struct{}) error {
	go i.informer.Run(stopCh)
	if ok := cache.WaitForCacheSync(stopCh, i.informer.HasSynced); !ok {
		return fmt.Errorf("Failed to wait for caches to sync")
	}
	go wait.Until(i.run, time.Second, stopCh)
	return nil
}

func (i *IpLookup) Stop() {
	i.queue.ShutDown()
}

func (i *IpLookup) run() {
	for i.process() {
	}
}

func (i *IpLookup) process() bool {
	obj, shutdown := i.queue.Get()

	if shutdown {
		return false
	}

	defer i.queue.Done(obj)
	retry := false
	if key, ok := obj.(string); ok {
		entity, exists, err := i.informer.GetStore().GetByKey(key)
		if err != nil {
			event.Recordf(IpMappingEvent, "Error retrieving pod %q: %s", key, err)
		}
		if exists {
			if pod, ok := entity.(*corev1.Pod); ok {
				i.updateLookup(pod.ObjectMeta.Name, key, pod.Status.PodIP)
			} else {
				event.Recordf(IpMappingEvent, "Expected pod, got %#v", entity)
			}
		} else {
			ip := i.deleteLookup(key)
			if ip != "" {
				event.Recordf(IpMappingEvent, "mapping for %s deleted", ip)
			}
		}
	} else {
		event.Recordf(IpMappingEvent, "Expected key to be string, was %#v", key)
	}
	i.queue.Forget(obj)

	if retry && i.queue.NumRequeues(obj) < 5 {
		i.queue.AddRateLimited(obj)
	}
	return true
}
