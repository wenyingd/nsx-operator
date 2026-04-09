/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

// InitializeDNSRecordService constructs DNSRecordService, then loads DNS records created by nsx-operator into
// DNSRecordStore via InitializeResourceStore.
func InitializeDNSRecordService(commonService common.Service, vpcService common.VPCServiceProvider) (*DNSRecordService, error) {
	s := &DNSRecordService{
		Service:        commonService,
		DNSRecordStore: BuildDNSRecordStore(),
	}

	// TODO [VCFN-2809]: initialize s.DNSRecordStore from NSX DNS records after NSX DNS are ready
	return s, nil
}
