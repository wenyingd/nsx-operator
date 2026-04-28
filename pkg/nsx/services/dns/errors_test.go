/* Copyright © 2026 Broadcom, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package dns

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDNSZoneValidationError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("inner")
	err := &DNSZoneValidationError{Msg: "outer", Cause: inner}
	var d *DNSZoneValidationError
	require.ErrorAs(t, err, &d)
	require.ErrorIs(t, err, inner)
}
