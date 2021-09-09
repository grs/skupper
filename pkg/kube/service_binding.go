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
	"reflect"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/skupperproject/skupper/pkg/event"
	"github.com/skupperproject/skupper/pkg/qdr"
	"github.com/skupperproject/skupper/pkg/types"
	"github.com/skupperproject/skupper/pkg/utils"
)

const (
	BridgeTargetEvent      string = "BridgeTargetEvent"
	ServiceCreateEvent     string = "ServiceCreateEvent"

        // label/annotation keys
	OriginalSelectorKey    string = "internal.skupper.io/originalSelector"
	OriginalTargetPortKey  string = "internal.skupper.io/originalTargetPort"
	LastAssignedPortsKey   string = "internal.skupper.io/originalAssignedPort"
	ComponentAnnotationKey string = "skupper.io/component"

	RouterComponent        string = "router"
)

type Services interface {
	CreateService(svc *corev1.Service) (*corev1.Service, error)
	UpdateService(svc *corev1.Service) (*corev1.Service, error)
	GetService(name string) (*corev1.Service, error)
}

type ServicePodWatcher interface {
	WatchServicePods(selector string) *StopablePodWatcher
}

type KubernetesBindings struct {
	services               Services
	podWatcher             ServicePodWatcher
	siteId                 string
	portAllocations        map[string][]int
        ports                  *qdr.FreePorts
}

func NewKubernetesBindings(siteId string, services Services, podWatcher ServicePodWatcher) *KubernetesBindings {
	kb := &KubernetesBindings{
		services:   services,
		podWatcher: podWatcher,
		siteId:     siteId,
		ports:      qdr.NewFreePorts(),
	}
	return kb
}

func (kb *KubernetesBindings) NewIngressBinding(definition *types.ServiceInterface) types.IngressBinding {
	if definition.Headless == nil {
		return kb.newSimpleIngress(definition)
	} else {
		//TODO
	}
	return nil
}

type SimpleIngress struct {
	types.BridgeBinding
	services Services
	ports    map[int]int
	labels   map[string]string
}

func equivalentSelectors(a map[string]string, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if v2, ok := b[k]; !ok || v != v2 {
			return false
		}
	}
	for k, v := range b {
		if v2, ok := a[k]; !ok || v != v2 {
			return false
		}
	}
	return true
}

func hasRouterSelector(service corev1.Service) bool {
	value, ok := service.Spec.Selector[ComponentAnnotationKey]
	return ok && value == RouterComponent
}

func getApplicationSelector(service *corev1.Service) string {
	if hasRouterSelector(*service) {
		selector := map[string]string{}
		for key, value := range service.Spec.Selector {
			if key != ComponentAnnotationKey && !(key == "application" && value == "skupper-router") {
				selector[key] = value
			}
		}
		return utils.StringifySelector(selector)
	} else {
		return utils.StringifySelector(service.Spec.Selector)
	}
}


func setAnnotation(obj *metav1.ObjectMeta, key string, value string) {
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations[key] = value
}

func arePortsCorrect(desired map[int]int, svc *corev1.Service) bool {
	if len(desired) != len(svc.Spec.Ports) {
		return false
	}
	for _, port := range svc.Spec.Ports {
		if targetPort, ok := desired[int(port.Port)]; !ok || targetPort != port.TargetPort.IntValue() {
			return false
		}
	}
	return true
}

func recordOriginalPorts(currentPorts map[int]int, svc *corev1.Service) {
	//update record of original target ports
	if !reflect.DeepEqual(currentPorts, GetOriginalAssignedPorts(svc)) {
		setAnnotation(&svc.ObjectMeta, OriginalTargetPortKey, PortMapToLabelStr(currentPorts))
	}
	setAnnotation(&svc.ObjectMeta, LastAssignedPortsKey, PortMapToLabelStr(GetServicePortMap(svc)))
}

func updatePorts(desired map[int]int, svc *corev1.Service) (bool, map[int]int) {
	if arePortsCorrect(desired, svc) {
		return false, nil
	}
	var ports []corev1.ServicePort
	for port, targetPort := range desired {
		ports = append(ports, corev1.ServicePort{
			Name:       fmt.Sprintf("port%d", port),
			Port:       int32(port),
			TargetPort: intstr.IntOrString{IntVal: int32(targetPort)},
		})
	}
	currentPorts := GetServicePortMap(svc)
	svc.Spec.Ports = ports
	return true, currentPorts
}

func updateSelector(svc *corev1.Service) (bool, string) {
	desired := GetLabelsForRouter()
	if equivalentSelectors(svc.Spec.Selector, desired) {
		return false, ""
	}
	original := getApplicationSelector(svc)
	svc.Spec.Selector = desired
	return true, original
}

func updateLabels(desired map[string]string, svc *corev1.Service) bool {
	if len(desired) == 0 {
		return false
	}
	if svc.Labels == nil {
		svc.Labels = desired
		return true
	} else {
		updated := false
		for k, v := range desired {
			if actual, ok := svc.Labels[k]; !ok || actual != v {
				svc.Labels[k] = v
				updated = true
			}
		}
		return updated
	}
}

func (s* SimpleIngress) updateService(svc *corev1.Service) bool {
	update := false
	if changed, original := updatePorts(s.ports, svc); changed {
		update = true
		recordOriginalPorts(original, svc)
	}
	if changed, original := updateSelector(svc); changed {
		update = true
		setAnnotation(&svc.ObjectMeta, OriginalSelectorKey, original)
	}
	if updateLabels(s.labels, svc) {
		update = true
	}
	return update
}

func (s *SimpleIngress) makeService() *corev1.Service {
	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: s.GetAddress(),
			Labels: s.labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: GetLabelsForRouter(),
		},
	}
	for port, targetPort := range s.ports {
		service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
			Name:       fmt.Sprintf("%s%d", s.GetAddress(), port),
			Port:       int32(port),
			TargetPort: intstr.FromInt(targetPort),
		})
	}
	return service
}

func (s *SimpleIngress) Realise() error {
	event.Recordf(ServiceControllerEvent, "Checking service for: %s", s.GetAddress())
	svc, err := s.services.GetService(s.GetAddress())
	if err != nil {
		return fmt.Errorf("Error checking service %s", err)
	} else if svc == nil {
		event.Recordf(ServiceCreateEvent, "Creating new service for %s", s.GetAddress())
		_, err := s.services.CreateService(s.makeService())
		if err != nil {
			event.Recordf(ServiceControllerError, "Error while creating service %s: %s", s.GetAddress(), err)
			return err
		}
		return nil
	}
	if s.updateService(svc) {
		_, err := s.services.UpdateService(svc)
		return err
	}
	return nil
}

func (s* SimpleIngress) Configure(bridges *qdr.BridgeConfig) {
	s.AddIngressBridge(bridges)
}

func (s* SimpleIngress) Compare(definition *types.ServiceInterface) bool {
	s.labels = definition.Labels
	return definition.Headless == nil && s.Match(definition) && s.PortsMatchKeys(definition.Ports)
}

func (s* SimpleIngress) Deleted() {}

func (kb *KubernetesBindings) newSimpleIngress(definition *types.ServiceInterface) types.IngressBinding {
	binding := &SimpleIngress{
		services: kb.services,
		ports:    map[int]int{},
		labels:   definition.Labels,
	}
	bridgePorts := kb.portAllocations[definition.Address]
	for i := 0; i < len(definition.Ports); i++ {
		if i >= len(bridgePorts) {
			port, err := kb.ports.NextFreePort()
			if err != nil {
				//TODO
			} else {
				bridgePorts = append(bridgePorts, port)
			}
		}
	}
	for i := len(definition.Ports); i < len(bridgePorts); i++ {
		kb.ports.Release(bridgePorts[i])
	}
	for i, port := range definition.Ports {
		binding.ports[port] = bridgePorts[i]
	}
	binding.InitFromDefinition("", kb.siteId, definition, binding.ports)
	return binding
}

func (f *KubernetesBindings) NewEgressBinding(definition *types.ServiceInterface, target *types.ServiceInterfaceTarget) types.EgressBinding {
	if definition.Headless == nil {
		if target.Selector != "" {
			return f.newSelectorEgress(definition, target)
		} else if target.Service != "" {
			return f.newServiceEgress(definition, target)
		}
	} else {
		//TODO
	}
	return nil
}

func (kb *KubernetesBindings) update(ingresses map[string]SimpleIngress, qualifiedAddress string, routerPort string, protocol string, aggregation string, eventChannel bool) {
	address, servicePort := types.ParsePortQualifiedAddress(qualifiedAddress)
	binding, ok := ingresses[address]
	if !ok {
		binding = SimpleIngress{
			services: kb.services,
			ports:    map[int]int{},
		}
	}
	rPort := qdr.PortAsInt(routerPort)
	kb.ports.InUse(rPort)
	binding.ports[servicePort] = rPort
	ingresses[address] = binding
}

func (kb *KubernetesBindings) Recover(bridges *qdr.BridgeConfig) map[string]types.ServiceBinding {
	ingresses := map[string]SimpleIngress{}
	for _, l := range bridges.TcpListeners {
		kb.update(ingresses, l.Address, l.Port, l.GetProtocol(), "", false)
	}
	for _, l := range bridges.HttpListeners {
		kb.update(ingresses, l.Address, l.Port, l.GetProtocol(), l.Aggregation, l.EventChannel)
	}
	bindings := map[string]types.ServiceBinding{}

	for _, binding := range ingresses {
		binding.SetPorts(binding.ports)
		bindings[binding.GetAddress()] = types.ServiceBinding {
			Ingress: &binding,
		}
	}

	return bindings
}

type ServiceEgress struct {
	types.BridgeBinding
	service string
}

func (s *ServiceEgress) Realise() error { return nil }
func (s *ServiceEgress) Deleted() {}

func (s *ServiceEgress) Configure(bridges *qdr.BridgeConfig) {
	s.AddEgressBridge(s.service, bridges)
}

func (s *ServiceEgress) Compare(definition *types.ServiceInterface, target *types.ServiceInterfaceTarget) bool {
	return definition.Headless == nil && s.GetName() == target.Name && s.Match(definition) && s.service == target.Service && s.PortsMatch(target.TargetPorts)
}

func (kb *KubernetesBindings) newServiceEgress(definition *types.ServiceInterface, target *types.ServiceInterfaceTarget) types.EgressBinding {
	egress := &ServiceEgress{
		service: target.Service,
	}
	egress.InitFromDefinition(target.Name, kb.siteId, definition, target.TargetPorts)
	egress.SetHostOverride(egress.service)
	return egress
}

type SelectorEgress struct {
	types.BridgeBinding
	selector string
	pods     *StopablePodWatcher
}

func (kb *KubernetesBindings) newSelectorEgress(definition *types.ServiceInterface, target *types.ServiceInterfaceTarget) types.EgressBinding {
	egress := &SelectorEgress{
		selector: target.Selector,
		pods:     kb.podWatcher.WatchServicePods(target.Selector),
	}
	egress.InitFromDefinition(target.Name, kb.siteId, definition, target.TargetPorts)
	return egress
}

func (s *SelectorEgress) Realise() error { return nil }
func (s *SelectorEgress) Deleted() {
	s.pods.Stop()
}

func (s *SelectorEgress) Configure(bridges *qdr.BridgeConfig) {
	for _, pod := range s.pods.List() {
		if IsPodRunning(pod) && IsPodReady(pod) && pod.DeletionTimestamp == nil {
			event.Recordf(BridgeTargetEvent, "Adding pod for %s: %s", s.GetAddress(), pod.ObjectMeta.Name)
			s.AddEgressBridge(pod.Status.PodIP, bridges)
		} else {
			event.Recordf(BridgeTargetEvent, "Pod for %s not ready/running: %s", s.GetAddress(), pod.ObjectMeta.Name)
		}
	}
}

func (s *SelectorEgress) Compare(definition *types.ServiceInterface, target *types.ServiceInterfaceTarget) bool {
	return definition.Headless == nil && s.GetName() == target.Name && s.Match(definition) && s.selector == target.Selector && s.PortsMatch(target.TargetPorts)
}
