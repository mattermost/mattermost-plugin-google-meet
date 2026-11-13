// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
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
		MeetingPostID: "existing-post-id",
		ChannelID:     "chan1",
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

// TestPollSubscription_CooldownSuppressesRejoin verifies that a conference starting shortly
// after the previous one ended on the same space is not announced, addressing the case where
// people reopen a stale meeting link right after the real meeting has ended.
func TestPollSubscription_CooldownSuppressesRejoin(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	firstStart := now.Add(-2 * time.Hour)
	firstEnd := firstStart.Add(30 * time.Minute)
	rejoinStart := firstEnd.Add(10 * time.Minute) // well inside a 1-hour cooldown

	var records []conferenceRecord
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/conferenceRecords" {
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": records}))
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

	p := pollTestPlugin(t, api, kv)
	p.configuration.ConferenceStartCooldownHours = 1

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: firstStart.Add(-time.Hour),
	}

	// First conference: nothing preceded it, so it always posts.
	records = []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &firstStart, EndTime: &firstEnd}}
	p.pollSubscription(kv, sub)
	require.Len(t, api.allPosts, 1)
	assert.Equal(t, postTypeConference, api.allPosts[0].Type)

	// Rejoin shortly after the first conference ended: should be suppressed. The anchor comes
	// from the persisted LastConferenceEndTime, so rec1 need not be re-fetched here.
	api.allPosts = nil
	records = []conferenceRecord{
		{Name: "conferenceRecords/rec2", StartTime: &rejoinStart},
	}
	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "rejoin within cooldown should not be announced")

	state, err := kv.GetConferencePostState("conferenceRecords/rec2")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Suppressed)
	assert.NotContains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec2")
}

// TestPollSubscription_CooldownAllowsAfterWindow verifies that a conference starting after the
// cooldown window has elapsed since the previous one ended is announced normally.
func TestPollSubscription_CooldownAllowsAfterWindow(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	firstStart := now.Add(-4 * time.Hour)
	firstEnd := firstStart.Add(30 * time.Minute)
	secondStart := firstEnd.Add(2 * time.Hour) // well outside a 1-hour cooldown

	var records []conferenceRecord
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/conferenceRecords" {
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": records}))
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

	p := pollTestPlugin(t, api, kv)
	p.configuration.ConferenceStartCooldownHours = 1

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: firstStart.Add(-time.Hour),
	}

	records = []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &firstStart, EndTime: &firstEnd}}
	p.pollSubscription(kv, sub)
	require.Len(t, api.allPosts, 1)

	api.allPosts = nil
	records = []conferenceRecord{
		{Name: "conferenceRecords/rec2", StartTime: &secondStart},
	}
	p.pollSubscription(kv, sub)

	require.Len(t, api.allPosts, 1, "conference starting after the cooldown window should be announced")
	assert.Equal(t, postTypeConference, api.allPosts[0].Type)

	state, err := kv.GetConferencePostState("conferenceRecords/rec2")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.False(t, state.Suppressed)
	assert.Contains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec2")
}

// TestPollSubscription_CooldownDisabledPostsAlways verifies that ConferenceStartCooldownHours=0
// restores the original always-announce behavior.
func TestPollSubscription_CooldownDisabledPostsAlways(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	firstStart := now.Add(-2 * time.Hour)
	firstEnd := firstStart.Add(30 * time.Minute)
	rejoinStart := firstEnd.Add(time.Minute)

	var records []conferenceRecord
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/conferenceRecords" {
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": records}))
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

	// pollTestPlugin already leaves ConferenceStartCooldownHours unset (0), i.e. disabled.
	p := pollTestPlugin(t, api, kv)

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: firstStart.Add(-time.Hour),
	}

	records = []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &firstStart, EndTime: &firstEnd}}
	p.pollSubscription(kv, sub)
	require.Len(t, api.allPosts, 1)

	api.allPosts = nil
	records = []conferenceRecord{
		{Name: "conferenceRecords/rec2", StartTime: &rejoinStart},
	}
	p.pollSubscription(kv, sub)

	require.Len(t, api.allPosts, 1, "cooldown disabled should always announce new conferences")
	assert.Equal(t, postTypeConference, api.allPosts[0].Type)
}

// TestPollSubscription_CooldownAnchoredOnEndNotStart verifies the cooldown is measured from the
// previous conference's end, not its start. A single meeting can legitimately run longer than the
// cooldown, so the start-to-start gap between two conferences must not be used as the anchor.
func TestPollSubscription_CooldownAnchoredOnEndNotStart(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	cooldown := time.Hour
	firstStart := now.Add(-6 * time.Hour)
	firstEnd := firstStart.Add(3 * time.Hour) // long meeting: start-to-start gap will exceed cooldown
	rejoinStart := firstEnd.Add(15 * time.Minute)

	// Sanity-check the scenario: if the anchor were mistakenly the previous start, this rejoin
	// would NOT be suppressed (gap from start > cooldown), even though it should be (gap from end < cooldown).
	require.Greater(t, rejoinStart.Sub(firstStart), cooldown)
	require.Less(t, rejoinStart.Sub(firstEnd), cooldown)

	var records []conferenceRecord
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/conferenceRecords" {
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": records}))
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

	p := pollTestPlugin(t, api, kv)
	p.configuration.ConferenceStartCooldownHours = 1

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: firstStart.Add(-time.Hour),
	}

	records = []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &firstStart, EndTime: &firstEnd}}
	p.pollSubscription(kv, sub)
	require.Len(t, api.allPosts, 1, "the long meeting itself always posts on first sight")

	api.allPosts = nil
	records = []conferenceRecord{
		{Name: "conferenceRecords/rec2", StartTime: &rejoinStart},
	}
	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "rejoin soon after the long meeting ended should be suppressed despite the large start-to-start gap")
}

// TestPollSubscription_RetryAfterPostStateFailureNotSuppressed is a regression test for the bug
// where a conference whose ConferencePostState failed to persist would be wrongly suppressed on
// retry. The failed cycle still advances the subscription's LastConferenceEndTime to the record's
// own end (which is after its start), so on the next poll that persisted end must not be reused as
// the cooldown anchor for the same record — otherwise the gap goes negative and the genuine
// conference is silently dropped instead of retried.
func TestPollSubscription_RetryAfterPostStateFailureNotSuppressed(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{
		AccessToken: "test-token",
		Expiry:      now.Add(time.Hour),
	}

	start := now.Add(-2 * time.Hour)
	end := start.Add(30 * time.Minute) // record's own end is after its start

	var records []conferenceRecord
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/conferenceRecords" {
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": records}))
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

	p := pollTestPlugin(t, api, kv)
	p.configuration.ConferenceStartCooldownHours = 1

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: start.Add(-time.Hour),
	}

	// First poll: the post is created but persisting its state fails, so no state is stored and
	// the watermark is not advanced. LastConferenceEndTime is still advanced to the record's end.
	records = []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &start, EndTime: &end}}
	kv.storeConfStateErr = assert.AnError
	p.pollSubscription(kv, sub)

	state, err := kv.GetConferencePostState("conferenceRecords/rec1")
	require.NoError(t, err)
	require.Nil(t, state, "state should not have persisted after the simulated failure")
	require.False(t, sub.LastConferenceEndTime.Before(end), "failed cycle still advances LastConferenceEndTime to the record end")

	// Second poll (retry): persistence works again. The record's own end must not be reused as the
	// cooldown anchor, so it should be posted and tracked rather than suppressed.
	api.allPosts = nil
	kv.storeConfStateErr = nil
	p.pollSubscription(kv, sub)

	state, err = kv.GetConferencePostState("conferenceRecords/rec1")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.False(t, state.Suppressed, "retry of a genuine conference must not be suppressed")
	assert.Contains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1", "retried conference must remain tracked for artifacts")
	assert.NotEmpty(t, api.allPosts, "retry should announce the conference")
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
		MeetingPostID: "root-post-id",
		ThreadRootID:  "root-post-id",
		ChannelID:     "chan1",
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
		MeetingPostID: "original-meet-post-id",
		ThreadRootID:  "original-meet-post-id",
		ChannelID:     "chan1",
		UserID:        "user1",
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

func TestPollAdHocMeetings_TranscriptPostedInThread(t *testing.T) {
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

	adHocEntry := &kvstore.AdHocMeetingPost{
		MeetingPostID: "original-meet-post-id",
		ThreadRootID:  "thread-root-1",
		ChannelID:     "chan1",
		UserID:        "user1",
	}
	require.NoError(t, kv.StoreAdHocMeetingPost("spaces/adhoc1", adHocEntry))
	require.NoError(t, kv.AddToAdHocIndex("spaces/adhoc1"))

	p := pollTestPlugin(t, api, kv)
	p.pollAdHocMeetings(kv)

	require.NotEmpty(t, api.allPosts, "expected at least one artifact post")
	trPost := api.allPosts[0]
	assert.Equal(t, postTypeTranscript, trPost.Type)
	assert.Equal(t, "thread-root-1", trPost.RootId)
	assert.Equal(t, "chan1", trPost.ChannelId)
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

// withCalendarPollServer wires both googleMeetURL and googleCalendarURL at a single httptest
// server so a test's handler can route on r.URL.Path across both APIs.
func withCalendarPollServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	origMeetURL, origCalURL, origClient := googleMeetURL, googleCalendarURL, httpClient
	googleMeetURL = server.URL + "/v2"
	googleCalendarURL = server.URL
	httpClient = server.Client()
	t.Cleanup(func() {
		googleMeetURL = origMeetURL
		googleCalendarURL = origCalURL
		httpClient = origClient
	})
}

func calendarEventForCode(id, meetingCode, summary string, start, end time.Time) calendarEvent {
	return calendarEvent{
		ID:             id,
		Summary:        summary,
		Start:          calendarEventDateTime{DateTime: &start},
		End:            calendarEventDateTime{DateTime: &end},
		ConferenceData: &calendarConferenceData{ConferenceID: meetingCode},
	}
}

// TestPollSubscription_CalendarEarlyJoinDefersUntilScheduledStart verifies the core fix: joining
// a subscribed space before its calendar event's scheduled start defers the "started" post
// instead of announcing it immediately, and the post only appears once the scheduled start
// actually arrives.
func TestPollSubscription_CalendarEarlyJoinDefersUntilScheduledStart(t *testing.T) {
	conferenceStart := time.Now().UTC()
	scheduledStart := conferenceStart.Add(2 * time.Second)
	scheduledEnd := scheduledStart.Add(30 * time.Minute)
	token := &kvstore.OAuth2Token{AccessToken: "test-token", Expiry: conferenceStart.Add(time.Hour)}

	var listedRecords []conferenceRecord
	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": listedRecords}))
		case "/v2/conferenceRecords/rec1":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(conferenceRecord{Name: "conferenceRecords/rec1", StartTime: &conferenceStart}))
		case "/calendars/primary/events":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"items": []calendarEvent{calendarEventForCode("evt1", "abc-mnop-xyz", "Weekly sync", scheduledStart, scheduledEnd)},
			}))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	kv.tokens["user1"] = token

	p := pollTestPlugin(t, api, kv)
	p.configuration.EnableCalendarScheduleSync = true

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: conferenceStart.Add(-time.Hour),
	}

	listedRecords = []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &conferenceStart}}
	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "an early join before the scheduled start should be deferred, not announced")
	require.Len(t, sub.ScheduledAnnouncements, 1)
	assert.Equal(t, "conferenceRecords/rec1", sub.ScheduledAnnouncements[0].ConferenceName)
	assert.NotContains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1", "a deferred conference should not be tracked as active yet")

	time.Sleep(time.Until(scheduledStart) + 100*time.Millisecond)
	listedRecords = nil // Google would not hand back this record again; the watermark already passed it.
	p.pollSubscription(kv, sub)

	require.Len(t, api.allPosts, 1, "the deferred announcement should post once the scheduled start arrives")
	assert.Equal(t, postTypeConference, api.allPosts[0].Type)
	assert.Equal(t, "Weekly sync", api.allPosts[0].Props["meeting_topic"])
	assert.Empty(t, sub.ScheduledAnnouncements)
	assert.Contains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1")
}

// TestPollSubscription_CalendarEarlyJoinDroppedIfLeftBeforeScheduledStart verifies that a
// deferred announcement whose conference already ended before the scheduled start is dropped
// rather than posted late — someone opened the link early and left, and the meeting never
// actually happened yet.
func TestPollSubscription_CalendarEarlyJoinDroppedIfLeftBeforeScheduledStart(t *testing.T) {
	conferenceStart := time.Now().UTC()
	conferenceEnd := conferenceStart.Add(100 * time.Millisecond)
	scheduledStart := conferenceStart.Add(2 * time.Second)
	scheduledEnd := scheduledStart.Add(30 * time.Minute)
	token := &kvstore.OAuth2Token{AccessToken: "test-token", Expiry: conferenceStart.Add(time.Hour)}

	var listedRecords []conferenceRecord
	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": listedRecords}))
		case "/v2/conferenceRecords/rec1":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(conferenceRecord{
				Name: "conferenceRecords/rec1", StartTime: &conferenceStart, EndTime: &conferenceEnd,
			}))
		case "/calendars/primary/events":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"items": []calendarEvent{calendarEventForCode("evt1", "abc-mnop-xyz", "Weekly sync", scheduledStart, scheduledEnd)},
			}))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	kv.tokens["user1"] = token

	p := pollTestPlugin(t, api, kv)
	p.configuration.EnableCalendarScheduleSync = true

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: conferenceStart.Add(-time.Hour),
	}

	listedRecords = []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &conferenceStart}}
	p.pollSubscription(kv, sub)
	require.Len(t, sub.ScheduledAnnouncements, 1)

	time.Sleep(time.Until(scheduledStart) + 100*time.Millisecond)
	listedRecords = nil
	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "a conference that ended before the scheduled start should be dropped, not posted late")
	assert.Empty(t, sub.ScheduledAnnouncements)
	state, err := kv.GetConferencePostState("conferenceRecords/rec1")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Suppressed)
}

// TestPollSubscription_CalendarInstanceReuseBindsArtifactsToSamePost verifies that a second
// conference record matching the same calendar event instance (a drop-and-rejoin) is bound to
// the already-announced post instead of creating a duplicate — fixing the current cooldown
// mechanism's side effect where a suppressed rejoin's artifacts were silently discarded.
func TestPollSubscription_CalendarInstanceReuseBindsArtifactsToSamePost(t *testing.T) {
	now := time.Now().UTC()
	scheduledStart := now.Add(-5 * time.Minute)
	scheduledEnd := scheduledStart.Add(30 * time.Minute)
	firstStart := scheduledStart
	rejoinStart := now.Add(-time.Minute)
	token := &kvstore.OAuth2Token{AccessToken: "test-token", Expiry: now.Add(time.Hour)}

	var listedRecords []conferenceRecord
	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": listedRecords}))
		case "/calendars/primary/events":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"items": []calendarEvent{calendarEventForCode("evt1", "abc-mnop-xyz", "Weekly sync", scheduledStart, scheduledEnd)},
			}))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	kv.tokens["user1"] = token

	p := pollTestPlugin(t, api, kv)
	p.configuration.EnableCalendarScheduleSync = true

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: firstStart.Add(-time.Hour),
	}

	listedRecords = []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &firstStart}}
	p.pollSubscription(kv, sub)
	require.Len(t, api.allPosts, 1, "the first record in the instance should post")
	firstPostID := api.allPosts[0].Id
	require.NotEmpty(t, firstPostID)

	api.allPosts = nil
	listedRecords = []conferenceRecord{{Name: "conferenceRecords/rec2", StartTime: &rejoinStart}}
	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "a rejoin within the same calendar instance must not create a second top-level post")

	state, err := kv.GetConferencePostState("conferenceRecords/rec2")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, firstPostID, state.MeetingPostID, "the rejoin should be bound to the original post so its artifacts thread correctly")
	assert.Contains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec2")

	require.Len(t, sub.EventPostBindings, 1)
	assert.ElementsMatch(t, []string{"conferenceRecords/rec1", "conferenceRecords/rec2"}, sub.EventPostBindings[0].ConferenceNames)
}

func TestClassifyAgainstCalendarInstance_DoesNotDuplicateDeferredAnnouncement(t *testing.T) {
	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	p := pollTestPlugin(t, api, kv)
	sub := &kvstore.Subscription{ChannelID: "chan1"}
	record := &conferenceRecord{Name: "conferenceRecords/rec1"}
	instance := &calendarInstance{
		InstanceID: "evt1",
		Start:      time.Now().Add(time.Hour),
		End:        time.Now().Add(2 * time.Hour),
	}

	deferred, err := p.classifyAgainstCalendarInstance(kv, sub, record, instance)
	require.NoError(t, err)
	require.True(t, deferred)
	deferred, err = p.classifyAgainstCalendarInstance(kv, sub, record, instance)
	require.NoError(t, err)
	require.True(t, deferred)
	assert.Len(t, sub.ScheduledAnnouncements, 1)
}

func TestPostAndBindToCalendarInstance_ReusesExistingBinding(t *testing.T) {
	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	p := pollTestPlugin(t, api, kv)
	sub := &kvstore.Subscription{
		ChannelID: "chan1",
		EventPostBindings: []kvstore.EventPostBinding{{
			EventInstanceID: "evt1",
			MeetingPostID:   "post1",
			ConferenceNames: []string{"conferenceRecords/rec1"},
		}},
	}
	record := &conferenceRecord{Name: "conferenceRecords/rec2"}
	instance := &calendarInstance{InstanceID: "evt1"}

	require.NoError(t, p.postAndBindToCalendarInstance(kv, sub, record, instance))
	assert.Empty(t, api.allPosts)
	assert.Equal(t, []string{"conferenceRecords/rec1", "conferenceRecords/rec2"}, sub.EventPostBindings[0].ConferenceNames)
	state, err := kv.GetConferencePostState(record.Name)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "post1", state.MeetingPostID)
}

func TestProcessDueScheduledAnnouncements_DropsExpiredRefreshFailures(t *testing.T) {
	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	p := pollTestPlugin(t, api, kv)
	now := time.Now()
	sub := &kvstore.Subscription{
		ScheduledAnnouncements: []kvstore.ScheduledAnnouncement{
			{
				ConferenceName: "conferenceRecords/expired",
				DueAt:          now.Add(-time.Hour),
				EventEnd:       now.Add(-eventBindingRetention - time.Hour),
			},
			{
				ConferenceName: "conferenceRecords/retry",
				DueAt:          now.Add(-time.Hour),
				EventEnd:       now,
			},
		},
	}

	changed := p.processDueScheduledAnnouncements(kv, sub, newTestToken())
	assert.True(t, changed)
	require.Len(t, sub.ScheduledAnnouncements, 1)
	assert.Equal(t, "conferenceRecords/retry", sub.ScheduledAnnouncements[0].ConferenceName)
}

func TestAggregateBindingEnd_TracksOnlyActiveConferences(t *testing.T) {
	ended := time.Now().Add(-time.Hour)
	names := []string{"conferenceRecords/stale", "conferenceRecords/ended", "conferenceRecords/unknown"}
	resolvedEnd := map[string]*time.Time{
		"conferenceRecords/ended": &ended,
	}

	latest, allEnded := aggregateBindingEnd(names, []string{"conferenceRecords/ended", "conferenceRecords/unknown"}, resolvedEnd)
	assert.False(t, allEnded, "an active conference with an unknown end must keep the binding active")
	assert.True(t, latest.IsZero())

	latest, allEnded = aggregateBindingEnd(names, []string{"conferenceRecords/ended"}, resolvedEnd)
	assert.True(t, allEnded, "a conference no longer tracked as active must not block the binding")
	assert.Equal(t, ended, latest)
}

// TestPollSubscription_CalendarNoMatchFallsBackToCooldown verifies that a conference with no
// matching calendar event (someone opening the link off-hours, or a meeting the creator wasn't
// invited to) is still handled by the pre-existing cooldown heuristic rather than being dropped.
func TestPollSubscription_CalendarNoMatchFallsBackToCooldown(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{AccessToken: "test-token", Expiry: now.Add(time.Hour)}

	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"conferenceRecords": []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &now}},
			}))
		case "/calendars/primary/events":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"items": []calendarEvent{}}))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	kv.tokens["user1"] = token

	p := pollTestPlugin(t, api, kv)
	p.configuration.EnableCalendarScheduleSync = true

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: now.Add(-time.Hour),
	}

	p.pollSubscription(kv, sub)

	require.Len(t, api.allPosts, 1, "an unscheduled conference with no matching calendar event should still post via the cooldown fallback")
	assert.Equal(t, postTypeConference, api.allPosts[0].Type)
	assert.Empty(t, sub.ScheduledAnnouncements)
	assert.Contains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1")
}

// recurringMeetingTestServer serves the listed conference records plus a calendar endpoint driven
// by calendarStatus/calendarItems, so tests can change the calendar's answer between poll cycles.
// Like Google, the calendar only returns events overlapping the queried [timeMin, timeMax).
type recurringMeetingTestServer struct {
	records        []conferenceRecord
	calendarStatus int
	calendarItems  []calendarEvent
	calendarCalls  int
}

func (s *recurringMeetingTestServer) install(t *testing.T) {
	t.Helper()
	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": s.records}))
		case "/calendars/primary/events":
			s.calendarCalls++
			if s.calendarStatus != http.StatusOK {
				w.WriteHeader(s.calendarStatus)
				return
			}
			timeMin, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("timeMin"))
			require.NoError(t, err)
			timeMax, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("timeMax"))
			require.NoError(t, err)
			var items []calendarEvent
			for _, event := range s.calendarItems {
				if event.End.DateTime.After(timeMin) && event.Start.DateTime.Before(timeMax) {
					items = append(items, event)
				}
			}
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"items": items}))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	})
}

// newRecurringMeetingTestPlugin returns a calendar-synced plugin and a subscription that has
// recently matched an occurrence of a recurring calendar event.
func newRecurringMeetingTestPlugin(t *testing.T) (*Plugin, *mockPluginAPI, *mockKVStore, *kvstore.Subscription) {
	t.Helper()
	now := time.Now().UTC()
	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	kv.tokens["user1"] = &kvstore.OAuth2Token{AccessToken: "test-token", Expiry: now.Add(time.Hour)}

	p := pollTestPlugin(t, api, kv)
	p.configuration.EnableCalendarScheduleSync = true

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: now.Add(-time.Hour),
		LastRecurringMatchAt:    now.Add(-7 * 24 * time.Hour),
	}
	return p, api, kv, sub
}

// TestPollSubscription_RecurringMeetingUnmatchedConferenceSuppressed reproduces the misfire where
// someone opens the link of a recurring calendar meeting outside any scheduled occurrence: only a
// calendar occurrence may announce a conference there, so nothing is posted.
func TestPollSubscription_RecurringMeetingUnmatchedConferenceSuppressed(t *testing.T) {
	start := time.Now().UTC()
	srv := &recurringMeetingTestServer{
		records:        []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &start}},
		calendarStatus: http.StatusOK,
	}
	srv.install(t)
	p, api, kv, sub := newRecurringMeetingTestPlugin(t)

	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "a conference outside every occurrence of a recurring calendar meeting must not be announced")
	assert.Empty(t, sub.PendingConferences)
	assert.NotContains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1")
	state, err := kv.GetConferencePostState("conferenceRecords/rec1")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Suppressed)
}

// TestPollSubscription_RecurringMeetingCalendarErrorRetried verifies that a transient calendar
// failure on a recurring calendar meeting doesn't fall back to posting immediately: the lookup is
// retried, and once it succeeds the calendar decides.
func TestPollSubscription_RecurringMeetingCalendarErrorRetried(t *testing.T) {
	cases := []struct {
		name          string
		matches       bool
		wantScheduled bool
	}{
		{"retry matches an occurrence", true, true},
		{"retry finds no occurrence", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			scheduledStart := now.Add(10 * time.Minute)
			srv := &recurringMeetingTestServer{
				records:        []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &now}},
				calendarStatus: http.StatusInternalServerError,
			}
			srv.install(t)
			p, api, kv, sub := newRecurringMeetingTestPlugin(t)

			p.pollSubscription(kv, sub)

			assert.Empty(t, api.allPosts, "a calendar lookup failure should not post immediately")
			require.Len(t, sub.PendingConferences, 1)
			assert.Equal(t, "conferenceRecords/rec1", sub.PendingConferences[0].ConferenceName)

			p.pollSubscription(kv, sub)
			require.Len(t, sub.PendingConferences, 1, "the lookup keeps being retried while it fails")

			srv.records = nil
			srv.calendarStatus = http.StatusOK
			if tc.matches {
				event := calendarEventForCode("evt1", "abc-mnop-xyz", "Weekly sync", scheduledStart, scheduledStart.Add(30*time.Minute))
				event.RecurringEventID = "series1"
				srv.calendarItems = []calendarEvent{event}
			}
			p.pollSubscription(kv, sub)

			assert.Empty(t, api.allPosts)
			assert.Empty(t, sub.PendingConferences)
			if tc.wantScheduled {
				require.Len(t, sub.ScheduledAnnouncements, 1, "a matched occurrence should be deferred to its scheduled start")
				return
			}
			assert.Empty(t, sub.ScheduledAnnouncements)
			state, err := kv.GetConferencePostState("conferenceRecords/rec1")
			require.NoError(t, err)
			require.NotNil(t, state)
			assert.True(t, state.Suppressed)
		})
	}
}

// TestPollSubscription_NonRecurringSubscriptionKeepsImmediateFallback verifies that the
// calendar-only rule applies only to recurring calendar meetings: otherwise (never matched a
// recurring occurrence, or last match outside recurringMatchWindow) unmatched conferences and
// calendar errors still fall back to the cooldown heuristic and post immediately.
func TestPollSubscription_NonRecurringSubscriptionKeepsImmediateFallback(t *testing.T) {
	cases := []struct {
		name                 string
		calendarStatus       int
		lastRecurringMatchAt time.Time
	}{
		{"no match, never matched a recurring event", http.StatusOK, time.Time{}},
		{"no match, recurring match too old", http.StatusOK, time.Now().Add(-recurringMatchWindow - time.Hour)},
		{"calendar error, never matched a recurring event", http.StatusInternalServerError, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now().UTC()
			srv := &recurringMeetingTestServer{
				records:        []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &start}},
				calendarStatus: tc.calendarStatus,
			}
			srv.install(t)
			p, api, kv, sub := newRecurringMeetingTestPlugin(t)
			sub.LastRecurringMatchAt = tc.lastRecurringMatchAt

			p.pollSubscription(kv, sub)

			require.Len(t, api.allPosts, 1, "a non-recurring subscription should announce immediately")
			assert.Equal(t, postTypeConference, api.allPosts[0].Type)
			assert.Empty(t, sub.PendingConferences)
		})
	}
}

// TestPollSubscription_RecurringCalendarMatchMarksSubscription verifies that matching a conference
// to an occurrence of a recurring calendar event marks the subscription as a recurring calendar
// meeting, while a one-off event does not.
func TestPollSubscription_RecurringCalendarMatchMarksSubscription(t *testing.T) {
	for _, recurringID := range []string{"", "series1"} {
		t.Run("recurringEventId="+recurringID, func(t *testing.T) {
			now := time.Now().UTC()
			scheduledStart := now.Add(-time.Minute)
			event := calendarEventForCode("evt1", "abc-mnop-xyz", "Weekly sync", scheduledStart, scheduledStart.Add(30*time.Minute))
			event.RecurringEventID = recurringID
			srv := &recurringMeetingTestServer{
				records:        []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &now}},
				calendarStatus: http.StatusOK,
				calendarItems:  []calendarEvent{event},
			}
			srv.install(t)
			p, api, kv, sub := newRecurringMeetingTestPlugin(t)
			sub.LastRecurringMatchAt = time.Time{}

			p.pollSubscription(kv, sub)

			require.Len(t, api.allPosts, 1)
			if recurringID == "" {
				assert.True(t, sub.LastRecurringMatchAt.IsZero())
				return
			}
			assert.True(t, sub.LastRecurringMatchAt.Equal(scheduledStart))
			assert.True(t, isRecurringCalendarMeeting(sub))
		})
	}
}

func TestProcessPendingConferences_DropsExpiredLookupFailures(t *testing.T) {
	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	p := pollTestPlugin(t, api, kv)
	now := time.Now()
	sub := &kvstore.Subscription{
		PendingConferences: []kvstore.PendingConference{
			{ConferenceName: "conferenceRecords/expired", ConferenceStart: now, FirstSeenAt: now.Add(-pendingConferenceRetention - time.Hour)},
			{ConferenceName: "conferenceRecords/retry", ConferenceStart: now, FirstSeenAt: now},
		},
	}

	changed := p.processPendingConferences(kv, sub, newTestToken())
	assert.True(t, changed)
	require.Len(t, sub.PendingConferences, 1)
	assert.Equal(t, "conferenceRecords/retry", sub.PendingConferences[0].ConferenceName)
	assert.Empty(t, api.allPosts)
}

// TestPollSubscription_CalendarInsufficientScopesFallsBackAndNotifiesCreator verifies that a
// token missing the calendar scope doesn't block announcements (falls back to the cooldown
// heuristic) and that the creator is DM'd once, not on every poll cycle.
func TestPollSubscription_CalendarInsufficientScopesFallsBackAndNotifiesCreator(t *testing.T) {
	now := time.Now().UTC()
	token := &kvstore.OAuth2Token{AccessToken: "test-token", Expiry: now.Add(time.Hour)}

	var records []conferenceRecord
	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords":
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": records}))
		case "/calendars/primary/events":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}}`))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065", captureAllPosts: true}
	kv := newMockKVStore()
	kv.tokens["user1"] = token

	p := pollTestPlugin(t, api, kv)
	p.configuration.EnableCalendarScheduleSync = true

	sub := &kvstore.Subscription{
		SpaceID:                 "spaces/abc123",
		MeetingCode:             "abc-mnop-xyz",
		ChannelID:               "chan1",
		CreatedBy:               "user1",
		LastSeenConferenceStart: now.Add(-time.Hour),
	}

	records = []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &now}}
	p.pollSubscription(kv, sub)

	require.Len(t, api.allPosts, 2, "expected the conference post plus one reconnect DM")
	var sawConferencePost, sawReconnectDM bool
	for _, post := range api.allPosts {
		if post.Type == postTypeConference {
			sawConferencePost = true
			continue
		}
		sawReconnectDM = true
		assert.Contains(t, post.Message, "/meet connect")
	}
	assert.True(t, sawConferencePost, "the conference should still be announced via the cooldown fallback")
	assert.True(t, sawReconnectDM, "the creator should be notified that calendar sync fell back")

	sent, err := kv.HasCalendarReconnectNoticeSent("user1")
	require.NoError(t, err)
	assert.True(t, sent)

	// A second, distinct conference still falls back correctly, but the DM must not repeat.
	api.allPosts = nil
	later := now.Add(time.Hour)
	records = []conferenceRecord{{Name: "conferenceRecords/rec2", StartTime: &later}}
	p.pollSubscription(kv, sub)

	require.Len(t, api.allPosts, 1, "second conference should post once via fallback, without a repeat DM")
	assert.Equal(t, postTypeConference, api.allPosts[0].Type)
}

// dailySeries returns occurrences of a daily recurring calendar meeting on "abc-mnop-xyz", one per
// day offset, each starting at anchor plus that many days and lasting an hour.
func dailySeries(anchor time.Time, days ...int) []calendarEvent {
	events := make([]calendarEvent, 0, len(days))
	for _, day := range days {
		start := anchor.Add(time.Duration(day) * 24 * time.Hour)
		event := calendarEventForCode(fmt.Sprintf("series1_%s", start.Format("20060102T150405Z")), "abc-mnop-xyz", "PDE Meeting", start, start.Add(time.Hour))
		event.RecurringEventID = "series1"
		events = append(events, event)
	}
	return events
}

// TestPollSubscription_RecurringMeetingIncidentReplay replays the reported misfire end to end: the
// subscription learns it backs a recurring meeting from a real occurrence, then someone opens the
// link the next day, hours away from any occurrence, and nothing must be posted. The cooldown is
// disabled, so the calendar rule alone has to stop it.
func TestPollSubscription_RecurringMeetingIncidentReplay(t *testing.T) {
	now := time.Now().UTC()
	yesterday := now.Add(-20 * time.Hour)
	yesterdayEnd := yesterday.Add(48 * time.Minute)
	srv := &recurringMeetingTestServer{
		records:        []conferenceRecord{{Name: "conferenceRecords/yesterday", StartTime: &yesterday, EndTime: &yesterdayEnd}},
		calendarStatus: http.StatusOK,
		calendarItems:  dailySeries(yesterday, 0, 1),
	}
	srv.install(t)
	p, api, kv, sub := newRecurringMeetingTestPlugin(t)
	sub.LastSeenConferenceStart = now.Add(-48 * time.Hour)
	sub.LastRecurringMatchAt = time.Time{}

	p.pollSubscription(kv, sub)

	require.Len(t, api.allPosts, 1, "yesterday's scheduled occurrence should be announced")
	assert.True(t, isRecurringCalendarMeeting(sub), "matching a recurring occurrence should mark the subscription")

	api.allPosts = nil
	srv.records = []conferenceRecord{{Name: "conferenceRecords/stray", StartTime: &now}}
	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "a stray join hours away from any occurrence must not be announced")
	state, err := kv.GetConferencePostState("conferenceRecords/stray")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Suppressed)
	assert.NotContains(t, sub.ActiveConferenceIDs, "conferenceRecords/stray")
}

// TestPollSubscription_RecurringMeetingOnlyCalendarOccurrencesAnnounce throws hundreds of
// conferences at random times (fixed seed, so failures reproduce) at a recurring calendar meeting
// and checks the invariant behind the fix: a conference is announced, or deferred to its
// occurrence's start, only when it starts within the match window of an occurrence, and is
// suppressed otherwise. The cooldown is disabled so it can't mask a misfire.
func TestPollSubscription_RecurringMeetingOnlyCalendarOccurrencesAnnounce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	// Occurrences 20 minutes from now (and the same time on earlier days), so conferences shortly
	// before the next one exercise the deferral path, not just immediate posts.
	occurrences := dailySeries(now.Add(20*time.Minute), -3, -2, -1, 0)
	srv := &recurringMeetingTestServer{calendarStatus: http.StatusOK, calendarItems: occurrences}
	srv.install(t)

	rng := rand.New(rand.NewSource(20261113)) //nolint:gosec // fixed seed so failures reproduce
	var announced, deferred, suppressed int
	for i := range 400 {
		// Half the samples anywhere in the last 80 hours, half within 45 minutes of an occurrence
		// so the window edges and the deferral path get dense coverage.
		start := now.Add(-time.Duration(rng.Int63n(int64(80 * time.Hour))))
		if i%2 == 1 {
			occurrence := *occurrences[rng.Intn(len(occurrences))].Start.DateTime
			start = occurrence.Add(time.Duration(rng.Int63n(int64(90*time.Minute))) - 45*time.Minute)
		}
		if start.After(now) {
			continue // conferences can't start in the future
		}
		var nearest *calendarEvent
		var nearestDelta time.Duration
		for j := range occurrences {
			delta := occurrences[j].Start.DateTime.Sub(start).Abs()
			if nearest == nil || delta < nearestDelta {
				nearest, nearestDelta = &occurrences[j], delta
			}
		}
		if (nearestDelta - calendarMatchLead).Abs() < time.Minute {
			continue // too close to the window edge to be deterministic
		}
		inWindow := nearestDelta < calendarMatchLead

		name := fmt.Sprintf("conferenceRecords/random%d", i)
		srv.records = []conferenceRecord{{Name: name, StartTime: &start}}
		p, api, kv, sub := newRecurringMeetingTestPlugin(t)
		sub.LastSeenConferenceStart = now.Add(-100 * time.Hour)

		p.pollSubscription(kv, sub)

		msg := fmt.Sprintf("sample %d: conference at %s, nearest occurrence %s away", i, start.Format(time.RFC3339), nearestDelta)
		state, err := kv.GetConferencePostState(name)
		require.NoError(t, err, msg)
		switch {
		case !inWindow:
			suppressed++
			assert.Empty(t, api.allPosts, msg)
			assert.Empty(t, sub.ScheduledAnnouncements, msg)
			require.NotNil(t, state, msg)
			assert.True(t, state.Suppressed, msg)
		case nearest.Start.DateTime.After(time.Now()):
			deferred++
			assert.Empty(t, api.allPosts, msg)
			require.Len(t, sub.ScheduledAnnouncements, 1, msg)
			assert.Equal(t, nearest.ID, sub.ScheduledAnnouncements[0].EventInstanceID, msg)
		default:
			announced++
			require.Len(t, api.allPosts, 1, msg)
			assert.Equal(t, nearest.Start.DateTime.UTC().Format(time.RFC3339), api.allPosts[0].Props["scheduled_start"], msg)
		}
		assert.Empty(t, sub.PendingConferences, msg)
	}

	assert.Positive(t, announced, "the samples should include conferences at an occurrence")
	assert.Positive(t, deferred, "the samples should include early joins before the next occurrence")
	assert.Greater(t, suppressed, announced+deferred, "most random conferences fall outside the occurrences")
}

// TestPollSubscription_RecurringMeetingStrayJoinsNeverPostAcrossCycles drives many poll cycles of
// stray joins, mixed with calendar outages, against a recurring calendar meeting, and checks
// nothing is ever announced and no conference is classified twice.
func TestPollSubscription_RecurringMeetingStrayJoinsNeverPostAcrossCycles(t *testing.T) {
	now := time.Now().UTC()
	srv := &recurringMeetingTestServer{
		calendarStatus: http.StatusOK,
		calendarItems:  dailySeries(now.Add(-6*time.Hour), -1, 0, 1),
	}
	srv.install(t)
	p, api, kv, sub := newRecurringMeetingTestPlugin(t)
	sub.LastSeenConferenceStart = now.Add(-5 * time.Hour)

	for cycle := range 12 {
		start := now.Add(-4*time.Hour + time.Duration(cycle)*15*time.Minute)
		srv.records = []conferenceRecord{{Name: fmt.Sprintf("conferenceRecords/stray%d", cycle), StartTime: &start}}
		// Every third cycle the calendar is down, so the stray goes through the retry path.
		if cycle%3 == 0 {
			srv.calendarStatus = http.StatusServiceUnavailable
		} else {
			srv.calendarStatus = http.StatusOK
		}
		p.pollSubscription(kv, sub)
		assert.Empty(t, api.allPosts, "cycle %d: a stray join must never be announced", cycle)
	}

	srv.records = nil
	srv.calendarStatus = http.StatusOK
	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts)
	assert.Empty(t, sub.PendingConferences, "every retried lookup should be resolved once the calendar is back")
	for cycle := range 12 {
		state, err := kv.GetConferencePostState(fmt.Sprintf("conferenceRecords/stray%d", cycle))
		require.NoError(t, err)
		require.NotNil(t, state, "cycle %d", cycle)
		assert.True(t, state.Suppressed, "cycle %d", cycle)
	}
	assert.Empty(t, sub.ScheduledAnnouncements)
	assert.Empty(t, sub.ActiveConferenceIDs)
}

// TestPollSubscription_RecurringMeetingPendingLookupNotDuplicated verifies that while a lookup is
// being retried, Google listing the same conference again doesn't queue it twice or spend a
// second calendar call on it.
func TestPollSubscription_RecurringMeetingPendingLookupNotDuplicated(t *testing.T) {
	start := time.Now().UTC()
	srv := &recurringMeetingTestServer{
		records:        []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &start}},
		calendarStatus: http.StatusInternalServerError,
	}
	srv.install(t)
	p, api, kv, sub := newRecurringMeetingTestPlugin(t)

	for cycle := 1; cycle <= 3; cycle++ {
		// Rewind the watermark so Google lists the record again, as after a partial batch failure.
		sub.LastSeenConferenceStart = start.Add(-time.Minute)
		p.pollSubscription(kv, sub)

		assert.Equal(t, cycle, srv.calendarCalls, "one calendar lookup per cycle")
		require.Len(t, sub.PendingConferences, 1)
		assert.Empty(t, api.allPosts)
	}
}

// TestPollSubscription_RecurringMeetingFallsBackWithoutPrerequisites verifies the calendar-only
// rule needs everything it relies on: with calendar sync off, or a token missing the calendar
// scope, a recurring meeting's conferences go through the cooldown heuristic and post.
func TestPollSubscription_RecurringMeetingFallsBackWithoutPrerequisites(t *testing.T) {
	cases := []struct {
		name           string
		syncEnabled    bool
		calendarStatus int
	}{
		{"calendar sync disabled", false, http.StatusOK},
		{"token missing the calendar scope", true, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now().UTC()
			srv := &recurringMeetingTestServer{
				records:        []conferenceRecord{{Name: "conferenceRecords/rec1", StartTime: &start}},
				calendarStatus: http.StatusOK,
			}
			srv.install(t)
			if tc.calendarStatus == http.StatusForbidden {
				withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v2/conferenceRecords":
						w.WriteHeader(http.StatusOK)
						require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": srv.records}))
					case "/calendars/primary/events":
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"error":{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}}`))
					default:
						w.WriteHeader(http.StatusOK)
						require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
					}
				})
			}
			p, api, kv, sub := newRecurringMeetingTestPlugin(t)
			p.configuration.EnableCalendarScheduleSync = tc.syncEnabled

			p.pollSubscription(kv, sub)

			var conferencePosts int
			for _, post := range api.allPosts {
				if post.Type == postTypeConference {
					conferencePosts++
				}
			}
			assert.Equal(t, 1, conferencePosts, "without its prerequisites the calendar-only rule must not apply")
			assert.Empty(t, sub.PendingConferences)
			if !tc.syncEnabled {
				assert.Zero(t, srv.calendarCalls, "with sync disabled the calendar must not be called")
			}
		})
	}
}

func TestIsRecurringCalendarMeeting(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name                 string
		lastRecurringMatchAt time.Time
		want                 bool
	}{
		{"never matched", time.Time{}, false},
		{"matched yesterday", now.Add(-24 * time.Hour), true},
		{"next occurrence matched early", now.Add(10 * time.Minute), true},
		{"just inside the window", now.Add(-recurringMatchWindow + time.Hour), true},
		{"outside the window", now.Add(-recurringMatchWindow - time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isRecurringCalendarMeeting(&kvstore.Subscription{LastRecurringMatchAt: tc.lastRecurringMatchAt}))
		})
	}
}
