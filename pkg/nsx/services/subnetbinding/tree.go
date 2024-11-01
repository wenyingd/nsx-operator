package subnetbinding

import (
	"github.com/vmware/vsphere-automation-sdk-go/runtime/data"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

var leafType = "SubnetConnectionBindingMap"

type hNode struct {
	resourceType string
	resourceID   string
	bindingMap   *model.SubnetConnectionBindingMap
	childNodes   []*hNode
}

func (n *hNode) mergeChildNode(node *hNode) {
	if node.resourceType == leafType {
		n.childNodes = append(n.childNodes, node)
		return
	}

	for _, cn := range n.childNodes {
		if cn.resourceType == node.resourceType && cn.resourceID == node.resourceID {
			for _, chN := range node.childNodes {
				cn.mergeChildNode(chN)
			}
			return
		}
	}
	n.childNodes = append(n.childNodes, node)
}

func (n *hNode) buildTree() ([]*data.StructValue, error) {
	if n.resourceType == leafType {
		dataValue, err := wrapSubnetBindingMap(n.bindingMap)
		if err != nil {
			return nil, err
		}
		return []*data.StructValue{dataValue}, nil
	}

	children := make([]*data.StructValue, 0)
	for _, cn := range n.childNodes {
		cnDataValues, err := cn.buildTree()
		if err != nil {
			return nil, err
		}
		children = append(children, cnDataValues...)
	}
	if n.resourceType == "OrgRoot" {
		return children, nil
	}

	return wrapChildResourceReference(n.resourceType, n.resourceID, children)
}

func buildHNodeFromSubnetConnectionBindingMap(subnetPath string, bindingMap *model.SubnetConnectionBindingMap) (*hNode, error) {
	vpcInfo, err := common.ParseVPCResourcePath(subnetPath)
	if err != nil {
		return nil, err
	}
	return &hNode{
		resourceType: "Org",
		resourceID:   vpcInfo.OrgID,
		childNodes: []*hNode{
			{
				resourceType: "Project",
				resourceID:   vpcInfo.ProjectID,
				childNodes: []*hNode{
					{
						resourceID:   vpcInfo.VPCID,
						resourceType: "Vpc",
						childNodes: []*hNode{
							{
								resourceID:   vpcInfo.ID,
								resourceType: "Subnet",
								childNodes: []*hNode{
									{
										resourceID:   *bindingMap.Id,
										resourceType: leafType,
										bindingMap:   bindingMap,
									},
								},
							},
						},
					},
				},
			},
		},
	}, nil
}

func buildOrgRootBySubnetConnectionBindingMaps(bindingMaps []*model.SubnetConnectionBindingMap, markForDelete *bool, subnetPath string) (*model.OrgRoot, error) {
	rootNode := &hNode{
		resourceType: "OrgRoot",
	}

	for _, bm := range bindingMaps {
		if markForDelete != nil {
			bm.MarkedForDelete = markForDelete
		}

		parentPath := subnetPath
		if parentPath == "" {
			parentPath = *bm.ParentPath
		}
		orgNode, err := buildHNodeFromSubnetConnectionBindingMap(parentPath, bm)
		if err != nil {
			log.Error(err, "Failed to build data value for SubnetConnectionBindingMap, ignore", "bindingMap", *bm.Path)
			continue
		}
		rootNode.mergeChildNode(orgNode)
	}

	children, err := rootNode.buildTree()
	if err != nil {
		log.Error(err, "Failed to build data values for multiple SubnetConnectionBindingMaps")
		return nil, err
	}

	return &model.OrgRoot{
		Children:     children,
		ResourceType: String("OrgRoot"),
	}, nil
}

func wrapChildResourceReference(targetType, resID string, children []*data.StructValue) ([]*data.StructValue, error) {
	childRes := model.ChildResourceReference{
		Id:           &resID,
		ResourceType: "ChildResourceReference",
		TargetType:   &targetType,
		Children:     children,
	}
	dataValue, errors := common.NewConverter().ConvertToVapi(childRes, model.ChildResourceReferenceBindingType())
	if len(errors) > 0 {
		return nil, errors[0]
	}
	return []*data.StructValue{dataValue.(*data.StructValue)}, nil
}

func wrapSubnetBindingMap(bindingMap *model.SubnetConnectionBindingMap) (*data.StructValue, error) {
	bindingMap.ResourceType = &common.ResourceTypeSubnetConnectionBIndingMap
	childBindingMap := model.ChildSubnetConnectionBindingMap{
		Id:                         bindingMap.Id,
		MarkedForDelete:            bindingMap.MarkedForDelete,
		ResourceType:               "ChildSubnetConnectionBindingMap",
		SubnetConnectionBindingMap: bindingMap,
	}
	dataValue, errors := common.NewConverter().ConvertToVapi(childBindingMap, model.ChildSubnetConnectionBindingMapBindingType())
	if len(errors) > 0 {
		return nil, errors[0]
	}
	return dataValue.(*data.StructValue), nil
}
