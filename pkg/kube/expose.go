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
	"time"

	corev1 "k8s.io/api/core/v1"

	routev1 "github.com/openshift/api/route/v1"

	"github.com/skupperproject/skupper/pkg/types"
)

type Address struct {
	Name string
	Host string
	Port int32
}

type ExposeStrategy interface {
	Expose(component *types.Component, service *types.Service) error
	Resolve(service *types.Service) ([]Address, error)
}

type LoadBalancerStrategy struct {
	mgr *SiteManager
}

func (l *LoadBalancerStrategy) Expose(component *types.Component, svc *types.Service) error {
	service := getService(component.Name, svc)
	service.Spec.Type = corev1.ServiceTypeLoadBalancer
	return l.mgr.ensureService(service)
}

func (l *LoadBalancerStrategy) Resolve(svc *types.Service) ([]Address, error) {
	service, err := l.mgr.getService(svc.Name)
	if err != nil {
		return nil, err
	}
	host := GetLoadBalancerHostOrIP(service)
	if host == "" {
		if time.Since(service.ObjectMeta.CreationTimestamp.Time).Minutes() > 10 {
			return nil, fmt.Errorf("Failed to get LoadBalancer IP or Hostname for service %s", svc.Name)
		}
		return nil, fmt.Errorf("LoadBalancer IP or Hostname for service %s not yet available", svc.Name) //not ready
	} else {
		addresses := []Address{}
		for _, port := range svc.Ports {
			addresses = append(addresses, Address {
				Name: port.Name,
				Host: host,
				Port: port.Port,
			})
		}
		return addresses, nil
	}
}

func NewLoadBalancerStrategy(m *SiteManager) *LoadBalancerStrategy {
	return &LoadBalancerStrategy{
		mgr: m,
	}
}

type NodePortStrategy struct {
	mgr  *SiteManager
	host string
}

func (s *NodePortStrategy) Expose(component *types.Component, svc *types.Service) error {
	service := getService(component.Name, svc)
	service.Spec.Type = corev1.ServiceTypeNodePort
	return s.mgr.ensureService(service)
}

func (s *NodePortStrategy) Resolve(svc *types.Service) ([]Address, error) {
	service, err := s.mgr.getService(svc.Name)
	if err != nil {
		return nil, err
	}
	addresses := []Address{}
	for _, port := range service.Spec.Ports {
		if port.NodePort == 0 {
			return nil, fmt.Errorf("NodePort for %s on service %s not yet available", port.Name, svc.Name) //not ready
		}
		addresses = append(addresses, Address {
			Name: port.Name,
			Host: s.host,
			Port: port.NodePort,
		})
	}
	return addresses, nil
}

func NewNodePortStrategy(m *SiteManager, host string) *NodePortStrategy {
	return &NodePortStrategy{
		mgr:  m,
		host: host,
	}
}

type RouteStrategy struct {
	mgr *SiteManager
}

func (s *RouteStrategy) Expose(component *types.Component, svc *types.Service) error {
	kubeSvc := getService(component.Name, svc)
	credName := svc.GetServerCredentialName()
	if svc.HasListenerTypeConsole() && credName != "" {
		kubeSvc.ObjectMeta.Annotations["service.alpha.openshift.io/serving-cert-secret-name"] = credName
	}
	err := s.mgr.ensureService(kubeSvc)
	if err != nil {
		return err
	}
	for _, port := range svc.Ports {
		termination := routev1.TLSTerminationPassthrough
		if port.Type == types.ListenerTypeConsole {
			termination = routev1.TLSTerminationReencrypt
		}
		routeName := "skupper-" + port.Name
		_, err := s.mgr.ensureRoute(routeName, svc.Name, port.Name, termination)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *RouteStrategy) Resolve(svc *types.Service) ([]Address, error) {
	addresses := []Address{}
	for _, port := range svc.Ports {
		name := "skupper-" + port.Name
		route, err := s.mgr.getRoute(name)
		if err != nil {
			return nil, err
		}
		if route.Spec.Host == "" {
			return addresses, fmt.Errorf("Host for route %s not yet specified", name)
		}
		addresses = append(addresses, Address{
			Name: port.Name,
			Host: route.Spec.Host,
			Port: 443,
		})
	}
	return addresses, nil
}

func NewRouteStrategy(m *SiteManager) *RouteStrategy {
	return &RouteStrategy{
		mgr:  m,
	}
}

type NginxIngressStrategy struct {
	mgr *SiteManager
}

func NewNginxIngressStrategy(m *SiteManager) *NginxIngressStrategy {
	return &NginxIngressStrategy{
		mgr:  m,
	}
}

func (s *NginxIngressStrategy) Expose(component *types.Component, svc *types.Service) error {
	err := s.mgr.ensureService(getService(component.Name, svc))
	if err != nil {
		return err
	}
	//TODO: create ingress
	ingressName := ""
	s.mgr.requiredResources.addIngress(ingressName)
	return nil
}

func (s *NginxIngressStrategy) Resolve(svc *types.Service) ([]Address, error) {
	addresses := []Address{}
	//TODO: lookup ingress
	return addresses, nil
}

type NoneStrategy struct {
	mgr *SiteManager
}

func NewNoneStrategy(m *SiteManager) *NoneStrategy {
	return &NoneStrategy{
		mgr:  m,
	}
}

func (s *NoneStrategy) Expose(component *types.Component, svc *types.Service) error {
	return s.mgr.ensureService(getService(component.Name, svc))
}

func (s *NoneStrategy) Resolve(svc *types.Service) ([]Address, error) {
	service, err := s.mgr.getService(svc.Name)
	if err != nil {
		return nil, err
	}
	addresses := []Address{}
	for _, port := range service.Spec.Ports {
		addresses = append(addresses, Address {
			Name: port.Name,
			Host: service.ObjectMeta.Name,
			Port: port.Port,
		})
		addresses = append(addresses, Address {
			Name: port.Name,
			Host: service.ObjectMeta.Name + "." + service.ObjectMeta.Namespace,
			Port: port.Port,
		})
	}
	return addresses, nil
}

func (mgr *SiteManager) getSiteExposeStrategy() ExposeStrategy {
	ingressHost := mgr.config.GetRouterIngressHost()
	if mgr.config.IsIngressLoadBalancer() {
		return NewLoadBalancerStrategy(mgr)
	} else if mgr.config.IsIngressRoute() {
		return NewRouteStrategy(mgr)
	} else if mgr.config.IsIngressNodePort() {
		return NewNodePortStrategy(mgr, ingressHost)
	} else if mgr.config.IsIngressNginxIngress() {
		return NewNginxIngressStrategy(mgr)
	} else if mgr.config.IsIngressNone() {
		return NewNoneStrategy(mgr)
	} else {
		//unspecified, use routes if have them, else loadbalancer
		if mgr.routecli != nil {
			return NewRouteStrategy(mgr)
		} else {
			return NewLoadBalancerStrategy(mgr)
		}
	}
}

func (mgr *SiteManager) getConsoleExposeStrategy() ExposeStrategy {
	ingressHost := mgr.config.GetControllerIngressHost()
	if mgr.config.IsConsoleIngressLoadBalancer() {
		return NewLoadBalancerStrategy(mgr)
	} else if mgr.config.IsConsoleIngressRoute() {
		return NewRouteStrategy(mgr)
	} else if mgr.config.IsConsoleIngressNodePort() {
		return NewNodePortStrategy(mgr, ingressHost)
	} else if mgr.config.IsConsoleIngressNginxIngress() {
		return NewNginxIngressStrategy(mgr)
	} else if mgr.config.IsConsoleIngressNone() {
		return NewNoneStrategy(mgr)
	} else {
		//unspecified, use routes if have them, else loadbalancer
		if mgr.routecli != nil {
			return NewRouteStrategy(mgr)
		} else {
			return NewLoadBalancerStrategy(mgr)
		}
	}
}

func hosts(addresses []Address) []string {
	hosts := []string{}
	lookup := map[string]bool{}
	for _, address := range addresses {
		if _, ok := lookup[address.Host]; !ok {
			lookup[address.Host] = true
			hosts = append(hosts, address.Host)
		}
	}
	return hosts
}
