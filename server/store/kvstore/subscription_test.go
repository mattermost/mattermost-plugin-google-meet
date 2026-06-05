// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package kvstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionKey_EscapesSlashes(t *testing.T) {
	key := subscriptionKey("spaces/abc123")
	assert.Equal(t, "subscription_spaces%2Fabc123", key)
	assert.NotContains(t, key, "/")
}

func TestConferencePostKey_EscapesSlashes(t *testing.T) {
	key := conferencePostKey("conferenceRecords/abc123")
	assert.Equal(t, "conference_post_conferenceRecords%2Fabc123", key)
	assert.NotContains(t, key, "/")
}

func TestSubscription_JSONRoundTrip(t *testing.T) {
	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	sub := Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "channel1",
		CreatedBy:               "user1",
		CreatedAt:               now,
		LastSeenConferenceStart: now.Add(time.Hour),
		ActiveConferenceIDs:     []string{"conferenceRecords/rec1", "conferenceRecords/rec2"},
	}

	data, err := json.Marshal(sub)
	require.NoError(t, err)

	var got Subscription
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, sub.SpaceID, got.SpaceID)
	assert.Equal(t, sub.MeetingCode, got.MeetingCode)
	assert.Equal(t, sub.ChannelID, got.ChannelID)
	assert.Equal(t, sub.CreatedBy, got.CreatedBy)
	assert.Equal(t, sub.ActiveConferenceIDs, got.ActiveConferenceIDs)
	assert.Equal(t, sub.LastSeenConferenceStart.UTC(), got.LastSeenConferenceStart.UTC())
}

func TestConferencePostState_JSONRoundTrip(t *testing.T) {
	state := ConferencePostState{
		RootPostID:          "root-123",
		ChannelID:           "chan-456",
		PostedRecordingIDs:  []string{"rec/r1", "rec/r2"},
		PostedTranscriptIDs: []string{"tr/t1"},
		PostedSmartNoteIDs:  []string{},
	}

	data, err := json.Marshal(state)
	require.NoError(t, err)

	var got ConferencePostState
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, state.RootPostID, got.RootPostID)
	assert.Equal(t, state.ChannelID, got.ChannelID)
	assert.Equal(t, state.PostedRecordingIDs, got.PostedRecordingIDs)
	assert.Equal(t, state.PostedTranscriptIDs, got.PostedTranscriptIDs)
}

func TestSubscription_EmptyActiveConferenceIDs(t *testing.T) {
	sub := Subscription{
		SpaceID:   "spaces/abc",
		ChannelID: "chan1",
	}

	data, err := json.Marshal(sub)
	require.NoError(t, err)

	var got Subscription
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Nil(t, got.ActiveConferenceIDs)
}
