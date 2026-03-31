/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vmware-tanzu/nsx-operator/pkg/config"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

// Test_DNSRecordService_manualConstruction mirrors the post-init shape of InitializeDNSRecordService without
// spawning async NSX ListResourceStore work (gomonkey on embedded *common.Service receivers is unreliable).
func Test_DNSRecordService_manualConstruction(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()

	commonService := common.Service{
		Client:    fc,
		NSXConfig: &config.NSXOperatorConfig{CoeConfig: &config.CoeConfig{}},
	}
	s := &DNSRecordService{
		Service:        commonService,
		DNSRecordStore: BuildDNSRecordStore(),
	}
	require.NotNil(t, s.DNSRecordStore)
	assert.Nil(t, s.DNSRecordStore.BindingType)
}
