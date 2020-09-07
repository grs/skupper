package qdr

import (
	"encoding/json"
	"fmt"
	"log"
	"reflect"

	corev1 "k8s.io/api/core/v1"

	"github.com/skupperproject/skupper/api/types"
)

type RouterConfig struct {
	Metadata        RouterMetadata
	SslProfiles     map[string]SslProfile
	Listeners       map[string]Listener
	Connectors      map[string]Connector
	Addresses       map[string]Address
	Bridges         BridgeConfig
}

type TcpEndpointMap map[string]TcpEndpoint
type HttpEndpointMap map[string]HttpEndpoint

type BridgeConfig struct {
	TcpListeners    TcpEndpointMap
	TcpConnectors   TcpEndpointMap
	HttpListeners   HttpEndpointMap
	HttpConnectors  HttpEndpointMap
}

func InitialConfig(id string, metadata string, edge bool) RouterConfig {
	config := RouterConfig{
		Metadata:     RouterMetadata{
			Id: id,
			Metadata: metadata,
		},
		Addresses:  map[string]Address{},
		SslProfiles:  map[string]SslProfile{},
		Listeners:  map[string]Listener{},
		Connectors:  map[string]Connector{},
		Bridges: BridgeConfig {
			TcpListeners:  map[string]TcpEndpoint{},
			TcpConnectors:  map[string]TcpEndpoint{},
			HttpListeners:  map[string]HttpEndpoint{},
			HttpConnectors:  map[string]HttpEndpoint{},
		},
	}
	if edge {
		config.Metadata.Mode = ModeEdge
	} else {
		config.Metadata.Mode = ModeInterior
	}
	return config
}

func NewBridgeConfig() BridgeConfig {
	return BridgeConfig {
		TcpListeners:  map[string]TcpEndpoint{},
		TcpConnectors:  map[string]TcpEndpoint{},
		HttpListeners:  map[string]HttpEndpoint{},
		HttpConnectors:  map[string]HttpEndpoint{},
	}
}

func (r *RouterConfig) AddListener(l Listener) {
	if l.Name == "" {
		l.Name = fmt.Sprintf("%s@%d", l.Host, l.Port)
	}
	r.Listeners[l.Name] = l
}

func (r *RouterConfig) AddConnector(c Connector) {
	r.Connectors[c.Name] = c
}

func (r *RouterConfig) RemoveConnector(name string) bool {
	_, ok := r.Connectors[name]
	if ok {
		delete(r.Connectors, name)
		return true
	} else {
		return false
	}
}

func (r *RouterConfig) IsEdge() bool {
	return r.Metadata.Mode == ModeEdge
}

func (r *RouterConfig) AddSslProfile(s SslProfile) {
	if s.CertFile == "" && s.CaCertFile == "" && s.PrivateKeyFile == "" {
		s.CertFile = fmt.Sprintf("/etc/qpid-dispatch-certs/%s/tls.crt", s.Name)
		s.PrivateKeyFile = fmt.Sprintf("/etc/qpid-dispatch-certs/%s/tls.key", s.Name)
		s.CaCertFile = fmt.Sprintf("/etc/qpid-dispatch-certs/%s/ca.crt", s.Name)
	}
	r.SslProfiles[s.Name] = s
}

func (r *RouterConfig) AddAddress(a Address) {
	r.Addresses[a.Prefix] = a
}

func (r *RouterConfig) AddTcpConnector(e TcpEndpoint) {
	r.Bridges.AddTcpConnector(e)
}

func (r *RouterConfig) AddTcpListener(e TcpEndpoint) {
	r.Bridges.AddTcpListener(e)
}

func (r *RouterConfig) AddHttpConnector(e HttpEndpoint) {
	r.Bridges.AddHttpConnector(e)
}

func (r *RouterConfig) AddHttpListener(e HttpEndpoint) {
	r.Bridges.AddHttpListener(e)
}

func (r *RouterConfig) UpdateBridgeConfig(desired BridgeConfig) bool {
	if reflect.DeepEqual(r.Bridges, desired) {
		return false
	} else {
		r.Bridges = desired
		return true
	}
}

func (bc *BridgeConfig) AddTcpConnector(e TcpEndpoint) {
	bc.TcpConnectors[e.Name] = e
}

func (bc *BridgeConfig) AddTcpListener(e TcpEndpoint) {
	bc.TcpListeners[e.Name] = e
}

func (bc *BridgeConfig) AddHttpConnector(e HttpEndpoint) {
	bc.HttpConnectors[e.Name] = e
}

func (bc *BridgeConfig) AddHttpListener(e HttpEndpoint) {
	bc.HttpListeners[e.Name] = e
}

type Role string

const (
	RoleInterRouter Role = "inter-router"
	RoleEdge             = "edge"
)

type Mode string

const (
	ModeInterior Mode = "interior"
	ModeEdge          = "edge"
)

type RouterMetadata struct {
	Id              string  `json:"id,omitempty"`
	Mode            Mode    `json:"mode,omitempty"`
	Metadata        string  `json:"metadata,omitempty"`
}

type SslProfile struct {
	Name            string `json:"name,omitempty"`
	CertFile        string `json:"certFile,omitempty"`
	PrivateKeyFile  string `json:"privateKeyFile,omitempty"`
	CaCertFile      string `json:"caCertFile,omitempty"`
}

type Listener struct {
	Name             string `json:"name,omitempty"`
	Role             Role   `json:"role,omitempty"`
	Host             string `json:"host,omitempty"`
	Port             int32  `json:"port"`
	RouteContainer   bool   `json:"routeContainer,omitempty"`
	Http             bool   `json:"http,omitempty"`
	Cost             int32  `json:"cost,omitempty"`
	SslProfile       string `json:"sslProfile,omitempty"`
	SaslMechanisms   string `json:"saslMechanisms,omitempty"`
	AuthenticatePeer bool   `json:"authenticatePeer,omitempty"`
	LinkCapacity     int32  `json:"linkCapacity,omitempty"`
	HttpRootDir      string `json:"httpRootDir,omitempty"`
	Websockets       bool   `json:"websockets,omitempty"`
	Healthz          bool   `json:"healthz,omitempty"`
	Metrics          bool   `json:"metrics,omitempty"`
}

type Connector struct {
	Name           string `json:"name,omitempty"`
	Role           Role   `json:"role,omitempty"`
	Host           string `json:"host"`
	Port           string `json:"port"`
	RouteContainer bool   `json:"routeContainer,omitempty"`
	Cost           int32  `json:"cost,omitempty"`
	VerifyHostname bool   `json:"verifyHostname,omitempty"`
	SslProfile     string `json:"sslProfile,omitempty"`
	LinkCapacity   int32  `json:"linkCapacity,omitempty"`
}

type Distribution string

const (
	DistributionBalanced Distribution = "balanced"
	DistributionMulticast             = "multicast"
	DistributionClosest               = "closest"
)

type Address struct {
	Prefix          string `json:"prefix,omitempty"`
	Distribution    string `json:"distribution,omitempty"`
}

type TcpEndpoint struct {
	Name         string `json:"name,omitempty"`
	Host         string `json:"host,omitempty"`
	Port         string `json:"port,omitempty"`
	Address      string `json:"address,omitempty"`
	SiteId       string `json:"siteId,omitempty"`
}

type HttpEndpoint struct {
	Name            string `json:"name,omitempty"`
	Host            string `json:"host,omitempty"`
	Port            string `json:"port,omitempty"`
	Address         string `json:"address,omitempty"`
	SiteId          string `json:"siteId,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	Aggregation     string `json:"aggregation,omitempty"`
	EventChannel    bool   `json:"eventChannel,omitempty"`
	HostOverride    string `json:"hostOverride,omitempty"`
}

func convert(from interface{}, to interface{}) error {
	data, err := json.Marshal(from)
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, to)
	if err != nil {
		return err
	}
	return nil
}

func UnmarshalRouterConfig(config string) (RouterConfig, error) {
	result := RouterConfig{
		Metadata:     RouterMetadata{},
		Addresses:  map[string]Address{},
		SslProfiles:  map[string]SslProfile{},
		Listeners:  map[string]Listener{},
		Connectors:  map[string]Connector{},
		Bridges: BridgeConfig {
			TcpListeners:  map[string]TcpEndpoint{},
			TcpConnectors:  map[string]TcpEndpoint{},
			HttpListeners:  map[string]HttpEndpoint{},
			HttpConnectors:  map[string]HttpEndpoint{},
		},
	}
	var obj interface{}
	err := json.Unmarshal([]byte(config), &obj)
	if err != nil {
		return result, err
	}
	elements, ok := obj.([]interface{})
	if !ok {
		return result, fmt.Errorf("Invalid JSON for router configuration, expected array at top level got %#v", obj)
	}
	for _, e := range elements {
		element, ok := e.([]interface{})
		if !ok || len(element) != 2 {
			return result, fmt.Errorf("Invalid JSON for router configuration, expected array with type and value got %#v", e)
		}
		entityType, ok := element[0].(string)
		if !ok {
			return result, fmt.Errorf("Invalid JSON for router configuration, expected entity type as string got %#v", element[0])
		}
		switch entityType {
		case "router":
			metadata := RouterMetadata{}
			err = convert(element[1], &metadata)
			if err != nil {
				return result, fmt.Errorf("Invalid %s element got %#v", entityType, element[1])
			}
			result.Metadata = metadata
		case "address":
			address := Address{}
			err = convert(element[1], &address)
			if err != nil {
				return result, fmt.Errorf("Invalid %s element got %#v", entityType, element[1])
			}
			result.Addresses[address.Prefix] = address
		case "connector":
			connector := Connector{}
			err = convert(element[1], &connector)
			if err != nil {
				return result, fmt.Errorf("Invalid %s element got %#v", entityType, element[1])
			}
			result.Connectors[connector.Name] = connector
		case "listener":
			listener := Listener{}
			err = convert(element[1], &listener)
			if err != nil {
				return result, fmt.Errorf("Invalid %s element got %#v", entityType, element[1])
			}
			result.Listeners[listener.Name] = listener
		case "sslProfile":
			sslProfile := SslProfile{}
			err = convert(element[1], &sslProfile)
			if err != nil {
				return result, fmt.Errorf("Invalid %s element got %#v", entityType, element[1])
			}
			result.SslProfiles[sslProfile.Name] = sslProfile
		case "tcpConnector":
			connector := TcpEndpoint{}
			err = convert(element[1], &connector)
			if err != nil {
				return result, fmt.Errorf("Invalid %s element got %#v", entityType, element[1])
			}
			result.Bridges.TcpConnectors[connector.Name] = connector
		case "tcpListener":
			listener := TcpEndpoint{}
			err = convert(element[1], &listener)
			if err != nil {
				return result, fmt.Errorf("Invalid %s element got %#v", entityType, element[1])
			}
			result.Bridges.TcpListeners[listener.Name] = listener
		case "httpConnector":
			connector := HttpEndpoint{}
			err = convert(element[1], &connector)
			if err != nil {
				return result, fmt.Errorf("Invalid %s element got %#v", entityType, element[1])
			}
			result.Bridges.HttpConnectors[connector.Name] = connector
		case "httpListener":
			listener := HttpEndpoint{}
			err = convert(element[1], &listener)
			if err != nil {
				return result, fmt.Errorf("Invalid %s element got %#v", entityType, element[1])
			}
			result.Bridges.HttpListeners[listener.Name] = listener
		default:
		}
	}
	return result, nil
}

func MarshalRouterConfig(config RouterConfig) (string, error) {
	elements := [][]interface{}{}
	tuple := []interface{}{
		"router",
		config.Metadata,
	}
	elements = append(elements, tuple)
	for _, e := range config.SslProfiles {
		tuple := []interface{}{
			"sslProfile",
			e,
		}
		elements = append(elements, tuple)
	}
	for _, e := range config.Connectors {
		tuple := []interface{}{
			"connector",
			e,
		}
		elements = append(elements, tuple)
	}
	for _, e := range config.Listeners {
		tuple := []interface{}{
			"listener",
			e,
		}
		elements = append(elements, tuple)
	}
	for _, e := range config.Addresses {
		tuple := []interface{}{
			"address",
			e,
		}
		elements = append(elements, tuple)
	}
	for _, e := range config.Bridges.TcpConnectors {
		tuple := []interface{}{
			"tcpConnector",
			e,
		}
		elements = append(elements, tuple)
	}
	for _, e := range config.Bridges.TcpListeners {
		tuple := []interface{}{
			"tcpListener",
			e,
		}
		elements = append(elements, tuple)
	}
	for _, e := range config.Bridges.HttpConnectors {
		tuple := []interface{}{
			"httpConnector",
			e,
		}
		elements = append(elements, tuple)
	}
	for _, e := range config.Bridges.HttpListeners {
		tuple := []interface{}{
			"httpListener",
			e,
		}
		elements = append(elements, tuple)
	}
	data, err := json.MarshalIndent(elements, "", "    ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func AsConfigMapData(config string) map[string]string {
	return map[string]string{
		types.TransportConfigFile: config,
	}
}

func (r *RouterConfig) AsConfigMapData() (map[string]string, error) {
	result := map[string]string{}
	marshalled, err := MarshalRouterConfig(*r)
	if err != nil {
		return result, err
	}
	result[types.TransportConfigFile] = marshalled
	return result, nil
}

func (r *RouterConfig) WriteToConfigMap(configmap *corev1.ConfigMap) error {
	var err error
	configmap.Data, err = r.AsConfigMapData()
	return err
}

func (r *RouterConfig) UpdateConfigMap(configmap *corev1.ConfigMap) (bool, error) {
	if configmap.Data != nil && configmap.Data[types.TransportConfigFile] != "" {
		existing, err := UnmarshalRouterConfig(configmap.Data[types.TransportConfigFile])
		if err != nil {
			return false, err
		}
		if reflect.DeepEqual(existing, r) {
			return false, nil
		}
	}
	err := r.WriteToConfigMap(configmap)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (b *BridgeConfig) UpdateConfigMap(configmap *corev1.ConfigMap) (bool, error) {
	if configmap.Data != nil && configmap.Data[types.TransportConfigFile] != "" {
		existing, err := UnmarshalRouterConfig(configmap.Data[types.TransportConfigFile])
		if err != nil {
			return false, err
		}
		if reflect.DeepEqual(existing.Bridges, b) {
			return false, nil
		} else {
			existing.Bridges = *b
			configmap.Data, err = existing.AsConfigMapData()
			if err != nil {
				return false, err
			}
			return true, nil
		}
	} else {
		return false, fmt.Errorf("Router config not defined")
	}
}

func GetRouterConfigFromConfigMap(configmap *corev1.ConfigMap) (*RouterConfig, error) {
	if configmap.Data == nil || configmap.Data[types.TransportConfigFile] == "" {
		return nil, nil
	} else {
		routerConfig, err := UnmarshalRouterConfig(configmap.Data[types.TransportConfigFile])
		if err != nil {
			return nil, err
		}
		return &routerConfig, nil
	}
}

func GetBridgeConfigFromConfigMap(configmap *corev1.ConfigMap) (*BridgeConfig, error) {
	routerConfig, err := GetRouterConfigFromConfigMap(configmap)
	if err != nil {
		return nil, err
	}
	return &routerConfig.Bridges, nil
}


type TcpEndpointDifference struct {
	Deleted []string
	Added []TcpEndpoint
}

type HttpEndpointDifference struct {
	Deleted []string
	Added []HttpEndpoint
}

type BridgeConfigDifference struct {
	TcpListeners    TcpEndpointDifference
	TcpConnectors   TcpEndpointDifference
	HttpListeners   HttpEndpointDifference
	HttpConnectors  HttpEndpointDifference
}

func (a TcpEndpointMap) Difference(b TcpEndpointMap) (TcpEndpointDifference) {
	result := TcpEndpointDifference{}
	for key, v1 := range b {
		v2, ok := a[key]
		if !ok {
			result.Added = append(result.Added, v1)
		} else if !reflect.DeepEqual(v1, v2) {
			result.Deleted = append(result.Deleted, v1.Name)
			result.Added = append(result.Added, v1)
		}
	}
	for key, _ := range a {
		v2, ok := b[key]
		if !ok {
			result.Deleted = append(result.Deleted, v2.Name)
		}
	}
	return result
}

func (a HttpEndpointMap) Difference(b HttpEndpointMap) (HttpEndpointDifference) {
	result := HttpEndpointDifference{}
	for key, v1 := range b {
		v2, ok := a[key]
		if !ok {
			result.Added = append(result.Added, v1)
		} else if !reflect.DeepEqual(v1, v2) {
			result.Deleted = append(result.Deleted, v1.Name)
			result.Added = append(result.Added, v1)
		}
	}
	for key, _ := range a {
		v2, ok := b[key]
		if !ok {
			result.Deleted = append(result.Deleted, v2.Name)
		}
	}
	return result
}

func (a *BridgeConfig) Difference(b *BridgeConfig) (*BridgeConfigDifference) {
	result := BridgeConfigDifference {
		TcpConnectors:    a.TcpConnectors.Difference(b.TcpConnectors),
		TcpListeners:   a.TcpListeners.Difference(b.TcpListeners),
		HttpConnectors:   a.HttpConnectors.Difference(b.HttpConnectors),
		HttpListeners:  a.HttpListeners.Difference(b.HttpListeners),
	}
	return &result
}

func (a *TcpEndpointDifference) Empty() bool {
	return len(a.Deleted) == 0 && len(a.Added) == 0
}

func (a *HttpEndpointDifference) Empty() bool {
	return len(a.Deleted) == 0 && len(a.Added) == 0
}

func (a *BridgeConfigDifference) Empty() bool {
	return a.TcpConnectors.Empty() && a.TcpListeners.Empty() && a.HttpConnectors.Empty() && a.HttpListeners.Empty()
}

func (a *BridgeConfigDifference) Print() {
	log.Printf("TcpConnectors added=%v, deleted=%v", a.TcpConnectors.Added, a.TcpConnectors.Deleted)
	log.Printf("TcpListeners added=%v, deleted=%v", a.TcpListeners.Added, a.TcpListeners.Deleted)
	log.Printf("HttpConnectors added=%v, deleted=%v", a.HttpConnectors.Added, a.HttpConnectors.Deleted)
	log.Printf("HttpListeners added=%v, deleted=%v", a.HttpListeners.Added, a.HttpListeners.Deleted)
}
