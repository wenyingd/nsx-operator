package childsubnet

import (
	"fmt"
	"github.com/vmware-tanzu/nsx-operator/pkg/apis/v1alpha1"
	"github.com/vmware-tanzu/nsx-operator/pkg/logger"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	nsxutil "github.com/vmware-tanzu/nsx-operator/pkg/nsx/util"
	"github.com/vmware-tanzu/nsx-operator/pkg/util"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	vnet "gitlab.eng.vmware.com/core-build/nsx-ujo/k8s-virtual-networking-client/pkg/apis/k8svirtualnetworking/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	log                       = logger.Log
	MarkedForDelete           = true
	EnforceRevisionCheckParam = false
	NewConverter              = common.NewConverter
)

type ChildSubnetService struct {
	clusterName string
	common.Service
	ipBlockStore              *IPBlockStore
	ipPoolStore               *IPPoolStore
	ipBlockSubnetStore        *IPPoolBlockSubnetStore
	childSegmentStore         *SegmentStore
	parentSegmentStore        *SegmentStore
	connectionBindingMapStore *SegmentConnectionBindingMapStore
	tier1Store                *Tier1Store
	natRuleStore              *NATRuleStore
	vpcEnabled                bool
	parentConfigStore         *ParentConfigStore
	exhaustedIPBlock          sets.Set[string]
}

func InitializeChildSubnet(service common.Service) (*ChildSubnetService, error) {
	wg := sync.WaitGroup{}
	wgDone := make(chan bool)
	fatalErrors := make(chan error)

	wg.Add(8)

	childSubnetService := &ChildSubnetService{
		Service:                   service,
		ipBlockStore:              newIPBlockStore(),
		ipPoolStore:               newIPPoolStore(),
		ipBlockSubnetStore:        newIPPoolBlockSubnetStore(),
		childSegmentStore:         newSegmentStore(false),
		parentSegmentStore:        newSegmentStore(true),
		connectionBindingMapStore: newSegmentConnectionBindingMapStore(),
		tier1Store:                newTier1Store(),
		natRuleStore:              newNATRuleStore(),
		parentConfigStore:         newParentConfigStore(),
	}

	go childSubnetService.InitializeCommonStore(&wg, fatalErrors, "", "", common.ResourceTypeIPBlock, childSubnetService.ipBlockStore.getInitTags(), childSubnetService.ipBlockStore, false)
	go childSubnetService.InitializeResourceStore(&wg, fatalErrors, common.ResourceTypeIPPool, childSubnetService.ipPoolStore.getInitTags(), childSubnetService.ipPoolStore)
	go childSubnetService.InitializeResourceStore(&wg, fatalErrors, common.ResourceTypeIPPoolBlockSubnet, childSubnetService.ipBlockSubnetStore.getInitTags(), childSubnetService.ipBlockSubnetStore)
	go childSubnetService.InitializeResourceStore(&wg, fatalErrors, common.ResourceTypeSegment, childSubnetService.childSegmentStore.getInitTags(), childSubnetService.childSegmentStore)
	go childSubnetService.InitializeCommonStore(&wg, fatalErrors, "", "", common.ResourceTypeSegment, childSubnetService.parentSegmentStore.getInitTags(), childSubnetService.parentSegmentStore, false)
	go childSubnetService.InitializeResourceStore(&wg, fatalErrors, common.ResourceTypeSegmentConnectionBindingMap, childSubnetService.connectionBindingMapStore.getInitTags(), childSubnetService.connectionBindingMapStore)
	go childSubnetService.InitializeCommonStore(&wg, fatalErrors, "", "", common.ResourceTypeTier1, childSubnetService.tier1Store.getInitTags(), childSubnetService.tier1Store, false)
	go childSubnetService.InitializeResourceStore(&wg, fatalErrors, common.ResourceTypePolicyNATRule, childSubnetService.natRuleStore.getInitTags(), childSubnetService.natRuleStore)

	go func() {
		wg.Wait()
		close(wgDone)
	}()
	select {
	case <-wgDone:
		break
	case err := <-fatalErrors:
		close(fatalErrors)
		return childSubnetService, err
	}
	return childSubnetService, nil
}

func (service *ChildSubnetService) CreateOrUpdateChildSubnet(childSubnet *v1alpha1.ChildSubnet) (bool, error) {
	parentConfig, err := service.getParentConfig(childSubnet)
	if err != nil {
		return false, err
	}

	basicTags := util.BuildBasicTags(service.getCluster(), childSubnet, "")
	childSegment, err := service.childSegmentStore.getByChildSubnet(childSubnet.UID)
	if err != nil {
		log.Error(err, "failed to find child segments by ChildSubnet", "id", childSubnet.UID)
		return false, err
	}
	if childSegment != nil {
		return true, service.updateChildSubnetBindingMaps(childSubnet, parentConfig, childSegment, basicTags)
	}

	childPath, gwNet, vlan, err := service.createChildSubnets(childSubnet, parentConfig, basicTags)
	if err != nil {
		return false, err
	}
	childSubnet.Status.NSXResourcePath = childPath
	childSubnet.Status.IPAddresses = append(childSubnet.Status.IPAddresses, gwNet.String())
	childSubnet.Status.Vlan = vlan
	return true, nil
}

func (service *ChildSubnetService) DeleteChildSubnet(childSubnet *v1alpha1.ChildSubnet) error {
	childSegment, err := service.childSegmentStore.getByChildSubnet(childSubnet.UID)
	if err != nil {
		return err
	}
	if childSegment == nil {
		log.Info("No child segment exists for ChildSubnet", "id", childSubnet)
		return nil
	}
	childSegment.MarkedForDelete = &MarkedForDelete
	bindingMaps, err := service.connectionBindingMapStore.listByChildSubnet(childSubnet.UID)
	for _, bindingMap := range bindingMaps {
		bindingMap.MarkedForDelete = &MarkedForDelete
	}
	parentConfig, err := service.getParentConfig(childSubnet)
	if err != nil {
		return err
	}
	tier1, err := service.getTier1ByParent(childSubnet.UID, parentConfig)
	if err != nil {
		log.Error(err, "failed to find valid tier1 for ChildSubnet", "id", childSubnet.UID)
		return err
	}
	natRules, err := service.natRuleStore.GetNATRulesByChildSubnet(childSubnet.UID)
	if err != nil {
		log.Error(err, "failed to find NAT rules for ChildSubnet", "id", childSubnet.UID)
		return err
	}
	for _, natRule := range natRules {
		natRule.MarkedForDelete = &MarkedForDelete
	}
	nat := BuildDefaultSNAT()
	ipPool, err := service.ipPoolStore.GetByChildSubnet(childSubnet.UID)
	if err != nil {
		log.Error(err, "failed to find IP Pool for ChildSubnet", "id", childSubnet.UID)
		return err
	}
	if ipPool != nil {
		ipPool.MarkedForDelete = &MarkedForDelete
	}

	ipPoolSubnet, err := service.ipBlockSubnetStore.GetByChildSubnet(childSubnet.UID)
	if err != nil {
		log.Error(err, "failed to find IP Pool block subnet for ChildSubnet", "id", childSubnet.UID)
		return err
	}
	if ipPoolSubnet != nil {
		ipPoolSubnet.MarkedForDelete = &MarkedForDelete
	}
	infraUpdate, err := service.WrapHierarchyInfra(ipPool, ipPoolSubnet, childSegment, bindingMaps, tier1, nat, natRules)
	if err != nil {
		log.Error(err, "failed to build hierarchy resources for ChildSubnet", "id", childSubnet.UID)
		return err
	}
	if infraUpdate != nil {
		err = service.NSXClient.InfraClient.Patch(*infraUpdate, &EnforceRevisionCheckParam)
		if err != nil {
			log.Error(err, "failed to delete resources from NSX for ChildSubnet",
				"id", childSubnet.UID)
			return err
		}
	}
	if err := service.applyResourcesInStore(ipPool, ipPoolSubnet, childSegment, bindingMaps, natRules); err != nil {
		return err
	}

	ipBlockPath := ipPoolSubnet.IpBlockPath
	if ipBlockPath != nil {
		if service.exhaustedIPBlock.Has(*ipBlockPath) {
			log.V(1).Info("IP subnet is released from an exhausted IP Block, mark it as unexhausted",
				"ip block", *ipBlockPath)
			service.exhaustedIPBlock.Delete(*ipBlockPath)
		}
	}
	return nil
}

func (service *ChildSubnetService) CreateOrUpdateVirtualNetwork(vnet *vnet.VirtualNetwork) error {
	if err := service.resyncParentResources(vnet); err != nil {
		return err
	}
	desiredParentConfig, err := service.buildParentConfigByVNet(vnet)
	if err != nil {
		return nil
	}
	existingParentConfig, err := service.parentConfigStore.get(string(vnet.UID))
	if err != nil {
		log.Error(err, "failed to find parent configuration by VirtualNetwork", "vnet", vnet.UID)
		return err
	}
	if desiredParentConfig.equals(existingParentConfig) {
		log.Info("No changes in VirtualNetwork", "vnet", vnet.UID)
		return nil
	}
	service.parentConfigStore.Apply([]*ParentConfig{desiredParentConfig})
	// TODO: update child subnets using the latest parent config.
	//service.updateChildSubnetBindingMaps()
	log.Info("Successfully created resources for VirtualNetwork", "vnet", vnet.UID)
	return nil
}

func (service *ChildSubnetService) getCluster() string {
	return service.NSXConfig.Cluster
}

func (service *ChildSubnetService) acquireSegmentCIDRAndGateway(childSubnet *v1alpha1.ChildSubnet, retry int) (*net.IPNet, net.IP, error) {
	intentPath := service.buildIPSubnetIntentPath(childSubnet)
	m, err := service.NSXClient.InfraRealizedStateClient.List(intentPath, nil)
	if err != nil {
		return nil, nil, err
	}
	var cidr *net.IPNet
	var gw net.IP
	for _, realizedEntity := range m.Results {
		if *realizedEntity.EntityType == "IpBlockSubnet" {
			for _, attr := range realizedEntity.ExtendedAttributes {
				if *attr.Key == "cidr" {
					_, cidr, _ = net.ParseCIDR(attr.Values[0])
					continue
				}
				if *attr.Key == "gateway_ip" {
					gw = net.ParseIP(attr.Values[0])
					continue
				}
			}
		}
	}
	if cidr != nil && gw != nil {
		log.V(1).Info("successfully realized ippool subnet from childSubnet", "childSubnet", childSubnet.UID)
		return cidr, gw, nil
	}
	if retry > 0 {
		log.V(1).Info("failed to acquire subnet cidr, retrying...", "childSubnet", childSubnet.UID, "retry", retry)
		time.Sleep(30 * time.Second)
		var e error
		cidr, gw, e = service.acquireSegmentCIDRAndGateway(childSubnet, retry-1)
		return cidr, gw, e
	} else {
		log.V(1).Info("failed to acquire subnet cidr after multiple retries", "childSubnet", childSubnet.UID)
		return nil, nil, nil
	}
}

// TODO: get valid IP Block path.
func (service *ChildSubnetService) getValidIPBlockPath(accessMode v1alpha1.AccessMode, parentConfig *ParentConfig) string {
	if string(accessMode) == v1alpha1.AccessModePublic {
		return parentConfig.publicIPBlockPath
	}
	return parentConfig.privateIPBlockPath
}

func (service *ChildSubnetService) getParentConfig(childSubnet *v1alpha1.ChildSubnet) (*ParentConfig, error) {
	parentConfig, err := service.parentConfigStore.getByNamespaceName(childSubnet.Spec.Parent, childSubnet.Namespace)
	if err != nil {
		log.Error(err, "failed to get parent configuration for ChildSubnet", "id", childSubnet.UID,
			"parent", childSubnet.Spec.Parent)
		return nil, err
	}
	if parentConfig == nil {
		log.Info("parent configuration for ChildSubnet doesn't exist", "id", childSubnet.UID, "parent", childSubnet.Spec.Parent)
		return nil, fmt.Errorf("no parent configuration found for ChildSubnet %s with value %s", childSubnet.UID, childSubnet.Spec.Parent)
	}
	return parentConfig, nil
}

func (service *ChildSubnetService) nextVlan(childSubnet *v1alpha1.ChildSubnet, parentConfig *ParentConfig) (int64, error) {
	parentPaths := parentConfig.segmentPaths
	existingVlans := sets.New[int64]()
	for parentPath := range parentPaths {
		bindingMaps, err := service.connectionBindingMapStore.listByParentSegmentPath(parentPath)
		if err != nil {
			log.Error(err, "failed to list segment connection binding maps via parent path", "parentPath", parentPaths)
			continue
		}
		for _, bm := range bindingMaps {
			existingVlans.Insert(*bm.VlanTrafficTag)
		}
	}
	for i := int64(1); i <= 4094; i++ {
		if !existingVlans.Has(i) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no valid VLAN for segment connection binding maps for ChildSubnet %s to parent %s",
		childSubnet.UID, childSubnet.Spec.Parent)
}

func (service *ChildSubnetService) updateChildSubnetBindingMaps(childSubnet *v1alpha1.ChildSubnet, parentConfig *ParentConfig, childSegment *model.Segment, tags []model.Tag) error {
	vlan := childSubnet.Status.Vlan
	desiredBindingMaps := service.buildSegmentConnectionBindingMaps(childSubnet, parentConfig, vlan, tags)
	existingBindingMaps, err := service.connectionBindingMapStore.listByChildSubnet(childSubnet.UID)
	if err != nil {
		log.Error(err, "failed to list segment connection binding maps via ChildSubnet", "id", childSubnet.UID)
		return err
	}
	changed, staled := common.CompareResources(SegmentConnectionBindingMapsToComparable(existingBindingMaps), SegmentConnectionBindingMapsToComparable(desiredBindingMaps))
	if len(changed) == 0 && len(staled) == 0 {
		log.Info("No changes in the segment binding maps for ChildSubnet", "id", childSubnet.UID)
		return nil
	}
	changedBindingMaps, staledBindingMaps := ComparableToSegmentConnectionBindingMaps(changed), ComparableToSegmentConnectionBindingMaps(staled)
	for i := range staledBindingMaps {
		staledBindingMaps[i].MarkedForDelete = &MarkedForDelete
	}
	finalBindingMaps := append(changedBindingMaps, staledBindingMaps...)
	return service.ApplySegmentConnectionBindingMaps(childSubnet.UID, childSegment, finalBindingMaps)
}

func (service *ChildSubnetService) parseParentPathFromBindingMaps(bindingMaps []*model.SegmentConnectionBindingMap) sets.Set[string] {
	parentPaths := sets.New[string]()
	for _, bindingMap := range bindingMaps {
		parentPaths.Insert(*bindingMap.ParentPath)
	}
	return parentPaths
}

func (service *ChildSubnetService) ApplySegmentConnectionBindingMaps(childSubnetID types.UID, childSegment *model.Segment, finalBindingMaps []*model.SegmentConnectionBindingMap) error {
	infraSegment, err := service.WrapHierarchyChildSegment(childSegment, finalBindingMaps)
	if err != nil {
		log.Error(err, "failed to build hierarchy segment with updated binding maps for ChildSubnet",
			"id", childSubnetID)
		return err
	}
	err = service.NSXClient.InfraClient.Patch(*infraSegment, &EnforceRevisionCheckParam)
	if err != nil {
		log.Error(err, "failed to patch child segment with connection binding maps for ChildSubnet",
			"childSubnet", childSubnetID, "childSegment", childSegment.Id)
		return err
	}
	err = service.connectionBindingMapStore.Apply(finalBindingMaps)
	if err != nil {
		return err
	}
	log.V(1).Info("successfully created or updated segment connection binding maps for ChildSubnet",
		"id", childSubnetID)
	return nil
}

func (service *ChildSubnetService) createChildSubnets(childSubnet *v1alpha1.ChildSubnet, parentConfig *ParentConfig, tags []model.Tag) (string, *net.IPNet, int64, error) {
	vlan, err := service.nextVlan(childSubnet, parentConfig)
	if err != nil {
		log.Error(err, "failed to find valid VLAN for ChildSubnet", "id", childSubnet.UID)
		return "", nil, 0, err
	}

	tier1, err := service.getTier1ByParent(childSubnet.UID, parentConfig)
	if err != nil {
		log.Error(err, "failed to find valid tier1 for ChildSubnet", "id", childSubnet.UID)
		return "", nil, 0, err
	}

	ipBlockPath := common.String(service.getValidIPBlockPath(childSubnet.Spec.AccessMode, parentConfig))
	nsxIPPool, nsxIPPoolSubnet := service.buildIPPoolWithSubnets(childSubnet, ipBlockPath, tags)
	infraIPPool, err := service.WrapHierarchyIPPool(nsxIPPool, nsxIPPoolSubnet)
	if err != nil {
		log.Error(err, "failed to build hierarchy IP Pool and block subnet on NSX for ChildSubnet",
			"id", childSubnet.UID)
		return "", nil, 0, err
	}
	if err := service.NSXClient.InfraClient.Patch(*infraIPPool, &EnforceRevisionCheckParam); err != nil {
		log.Error(err, "failed to patch IP Pool with block subnet for ChildSubnet",
			"id", childSubnet.UID, "childSegment")
		// check if ipblock is exhausted
		apiErr, _ := nsxutil.DumpAPIError(err)
		if apiErr != nil {
			for _, apiErrItem := range apiErr.RelatedErrors {
				// 520012=IpAddressBlock with max size does not have spare capacity to satisfy new block subnet of size
				if *apiErrItem.ErrorCode == 520012 {
					pathPattern := `path=\[([^\]]+)\]`
					pathRegex := regexp.MustCompile(pathPattern)
					pathMatch := pathRegex.FindStringSubmatch(*apiErrItem.ErrorMessage)
					if len(pathMatch) > 1 {
						path := pathMatch[1]
						if !service.exhaustedIPBlock.Has(path) {
							service.exhaustedIPBlock.Insert(path)
							log.Info("ExhaustedIPBlock: ", "ExhaustedIPBlock", path)
						}
						return "", nil, 0, &nsxutil.IPBlockExhaustedError{Desc: fmt.Sprintf("ip block %s is exhausted", path)}
					}
				}
			}
		}
		return "", nil, 0, err
	}
	var nsxErr error
	defer func() {
		if nsxErr != nil {
			nsxIPPool.MarkedForDelete = &MarkedForDelete
			nsxIPPoolSubnet.MarkedForDelete = &MarkedForDelete
			deleteInfraIPPool, _ := service.WrapHierarchyIPPool(nsxIPPool, nsxIPPoolSubnet)
			service.NSXClient.InfraClient.Patch(*deleteInfraIPPool, &EnforceRevisionCheckParam)
		}
	}()

	var cidr *net.IPNet
	var gw net.IP
	cidr, gw, nsxErr = service.acquireSegmentCIDRAndGateway(childSubnet, common.RealizeMaxRetries)
	if nsxErr != nil {
		log.Error(err, "failed to acquire subnet CIDR and gateway address for ChildSubnet", "id", childSubnet.UID)
		return "", nil, 0, nsxErr
	}
	gwNet := &net.IPNet{IP: gw, Mask: cidr.Mask}
	ipPoolIntentPath := service.buildIPPoolIntentPath(childSubnet)
	segment := service.buildSegment(childSubnet, parentConfig, ipPoolIntentPath, []*net.IPNet{gwNet}, tags)
	bindingMaps := service.buildSegmentConnectionBindingMaps(childSubnet, parentConfig, vlan, tags)
	nat := BuildDefaultSNAT()
	natRules := service.buildPolicySNATRules(childSubnet, []*net.IPNet{cidr}, tags)
	infraUpdate, err := service.WrapHierarchySegmentAndNAT(segment, bindingMaps, tier1, nat, natRules)
	if err != nil {
		log.Error(err, "failed to build hierarchy IP Pool and block subnet on NSX for ChildSubnet",
			"id", childSubnet.UID)
		nsxErr = err
		return "", nil, 0, err
	}

	nsxErr = service.NSXClient.InfraClient.Patch(*infraUpdate, &EnforceRevisionCheckParam)
	if nsxErr != nil {
		log.Error(nsxErr, "failed to patch segment, connection binding maps and SNAT rules for ChildSubnet",
			"id", childSubnet.UID, "childSegment")
		return "", nil, 0, nsxErr
	}

	segmentPath := service.buildSegmentIntentPath(childSubnet)
	if err := service.applyResourcesInStore(nsxIPPool, nsxIPPoolSubnet, segment, bindingMaps, natRules); err != nil {
		return segmentPath, gwNet, vlan, err
	}
	log.Info("Successfully created resources for ChildSubnet", "id", childSubnet.UID)
	return segmentPath, gwNet, vlan, nil
}

func (service *ChildSubnetService) applyResourcesInStore(nsxIPPool *model.IpAddressPool,
	nsxIPPoolSubnet *model.IpAddressPoolBlockSubnet,
	segment *model.Segment,
	bindingMaps []*model.SegmentConnectionBindingMap,
	natRules []*model.PolicyNatRule) error {
	if err := service.ipPoolStore.Apply([]*model.IpAddressPool{nsxIPPool}); err != nil {
		log.Error(err, "failed to apply IP pool in store", "id", nsxIPPool.Id, "delete", MarkedForDelete)
		return err
	}
	if err := service.ipBlockSubnetStore.Apply([]*model.IpAddressPoolBlockSubnet{nsxIPPoolSubnet}); err != nil {
		log.Error(err, "failed to apply IP pool subnet in store", "id", nsxIPPoolSubnet.Id, "delete", MarkedForDelete)
		return err
	}
	if err := service.childSegmentStore.Apply([]*model.Segment{segment}); err != nil {
		log.Error(err, "failed to apply segment in store", "id", segment.Id, "delete", MarkedForDelete)
		return err
	}
	if err := service.connectionBindingMapStore.Apply(bindingMaps); err != nil {
		log.Error(err, "failed to apply segment connection bindingMaps in store", "size", len(bindingMaps), "delete", MarkedForDelete)
		return err
	}
	if err := service.natRuleStore.Apply(natRules); err != nil {
		log.Error(err, "failed to apply policy NAT rules in store", "size", len(bindingMaps), "delete", MarkedForDelete)
		return err
	}
	return nil
}

func (service *ChildSubnetService) getTier1ByParent(childSubnetID types.UID, parentConfig *ParentConfig) (*model.Tier1, error) {
	tier1Path := parentConfig.tier1Path
	if tier1Path == "" {
		return nil, nil
	}
	tier1, err := service.tier1Store.getByPolicyPath(tier1Path)
	if err != nil {
		log.Error(err, "failed to find valid tier1 for ChildSubnet", "id", childSubnetID)
		return nil, err
	}
	return tier1, nil
}

func (service *ChildSubnetService) resyncParentResources(vnet *vnet.VirtualNetwork) error {
	return service.syncParentSegments(vnet)
}

func (service *ChildSubnetService) syncParentSegments(vnet *vnet.VirtualNetwork) error {
	existingSegments, err := service.parentSegmentStore.listByParent(vnet.UID)
	if err != nil {
		log.Error(err, "failed to list segments by VirtualNetwork", "vnet", vnet.UID)
		return err
	}
	segmentQueryParam := generateQueryParams(common.ResourceTypeSegment, []model.Tag{
		{Scope: common.String(common.TagScopeNCPVNetworkUID), Tag: common.String(string(vnet.UID))}})
	objects, err := service.SearchResourceWithoutStore(common.ResourceTypeSegment, segmentQueryParam, true, model.SegmentBindingType(), nil)
	if err != nil {
		log.Error(err, "failed to list segments on NSX by VirtualNetwork", "vnet", vnet.UID)
		return err
	}
	desiredSegments := make([]*model.Segment, len(objects))
	for i := range objects {
		segment := objects[i].(model.Segment)
		desiredSegments = append(desiredSegments, &segment)
	}
	changed, staled := common.CompareResources(SegmentsToComparable(existingSegments), SegmentsToComparable(desiredSegments))
	if len(changed) == 0 && len(staled) == 0 {
		log.Info("No changes in the segment binding maps for ChildSubnet", "id", vnet.UID)
		return nil
	}
	changedSegments, staleSegments := ComparableToSegments(changed), ComparableToSegments(staled)
	for i := range staleSegments {
		staleSegments[i].MarkedForDelete = &MarkedForDelete
	}
	changedSegments = append(changedSegments, staleSegments...)
	return service.parentSegmentStore.Apply(changedSegments)
}

func generateQueryParams(resourceTypeValue string, tags []model.Tag) string {
	tagParams := make([]string, 0)

	for _, tag := range tags {
		tagKey := strings.Replace(*tag.Scope, "/", "\\/", -1)
		tagParams = append(tagParams, fmt.Sprintf("tags.scope:%s", tagKey))
		if tag.Tag != nil {
			tagValue := strings.Replace(*tag.Tag, ":", "\\:", -1)
			tagParams = append(tagParams, fmt.Sprintf("tags.tag:%s", tagValue))
		}
	}

	resourceParam := fmt.Sprintf("%s:%s", common.ResourceType, resourceTypeValue)
	params := append([]string{resourceParam}, tagParams...)
	return strings.Join(params, " AND ")
}
