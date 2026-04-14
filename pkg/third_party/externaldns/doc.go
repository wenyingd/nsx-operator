// Package externaldns contains code derived from Kubernetes ExternalDNS
// (https://github.com/kubernetes-sigs/external-dns, module sigs.k8s.io/external-dns)
// for use inside nsx-operator only.
//
// Subpackages:
//
//   - [github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/annotations]: annotation keys and hostname parsing from external-dns/source/annotations.
//   - [github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/endpoint]: Endpoint model and record helpers from external-dns/endpoint.
//   - [github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/source]: Gateway API hostname and route-status helpers aligned with external-dns/source/gateway.go.
//
// Each subpackage’s doc.go lists which symbols are direct copies from upstream versus modified or nsx-operator-only.
package externaldns
