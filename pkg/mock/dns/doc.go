/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

// Package mockdns holds gomock mocks for pkg/nsx/services/dns.DNSRecordProvider.
//
// mock_dns_record_provider.go follows the golang/mock layout. golang/mock v1.6.0's mockgen
// does not emit valid Go for interface methods that return sets.Set[types.NamespacedName], so
// the file is maintained by hand; align it with DNSRecordProvider when that interface changes.
package mockdns
