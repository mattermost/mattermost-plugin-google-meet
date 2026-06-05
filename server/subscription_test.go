// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
