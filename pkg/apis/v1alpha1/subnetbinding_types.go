package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type SubnetBindingSpec struct {
	// Type of the parent resource of ChildSubnet.
	// +kubebuilder:validation:Enum=subnets;subnetSet;virtualNetwork;segments
	Type ParentType `json:"type"`
	// Name of the "virtualNetwork" or "subnetSet" resource when Type is "virtualNetwork" or "subnetSet".
	// Name is empty if Type is "segments" or "subnets"
	Name string `json:"name,omitempty"`
	// Subnets is a list of the existing subnets in the cluster. It is set only when Type is "subnets".
	Subnets []string `json:"subnets,omitempty"`
	// Segments is a list of policy paths for the existing segments. It is set only when Type is "segments".
	Segments []string `json:"segments,omitempty"`
	// Vlan is the vlan tag configured in the binding. It can be empty, then the handler will choose a valid
	// value based on the existing configurations on the given parent.
	Vlan int64 `json:"vlan,omitempty"`
}

// SubnetBindingStatus defines the observed state of SubnetBinding.
type SubnetBindingStatus struct {
	Vlan       int64       `json:"vlan,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}

// +genclient
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:storageversion

// SubnetBinding is the Schema for the parentConfig API.
type SubnetBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SubnetBindingSpec   `json:"spec,omitempty"`
	Status            SubnetBindingStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SubnetBindingList contains a list of SubnetBinding.
type SubnetBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubnetBinding `json:"items,omitempty"`
}

func init() {
	SchemeBuilder.Register(&SubnetBinding{}, &SubnetBindingList{})
}
