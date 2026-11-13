// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package kvstore

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDecryptE2ECryptoVector pins the token storage format the Playwright e2e suite relies on to
// seed connected users directly into the plugin KV store: the encryption, and the JSON field names
// of OAuth2Token. The vector in testdata/e2e_crypto_vector.json is also checked by the e2e suite
// (e2e/playwright/script/verify_crypto_vector.ts), so a change on either side fails one of the two.
func TestDecryptE2ECryptoVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/e2e_crypto_vector.json")
	require.NoError(t, err)
	var vector struct {
		Key           string `json:"key"`
		Plaintext     string `json:"plaintext"`
		CiphertextHex string `json:"ciphertextHex"`
	}
	require.NoError(t, json.Unmarshal(raw, &vector))

	sealed, err := hex.DecodeString(vector.CiphertextHex)
	require.NoError(t, err)

	decrypted, err := decrypt(sealed, vector.Key)
	require.NoError(t, err)
	require.Equal(t, vector.Plaintext, string(decrypted))

	var token OAuth2Token
	require.NoError(t, json.Unmarshal(decrypted, &token))
	require.Equal(t, OAuth2Token{
		AccessToken:  "e2e-access",
		TokenType:    "Bearer",
		RefreshToken: "e2e-refresh",
		Expiry:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, token)
}
