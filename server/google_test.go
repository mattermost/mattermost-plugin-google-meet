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
			Scope:        meetScopeCreated,
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
	assert.Equal(t, meetScopeCreated, token.Scope)
}

func TestExchangeCodeForToken_CalendarScopeOptional(t *testing.T) {
	// The granted scope omits calendar.events.readonly (declined via Google's granular
	// consent screen); the exchange should still succeed since it isn't mandatory.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{
			AccessToken:  "access-123",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "refresh-456",
			Scope:        meetScopeCreated + " " + meetScopeReadonly,
		}
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(resp))
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
		GoogleClientID:                "client-id",
		GoogleClientSecret:            "client-secret",
		EncryptionKey:                 "enc-key",
		EnableConferenceArtifactPosts: true,
		EnableCalendarScheduleSync:    true,
	})
	p.API = &mockPluginAPI{siteURL: "http://localhost:8065"}

	token, err := p.exchangeCodeForToken("test-code")
	require.NoError(t, err)
	assert.Equal(t, "access-123", token.AccessToken)
}

func TestExchangeCodeForToken_MissingMandatoryScope(t *testing.T) {
	// The user declined meetings.space.created itself, which the plugin cannot work
	// without regardless of Calendar sync being enabled.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{
			AccessToken:  "access-123",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "refresh-456",
			Scope:        calendarScopeEventsReadonly,
		}
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(resp))
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
		GoogleClientID:             "client-id",
		GoogleClientSecret:         "client-secret",
		EncryptionKey:              "enc-key",
		EnableCalendarScheduleSync: true,
	})
	p.API = &mockPluginAPI{siteURL: "http://localhost:8065"}

	_, err := p.exchangeCodeForToken("test-code")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingMandatoryScopes))
	assert.Contains(t, err.Error(), meetScopeCreated)
}

func TestMissingScopes(t *testing.T) {
	required := []string{meetScopeCreated, meetScopeReadonly}

	assert.Empty(t, missingScopes(meetScopeCreated+" "+meetScopeReadonly+" "+calendarScopeEventsReadonly, required))
	assert.Equal(t, []string{meetScopeReadonly}, missingScopes(meetScopeCreated, required))
	assert.Equal(t, required, missingScopes("", required))
}

func TestTokenHasScopes(t *testing.T) {
	cases := []struct {
		name     string
		scope    string
		required []string
		want     bool
	}{
		{"unknown scope is treated as sufficient", "", []string{meetScopeReadonly}, true},
		{"all granted", meetScopeCreated + " " + meetScopeReadonly, []string{meetScopeCreated, meetScopeReadonly}, true},
		{"missing one", meetScopeCreated, []string{meetScopeCreated, meetScopeReadonly}, false},
		{"nothing required", meetScopeCreated, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tokenHasScopes(&kvstore.OAuth2Token{Scope: tc.scope}, tc.required...))
		})
	}
}

func TestTokenHasAnyScope(t *testing.T) {
	cases := []struct {
		name  string
		scope string
		want  bool
	}{
		{"unknown scope is treated as sufficient", "", true},
		{"first scope granted", meetScopeReadonly, true},
		{"second scope granted", meetScopeCreated + " " + calendarScopeEventsReadonly, true},
		{"neither granted", calendarScopeEventsReadonly, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := &kvstore.OAuth2Token{Scope: tc.scope}
			assert.Equal(t, tc.want, tokenHasAnyScope(token, meetScopeReadonly, meetScopeCreated))
		})
	}
}

func overrideTokenInfo(t *testing.T, handler http.HandlerFunc) *int {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		handler(w, r)
	}))
	origURL := googleTokenInfoURL
	origClient := httpClient
	googleTokenInfoURL = server.URL
	httpClient = server.Client()
	t.Cleanup(func() {
		googleTokenInfoURL = origURL
		httpClient = origClient
		server.Close()
	})
	return &calls
}

func TestGetValidToken_BackfillsMissingScope(t *testing.T) {
	calls := overrideTokenInfo(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "legacy-access", r.URL.Query().Get("access_token"))
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`{"scope":"` + meetScopeCreated + `","expires_in":"3500"}`))
		require.NoError(t, err)
	})

	p, _, kv := setupPlugin(t)
	kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "legacy-access", Expiry: time.Now().Add(time.Hour)}

	token, err := p.getValidToken("user1")
	require.NoError(t, err)
	assert.Equal(t, meetScopeCreated, token.Scope)
	assert.Equal(t, meetScopeCreated, kv.tokens["user1"].Scope)

	_, err = p.getValidToken("user1")
	require.NoError(t, err)
	assert.Equal(t, 1, *calls, "tokeninfo should only be called once per legacy token")
}

func TestGetValidToken_SkipsTokenInfoWhenScopeKnown(t *testing.T) {
	calls := overrideTokenInfo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	p, _, kv := setupPlugin(t)
	kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "access", Expiry: time.Now().Add(time.Hour), Scope: meetScopeCreated}

	token, err := p.getValidToken("user1")
	require.NoError(t, err)
	assert.Equal(t, meetScopeCreated, token.Scope)
	assert.Zero(t, *calls)
}

func TestGetValidToken_TokenInfoFailureIsNonFatal(t *testing.T) {
	overrideTokenInfo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, err := w.Write([]byte(`{"error":"invalid_token"}`))
		require.NoError(t, err)
	})

	p, _, kv := setupPlugin(t)
	kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "legacy-access", Expiry: time.Now().Add(time.Hour)}

	token, err := p.getValidToken("user1")
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Empty(t, token.Scope)
	assert.Empty(t, kv.tokens["user1"].Scope)
}

func TestGetValidToken_TokenInfoFailureBacksOff(t *testing.T) {
	calls := overrideTokenInfo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	p, _, kv := setupPlugin(t)
	kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "legacy-access", Expiry: time.Now().Add(time.Hour)}

	for range 3 {
		_, err := p.getValidToken("user1")
		require.NoError(t, err)
	}
	assert.Equal(t, 1, *calls, "failed lookups should not be retried until the backoff expires")

	p.scopeLookupRetryAt.Store("user1", time.Now().Add(-time.Second))
	_, err := p.getValidToken("user1")
	require.NoError(t, err)
	assert.Equal(t, 2, *calls, "lookup should be retried once the backoff expires")
}

func TestGetValidToken_BackfillDoesNotOverwriteNewerToken(t *testing.T) {
	var kv *mockKVStore
	overrideTokenInfo(t, func(w http.ResponseWriter, _ *http.Request) {
		// The user reconnects while the lookup is in flight.
		kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "new-access", Expiry: time.Now().Add(time.Hour), Scope: meetScopeCreated + " " + meetScopeReadonly}
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`{"scope":"` + meetScopeCreated + `"}`))
		require.NoError(t, err)
	})

	var p *Plugin
	p, _, kv = setupPlugin(t)
	kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "legacy-access", Expiry: time.Now().Add(time.Hour)}

	token, err := p.getValidToken("user1")
	require.NoError(t, err)
	assert.Equal(t, meetScopeCreated, token.Scope)

	stored := kv.tokens["user1"]
	assert.Equal(t, "new-access", stored.AccessToken)
	assert.Equal(t, meetScopeCreated+" "+meetScopeReadonly, stored.Scope)
}

func TestFetchTokenScope_RequestErrorOmitsAccessToken(t *testing.T) {
	origURL := googleTokenInfoURL
	googleTokenInfoURL = "http://127.0.0.1:1" // #nosec G101 -- unroutable test address
	defer func() { googleTokenInfoURL = origURL }()

	p := &Plugin{}
	_, err := p.fetchTokenScope(&kvstore.OAuth2Token{AccessToken: "secret-access-token"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-access-token")
}

func TestMeetGet_PermissionDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"reason":"PERMISSION_DENIED_ON_RESOURCE"}]}}`))
		require.NoError(t, err)
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	p := &Plugin{}
	_, err := p.getSpace(&kvstore.OAuth2Token{AccessToken: "access"}, "abc-mnop-xyz")
	require.ErrorIs(t, err, ErrPermissionDenied)
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
		Scope:        meetScopeCreated,
	}

	newToken, err := p.refreshToken(oldToken)
	require.NoError(t, err)
	assert.Equal(t, "new-access", newToken.AccessToken)
	assert.Equal(t, "original-refresh", newToken.RefreshToken, "should keep original refresh token when none returned")
	assert.Equal(t, meetScopeCreated, newToken.Scope, "should keep original scope when none returned")
}

func TestRefreshToken_UpdatesScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "new-access",
			ExpiresIn:   3600,
			Scope:       meetScopeCreated + " " + meetScopeReadonly,
		}))
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
	p.setConfiguration(&configuration{GoogleClientID: "client-id", GoogleClientSecret: "client-secret"})

	newToken, err := p.refreshToken(&kvstore.OAuth2Token{RefreshToken: "refresh"})
	require.NoError(t, err)
	assert.Equal(t, meetScopeCreated+" "+meetScopeReadonly, newToken.Scope)
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
