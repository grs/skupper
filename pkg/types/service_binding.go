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

package types

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/skupperproject/skupper/pkg/qdr"
)

type IngressBinding interface {
	Realise() error
	Configure(bridges *qdr.BridgeConfig)
	Compare(definition *ServiceInterface) bool
	Deleted()
}

type EgressBinding interface {
	Realise() error
	Configure(bridges *qdr.BridgeConfig)
	Compare(definition *ServiceInterface, target *ServiceInterfaceTarget) bool
	Deleted()
}

type BindingFactory interface {
	NewIngressBinding(definition *ServiceInterface) IngressBinding
	NewEgressBinding(definition *ServiceInterface, target *ServiceInterfaceTarget) EgressBinding
	Recover(bridges *qdr.BridgeConfig) map[string]ServiceBinding
}

type ServiceBinding struct {
	Ingress IngressBinding
	Egress  []EgressBinding
}

func (s *ServiceBinding) Realise() error {
	err := s.Ingress.Realise()
	if err != nil {
		return err
	}
	for _, egress := range s.Egress {
		err = egress.Realise()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *ServiceBinding) Configure(bridges *qdr.BridgeConfig) {
	s.Ingress.Configure(bridges)
	for _, egress := range s.Egress {
		egress.Configure(bridges)
	}
}

type ServiceBindings struct {
	bindings map[string]ServiceBinding
	factory  BindingFactory
}

func NewServiceBindings(factory BindingFactory, bridges *qdr.BridgeConfig) *ServiceBindings {
	return &ServiceBindings{
		bindings: factory.Recover(bridges),
		factory:  factory,
	}
}

func (b *ServiceBindings) Update(definitions map[string]ServiceInterface) error {
	//TODO
	return nil
}

type EgressBindingsMatcher struct {
	unmatched []EgressBinding
	matched []EgressBinding
}

func (s *ServiceBinding) newEgressBindingsMatcher() *EgressBindingsMatcher {
	return &EgressBindingsMatcher{
		unmatched: s.Egress,
	}
}

func (bindings *EgressBindingsMatcher) match(definition *ServiceInterface, target *ServiceInterfaceTarget) bool {
	var pending []EgressBinding
	for _, egress := range bindings.unmatched {
		if egress.Compare(definition, target) {
			bindings.matched = append(bindings.matched, egress)
			return true
		} else {
			pending = append(pending, egress)
		}
	}
	bindings.unmatched = pending
	return false
}

func (s *ServiceBinding) Update(definition *ServiceInterface, factory BindingFactory) {
	if !s.Ingress.Compare(definition) {
		s.Ingress.Deleted()
		s.Ingress = factory.NewIngressBinding(definition)
	}
	egresses := s.newEgressBindingsMatcher()
	for _, target := range definition.Targets {
		if !egresses.match(definition, &target) {
			s.Egress = append(s.Egress, factory.NewEgressBinding(definition, &target))
		}
	}
	for _, egress := range egresses.unmatched {
		egress.Deleted()
	}
}

type BridgeBinding struct {
	name         string
	address      string
	protocol     string
	ports        map[int]int
	siteId       string
	hostOverride string
	aggregation  string
	eventchannel bool
}

func (s *BridgeBinding) InitFromDefinition(name string, siteId string, definition *ServiceInterface, ports map[int]int) {
	s.Init(name, siteId, definition.Address, definition.Protocol, definition.Aggregate, definition.EventChannel)
	s.SetPorts(ports)
}

func (s *BridgeBinding) Init(name string, siteId string, address string, protocol string, aggregation string, eventChannel bool) {
	s.name = name
	s.address = address
	s.protocol = protocol
	s.siteId = siteId
	s.aggregation = aggregation
	s.eventchannel = eventChannel
}

func (s *BridgeBinding) SetPorts(ports map[int]int) {
	s.ports = ports
}

func (s *BridgeBinding) SetHostOverride(host string) {
	s.hostOverride = host
}

func (s *BridgeBinding) AddEgressBridge(host string, bridges *qdr.BridgeConfig) (bool, error) {
	if host == "" {
		return false, fmt.Errorf("Cannot add connector without host (%s %s)", s.address, s.protocol)
	}
	for sPort, tPort := range s.ports {
		endpointName := getBridgeName(s.address+"."+s.name, host, sPort, tPort)
		endpointAddr := fmt.Sprintf("%s:%d", s.address, sPort)
		switch s.protocol {
		case ProtocolHTTP:
			b := qdr.HttpEndpoint{
				Name:    endpointName,
				Host:    host,
				Port:    strconv.Itoa(tPort),
				Address: endpointAddr,
				SiteId:  s.siteId,
			}
			if s.aggregation != "" {
				b.Aggregation = s.aggregation
				b.Address = "mc/" + endpointAddr
			}
			if s.eventchannel {
				b.EventChannel = s.eventchannel
				b.Address = "mc/" + endpointAddr
			}
			if s.hostOverride != "" {
				b.HostOverride = s.hostOverride
			}
			bridges.AddHttpConnector(b)
		case ProtocolHTTP2:
			bridges.AddHttpConnector(qdr.HttpEndpoint{
				Name:            endpointName,
				Host:            host,
				Port:            strconv.Itoa(tPort),
				Address:         endpointAddr,
				SiteId:          s.siteId,
				ProtocolVersion: qdr.HttpVersion2,
			})
		case ProtocolTCP:
			bridges.AddTcpConnector(qdr.TcpEndpoint{
				Name:    endpointName,
				Host:    host,
				Port:    strconv.Itoa(tPort),
				Address: endpointAddr,
				SiteId:  s.siteId,
			})
		default:
			return false, fmt.Errorf("Unrecognised protocol for service %s: %s", s.address, s.protocol)
		}
	}
	return true, nil
}

func (s *BridgeBinding) AddIngressBridge(bridges *qdr.BridgeConfig) (bool, error) {
	for pPort, iPort := range s.ports {
		endpointName := getBridgeName(s.address, "", pPort)
		endpointAddr := fmt.Sprintf("%s:%d", s.address, pPort)

		switch s.protocol {
		case ProtocolHTTP:
			if s.aggregation != "" || s.eventchannel {
				endpointAddr = "mc/" + endpointAddr
			}
			bridges.AddHttpListener(qdr.HttpEndpoint{
				Name:         endpointName,
				Host:         "0.0.0.0",
				Port:         strconv.Itoa(iPort),
				Address:      endpointAddr,
				SiteId:       s.siteId,
				Aggregation:  s.aggregation,
				EventChannel: s.eventchannel,
			})

		case ProtocolHTTP2:
			bridges.AddHttpListener(qdr.HttpEndpoint{
				Name:            endpointName,
				Host:            "0.0.0.0",
				Port:            strconv.Itoa(iPort),
				Address:         endpointAddr,
				SiteId:          s.siteId,
				Aggregation:     s.aggregation,
				EventChannel:    s.eventchannel,
				ProtocolVersion: qdr.HttpVersion2,
			})
		case ProtocolTCP:
			bridges.AddTcpListener(qdr.TcpEndpoint{
				Name:    endpointName,
				Host:    "0.0.0.0",
				Port:    strconv.Itoa(iPort),
				Address: endpointAddr,
				SiteId:  s.siteId,
			})
		default:
			return false, fmt.Errorf("Unrecognised protocol for service %s: %s", s.address, s.protocol)
		}
	}
	return true, nil
}

func (s *BridgeBinding) GetName() string {
	return s.name
}

func (s *BridgeBinding) GetAddress() string {
	return s.address
}

func (s *BridgeBinding) PortsMatch(ports map[int]int) bool {
	return reflect.DeepEqual(s.ports, ports)
}

func (s *BridgeBinding) PortsMatchKeys(ports []int) bool {
	for key := range ports {
		if _, ok := s.ports[key]; !ok {
			return false
		}
	}
	return len(ports) == len(s.ports)
}

func (s *BridgeBinding) Match(def *ServiceInterface) bool {
	return s.protocol == def.Protocol && s.eventchannel == def.EventChannel && s.aggregation == def.Aggregate
}

func getBridgeName(address string, host string, port ...int) string {
	portSuffix := func(port ...int) string {
		s := ""
		for _, p := range port {
			s += ":" + strconv.Itoa(p)
		}
		return s
	}
	if host == "" {
		return address + portSuffix(port...)
	} else {
		return address + "@" + host + portSuffix(port...)
	}
}

func ParsePortQualifiedAddress(qualifiedAddress string) (string, int) {
	parts := strings.Split(qualifiedAddress, ":")
	address := parts[0]
	servicePort, _ := strconv.Atoi(parts[1])
	return address, servicePort
}

