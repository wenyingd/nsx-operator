/* Copyright © 2022-2023 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ParentType string

const (
	ParentTypeSubnets        ParentType = "subnets"
	ParentTypeSubnetSet      ParentType = "subnetSet"
	ParentTypeVirtualNetwork ParentType = "virtualNetwork"
	ParentTypeSegments       ParentType = "segments"
)

// ChildSubnetSpec defines the desired state of ChildSubnet.
type ChildSubnetSpec struct {
	// Workload cluster identifier.
	Parent string `json:"parent"`
	// IP version.
	// +kubebuilder:validation:Enum=ipv4;ipv6;dual
	IPVersion string `json:"ipVersion"`
	// Size of Subnet based upon estimated workload count.
	// +kubebuilder:validation:Maximum:=128
	// +kubebuilder:validation:Minimum:=1
	SubnetPrefixLength int `json:"SubnetPrefixLength,omitempty"`
	// Access mode of Subnet, accessible only from within VPC or from outside VPC.
	// +kubebuilder:validation:Enum=Private;Public
	AccessMode AccessMode `json:"accessMode,omitempty"`
	// Subnet advanced configuration.
	AdvancedConfig AdvancedConfig `json:"advancedConfig,omitempty"`
	// DHCPConfig DHCP configuration.
	DHCPConfig DHCPConfig `json:"DHCPConfig,omitempty"`
}

// ChildSubnetStatus defines the observed state of ChildSubnet.
type ChildSubnetStatus struct {
	NSXResourcePath string `json:"nsxResourcePath,omitempty"`
	// Subnet addresses. It is supposed to be one IPv4 address and IPv6 address at most. The format for each IPAddress
	// should be $gateway/$prefixLength
	IPAddresses []string    `json:"ipAddresses,omitempty"`
	Vlan        int64       `json:"vlan,omitempty"`
	Conditions  []Condition `json:"conditions,omitempty"`
}

// +genclient
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:storageversion

// ChildSubnet is the Schema for the childsubnets API.
// +kubebuilder:printcolumn:name="Claimed",type=integer,JSONPath=`.status.count.claimed`,description="The number of total claimed child subnets"
// +kubebuilder:printcolumn:name="Allocated",type=integer,JSONPath=`.status.count.allocated`,description="The number of successfully allocated child subnets"
type ChildSubnet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ChildSubnetSpec   `json:"spec,omitempty"`
	Status            ChildSubnetStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ChildSubnetList contains a list of ChildSubnet.
type ChildSubnetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChildSubnet `json:"items,omitempty"`
}

func init() {
	SchemeBuilder.Register(&ChildSubnet{}, &ChildSubnetList{})
}
