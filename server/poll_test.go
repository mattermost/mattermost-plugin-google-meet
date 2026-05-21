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

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

// pollTestPlugin creates a Plugin wired up for polling tests.
func pollTestPlugin(t *testing.T, api *mockPluginAPI, kv *mockKVStore) *Plugin {
	t.Helper()
	p := &Plugin{}
	p.API = api
	p.botID = "bot1"
	p.setKVStore(kv)
	p.setConfiguration(&configuration{
		GoogleClientID:                "test-client-id",
		GoogleClientSecret:            "test-client-secret",
		EncryptionKey:                 "test-encryption-key",
		EnableConferenceArtifactPosts: true,
		PollIntervalSeconds:           60,
	})
	return p
}

func TestPollSubscription_NewConferenceCreatesPost(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"conferenceRecords": []conferenceRecord{
					{Name: "conferenceRecords/rec1", StartTime: &now},
				},
			}))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	kv.tokens["user1"] = token

	p := pollTestPlugin(t, api, kv)

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		CreatedAt:               now.Add(-time.Hour),
		LastSeenConferenceStart: now.Add(-time.Hour),
	}

	p.pollSubscription(kv, sub)

	// A top-level post should have been created.
	require.NotNil(t, api.post)
	assert.Equal(t, postTypeConference, api.post.Type)
	assert.Equal(t, "chan1", api.post.ChannelId)
	assert.Equal(t, "bot1", api.post.UserId)

	// Conference post state should be stored.
	state, err := kv.GetConferencePostState("conferenceRecords/rec1")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "chan1", state.ChannelID)
}

func TestPollSubscription_DuplicateConferenceNotPostedAgain(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/conferenceRecords" {
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"conferenceRecords": []conferenceRecord{
					{Name: "conferenceRecords/rec1", StartTime: &now},
				},
			}))
			return
		}
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	kv.tokens["user1"] = token

	// Pre-seed state as if we already processed this conference.
	existingState := &kvstore.ConferencePostState{
		RootPostID: "existing-post-id",
		ChannelID:  "chan1",
	}
	require.NoError(t, kv.StoreConferencePostState("conferenceRecords/rec1", existingState))

	p := pollTestPlugin(t, api, kv)
	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: now.Add(-time.Hour),
		ActiveConferenceIDs:     []string{"conferenceRecords/rec1"},
	}

	p.pollSubscription(kv, sub)

	// No new post should have been created (existing state was preserved).
	// The mock only tracks the last post; if no new conference post was created it stays nil.
	if api.post != nil {
		assert.NotEqual(t, postTypeConference, api.post.Type, "should not create duplicate conference post")
	}
}

// TestPollSubscription_StaleRecordNotRepostedAfterStateTTL is a regression test for the bug where
// a recurring meeting whose ConferencePostState had TTL'd out would be re-posted as if it were new.
// The Google API still returns the most-recent (already-processed) conference record on every poll,
// so the watermark filter must exclude it strictly — even when KV state has expired.
func TestPollSubscription_StaleRecordNotRepostedAfterStateTTL(t *testing.T) {
	now := time.Now().UTC()
	staleStart := now.Add(-10 * 24 * time.Hour) // older than the 7-day state TTL
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/conferenceRecords" {
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"conferenceRecords": []conferenceRecord{
					{Name: "conferenceRecords/recOld", StartTime: &staleStart},
				},
			}))
			return
		}
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	kv.tokens["user1"] = token

	// Watermark exactly equals the stale record's StartTime, mirroring how it was set
	// when that record was last processed. No ConferencePostState — simulates TTL expiry.
	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: staleStart,
	}

	p := pollTestPlugin(t, api, kv)
	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "stale record should not be re-posted after ConferencePostState TTL expiry")
	assert.Empty(t, sub.ActiveConferenceIDs, "stale record should not be tracked as active")
}

func TestPollConferenceArtifacts_RecordingPostedOnce(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords/rec1/recordings":
			callCount++
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"recordings": []meetRecording{
					{
						Name:             "conferenceRecords/rec1/recordings/r1",
						State:            meetStateFileGenerated,
						DriveDestination: &driveDestination{ExportURI: "https://drive.google.com/file/abc"},
					},
				},
			}))
		case "/v2/conferenceRecords/rec1/transcripts":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"transcripts": []meetTranscript{}}))
		case "/v2/conferenceRecords/rec1/smartNotes":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"smartNotes": []meetSmartNote{}}))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	api.captureAllPosts = true

	kv := newMockKVStore()
	kv.tokens["user1"] = token

	state := &kvstore.ConferencePostState{
		RootPostID: "root-post-id",
		ChannelID:  "chan1",
	}
	require.NoError(t, kv.StoreConferencePostState("conferenceRecords/rec1", state))

	p := pollTestPlugin(t, api, kv)

	done := p.pollConferenceArtifacts(kv, token, "conferenceRecords/rec1")
	assert.False(t, done)

	// Recording post should have been created.
	assert.Equal(t, 1, len(api.allPosts))
	recPost := api.allPosts[0]
	assert.Equal(t, postTypeRecording, recPost.Type)
	assert.Equal(t, "root-post-id", recPost.RootId)
	assert.Equal(t, "chan1", recPost.ChannelId)

	// Verify state was updated with the posted recording ID.
	updatedState, err := kv.GetConferencePostState("conferenceRecords/rec1")
	require.NoError(t, err)
	assert.Contains(t, updatedState.PostedRecordingIDs, "conferenceRecords/rec1/recordings/r1")

	// Poll again — recording should NOT be posted again.
	api.allPosts = nil
	p.pollConferenceArtifacts(kv, token, "conferenceRecords/rec1")
	assert.Empty(t, api.allPosts, "recording should not be posted twice")
}

func TestPollSubscription_NoTokenSkipped(t *testing.T) {
	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	// No token stored for "user1"

	p := pollTestPlugin(t, api, kv)
	sub := &kvstore.Subscription{
		SpaceID:     "spaces/abc123",
		MeetingCode: "abc-mnop-xyz",
		ChannelID:   "chan1",
		CreatedBy:   "user1",
	}

	p.pollSubscription(kv, sub)
	assert.Nil(t, api.post, "no post should be created when token is missing")
}

// TestPollAdHocMeetings verifies that a transcript posted as a reply to the original
// /meet start post when the conference record appears for an ad-hoc meeting.
func TestPollAdHocMeetings_TranscriptPostedAsReply(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"conferenceRecords": []conferenceRecord{
					{Name: "conferenceRecords/rec1", StartTime: &now},
				},
			}))
		case "/v2/conferenceRecords/rec1/transcripts":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"transcripts": []meetTranscript{
					{Name: "conferenceRecords/rec1/transcripts/t1", State: meetStateFileGenerated},
				},
			}))
		case "/v2/conferenceRecords/rec1/transcripts/t1/entries":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"transcriptEntries": []transcriptEntry{
					{Name: "conferenceRecords/rec1/transcripts/t1/entries/e1", Text: "Hello world", StartTime: now},
				},
			}))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	kv.tokens["user1"] = token

	// Simulate an ad-hoc entry as created by StartMeeting.
	adHocEntry := &kvstore.AdHocMeetingPost{
		RootPostID: "original-meet-post-id",
		ChannelID:  "chan1",
		UserID:     "user1",
	}
	require.NoError(t, kv.StoreAdHocMeetingPost("spaces/adhoc1", adHocEntry))
	require.NoError(t, kv.AddToAdHocIndex("spaces/adhoc1"))

	p := pollTestPlugin(t, api, kv)
	p.pollAdHocMeetings(kv)

	// The transcript reply should be threaded under the original /meet start post.
	require.NotEmpty(t, api.allPosts, "expected at least one artifact post")
	trPost := api.allPosts[0]
	assert.Equal(t, postTypeTranscript, trPost.Type)
	assert.Equal(t, "original-meet-post-id", trPost.RootId)
	assert.Equal(t, "chan1", trPost.ChannelId)

	// A second poll should not duplicate the post.
	api.allPosts = nil
	p.pollAdHocMeetings(kv)
	assert.Empty(t, api.allPosts, "transcript should not be posted twice")
}

// TestPollAdHocMeetings_ExpiredEntryPruned verifies that a TTL-expired ad-hoc entry
// is removed from the index without error.
func TestPollAdHocMeetings_ExpiredEntryPruned(t *testing.T) {
	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	// Add a space ID to the index but do NOT store the entry (simulates TTL expiry).
	require.NoError(t, kv.AddToAdHocIndex("spaces/expired"))

	p := pollTestPlugin(t, api, kv)
	p.pollAdHocMeetings(kv)

	// Index should be pruned.
	ids, err := kv.ListAdHocSpaceIDs()
	require.NoError(t, err)
	assert.NotContains(t, ids, "spaces/expired")
}
