/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package source

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestToLowerCaseASCII(t *testing.T) {
	assert.Equal(t, "foo.com", ToLowerCaseASCII("Foo.com"))
	assert.Equal(t, "foo.com", ToLowerCaseASCII("foo.com"))
}

func TestGwMatchingHost(t *testing.T) {
	tests := []struct {
		a, b string
		want string
		ok   bool
	}{
		{"*.example.com", "foo.example.com", "foo.example.com", true},
		{"foo.example.com", "*.example.com", "foo.example.com", true},
		{"foo.com", "bar.com", "", false},
		{"", "foo.com", "foo.com", true},
	}
	for _, tt := range tests {
		got, ok := GwMatchingHost(tt.a, tt.b)
		assert.Equal(t, tt.ok, ok)
		assert.Equal(t, tt.want, got)
	}
}

func TestRouteHostnames(t *testing.T) {
	meta := &metav1.ObjectMeta{
		Annotations: map[string]string{
			"gateway-hostname-source": "annotation-only",
			"hostname":                "anno.com",
		},
	}
	res := RouteHostnames(meta, []string{"spec.com"}, "gateway-hostname-source", "hostname", false)
	assert.Equal(t, []string{"anno.com"}, res)
}
