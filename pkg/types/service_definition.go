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
	jsonencoding "encoding/json"

	"k8s.io/apimachinery/pkg/api/resource"
)
const (
	ProtocolTCP   string = "tcp"
	ProtocolHTTP  string = "http"
	ProtocolHTTP2 string = "http2"
)

type ServiceInterface struct {
	Address      string                   `json:"address" yaml:"address"`
	Protocol     string                   `json:"protocol" yaml:"protocol"`
	Ports        []int                    `json:"ports" yaml:"ports"`
	EventChannel bool                     `json:"eventchannel,omitempty" yaml:"eventchannel,omitempty"`
	Aggregate    string                   `json:"aggregate,omitempty" yaml:"aggregate,omitempty"`
	Headless     *Headless                `json:"headless,omitempty" yaml:"headless,omitempty"`
	Labels       map[string]string        `json:"labels,omitempty" yaml:"labels,omitempty"`
	Targets      []ServiceInterfaceTarget `json:"targets" yaml:"targets,omitempty"`
	Origin       string                   `json:"origin,omitempty" yaml:"origin,omitempty"`
}

type ServiceInterfaceList []ServiceInterface

func (sl *ServiceInterfaceList) ConvertFrom(updates string) error {
	err := jsonencoding.Unmarshal([]byte(updates), sl)
	portsDefined := len(*sl) == 0 || len((*sl)[0].Ports) > 0
	if err == nil && portsDefined {
		return nil
	}
	// Try converting from V1 format
	v1ServiceList := []ServiceInterfaceV1{}
	err = jsonencoding.Unmarshal([]byte(updates), &v1ServiceList)
	if err != nil {
		return err
	}
	// Converting from V1
	for i, v1Svc := range v1ServiceList {
		svc := &(*sl)[i]
		svc.Ports = []int{v1Svc.Port}
		// Converting Headless
		if v1Svc.Headless != nil {
			v1hl := v1Svc.Headless
			tPort := v1Svc.Port
			if v1hl.TargetPort > 0 {
				tPort = v1hl.TargetPort
			}
			svc.Headless.TargetPorts = map[int]int{
				v1Svc.Port: tPort,
			}
		}
		// Converting Targets
		if len(v1Svc.Targets) > 0 {
			for i, v1Target := range v1Svc.Targets {
				tPort := v1Svc.Port
				if v1Target.TargetPort > 0 {
					tPort = v1Target.TargetPort
				}
				svc.Targets[i].TargetPorts = map[int]int{
					v1Svc.Port: tPort,
				}
			}
		}
	}
	return nil
}

type ServiceInterfaceTarget struct {
	Name        string      `json:"name,omitempty"`
	Selector    string      `json:"selector,omitempty"`
	TargetPorts map[int]int `json:"targetPorts,omitempty"`
	Service     string      `json:"service,omitempty"`
}

func (s *ServiceInterfaceTarget) GetName() string {
	if s.Name != "" {
		return s.Name
	}
	if s.Selector != "" {
		return s.Selector
	}
	if s.Service != "" {
		return s.Service
	}
	return ""
}

type ServiceInterfaceV1 struct {
	Address      string                     `json:"address" yaml:"address"`
	Protocol     string                     `json:"protocol" yaml:"protocol"`
	Port         int                        `json:"port" yaml:"port"`
	EventChannel bool                       `json:"eventchannel,omitempty" yaml:"eventchannel,omitempty"`
	Aggregate    string                     `json:"aggregate,omitempty" yaml:"aggregate,omitempty"`
	Headless     *HeadlessV1                `json:"headless,omitempty" yaml:"headless,omitempty"`
	Labels       map[string]string          `json:"labels,omitempty" yaml:"labels,omitempty"`
	Targets      []ServiceInterfaceTargetV1 `json:"targets" yaml:"targets,omitempty"`
	Origin       string                     `json:"origin,omitempty" yaml:"origin,omitempty"`
}

type ServiceInterfaceTargetV1 struct {
	Name       string `json:"name,omitempty"`
	Selector   string `json:"selector,omitempty"`
	TargetPort int    `json:"targetPort,omitempty"`
	Service    string `json:"service,omitempty"`
}

func (service *ServiceInterface) AddTarget(target *ServiceInterfaceTarget) {
	modified := false
	targets := []ServiceInterfaceTarget{}
	for _, t := range service.Targets {
		if t.Name == target.Name {
			modified = true
			targets = append(targets, *target)
		} else {
			targets = append(targets, t)
		}
	}
	if !modified {
		targets = append(targets, *target)
	}
	service.Targets = targets
}

type Headless struct {
	Name          string             `json:"name" yaml:"name"`
	Size          int                `json:"size" yaml:"size"`
	TargetPorts   map[int]int        `json:"targetPorts,omitempty" yaml:"targetPorts,omitempty"`
	Affinity      map[string]string  `json:"affinity,omitempty" yaml:"affinity,omitempty"`
	AntiAffinity  map[string]string  `json:"antiAffinity,omitempty" yaml:"antiAffinity,omitempty"`
	NodeSelector  map[string]string  `json:"nodeSelector,omitempty" yaml:"nodeSelector,omitempty"`
	CpuRequest    *resource.Quantity `json:"cpuRequest,omitempty" yaml:"cpuRequest,omitempty"`
	MemoryRequest *resource.Quantity `json:"memoryRequest,omitempty" yaml:"memoryRequest,omitempty"`
	CpuLimit      *resource.Quantity `json:"cpuLimit,omitempty" yaml:"cpuLimit,omitempty"`
	MemoryLimit   *resource.Quantity `json:"memoryLimit,omitempty" yaml:"memoryLimit,omitempty"`
}

func (s *Headless) GetCpuRequest() resource.Quantity {
	if s.CpuRequest == nil {
		return resource.Quantity{}
	}
	return *s.CpuRequest
}

func (s *Headless) GetMemoryRequest() resource.Quantity {
	if s.MemoryRequest == nil {
		return resource.Quantity{}
	}
	return *s.MemoryRequest
}

func (s *Headless) GetCpuLimit() resource.Quantity {
	if s.CpuLimit == nil {
		return s.GetCpuRequest()
	}
	return *s.CpuLimit
}

func (s *Headless) GetMemoryLimit() resource.Quantity {
	if s.MemoryLimit == nil {
		return s.GetMemoryRequest()
	}
	return *s.MemoryLimit
}

func (s *Headless) HasCpuRequest() bool {
	return s.CpuRequest != nil
}

func (s *Headless) HasMemoryRequest() bool {
	return s.MemoryRequest != nil
}

func (s *Headless) HasCpuLimit() bool {
	return s.CpuLimit != nil || s.HasCpuRequest()
}

func (s *Headless) HasMemoryLimit() bool {
	return s.MemoryLimit != nil || s.HasMemoryRequest()
}

type HeadlessV1 struct {
	Name          string             `json:"name"`
	Size          int                `json:"size"`
	TargetPort    int                `json:"targetPort,omitempty"`
	Affinity      map[string]string  `json:"affinity,omitempty"`
	AntiAffinity  map[string]string  `json:"antiAffinity,omitempty"`
	NodeSelector  map[string]string  `json:"nodeSelector,omitempty"`
	CpuRequest    *resource.Quantity `json:"cpuRequest,omitempty"`
	MemoryRequest *resource.Quantity `json:"memoryRequest,omitempty"`
}

type ByServiceInterfaceAddress []ServiceInterface

func (a ByServiceInterfaceAddress) Len() int {
	return len(a)
}

func (a ByServiceInterfaceAddress) Less(i, j int) bool {
	return a[i].Address > a[i].Address
}

func (a ByServiceInterfaceAddress) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}
