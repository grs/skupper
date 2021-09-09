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
	"bytes"
	"crypto/tls"
	jsonencoding "encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/apimachinery/pkg/util/intstr"
	routev1 "github.com/openshift/api/route/v1"
	routev1client "github.com/openshift/client-go/route/clientset/versioned/typed/route/v1"

	"github.com/skupperproject/skupper/pkg/certs"
	"github.com/skupperproject/skupper/pkg/data"
	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/qdr"
	"github.com/skupperproject/skupper/pkg/service_sync"
	"github.com/skupperproject/skupper/pkg/types"
	"github.com/skupperproject/skupper/pkg/utils"
	"github.com/skupperproject/skupper/pkg/utils/configs"
)


type SiteManager struct {
	cli               kubernetes.Interface
	routecli          *routev1client.RouteV1Client
	namespace         string
	config            *types.SiteConfig
	root              *metav1.ObjectMeta
	local             ExposeStrategy
	siteIngress       ExposeStrategy
	consoleIngress    ExposeStrategy
	serviceAccount    string
	controller        *Controller
	consoleAuth       *ConsoleAuth
	console           *types.HttpServer
	routerConfig      *ConfigMapWatcher
	serviceDefs       *ConfigMapWatcher
	services          *ServiceWatcher
	bindings          *types.ServiceBindings
	serviceSync       *service_sync.ServiceSync
	ready             chan error
	stopCh            <-chan struct{}
	readySignalled    bool
	requiredResources RequiredResources
	connectionFactory *qdr.ConnectionFactory
}

func NewSiteManager(namespace string, cli kubernetes.Interface, routecli *routev1client.RouteV1Client) *SiteManager {
	return &SiteManager{
		cli:               cli,
		routecli:          routecli,
		namespace:         namespace,
		ready:             make(chan error),
		readySignalled:    false,
		requiredResources: RequiredResources{},
	}
}

func (m * SiteManager) Start(stopCh <-chan struct{}) error {
	config, root, err := GetSiteConfig(m.namespace, m.cli)
	if err != nil {
		return err
	}
	err = m.getServiceAccount()
	if err != nil {
		return err
	}
	m.config = config
	m.root = root
	m.local = NewNoneStrategy(m)
	m.siteIngress = m.getSiteExposeStrategy()
	m.consoleIngress = m.getConsoleExposeStrategy()

	m.stopCh = stopCh
	m.controller = NewController("ServiceController", m.cli)
	watcher := m.controller.WatchConfigMap("skupper-site", m.namespace, m.siteConfigChanged)
	watcher.Start(stopCh)
	watcher.Sync(stopCh)
	m.controller.Start(stopCh)
	err = <- m.ready
	return err
}

func (m * SiteManager) siteConfigChanged(name string, configmap *corev1.ConfigMap) error {
	if configmap == nil {
		log.Println("Site config deleted")
		//TODO: site has been deleted
		return nil
	} else {
		m.config = GetSiteConfigFromConfigMap(configmap)
		err :=  m.reconcile()
		if err != nil {
			log.Printf("Error in reconcile: %s", err)
			return err
		}
		return nil
		//return m.reconcile()
	}
}

func (m * SiteManager) serviceDefinitionsChanged(name string, cm *corev1.ConfigMap) error {
	event.Record(ServiceControllerEvent, "Service definitions have changed")
	definitions := map[string]types.ServiceInterface{}
	for k, v := range cm.Data {
		si := types.ServiceInterface{}
		err := jsonencoding.Unmarshal([]byte(v), &si)
		if err != nil {
			event.Recordf(ServiceControllerError, "Could not parse service definition for %s: %s", k, err)
		}
		definitions[k] = si
	}
	if m.bindings != nil {
		m.bindings.Update(definitions)
	}
	m.serviceSync.LocalDefinitionsUpdated(definitions)
	err := m.updateRouterConfig()
	if err != nil {
		return err
	}
	m.updateActualServices()
	m.updateHeadlessProxies()

	return nil
}

func (m * SiteManager) routerConfigChanged(name string, configmap *corev1.ConfigMap) error {
	err := m.updateRouterConfig()
	if err != nil {
		return err
	}
	return nil
}

func (m * SiteManager) updateRouterConfig() error {
	//TODO
	return nil
}

func (m * SiteManager) updateActualServices() error {
	//TODO
	return nil
}

func (m * SiteManager) updateHeadlessProxies() error {
	//TODO
	return nil
}

func (m * SiteManager) GetSiteId() string {
	return m.config.Id
}

func (m * SiteManager) GetSiteConfig() *types.SiteConfig {
	return m.config
}

func (m * SiteManager) GetVersion() string {
	return types.Version
}

func (m * SiteManager) GetConsoleServerAddress() string {
	if m.config.IsConsoleAuthOpenshift() {
		return "localhost:8888"
	} else {
		return ":8080"
	}
}

func (m * SiteManager) GetConsoleCredentials() (*tls.Config, error) {
	return GetTlsConfigFromSecret("skupper-console-certs", m.namespace, m.cli)
}

func (m * SiteManager) GetAmqpClientCredentials() (*tls.Config, error) {
	return GetTlsConfigFromSecret("skupper-local-client", m.namespace, m.cli)
}

func (m * SiteManager) reconcile() error {
	router := types.ConfiguredRouter(m.config)
	components := []types.Component{
		router,
		types.ConfiguredController(m.config),
	}
	err := m.ensureServicesFor(components)
	if err != nil {
		return err
	}
	log.Println("Services are as desired")
	err = m.ensureCertsFor(components)
	if err != nil {
		return err
	}
	log.Println("Secrets are as desired")

	err = m.ensureConfigFor(components)
	if err != nil {
		return err
	}
	log.Println("ConfigMaps are as desired")

	err = m.checkServiceAccount()
	if err != nil {
		return err
	}
	log.Println("ServiceAccount is as desired")

	if m.config.IsConsoleAuthInternal() && (m.config.EnableConsole || m.config.EnableRouterConsole) {
		err = m.ensureSecret("skupper-console-users", map[string][]byte{m.config.GetConsoleUser(): []byte(m.config.GetConsolePassword())})
		if err != nil {
			return err
		}
		if m.consoleAuth == nil {
			m.consoleAuth = newConsoleAuth(m.cli, m.namespace, nil)
		}
		log.Println("Console auth enabled")
	} else if m.consoleAuth != nil {
		m.consoleAuth.controller.Stop()
		log.Println("Auth controller stopped")
		m.consoleAuth = nil
	}

	if m.config.EnableConsole && !m.console.IsRunning() {
		//TODO: start oauth proxying for skupper console
		consoleCredentials, err := m.GetConsoleCredentials()
		if err != nil {
			log.Printf("Error getting tls config for console: %s", err.Error())
		}
		go func() {
			err := m.console.ListenAndServe(m.GetConsoleServerAddress(), consoleCredentials)
			log.Println(err.Error())
		}()
		log.Printf("Console server listening on %s", m.GetConsoleServerAddress())
	} else if !m.config.EnableConsole && m.console.IsRunning() {
		//TODO: stop oauth proxying for skupper console
		m.console.Close()
		log.Println("Console server stopped")
	}

	err = m.ensureDeploymentFor(&router)
	if err != nil {
		return err
	}
	log.Println("Deployment is as desired")

	if !m.readySignalled {
		err = m.siteReady()
		if err != nil {
			return err
		}

		m.readySignalled = true
		m.ready <- nil
	}
	return nil
}

func (m * SiteManager) siteReady() error {
	//start watching skupper-services & skupper-internal configmaps, all services, headless proxy statefulsets
	m.serviceDefs = m.controller.WatchConfigMap("skupper-services", m.namespace, m.serviceDefinitionsChanged)
	m.serviceDefs.Start(m.stopCh)
	m.routerConfig = m.controller.WatchConfigMap("skupper-internal", m.namespace, m.routerConfigChanged)
	m.routerConfig.Start(m.stopCh)
	m.routerConfig.Sync(m.stopCh)
	configmap, err := m.routerConfig.Get()
	if err != nil {
		return err
	}
	config, err := qdr.GetRouterConfigFromConfigMap(configmap)
	if err != nil {
		return err
	}
	m.services = m.controller.WatchServices(m.namespace, m.kubeServiceChanged)
	m.services.Start(m.stopCh)
	m.bindings = types.NewServiceBindings(NewKubernetesBindings(m.config.Id, m.services, m), &config.Bridges)
	m.serviceSync = service_sync.NewServiceSync(m.config.Id, m.updateServiceDefinitions, m.connectionFactory)
	return nil
}

func (m *SiteManager) updateServiceDefinitions(changed []types.ServiceInterface, deleted []string, origin string) error {
	//TODO: need to coordinate with definition monitor, which will also update the skupper-services configmap
	return updateServiceDefinitions(changed, deleted, origin, m.namespace, m.cli)
}

func (m *SiteManager) kubeServiceChanged(key string, svc *corev1.Service) error {
	//TODO
	return nil
}

func (m *SiteManager) ensureCAs() (map[string]*corev1.Secret, error) {
	names := []string{string(types.LocalCA), string(types.SiteCA)}
	results := map[string]*corev1.Secret{}
	for _, name := range names {
		ca, err := m.ensureCA(name)
		if err != nil {
			return nil, err
		}
		results[name] = ca
	}
	return results, nil
}

func (m * SiteManager) ensureCertsFor(components []types.Component) error {
	cas, err := m.ensureCAs()
	if err != nil {
		return err
	}
	for _, component := range components {
		for _, svc := range component.Services {
			addresses, err := m.getIngress(svc.Expose).Resolve(&svc)
			if err != nil {
				return err
			}
			if addresses != nil {
				for _, credential := range svc.Credentials {
					if component.Proxy != nil && component.Proxy.CredentialName == credential.Name {
						//if using oauth proxy, the certificate is generated by openshift
						continue
					}
					err = m.ensureCert(credential.Name, svc.Name, hosts(addresses), cas[string(credential.CA)], credential.Client)
					if err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (m * SiteManager) ensureConfigFor(components []types.Component) error {
	for _, component := range components {
		for _, config := range component.Configs {
			_, err := m.cli.CoreV1().ConfigMaps(m.namespace).Get(config.Name, metav1.GetOptions{})
			if errors.IsNotFound(err) {
				cm := &corev1.ConfigMap{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1",
						Kind:       "ConfigMap",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: config.Name,
					},
					Data: config.Data,
				}
				m.setOwnerReferences(&cm.ObjectMeta)
				_, err := m.cli.CoreV1().ConfigMaps(m.namespace).Create(cm)
				if err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
	}
	return nil
}

func (m * SiteManager) createSecret(secret *corev1.Secret) error {
	m.setOwnerReferences(&secret.ObjectMeta)
	_, err := m.cli.CoreV1().Secrets(m.namespace).Create(secret)
	return err
}

func (m * SiteManager) ensureSecret(name string, data map[string][]byte) error {
	m.requiredResources.addSecret(name)
	_, err := m.cli.CoreV1().Secrets(m.namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		cm := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Data: data,
		}
		err = m.createSecret(cm)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}

func (m * SiteManager) ensureCA(name string) (*corev1.Secret, error) {
	existing, err := m.cli.CoreV1().Secrets(m.namespace).Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		newca := certs.GenerateCASecret(name, name)
		m.setOwnerReferences(&newca.ObjectMeta)
		existing, err = m.cli.CoreV1().Secrets(m.namespace).Create(&newca)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	err = checkKeys(existing.Data, "tls.crt", "tls.key")
	if err != nil {
		newca := certs.GenerateCASecret(name, name)
		existing.Data = newca.Data
		existing, err = m.cli.CoreV1().Secrets(m.namespace).Update(existing)
		if err != nil {
			return nil, err
		}
	}
	return existing, nil
}

func checkKeys(data map[string][]byte, keys ...string) error {
	if data == nil {
		return fmt.Errorf("Secret does not have keys %s", strings.Join(keys, ","))
	}
	for _, key := range keys {
		if _, ok := data[key]; !ok {
			return fmt.Errorf("Secret does not have key %s", keys)
		}
	}
	return nil
}

func verifyCert(cert *corev1.Secret, subject string, hosts []string, ca *corev1.Secret, connectJson bool) error {
	err := checkKeys(cert.Data, "tls.crt", "tls.key", "ca.crt")
	if err != nil {
		return err
	}
	if connectJson {
		err = checkKeys(cert.Data, "connect.json")
		if err != nil {
			return err
		}
	}
	err = certs.VerifySecret(cert, subject, hosts)
	if err != nil {
		return err
	}
	if !bytes.Equal(ca.Data["tls.crt"], cert.Data["ca.crt"]) {
		return fmt.Errorf("CA does not match")
	}
	return nil
}

func (m * SiteManager) ensureCert(name string, subject string, hosts []string, ca *corev1.Secret, connectJson bool) error {
	secret, err := m.cli.CoreV1().Secrets(m.namespace).Get(name, metav1.GetOptions{})
	if err == nil {
		err = verifyCert(secret, subject, hosts, ca, connectJson)
		if err != nil {
			//regenerate
			updated := certs.GenerateSecret(name, subject, strings.Join(hosts, ","), ca)
			secret.Data = updated.Data
			if connectJson {
				secret.Data["connect.json"] = []byte(configs.ConnectJson())
			}
			m.setOwnerReferences(&secret.ObjectMeta)
			_, err = m.cli.CoreV1().Secrets(m.namespace).Update(secret)
			return err
		}
		return nil
	} else if !errors.IsNotFound(err) {
		return err
	}

	s := certs.GenerateSecret(name, subject, strings.Join(hosts, ","), ca)
	secret = &s
	if connectJson {
		secret.Data["connect.json"] = []byte(configs.ConnectJson())
	}
	m.setOwnerReferences(&secret.ObjectMeta)
	_, err = m.cli.CoreV1().Secrets(m.namespace).Create(secret)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (m * SiteManager) setOwnerReferences(object *metav1.ObjectMeta) {
	if m.root != nil {
		object.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: "core/v1",
				Kind:       "ConfigMap",
				Name:       m.root.Name,
				UID:        m.root.UID,
			},
		}
	}
	if object.Labels == nil {
		object.Labels = map[string]string{}
	}
	object.Labels["internal.skupper.io/controlled"] = "true"
}

func checkMap(actual map[string]string, desired map[string]string) bool {
	for k, v := range desired {
		if av, ok := actual[k]; !ok || av != v {
			return false
		}
	}
	return true
}

func checkService(actual *corev1.Service, desired *corev1.Service) bool {
	if actual.Spec.Type != desired.Spec.Type {
		log.Printf("Type does not match for service %s: want %s got %s", actual.ObjectMeta.Name, desired.Spec.Type, actual.Spec.Type)
		return false
	}
	if !checkMap(actual.ObjectMeta.Labels, desired.ObjectMeta.Labels) {
		log.Printf("Labels do not match for service %s: want %v got %v", actual.ObjectMeta.Name, desired.ObjectMeta.Labels, actual.ObjectMeta.Labels)
		return false
	}
	if !checkMap(actual.ObjectMeta.Annotations, desired.ObjectMeta.Annotations) {
		log.Printf("Annotations do not match for service %s: want %v got %v", actual.ObjectMeta.Name, desired.ObjectMeta.Annotations, actual.ObjectMeta.Annotations)
		return false
	}
	if !checkMap(actual.Spec.Selector, desired.Spec.Selector) {
		log.Printf("Selector does not match for service %s: want %v got %v", actual.ObjectMeta.Name, desired.Spec.Selector, actual.Spec.Selector)
		return false
	}
	if !checkPorts(desired.Spec.Ports, actual.Spec.Ports) {
		log.Printf("Ports do not match for service %s: want %v got %v", actual.ObjectMeta.Name, desired.Spec.Ports, actual.Spec.Ports)
		return false
	}
	log.Printf("Service %s is as desired", actual.ObjectMeta.Name)
	return true
}

func (m * SiteManager) ensureService(service *corev1.Service) error {
	m.requiredResources.addService(service.ObjectMeta.Name)
	actual, err := m.cli.CoreV1().Services(m.namespace).Get(service.ObjectMeta.Name, metav1.GetOptions{})
	if err == nil {
		if !checkService(actual, service) {
			err = m.cli.CoreV1().Services(m.namespace).Delete(service.ObjectMeta.Name, &metav1.DeleteOptions{})
			if err != nil {
				return err
			}
		} else {
			return nil
		}
	} else if !errors.IsNotFound(err) {
		return err
	}
	m.setOwnerReferences(&service.ObjectMeta)
	_, err = m.cli.CoreV1().Services(m.namespace).Create(service)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (m *SiteManager) deleteService(name string) error {
	return m.cli.CoreV1().Services(m.namespace).Delete(name, &metav1.DeleteOptions{})
}

func (m * SiteManager) getService(name string) (*corev1.Service, error) {
	return m.cli.CoreV1().Services(m.namespace).Get(name, metav1.GetOptions{})
}

func (m *SiteManager) ensureServicesFor(components []types.Component) error {
	for _, component := range components {
		for _, svc := range component.Services {
			err := m.getIngress(svc.Expose).Expose(&component, &svc)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *SiteManager) getIngress(scope types.ExposeScope) ExposeStrategy {
	switch scope {
	case types.ExposeScopeSite:
		return m.siteIngress
	case types.ExposeScopeConsole:
		return m.consoleIngress
	case types.ExposeScopeNone:
		return m.local
	}
	return m.local
}

func getService(component string, def *types.Service) *corev1.Service {
	selector := map[string]string{
		"skupper.io/component": component,
	}
	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: def.Name,
			Annotations: map[string]string{
				"internal.skupper.io/controlled": "true",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
		},
	}
	for _, port := range def.Ports {
		addPort(service, port.Name, port.Port, port.Port)
	}
	return service
}

func addPort(service *corev1.Service, name string, port int32, targetPort int32)  {
	service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
		Name:       name,
		Port:       port,
		TargetPort: intstr.FromInt(int(targetPort)),
	})
}

func checkPorts(desired []corev1.ServicePort, actual []corev1.ServicePort) bool {
	if len(desired) != len(actual) {
		return false
	}
	if len(desired) == 1 {
		return checkPort(desired[0], actual[0])
	}
	byname := map[string]corev1.ServicePort{}
	for _, p := range desired {
		byname[p.Name] = p
	}
	for _, p := range actual {
		if !checkPort(byname[p.Name], p) {
			return false
		}
	}
	return true
}

func checkPort(desired corev1.ServicePort, actual corev1.ServicePort) bool {
	return desired.Name == actual.Name && desired.Port == actual.Port && desired.TargetPort == actual.TargetPort
}

func checkRoute(actual *routev1.Route, desired *routev1.Route) bool {
	if actual.Spec.Port != desired.Spec.Port {
		return false
	}
	if actual.Spec.To != desired.Spec.To {
		return false
	}
	if actual.Spec.TLS != desired.Spec.TLS {
		return false
	}
	return true
}

func (m * SiteManager) getRoute(name string) (*routev1.Route, error) {
	return m.routecli.Routes(m.namespace).Get(name, metav1.GetOptions{})
}

func (m * SiteManager) ensureRoute(name string, serviceName string, portName string, termination routev1.TLSTerminationType) (*routev1.Route, error) {
	m.requiredResources.addRoute(name)
	desired := getRoute(name, serviceName, portName, termination)
	m.setOwnerReferences(&desired.ObjectMeta)
	actual, err := m.routecli.Routes(m.namespace).Get(name, metav1.GetOptions{})
	if err == nil {
		if checkRoute(actual, desired) {
			return actual, nil
		} else {
			actual.Spec = desired.Spec
			updated, err := m.routecli.Routes(m.namespace).Update(actual)
			if err != nil {
				return nil, err
			}
			return updated, nil
		}
	} else if !errors.IsNotFound(err) {
		return nil, err
	}
	created, err := m.routecli.Routes(m.namespace).Create(desired)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func getRoute(name string, serviceName string, portName string, termination routev1.TLSTerminationType) *routev1.Route {
	policy := routev1.InsecureEdgeTerminationPolicyRedirect
	if termination == routev1.TLSTerminationPassthrough {
		policy = routev1.InsecureEdgeTerminationPolicyNone
	}
	return &routev1.Route{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Route",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: routev1.RouteSpec{
			Path: "",
			Port: &routev1.RoutePort{
				TargetPort: intstr.FromString(portName),
			},
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: serviceName,
			},
			TLS: &routev1.TLSConfig{
				Termination:                   termination,
				InsecureEdgeTerminationPolicy: policy,
			},
		},
	}
}

func getContainerPorts(component *types.Component) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{}
	for _, port := range component.GetAllPorts() {
		ports = append(ports, corev1.ContainerPort{Name: port.Name, ContainerPort: port.Port})
	}
	return ports
}

func getProbe(component *types.Component) *corev1.Probe {
	if component.ProbePath == "" {
		return nil
	}
	for _, p := range component.Ports {
		if p.Type == types.ListenerTypeProbe {
			return &corev1.Probe{
				InitialDelaySeconds: 60,
				Handler: corev1.Handler{
					HTTPGet: &corev1.HTTPGetAction{
						Port: intstr.FromInt(int(p.Port)),
						Path: component.ProbePath,
					},
				},
			}
		}
	}
	return nil
}

func getEnvVars(values map[string]string) []corev1.EnvVar {
	envVars := []corev1.EnvVar{}
	for name, value := range values {
		envVars = append(envVars, corev1.EnvVar{Name: name, Value: value})
	}
	return envVars
}

func  (m * SiteManager) getDeploymentFor(component *types.Component) *appsv1.Deployment {
	volumes := []corev1.Volume{}
	mounts := []corev1.VolumeMount{}
	creds := component.GetServerCredentialNames()
	for _, cred := range creds {
		AppendSecretVolume(&volumes, &mounts, cred, component.MountPath + cred)
	}
	if component.IsRouter() && m.config.IsConsoleAuthInternal() && m.config.EnableRouterConsole {
		AppendSecretVolume(&volumes, &mounts, "skupper-console-users", component.MountPath + "skupper-console-users")
	}
	for _, config := range component.Configs {
		mountPath := config.MountPath
		if mountPath == "" {
			mountPath = component.MountPath + config.Name
		}
		AppendConfigVolume(&volumes, &mounts, config.Name, config.Name, mountPath)
	}

	container := corev1.Container{
		Image:           component.Image(),
		ImagePullPolicy: corev1.PullPolicy(component.PullPolicy()),
		Name:            component.Name,
		LivenessProbe:   getProbe(component),
		Env:             getEnvVars(component.Environment),
		Ports:           getContainerPorts(component),
	}
	if component.IsRouter() && !m.config.IsEdge() {
		//need to add a couple of env vars using downward api
		//that are used in automeshing
		updateRouterEnv(&container)
	}
	configureResources(&container, &m.config.Router.Tuning)
	container.VolumeMounts = mounts
	replicas := int32(1)
	dep := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.DeploymentName(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"skupper.io/component": component.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      component.GetLabels(m.config),
					Annotations: component.GetAnnotations(m.config),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: m.serviceAccount,
					Containers: []corev1.Container{
						container,
					},
					Volumes: volumes,
				},
			},
		},
	}
	configureAffinity(&m.config.Router.Tuning, &dep.Spec.Template.Spec)
	m.setOwnerReferences(&dep.ObjectMeta)
	return dep
}

func checkDeployment(actual *appsv1.Deployment, desired *appsv1.Deployment) bool {
	equivalent := true
	if reflect.DeepEqual(actual.Labels, desired.Labels) {
		actual.Labels = desired.Labels
		equivalent = false
	}
	if reflect.DeepEqual(actual.Annotations, desired.Annotations) {
		actual.Annotations = desired.Annotations
		equivalent = false
	}
	if reflect.DeepEqual(actual.OwnerReferences, desired.OwnerReferences) {
		actual.OwnerReferences = desired.OwnerReferences
		equivalent = false
	}
	if reflect.DeepEqual(actual.Spec, desired.Spec) {
		actual.Spec = desired.Spec
		equivalent = false
	}
	return equivalent
}

func  (m * SiteManager) ensureDeploymentFor(component *types.Component) error {
	actual, err := m.cli.AppsV1().Deployments(m.namespace).Get(component.DeploymentName(), metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	desired := m.getDeploymentFor(component)
	if component.Proxy != nil {
		addOauthProxySidecar(desired, m.serviceAccount, component.Proxy.PrivatePort, component.Proxy.PublicPort, component.Proxy.CredentialName, component.MountPath)
	}
	if errors.IsNotFound(err) {
		fmt.Println("Creating deployment for ", component.Name)
		_, err = m.cli.AppsV1().Deployments(m.namespace).Create(desired)
		if err != nil {
			fmt.Println("Failed to create deployment for ", component.Name, err, desired)
		}
		return err
	} else if !checkDeployment(actual, desired) {
		fmt.Println("Updating deployment for ", component.Name)
		_, err = m.cli.AppsV1().Deployments(m.namespace).Update(desired)
		return err
	} else {
		fmt.Println("Deployment up to date for ", component.Name)
		return nil
	}
}

func updateRouterEnv(container *corev1.Container) {
	container.Env = append(container.Env, corev1.EnvVar{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{
		FieldRef: &corev1.ObjectFieldSelector{
			FieldPath: "metadata.namespace",
		},
	},
	})
	container.Env = append(container.Env, corev1.EnvVar{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{
		FieldRef: &corev1.ObjectFieldSelector{
			FieldPath: "status.podIP",
		},
	},
	})
}

func  (m * SiteManager) getServiceAccount() error {
	pod, err := m.cli.CoreV1().Pods(m.namespace).Get(os.Getenv("HOSTNAME"), metav1.GetOptions{})
	if errors.IsNotFound(err) {
		m.serviceAccount = os.Getenv("SERVICEACCOUNT")
	} else if err != nil {
		return err
	} else {
		m.serviceAccount = pod.Spec.ServiceAccountName
	}
	if m.serviceAccount == "" {
		return fmt.Errorf("Could not determine service account")
	}
	return nil
}

func  (m * SiteManager) checkServiceAccountAnnotations(name string, annotations map[string]string) error {
	sa, err := m.cli.CoreV1().ServiceAccounts(m.namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if sa.ObjectMeta.Annotations == nil {
		sa.ObjectMeta.Annotations = map[string]string{}
	}
	update := false
	for key, desiredValue := range annotations {
		if actualValue, ok := sa.ObjectMeta.Annotations[key]; !ok || actualValue != desiredValue {
			sa.ObjectMeta.Annotations[key] = desiredValue
			update = true
		}
	}
	if update {
		_, err = m.cli.CoreV1().ServiceAccounts(m.namespace).Update(sa)
		return err
	}
	return nil
}

func  (m * SiteManager) checkServiceAccount() error {
	if m.config.IsConsoleAuthOpenshift() && (m.config.EnableConsole || m.config.EnableRouterConsole) {
		annotations := map[string]string{}
		//TODO: get route names from configured components
		if m.config.EnableConsole {
			addRedirectAnnotation(annotations, "skupper-console")
		}
		if m.config.EnableRouterConsole {
			addRedirectAnnotation(annotations, "skupper-router-console")
		}
		err := m.checkServiceAccountAnnotations(m.serviceAccount, annotations)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m * SiteManager) Authenticate(user string, password string) bool {
	return m.consoleAuth.Authenticate(user, password)
}

func (m * SiteManager) RequireAuthentication() bool {
	return m.consoleAuth != nil
}

func (m * SiteManager) SetConsoleControl(console *types.HttpServer) {
	m.console = console
}

type Stopable interface {
	Stop()
}

type StopablePodWatcher struct {
	watcher *PodWatcher
	stopper chan struct{}
}

func (m *SiteManager) servicePodsChanged(string, *corev1.Pod) error {
	return m.updateRouterConfig()
}

func (m *SiteManager) WatchServicePods(selector string) *StopablePodWatcher {
	s := &StopablePodWatcher {
		watcher: m.controller.WatchPods(selector, m.namespace, m.servicePodsChanged),
		stopper: make(chan struct{}),
	}
	s.watcher.Start(s.stopper)
	return s
}

func (s *StopablePodWatcher) Stop() {
	close(s.stopper)
}

func (s *StopablePodWatcher) List() []*corev1.Pod {
	return s.watcher.List()
}

func (m *SiteManager) LookupSiteInfo(site *data.Site) error {
	site.SiteName =  m.config.Name
	site.SiteId = m.config.Id
	site.Version = types.Version
	site.Namespace = m.namespace
	site.Url = ""//TODO
	site.Edge = m.config.IsEdge()
	return nil
}

func (m *SiteManager) LookupServiceDefinition(address string, svc *data.ServiceDetail) error {
	definitions, err := m.serviceDefs.Get()
	if err != nil {
		return err
	}

	if definitions == nil {
		return fmt.Errorf("No services definitions found")
	}

	definition, ok := definitions.Data[address]
	if !ok {
		return fmt.Errorf("No such service %q", address)
	}

	service := types.ServiceInterface{}
	err = jsonencoding.Unmarshal([]byte(definition), &service)
	if err != nil {
		return fmt.Errorf("Failed to read json for service definition %s: %s", address, err)
	}

	svc.Definition = service
	return nil
}

func (m *SiteManager) LookupIngressPorts(detail *data.ServiceDetail) error {
	service, err := GetService(detail.Definition.Address, m.namespace, m.cli)
	if err != nil {
		return err
	}

	detail.IngressBinding.ServicePorts = map[int]int{}
	for _, ports := range service.Spec.Ports {
		if utils.IntSliceContains(detail.Definition.Ports, int(ports.Port)) {
			detail.IngressBinding.ServicePorts[int(ports.Port)] = ports.TargetPort.IntValue()
		} else {
			detail.AddObservation(fmt.Sprintf("Kubernetes service defines port %s %d:%d which is not in skupper service definition", ports.Name, ports.Port, ports.TargetPort.IntValue()))
		}
	}

	detail.IngressBinding.ServiceSelector = service.Spec.Selector
	return nil
}
