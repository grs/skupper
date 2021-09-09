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
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/skupperproject/skupper/pkg/utils"
)

type RouterLogConfig struct {
	Module string
	Level  string
}

type Tuning struct {
	NodeSelector string
	Affinity     string
	AntiAffinity string
	Cpu          string
	Memory       string
}

type RouterOptions struct {
	Tuning
	Logging          []RouterLogConfig
	DebugMode        string
	MaxFrameSize     int
	MaxSessionFrames int
	IngressHost      string
}

type ControllerOptions struct {
	Tuning
	IngressHost string
}

type SiteConfig struct {
	Id                  string
	Name                string
	RouterMode          string
	EnableController    bool
	EnableServiceSync   bool
	EnableRouterConsole bool
	EnableConsole       bool
	AuthMode            string
	User                string
	Password            string
	Ingress             string
	ConsoleIngress      string
	IngressHost         string
	Replicas            int32
	CreateNetworkPolicy bool
	Annotations         map[string]string
	Labels              map[string]string
	Router              RouterOptions
	Controller          ControllerOptions
}

func (s *SiteConfig) GetConsoleUser() string {
	if s.User == "" {
		return "admin"
	}
	return s.User
}

func (s *SiteConfig) GetConsolePassword() string {
	if s.Password == "" {
		return utils.RandomId(10)
	}
	return s.Password
}

func (s *SiteConfig) IsInterior() bool {
	return s.RouterMode == TransportModeInterior
}

func (s *SiteConfig) IsEdge() bool {
	return s.RouterMode == TransportModeEdge
}

const (
	IngressRouteString        string = "route"
	IngressLoadBalancerString string = "loadbalancer"
	IngressNodePortString     string = "nodeport"
	IngressNginxIngressString string = "nginx-ingress-v1"
	IngressNoneString         string = "none"
)

func (s *SiteConfig) IsIngressRoute() bool {
	return s.Ingress == IngressRouteString
}
func (s *SiteConfig) IsIngressLoadBalancer() bool {
	return s.Ingress == IngressLoadBalancerString
}
func (s *SiteConfig) IsIngressNodePort() bool {
	return s.Ingress == IngressNodePortString
}
func (s *SiteConfig) IsIngressNginxIngress() bool {
	return s.Ingress == IngressNginxIngressString
}
func (s *SiteConfig) IsIngressNone() bool {
	return s.Ingress == IngressNoneString
}

func (s *SiteConfig) IsConsoleIngressRoute() bool {
	return s.getConsoleIngress() == IngressRouteString
}
func (s *SiteConfig) IsConsoleIngressLoadBalancer() bool {
	return s.getConsoleIngress() == IngressLoadBalancerString
}
func (s *SiteConfig) IsConsoleIngressNodePort() bool {
	return s.getConsoleIngress() == IngressNodePortString
}
func (s *SiteConfig) IsConsoleIngressNginxIngress() bool {
	return s.getConsoleIngress() == IngressNginxIngressString
}
func (s *SiteConfig) IsConsoleIngressNone() bool {
	return s.getConsoleIngress() == IngressNoneString
}
func (s *SiteConfig) getConsoleIngress() string {
	if s.ConsoleIngress == "" {
		return s.Ingress
	}
	return s.ConsoleIngress
}

func (s *SiteConfig) IsConsoleAuthOpenshift() bool {
	return s.AuthMode == ConsoleAuthModeOpenshift
}

func (s *SiteConfig) IsConsoleAuthInternal() bool {
	return s.AuthMode == ConsoleAuthModeInternal
}

func isValidIngress(ingress string) bool {
	return ingress == "" || ingress == IngressRouteString || ingress == IngressLoadBalancerString || ingress == IngressNodePortString || ingress == IngressNginxIngressString || ingress == IngressNoneString
}

func (s *SiteConfig) CheckIngress() error {
	if !isValidIngress(s.Ingress) {
		return fmt.Errorf("Invalid value for ingress: %s", s.Ingress)
	}
	return nil
}

func (s *SiteConfig) CheckConsoleIngress() error {
	if !isValidIngress(s.ConsoleIngress) {
		return fmt.Errorf("Invalid value for console-ingress: %s", s.ConsoleIngress)
	}
	return nil
}

func (s *SiteConfig) GetRouterIngressHost() string {
	if s.Router.IngressHost != "" {
		return s.Router.IngressHost
	}
	return s.IngressHost
}

func (s *SiteConfig) GetControllerIngressHost() string {
	if s.Controller.IngressHost != "" {
		return s.Controller.IngressHost
	}
	return s.IngressHost
}

const (
	//core options
	SiteConfigNameKey                string = "name"
	SiteConfigRouterModeKey          string = "router-mode"
	SiteConfigIngressKey             string = "ingress"
	SiteConfigIngressHostKey         string = "ingress-host"
	SiteConfigCreateNetworkPolicyKey string = "create-network-policy"

	//console options
	SiteConfigConsoleKey               string = "console"
	SiteConfigConsoleAuthenticationKey string = "console-authentication"
	SiteConfigConsoleUserKey           string = "console-user"
	SiteConfigConsolePasswordKey       string = "console-password"
	SiteConfigConsoleIngressKey        string = "console-ingress"

	//router options
	SiteConfigRouterConsoleKey          string = "router-console"
	SiteConfigRouterLoggingKey          string = "router-logging"
	SiteConfigRouterDebugModeKey        string = "router-debug-mode"
	SiteConfigRouterCpuKey              string = "router-cpu"
	SiteConfigRouterMemoryKey           string = "router-memory"
	SiteConfigRouterAffinityKey         string = "router-pod-affinity"
	SiteConfigRouterAntiAffinityKey     string = "router-pod-antiaffinity"
	SiteConfigRouterNodeSelectorKey     string = "router-node-selector"
	SiteConfigRouterMaxFrameSizeKey     string = "xp-router-max-frame-size"
	SiteConfigRouterMaxSessionFramesKey string = "xp-router-max-session-frames"
	SiteConfigRouterIngressHostKey      string = "router-ingress-host"

	//controller options
	SiteConfigServiceControllerKey      string = "service-controller"
	SiteConfigServiceSyncKey            string = "service-sync"
	SiteConfigControllerCpuKey          string = "controller-cpu"
	SiteConfigControllerMemoryKey       string = "controller-memory"
	SiteConfigControllerAffinityKey     string = "controller-pod-affinity"
	SiteConfigControllerAntiAffinityKey string = "controller-pod-antiaffinity"
	SiteConfigControllerNodeSelectorKey string = "controller-node-selector"
	SiteConfigControllerIngressHostKey  string = "controller-ingress-host"

	//defaults
	RouterMaxFrameSizeDefault           int    = 16384
	RouterMaxSessionFramesDefault       int    = 640

	//other constants
	AnnotationExcludes                  string = "skupper.io/exclude-annotations"
	LabelExcludes                       string = "skupper.io/exclude-labels"
	TransportModeInterior               string = "interior"
	TransportModeEdge                   string = "edge"
	ConsoleAuthModeOpenshift            string = "openshift"
	ConsoleAuthModeInternal             string = "internal"
	ConsoleAuthModeUnsecured            string = "unsecured"
)

func (config *SiteConfig) ReadFromMap(data map[string]string) error {
	if skupperName, ok := data[SiteConfigNameKey]; ok {
		config.Name = skupperName
	}
	if routerMode, ok := data[SiteConfigRouterModeKey]; ok {
		config.RouterMode = routerMode
	} else {
		// check for deprecated key
		if isEdge, ok := data["edge"]; ok {
			if isEdge == "true" {
				config.RouterMode = string(TransportModeEdge)
			} else {
				config.RouterMode = string(TransportModeInterior)
			}
		} else {
			config.RouterMode = string(TransportModeInterior)
		}
	}
	if enableController, ok := data[SiteConfigServiceControllerKey]; ok {
		config.EnableController, _ = strconv.ParseBool(enableController)
	} else {
		config.EnableController = true
	}
	if enableServiceSync, ok := data[SiteConfigServiceSyncKey]; ok {
		config.EnableServiceSync, _ = strconv.ParseBool(enableServiceSync)
	} else {
		config.EnableServiceSync = true
	}
	if enableConsole, ok := data[SiteConfigConsoleKey]; ok {
		config.EnableConsole, _ = strconv.ParseBool(enableConsole)
	} else {
		config.EnableConsole = true
	}
	if enableRouterConsole, ok := data[SiteConfigRouterConsoleKey]; ok {
		config.EnableRouterConsole, _ = strconv.ParseBool(enableRouterConsole)
	} else {
		config.EnableRouterConsole = false
	}
	if createNetworkPolicy, ok := data[SiteConfigCreateNetworkPolicyKey]; ok {
		config.CreateNetworkPolicy, _ = strconv.ParseBool(createNetworkPolicy)
	} else {
		config.CreateNetworkPolicy = false
	}
	if authMode, ok := data[SiteConfigConsoleAuthenticationKey]; ok {
		config.AuthMode = authMode
	} else {
		config.AuthMode = ConsoleAuthModeInternal
	}
	if user, ok := data[SiteConfigConsoleUserKey]; ok {
		config.User = user
	} else {
		config.User = ""
	}
	if password, ok := data[SiteConfigConsolePasswordKey]; ok {
		config.Password = password
	} else {
		config.Password = ""
	}
	if ingress, ok := data[SiteConfigIngressKey]; ok {
		config.Ingress = ingress
	} else {
		// check for deprecated key
		if clusterLocal, ok := data["cluster-local"]; ok {
			if clusterLocal == "true" {
				config.Ingress = IngressNoneString
			} else {
				config.Ingress = IngressLoadBalancerString
			}
		}
	}
	if consoleIngress, ok := data[SiteConfigConsoleIngressKey]; ok {
		config.ConsoleIngress = consoleIngress
	}
	if ingressHost, ok := data[SiteConfigIngressHostKey]; ok {
		config.IngressHost = ingressHost
	}
	if routerDebugMode, ok := data[SiteConfigRouterDebugModeKey]; ok && routerDebugMode != "" {
		config.Router.DebugMode = routerDebugMode
	}
	if routerLogging, ok := data[SiteConfigRouterLoggingKey]; ok && routerLogging != "" {
		logConf, err := ParseRouterLogConfig(routerLogging)
		if err != nil {
			return err
		}
		config.Router.Logging = logConf
	}
	if routerCpu, ok := data[SiteConfigRouterCpuKey]; ok && routerCpu != "" {
		config.Router.Cpu = routerCpu
	}
	if routerMemory, ok := data[SiteConfigRouterMemoryKey]; ok && routerMemory != "" {
		config.Router.Memory = routerMemory
	}
	if routerNodeSelector, ok := data[SiteConfigRouterNodeSelectorKey]; ok && routerNodeSelector != "" {
		config.Router.NodeSelector = routerNodeSelector
	}
	if routerAffinity, ok := data[SiteConfigRouterAffinityKey]; ok && routerAffinity != "" {
		config.Router.Affinity = routerAffinity
	}
	if routerAntiAffinity, ok := data[SiteConfigRouterAntiAffinityKey]; ok && routerAntiAffinity != "" {
		config.Router.AntiAffinity = routerAntiAffinity
	}
	if routerIngressHost, ok := data[SiteConfigRouterIngressHostKey]; ok && routerIngressHost != "" {
		config.Router.IngressHost = routerIngressHost
	}

	if routerMaxFrameSize, ok := data[SiteConfigRouterMaxFrameSizeKey]; ok && routerMaxFrameSize != "" {
		val, err := strconv.Atoi(routerMaxFrameSize)
		if err != nil {
			return err
		}
		config.Router.MaxFrameSize = val
	} else {
		config.Router.MaxFrameSize = RouterMaxFrameSizeDefault
	}
	if routerMaxSessionFrames, ok := data[SiteConfigRouterMaxSessionFramesKey]; ok && routerMaxSessionFrames != "" {
		val, err := strconv.Atoi(routerMaxSessionFrames)
		if err != nil {
			return err
		}
		config.Router.MaxSessionFrames = val
	} else {
		config.Router.MaxSessionFrames = RouterMaxSessionFramesDefault
	}

	if controllerCpu, ok := data[SiteConfigControllerCpuKey]; ok && controllerCpu != "" {
		config.Controller.Cpu = controllerCpu
	}
	if controllerMemory, ok := data[SiteConfigControllerMemoryKey]; ok && controllerMemory != "" {
		config.Controller.Memory = controllerMemory
	}
	if controllerNodeSelector, ok := data[SiteConfigControllerNodeSelectorKey]; ok && controllerNodeSelector != "" {
		config.Controller.NodeSelector = controllerNodeSelector
	}
	if controllerAffinity, ok := data[SiteConfigControllerAffinityKey]; ok && controllerAffinity != "" {
		config.Controller.Affinity = controllerAffinity
	}
	if controllerAntiAffinity, ok := data[SiteConfigControllerAntiAffinityKey]; ok && controllerAntiAffinity != "" {
		config.Controller.AntiAffinity = controllerAntiAffinity
	}
	if controllerIngressHost, ok := data[SiteConfigControllerIngressHostKey]; ok && controllerIngressHost != "" {
		config.Controller.IngressHost = controllerIngressHost
	}
	return nil
}

func (config *SiteConfig) ReadLabelsAndAnnotations(labels map[string]string, annotations map[string]string) {
	annotationExclusions := []string{}
	labelExclusions := []string{}
	config.Annotations = map[string]string{}
	for key, value := range annotations {
		if key == AnnotationExcludes {
			annotationExclusions = strings.Split(value, ",")
		} else if key == LabelExcludes {
			labelExclusions = strings.Split(value, ",")
		} else {
			config.Annotations[key] = value
		}
	}
	for _, key := range annotationExclusions {
		delete(config.Annotations, key)
	}
	config.Labels = map[string]string{}
	for key, value := range labels {
		config.Labels[key] = value
	}
	for _, key := range labelExclusions {
		delete(config.Labels, key)
	}
}

func (config *SiteConfig) WriteLabelsAndAnnotations(labels map[string]string, annotations map[string]string) {
	for k, v := range config.Labels {
		labels[k] = v
	}
	for k, v := range config.Annotations {
		annotations[k] = v
	}
}

func (config *SiteConfig) WriteToMap(data map[string]string) {
	if config.Name != "" {
		data[SiteConfigNameKey] = config.Name
	}
	if config.RouterMode != "" {
		data[SiteConfigRouterModeKey] = config.RouterMode
	}
	if !config.EnableController {
		data[SiteConfigServiceControllerKey] = "false"
	}
	if !config.EnableServiceSync {
		data[SiteConfigServiceSyncKey] = "false"
	}
	if !config.EnableConsole {
		data[SiteConfigConsoleKey] = "false"
	}
	if config.EnableRouterConsole {
		data[SiteConfigRouterConsoleKey] = "true"
	}
	if config.AuthMode != "" {
		data[SiteConfigConsoleAuthenticationKey] = config.AuthMode
	}
	if config.User != "" {
		data[SiteConfigConsoleUserKey] = config.User
	}
	if config.Password != "" {
		data[SiteConfigConsolePasswordKey] = config.Password
	}
	if config.Ingress != "" {
		data[SiteConfigIngressKey] = config.Ingress
	}
	if config.ConsoleIngress != "" {
		data[SiteConfigConsoleIngressKey] = config.ConsoleIngress
	}
	if config.IngressHost != "" {
		data[SiteConfigIngressHostKey] = config.IngressHost
	}
	if config.CreateNetworkPolicy {
		data[SiteConfigCreateNetworkPolicyKey] = "true"
	}
	if config.Router.Logging != nil {
		data[SiteConfigRouterLoggingKey] = RouterLogConfigToString(config.Router.Logging)
	}
	if config.Router.DebugMode != "" {
		data[SiteConfigRouterDebugModeKey] = config.Router.DebugMode
	}
	if config.Router.Cpu != "" {
		data[SiteConfigRouterCpuKey] = config.Router.Cpu
	}
	if config.Router.Memory != "" {
		data[SiteConfigRouterMemoryKey] = config.Router.Memory
	}
	if config.Router.Affinity != "" {
		data[SiteConfigRouterAffinityKey] = config.Router.Affinity
	}
	if config.Router.AntiAffinity != "" {
		data[SiteConfigRouterAntiAffinityKey] = config.Router.AntiAffinity
	}
	if config.Router.NodeSelector != "" {
		data[SiteConfigRouterNodeSelectorKey] = config.Router.NodeSelector
	}
	if config.Router.IngressHost != "" {
		data[SiteConfigRouterIngressHostKey] = config.Router.IngressHost
	}
	if config.Router.MaxFrameSize != RouterMaxFrameSizeDefault {
		data[SiteConfigRouterMaxFrameSizeKey] = strconv.Itoa(config.Router.MaxFrameSize)
	}
	if config.Router.MaxSessionFrames != RouterMaxSessionFramesDefault {
		data[SiteConfigRouterMaxSessionFramesKey] = strconv.Itoa(config.Router.MaxSessionFrames)
	}
	if config.Controller.Cpu != "" {
		data[SiteConfigControllerCpuKey] = config.Controller.Cpu
	}
	if config.Controller.Memory != "" {
		data[SiteConfigControllerMemoryKey] = config.Controller.Memory
	}
	if config.Controller.Affinity != "" {
		data[SiteConfigControllerAffinityKey] = config.Controller.Affinity
	}
	if config.Controller.AntiAffinity != "" {
		data[SiteConfigControllerAntiAffinityKey] = config.Controller.AntiAffinity
	}
	if config.Controller.NodeSelector != "" {
		data[SiteConfigControllerNodeSelectorKey] = config.Controller.NodeSelector
	}
	if config.Controller.IngressHost != "" {
		data[SiteConfigControllerIngressHostKey] = config.Controller.IngressHost
	}
}

func (config *SiteConfig) Verify() error {
	if _, err := resource.ParseQuantity(config.Router.Cpu); err != nil {
		return fmt.Errorf("Invalid value for %s %q: %s", SiteConfigRouterCpuKey, config.Router.Cpu, err)
	}
	if _, err := resource.ParseQuantity(config.Router.Memory); err != nil {
		return fmt.Errorf("Invalid value for %s %q: %s", SiteConfigRouterMemoryKey, config.Router.Memory, err)
	}
	if _, err := resource.ParseQuantity(config.Controller.Cpu); err != nil {
		return fmt.Errorf("Invalid value for %s %q: %s", SiteConfigControllerCpuKey, config.Controller.Cpu, err)
	}
	if _, err := resource.ParseQuantity(config.Controller.Memory); err != nil {
		return fmt.Errorf("Invalid value for %s %q: %s", SiteConfigControllerMemoryKey, config.Controller.Memory, err)
	}
	return nil
}

func DefaultSiteConfig() *SiteConfig {
	return &SiteConfig {
		RouterMode:          "interior",
		EnableController:    true,
		EnableServiceSync:   true,
		EnableRouterConsole: false,
		EnableConsole:       true,
	}
}

func RouterLogConfigToString(config []RouterLogConfig) string {
	items := []string{}
	for _, l := range config {
		if l.Module != "" && l.Level != "" {
			items = append(items, l.Module+":"+l.Level)
		} else if l.Level != "" {
			items = append(items, l.Level)
		}
	}
	return strings.Join(items, ",")
}

func ParseRouterLogConfig(config string) ([]RouterLogConfig, error) {
	items := strings.Split(config, ",")
	parsed := []RouterLogConfig{}
	for _, item := range items {
		parts := strings.Split(item, ":")
		var mod string
		var level string
		if len(parts) > 1 {
			mod = parts[0]
			level = parts[1]
		} else if len(parts) > 0 {
			level = parts[0]
		}
		err := checkLoggingModule(mod)
		if err != nil {
			return nil, err
		}
		err = checkLoggingLevel(level)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, RouterLogConfig{
			Module: mod,
			Level:  level,
		})
	}
	return parsed, nil
}

var LoggingModules []string = []string{
	"", /*implies DEFAULT*/
	"ROUTER",
	"ROUTER_CORE",
	"ROUTER_HELLO",
	"ROUTER_LS",
	"ROUTER_MA",
	"MESSAGE",
	"SERVER",
	"AGENT",
	"AUTHSERVICE",
	"CONTAINER",
	"ERROR",
	"POLICY",
	"HTTP",
	"CONN_MGR",
	"PYTHON",
	"PROTOCOL",
	"TCP_ADAPTOR",
	"HTTP_ADAPTOR",
	"DEFAULT",
}

var LoggingLevels []string = []string{
	"trace",
	"debug",
	"info",
	"notice",
	"warning",
	"error",
	"critical",
	"trace+",
	"debug+",
	"info+",
	"notice+",
	"warning+",
	"error+",
	"critical+",
}

func checkLoggingModule(mod string) error {
	for _, m := range LoggingModules {
		if mod == m {
			return nil
		}
	}
	return fmt.Errorf("Invalid logging module for router: %s", mod)
}

func checkLoggingLevel(level string) error {
	for _, l := range LoggingLevels {
		if level == l {
			return nil
		}
	}
	return fmt.Errorf("Invalid logging level for router: %s", level)
}

