package childsubnet

import (
	"errors"
	"fmt"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
)

const (
	childSegmentPathKey        = "childSegment"
	parentSegmentPathKey       = "parentSegment"
	t1PathIndexer              = "t1PolicyPath"
	namespacedNameIndexKey     = "namespacedName"
	clusteredNamespaceIndexKey = "clusteredNamespace"
)

func keyFunc(obj interface{}) (string, error) {
	switch v := obj.(type) {
	case model.IpAddressBlock:
		return *v.Id, nil
	case model.IpAddressPool:
		return *v.Id, nil
	case model.IpAddressPoolBlockSubnet:
		return *v.Id, nil
	case model.Segment:
		return *v.Id, nil
	case model.SegmentConnectionBindingMap:
		return *v.Id, nil
	case model.Tier1:
		return *v.Id, nil
	case model.PolicyNatRule:
		return *v.Id, nil
	default:
		return "", errors.New("keyFunc doesn't support unknown type")
	}
}

func indexFuncByScope(obj interface{}, tagScope string) ([]string, error) {
	res := make([]string, 0, 5)
	switch v := obj.(type) {
	case model.IpAddressBlock:
		return filterTag(v.Tags, tagScope), nil
	case model.IpAddressPoolBlockSubnet:
		return filterTag(v.Tags, tagScope), nil
	case model.IpAddressPool:
		return filterTag(v.Tags, tagScope), nil
	case model.Segment:
		return filterTag(v.Tags, tagScope), nil
	case model.SegmentConnectionBindingMap:
		return filterTag(v.Tags, tagScope), nil
	case model.Tier1:
		return filterTag(v.Tags, tagScope), nil
	case model.PolicyNatRule:
		return filterTag(v.Tags, tagScope), nil
	default:
		return res, errors.New("indexFunc doesn't support unknown type")
	}
}

func childSubnetUidIndexFunc(obj interface{}) ([]string, error) {
	return indexFuncByScope(obj, common.TagScopeChildSubnetUID)
}

func projectUidIndexFunc(obj interface{}) ([]string, error) {
	return indexFuncByScope(obj, common.TagScopeNCPProjectUID)
}

func filterTag(tags []model.Tag, tagScope string) []string {
	res := make([]string, 0, 5)
	for _, tag := range tags {
		if *tag.Scope == tagScope {
			res = append(res, *tag.Tag)
		}
	}
	return res
}

// IPBlockStore is a store for nsx IPBlock which is used to allocate IpAddressPoolBlockSubnet.
type IPBlockStore struct {
	common.ResourceStore
}

func (ipBlockStore *IPBlockStore) Apply(i interface{}) error {
	if i == nil {
		return nil
	}
	ipblock := i.(*model.IpAddressBlock)
	if ipblock.MarkedForDelete != nil && *ipblock.MarkedForDelete {
		err := ipBlockStore.Delete(*ipblock)
		log.V(1).Info("delete ipblock from store", "IPBlock", ipblock)
		if err != nil {
			return err
		}
	} else {
		err := ipBlockStore.Add(*ipblock)
		log.V(1).Info("add IPBlock to store", "IPBlock", ipblock)
		if err != nil {
			return err
		}
	}
	return nil
}

func (ipBlockStore *IPBlockStore) getByIndex(index string, value string, logKey string) ([]*model.IpAddressBlock, error) {
	ipBlocks := make([]*model.IpAddressBlock, 0)
	indexResults, err := ipBlockStore.ResourceStore.Indexer.ByIndex(index, value)
	if err != nil {
		log.Error(err, "failed to get obj by index", logKey, value)
		return nil, err
	}
	if len(indexResults) == 0 {
		log.Info("did not get ip pool with index", logKey, value)
	}
	for _, ipBlock := range indexResults {
		b := ipBlock.(model.IpAddressBlock)
		ipBlocks = append(ipBlocks, &b)
	}
	return ipBlocks, nil
}

func (ipBlockStore *IPBlockStore) GetBySupervisorCluster(clusterName string) (*model.IpAddressBlock, error) {
	ipBlocks, err := ipBlockStore.getByIndex(common.TagScopeNCPCluster, clusterName, "Supervisor Cluster")
	if err != nil {
		return nil, err
	}
	if len(ipBlocks) > 0 {
		return ipBlocks[0], nil
	}
	return nil, fmt.Errorf("no IPBlock configured in cluster %s", clusterName)
}

func (ipBlockStore *IPBlockStore) GetByNamespace(uid types.UID) ([]*model.IpAddressBlock, error) {
	return ipBlockStore.getByIndex(common.TagScopeNCPProjectUID, string(uid), "supervisor Namespace")
}

func (ipBlockStore *IPBlockStore) getInitTags() []model.Tag {
	return []model.Tag{
		{Scope: common.String(common.TagScopeNCPCluster)},
	}
}

func ipBlockByOnlyClusterIndexFunc(obj interface{}) ([]string, error) {
	ipBlock, ok := obj.(*model.IpAddressBlock)
	if !ok {
		return nil, errors.New("indexFunc doesn't support unknown type")
	}
	var index int
	for i, tag := range ipBlock.Tags {
		if *tag.Scope == common.TagScopeNCPCluster {
			return nil, nil
		}
		if *tag.Scope == common.TagScopeNCPCluster {
			index = i
		}
	}
	return []string{*ipBlock.Tags[index].Tag}, nil
}

func newIPBlockStore() *IPBlockStore {
	return &IPBlockStore{ResourceStore: common.ResourceStore{
		Indexer: cache.NewIndexer(keyFunc, cache.Indexers{
			common.TagScopeChildSubnetBlock: ipBlockByOnlyClusterIndexFunc,
			common.TagScopeNCPProjectUID:    projectUidIndexFunc,
		}),
		BindingType: model.IpAddressBlockBindingType(),
	}}
}

// IPPoolStore is a store for nsx IPPool.
type IPPoolStore struct {
	common.ResourceStore
}

func (ipPoolStore *IPPoolStore) Apply(i interface{}) error {
	ipPool := i.(*model.IpAddressPool)
	if ipPool.MarkedForDelete != nil && *ipPool.MarkedForDelete {
		err := ipPoolStore.Delete(*ipPool)
		log.V(1).Info("delete ipPool from store", "ipPool", ipPool)
		if err != nil {
			return err
		}
	} else {
		err := ipPoolStore.Add(*ipPool)
		log.V(1).Info("add ipPool to store", "ipPool", ipPool)
		if err != nil {
			return err
		}
	}
	return nil
}

func (ipPoolStore *IPPoolStore) getByIndex(key string, value string, logKey string) ([]*model.IpAddressPool, error) {
	nsxIPPools := make([]*model.IpAddressPool, 0)
	indexResults, err := ipPoolStore.ResourceStore.ByIndex(key, value)
	if err != nil {
		log.Error(err, "failed to get ip pool", logKey, value)
		return nil, err
	}
	if len(indexResults) == 0 {
		log.Info("did not get ip pool with index", logKey, value)
	}
	for _, ipPool := range indexResults {
		p := ipPool.(model.IpAddressPool)
		nsxIPPools = append(nsxIPPools, &p)
	}
	return nsxIPPools, nil
}

func (ipPoolStore *IPPoolStore) GetByChildSubnet(uid types.UID) (*model.IpAddressPool, error) {
	ipPools, err := ipPoolStore.getByIndex(common.TagScopeChildSubnetUID, string(uid), "ChildSubnet ID")
	if err != nil {
		return nil, err
	}
	if len(ipPools) > 0 {
		return ipPools[0], nil
	}
	return nil, nil
}

func (ipPoolStore *IPPoolStore) getInitTags() []model.Tag {
	return []model.Tag{
		{Scope: common.String(common.TagScopeChildSubnetUID)},
	}
}

func newIPPoolStore() *IPPoolStore {
	return &IPPoolStore{ResourceStore: common.ResourceStore{
		Indexer: cache.NewIndexer(keyFunc, cache.Indexers{
			common.TagScopeChildSubnetUID: childSubnetUidIndexFunc,
		}),
		BindingType: model.IpAddressPoolBindingType(),
	}}
}

// IPPoolBlockSubnetStore is a store for nsx IpAddressPoolBlockSubnet.
type IPPoolBlockSubnetStore struct {
	common.ResourceStore
}

func (ipPoolBlockSubnetStore *IPPoolBlockSubnetStore) getByIndex(key string, value string, logKey string) ([]*model.IpAddressPoolBlockSubnet, error) {
	nsxIPSubnets := make([]*model.IpAddressPoolBlockSubnet, 0)
	indexResults, err := ipPoolBlockSubnetStore.ResourceStore.ByIndex(key, value)
	if err != nil {
		log.Error(err, "failed to get ip subnets", logKey, value)
		return nil, err
	}
	if len(indexResults) == 0 {
		log.Info("did not get ip subnets with index", logKey, value)
	}
	for _, ipSubnet := range indexResults {
		t := ipSubnet.(model.IpAddressPoolBlockSubnet)
		nsxIPSubnets = append(nsxIPSubnets, &t)
	}
	return nsxIPSubnets, nil
}

func (ipPoolBlockSubnetStore *IPPoolBlockSubnetStore) GetByChildSubnet(uid types.UID) (*model.IpAddressPoolBlockSubnet, error) {
	ipPoolSubnets, err := ipPoolBlockSubnetStore.getByIndex(common.TagScopeChildSubnetUID, string(uid), "ChildSubnet ID")
	if err != nil {
		return nil, err
	}
	if len(ipPoolSubnets) > 0 {
		return ipPoolSubnets[0], nil
	}
	return nil, nil
}

func (ipPoolBlockSubnetStore *IPPoolBlockSubnetStore) Apply(i interface{}) error {
	ipPoolBlockSubnets := i.([]*model.IpAddressPoolBlockSubnet)
	for _, ipPoolBlockSubnet := range ipPoolBlockSubnets {
		if ipPoolBlockSubnet.MarkedForDelete != nil && *ipPoolBlockSubnet.MarkedForDelete {
			err := ipPoolBlockSubnetStore.Delete(*ipPoolBlockSubnet)
			log.V(1).Info("delete ipPoolBlockSubnet from store", "ipPoolBlockSubnet", ipPoolBlockSubnet)
			if err != nil {
				return err
			}
		} else {
			err := ipPoolBlockSubnetStore.Add(*ipPoolBlockSubnet)
			log.V(1).Info("add ipPoolBlockSubnet to store", "ipPoolBlockSubnet", ipPoolBlockSubnet)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (ipPoolBlockSubnetStore *IPPoolBlockSubnetStore) getInitTags() []model.Tag {
	return []model.Tag{
		{Scope: common.String(common.TagScopeChildSubnetUID)},
	}
}

func newIPPoolBlockSubnetStore() *IPPoolBlockSubnetStore {
	return &IPPoolBlockSubnetStore{ResourceStore: common.ResourceStore{
		Indexer: cache.NewIndexer(keyFunc, cache.Indexers{
			common.TagScopeChildSubnetUID: childSubnetUidIndexFunc,
		}),
		BindingType: model.IpAddressPoolBlockSubnetBindingType(),
	}}
}

// SegmentStore is a store for nsx Segment.
type SegmentStore struct {
	common.ResourceStore
	isParent bool
}

func (segmentStore *SegmentStore) getByIndex(key string, value string, logKey string) ([]*model.Segment, error) {
	nsxSegments := make([]*model.Segment, 0)
	indexResults, err := segmentStore.ResourceStore.ByIndex(key, value)
	if err != nil {
		log.Error(err, "failed to get segments", logKey, value)
		return nil, err
	}
	if len(indexResults) == 0 {
		log.Info("did not get segments with index", logKey, value)
	}
	for _, ipSubnet := range indexResults {
		t := ipSubnet.(model.Segment)
		nsxSegments = append(nsxSegments, &t)
	}
	return nsxSegments, nil
}

func (segmentStore *SegmentStore) getByChildSubnet(uid types.UID) (*model.Segment, error) {
	segments, err := segmentStore.getByIndex(common.TagScopeChildSubnetUID, string(uid), "ChildSubnet ID")
	if err != nil {
		return nil, err
	}
	if len(segments) > 0 {
		return segments[0], nil
	}
	return nil, nil
}

func (segmentStore *SegmentStore) listByParent(uid types.UID) ([]*model.Segment, error) {
	segments, err := segmentStore.getByIndex(common.TagScopeNCPVNetworkUID, string(uid), "VirtualNetwork ID")
	if err != nil {
		return nil, err
	}
	return segments, nil
}

func segmentByVNetIndexFunc(obj interface{}) ([]string, error) {
	segment, ok := obj.(*model.Segment)
	if !ok {
		return []string{}, errors.New("indexFunc doesn't support unknown type")
	}
	return filterTag(segment.Tags, common.TagScopeNCPVNetworkUID), nil
}

func (segmentStore *SegmentStore) Apply(i interface{}) error {
	segments := i.([]*model.Segment)
	for _, segment := range segments {
		if segment.MarkedForDelete != nil && *segment.MarkedForDelete {
			err := segmentStore.Delete(*segment)
			log.V(1).Info("delete segment from store", "segment", segment)
			if err != nil {
				return err
			}
		} else {
			err := segmentStore.Add(*segment)
			log.V(1).Info("add segment to store", "segment", segment)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (segmentStore *SegmentStore) getInitTags() []model.Tag {
	if segmentStore.isParent {
		return []model.Tag{
			{Scope: common.String(common.TagScopeNCPVNetworkUID)},
		}
	}
	return []model.Tag{
		{Scope: common.String(common.TagScopeChildSubnetUID)},
	}
}

func newSegmentStore(isParent bool) *SegmentStore {
	store := &SegmentStore{isParent: isParent}
	if !isParent {
		store.ResourceStore = common.ResourceStore{
			Indexer: cache.NewIndexer(keyFunc, cache.Indexers{
				common.TagScopeChildSubnetUID: childSubnetUidIndexFunc,
			}),
			BindingType: model.SegmentBindingType(),
		}
	} else {
		store.ResourceStore = common.ResourceStore{
			Indexer: cache.NewIndexer(keyFunc, cache.Indexers{
				common.TagScopeNCPVNetworkUID: segmentByVNetIndexFunc,
			}),
			BindingType: model.SegmentBindingType(),
		}
	}

	return store
}

// SegmentConnectionBindingMapStore is a store for nsx SegmentConnectionBindingMap.
type SegmentConnectionBindingMapStore struct {
	common.ResourceStore
}

func (s *SegmentConnectionBindingMapStore) Apply(i interface{}) error {
	bindingMaps := i.([]*model.SegmentConnectionBindingMap)
	for _, bindingMap := range bindingMaps {
		if bindingMap.MarkedForDelete != nil && *bindingMap.MarkedForDelete {
			err := s.Delete(*bindingMap)
			log.V(1).Info("delete segmentConenctionBindingMap from store", "connectionBindingMap", bindingMap)
			if err != nil {
				return err
			}
		} else {
			err := s.Add(*bindingMap)
			log.V(1).Info("add segmentConenctionBindingMap to store", "connectionBindingMap", bindingMaps)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SegmentConnectionBindingMapStore) getByIndex(key string, value string, logKey string) ([]*model.SegmentConnectionBindingMap, error) {
	nsxSegmentConnectionBindingMaps := make([]*model.SegmentConnectionBindingMap, 0)
	indexResults, err := s.ResourceStore.ByIndex(key, value)
	if err != nil {
		log.Error(err, "failed to get segmentConnectionBindingMap", logKey, value)
		return nil, err
	}
	if len(indexResults) == 0 {
		log.Info("did not get segmentConnectionBindingMap with index", logKey, value)
	}
	for _, ipSubnet := range indexResults {
		t := ipSubnet.(model.SegmentConnectionBindingMap)
		nsxSegmentConnectionBindingMaps = append(nsxSegmentConnectionBindingMaps, &t)
	}
	return nsxSegmentConnectionBindingMaps, nil
}

func parentConfigUidIndexFunc(obj interface{}) ([]string, error) {
	return indexFuncByScope(obj, common.TagScopeParentConfigUID)
}

func (s *SegmentConnectionBindingMapStore) listByChildSubnet(uid types.UID) ([]*model.SegmentConnectionBindingMap, error) {
	return s.getByIndex(common.TagScopeChildSubnetUID, string(uid), "ChildSubnet ID")
}

func (s *SegmentConnectionBindingMapStore) listByParentConfig(uid string) ([]*model.SegmentConnectionBindingMap, error) {
	return s.getByIndex(common.TagScopeParentConfigUID, string(uid), "ParentConfig ID")
}

func (s *SegmentConnectionBindingMapStore) listByChildSegmentPath(path string) ([]*model.SegmentConnectionBindingMap, error) {
	return s.getByIndex(childSegmentPathKey, path, "Child Segment")
}

func (s *SegmentConnectionBindingMapStore) listByParentSegmentPath(path string) ([]*model.SegmentConnectionBindingMap, error) {
	return s.getByIndex(parentSegmentPathKey, path, "Parent Segment")
}

func (s *SegmentConnectionBindingMapStore) getInitTags() []model.Tag {
	return []model.Tag{
		{Scope: common.String(common.TagScopeChildSubnetUID)},
	}
}

func connectionBindingMapByChildSegmentPathIndexFunc(obj interface{}) ([]string, error) {
	v, ok := obj.(*model.SegmentConnectionBindingMap)
	if !ok {
		return []string{}, nil
	}
	return []string{*v.ParentPath}, nil
}

func connectionBindingMapByParentSegmentPathIndexFunc(obj interface{}) ([]string, error) {
	v, ok := obj.(*model.SegmentConnectionBindingMap)
	if !ok {
		return []string{}, nil
	}
	return []string{*v.SegmentPath}, nil
}

func newSegmentConnectionBindingMapStore() *SegmentConnectionBindingMapStore {
	return &SegmentConnectionBindingMapStore{ResourceStore: common.ResourceStore{
		Indexer: cache.NewIndexer(keyFunc, cache.Indexers{
			common.TagScopeChildSubnetUID:  childSubnetUidIndexFunc,
			common.TagScopeParentConfigUID: parentConfigUidIndexFunc,
			childSegmentPathKey:            connectionBindingMapByChildSegmentPathIndexFunc,
			parentSegmentPathKey:           connectionBindingMapByParentSegmentPathIndexFunc,
		}),
		BindingType: model.SegmentConnectionBindingMapBindingType(),
	}}
}

// Tier1Store is a store for nsx Tier-1s which the parent and child segments are attaching to.
// The Tier1 is also used when creating noSNAT rules for the CIDR in IpAddressBlockSubnet.
type Tier1Store struct {
	common.ResourceStore
}

func (tier1Store *Tier1Store) Apply(i interface{}) error {
	if i == nil {
		return nil
	}
	t := i.(*model.Tier1)
	if t.MarkedForDelete != nil && *t.MarkedForDelete {
		err := tier1Store.Delete(*t)
		log.V(1).Info("delete tier1 from store", "tier1", t)
		if err != nil {
			return err
		}
	} else {
		err := tier1Store.Add(*t)
		log.V(1).Info("add tier1 to store", "tier1", t)
		if err != nil {
			return err
		}
	}
	return nil
}

func (tier1Store *Tier1Store) getByIndex(index string, value string, logKey string) ([]*model.Tier1, error) {
	tier1s := make([]*model.Tier1, 0)
	indexResults, err := tier1Store.ResourceStore.Indexer.ByIndex(index, value)
	if err != nil {
		log.Error(err, "failed to get obj by index", logKey, value)
		return nil, err
	}
	if len(indexResults) == 0 {
		log.Info("did not get Tier1 with index", logKey, value)
	}
	for _, r := range indexResults {
		t := r.(model.Tier1)
		tier1s = append(tier1s, &t)
	}
	return tier1s, nil
}

func (tier1Store *Tier1Store) getByNamespaceID(uid types.UID) ([]*model.Tier1, error) {
	return tier1Store.getByIndex(common.TagScopeNCPProjectUID, string(uid), "supervisor Namespace")
}

func (tier1Store *Tier1Store) getByNamespaceName(namespace string, cluster string) ([]*model.Tier1, error) {
	return tier1Store.getByIndex(clusteredNamespaceIndexKey, getClusterNamespaceKey(cluster, namespace), "supervisor Namespace")
}

func getClusterNamespaceKey(cluster string, namespace string) string {
	return fmt.Sprintf("%s/%s", cluster, namespace)
}

func (tier1Store *Tier1Store) getByPolicyPath(policyPath string) (*model.Tier1, error) {
	tier1s, err := tier1Store.getByIndex(t1PathIndexer, policyPath, "policy path")
	if err != nil {
		return nil, err
	}
	if len(tier1s) > 0 {
		return tier1s[0], nil
	}
	return nil, nil
}

func tier1ByPolicyPathFunc(obj interface{}) ([]string, error) {
	t, ok := obj.(*model.Tier1)
	if !ok {
		return []string{}, errors.New("indexFunc doesn't support unknown type")
	}
	return []string{*(t.Path)}, nil
}

func (tier1Store *Tier1Store) getInitTags() []model.Tag {
	return []model.Tag{
		{Scope: common.String(common.TagScopeNCPProjectUID)},
	}
}

func newTier1Store() *Tier1Store {
	return &Tier1Store{ResourceStore: common.ResourceStore{
		Indexer: cache.NewIndexer(keyFunc, cache.Indexers{
			common.TagScopeNCPProjectUID: projectUidIndexFunc,
			t1PathIndexer:                tier1ByPolicyPathFunc,
			clusteredNamespaceIndexKey:   tier1ByClusteredNamespaceFunc,
		}),
		BindingType: model.Tier1BindingType(),
	}}
}

func tier1ByClusteredNamespaceFunc(obj interface{}) ([]string, error) {
	t, ok := obj.(*model.Tier1)
	if !ok {
		return []string{}, errors.New("indexFunc doesn't support unknown type")
	}
	var cluster, namespace string
	for _, tag := range t.Tags {
		if *tag.Scope == common.TagScopeNCPCluster {
			cluster = *(tag.Tag)
		} else if *tag.Scope == common.TagScopeNCPProject {
			namespace = *(tag.Tag)
		}
	}
	if cluster != "" && namespace != "" {
		return []string{getClusterNamespaceKey(cluster, namespace)}, nil
	}
	log.Info("Either NCP cluster or Namespace is not tag on Tier1", "t1", t.Id,
		common.TagScopeNCPCluster, cluster, common.TagScopeNCPProject, namespace)
	return []string{*(t.Path)}, nil
}

// NATRuleStore is a store for nsx PolicyNatRule which is created on Tier-1 to perform noSNAT if the packet is
// from or to IP addresses in the IpAddressBlockSubnet.
type NATRuleStore struct {
	common.ResourceStore
}

func (natRuleStore *NATRuleStore) Apply(i interface{}) error {
	if i == nil {
		return nil
	}
	rule := i.(*model.PolicyNatRule)
	if rule.MarkedForDelete != nil && *rule.MarkedForDelete {
		err := natRuleStore.Delete(*rule)
		log.V(1).Info("delete NAT rule from store", "rule", rule)
		if err != nil {
			return err
		}
	} else {
		err := natRuleStore.Add(*rule)
		log.V(1).Info("add NAT rule to store", "rule", rule)
		if err != nil {
			return err
		}
	}
	return nil
}

func (natRuleStore *NATRuleStore) getByIndex(index string, value string, logKey string) ([]*model.PolicyNatRule, error) {
	natRules := make([]*model.PolicyNatRule, 0)
	indexResults, err := natRuleStore.ResourceStore.Indexer.ByIndex(index, value)
	if err != nil {
		log.Error(err, "failed to get obj by index", logKey, value)
		return nil, err
	}
	if len(indexResults) == 0 {
		log.Info("did not get PolicyNATRule with index", logKey, value)
	}
	for _, r := range indexResults {
		rule := r.(model.PolicyNatRule)
		natRules = append(natRules, &rule)
	}
	return natRules, nil
}

func (natRuleStore *NATRuleStore) GetNATRulesByChildSubnet(uid types.UID) ([]*model.PolicyNatRule, error) {
	return natRuleStore.getByIndex(common.TagScopeChildSubnetUID, string(uid), "ChildSubnet ID")
}

func (natRuleStore *NATRuleStore) getInitTags() []model.Tag {
	return []model.Tag{
		{Scope: common.String(common.TagScopeChildSubnetUID)},
	}
}

func newNATRuleStore() *NATRuleStore {
	return &NATRuleStore{ResourceStore: common.ResourceStore{
		Indexer: cache.NewIndexer(keyFunc, cache.Indexers{
			common.TagScopeChildSubnetUID: childSubnetUidIndexFunc,
		}),
		BindingType: model.PolicyNatRuleBindingType(),
	}}
}

type ParentConfigStore struct {
	cache.Indexer
}

func parentConfigKeyFunc(obj interface{}) (string, error) {
	pc, ok := obj.(*ParentConfig)
	if !ok {
		return "", errors.New("unsupported type in parentConfig key function")
	}
	return pc.id, nil
}

func parentConfigNamespacedNameIndexFunc(obj interface{}) (string, error) {
	pc, ok := obj.(*ParentConfig)
	if !ok {
		return "", errors.New("unsupported type in parentConfig key function")
	}
	return pc.getNamespacedName(), nil
}

func (parentConfigStore *ParentConfigStore) Apply(i interface{}) error {
	if i == nil {
		return nil
	}
	config := i.(*ParentConfig)
	if config.markedForDelete != nil && *config.markedForDelete {
		err := parentConfigStore.Delete(*config)
		log.V(1).Info("delete parent config from store", "name", config.name, "namespace", config.namespace)
		if err != nil {
			return err
		}
	} else {
		err := parentConfigStore.Add(*config)
		log.V(1).Info("add parent config to store", "config", config)
		if err != nil {
			return err
		}
	}
	return nil
}

func (parentConfigStore *ParentConfigStore) get(id string) (*ParentConfig, error) {
	pc, exists, err := parentConfigStore.GetByKey(id)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return pc.(*ParentConfig), nil
}

func (parentConfigStore *ParentConfigStore) getByNamespaceName(name, namespace string) (*ParentConfig, error) {
	pcs, err := parentConfigStore.ByIndex(namespacedNameIndexKey, getNamespacedName(namespace, name))
	if err != nil {
		return nil, err
	}
	if len(pcs) == 0 {
		return nil, nil
	}
	return pcs[0].(*ParentConfig), nil
}

func newParentConfigStore() *ParentConfigStore {
	return &ParentConfigStore{
		Indexer: cache.NewIndexer(parentConfigKeyFunc, cache.Indexers{
			namespacedNameIndexKey: parentConfigUidIndexFunc,
		})}
}
