/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
)

func Test_DNSRecordComparable_Key(t *testing.T) {
	tests := []struct {
		name string
		rec  *DNSRecord
		want string
	}{
		{
			name: "all fields set",
			rec: &DNSRecord{
				DnsZonePath:  strPtr("/zone/test"),
				Fqdn:         strPtr("svc.example.com"),
				RecordType:   strPtr("A"),
				RecordValues: []string{"1.2.3.4"},
			},
			want: "/zone/test|svc.example.com|A|1.2.3.4",
		},
		{
			name: "nil fields produce empty segments",
			rec:  &DNSRecord{},
			want: "|||",
		},
		{
			name: "multi-value record joins values with comma",
			rec: &DNSRecord{
				DnsZonePath:  strPtr("/zone/test"),
				Fqdn:         strPtr("svc.example.com"),
				RecordType:   strPtr("A"),
				RecordValues: []string{"1.2.3.4", "5.6.7.8"},
			},
			want: "/zone/test|svc.example.com|A|1.2.3.4,5.6.7.8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := (*DNSRecordComparable)(tt.rec)
			assert.Equal(t, tt.want, c.Key())
		})
	}
}

func Test_DNSRecordComparable_Value_NonNil(t *testing.T) {
	var c *DNSRecordComparable
	assert.Nil(t, c.Value())

	rec := &DNSRecord{
		DnsZonePath:  strPtr("/zone/test"),
		Fqdn:         strPtr("svc.example.com"),
		RecordType:   strPtr("A"),
		RecordValues: []string{"1.2.3.4"},
	}
	c = (*DNSRecordComparable)(rec)
	assert.NotNil(t, c.Value())
}

// Test_CompareResource exercises Value() indirectly through the common diff logic.
func Test_CompareResource(t *testing.T) {
	makeC := func(ip string) common.Comparable {
		return DNSRecordToComparable(&DNSRecord{
			DnsZonePath:  strPtr("/zone/test"),
			Fqdn:         strPtr("svc.example.com"),
			RecordType:   strPtr("A"),
			RecordValues: []string{ip},
		})
	}

	t.Run("identical records are not changed", func(t *testing.T) {
		assert.False(t, common.CompareResource(makeC("1.2.3.4"), makeC("1.2.3.4")))
	})
	t.Run("different RecordValues are changed", func(t *testing.T) {
		assert.True(t, common.CompareResource(makeC("1.2.3.4"), makeC("5.6.7.8")))
	})
}

func Test_DNSRecordToComparable_RoundTrip(t *testing.T) {
	rec := &DNSRecord{
		Id:           strPtr("rec-1"),
		Fqdn:         strPtr("svc.example.com"),
		DnsZonePath:  strPtr("/zone/test"),
		RecordType:   strPtr("A"),
		RecordValues: []string{"1.2.3.4"},
	}
	c := DNSRecordToComparable(rec)
	require.NotNil(t, c)
	got := ComparableToDNSRecord(c)
	assert.Equal(t, rec, got)
}
