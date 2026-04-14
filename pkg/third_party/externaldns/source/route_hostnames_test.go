/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package source

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	extann "github.com/vmware-tanzu/nsx-operator/pkg/third_party/externaldns/annotations"
)

func TestRouteHostnames_defaultWithAnnotation(t *testing.T) {
	meta := &metav1.ObjectMeta{
		Namespace: "ns1",
		Name:      "r1",
		Annotations: map[string]string{
			extann.HostnameKey: "ann.example.com",
		},
	}
	h, err := RouteHostnames(meta, nil, false)
	require.NoError(t, err)
	assert.Contains(t, h, "")
	assert.Contains(t, h, "ann.example.com")
	// Annotation hostnames are prepended before the empty-spec placeholder.
	assert.Equal(t, "ann.example.com", h[0])
}

func TestRouteHostnames_defaultAnnotationPrependedBeforeSpec(t *testing.T) {
	meta := &metav1.ObjectMeta{
		Namespace:   "ns1",
		Name:        "r1",
		Annotations: map[string]string{extann.HostnameKey: "ann.example.com"},
	}
	h, err := RouteHostnames(meta, []string{"spec.example.com"}, false)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(h), 2)
	assert.Equal(t, "ann.example.com", h[0])
	assert.Contains(t, h, "spec.example.com")
}

func TestRouteHostnames_definedHostsOnlyIgnoresHostnameAnnotation(t *testing.T) {
	meta := &metav1.ObjectMeta{
		Namespace: "ns1",
		Name:      "r1",
		Annotations: map[string]string{
			extann.GatewayHostnameSourceKey: GatewayHostnameSourceDefinedHostsOnly,
			extann.HostnameKey:              "ignored.example.com",
		},
	}
	h, err := RouteHostnames(meta, []string{"spec.example.com"}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"spec.example.com"}, h)
}

func TestRouteHostnames_annotationOnly(t *testing.T) {
	meta := &metav1.ObjectMeta{
		Namespace: "ns1",
		Name:      "r1",
		Annotations: map[string]string{
			extann.GatewayHostnameSourceKey: GatewayHostnameSourceAnnotationOnly,
			extann.HostnameKey:              "only.example.com",
		},
	}
	h, err := RouteHostnames(meta, []string{"spec.example.com"}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"only.example.com"}, h)
}

func TestRouteHostnamesForRoute_templateAppendsBeforeSource(t *testing.T) {
	meta := &metav1.ObjectMeta{
		Namespace:   "ns1",
		Name:        "r1",
		Annotations: map[string]string{extann.GatewayHostnameSourceKey: GatewayHostnameSourceDefinedHostsOnly},
	}
	h, err := RouteHostnamesForRoute(meta, []string{"spec.example.com"}, []string{"tpl.example.com"}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"spec.example.com", "tpl.example.com"}, h)
}

func TestRouteHostnamesForRoute_emptySpecPlaceholderUsesSpecOnly(t *testing.T) {
	meta := &metav1.ObjectMeta{
		Namespace:   "ns1",
		Name:        "r1",
		Annotations: map[string]string{extann.HostnameKey: "ann.example.com"},
	}
	h, err := RouteHostnamesForRoute(meta, nil, []string{"from.template.example.com"}, false)
	require.NoError(t, err)
	assert.Contains(t, h, "")
	assert.Contains(t, h, "ann.example.com")
	assert.Contains(t, h, "from.template.example.com")
}

func TestRouteHostnames_invalidGatewayHostnameSourceLogsAndFallsBack(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	meta := &metav1.ObjectMeta{
		Namespace: "ns1",
		Name:      "r1",
		Annotations: map[string]string{
			extann.GatewayHostnameSourceKey: "not-a-valid-mode",
			extann.HostnameKey:              "ann.example.com",
		},
	}
	h, err := RouteHostnames(meta, []string{"spec.example.com"}, false)
	require.NoError(t, err)
	assert.Contains(t, h, "spec.example.com")
	assert.Contains(t, h, "ann.example.com")
	assert.Contains(t, buf.String(), "invalid gateway-hostname-source")
	assert.Contains(t, buf.String(), "not-a-valid-mode")
}
