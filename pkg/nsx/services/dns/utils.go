/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"strings"
)

// GetDNSDomain returns the best-matching (longest) permitted zone domain for the given FQDN.
// Returns "" if no permitted zone matches.
func GetDNSDomain(fqdn string, permittedZones []string) string {
	normalized := normalizeDomain(fqdn)
	best := ""
	for _, zone := range permittedZones {
		nz := normalizeDomain(zone)
		if isDomainMatch(normalized, nz) && len(nz) > len(best) {
			best = zone
		}
	}
	return best
}

// filterValidFQDNs splits hostnames into those matching a permitted zone and those that do not.
func filterValidFQDNs(hostnames []string, permittedZones []string) (valid []string, invalid []string) {
	for _, h := range hostnames {
		if GetDNSDomain(h, permittedZones) != "" {
			valid = append(valid, h)
		} else {
			invalid = append(invalid, h)
		}
	}
	return
}

// normalizeDomain lowercases, trims whitespace, and strips a trailing dot.
func normalizeDomain(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	return strings.TrimSuffix(d, ".")
}

// isDomainMatch reports whether fqdn equals domain or is a subdomain of domain (both normalized).
func isDomainMatch(fqdn, domain string) bool {
	nFQDN := normalizeDomain(fqdn)
	nDomain := normalizeDomain(domain)
	if nDomain == "" {
		return false
	}
	return nFQDN == nDomain || strings.HasSuffix(nFQDN, "."+nDomain)
}
