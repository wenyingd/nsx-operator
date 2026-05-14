package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	extdnssrc "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/source"
)

func TestSortAdmissionRowsForCompare(t *testing.T) {
	rows := []extdnssrc.AdmissionHostCacheRow{
		{
			FromListenerSet: true,
			ListenerSet:     types.NamespacedName{Namespace: "ns", Name: "ls2"},
			Section:         gatewayv1.SectionName("sec1"),
			Filter:          "b.com",
		},
		{
			FromListenerSet: false,
			Section:         gatewayv1.SectionName("sec2"),
			Filter:          "a.com",
		},
		{
			FromListenerSet: true,
			ListenerSet:     types.NamespacedName{Namespace: "ns", Name: "ls1"},
			Section:         gatewayv1.SectionName("sec1"),
			Filter:          "c.com",
		},
		{
			FromListenerSet: true,
			ListenerSet:     types.NamespacedName{Namespace: "ns", Name: "ls2"},
			Section:         gatewayv1.SectionName("sec0"),
			Filter:          "d.com",
		},
		{
			FromListenerSet: true,
			ListenerSet:     types.NamespacedName{Namespace: "ns", Name: "ls2"},
			Section:         gatewayv1.SectionName("sec1"),
			Filter:          "a.com",
		},
	}

	sorted := sortAdmissionRowsForCompare(rows)

	assert.Len(t, sorted, 5)
	assert.False(t, sorted[0].FromListenerSet)
	assert.Equal(t, "a.com", sorted[0].Filter)

	assert.True(t, sorted[1].FromListenerSet)
	assert.Equal(t, "ns/ls1", sorted[1].ListenerSet.String())

	assert.True(t, sorted[2].FromListenerSet)
	assert.Equal(t, "ns/ls2", sorted[2].ListenerSet.String())
	assert.Equal(t, "sec0", string(sorted[2].Section))

	assert.True(t, sorted[3].FromListenerSet)
	assert.Equal(t, "ns/ls2", sorted[3].ListenerSet.String())
	assert.Equal(t, "sec1", string(sorted[3].Section))
	assert.Equal(t, "a.com", sorted[3].Filter)

	assert.True(t, sorted[4].FromListenerSet)
	assert.Equal(t, "ns/ls2", sorted[4].ListenerSet.String())
	assert.Equal(t, "sec1", string(sorted[4].Section))
	assert.Equal(t, "b.com", sorted[4].Filter)
}
