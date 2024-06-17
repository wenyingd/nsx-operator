package childsubnet

import (
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	"k8s.io/kube-openapi/pkg/util/sets"
)

type ParentConfig struct {
	id                 string
	name               string
	namespace          string
	tier1Path          string
	transportZonePath  string
	segmentPaths       sets.String
	publicIPBlockPath  string
	privateIPBlockPath string
	markedForDelete    *bool
}

func (c *ParentConfig) getNamespacedName() string {
	return getNamespacedName(c.namespace, c.name)
}

func getNamespacedName(namespace, name string) string {
	return namespace + "/" + name
}

func (c *ParentConfig) setIPBlockPaths(privateIPBlock, publicIPBlock *model.IpAddressBlock) {
	c.privateIPBlockPath = *(privateIPBlock.Path)
	c.publicIPBlockPath = *(publicIPBlock.Path)
}

func (c *ParentConfig) equals(config *ParentConfig) bool {
	if c.id != config.id || c.namespace != config.namespace || c.name != config.name ||
		c.tier1Path != config.tier1Path || c.transportZonePath != config.transportZonePath ||
		c.publicIPBlockPath != config.publicIPBlockPath || c.privateIPBlockPath != config.privateIPBlockPath {
		return false
	}
	return c.segmentPaths.Equal(config.segmentPaths)
}
