package kube

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skupperproject/skupper/pkg/data"
	"github.com/skupperproject/skupper/pkg/types"
	"github.com/skupperproject/skupper/pkg/utils"
)

const (
	ServiceManagement string = "ServiceManagement"
	ServiceInterfaceConfigMap string = "skupper-services"
)

func asPortDescriptions(in []corev1.ContainerPort) []data.PortDescription {
	out := []data.PortDescription{}
	for _, port := range in {
		out = append(out, data.PortDescription{Name: port.Name, Port: int(port.ContainerPort)})
	}
	return out
}

type ServiceManager struct {
	cli       kubernetes.Interface
	namespace string
}

func newServiceManager(cli kubernetes.Interface, namespace string) *ServiceManager {
	return &ServiceManager{
		cli: cli,
		namespace: namespace,
	}
}

func (m *ServiceManager) resolveEndpoints(ctx context.Context, targetName string, selector string, targetPort map[int]int) ([]data.ServiceEndpoint, error) {
	endpoints := []data.ServiceEndpoint{}
	pods, err := GetPods(selector, m.namespace, m.cli)
	if err != nil {
		return endpoints, err
	}
	for _, pod := range pods {
		endpoints = append(endpoints, data.ServiceEndpoint{Name: pod.ObjectMeta.Name, Target: targetName, Ports: targetPort})
	}
	return endpoints, nil
}

func (m *ServiceManager) asServiceDefinition(def *types.ServiceInterface) (*data.ServiceDefinition, error) {
	svc := &data.ServiceDefinition{
		Name:     def.Address,
		Protocol: def.Protocol,
		Ports:    def.Ports,
	}
	ctx := context.Background()
	for _, target := range def.Targets {
		if target.Selector != "" {
			endpoints, err := m.resolveEndpoints(ctx, target.Name, target.Selector, target.TargetPorts)
			if err != nil {
				return svc, err
			}
			svc.Endpoints = append(svc.Endpoints, endpoints...)
		}
	}
	return svc, nil
}

func (m *ServiceManager) getServiceDefinitions() (*corev1.ConfigMap, error) {
	return m.cli.CoreV1().ConfigMaps(m.namespace).Get(ServiceInterfaceConfigMap, metav1.GetOptions{})
}

func (m *ServiceManager) GetServices() ([]data.ServiceDefinition, error) {
	services := []data.ServiceDefinition{}

	current, err := m.getServiceDefinitions()
	if err != nil {
		return services, err
	}
	for _, v := range current.Data {
		if v != "" {
			si := types.ServiceInterface{}
			err = json.Unmarshal([]byte(v), &si)
			if err != nil {
				return services, err
			}
			svc, err := m.asServiceDefinition(&si)
			if err != nil {
				return services, err
			}
			services = append(services, *svc)
		}
	}

	return services, nil
}

func (m *ServiceManager) GetService(name string) (*data.ServiceDefinition, error) {
	current, err := m.getServiceDefinitions()
	if err != nil {
		return nil, err
	}
	if v, ok := current.Data[name]; ok && v != "" {
		si := types.ServiceInterface{}
		err = json.Unmarshal([]byte(v), &si)
		if err != nil {
			return nil, err
		}
		svc, err := m.asServiceDefinition(&si)
		if err != nil {
			return nil, err
		}
		return svc, nil
	}
	return nil, fmt.Errorf("No definition found for service %s", name)
}

func (m *ServiceManager) CreateService(options *data.ServiceOptions) error {
	def := &types.ServiceInterface{
		Address:  options.GetServiceName(),
		Protocol: options.GetProtocol(),
		Ports:    options.GetPorts(),
	}
	deducePort := options.DeducePort()
	target, err := getServiceInterfaceTarget(options.GetTargetType(), options.GetTargetName(), deducePort, m.namespace, m.cli)
	if err != nil {
		return err
	}
	if deducePort {
		def.Ports = []int{}
		for _, tPort := range target.TargetPorts {
			def.Ports = append(def.Ports, tPort)
		}
		target.TargetPorts = map[int]int{}
	} else {
		target.TargetPorts = options.GetTargetPorts()
	}
	def.AddTarget(target)
	def.Labels = options.Labels


	encoded, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("Failed to encode service interface as json: %s", err)
	}
	current, err := m.getServiceDefinitions()
	if err != nil {
		return err
	}
	if current.Data == nil {
		current.Data = map[string]string{}
	}
	if _, ok := current.Data[def.Address]; !ok {
		return fmt.Errorf("Service %s already defined", def.Address)
	}
	current.Data[def.Address] = string(encoded)
	_, err = m.cli.CoreV1().ConfigMaps(m.namespace).Update(current)
	return err
}

func isServiceNotDefined(err error, name string) bool {
	msg := "Service " + name + " not defined"
	return err.Error() == msg
}

func (m *ServiceManager) DeleteService(name string) (bool, error) {
	current, err := m.getServiceDefinitions()
	if err != nil {
		return false, err
	}
	if current.Data != nil && current.Data[name] != "" {
		delete(current.Data, name)
		_, err = m.cli.CoreV1().ConfigMaps(m.namespace).Update(current)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (m *ServiceManager) GetServiceTargets() ([]data.ServiceTargetDefinition, error) {
	targets := []data.ServiceTargetDefinition{}
	deployments, err := m.cli.AppsV1().Deployments(m.namespace).List(metav1.ListOptions{})
	if err != nil {
		return targets, err
	}
	for _, deployment := range deployments.Items {
		if !types.IsInternalDeployment(deployment.ObjectMeta.Name) {
			targets = append(targets, data.ServiceTargetDefinition{
				Name:  deployment.ObjectMeta.Name,
				Type:  "deployment",
				Ports: asPortDescriptions(GetContainerPorts(&deployment.Spec.Template.Spec)),
			})
		}
	}
	statefulsets, err := m.cli.AppsV1().StatefulSets(m.namespace).List(metav1.ListOptions{})
	if err != nil {
		return targets, err
	}
	for _, statefulset := range statefulsets.Items {
		targets = append(targets, data.ServiceTargetDefinition{
			Name:  statefulset.ObjectMeta.Name,
			Type:  "statefulset",
			Ports: asPortDescriptions(GetContainerPorts(&statefulset.Spec.Template.Spec)),
		})
	}
	return targets, nil
}


func getServiceInterfaceTarget(targetType string, targetName string, deducePort bool, namespace string, cli kubernetes.Interface) (*types.ServiceInterfaceTarget, error) {
	if targetType == "deployment" {
		deployment, err := cli.AppsV1().Deployments(namespace).Get(targetName, metav1.GetOptions{})
		if err == nil {
			target := types.ServiceInterfaceTarget{
				Name:     deployment.ObjectMeta.Name,
				Selector: utils.StringifySelector(deployment.Spec.Selector.MatchLabels),
			}
			if deducePort {
				//TODO: handle case where there is more than one container (need --container option?)
				if deployment.Spec.Template.Spec.Containers[0].Ports != nil {
					target.TargetPorts = GetAllContainerPorts(deployment.Spec.Template.Spec.Containers[0])
				}
			}
			return &target, nil
		} else {
			return nil, fmt.Errorf("Could not read deployment %s: %s", targetName, err)
		}
	} else if targetType == "statefulset" {
		statefulset, err := cli.AppsV1().StatefulSets(namespace).Get(targetName, metav1.GetOptions{})
		if err == nil {
			target := types.ServiceInterfaceTarget{
				Name:     statefulset.ObjectMeta.Name,
				Selector: utils.StringifySelector(statefulset.Spec.Selector.MatchLabels),
			}
			if deducePort {
				//TODO: handle case where there is more than one container (need --container option?)
				if statefulset.Spec.Template.Spec.Containers[0].Ports != nil {
					target.TargetPorts = GetAllContainerPorts(statefulset.Spec.Template.Spec.Containers[0])
				}
			}
			return &target, nil
		} else {
			return nil, fmt.Errorf("Could not read statefulset %s: %s", targetName, err)
		}
	} else if targetType == "pods" {
		return nil, fmt.Errorf("VAN service interfaces for pods not yet implemented")
	} else if targetType == "service" {
		target := types.ServiceInterfaceTarget{
			Name:    targetName,
			Service: targetName,
		}
		if deducePort {
			ports, err := GetPortsForServiceTarget(targetName, namespace, cli)
			if err != nil {
				return nil, err
			}
			if len(ports) > 0 {
				target.TargetPorts = ports
			}
		}
		return &target, nil
	} else {
		return nil, fmt.Errorf("VAN service interface unsupported target type")
	}
}
