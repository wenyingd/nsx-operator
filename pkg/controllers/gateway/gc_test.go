package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	mockclient "github.com/vmware-tanzu/nsx-operator/pkg/mock/controller-runtime/client"
	mockdns "github.com/vmware-tanzu/nsx-operator/pkg/mock/dnsrecordprovider"
)

func TestCollectGarbage(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()

	c := mockclient.NewMockClient(ctrlMock)
	d := mockdns.NewMockDNSRecordProvider(ctrlMock)

	r := &GatewayReconciler{
		Client:        c,
		DNS:           d,
		StatusUpdater: setupMockStatusUpdater(ctrlMock),
		apiResources: gatewayAPIResources{
			gatewayEnabled:     true,
			listenerSetEnabled: true,
		},
	}

	c.EXPECT().List(ctx, gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		return nil
	}).AnyTimes()
	c.EXPECT().Get(ctx, gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	d.EXPECT().ListRecordOwnerResource().Return(map[string]sets.Set[types.NamespacedName]{
		"Gateway": sets.New(types.NamespacedName{Namespace: "ns", Name: "gw1"}),
	}).AnyTimes()
	d.EXPECT().ListReferredGatewayNN().Return(sets.New(types.NamespacedName{Namespace: "ns", Name: "gw1"})).AnyTimes()
	d.EXPECT().DeleteRecordByOwnerNN(ctx, gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil).AnyTimes()

	err := r.CollectGarbage(ctx)
	assert.NoError(t, err)
}

func TestGcListExistingOwners_Error(t *testing.T) {
	ctx := context.Background()
	ctrlMock := gomock.NewController(t)
	defer ctrlMock.Finish()

	c := mockclient.NewMockClient(ctrlMock)
	r := &GatewayReconciler{Client: c}

	// Mock a route reconciler that returns an error
	rr := newRouteReconciler[*HTTPRoute, gatewayv1.HTTPRoute, *gatewayv1.HTTPRoute](
		r, "HTTPRoute", func(v *gatewayv1.HTTPRoute) *HTTPRoute { return nil }, func() client.ObjectList { return &gatewayv1.HTTPRouteList{} })
	r.apiResources.routeReconcilers = []routeReconciler{rr}

	c.EXPECT().List(ctx, gomock.Any(), gomock.Any()).Return(assert.AnError).Times(1)

	_, err := gcListExistingOwners(ctx, c, r)
	assert.Error(t, err)
	assert.Equal(t, assert.AnError, err)
}
