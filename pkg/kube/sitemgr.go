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
	"reflect"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	routev1 "github.com/openshift/api/route/v1"
	routev1client "github.com/openshift/client-go/route/clientset/versioned/typed/route/v1"

	"github.com/skupperproject/skupper/api/types"
	skupperv1alpha1 "github.com/skupperproject/skupper/pkg/apis/skupper/v1alpha1"
	skupperclient "github.com/skupperproject/skupper/pkg/generated/client/clientset/versioned"
	"github.com/skupperproject/skupper/pkg/qdr"
	"github.com/skupperproject/skupper/pkg/version"
)

type EgressBinding struct {
	binding   *skupperv1alpha1.ProvidedService
	watcher   *PodWatcher
	connectors   map[string]qdr.TcpEndpoint
}

type IngressBinding struct {
	binding     *skupperv1alpha1.RequiredService
	routerPorts map[string]int
	listeners   map[string]qdr.TcpEndpoint
}

type IngressMode string

const (
	IngressModeLoadBalancer IngressMode = "loadbalancer"
	IngressModeNginxIngress IngressMode = "nginx-ingress-v1"
	IngressModeRoute        IngressMode = "route"
)

func GetIngressModeFromString(s string) IngressMode {
	if s == string(IngressModeLoadBalancer) {
		return IngressModeLoadBalancer
	}
	if s == string(IngressModeNginxIngress) {
		return IngressModeNginxIngress
	}
	if s == string(IngressModeRoute) {
		return IngressModeRoute
	}
	return ""
}

type DefaultSiteOptions struct {
	IngressMode       IngressMode
	IngressHostSuffix string
}

type SiteManager struct {
	site             *skupperv1alpha1.Site
	client           kubernetes.Interface
	skupperClient    skupperclient.Interface
	routeClient      *routev1client.RouteV1Client
	dynamicClient    dynamic.Interface
	pendingLinks     map[string]*corev1.Secret
	ingressBindings  map[string]*IngressBinding
	egressBindings   map[string]*EgressBinding
	controller       *Controller
	freePorts        *qdr.FreePorts
	portMappings     map[string]int
	haveServerSecret bool
	defaultOptions   DefaultSiteOptions
	ingressMode      IngressMode
}

func newIngressBinding(binding *skupperv1alpha1.RequiredService) *IngressBinding {
	return &IngressBinding{
		binding:     binding,
		routerPorts: map[string]int{},
		listeners:   map[string]qdr.TcpEndpoint{},
	}
}

func newEgressBinding(binding *skupperv1alpha1.ProvidedService) *EgressBinding {
	return &EgressBinding{
		binding:     binding,
		connectors:  map[string]qdr.TcpEndpoint{},
	}
}

func NewSiteManager(client kubernetes.Interface, skupperClient skupperclient.Interface, routeClient *routev1client.RouteV1Client, dynamicClient dynamic.Interface, options *DefaultSiteOptions) *SiteManager {
	siteMgr := &SiteManager{
		client:          client,
		skupperClient:   skupperClient,
		routeClient:     routeClient,
		dynamicClient:   dynamicClient,
		pendingLinks:    map[string]*corev1.Secret{},
		freePorts:       qdr.NewFreePorts(),
		portMappings:    map[string]int{},
		ingressBindings: map[string]*IngressBinding{},
		egressBindings:  map[string]*EgressBinding{},
	}
	if options != nil {
		siteMgr.defaultOptions = *options
	}
	siteMgr.ingressMode = siteMgr.getIngressMode()
	return siteMgr
}

func (m *SiteManager) getIngressMode() IngressMode {
	if m.defaultOptions.IngressMode == IngressModeNginxIngress {
		return IngressModeNginxIngress
	}
	if m.routeClient != nil && (m.defaultOptions.IngressMode == IngressModeRoute || m.defaultOptions.IngressMode == "")  {
		return IngressModeRoute
	}
	return IngressModeLoadBalancer
}

func (m *SiteManager) Reconcile(site *skupperv1alpha1.Site) (bool, error) {
	if m.site != nil && m.site.ObjectMeta.Name != site.ObjectMeta.Name {
		//TODO: indicate issue in new site's status in some way
		return false, fmt.Errorf("Only one site resource per namespace is allowed. Have %q, rejecting %q", m.site.ObjectMeta.Name, site.ObjectMeta.Name)
	}
	m.site = site
	if m.site.Spec.ServiceAccount == "" {
		err := m.checkRouterServiceAccount()
		if err != nil {
			return false, err
		}
	}
	needRouterRestart, err := m.checkRouterConfig()
	if err != nil {
		return false, err
	}
	err = m.checkRouterDeployment(needRouterRestart)
	if err != nil {
		return false, err
	}
	err = m.checkRouterService()
	if err != nil {
		return false, err
	}
	err = m.checkRouterIngress()
	if err != nil {
		return false, err
	}
	return m.UpdateAddress()
}

func (m *SiteManager) UpdateAddress() (bool, error) {
	addresses, err := m.resolveRouterAddresses()
	if err != nil {
		return false, err
	}
	if len(addresses) == 0 {
		return false, nil
	}
	if equivalentAddresses(m.site.Status.Addresses, addresses) {
		log.Printf("Addresses are equivalent for %s: %v", m.site.ObjectMeta.Name, m.site.Status.Addresses)
		return true, nil
	}
	m.site.Status.Addresses = addresses
	updated, err := m.skupperClient.SkupperV1alpha1().Sites(m.site.ObjectMeta.Namespace).UpdateStatus(m.site)
	if err != nil {
		return false, err
	}
	m.site = updated
	return true, nil
}

func (m *SiteManager) EnableListeners() error {
	m.haveServerSecret = true
	if m.site == nil {
		return nil
	}
	_, err := m.checkRouterConfig()
	if err != nil {
		return err
	}
	return m.checkRouterDeployment(true)
}

func (m *SiteManager) RemoveLink(name string) error {
	if m.site == nil {
		delete(m.pendingLinks, name)
		return nil
	}
	return m.updateRouterConfig(&RemoveLink{name})
}

func (m *SiteManager) EnsureLink(secret *corev1.Secret) error {
	if m.site == nil {
		m.pendingLinks[secret.ObjectMeta.Name] = secret
		return nil
	}
	return m.updateRouterConfig(newAddLinks(secret))
}

func (m *SiteManager) EnsureIngressBinding(binding *skupperv1alpha1.RequiredService) error {
	existing, ok := m.ingressBindings[binding.ObjectMeta.Name]
	if ok {
		existing.reconfigure(binding, m)
	} else {
		existing = newIngressBinding(binding)
		m.ingressBindings[binding.ObjectMeta.Name] = existing
	}
	if m.site == nil {
		return nil
	}
	err := existing.configure(m)
	if err != nil {
		return err
	}
	return m.updateRouterConfig(existing)
}

func (m *SiteManager) RemoveIngressBinding(name string) error {
	delete(m.ingressBindings, name)
	if m.site == nil {
		return nil
	}
	return m.updateRouterConfig(&RemoveTcpListener{name})
}

func (m *SiteManager) EnsureEgressBinding(binding *skupperv1alpha1.ProvidedService) error {
	existing, ok := m.egressBindings[binding.ObjectMeta.Name]
	if ok {
		existing.binding = binding
	} else {
		existing = newEgressBinding(binding)
		m.egressBindings[binding.ObjectMeta.Name] = existing
	}
	if m.site == nil {
		return nil
	}
	if len(binding.Spec.Selector) > 0 {
		//TODO: need to watch the pods
	}
	existing.configure(m)
	return m.updateRouterConfig(existing)
}

func (m *SiteManager) RemoveEgressBinding(name string) error {
	delete(m.egressBindings, name)
	if m.site == nil {
		return nil
	}
	return m.updateRouterConfig(&RemoveTcpConnector{name})
}

func (m *SiteManager) checkRouterConfig() (bool, error) {
	coreUpdate := m.newCoreRouterConfigCheck()
	err := m.updateRouterConfig(coreUpdate)
	if err != nil {
		return false, err
	}
	return coreUpdate.changed, nil
}

type RouterConfigUpdate interface {
	Update(current *qdr.RouterConfig) bool
}

func (m *SiteManager) updateRouterConfig(op RouterConfigUpdate) error {
	name := "skupper-internal"
	namespace := m.site.ObjectMeta.Namespace
	existing, err := m.client.CoreV1().ConfigMaps(namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		config := qdr.NewRouterConfig()
		op.Update(config)
		cm := &corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ConfigMap",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "skupper-internal",
				OwnerReferences: m.getOwnerReferences(),
			},
		}
		err = config.WriteToConfigMap(cm)
		if err != nil {
			return err
		}
		_, err = m.client.CoreV1().ConfigMaps(namespace).Create(cm)
		if err != nil {
			return err
		}
		m.clearPendingLinks()
		return nil
	} else if err != nil {
		return err
	}
	config, err := qdr.GetRouterConfigFromConfigMap(existing)
	if err != nil {
		return err
	}
	//recover any existing port mappings
	if m.portMappings == nil {
		m.portMappings = m.freePorts.GetPortMappings(config)
	}
	if !op.Update(config) {
		return nil
	}
	err = config.WriteToConfigMap(existing)
	if err != nil {
		return err
	}
	_, err = m.client.CoreV1().ConfigMaps(namespace).Update(existing)
	if err != nil {
		return err
	}
	return nil
}

func (m *SiteManager) clearPendingLinks() {
	m.pendingLinks = map[string]*corev1.Secret{}
}

func (m *SiteManager) getPortForListener(name string) (int, error) {
	if port, ok := m.portMappings[name]; ok {
		return port, nil
	}
	return m.freePorts.NextFreePort()
}

func (m *SiteManager) getOwnerReferences() []metav1.OwnerReference {
	return getOwnerReferencesForSite(m.site)
}

func getOwnerReferencesForSite(site *skupperv1alpha1.Site) []metav1.OwnerReference {
	if site == nil {
		return nil
	}
	return []metav1.OwnerReference{
		{
			APIVersion: "skupper.io/v1alpha1",
			Kind:       "Site",
			Name:       site.ObjectMeta.Name,
			UID:        site.ObjectMeta.UID,
		},
	}
}

func (m *SiteManager) checkRouterDeployment(force bool) error {
	name := "skupper-router"
	namespace := m.site.ObjectMeta.Namespace
	//TODO: get from CR
	replicas := int32(1)
	serviceAccountName := "skupper-router"
	isEdge := false
	debugMode := ""
	routerImageName := "quay.io/skupper/skupper-router:main"
	routerImagePullPolicy := corev1.PullAlways
	configSyncImageName := "quay.io/skupper/config-sync:master"
	configSyncImagePullPolicy := corev1.PullAlways
	var annotations map[string]string

	labelSelector := map[string]string{
		types.ComponentAnnotation: types.TransportComponentName,
	}
	labels := map[string]string{
		types.PartOfLabel: types.AppName,
		types.AppLabel:    types.TransportDeploymentName,
		"application":     types.TransportDeploymentName, // needed by automeshing in image
	}
	for key, value := range labelSelector {
		labels[key] = value
	}
	//TODO: add any labels specified in CR

	envVars := getRouterEnvVars(isEdge, debugMode)
	ports := getRouterPorts(isEdge)

	volumes := []corev1.Volume{}
	routerMounts := []corev1.VolumeMount{}
	configSyncMounts := []corev1.VolumeMount{}
	//AppendSecretVolume(&volumes, &routerMounts, types.LocalServerSecret, "/etc/skupper-router-certs/skupper-amqps/")
	AppendConfigVolume(&volumes, &routerMounts, "router-config", types.TransportConfigMapName, "/etc/skupper-router/config/")
	if !isEdge && m.haveServerSecret {
		AppendSecretVolume(&volumes, &routerMounts, types.SiteServerSecret, "/etc/skupper-router-certs/skupper-internal/")
	}
	AppendSharedVolume(&volumes, &routerMounts, &configSyncMounts, "skupper-router-certs", "/etc/skupper-router-certs")

	desired := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: m.getOwnerReferences(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labelSelector,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccountName,
					Volumes:            volumes,
					Containers:         []corev1.Container{
						{
							Image:           routerImageName,
							ImagePullPolicy: routerImagePullPolicy,
							Name:            types.TransportContainerName,
							LivenessProbe:   getLivenessProbe(int(types.TransportLivenessPort), "/healthz"),
							Env:   envVars,
							Ports: ports,
							VolumeMounts: routerMounts,
						},
						{
							Image:           configSyncImageName,
							ImagePullPolicy: configSyncImagePullPolicy,
							Name:            "config-sync",
							VolumeMounts:    configSyncMounts,
						},
					},
				},
			},
		},
	}
	//TODO: set resources for both containers
	//TODO: set affinity

	existing, err := m.client.AppsV1().Deployments(namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = m.client.AppsV1().Deployments(namespace).Create(desired)
		return err
	} else if err != nil {
		return err
	}
        if force || !reflect.DeepEqual(desired.Spec, existing.Spec) {
		existing.Spec = desired.Spec
		_, err = m.client.AppsV1().Deployments(namespace).Update(existing)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *SiteManager) checkRouterServiceAccount() error {
	name := "skupper-router"

	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: m.getOwnerReferences(),
		},
		Rules: types.TransportPolicyRule,
	}
	serviceAccount := &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			OwnerReferences: m.getOwnerReferences(),
		},
	}
	roleBinding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: m.getOwnerReferences(),
		},
		Subjects: []rbacv1.Subject{{
			Kind: "ServiceAccount",
			Name: name,
		}},
		RoleRef: rbacv1.RoleRef{
			Kind: "Role",
			Name: name,
		},
	}
	err := m.ensureRole(role)
	if err != nil {
		return err
	}
	err = m.ensureServiceAccount(serviceAccount)
	if err != nil {
		return err
	}
	err = m.ensureRoleBinding(roleBinding)
	if err != nil {
		return err
	}
	return nil
}

func (m *SiteManager) checkRouterService() error {
	//TODO: populate from CR
	var annotations map[string]string

	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        types.TransportServiceName,
			Annotations: annotations,
			OwnerReferences: m.getOwnerReferences(),
		},
		Spec: corev1.ServiceSpec{
			Selector:       map[string]string{
				types.ComponentAnnotation: types.TransportComponentName,
			},
			Ports:          []corev1.ServicePort{
				{
					Name:       "inter-router",
					Protocol:   "TCP",
					Port:       types.InterRouterListenerPort,
					TargetPort: intstr.FromInt(int(types.InterRouterListenerPort)),
				},
				{
					Name:       "edge",
					Protocol:   "TCP",
					Port:       types.EdgeListenerPort,
					TargetPort: intstr.FromInt(int(types.EdgeListenerPort)),
				},
			},
		},
	}
	if m.ingressMode == IngressModeLoadBalancer {
		svc.Spec.Type = corev1.ServiceTypeLoadBalancer
		//TODO: populate svc.Spec.LoadBalancerIP if indicated in CRD
	}
	return m.ensureService(svc)
}

func (m *SiteManager) checkRouterIngress() error {
	if m.ingressMode == IngressModeNginxIngress {
		return m.ensureNginxIngress()
	}
	if m.ingressMode == IngressModeRoute {
		return m.ensureRoutes()
	}
	return nil
}

func (m *SiteManager) resolveRouterAddresses() ([]skupperv1alpha1.Address, error) {
	//TODO: support for other site ingress options
	if m.ingressMode == IngressModeNginxIngress {
		return m.resolveNginxIngress()
	}
	if m.ingressMode == IngressModeRoute {
		return m.resolveRoute()
	}
	return m.resolveLoadBalancer()
}

func (m *SiteManager) resolveLoadBalancer() ([]skupperv1alpha1.Address, error) {
	namespace := m.site.ObjectMeta.Namespace
	service, err := m.client.CoreV1().Services(namespace).Get(types.TransportServiceName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	host := GetLoadBalancerHostOrIP(service)
	if host == "" {
		return nil, nil
	}
	var addresses []skupperv1alpha1.Address
	addresses = append(addresses, skupperv1alpha1.Address {
		Name: "inter-router",
		Host: host,
		Port: strconv.Itoa(int(types.InterRouterListenerPort)),
	})
	addresses = append(addresses, skupperv1alpha1.Address {
		Name: "edge",
		Host: host,
		Port: strconv.Itoa(int(types.EdgeListenerPort)),
	})
	return addresses, nil
}

func (m *SiteManager) resolveNginxIngress() ([]skupperv1alpha1.Address, error) {
	namespace := m.site.ObjectMeta.Namespace

	routes, err := getIngressRoutesV1(m.dynamicClient, namespace, types.IngressName)
	if err != nil {
		return nil, err
	}
	var addresses []skupperv1alpha1.Address
	for _, route := range routes {
		if m.defaultOptions.IngressHostSuffix == "" && len(strings.Split(route.Host, ".")) == 1 {
			return nil, nil
		}
		addresses = append(addresses, skupperv1alpha1.Address {
			Name: strings.Split(route.Host, ".")[0],
			Host: route.Host,
			Port: "443",
		})
	}
	return addresses, nil
}

func (m *SiteManager) resolveRoute() ([]skupperv1alpha1.Address, error) {
	namespace := m.site.ObjectMeta.Namespace
	interRouterRoute, err := m.routeClient.Routes(namespace).Get("skupper-inter-router", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	edgeRoute, err := m.routeClient.Routes(namespace).Get("skupper-edge", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var addresses []skupperv1alpha1.Address
	addresses = append(addresses, skupperv1alpha1.Address {
		Name: "inter-router",
		Host: interRouterRoute.Spec.Host,
		Port: "443",
	})
	addresses = append(addresses, skupperv1alpha1.Address {
		Name: "edge",
		Host: edgeRoute.Spec.Host,
		Port: "443",
	})
	return addresses, nil
}

func (m *SiteManager) qualifiedIngressHost(host string) string {
	if m.defaultOptions.IngressHostSuffix != "" {
		return strings.Join([]string{host, m.site.ObjectMeta.Namespace, m.defaultOptions.IngressHostSuffix}, ".")
	}
	return host
}

func (m *SiteManager) ensureNginxIngress() error {
	namespace := m.site.ObjectMeta.Namespace
	routes := []IngressRoute {
		{
			Host:        m.qualifiedIngressHost("inter-router"),
			ServiceName: types.TransportServiceName,
			ServicePort: int(types.InterRouterListenerPort),
		},
		{
			Host:        m.qualifiedIngressHost("edge"),
			ServiceName: types.TransportServiceName,
			ServicePort: int(types.EdgeListenerPort),
		},
	}
	annotations := map[string]string{}
	addNginxIngressAnnotations(true, annotations)
	return ensureIngressRoutesV1(m.dynamicClient, namespace, "skupper", routes, annotations, m.getOwnerReferences(), m.defaultOptions.IngressHostSuffix == "")
}

func (m *SiteManager) ensureRoutes() error {
	hostInterRouter := ""
	hostEdge := ""
	host := m.defaultOptions.IngressHostSuffix
	if host != "" {
		namespace := m.site.ObjectMeta.Namespace
		hostInterRouter = types.InterRouterRouteName + "-" + namespace + "." + host
		hostEdge = types.EdgeRouteName + "-" + namespace + "." + host
	}

	err := m.ensureRoute(&routev1.Route{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Route",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: types.InterRouterRouteName,
		},
		Spec: routev1.RouteSpec{
			Path: "",
			Host: hostInterRouter,
			Port: &routev1.RoutePort{
				TargetPort: intstr.FromString(types.InterRouterRole),
			},
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: types.TransportServiceName,
			},
			TLS: &routev1.TLSConfig{
				Termination:                   routev1.TLSTerminationPassthrough,
				InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyNone,
			},
		},
	})
	if err != nil {
		return err
	}
	err = m.ensureRoute(&routev1.Route{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Route",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: types.EdgeRouteName,
		},
		Spec: routev1.RouteSpec{
			Path: "",
			Host: hostEdge,
			Port: &routev1.RoutePort{
				TargetPort: intstr.FromString(types.EdgeRole),
			},
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: types.TransportServiceName,
			},
			TLS: &routev1.TLSConfig{
				Termination:                   routev1.TLSTerminationPassthrough,
				InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyNone,
			},
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func (m *SiteManager) ensureRoute(route *routev1.Route) error {
	namespace := m.site.ObjectMeta.Namespace
	route.ObjectMeta.OwnerReferences = m.getOwnerReferences()

	current, err := m.routeClient.Routes(namespace).Get(route.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err := m.routeClient.Routes(namespace).Create(route)
		return err
	} else if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.Spec, route.Spec) {
		current.Spec = route.Spec
		_, err = m.routeClient.Routes(namespace).Update(current)
		return err
	}
	return nil
}

func (m *SiteManager) ensureRole(desired *rbacv1.Role) error {
	namespace := m.site.ObjectMeta.Namespace
	actual, err := m.client.RbacV1().Roles(namespace).Get(desired.ObjectMeta.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = m.client.RbacV1().Roles(namespace).Create(desired)
		return err
	} else if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual.Rules, desired.Rules) {
		actual.Rules = desired.Rules
		_, err = m.client.RbacV1().Roles(namespace).Update(actual)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *SiteManager) ensureServiceAccount(desired *corev1.ServiceAccount) error {
	namespace := m.site.ObjectMeta.Namespace
	_, err := m.client.CoreV1().ServiceAccounts(namespace).Get(desired.ObjectMeta.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = m.client.CoreV1().ServiceAccounts(namespace).Create(desired)
		return err
	} else if err != nil {
		return err
	}
	return nil
}

func (m *SiteManager) ensureRoleBinding(desired *rbacv1.RoleBinding) error {
	namespace := m.site.ObjectMeta.Namespace
	actual, err := m.client.RbacV1().RoleBindings(namespace).Get(desired.ObjectMeta.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = m.client.RbacV1().RoleBindings(namespace).Create(desired)
		return err
	} else if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual.RoleRef, desired.RoleRef) || !reflect.DeepEqual(actual.Subjects, desired.Subjects) {
		actual.RoleRef = desired.RoleRef
		actual.Subjects = desired.Subjects
		_, err = m.client.RbacV1().RoleBindings(namespace).Update(actual)
		if err != nil {
			return err
		}
	}
	return nil
}

func updateService(actual *corev1.Service, desired *corev1.Service) bool {
	updated := false
	if !reflect.DeepEqual(actual.Spec.Selector, desired.Spec.Selector) {
		actual.Spec.Selector = desired.Spec.Selector
		updated = true
	}
	if !reflect.DeepEqual(actual.Spec.Ports, desired.Spec.Ports) {
		actual.Spec.Ports = desired.Spec.Ports
		updated = true
	}
	if actual.Spec.Type != desired.Spec.Type {
		actual.Spec.Type = desired.Spec.Type
		updated = true
	}
	if desired.Spec.LoadBalancerIP != "" && actual.Spec.LoadBalancerIP != desired.Spec.LoadBalancerIP {
		actual.Spec.LoadBalancerIP = desired.Spec.LoadBalancerIP
		updated = true
	}
	//TODO: also check annotations and labels
	return updated
}

func (m *SiteManager) ensureService(desired *corev1.Service) error {
	namespace := m.site.ObjectMeta.Namespace
	actual, err := m.client.CoreV1().Services(namespace).Get(desired.ObjectMeta.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = m.client.CoreV1().Services(namespace).Create(desired)
		return err
	} else if err != nil {
		return err
	}
	if updateService(actual, desired) {
		_, err = m.client.CoreV1().Services(namespace).Update(actual)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *SiteManager) newCoreRouterConfigCheck() *CoreRouterConfigCheck  {
	//TODO get mode and logging from CR
	mode := qdr.ModeInterior
	result := &CoreRouterConfigCheck{
		metadata:  qdr.RouterMetadata {
			Id:                 m.site.ObjectMeta.Name + "-${HOSTNAME}",
			Mode:               mode,
			HelloMaxAgeSeconds: "3",
			Metadata:           qdr.GetSiteMetadataString(string(m.site.ObjectMeta.UID), version.Version),
		},
		addresses: []qdr.Address{
			{
				Prefix:       "mc",
				Distribution: "multicast",
			},
		},
		listeners: []qdr.Listener{
			{
				Port:        9090,
				Role:        "normal",
				Http:        true,
				HttpRootDir: "disabled",
				Websockets:  false,
				Healthz:     true,
				Metrics:     true,
			},
			{
				Name: "amqp",
				Host: "localhost",
				Port: types.AmqpDefaultPort,
			},
		},
		pendingLinks: AddLinks{
			links: m.pendingLinks,
		},
	}
	if m.haveServerSecret {
		result.listeners = append(result.listeners, getInteriorListener())
		result.listeners = append(result.listeners, getEdgeListener())
	}
	return result
}

type RemoveLink struct {
	name string
}

func (c *RemoveLink) Update(config *qdr.RouterConfig) bool {
	changed := false
	if config.RemoveSslProfile(c.name + "-profile") {
		changed = true
	}
	removed, _ := config.RemoveConnector(c.name)
	if removed {
		changed = true
	}
	return changed
}

type AddLinks struct {
	links map[string]*corev1.Secret
}

func newAddLinks(link *corev1.Secret) *AddLinks {
	return &AddLinks {
		links: map[string]*corev1.Secret{
			link.ObjectMeta.Name: link,
		},
	}
}

func (c *AddLinks) Update(config *qdr.RouterConfig) bool {
	changed := false
	for _, link := range c.links {
		connector := getConnectorForToken(config.IsEdge(), link)
		sslProfile := qdr.SslProfile{
			Name: connector.SslProfile,
		}
		if config.AddConnector(connector) {
			changed = true
		}
		if config.AddSslProfile(sslProfile) {
			changed = true
		}
	}
	return changed
}

type CoreRouterConfigCheck struct {
	metadata     qdr.RouterMetadata
	logging      []types.RouterLogConfig
	addresses    []qdr.Address
	listeners    []qdr.Listener
	pendingLinks AddLinks
	changed      bool
}

func (c *CoreRouterConfigCheck) Update(config *qdr.RouterConfig) bool {
	if config.Metadata != c.metadata {
		config.Metadata = c.metadata
		c.changed = true
	}
	if config.ConfigureLogging(c.logging) {
		c.changed = true
	}
        for _, a := range c.addresses {
		if config.AddAddress(a) {
			c.changed = true
		}
	}
	for _, l := range c.listeners {
		if config.AddListener(l) {
			c.changed = true
		}
		if l.SslProfile != "" && config.AddSslProfile(qdr.SslProfile{Name: l.SslProfile}) {
			c.changed = true
		}
	}
	//changes to links does not necessitate a router restart, so
	//track that separately
	linksChanged := c.pendingLinks.Update(config)

	return c.changed || linksChanged
}

func equivalentAddresses(a []skupperv1alpha1.Address, b []skupperv1alpha1.Address) bool {
	return reflect.DeepEqual(a, b)
}

func getRouterEnvVars(isEdge bool, debugMode string) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{Name: "QDROUTERD_CONF", Value: "/etc/skupper-router/config/" + types.TransportConfigFile},
		{Name: "QDROUTERD_CONF_TYPE", Value: "json"},
	}
	if !isEdge {
		envVars = append(envVars, corev1.EnvVar{Name: "APPLICATION_NAME", Value: types.TransportDeploymentName})
		envVars = append(envVars, corev1.EnvVar{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: "metadata.namespace",
			},
		},
		})
		envVars = append(envVars, corev1.EnvVar{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: "status.podIP",
			},
		},
		})
		envVars = append(envVars, corev1.EnvVar{Name: "QDROUTERD_AUTO_MESH_DISCOVERY", Value: "QUERY"})
	}
	if debugMode != "" {
		envVars = append(envVars, corev1.EnvVar{Name:  "QDROUTERD_DEBUG", Value: debugMode})
	}
	return envVars
}

func getRouterPorts(isEdge bool) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{}
	ports = append(ports, corev1.ContainerPort{
		Name:          "amqps",
		ContainerPort: types.AmqpsDefaultPort,
	})
	if !isEdge {
		ports = append(ports, corev1.ContainerPort{
			Name:          types.InterRouterRole,
			ContainerPort: types.InterRouterListenerPort,
		})
		ports = append(ports, corev1.ContainerPort{
			Name:          types.EdgeRole,
			ContainerPort: types.EdgeListenerPort,
		})
	}
	return ports
}

func getLivenessProbe(port int, path string) *corev1.Probe {
	return &corev1.Probe{
		InitialDelaySeconds: 60,
		Handler: corev1.Handler{
			HTTPGet: &corev1.HTTPGetAction{
				Port: intstr.FromInt(port),
				Path: path,
			},
		},
	}
}

func getCostFromToken(token *corev1.Secret) int32 {
	if token.ObjectMeta.Annotations == nil {
		return 0
	}
	if costString, ok := token.ObjectMeta.Annotations[types.TokenCost]; ok {
		cost, err := strconv.Atoi(costString)
		if err != nil {
			return 0
		}
		return int32(cost)
	}
	return 0
}

func getConnectorForToken(isEdge bool, token *corev1.Secret) qdr.Connector {
	profileName := token.ObjectMeta.Name + "-profile"
	connector := qdr.Connector{
		Name:       token.ObjectMeta.Name,
		Cost:       getCostFromToken(token),
		SslProfile: profileName,
	}
	if isEdge {
		connector.Host = token.ObjectMeta.Annotations["edge-host"]
		connector.Port = token.ObjectMeta.Annotations["edge-port"]
		connector.Role = qdr.RoleEdge
	} else {
		connector.Host = token.ObjectMeta.Annotations["inter-router-host"]
		connector.Port = token.ObjectMeta.Annotations["inter-router-port"]
		connector.Role = qdr.RoleInterRouter
	}
	return connector
}

func getInteriorListener() qdr.Listener {
	return qdr.Listener{
		Name:             "interior-listener",
		Role:             qdr.RoleInterRouter,
		Port:             types.InterRouterListenerPort,
		SslProfile:       types.InterRouterProfile,
		SaslMechanisms:   "EXTERNAL",
		AuthenticatePeer: true,
	}
}

func getEdgeListener() qdr.Listener {
	return qdr.Listener{
		Name:             "edge-listener",
		Role:             qdr.RoleEdge,
		Port:             types.EdgeListenerPort,
		SslProfile:       types.InterRouterProfile,
		SaslMechanisms:   "EXTERNAL",
		AuthenticatePeer: true,
	}
}

func (b *IngressBinding) reconfigure(binding *skupperv1alpha1.RequiredService, mgr *SiteManager) {
	b.binding = binding
}

func (b *IngressBinding) configure(mgr *SiteManager) error {
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            b.binding.ObjectMeta.Name,
			OwnerReferences: mgr.getOwnerReferences(),
		},
		Spec: corev1.ServiceSpec{
			Selector:       map[string]string{
				types.ComponentAnnotation: types.TransportComponentName,
			},
		},
	}
	var err error
	listeners := map[string]qdr.TcpEndpoint{}
	for _, port := range b.binding.Spec.Ports {
		name := b.binding.ObjectMeta.Name + "." + port.Name
		address := b.binding.Spec.Address
		if address == "" {
			address = b.binding.ObjectMeta.Name
		}
		targetPort, ok := b.routerPorts[name]
		if !ok {
			targetPort, err = mgr.getPortForListener(name)
			if err != nil {
				return err
			}
			b.routerPorts[name] = targetPort
		}
		listeners[name] = qdr.TcpEndpoint{
			Name:    name,
			Port:    strconv.Itoa(targetPort),
			Address: address + "." + port.Name,
			SiteId:  string(mgr.site.ObjectMeta.UID),
		}

		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
			Name:       port.Name,
			Protocol:   "TCP",
			Port:       int32(port.Port),
			TargetPort: intstr.FromInt(targetPort),
		})
	}
	for key, _ := range b.listeners {
		if _, ok := listeners[key]; !ok {
			port := b.routerPorts[key]
			mgr.freePorts.Release(port)
		}
	}
	b.listeners = listeners
	return mgr.ensureService(svc)
}

func (b *IngressBinding) Update(config *qdr.RouterConfig) bool {
	changed := false
	for _, l := range b.listeners {
		if config.AddTcpListener(l) {
			changed = true
		}
	}
	prefix := b.binding.ObjectMeta.Name + "."
	var stale []string
	for name, _ := range config.Bridges.TcpListeners {
		if strings.HasPrefix(name, prefix) {
			if _, ok := b.listeners[name]; ! ok {
				stale = append(stale, name)
				changed = true
			}
		}
	}
	for _, name := range stale {
		delete(config.Bridges.TcpListeners, name)
	}
	return changed
}

func (b *EgressBinding) configure(mgr *SiteManager) {
	host := b.binding.Spec.Host
	if host == "" {
		host = b.binding.ObjectMeta.Name
	}
	b.connectors = map[string]qdr.TcpEndpoint{}
	for _, port := range b.binding.Spec.Ports {
		name := b.binding.ObjectMeta.Name + "." + port.Name + ".connector"
		address := b.binding.Spec.Address
		if address == "" {
			address = b.binding.ObjectMeta.Name
		}
		b.connectors[name] = qdr.TcpEndpoint{
			Name:    name,
			Host:    host,
			Port:    strconv.Itoa(port.Port),
			Address: address  + "." + port.Name,
			SiteId:  string(mgr.site.ObjectMeta.UID),
		}
	}
}

func (b *EgressBinding) Update(config *qdr.RouterConfig) bool {
	changed := false
	for _, l := range b.connectors {
		if config.AddTcpConnector(l) {
			changed = true
		}
	}
	prefix := b.binding.ObjectMeta.Name + "."
	var stale []string
	for name, _ := range config.Bridges.TcpConnectors {
		if strings.HasPrefix(name, prefix) {
			if _, ok := b.connectors[name]; ! ok {
				stale = append(stale, name)
				changed = true
			}
		}
	}
	for _, name := range stale {
		delete(config.Bridges.TcpConnectors, name)
	}

	return changed
}

type RemoveTcpListener struct {
	name string
}

func (c *RemoveTcpListener) Update(config *qdr.RouterConfig) bool {
	changed, _ := config.RemoveTcpListener(c.name)
	return changed
}

type RemoveTcpConnector struct {
	name string
}

func (c *RemoveTcpConnector) Update(config *qdr.RouterConfig) bool {
	changed, _ := config.RemoveTcpConnector(c.name)
	return changed
}
