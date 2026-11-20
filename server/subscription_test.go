// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-google-meet/server/command"
	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

func subscriptionTestPlugin(t *testing.T, api *mockPluginAPI, kv *mockKVStore) *Plugin {
	t.Helper()
	p := &Plugin{}
	p.API = api
	p.setKVStore(kv)
	p.setConfiguration(&configuration{
		GoogleClientID:     "test-client-id",
		GoogleClientSecret: "test-client-secret",
		EncryptionKey:      "test-encryption-key",
	})
	return p
}

func TestRemoveSubscription_RejectsWrongChannel(t *testing.T) {
	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	sub := &kvstore.Subscription{
		SpaceID:     "spaces/abc123",
		MeetingCode: "abc-mnop-xyz",
		ChannelID:   "chan-owner",
		CreatedBy:   "user-owner",
		CreatedAt:   time.Now(),
	}
	require.NoError(t, kv.StoreSubscription(sub))
	require.NoError(t, kv.AddToUserSubscriptionIndex("user-owner", sub.SpaceID))

	p := subscriptionTestPlugin(t, api, kv)

	err := p.RemoveSubscription("user-intruder", "chan-other", "abc-mnop-xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no subscription")

	stored, getErr := kv.GetSubscription("spaces/abc123")
	require.NoError(t, getErr)
	require.NotNil(t, stored)
	assert.Equal(t, "chan-owner", stored.ChannelID)
}

func TestRemoveSubscription_SucceedsWhenChannelMatches(t *testing.T) {
	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	sub := &kvstore.Subscription{
		SpaceID:     "spaces/abc123",
		MeetingCode: "abc-mnop-xyz",
		ChannelID:   "chan-owner",
		CreatedBy:   "user-creator",
		CreatedAt:   time.Now(),
	}
	require.NoError(t, kv.StoreSubscription(sub))
	require.NoError(t, kv.AddToUserSubscriptionIndex("user-creator", sub.SpaceID))

	p := subscriptionTestPlugin(t, api, kv)

	require.NoError(t, p.RemoveSubscription("user-other-in-channel", "chan-owner", "abc-mnop-xyz"))

	_, getErr := kv.GetSubscription("spaces/abc123")
	assert.ErrorIs(t, getErr, kvstore.ErrSubscriptionNotFound)

	creatorIDs, listErr := kv.ListUserSubscriptionSpaceIDs("user-creator")
	require.NoError(t, listErr)
	assert.NotContains(t, creatorIDs, "spaces/abc123")
}

func TestRemoveSubscription_AcceptsMeetingURL(t *testing.T) {
	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	sub := &kvstore.Subscription{
		SpaceID:     "spaces/abc123",
		MeetingCode: "abc-mnop-xyz",
		ChannelID:   "chan1",
		CreatedBy:   "user1",
		CreatedAt:   time.Now(),
	}
	require.NoError(t, kv.StoreSubscription(sub))
	require.NoError(t, kv.AddToUserSubscriptionIndex("user1", sub.SpaceID))

	p := subscriptionTestPlugin(t, api, kv)

	require.NoError(t, p.RemoveSubscription("user1", "chan1", "https://meet.google.com/abc-mnop-xyz"))

	_, getErr := kv.GetSubscription("spaces/abc123")
	assert.ErrorIs(t, getErr, kvstore.ErrSubscriptionNotFound)
}

func TestRemoveSubscription_NoGoogleCall(t *testing.T) {
	origURL := googleMeetURL
	googleMeetURL = "http://127.0.0.1:1" // unroutable; any call here would fail fast
	defer func() { googleMeetURL = origURL }()

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	sub := &kvstore.Subscription{
		SpaceID:     "spaces/abc123",
		MeetingCode: "abc-mnop-xyz",
		ChannelID:   "chan1",
		CreatedBy:   "user1",
		CreatedAt:   time.Now(),
	}
	require.NoError(t, kv.StoreSubscription(sub))
	require.NoError(t, kv.AddToUserSubscriptionIndex("user1", sub.SpaceID))

	p := subscriptionTestPlugin(t, api, kv)

	require.NoError(t, p.RemoveSubscription("user1", "chan1", "abc-mnop-xyz"))
}

const permissionDeniedBody = `{"error":{"code":403,"message":"Permission denied on resource MeetingSpace (or it might not exist)","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"PERMISSION_DENIED_ON_RESOURCE","domain":"meet.googleapis.com"}]}}`

func overrideMeetAPI(t *testing.T, handler http.HandlerFunc) *int {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		handler(w, r)
	}))
	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	t.Cleanup(func() {
		googleMeetURL = origURL
		httpClient = origClient
		server.Close()
	})
	return &calls
}

func artifactPostsPlugin(t *testing.T, api *mockPluginAPI, kv *mockKVStore) *Plugin {
	t.Helper()
	p := subscriptionTestPlugin(t, api, kv)
	config := p.getConfiguration().Clone()
	config.EnableConferenceArtifactPosts = true
	p.setConfiguration(config)
	return p
}

func TestAddSubscription_KnownMissingReadonlyScopeNeedsReconnect(t *testing.T) {
	calls := overrideMeetAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "access", Expiry: time.Now().Add(time.Hour), Scope: meetScopeCreated}
	p := artifactPostsPlugin(t, api, kv)

	_, err := p.AddSubscription("user1", "chan1", "abc-mnop-xyz", "")
	require.ErrorIs(t, err, command.ErrNeedsReconnect)
	assert.Zero(t, *calls, "should not call Google when the scope is known to be missing")
	assert.NotContains(t, kv.tokens, "user1", "token should be deleted so the user reconnects cleanly")
}

func TestAddSubscription_ReadonlyNotRequiredWhenArtifactPostsDisabled(t *testing.T) {
	overrideMeetAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(meetSpace{Name: "spaces/abc123", MeetingCode: "abc-mnop-xyz"}))
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "access", Expiry: time.Now().Add(time.Hour), Scope: meetScopeCreated}
	p := subscriptionTestPlugin(t, api, kv)

	sub, err := p.AddSubscription("user1", "chan1", "abc-mnop-xyz", "")
	require.NoError(t, err)
	assert.Equal(t, "spaces/abc123", sub.SpaceID)
}

func TestAddSubscription_UnknownScopePermissionDeniedNeedsReconnect(t *testing.T) {
	overrideMeetAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte(permissionDeniedBody))
		require.NoError(t, err)
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "access", Expiry: time.Now().Add(time.Hour)}
	p := artifactPostsPlugin(t, api, kv)

	_, err := p.AddSubscription("user1", "chan1", "ruz-ryrk-ngg", "")
	require.ErrorIs(t, err, command.ErrNeedsReconnect)
}

func TestAddSubscription_KnownScopePermissionDeniedKeepsError(t *testing.T) {
	overrideMeetAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte(permissionDeniedBody))
		require.NoError(t, err)
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	kv.tokens["user1"] = &kvstore.OAuth2Token{
		AccessToken: "access",
		Expiry:      time.Now().Add(time.Hour),
		Scope:       meetScopeCreated + " " + meetScopeReadonly,
	}
	p := artifactPostsPlugin(t, api, kv)

	_, err := p.AddSubscription("user1", "chan1", "ruz-ryrk-ngg", "")
	require.ErrorIs(t, err, ErrPermissionDenied)
	assert.NotErrorIs(t, err, command.ErrNeedsReconnect)
	assert.Contains(t, err.Error(), `could not find meeting space "ruz-ryrk-ngg"`)
	assert.Contains(t, kv.tokens, "user1")
}
