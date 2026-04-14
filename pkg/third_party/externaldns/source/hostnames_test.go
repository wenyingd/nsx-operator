/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package source

import (
	"testing"

	"github.com/stretchr/testify/assert"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestRouteSpecHostnames(t *testing.T) {
	h := gatewayv1.Hostname("  foo.example.com ")
	assert.Equal(t, []string{"foo.example.com"}, RouteSpecHostnames([]gatewayv1.Hostname{h}))
	assert.Nil(t, RouteSpecHostnames(nil))
}

func TestGatewayListenerHostnames(t *testing.T) {
	h := gatewayv1.Hostname("gw.example.com")
	ls := []gatewayv1.Listener{{Name: "l1", Hostname: &h}}
	assert.Equal(t, []string{"gw.example.com"}, GatewayListenerHostnames(ls))
}
