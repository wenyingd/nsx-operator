/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_isDomainMatch(t *testing.T) {
	tests := []struct {
		fqdn   string
		domain string
		want   bool
	}{
		{"svc.example.com", "example.com", true},
		{"example.com", "example.com", true},           // exact match
		{"SVC.EXAMPLE.COM", "example.com", true},       // case-insensitive
		{"svc.example.com.", "example.com.", true},     // trailing dots
		{"notexample.com", "example.com", false},       // suffix but not a subdomain
		{"other.com", "example.com", false},
		{"svc.example.com", "", false},                 // empty domain always false
		{"", "example.com", false},
		{"a.b.example.com", "example.com", true},       // deeper subdomain
	}
	for _, tt := range tests {
		t.Run(tt.fqdn+"_"+tt.domain, func(t *testing.T) {
			assert.Equal(t, tt.want, isDomainMatch(tt.fqdn, tt.domain))
		})
	}
}

func Test_normalizeDomain(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Example.Com.", "example.com"},
		{"  example.com  ", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		{"example.com", "example.com"},
		{"example.com.", "example.com"}, // trailing dot stripped
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeDomain(tt.input))
		})
	}
}

func Test_GetDNSDomain(t *testing.T) {
	zones := []string{"example.com", "sub.example.com", "other.org"}
	tests := []struct {
		fqdn string
		want string
	}{
		{"svc.sub.example.com", "sub.example.com"}, // longest match wins
		{"svc.example.com", "example.com"},
		{"api.other.org", "other.org"},
		{"svc.unknown.net", ""}, // no match
		{"sub.example.com", "sub.example.com"}, // exact match on longer zone
	}
	for _, tt := range tests {
		t.Run(tt.fqdn, func(t *testing.T) {
			assert.Equal(t, tt.want, GetDNSDomain(tt.fqdn, zones))
		})
	}
}

func Test_filterValidFQDNs_packageLevel(t *testing.T) {
	zones := []string{"example.com", "other.org"}

	t.Run("nil hostnames returns empty slices", func(t *testing.T) {
		valid, invalid := filterValidFQDNs(nil, zones)
		assert.Empty(t, valid)
		assert.Empty(t, invalid)
	})
	t.Run("mixed valid and invalid", func(t *testing.T) {
		valid, invalid := filterValidFQDNs([]string{"svc.example.com", "bad.net", "api.other.org"}, zones)
		assert.ElementsMatch(t, []string{"svc.example.com", "api.other.org"}, valid)
		assert.ElementsMatch(t, []string{"bad.net"}, invalid)
	})
	t.Run("all valid", func(t *testing.T) {
		valid, invalid := filterValidFQDNs([]string{"a.example.com", "b.example.com"}, zones)
		assert.Len(t, valid, 2)
		assert.Empty(t, invalid)
	})
	t.Run("all invalid", func(t *testing.T) {
		valid, invalid := filterValidFQDNs([]string{"a.bad.net", "b.bad.net"}, zones)
		assert.Empty(t, valid)
		assert.Len(t, invalid, 2)
	})
}
