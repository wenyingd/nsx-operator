package childsubnet

import (
	"github.com/openlyinc/pointy"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware/vsphere-automation-sdk-go/runtime/data"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
)

func (service *ChildSubnetService) WrapHierarchyInfra(ipPool *model.IpAddressPool, ipSubnet *model.IpAddressPoolBlockSubnet, segment *model.Segment, bindingMaps []*model.SegmentConnectionBindingMap, tier1 *model.Tier1, nat *model.PolicyNat, natRules []*model.PolicyNatRule) (*model.Infra, error) {
	var infraChildren []*data.StructValue
	if ipPool != nil {
		IPPoolChildren, err := service.wrapIPPoolAndSubnets(ipPool, []*model.IpAddressPoolBlockSubnet{ipSubnet})
		if err != nil {
			return nil, err
		}
		infraChildren = append(infraChildren, IPPoolChildren)
	}
	if segment != nil {
		segmentChildren, err := service.wrapSegmentAndConnectionBindingMaps(segment, bindingMaps)
		if err != nil {
			return nil, err
		}
		infraChildren = append(infraChildren, segmentChildren)
	}
	if tier1 != nil && len(natRules) > 0 {
		tierChildren, err := service.wrapTier1AndNATRules(tier1, nat, natRules)
		if err != nil {
			return nil, err
		}
		infraChildren = append(infraChildren, tierChildren)
	}
	if len(infraChildren) == 0 {
		return nil, nil
	}
	return service.wrapInfra(infraChildren)
}

func (service *ChildSubnetService) WrapHierarchyIPPool(iap *model.IpAddressPool, iapbs *model.IpAddressPoolBlockSubnet) (*model.Infra, error) {
	IPPoolChildren, err := service.wrapIPPoolAndSubnets(iap, []*model.IpAddressPoolBlockSubnet{iapbs})
	if err != nil {
		return nil, err
	}
	return service.wrapInfra([]*data.StructValue{IPPoolChildren})
}

func (service *ChildSubnetService) WrapHierarchySegmentAndNAT(segment *model.Segment,
	bindingMaps []*model.SegmentConnectionBindingMap,
	tier1 *model.Tier1,
	nat *model.PolicyNat,
	natRules []*model.PolicyNatRule) (*model.Infra, error) {
	var infraChildren []*data.StructValue
	hChildSegmentBindingMaps, err := service.wrapSegmentAndConnectionBindingMaps(segment, bindingMaps)
	if err != nil {
		return nil, err
	}
	infraChildren = append(infraChildren, hChildSegmentBindingMaps)
	if tier1 != nil {
		hNatsRules, err := service.wrapTier1AndNATRules(tier1, nat, natRules)
		if err != nil {
			return nil, err
		}
		infraChildren = append(infraChildren, hNatsRules)
	}
	return service.wrapInfra(infraChildren)
}

func (service *ChildSubnetService) WrapHierarchyChildSegment(segment *model.Segment,
	bindingMaps []*model.SegmentConnectionBindingMap) (*model.Infra, error) {
	hChildSegmentBindingMaps, err := service.wrapSegmentAndConnectionBindingMaps(segment, bindingMaps)
	if err != nil {
		return nil, err
	}
	var infraChildren []*data.StructValue
	infraChildren = append(infraChildren, hChildSegmentBindingMaps)
	return service.wrapInfra(infraChildren)
}

func (service *ChildSubnetService) wrapInfra(children []*data.StructValue) (*model.Infra, error) {
	// This is the outermost layer of the hierarchy policy.
	// It doesn't need ID field.
	infraType := "Infra"
	infraObj := model.Infra{
		Children:     children,
		ResourceType: &infraType,
	}
	return &infraObj, nil
}

func (service *ChildSubnetService) wrapIPPoolAndSubnets(ipPool *model.IpAddressPool, ipSubnets []*model.IpAddressPoolBlockSubnet) (*data.StructValue, error) {
	IPSubnetsChildren, err := service.wrapIPSubnets(ipSubnets)
	if err != nil {
		return nil, err
	}
	ipPool.Children = append(ipPool.Children, IPSubnetsChildren...)
	IPPoolChildren, err := service.wrapIPPool(ipPool)
	if err != nil {
		return nil, err
	}
	return IPPoolChildren, nil
}

func (service *ChildSubnetService) wrapIPSubnets(IPSubnets []*model.IpAddressPoolBlockSubnet) ([]*data.StructValue, error) {
	var IPSubnetsChildren []*data.StructValue
	for _, IPSubnet := range IPSubnets {
		IPSubnet.ResourceType = common.ResourceTypeIPPoolBlockSubnet
		dataValue, errs := NewConverter().ConvertToVapi(IPSubnet, model.IpAddressPoolBlockSubnetBindingType())
		if len(errs) > 0 {
			return nil, errs[0]
		}
		childIPSubnet := model.ChildIpAddressPoolSubnet{
			ResourceType:        "ChildIpAddressPoolSubnet",
			Id:                  IPSubnet.Id,
			MarkedForDelete:     IPSubnet.MarkedForDelete,
			IpAddressPoolSubnet: dataValue.(*data.StructValue),
		}
		dataValue, errs = NewConverter().ConvertToVapi(childIPSubnet, model.ChildIpAddressPoolSubnetBindingType())
		if len(errs) > 0 {
			return nil, errs[0]
		}
		IPSubnetsChildren = append(IPSubnetsChildren, dataValue.(*data.StructValue))
	}
	return IPSubnetsChildren, nil
}

func (service *ChildSubnetService) wrapIPPool(iap *model.IpAddressPool) (*data.StructValue, error) {
	iap.ResourceType = pointy.String(common.ResourceTypeIPPool)
	childIPool := model.ChildIpAddressPool{
		Id:              iap.Id,
		MarkedForDelete: iap.MarkedForDelete,
		ResourceType:    "ChildIpAddressPool",
		IpAddressPool:   iap,
	}
	dataValue, errs := NewConverter().ConvertToVapi(childIPool, model.ChildIpAddressPoolBindingType())
	if len(errs) > 0 {
		return nil, errs[0]
	}
	return dataValue.(*data.StructValue), nil
}

func (service *ChildSubnetService) wrapSegmentAndConnectionBindingMaps(segment *model.Segment, bindingMaps []*model.SegmentConnectionBindingMap) (*data.StructValue, error) {
	bindingMapsChildren, err := service.wrapSegmentConnectionBindingMap(bindingMaps)
	if err != nil {
		return nil, err
	}
	if len(bindingMapsChildren) > 0 {
		segment.Children = append(segment.Children, bindingMapsChildren...)
	}
	segmentsChildren, err := service.wrapSegments(segment)
	if err != nil {
		return nil, err
	}
	return segmentsChildren, nil
}

func (service *ChildSubnetService) wrapSegmentConnectionBindingMap(bindingMaps []*model.SegmentConnectionBindingMap) ([]*data.StructValue, error) {
	var bindingMapsChildren []*data.StructValue
	for _, bindingMap := range bindingMaps {
		bindingMap.ResourceType = &common.ResourceTypeSegmentConnectionBindingMap
		childIPSubnet := model.ChildSegmentConnectionBindingMap{
			ResourceType:                "ChildSegmentConnectionBindingMap",
			Id:                          bindingMap.Id,
			MarkedForDelete:             bindingMap.MarkedForDelete,
			SegmentConnectionBindingMap: bindingMap,
		}
		dataValue, errs := NewConverter().ConvertToVapi(childIPSubnet, model.ChildSegmentConnectionBindingMapBindingType())
		if len(errs) > 0 {
			return nil, errs[0]
		}
		bindingMapsChildren = append(bindingMapsChildren, dataValue.(*data.StructValue))
	}
	return bindingMapsChildren, nil
}

func (service *ChildSubnetService) wrapSegments(segment *model.Segment) (*data.StructValue, error) {
	segment.ResourceType = pointy.String(common.ResourceTypeSegment)
	childSegment := model.ChildSegment{
		Id:              segment.Id,
		MarkedForDelete: segment.MarkedForDelete,
		ResourceType:    "ChildSegment",
		Segment:         segment,
	}
	dataValue, errs := NewConverter().ConvertToVapi(childSegment, model.ChildSegmentBindingType())
	if len(errs) > 0 {
		return nil, errs[0]
	}
	return dataValue.(*data.StructValue), nil
}

func (service *ChildSubnetService) wrapTier1AndNATRules(tier1 *model.Tier1, nat *model.PolicyNat, natRules []*model.PolicyNatRule) (*data.StructValue, error) {
	natRulesChildren, err := service.wrapPolicyNatRules(natRules)
	if err != nil {
		return nil, err
	}
	nat.Children = append(nat.Children, natRulesChildren...)
	natChildren, err := service.wrapPolicyNats(nat)
	if err != nil {
		return nil, err
	}
	tier1.Children = append(tier1.Children, natChildren)
	tier1Children, err := service.wrapPolicyTier1s(tier1)
	if err != nil {
		return nil, err
	}
	return tier1Children, nil
}

func (service *ChildSubnetService) wrapPolicyNatRules(rules []*model.PolicyNatRule) ([]*data.StructValue, error) {
	var natRulesChildren []*data.StructValue
	for _, natRule := range rules {
		natRule.ResourceType = &common.ResourceTypePolicyNATRule
		childIPSubnet := model.ChildPolicyNatRule{
			ResourceType:    "ChildPolicyNatRule",
			Id:              natRule.Id,
			MarkedForDelete: natRule.MarkedForDelete,
			PolicyNatRule:   natRule,
		}
		dataValue, errs := NewConverter().ConvertToVapi(childIPSubnet, model.ChildPolicyNatRuleBindingType())
		if len(errs) > 0 {
			return nil, errs[0]
		}
		natRulesChildren = append(natRulesChildren, dataValue.(*data.StructValue))
	}
	return natRulesChildren, nil
}

func (service *ChildSubnetService) wrapPolicyNats(nat *model.PolicyNat) (*data.StructValue, error) {
	nat.ResourceType = pointy.String(common.ResourceTypePolicyNAT)
	childPolicyNat := model.ChildPolicyNat{
		Id:              nat.Id,
		MarkedForDelete: nat.MarkedForDelete,
		ResourceType:    "ChildPolicyNat",
		PolicyNat:       nat,
	}
	dataValue, errs := NewConverter().ConvertToVapi(childPolicyNat, model.ChildPolicyNatBindingType())
	if len(errs) > 0 {
		return nil, errs[0]
	}
	return dataValue.(*data.StructValue), nil
}

func (service *ChildSubnetService) wrapPolicyTier1s(tier1 *model.Tier1) (*data.StructValue, error) {
	tier1.ResourceType = pointy.String(common.ResourceTypeTier1)
	childTier1 := model.ChildTier1{
		Id:              tier1.Id,
		MarkedForDelete: tier1.MarkedForDelete,
		ResourceType:    "ChildTier1",
		Tier1:           tier1,
	}
	dataValue, errs := NewConverter().ConvertToVapi(childTier1, model.ChildTier1BindingType())
	if len(errs) > 0 {
		return nil, errs[0]
	}
	return dataValue.(*data.StructValue), nil
}
