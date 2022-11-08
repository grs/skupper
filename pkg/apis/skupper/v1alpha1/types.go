package v1alpha1

import (
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:noStatus
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SkupperClusterPolicy defines optional cluster level policies
type SkupperClusterPolicy struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata,omitempty"`
	Spec          SkupperClusterPolicySpec `json:"spec,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SkupperClusterPolicyList contains a List of SkupperClusterPolicy
type SkupperClusterPolicyList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata,omitempty"`
	Items       []SkupperClusterPolicy `json:"items"`
}

type SkupperClusterPolicySpec struct {
	Namespaces                    []string `json:"namespaces"`
	AllowIncomingLinks            bool     `json:"allowIncomingLinks"`
	AllowedOutgoingLinksHostnames []string `json:"allowedOutgoingLinksHostnames"`
	AllowedExposedResources       []string `json:"allowedExposedResources"`
	AllowedServices               []string `json:"allowedServices"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Site defines the location and configuration of a skupper site
type Site struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata,omitempty"`
	Spec          SiteSpec `json:"spec,omitempty"`
	Status        SiteStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SiteList contains a List of Site instances
type SiteList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata,omitempty"`
	Items       []Site `json:"items"`
}

type SiteSpec struct {
	ServiceAccount string `json:"serviceAccount,omitempty"`
	Settings []NamedValue `json:"settings,omitempty"`
}

type SiteStatus struct {
	Addresses []Address `json:"addresses,omitempty"`
}

type Address struct {
	Name  string `json:"name"`
	Host  string `json:"host,omitempty"`
	Port  string `json:"port,omitempty"`
}

type NamedValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type RequiredService struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata,omitempty"`
	Spec          RequiredServiceSpec `json:"spec,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RequiredServiceList contains a List of RequiredService instances
type RequiredServiceList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata,omitempty"`
	Items       []RequiredService `json:"items"`
}

type RequiredServiceSpec struct {
	Address string        `json:"address"`
	Ports   []ServicePort `json:"ports,omitempty"`
}

type ServicePort struct {
	Name    string `json:"name"`
	Port    int    `json:"port"`
}
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type ProvidedService struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata,omitempty"`
	Spec          ProvidedServiceSpec `json:"spec,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ProvidedServiceList contains a List of ProvidedService instances
type ProvidedServiceList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata,omitempty"`
	Items       []ProvidedService `json:"items"`
}

type ProvidedServiceSpec struct {
	Selector map[string]string `json:"selector,omitempty"`
	Host     string            `json:"host,omitempty"`
	Address  string            `json:"address"`
	Ports    []ServicePort     `json:"ports,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ServiceGroup struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ServicegroupList contains a List of Servicegroup instances
type ServiceGroupList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata,omitempty"`
	Items       []ServiceGroup `json:"items"`
}
