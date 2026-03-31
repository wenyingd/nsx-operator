/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"encoding/json"
	"strings"

	"github.com/vmware/vsphere-automation-sdk-go/runtime/data"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

type Comparable = common.Comparable

// DNSRecordComparable is a type alias for DNSRecord that implements the Comparable interface.
// The logical key is zone|fqdn|recordType|recordValues so compare can match records across reconciles
// even when the server-generated Id changes.
type DNSRecordComparable DNSRecord

// Key returns a logical identity string used to match expected and existing records during diff.
func (d *DNSRecordComparable) Key() string {
	return strings.Join([]string{
		ptrStr(d.DnsZonePath),
		ptrStr(d.Fqdn),
		ptrStr(d.RecordType),
		strings.Join(d.RecordValues, ","),
	}, "|")
}

// Value returns a JSON-encoded data value of the comparison-relevant fields.
func (d *DNSRecordComparable) Value() data.DataValue {
	if d == nil {
		return nil
	}
	s := &DNSRecordComparable{
		DnsZonePath:  d.DnsZonePath,
		Fqdn:         d.Fqdn,
		RecordType:   d.RecordType,
		RecordValues: d.RecordValues,
	}
	b, _ := json.Marshal(s)
	return data.NewStringValue(string(b))
}

// DNSRecordToComparable converts a *DNSRecord to a Comparable via a zero-copy type cast.
func DNSRecordToComparable(rec *DNSRecord) Comparable {
	return (*DNSRecordComparable)(rec)
}

// ComparableToDNSRecord converts a Comparable back to a *DNSRecord.
func ComparableToDNSRecord(c Comparable) *DNSRecord {
	return (*DNSRecord)(c.(*DNSRecordComparable))
}
