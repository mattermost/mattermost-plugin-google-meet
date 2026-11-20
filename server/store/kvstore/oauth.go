// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package kvstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	stderrors "errors"
	"io"
	"time"

	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"
	"golang.org/x/crypto/pbkdf2"
)

const (
	// #nosec G101 -- KV key prefixes are identifiers, not secrets.
	oauthTokenPrefix = "oauth_token_"
	oauthStatePrefix = "oauth_state_"
	stateTTL         = 10 * time.Minute
	pbkdf2Iterations = 100000
)

// ErrEncryptionKeyNotConfigured is returned when token encryption is unavailable.
var ErrEncryptionKeyNotConfigured = stderrors.New("OAuth encryption key is not configured")

type Client struct {
	client        *pluginapi.Client
	encryptionKey string
}

func NewKVStore(client *pluginapi.Client, encryptionKey string) KVStore {
	return &Client{
		client:        client,
		encryptionKey: encryptionKey,
	}
}

func (kv *Client) requireEncryptionKey() error {
	if kv.encryptionKey == "" {
		return ErrEncryptionKeyNotConfigured
	}

	return nil
}

func (kv *Client) StoreOAuth2Token(userID string, token *OAuth2Token) error {
	if err := kv.requireEncryptionKey(); err != nil {
		return err
	}

	data, err := json.Marshal(token)
	if err != nil {
		return errors.Wrap(err, "failed to marshal token")
	}

	encrypted, err := encrypt(data, kv.encryptionKey)
	if err != nil {
		return errors.Wrap(err, "failed to encrypt token")
	}

	_, err = kv.client.KV.Set(oauthTokenPrefix+userID, encrypted)
	if err != nil {
		return errors.Wrap(err, "failed to store token")
	}
	return nil
}

func (kv *Client) GetOAuth2Token(userID string) (*OAuth2Token, error) {
	if err := kv.requireEncryptionKey(); err != nil {
		return nil, err
	}

	var encrypted []byte
	err := kv.client.KV.Get(oauthTokenPrefix+userID, &encrypted)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get token")
	}
	if len(encrypted) == 0 {
		return nil, nil
	}

	data, err := decrypt(encrypted, kv.encryptionKey)
	if err != nil {
		// Token was stored with a different encryption key; delete it so the user can re-connect
		kv.client.Log.Warn("Deleting OAuth token after decryption failure", "user_id", userID, "error", err.Error())
		if delErr := kv.client.KV.Delete(oauthTokenPrefix + userID); delErr != nil {
			kv.client.Log.Warn("Failed to delete OAuth token after decryption failure", "user_id", userID, "error", delErr.Error())
		}
		return nil, nil
	}

	var token OAuth2Token
	if err := json.Unmarshal(data, &token); err != nil {
		kv.client.Log.Warn("Deleting OAuth token after unmarshal failure", "user_id", userID, "error", err.Error())
		if delErr := kv.client.KV.Delete(oauthTokenPrefix + userID); delErr != nil {
			kv.client.Log.Warn("Failed to delete OAuth token after unmarshal failure", "user_id", userID, "error", delErr.Error())
		}
		return nil, nil
	}
	return &token, nil
}

// SetOAuth2TokenScope records the granted scope on the stored token, but only if it is still the
// token identified by accessToken. Returns false when the token was replaced or removed meanwhile.
func (kv *Client) SetOAuth2TokenScope(userID, accessToken, scope string) (bool, error) {
	if err := kv.requireEncryptionKey(); err != nil {
		return false, err
	}

	key := oauthTokenPrefix + userID
	var encrypted []byte
	if err := kv.client.KV.Get(key, &encrypted); err != nil {
		return false, errors.Wrap(err, "failed to get token")
	}
	if len(encrypted) == 0 {
		return false, nil
	}

	data, err := decrypt(encrypted, kv.encryptionKey)
	if err != nil {
		return false, errors.Wrap(err, "failed to decrypt token")
	}
	var token OAuth2Token
	if err = json.Unmarshal(data, &token); err != nil {
		return false, errors.Wrap(err, "failed to unmarshal token")
	}
	if token.AccessToken != accessToken {
		return false, nil
	}

	token.Scope = scope
	updated, err := json.Marshal(&token)
	if err != nil {
		return false, errors.Wrap(err, "failed to marshal token")
	}
	updatedEncrypted, err := encrypt(updated, kv.encryptionKey)
	if err != nil {
		return false, errors.Wrap(err, "failed to encrypt token")
	}

	saved, err := kv.client.KV.Set(key, updatedEncrypted, pluginapi.SetAtomic(encrypted))
	if err != nil {
		return false, errors.Wrap(err, "failed to store token")
	}
	return saved, nil
}

func (kv *Client) DeleteOAuth2Token(userID string) error {
	err := kv.client.KV.Delete(oauthTokenPrefix + userID)
	if err != nil {
		return errors.Wrap(err, "failed to delete token")
	}
	return nil
}

func (kv *Client) StoreOAuth2State(state string, userID string) error {
	_, err := kv.client.KV.Set(
		oauthStatePrefix+state,
		[]byte(userID),
		pluginapi.SetExpiry(stateTTL),
	)
	if err != nil {
		return errors.Wrap(err, "failed to store OAuth state")
	}
	return nil
}

func (kv *Client) GetAndDeleteOAuth2State(state string) (string, error) {
	var userID []byte
	err := kv.client.KV.Get(oauthStatePrefix+state, &userID)
	if err != nil {
		return "", errors.Wrap(err, "failed to get OAuth state")
	}
	if userID == nil {
		return "", ErrStateNotFound
	}

	if err := kv.client.KV.Delete(oauthStatePrefix + state); err != nil {
		return "", errors.Wrap(err, "failed to delete OAuth state")
	}

	return string(userID), nil
}

// deriveKey uses PBKDF2-SHA256 with a plugin-specific static salt and 100k
// iterations to turn the configured secret into a 32-byte AES-256 key. The
// fixed salt namespaces the derivation to this plugin, while PBKDF2 adds
// computational cost beyond a raw hash without requiring extra persisted state.
func deriveKey(encryptionKey string) []byte {
	return pbkdf2.Key(
		[]byte(encryptionKey),
		[]byte("com.mattermost.google-meet/oauth-token"),
		pbkdf2Iterations,
		32,
		sha256.New,
	)
}

func encrypt(data []byte, encryptionKey string) ([]byte, error) {
	key := deriveKey(encryptionKey)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, data, nil), nil
}

func decrypt(data []byte, encryptionKey string) ([]byte, error) {
	key := deriveKey(encryptionKey)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
