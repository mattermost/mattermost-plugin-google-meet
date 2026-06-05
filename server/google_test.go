// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

func TestCreateMeeting_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v2/spaces", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, string(body))

		resp := meetSpaceResponse{
			Name:        "spaces/abc123",
			MeetingURI:  "https://meet.google.com/abc-defg-hij",
			MeetingCode: "abc-defg-hij",
		}
		w.WriteHeader(http.StatusOK)
		err = json.NewEncoder(w).Encode(resp)
		require.NoError(t, err)
	}))
	defer server.Close()

	// Override the module-level URL and client for testing
	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() {
		googleMeetURL = origURL
		httpClient = origClient
	}()

	p := &Plugin{}
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Hour),
	}

	meetURL, spaceName, err := p.createMeeting(token, "my topic")
	require.NoError(t, err)
	assert.Equal(t, "https://meet.google.com/abc-defg-hij", meetURL)
	assert.Equal(t, "spaces/abc123", spaceName)
}

func TestCreateMeeting_InsufficientScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte(`{"error":{"code":403,"message":"Request had insufficient authentication scopes.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`))
		require.NoError(t, err)
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() {
		googleMeetURL = origURL
		httpClient = origClient
	}()

	p := &Plugin{}
	token := &kvstore.OAuth2Token{
		AccessToken: "old-token",
		Expiry:      time.Now().Add(time.Hour),
	}

	_, _, err := p.createMeeting(token, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInsufficientScopes))
}

func TestCreateMeeting_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, err := w.Write([]byte(`{"error":"internal"}`))
		require.NoError(t, err)
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() {
		googleMeetURL = origURL
		httpClient = origClient
	}()

	p := &Plugin{}
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      time.Now().Add(time.Hour),
	}

	_, _, err := p.createMeeting(token, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "meet API returned status 500")
}

func TestCreateMeeting_EmptyMeetingURI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := meetSpaceResponse{Name: "spaces/abc123"}
		w.WriteHeader(http.StatusOK)
		err := json.NewEncoder(w).Encode(resp)
		require.NoError(t, err)
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() {
		googleMeetURL = origURL
		httpClient = origClient
	}()

	p := &Plugin{}
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      time.Now().Add(time.Hour),
	}

	_, _, err := p.createMeeting(token, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no meeting URI")
}

func TestCreateMeeting_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`not json`))
		require.NoError(t, err)
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() {
		googleMeetURL = origURL
		httpClient = origClient
	}()

	p := &Plugin{}
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      time.Now().Add(time.Hour),
	}

	_, _, err := p.createMeeting(token, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse Meet API response")
}

func TestExchangeCodeForToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)

		err := r.ParseForm()
		require.NoError(t, err)
		assert.Equal(t, "test-code", r.Form.Get("code"))
		assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))

		resp := tokenResponse{
			AccessToken:  "access-123",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "refresh-456",
		}
		w.WriteHeader(http.StatusOK)
		err = json.NewEncoder(w).Encode(resp)
		require.NoError(t, err)
	}))
	defer server.Close()

	origTokenURL := googleTokenURL
	origClient := httpClient
	googleTokenURL = server.URL
	httpClient = server.Client()
	defer func() {
		googleTokenURL = origTokenURL
		httpClient = origClient
	}()

	siteURL := "http://localhost:8065"
	p := &Plugin{}
	p.setConfiguration(&configuration{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		EncryptionKey:      "enc-key",
	})
	p.API = &mockPluginAPI{siteURL: siteURL}

	token, err := p.exchangeCodeForToken("test-code")
	require.NoError(t, err)
	assert.Equal(t, "access-123", token.AccessToken)
	assert.Equal(t, "Bearer", token.TokenType)
	assert.Equal(t, "refresh-456", token.RefreshToken)
	assert.True(t, token.Expiry.After(time.Now()))
}

func TestExchangeCodeForToken_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, err := w.Write([]byte(`{"error":"invalid_grant"}`))
		require.NoError(t, err)
	}))
	defer server.Close()

	origTokenURL := googleTokenURL
	origClient := httpClient
	googleTokenURL = server.URL
	httpClient = server.Client()
	defer func() {
		googleTokenURL = origTokenURL
		httpClient = origClient
	}()

	siteURL := "http://localhost:8065"
	p := &Plugin{}
	p.setConfiguration(&configuration{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		EncryptionKey:      "enc-key",
	})
	p.API = &mockPluginAPI{siteURL: siteURL}

	_, err := p.exchangeCodeForToken("bad-code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token exchange failed with status 400")
}

func TestExchangeCodeForToken_RequiresSiteURL(t *testing.T) {
	p := &Plugin{}
	p.setConfiguration(&configuration{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		EncryptionKey:      "enc-key",
	})
	p.API = &mockPluginAPI{}

	_, err := p.exchangeCodeForToken("test-code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "site URL")
}

func TestRefreshToken_KeepsExistingRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)
		assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		assert.Equal(t, "original-refresh", r.Form.Get("refresh_token"))

		resp := tokenResponse{
			AccessToken: "new-access",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
			// No refresh token in response
		}
		w.WriteHeader(http.StatusOK)
		encodeErr := json.NewEncoder(w).Encode(resp)
		require.NoError(t, encodeErr)
	}))
	defer server.Close()

	origTokenURL := googleTokenURL
	origClient := httpClient
	googleTokenURL = server.URL
	httpClient = server.Client()
	defer func() {
		googleTokenURL = origTokenURL
		httpClient = origClient
	}()

	p := &Plugin{}
	p.setConfiguration(&configuration{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		EncryptionKey:      "enc-key",
	})

	oldToken := &kvstore.OAuth2Token{
		AccessToken:  "old-access",
		TokenType:    "Bearer",
		RefreshToken: "original-refresh",
		Expiry:       time.Now().Add(-time.Hour),
	}

	newToken, err := p.refreshToken(oldToken)
	require.NoError(t, err)
	assert.Equal(t, "new-access", newToken.AccessToken)
	assert.Equal(t, "original-refresh", newToken.RefreshToken, "should keep original refresh token when none returned")
}

func TestRefreshToken_UpdatesRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := tokenResponse{
			AccessToken:  "new-access",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "new-refresh",
		}
		w.WriteHeader(http.StatusOK)
		err := json.NewEncoder(w).Encode(resp)
		require.NoError(t, err)
	}))
	defer server.Close()

	origTokenURL := googleTokenURL
	origClient := httpClient
	googleTokenURL = server.URL
	httpClient = server.Client()
	defer func() {
		googleTokenURL = origTokenURL
		httpClient = origClient
	}()

	p := &Plugin{}
	p.setConfiguration(&configuration{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		EncryptionKey:      "enc-key",
	})

	oldToken := &kvstore.OAuth2Token{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		Expiry:       time.Now().Add(-time.Hour),
	}

	newToken, err := p.refreshToken(oldToken)
	require.NoError(t, err)
	assert.Equal(t, "new-refresh", newToken.RefreshToken, "should use new refresh token when returned")
}

func TestGetValidToken_RefreshesWithinBuffer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)
		assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		assert.Equal(t, "original-refresh", r.Form.Get("refresh_token"))

		resp := tokenResponse{
			AccessToken:  "new-access",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "original-refresh",
		}
		w.WriteHeader(http.StatusOK)
		encodeErr := json.NewEncoder(w).Encode(resp)
		require.NoError(t, encodeErr)
	}))
	defer server.Close()

	origTokenURL := googleTokenURL
	origClient := httpClient
	googleTokenURL = server.URL
	httpClient = server.Client()
	defer func() {
		googleTokenURL = origTokenURL
		httpClient = origClient
	}()

	p, _, kv := setupPlugin(t)
	kv.tokens["user1"] = &kvstore.OAuth2Token{
		AccessToken:  "old-access",
		TokenType:    "Bearer",
		RefreshToken: "original-refresh",
		Expiry:       time.Now().Add(4 * time.Minute),
	}

	token, err := p.getValidToken("user1")
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "new-access", token.AccessToken)
	assert.Equal(t, "new-access", kv.tokens["user1"].AccessToken)
}

func TestExtractMeetingCode(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"abc-mnop-xyz", "abc-mnop-xyz"},
		{"https://meet.google.com/abc-mnop-xyz", "abc-mnop-xyz"},
		{"https://meet.google.com/abc-mnop-xyz?authuser=0", "abc-mnop-xyz"},
		{"https://meet.google.com/abc-mnop-xyz/", "abc-mnop-xyz"},
		{"http://meet.google.com/abc-mnop-xyz", "abc-mnop-xyz"},
		{"meet.google.com/abc-mnop-xyz", "abc-mnop-xyz"},
		{"  abc-mnop-xyz  ", "abc-mnop-xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.want, extractMeetingCode(tc.input))
		})
	}
}
