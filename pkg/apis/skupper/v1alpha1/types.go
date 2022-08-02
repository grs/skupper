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

type IngressBinding struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata,omitempty"`
	Spec          IngressBindingSpec `json:"spec,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// IngressBindingList contains a List of IngressBinding instances
type IngressBindingList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata,omitempty"`
	Items       []IngressBinding `json:"items"`
}

type IngressBindingSpec struct {
	Address string        `json:"address"`
	Ports   []ServicePort `json:"ports,omitempty"`
}

type ServicePort struct {
	Name    string `json:"name"`
	Port    int    `json:"port"`
}
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type EgressBinding struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata,omitempty"`
	Spec          EgressBindingSpec `json:"spec,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// EgressBindingList contains a List of EgressBinding instances
type EgressBindingList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata,omitempty"`
	Items       []EgressBinding `json:"items"`
}

type EgressBindingSpec struct {
	Selector map[string]string `json:"selector,omitempty"`
	Host     string            `json:"host,omitempty"`
	Address  string            `json:"address"`
	Ports    []ServicePort     `json:"ports,omitempty"`
}
