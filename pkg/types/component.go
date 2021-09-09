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
	"os"

	"github.com/skupperproject/skupper/pkg/qdr"
)

var Version = "undefined"

type ListenerType int

const (
	ListenerTypeNormal ListenerType = iota
	ListenerTypeInterRouter
	ListenerTypeEdge
	ListenerTypeConsole
	ListenerTypeProbe
	ListenerTypePrivate
)

type NamedPort struct {
	Name    string
	Port    int32
	Type    ListenerType
}

type ExposeScope int

const (
	ExposeScopeNone ExposeScope = iota
	ExposeScopeSite
	ExposeScopeConsole
)

type CertificateAuthority string

const (
	SiteCA  CertificateAuthority = "skupper-site-ca"
	LocalCA CertificateAuthority = "skupper-local-ca"
)

type Credential struct {
	Name   string
	Client bool
	CA     CertificateAuthority
}

type Service struct {
	Name         string
	Ports        []NamedPort
	Expose       ExposeScope
	Credentials  []Credential
}

type Config struct {
	Name      string
	Data      map[string]string
	MountPath string
}

type Proxy struct {
	PrivatePort    int32
	PublicPort     int32
	CredentialName string
}

type Component struct {
	Name          string
	Services      []Service
	Ports         []NamedPort
	Configs       []Config
	Environment   map[string]string
	ProbePath     string
	MountPath     string
	ImageDefault  string
	ImageKey      string
	PullPolicyKey string
	Proxy         *Proxy
}

func (s *Service) GetServerCredentialName() string {
	for _, c := range s.Credentials {
		if !c.Client {
			return c.Name
		}
	}
	return ""
}

func (s *Service) HasListenerTypeConsole() bool {
	for _, p := range s.Ports {
		if p.Type == ListenerTypeConsole {
			return true
		}
	}
	return false
}

func (c *Component) GetServerCredentialNames() []string {
	creds := []string{}
	for _, svc := range c.Services {
		cred := svc.GetServerCredentialName()
		if cred != "" {
			creds = append(creds, cred)
		}
	}
	return creds
}

func (c *Component) GetAllPorts() []NamedPort {
	ports := []NamedPort{}
	for _, svc := range c.Services {
		ports = append(ports, svc.Ports...)
	}
	for _, port := range c.Ports {
		ports = append(ports, port)
	}
	return ports
}

func (c *Component) GetConsolePortAndCredential() (*NamedPort, *Credential) {
	for _, svc := range c.Services {
		for _, port := range svc.Ports {
			if port.Type == ListenerTypeConsole {
				for _, c := range svc.Credentials {
					if !c.Client {
						return &port, &c
					}
				}
				return &port, nil
			}
		}
	}
	return nil, nil
}

func (c *Component) DeploymentName() string {
	return "skupper-" + c.Name
}

func (c *Component) IsRouter() bool {
	return c.Name == "router"
}

func IsInternalDeployment(name string) bool {
	router := Router()
	if router.DeploymentName() == name {
		return true
	}
	controller := Controller()
	if controller.DeploymentName() == name {
		return true
	}
	return false
}

func (c *Component) GetLabels(config *SiteConfig) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/name":    "skupper",
		"app.kubernetes.io/part-of": c.DeploymentName(),
		"skupper.io/component":      c.Name,
	}
	if c.IsRouter() {
		//needed by automeshing in image
		labels["application"] = c.DeploymentName()
	}
	for k, v := range config.Labels {
		labels[k] = v
	}
	return labels
}

func (c *Component) GetAnnotations(config *SiteConfig) map[string]string {
	annotations := map[string]string{}
	if c.IsRouter() {
		annotations = map[string]string{
			"prometheus.io/port":   "9090",
			"prometheus.io/scrape": "true",
		}
	}
	for k, v := range config.Annotations {
		annotations[k] = v
	}
	return annotations
}

func getEnvVar(key string, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func (component *Component) Image() string {
	return getEnvVar(component.ImageKey, component.ImageDefault)
}

func (component *Component) PullPolicy() string {
	return getEnvVar(component.PullPolicyKey, "Always")
}

func ConfiguredRouter(config *SiteConfig) Component {
	router := Router()
	router.Environment = map[string]string{
		"QDROUTERD_CONF":      router.MountPath + "skupper-internal/qdrouterd.json",
		"QDROUTERD_CONF_TYPE": "json",
		"SKUPPER_SITE_ID":     config.Id,
	}
	excludes := []ListenerType{}
	if config.IsEdge() {
		excludes = append(excludes, ListenerTypeInterRouter)
		excludes = append(excludes, ListenerTypeEdge)
	} else {
		router.Environment["APPLICATION_NAME"] = router.DeploymentName()
		router.Environment["QDROUTERD_AUTO_MESH_DISCOVERY"] = "QUERY"
	}
	if config.EnableRouterConsole {
		if config.IsConsoleAuthInternal() {
			router.Environment["QDROUTERD_AUTO_CREATE_SASLDB_SOURCE"] = router.MountPath + "skupper-console-users/"
			router.Environment["QDROUTERD_AUTO_CREATE_SASLDB_PATH"] = "/tmp/qdrouterd.sasldb"
			router.Configs = append(router.Configs, Config{
				Name:      "skupper-sasl-config",
				Data:      skupperSaslConfig(),
				MountPath: "/etc/sasl2/",
			})
		} else if config.IsConsoleAuthOpenshift() {
			port, cred := router.GetConsolePortAndCredential()
			if port != nil && cred != nil {
				router.Proxy = &Proxy {
					PrivatePort: 8888,
					PublicPort:  port.Port,
					CredentialName: cred.Name,
				}
			}
		}
	} else {
		excludes = append(excludes, ListenerTypeConsole)
	}
	router.Services = ExcludeListenersByType(router.Services, excludes)
	router.Configs = append(router.Configs, Config{
		Name: "skupper-internal",
		Data: GetInitialRouterConfig(config, &router),
	})

	return router
}

func skupperSaslConfig() map[string]string {
	config := `
pwcheck_method: auxprop
auxprop_plugin: sasldb
sasldb_path: /tmp/qdrouterd.sasldb
`
	return map[string]string{
		"qdrouterd.conf": config,
	}
}

func ConfiguredController(config *SiteConfig) Component {
	controller := Controller()
	excludes := []ListenerType{}
	if !config.EnableConsole {
		excludes = append(excludes, ListenerTypeConsole)
	} else if config.AuthMode == "internal" {
		controller.Configs = append(controller.Configs, Config{
			Name: "skupper-console-users",
			Data: map[string]string{
				config.GetConsoleUser(): config.GetConsolePassword(),
			},
		})
	}
	controller.Services = ExcludeListenersByType(controller.Services, excludes)
	return controller
}

func (a ListenerType) In(types []ListenerType) bool {
	for _, b := range types {
		if a == b {
			return true
		}
	}
	return false
}

func ExcludeListenersByType(in []Service, excludes []ListenerType) []Service {
	out := []Service{}
	for _, service := range in {
		ports := []NamedPort{}
		for _, port := range service.Ports {
			if !port.Type.In(excludes) {
				ports = append(ports, port)
			}
		}
		if len(ports) > 0 {
			service.Ports = ports
			out = append(out, service)
		}
	}
	return out
}

func Router() Component {
	return Component {
		Name:       "router",
		Services: []Service{
			{
				Name: "skupper-router-local",
				Ports: []NamedPort{
					{"amqps", 5671, ListenerTypeNormal},
				},
				Expose: ExposeScopeNone,
				Credentials: []Credential{
					{"skupper-local-client", true, LocalCA},
					{"skupper-local-server", false, LocalCA},
				},
			},
			{
				Name: "skupper-router",
				Ports: []NamedPort{
					{"inter-router", 55671, ListenerTypeInterRouter},
					{"edge", 45671, ListenerTypeEdge},
				},
				Expose: ExposeScopeSite,
				Credentials: []Credential{
					{"skupper-site-server", false, SiteCA},
				},
			},
			{
				Name: "skupper-router-console",
				Ports: []NamedPort{
					{"router-console", 8080, ListenerTypeConsole},
				},
				Expose: ExposeScopeConsole,
				Credentials: []Credential{
					{"skupper-router-console-certs", false, SiteCA},
				},
			},
		},
		Ports: []NamedPort{
			{"probe", 9090, ListenerTypeProbe},
			{"amqp", 5672, ListenerTypePrivate},
		},
		ProbePath:     "/healthz",
		MountPath:     "/etc/qpid-dispatch/mounts/",
		ImageDefault:  DefaultRouterImage,
		ImageKey:      "QDROUTERD_IMAGE",
		PullPolicyKey: "QDROUTERD_IMAGE_PULL_POLICY",
	}
}


func Controller() Component {
	return Component {
		Name:       "service-controller",
		Services: []Service{
			{
				Name: "skupper-claims",
				Ports: []NamedPort{
					{"claims", 8081, ListenerTypeNormal},
				},
				Expose: ExposeScopeConsole,
				Credentials: []Credential{
					{"skupper-claims-server", false, SiteCA},
				},
			},
			{
				Name: "skupper",
				Ports: []NamedPort{
					{"console", 8080, ListenerTypeConsole},
				},
				Expose: ExposeScopeConsole,
				Credentials: []Credential{
					{"skupper-console-certs", false, SiteCA},
				},
			},
		},
		Configs: []Config{
			Config{Name:"skupper-services"},
		},
	}
}

func GetInitialRouterConfig(options *SiteConfig, router *Component) map[string]string {
	routerConfig := qdr.InitialConfig(options.Name+"-${HOSTNAME}", options.Id, Version, options.IsEdge(), 3)
	if options.Router.Logging != nil {
		configureRouterLogging(&routerConfig, options.Router.Logging)
	}
	routerConfig.AddAddress(qdr.Address{
		Prefix:       "mc",
		Distribution: "multicast",
	})
	for _, port := range router.Ports {
		listener := qdr.Listener{
			Name: port.Name,
			Host: "0.0.0.0",
			Port: port.Port,
		}
		if port.Type == ListenerTypeProbe {
			listener.Http = true
			listener.HttpRootDir = "disabled"
			listener.Websockets = false
			listener.Healthz = true
			listener.Metrics = true
		} else if port.Type == ListenerTypePrivate {
			listener.Host = "localhost"
		}
		routerConfig.AddListener(listener)
	}
	for _, svc := range router.Services {
		profile := svc.GetServerCredentialName()
		if profile != "" {
			routerConfig.AddSslProfileWithPath(router.MountPath, qdr.SslProfile{
				Name: profile,
			})
		}
		for _, port := range svc.Ports {
			listener := qdr.Listener{
				Name: port.Name,
				Host: "0.0.0.0",
				Port: port.Port,
			}
			if profile != "" {
				listener.SslProfile = profile
				listener.SaslMechanisms = "EXTERNAL"
				listener.AuthenticatePeer = true
			}
			if port.Type == ListenerTypeConsole {
				listener.Http = true
				if options.AuthMode == ConsoleAuthModeInternal {
					listener.AuthenticatePeer = true
					listener.SaslMechanisms = ""
				} else if options.AuthMode == ConsoleAuthModeOpenshift {
					//when using oauth proxy, the console listener listens without
					//ssl or authentication on localhost so it is only available to
					//the proxy
					listener.Host = "localhost"
					listener.Port = router.Proxy.PrivatePort
					listener.SslProfile = ""
					listener.SaslMechanisms = ""
					listener.AuthenticatePeer = false
				}
			} else if port.Type == ListenerTypeInterRouter {
				listener.Role = qdr.RoleInterRouter
				listener.SetMaxFrameSize(options.Router.MaxFrameSize)
				listener.SetMaxSessionFrames(options.Router.MaxSessionFrames)
			} else if port.Type == ListenerTypeEdge {
				listener.Role = qdr.RoleEdge
				listener.SetMaxFrameSize(options.Router.MaxFrameSize)
				listener.SetMaxSessionFrames(options.Router.MaxSessionFrames)
			}
			routerConfig.AddListener(listener)
		}
	}
	result, _ := qdr.MarshalRouterConfig(routerConfig)
	return qdr.AsConfigMapData(result)
}

func configureRouterLogging(routerConfig *qdr.RouterConfig, logConfig []RouterLogConfig) bool {
	levels := map[string]string{}
	for _, l := range logConfig {
		levels[l.Module] = l.Level
	}
	return routerConfig.SetLogLevels(levels)
}

