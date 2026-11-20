// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Tokens seeded without a scope trigger the tokeninfo backfill; keep it off the network.
	googleTokenInfoURL = "http://127.0.0.1:1" // #nosec G101 -- unroutable test address
	os.Exit(m.Run())
}
