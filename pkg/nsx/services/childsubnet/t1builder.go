package childsubnet

import (
	"fmt"
	"github.com/vmware-tanzu/nsx-operator/pkg/apis/v1alpha1"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/ippool"
	"github.com/vmware-tanzu/nsx-operator/pkg/util"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	vnet "gitlab.eng.vmware.com/core-build/nsx-ujo/k8s-virtual-networking-client/pkg/apis/k8svirtualnetworking/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"net"
	"strings"
)

const (
	ipPoolPPrefix                     = "ipc"
	ipPoolSubnetPrefix                = "ibs"
	childSegmentPrefix                = "cs"
	segmentConnectionBindingMapPrefix = "scbm"
	policyNATRulePrefix               = "pnr"
	policyNATPrefix                   = "pn"
	defaultNAT                        = "DEFAULT"
)

func (service *ChildSubnetService) buildIPPoolWithSubnets(childSubnet *v1alpha1.ChildSubnet, ipBlockPath *string, basicTags []model.Tag) (*model.IpAddressPool, *model.IpAddressPoolBlockSubnet) {
	id := service.buildIPPoolID(childSubnet)
	name := service.buildIPPoolName(childSubnet)
	ipPool := ippool.BuildIPPool(common.String(id), common.String(name), basicTags)
	return ipPool, service.buildIPSubnet(childSubnet, ipBlockPath, basicTags)
}

func (service *ChildSubnetService) buildIPPoolID(childSubnet *v1alpha1.ChildSubnet) string {
	return util.GenerateID(string(childSubnet.UID), ipPoolPPrefix, "", "")
}

func (service *ChildSubnetService) buildIPPoolName(childSubnet *v1alpha1.ChildSubnet) string {
	return util.GenerateDisplayName(childSubnet.Name, ipPoolPPrefix, "", "", "")
}

func (service *ChildSubnetService) buildIPPoolIntentPath(childSubnet *v1alpha1.ChildSubnet) string {
	return strings.Join([]string{"/infra/ip-pools", service.buildIPPoolID(childSubnet)}, "/")
}

func (service *ChildSubnetService) buildIPSubnetID(childSubnet *v1alpha1.ChildSubnet) string {
	return util.GenerateID(string(childSubnet.UID), ipPoolSubnetPrefix, "", "")
}

func (service *ChildSubnetService) buildIPSubnetName(childSubnet *v1alpha1.ChildSubnet) string {
	return util.GenerateDisplayName(childSubnet.Name, ipPoolSubnetPrefix, "", "", "")
}

func (service *ChildSubnetService) buildIPSubnetIntentPath(childSubnet *v1alpha1.ChildSubnet) string {
	return strings.Join([]string{"/infra/ip-pools", service.buildIPPoolID(childSubnet),
		"ip-subnets", service.buildIPSubnetID(childSubnet)}, "/")
}

func (service *ChildSubnetService) buildIPSubnet(childSubnet *v1alpha1.ChildSubnet, ipBlockPath *string, basicTags []model.Tag) *model.IpAddressPoolBlockSubnet {
	id := common.String(service.buildIPSubnetID(childSubnet))
	name := common.String(service.buildIPSubnetName(childSubnet))
	size := common.Int64(util.CalculateSubnetSize(childSubnet.Spec.SubnetPrefixLength))
	return ippool.BuildIPSubnet(id, name, ipBlockPath, basicTags, size)
}

func (service *ChildSubnetService) buildSegment(childSubnet *v1alpha1.ChildSubnet, parentConfig *ParentConfig, ipPoolPath string, subnetGateways []*net.IPNet, tags []model.Tag) *model.Segment {
	id := common.String(service.buildSegmentID(childSubnet))
	name := common.String(service.buildSegmentName(childSubnet))
	t1Path := common.String(parentConfig.tier1Path)
	tzPath := common.String(parentConfig.transportZonePath)
	return BuildSegment(id, name, t1Path, tzPath, subnetGateways, ipPoolPath, tags)
}

func (service *ChildSubnetService) buildSegmentIntentPath(childSubnet *v1alpha1.ChildSubnet) string {
	return strings.Join([]string{"/infra/segments", service.buildSegmentID(childSubnet)}, "/")
}

func (service *ChildSubnetService) buildSegmentID(childSubnet *v1alpha1.ChildSubnet) string {
	return util.GenerateID(string(childSubnet.UID), childSegmentPrefix, "", "")
}

func (service *ChildSubnetService) buildSegmentName(childSubnet *v1alpha1.ChildSubnet) string {
	return util.GenerateDisplayName(childSubnet.Name, childSegmentPrefix, "", "", "")
}

func (service *ChildSubnetService) buildSegmentConnectionBindingMaps(childSubnet *v1alpha1.ChildSubnet, parentConfig *ParentConfig, vlanTag int64, tags []model.Tag) []*model.SegmentConnectionBindingMap {
	parentPaths := parentConfig.segmentPaths
	bindingMaps := make([]*model.SegmentConnectionBindingMap, len(parentPaths))
	bindMapTags := append(tags, model.Tag{
		Scope: common.String(common.TagScopeParentConfigUID),
		Tag:   common.String(parentConfig.id),
	})
	for parentPath := range parentPaths {
		parentID := getSegmentIDFromPath(parentPath)
		id := common.String(service.buildSegmentBindingMapID(childSubnet, parentID))
		name := common.String(service.buildSegmentBindingMapName(childSubnet, parentID))
		bindingMap := BuildSegmentConnectionBindingMap(id, name, common.String(parentPath), vlanTag, bindMapTags)
		bindingMaps = append(bindingMaps, bindingMap)
	}
	return bindingMaps
}

func getSegmentIDFromPath(path string) string {
	items := strings.Split(path, "/")
	return items[len(items)-1]
}

func (service *ChildSubnetService) buildSegmentBindingMapID(childSubnet *v1alpha1.ChildSubnet, parentID string) string {
	return util.GenerateID(string(childSubnet.UID), segmentConnectionBindingMapPrefix, parentID, "")
}

func (service *ChildSubnetService) buildSegmentBindingMapName(childSubnet *v1alpha1.ChildSubnet, parentID string) string {
	return util.GenerateDisplayName(childSubnet.Name, segmentConnectionBindingMapPrefix, parentID, "", "")
}

func (service *ChildSubnetService) buildPolicySNATRules(childSubnet *v1alpha1.ChildSubnet, subnetNetworks []*net.IPNet, tags []model.Tag) []*model.PolicyNatRule {
	snatAction := common.String(model.PolicyNatRule_ACTION_SNAT)
	if string(childSubnet.Spec.AccessMode) == v1alpha1.AccessModePublic {
		snatAction = common.String(model.PolicyNatRule_ACTION_NO_SNAT)
	}
	natRules := make([]*model.PolicyNatRule, 0)
	for i := range subnetNetworks {
		indexBase := i * 2
		networkCDIR := subnetNetworks[i]
		natRules = append(natRules,
			BuildNATRules(
				common.String(service.buildPolicyNATRuleID(childSubnet, indexBase)),
				common.String(service.buildPolicyNATRuleName(childSubnet, indexBase)),
				snatAction, networkCDIR, true, tags),
			BuildNATRules(
				common.String(service.buildPolicyNATRuleID(childSubnet, indexBase+1)),
				common.String(service.buildPolicyNATRuleName(childSubnet, indexBase+1)),
				snatAction, networkCDIR, false, tags),
		)
	}
	return natRules
}

func (service *ChildSubnetService) buildPolicyNATRuleID(childSubnet *v1alpha1.ChildSubnet, index int) string {
	return util.GenerateID(string(childSubnet.UID), policyNATRulePrefix, "", fmt.Sprintf("%d", index))
}

func (service *ChildSubnetService) buildPolicyNATRuleName(childSubnet *v1alpha1.ChildSubnet, index int) string {
	return util.GenerateDisplayName(childSubnet.Name, policyNATRulePrefix, fmt.Sprintf("%d", index), "", "")
}

func (service *ChildSubnetService) buildPolicyNATID(childSubnet *v1alpha1.ChildSubnet) string {
	return util.GenerateID(childSubnet.Namespace, policyNATPrefix, "", "")
}

func (service *ChildSubnetService) buildPolicyNATName(childSubnet *v1alpha1.ChildSubnet) string {
	return util.GenerateDisplayName(childSubnet.Name, policyNATPrefix, "", "", "")
}

func (service *ChildSubnetService) buildParentConfigByVNet(vnet *vnet.VirtualNetwork) (*ParentConfig, error) {
	var namespaceID types.UID
	segments, err := service.parentSegmentStore.listByParent(vnet.UID)
	if err != nil {
		log.Error(err, "failed to list segments by VirtualNetwork", "vnet", vnet.UID)
		return nil, err
	}
	if len(segments) == 0 {
		log.Info("No segments exist for VirtualNetwork", "vnet", vnet.UID)
		return nil, nil
	}
	ipBlock, err := service.ipBlockStore.GetBySupervisorCluster(service.getCluster())
	if err != nil {
		return nil, err
	}
	pc := &ParentConfig{
		id:        string(vnet.UID),
		name:      vnet.Name,
		namespace: vnet.Namespace,
	}
	for _, segment := range segments {
		pc.segmentPaths.Insert(*(segment.Path))
		if pc.tier1Path == "" {
			pc.tier1Path = *(segment.ConnectivityPath)
		}
		if pc.transportZonePath == "" {
			pc.transportZonePath = *(segment.TransportZonePath)
		}
	}
	if !service.vpcEnabled {
		pc.setIPBlockPaths(ipBlock, ipBlock)
	}

	// Update privateIPBlockPath/publicIPBlockPath with the IP Block configured on the Namespace if exists.
	if pc.tier1Path != "" {
		tier1, err := service.tier1Store.getByPolicyPath(pc.tier1Path)
		if err != nil {
			log.Info("No tier-1 exist for VirtualNetwork", "vnet", vnet.UID, "path", pc.tier1Path)
			return nil, err
		}
		if tier1 != nil {
			namespaceID, err = parseNamespaceIDFromTier1(tier1)
			if err != nil {
				return nil, err
			}
			namespacedIPBlocks, err := service.ipBlockStore.GetByNamespace(namespaceID)
			if err != nil {
				log.Error(err, "failed to find IPBlock by namespace ID", "namespace", namespaceID)
				return nil, err
			}
			if len(namespacedIPBlocks) > 0 {
				if service.vpcEnabled {
					pc.setIPBlockPaths(namespacedIPBlocks[0], namespacedIPBlocks[0])
				}
			}
		}
	}
	return pc, nil
}

func parseNamespaceIDFromTier1(tier1 *model.Tier1) (types.UID, error) {
	for _, tag := range tier1.Tags {
		if *tag.Scope == common.TagScopeNCPProjectUID {
			return types.UID(*tag.Tag), nil
		}
	}
	return "", fmt.Errorf("unable to find Namespace ID from tier1 %s", *(tier1.Path))
}

func BuildSegment(id, name, connectivityPath, tzPath *string, gateways []*net.IPNet, ipPoolPath string, tags []model.Tag) *model.Segment {
	subnets := make([]model.SegmentSubnet, len(gateways))
	for i := range gateways {
		subnets[i] = model.SegmentSubnet{
			GatewayAddress: common.String(gateways[i].String()),
		}
	}
	advancedConfig := &model.SegmentAdvancedConfig{AddressPoolPaths: []string{ipPoolPath}}
	return &model.Segment{
		Id:                id,
		DisplayName:       name,
		ConnectivityPath:  connectivityPath,
		Subnets:           subnets,
		AdvancedConfig:    advancedConfig,
		Tags:              tags,
		TransportZonePath: tzPath,
	}
}

func BuildSegmentConnectionBindingMap(id, name, parentPath *string, vlanTag int64, tags []model.Tag) *model.SegmentConnectionBindingMap {
	return &model.SegmentConnectionBindingMap{
		Id:             id,
		DisplayName:    name,
		SegmentPath:    parentPath,
		VlanTrafficTag: common.Int64(vlanTag),
		Tags:           tags,
	}
}

func BuildDefaultSNAT() *model.PolicyNat {
	return &model.PolicyNat{
		Id:          common.String(defaultNAT),
		DisplayName: common.String(defaultNAT),
		NatType:     common.String(model.PolicyNat_NAT_TYPE_DEFAULT),
	}
}

func BuildNATRules(id, name, natAction *string, networkCIDR *net.IPNet, isSrc bool, tags []model.Tag) *model.PolicyNatRule {
	rule := &model.PolicyNatRule{
		Id:          id,
		DisplayName: name,
		Action:      natAction,
		Tags:        tags,
	}
	network := common.String(networkCIDR.String())
	if isSrc {
		rule.SourceNetwork = network
	} else {
		rule.DestinationNetwork = network
	}
	return rule
}
