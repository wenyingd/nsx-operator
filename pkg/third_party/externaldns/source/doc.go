// Package source implements Gateway API hostname and status checks aligned with ExternalDNS’s gateway route source.
// Primary upstream reference: sigs.k8s.io/external-dns/source/gateway.go (plus gateway_hostname.go for ASCII lower-case).
//
// # Direct copy from external-dns (logic preserved; names may differ — exported copies listed)
//
//	ToLowerCaseASCII — upstream unexported toLowerCaseASCII in source/gateway_hostname.go (same implementation; exported here).
//	GwMatchingHost — upstream gwMatchingHost.
//	GatewayCanonicalHost — upstream gwHost (exported); gatewayCanonicalHost, isDNS1123Domain, isDNS1123Label, isAlphaNum — same private helpers as upstream gateway.go.
//	isGatewayHostIP — upstream isIPAddr; implementation calls this repo’s endpoint.SuitableType (not upstream endpoint import).
//	ParentRefMatchesGateway — ref/group/kind/name/namespace checks aligned with upstream Gateway parent ref conventions (factored helper for nsx-operator; not a single upstream function name).
//	conditionTrue — same logic as upstream conditionStatusIsTrue (renamed).
//
// # Modified from external-dns
//
//	HTTPRouteParentReadyForGateway — requires RouteConditionProgrammed=True in addition to RouteConditionAccepted=True (upstream parent gate in gateway.go checks Accepted; nsx-operator adds Programmed).
//	GRPCRouteParentReadyForGateway, TLSRouteParentReadyForGateway — same Accepted+Programmed rule as HTTPRouteParentReadyForGateway.
//	RouteHostnames, RouteHostnamesForRoute, mergeGatewayRouteHostnameAnnotations — derived from (*gatewayRouteResolver).hosts(): FQDN template expansion is not inlined; pass fqdnTemplateHostnames into RouteHostnamesForRoute; invalid gateway-hostname-source uses log/slog instead of logrus.
//	appendAnnotationHostsDefault — derived from upstream default hosts() merge; modified so non-empty external-dns.alpha.kubernetes.io/hostname values are prepended before spec/template names (upstream appends after spec) for clearer DNS override semantics in nsx-operator.
//	GetDesiredHostnames — composes HostnamesFromAnnotations + spec list (Ingress-style pattern); not a single upstream symbol.
//	normalizeHostnamesList, NormalizeHostnameStrings — local normalization helpers.
//	GatewayListenerHostnames, ListenerSetEntryHostnames, RouteSpecHostnames — spec extraction helpers used by nsx-operator (not separate upstream exported helpers).
//
// # nsx-operator admission (Gateway API DNS scope)
//
//	CollectAdmissionHostnameFilters — builds the listener hostname allow-list (nil Hostname → "", invalid hosts skipped); factored for nsx-operator from ExternalDNS gateway listener + gwMatchingHost usage.
//	RouteHostnamesMatchingAdmission — applies GwMatchingHost per route hostname token, skips empty+empty pairs like matchRouteToListener; empty route host with multiple listener hosts yields multiple names; wildcard DNS names require external-dns.alpha.kubernetes.io/hostname (non-empty).
//	admissionMatchesForRouteHost, hostnameMoreSpecific — local helpers for the above.
package source
