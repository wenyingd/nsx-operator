package networkinfo

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"

	"github.com/vmware-tanzu/nsx-operator/pkg/apis/v1alpha1"
	"github.com/vmware-tanzu/nsx-operator/pkg/config"
	mock_client "github.com/vmware-tanzu/nsx-operator/pkg/mock/controller-runtime/client"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/vpc"
)

func TestGetVPCState(t *testing.T) {
	service := &vpc.VPCService{
		Service: common.Service{
			NSXConfig: &config.NSXOperatorConfig{
				NsxConfig: &config.NsxConfig{
					EnforcementPoint: "vmc-enforcementpoint",
				},
			},
		},
	}
	mockCtl := gomock.NewController(t)
	k8sClient := mock_client.NewMockClient(mockCtl)
	r := &NetworkInfoReconciler{
		Client:  k8sClient,
		Scheme:  nil,
		Service: service,
	}

	vpcModel := &model.Vpc{
		Path:        common.String("vpc-path1"),
		DisplayName: common.String("vpc1"),
		PrivateIps:  []string{"1.1.1.0/24"},
		ServiceGateway: &model.ServiceGateway{
			AutoSnat: common.Bool(true),
		},
		LoadBalancerVpcEndpoint: &model.LoadBalancerVPCEndpoint{
			Enabled: common.Bool(true),
		},
	}

	for _, tc := range []struct {
		name            string
		mockServiceFunc func() *gomonkey.Patches
		expErr          string
		expLBPath       string
		expVpcState     *v1alpha1.VPCState
	}{
		{
			name: "failed to get default SNAT IP",
			mockServiceFunc: func() *gomonkey.Patches {
				patch := gomonkey.ApplyMethod(reflect.TypeOf(service), "GetDefaultSNATIP", func(_ *vpc.VPCService, vpcModel *model.Vpc) (string, error) {
					return "", errors.New("SNAT IP not exist")
				})
				return patch
			},
			expErr: "SNAT IP not exist",
			expVpcState: &v1alpha1.VPCState{
				Name:                    *vpcModel.DisplayName,
				VPCPath:                 *vpcModel.Path,
				DefaultSNATIP:           "",
				LoadBalancerIPAddresses: "",
				PrivateIPv4CIDRs:        vpcModel.PrivateIps,
			},
		},
		{
			name: "failed to get LB endpoint",
			mockServiceFunc: func() *gomonkey.Patches {
				patch := gomonkey.ApplyMethod(reflect.TypeOf(service), "GetDefaultSNATIP", func(_ *vpc.VPCService, vpcModel *model.Vpc) (string, error) {
					return "100.100.100.11", nil
				})
				patch.ApplyMethod(reflect.TypeOf(service), "GetAVISubnetInfo", func(_ *vpc.VPCService, vpcModel *model.Vpc) (string, string, error) {
					return "", "", errors.New("AVI endpoint not found")
				})
				return patch
			},
			expErr: "AVI endpoint not found",
			expVpcState: &v1alpha1.VPCState{
				Name:                    *vpcModel.DisplayName,
				VPCPath:                 *vpcModel.Path,
				DefaultSNATIP:           "100.100.100.11",
				LoadBalancerIPAddresses: "",
				PrivateIPv4CIDRs:        vpcModel.PrivateIps,
			},
		},
		{
			name: "successful read",
			mockServiceFunc: func() *gomonkey.Patches {
				patch := gomonkey.ApplyMethod(reflect.TypeOf(service), "GetDefaultSNATIP", func(_ *vpc.VPCService, vpcModel *model.Vpc) (string, error) {
					return "100.100.100.11", nil
				})
				patch.ApplyMethod(reflect.TypeOf(service), "GetAVISubnetInfo", func(_ *vpc.VPCService, vpcModel *model.Vpc) (string, string, error) {
					return "avi-path", "99.99.99.0/28", nil
				})
				return patch
			},
			expErr:    "",
			expLBPath: "avi-path",
			expVpcState: &v1alpha1.VPCState{
				Name:                    *vpcModel.DisplayName,
				VPCPath:                 *vpcModel.Path,
				DefaultSNATIP:           "100.100.100.11",
				LoadBalancerIPAddresses: "99.99.99.0/28",
				PrivateIPv4CIDRs:        vpcModel.PrivateIps,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patch := tc.mockServiceFunc()
			defer patch.Reset()
			vpcState, lbPath, err := r.getVPCState(vpcModel)
			if tc.expErr != "" {
				assert.EqualError(t, err, tc.expErr)
			} else {
				assert.Equal(t, tc.expLBPath, lbPath)
			}
			assert.Equal(t, tc.expVpcState, vpcState)
		})
	}
}

func TestSyncPreCreatedVPCState(t *testing.T) {
	vpc1Path := "vpc-path1"
	vpc2Path := "vpc-path2"
	service := &vpc.VPCService{
		Service: common.Service{
			NSXConfig: &config.NSXOperatorConfig{
				NsxConfig: &config.NsxConfig{
					EnforcementPoint: "vmc-enforcementpoint",
				},
			},
		},
	}
	mockCtl := gomock.NewController(t)
	k8sClient := mock_client.NewMockClient(mockCtl)
	r := &NetworkInfoReconciler{
		Client:  k8sClient,
		Scheme:  nil,
		Service: service,
	}
	updateCalled := 0
	for _, tc := range []struct {
		name             string
		mockResetFunc    func() func()
		expUpdatedCalled int
	}{
		{
			name: "empty VPC path and Namespaces mapping is returned",
			mockResetFunc: func() func() {
				patch := gomonkey.ApplyMethod(reflect.TypeOf(service), "ListNamespacesWithPreCreatedVPC", func(_ *vpc.VPCService) map[string][]string {
					return map[string][]string{}
				})
				patch.ApplyMethod(reflect.TypeOf(service), "GetVPCFromNSXByPath", func(_ *vpc.VPCService, vpcPath string) (*model.Vpc, error) {
					assert.FailNow(t, "GetVPCFromNSXByPath is not supposed to be called with empty pre-created VPC mappings")
					return nil, nil
				})

				return func() {
					patch.Reset()
				}
			},
		},
		{
			name: "failed to get pre-created VPC from NSX",
			mockResetFunc: func() func() {
				patch := gomonkey.ApplyMethod(reflect.TypeOf(service), "ListNamespacesWithPreCreatedVPC", func(_ *vpc.VPCService) map[string][]string {
					return map[string][]string{
						vpc1Path: {"ns1", "ns2"},
					}
				})
				patch.ApplyMethod(reflect.TypeOf(service), "GetVPCFromNSXByPath", func(_ *vpc.VPCService, vpcPath string) (*model.Vpc, error) {
					return nil, errors.New("unable to get VPC")
				})
				patch2 := gomonkey.ApplyFunc(updateNetworkInfoVPCStates, func(r *NetworkInfoReconciler, ctx context.Context, ns string, vpcState *v1alpha1.VPCState) {
					assert.FailNow(t, "updateNetworkInfoVPCStates is not supposed to be called with empty pre-created VPC mappings")
				})

				return func() {
					patch.Reset()
					patch2.Reset()
				}
			},
		},
		{
			name: "failed to get VPC State with pre-created VPC model",
			mockResetFunc: func() func() {
				patch := gomonkey.ApplyMethod(reflect.TypeOf(service), "ListNamespacesWithPreCreatedVPC", func(_ *vpc.VPCService) map[string][]string {
					return map[string][]string{
						vpc1Path: {"ns1", "ns2"},
					}
				})
				patch.ApplyMethod(reflect.TypeOf(service), "GetVPCFromNSXByPath", func(_ *vpc.VPCService, vpcPath string) (*model.Vpc, error) {
					return &model.Vpc{
						Path:        &vpc2Path,
						DisplayName: common.String("vpc2"),
						PrivateIps:  []string{"1.1.1.0/24"},
					}, nil
				})
				patch2 := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getVPCState", func(_ *NetworkInfoReconciler, vpcModel *model.Vpc) (*v1alpha1.VPCState, string, error) {
					return nil, "", errors.New("unable to get VPC state")
				})
				patch3 := gomonkey.ApplyFunc(updateNetworkInfoVPCStates, func(r *NetworkInfoReconciler, ctx context.Context, ns string, vpcState *v1alpha1.VPCState) {
					assert.FailNow(t, "updateNetworkInfoVPCStates is not supposed to be called with empty pre-created VPC mappings")
				})

				return func() {
					patch.Reset()
					patch2.Reset()
					patch3.Reset()
				}
			},
		},
		{
			name: "failed to get VPC State with pre-created VPC model",
			mockResetFunc: func() func() {
				patch := gomonkey.ApplyMethod(reflect.TypeOf(service), "ListNamespacesWithPreCreatedVPC", func(_ *vpc.VPCService) map[string][]string {
					return map[string][]string{
						vpc1Path: {"ns1", "ns2"},
					}
				})
				patch.ApplyMethod(reflect.TypeOf(service), "GetVPCFromNSXByPath", func(_ *vpc.VPCService, vpcPath string) (*model.Vpc, error) {
					return &model.Vpc{
						Path:        &vpc2Path,
						DisplayName: common.String("vpc2"),
						PrivateIps:  []string{"1.1.1.0/24"},
					}, nil
				})
				patch2 := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getVPCState", func(_ *NetworkInfoReconciler, vpcModel *model.Vpc) (*v1alpha1.VPCState, string, error) {
					return &v1alpha1.VPCState{
						Name:                    *vpcModel.DisplayName,
						VPCPath:                 *vpcModel.Path,
						DefaultSNATIP:           "AVI endpoint not found",
						LoadBalancerIPAddresses: "99.99.99.0/28",
						PrivateIPv4CIDRs:        vpcModel.PrivateIps,
					}, "", nil
				})
				patch3 := gomonkey.ApplyFunc(updateNetworkInfoVPCStates, func(r *NetworkInfoReconciler, ctx context.Context, ns string, vpcState *v1alpha1.VPCState) {
					updateCalled += 1
				})

				return func() {
					patch.Reset()
					patch2.Reset()
					patch3.Reset()
				}
			},
			expUpdatedCalled: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			updateCalled = 0
			resetFunc := tc.mockResetFunc()
			defer resetFunc()
			r.SyncPreCreatedVPCState(ctx)
			assert.Equal(t, tc.expUpdatedCalled, updateCalled)
		})
	}

}
