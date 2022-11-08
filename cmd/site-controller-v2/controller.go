package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	routev1client "github.com/openshift/client-go/route/clientset/versioned/typed/route/v1"

	"github.com/skupperproject/skupper/api/types"
	"github.com/skupperproject/skupper/client"
	skupperv1alpha1 "github.com/skupperproject/skupper/pkg/apis/skupper/v1alpha1"
	"github.com/skupperproject/skupper/pkg/event"
	skupperclient "github.com/skupperproject/skupper/pkg/generated/client/clientset/versioned"
	"github.com/skupperproject/skupper/pkg/kube"
	"github.com/skupperproject/skupper/pkg/utils"
	"github.com/skupperproject/skupper/pkg/version"
)

type ServiceGroupController struct {
	name           string
	namespace      string
	selector       string
	ports          []skupperv1alpha1.ServicePort
	client         skupperclient.Interface
	controller     *kube.Controller
	podWatcher     *kube.PodWatcher
	serviceWatcher *kube.ServiceWatcher
	stopPodWatch   chan struct{}
	stopSvcWatch   chan struct{}
	ownerRefs      []metav1.OwnerReference
}

type SiteController struct {
	vanClient             *client.VanClient
	skupperClient         skupperclient.Interface
	routeClient           *routev1client.RouteV1Client
	controller            *kube.Controller
	stopCh                <-chan struct{}
	siteWatcher           *kube.SiteWatcher
	ingressBindingWatcher *kube.RequiredServiceWatcher
	egressBindingWatcher  *kube.ProvidedServiceWatcher
	serviceGroupWatcher   *kube.ServiceGroupWatcher
	tokenWatcher          *kube.SecretWatcher
	serverSecretWatcher   *kube.SecretWatcher
	sites                 map[string]*kube.SiteManager
	defaultSiteOptions    kube.DefaultSiteOptions
	serviceGroups         map[string]*ServiceGroupController
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
		serviceGroups:        map[string]*ServiceGroupController{},
	}
	controller.siteWatcher = controller.controller.WatchSites(watchNamespace, controller.checkSite)
	controller.ingressBindingWatcher = controller.controller.WatchRequiredServices(watchNamespace, controller.checkRequiredService)
	controller.egressBindingWatcher = controller.controller.WatchProvidedServices(watchNamespace, controller.checkProvidedService)
	controller.serviceGroupWatcher = controller.controller.WatchServiceGroups(watchNamespace, controller.serviceGroupEvent)
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
	c.serviceGroupWatcher.Start(stopCh)
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
	if ok := c.serviceGroupWatcher.Sync(stopCh); !ok {
		return fmt.Errorf("Failed to wait for service group watcher to sync")
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
	mgr := kube.NewSiteManager(c.vanClient.KubeClient, c.skupperClient, c.vanClient.RouteClient, c.vanClient.DynamicClient, &c.defaultSiteOptions)
	c.sites[namespace] = mgr
	return mgr
}

func (c *SiteController) checkSite(key string, site *skupperv1alpha1.Site) error {
	if site == nil {
		return nil
	}
	if kube.HasSyncTarget(&site.ObjectMeta) {
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
	if secret != nil && kube.HasSyncTarget(&secret.ObjectMeta) {
		return nil
	}
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
	if secret != nil && kube.HasSyncTarget(&secret.ObjectMeta) {
		return nil
	}
	log.Printf("Checking link for %s (%s)", key, secret == nil)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Printf("Error resolving namespace for %q: ", key, err)
		return err
	}

	siteMgr := c.siteMgr(namespace)
	if secret == nil {
		log.Printf("Handling link deletion for %s", key)
		return siteMgr.RemoveLink(name)
	} else {
		log.Printf("Handling link creation for %s", key)
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

func getOwnerReferencesForServiceGroup(sg *skupperv1alpha1.ServiceGroup) []metav1.OwnerReference {
	if sg == nil {
		return nil
	}
	return []metav1.OwnerReference{
		{
			APIVersion: "skupper.io/v1alpha1",
			Kind:       "ServiceGroup",
			Name:       sg.ObjectMeta.Name,
			UID:        sg.ObjectMeta.UID,
		},
	}
}

func (c *SiteController) serviceGroupEvent(key string, group *skupperv1alpha1.ServiceGroup) error {
	log.Printf("ServiceGroup event for %s", key)
	sgc, ok := c.serviceGroups[key]
	if ok {
		if group == nil {
			sgc.stop()
			delete(c.serviceGroups, key)
		}
	} else if group != nil {
		sgc := &ServiceGroupController{
			name:           group.ObjectMeta.Name,
			namespace:      group.ObjectMeta.Namespace,
			client:         c.skupperClient,
			controller:     c.controller,
			ownerRefs:      getOwnerReferencesForServiceGroup(group),
		}
		c.serviceGroups[key] = sgc
		log.Printf("Watching ServiceGroup %s", sgc.name)
		return sgc.startSvcWatcher()
	}
	return nil
}

func (c *ServiceGroupController) startSvcWatcher() error {
	c.stopSvcWatch = make(chan struct{})
	c.serviceWatcher = c.controller.WatchService(c.name, c.namespace, c.svcEvent)
	c.serviceWatcher.Start(c.stopSvcWatch)
	if ok := c.serviceWatcher.Sync(c.stopSvcWatch); !ok {
		return fmt.Errorf("Failed to wait for pod watcher to sync")
	}
	return nil
}

func (c *ServiceGroupController) startPodWatcher() error {
	c.stopPodWatch = make(chan struct{})
	c.podWatcher = c.controller.WatchPods(c.selector, c.namespace, c.podEvent)
	c.podWatcher.Start(c.stopPodWatch)
	if ok := c.podWatcher.Sync(c.stopPodWatch); !ok {
		return fmt.Errorf("Failed to wait for pod watcher to sync")
	}
	return nil
}

func (c *ServiceGroupController) stop() {
	c.stopPodWatcher()
	c.stopSvcWatcher()
}

func (c *ServiceGroupController) stopPodWatcher() {
	close(c.stopPodWatch)
}

func (c *ServiceGroupController) stopSvcWatcher() {
	close(c.stopSvcWatch)
}

func (s *ServiceGroupController) svcEvent(key string, svc *corev1.Service) error {
	log.Printf("Got service event %s for ServiceGroup %s", key, s.name)
	if svc == nil {
		s.stopPodWatcher()
	}
	var ports []skupperv1alpha1.ServicePort
	for _, p := range svc.Spec.Ports {
		ports = append(ports, skupperv1alpha1.ServicePort{Name: p.Name, Port: int(p.Port)})
	}
	s.ports = ports
	if selector := utils.StringifySelector(svc.Spec.Selector); selector != s.selector || s.podWatcher == nil {
		if s.podWatcher != nil {
			s.stopPodWatcher()
		}
		s.selector = selector
		s.startPodWatcher()
	}

	return nil
}

func (s *ServiceGroupController) podEvent(key string, pod *corev1.Pod) error {
	podName := strings.Split(key, "/")[1]
	svcName := podName + "-" + s.name
	if pod == nil {
		log.Printf("Deleting ProvidedService %s/%s", s.namespace, svcName)
		err := s.client.SkupperV1alpha1().ProvidedServices(s.namespace).Delete(svcName, &metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		log.Printf("Deleting ExternlaName service %s/%s", s.namespace, svcName)
		err = s.serviceWatcher.DeleteService(svcName)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		return nil
	} else {
		host := podName + "." + s.name + "." + s.namespace + ".svc.cluster.local"
		err := s.ensureProvidedService(svcName, host)
		if err != nil {
			return err
		}
		return s.ensureExternalNameService(svcName, host)
	}
	return nil
}

func (s *ServiceGroupController) ensureExternalNameService(name string, host string) error {
	log.Printf("Checking for ExternalName service %s/%s", s.namespace, name)
	svc, err := s.serviceWatcher.Get(s.namespace + "/" +name)
	if errors.IsNotFound(err) || svc == nil{
		log.Printf("Creating ExternalName service %s/%s", s.namespace, name)
		service := &corev1.Service{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Service",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				OwnerReferences: s.ownerRefs,
			},
			Spec: corev1.ServiceSpec{
				Type:         corev1.ServiceTypeExternalName,
				ExternalName: host,
			},
		}
		_, err = s.serviceWatcher.CreateService(service)
		if !errors.IsAlreadyExists(err) {
			return err
		}
		return nil
	} else if err != nil {
		return err
	}
	log.Printf("ExternalName service %s/%s already exists", s.namespace, name)
	return nil
}

func (s *ServiceGroupController) ensureProvidedService(name string, host string) error {
	existing, err := s.client.SkupperV1alpha1().ProvidedServices(s.namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		//create it
		svc := &skupperv1alpha1.ProvidedService{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "skupper.io/v1alpha1",
				Kind:       "ProvidedService",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				OwnerReferences: s.ownerRefs,
				Labels:          map[string]string{
					"skupper.io/advertise":    "true",
					"skupper.io/servicegroup": s.name,
				},
			},
			Spec: skupperv1alpha1.ProvidedServiceSpec{
				Host: host,
				Ports: s.ports,
			},
		}
		_, err := s.client.SkupperV1alpha1().ProvidedServices(s.namespace).Create(svc)
		return err
	} else if err != nil {
		return err
	}
	if existing.Spec.Host != host {
		existing.Spec.Host = host
		_, err = s.client.SkupperV1alpha1().ProvidedServices(s.namespace).Update(existing)
		return err
	}
	return nil
}
