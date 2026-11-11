/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	apierrors "github.com/vmware/vsphere-automation-sdk-go/lib/vapi/std/errors"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"

	"github.com/vmware-tanzu/nsx-operator/pkg/apis/vpc/v1alpha1"
	nsxutil "github.com/vmware-tanzu/nsx-operator/pkg/nsx/util"
	extdns "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint"
	extprovider "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/provider"
)

// NSX Policy path: /orgs/{org}/projects/{project}/dns-services/{dnsService}/zones/{zoneId}
var dnsZonePathRe = regexp.MustCompile(`^/orgs/([^/]+)/projects/([^/]+)/dns-services/([^/]+)/zones/([^/]+)$`)

// parseDnsZonePath splits a Policy zone path into org, project, DNS service, and zone ID.
func parseDnsZonePath(zonePath string) (orgID, projectID, dnsServiceID, zoneID string, err error) {
	p := strings.TrimSpace(zonePath)
	if p == "" {
		return "", "", "", "", fmt.Errorf("empty DNS zone path")
	}
	matches := dnsZonePathRe.FindStringSubmatch(p)
	if len(matches) != 5 {
		return "", "", "", "", fmt.Errorf("invalid DNS zone path %q: expected /orgs/{org}/projects/{project}/dns-services/{dns-service}/zones/{zone}", zonePath)
	}
	return matches[1], matches[2], matches[3], matches[4], nil
}

// endpointDNSNameIsWildcard reports whether dnsName requests a wildcard apex record (e.g. "*.example.com").
// NSX DNS policy does not publish such names; ValidateEndpointsByZone skips them without error.
func endpointDNSNameIsWildcard(dnsName string) bool {
	h := strings.TrimSpace(dnsName)
	return strings.HasPrefix(strings.ToLower(h), "*.")
}

// dnsNameForZoneMatch normalizes hostname for FindZone (trim, strip trailing dot, strip leading "*." only).
func dnsNameForZoneMatch(hostname string) string {
	h := strings.TrimSpace(hostname)
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(h), "*.") && len(h) >= 2 {
		return h[2:]
	}
	return h
}

// getZonePathForHostname returns (relativeRecordName, zonePath, err) for hostname against z.
func (s *DNSRecordService) getZonePathForHostname(z extprovider.ZoneIDName, hostname string) (string, string, error) {
	name := dnsNameForZoneMatch(hostname)
	if name == "" {
		return "", "", fmt.Errorf("empty hostname")
	}

	zonePath, matchedDomain, normalizedFQDN := z.FindZone(name)
	if matchedDomain == "" {
		return "", "", fmt.Errorf("hostname %q does not match any allowed DNS domain in the namespace", hostname)
	}
	if normalizedFQDN == matchedDomain {
		return "", "", fmt.Errorf("hostname %q must not equal to the allowed DNS domain %q", hostname, matchedDomain)
	}
	suffix := "." + matchedDomain
	if !strings.HasSuffix(normalizedFQDN, suffix) || normalizedFQDN == suffix {
		return "", "", fmt.Errorf("hostname %q does not lie under matched zone %q", hostname, matchedDomain)
	}
	recordName := strings.TrimSuffix(normalizedFQDN, suffix)
	return recordName, zonePath, nil
}

// ValidateEndpointsByZone maps each endpoint to a zone and row; returns validated rows, invalid rows, path→domain map for permitted zones (from sync), and err. owner must be non-nil.
func (s *DNSRecordService) ValidateEndpointsByZone(namespace string, owner *ResourceRef, eps []*extdns.Endpoint) ([]EndpointRow, map[string]string, error) {
	log.Info("Validating DNS endpoints by zone", "namespace", namespace,
		"owner", owner.GetName(), "endpoints", len(eps))
	vpcConfig, err := s.VPCService.GetVPCNetworkConfigByNamespace(namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find VPCNetworkConfiguration for the Namespace %s", namespace)
	}

	allowedZones, err := s.SyncDNSZonesByVpcNetworkConfig(vpcConfig)
	if err != nil {
		return nil, nil, err
	}
	if len(allowedZones) == 0 {
		return nil, nil, &DNSZoneValidationError{Msg: fmt.Sprintf("no DNS zones are permitted for the namespace %s", namespace)}
	}
	z := generateZoneIdFromMap(allowedZones)

	var validRows []EndpointRow
	var validationErr error

	for i := range eps {
		ep := eps[i]
		if endpointDNSNameIsWildcard(ep.DNSName) {
			log.Info("Skipping DNS endpoint: wildcard DNS names are not supported for NSX DNS records", "dnsName", ep.DNSName)
			continue
		}
		recName, zonePath, parseErr := s.getZonePathForHostname(z, ep.DNSName)
		if parseErr != nil {
			if validationErr == nil {
				validationErr = &DNSZoneValidationError{Msg: parseErr.Error()}
			}
			continue
		}
		log.Debug("Mapped DNS endpoint to zone", "dnsName", ep.DNSName, "zonePath", zonePath, "recordName", recName)
		row := NewEndpointRow(ep, zonePath, recName)
		row.validatedZoneDomain = allowedZones[zonePath]
		validRows = append(validRows, *row)
	}
	return validRows, allowedZones, validationErr
}

// SyncDNSZonesByVpcNetworkConfig ensures DNSZoneMap entries for vpcConfig.Spec.DNSZones; returns path→domain map or err.
func (s *DNSRecordService) SyncDNSZonesByVpcNetworkConfig(vpcConfig *v1alpha1.VPCNetworkConfiguration) (map[string]string, error) {
	dnsZonePaths := vpcConfig.Spec.DNSZones
	if len(dnsZonePaths) == 0 {
		return nil, nil
	}
	log.Info("Syncing DNS zones for VPCNetworkConfig", "zones", len(dnsZonePaths))

	dnsZoneDomainMapping := make(map[string]string)
	for _, p := range dnsZonePaths {
		domain, err := s.getOrFetchDNSZone(p)
		if err != nil {
			var missing *DNSZoneNotFoundError
			if errors.As(err, &missing) {
				s.notifyDNSZoneChanges([]string{p})
				continue
			}
			return nil, err
		}
		dnsZoneDomainMapping[p] = domain
	}
	return dnsZoneDomainMapping, nil
}

// RefreshDNSZone refreshes an empty zone, or bypasses the caches when requested.
// On a non-NotFound refresh failure, a non-forced lookup returns the cached
// domain alongside the error so callers can retain it while reporting uncertainty.
func (s *DNSRecordService) RefreshDNSZone(path string, bypassStore bool) (string, error) {
	key := dnsZoneLockKey(path)
	recordArbitrator.lock(key)
	defer recordArbitrator.unlock(key)
	cached, found := s.DNSZoneMap.get(path)
	if !bypassStore && found && s.DNSRecordStore != nil && s.DNSRecordStore.HasActiveRecordsForZone(path) {
		return cached, nil
	}
	domain, err := s.refreshDNSZoneFromNSXLocked(path)
	var missing *DNSZoneNotFoundError
	if err != nil && !bypassStore && found && cached != "" && !errors.As(err, &missing) {
		return cached, err
	}
	return domain, err
}

// The zone lock also covers record writes through their store updates. Keep the
// lookup and NSX response publication together so an older GET cannot win later.
func (s *DNSRecordService) getOrFetchDNSZone(path string) (string, error) {
	key := dnsZoneLockKey(path)
	recordArbitrator.lock(key)
	defer recordArbitrator.unlock(key)
	if domain, found := s.DNSZoneMap.get(path); found {
		return domain, nil
	}
	return s.refreshDNSZoneFromNSXLocked(path)
}

func (s *DNSRecordService) refreshDNSZoneFromNSXLocked(path string) (string, error) {
	zone, err := s.getDNSZoneFromNSX(path)
	if err != nil {
		var missing *DNSZoneNotFoundError
		if errors.As(err, &missing) {
			s.DNSZoneMap.delete(path)
		}
		return "", err
	}
	if zone.DnsDomainName == nil || strings.TrimSpace(*zone.DnsDomainName) == "" {
		return "", fmt.Errorf("DNS zone %s returned from NSX with no domain name", path)
	}
	domain := strings.TrimSpace(*zone.DnsDomainName)
	s.DNSZoneMap.set(path, domain)
	return domain, nil
}

func (s *DNSRecordService) getDNSZoneFromNSX(zonePath string) (*model.DnsZone, error) {
	orgID, projectID, dnsServiceID, zoneID, err := parseDnsZonePath(zonePath)
	if err != nil {
		return nil, err
	}
	z, err := s.NSXClient.DnsZoneClient.Get(orgID, projectID, dnsServiceID, zoneID)
	if err != nil {
		translated := nsxutil.TransNSXApiError(err)
		var sdkNotFoundValue apierrors.NotFound
		var sdkNotFoundPointer *apierrors.NotFound
		isSDKNotFound := errors.As(err, &sdkNotFoundValue) || errors.As(err, &sdkNotFoundPointer)
		// TransNSXApiError handles SDK error values; normalize its pointer form too.
		if sdkNotFoundPointer != nil {
			translated = nsxutil.TransNSXApiError(*sdkNotFoundPointer)
		}
		var nsxErr *nsxutil.NSXApiError
		isNSXNotFound := errors.As(translated, &nsxErr) && nsxErr.Type() == apierrors.ErrorType_NOT_FOUND
		indexNotReady := isNSXNotFound && nsxErr.ApiError != nil && nsxErr.ErrorCode != nil && *nsxErr.ErrorCode == 500090
		if !indexNotReady && (isNSXNotFound || isSDKNotFound) {
			return nil, &DNSZoneNotFoundError{Path: zonePath, Err: translated}
		}
		return nil, translated
	}
	out := z
	return &out, nil
}

func generateZoneIdFromMap(allowedZones map[string]string) extprovider.ZoneIDName {
	z := make(extprovider.ZoneIDName)
	for zonePath, domainName := range allowedZones {
		z.Add(zonePath, domainName)
	}
	return z
}
