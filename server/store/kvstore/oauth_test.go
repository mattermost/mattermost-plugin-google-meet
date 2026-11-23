// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package kvstore

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKVAPI implements only the KV calls SetOAuth2TokenScope makes; any other call panics.
type fakeKVAPI struct {
	plugin.API
	value   []byte
	setOK   bool
	setOpts *model.PluginKVSetOptions
	setVal  []byte
}

func (f *fakeKVAPI) KVGet(_ string) ([]byte, *model.AppError) { return f.value, nil }

func (f *fakeKVAPI) KVSetWithOptions(_ string, value []byte, opts model.PluginKVSetOptions) (bool, *model.AppError) {
	f.setVal = value
	f.setOpts = &opts
	return f.setOK, nil
}

func TestSetOAuth2TokenScope(t *testing.T) {
	const encKey = "test-encryption-key"
	stored := func(t *testing.T, accessToken string) []byte {
		t.Helper()
		data, err := json.Marshal(&OAuth2Token{AccessToken: accessToken})
		require.NoError(t, err)
		encrypted, err := encrypt(data, encKey)
		require.NoError(t, err)
		return encrypted
	}

	t.Run("writes scope atomically when the token is unchanged", func(t *testing.T) {
		current := stored(t, "access")
		api := &fakeKVAPI{value: current, setOK: true}
		kv := NewKVStore(pluginapi.NewClient(api, nil), encKey)

		saved, err := kv.SetOAuth2TokenScope("user1", "access", "scope-a")
		require.NoError(t, err)
		assert.True(t, saved)

		require.NotNil(t, api.setOpts)
		assert.True(t, api.setOpts.Atomic)
		assert.True(t, bytes.Equal(current, api.setOpts.OldValue))
		data, err := decrypt(api.setVal, encKey)
		require.NoError(t, err)
		var token OAuth2Token
		require.NoError(t, json.Unmarshal(data, &token))
		assert.Equal(t, "access", token.AccessToken)
		assert.Equal(t, "scope-a", token.Scope)
	})

	t.Run("skips when the stored token was replaced", func(t *testing.T) {
		api := &fakeKVAPI{value: stored(t, "newer-access")}
		kv := NewKVStore(pluginapi.NewClient(api, nil), encKey)

		saved, err := kv.SetOAuth2TokenScope("user1", "access", "scope-a")
		require.NoError(t, err)
		assert.False(t, saved)
		assert.Nil(t, api.setOpts, "should not write")
	})

	t.Run("reports a lost compare-and-set race", func(t *testing.T) {
		api := &fakeKVAPI{value: stored(t, "access"), setOK: false}
		kv := NewKVStore(pluginapi.NewClient(api, nil), encKey)

		saved, err := kv.SetOAuth2TokenScope("user1", "access", "scope-a")
		require.NoError(t, err)
		assert.False(t, saved)
	})
}

func TestEncryptDecrypt(t *testing.T) {
	key := "test-encryption-key"
	plaintext := []byte("hello, world!")

	encrypted, err := encrypt(plaintext, key)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, encrypted)

	decrypted, err := decrypt(encrypted, key)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptDecrypt_DifferentKeys(t *testing.T) {
	plaintext := []byte("secret data")

	encrypted, err := encrypt(plaintext, "key1")
	require.NoError(t, err)

	_, err = decrypt(encrypted, "key2")
	assert.Error(t, err, "decrypting with a different key should fail")
}

func TestEncryptDecrypt_EmptyData(t *testing.T) {
	key := "test-key"

	encrypted, err := encrypt([]byte{}, key)
	require.NoError(t, err)

	decrypted, err := decrypt(encrypted, key)
	require.NoError(t, err)
	assert.Empty(t, decrypted)
}

func TestEncrypt_ProducesDifferentCiphertext(t *testing.T) {
	key := "test-key"
	plaintext := []byte("same data")

	enc1, err := encrypt(plaintext, key)
	require.NoError(t, err)

	enc2, err := encrypt(plaintext, key)
	require.NoError(t, err)

	assert.NotEqual(t, enc1, enc2, "encrypting same data twice should produce different ciphertext due to random nonce")
}

func TestDecrypt_CiphertextTooShort(t *testing.T) {
	_, err := decrypt([]byte{1, 2}, "key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ciphertext too short")
}

func TestDeriveKey_Deterministic(t *testing.T) {
	key1 := deriveKey("my-secret")
	key2 := deriveKey("my-secret")
	assert.Equal(t, key1, key2)
	assert.Len(t, key1, 32, "derived key should be 32 bytes (AES-256)")
}

func TestDeriveKey_DifferentInputs(t *testing.T) {
	key1 := deriveKey("key-a")
	key2 := deriveKey("key-b")
	assert.NotEqual(t, key1, key2)
}
