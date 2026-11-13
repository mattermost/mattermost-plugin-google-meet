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

// TestPollSubscription_CalendarNoMatchFallsBackToCooldown verifies that, on a subscription not
// known to back a recurring meeting, a conference with no matching calendar event (an unscheduled
// meeting on the link) is announced immediately via the pre-existing cooldown heuristic.
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
	assert.Empty(t, sub.PendingConferences)
	assert.Contains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1")
}

// pendingConferenceTestServer serves a single conference record (both from the list endpoint
// while listed is true and from the per-record endpoint) plus a calendar endpoint driven by
// calendarStatus/calendarItems, so tests can mutate them between poll cycles.
type pendingConferenceTestServer struct {
	record         conferenceRecord
	listed         bool
	calendarStatus int
	calendarItems  []calendarEvent
}

func (s *pendingConferenceTestServer) install(t *testing.T) {
	t.Helper()
	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/conferenceRecords":
			var records []conferenceRecord
			if s.listed {
				records = []conferenceRecord{s.record}
			}
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"conferenceRecords": records}))
		case "/v2/" + s.record.Name:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(s.record))
		case "/calendars/primary/events":
			if s.calendarStatus != http.StatusOK {
				w.WriteHeader(s.calendarStatus)
				return
			}
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"items": s.calendarItems}))
		default:
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{}))
		}
	})
}

// newPendingConferenceTestPlugin returns a calendar-synced plugin and a subscription that has
// recently matched an occurrence of a recurring calendar event.
func newPendingConferenceTestPlugin(t *testing.T) (*Plugin, *mockPluginAPI, *mockKVStore, *kvstore.Subscription) {
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

// TestPollSubscription_CalendarNoMatchShortConferenceSuppressed reproduces the misfire where
// someone briefly opens a subscribed link outside any scheduled calendar event: with no matching
// event and a conference that ended almost immediately, nothing should be announced.
func TestPollSubscription_CalendarNoMatchShortConferenceSuppressed(t *testing.T) {
	start := time.Now().UTC().Add(-2 * time.Minute)
	end := start.Add(20 * time.Second)
	srv := &pendingConferenceTestServer{
		record:         conferenceRecord{Name: "conferenceRecords/rec1", StartTime: &start, EndTime: &end},
		listed:         true,
		calendarStatus: http.StatusOK,
	}
	srv.install(t)
	p, api, kv, sub := newPendingConferenceTestPlugin(t)

	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "a short unscheduled conference must not be announced")
	assert.Empty(t, sub.PendingConferences)
	assert.NotContains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1")
	state, err := kv.GetConferencePostState("conferenceRecords/rec1")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Suppressed)
}

// TestPollSubscription_CalendarNoMatchDefersUntilMinDuration verifies that a still-running
// unscheduled conference is held back until it has run for unscheduledConferenceMinDuration,
// and then announced.
func TestPollSubscription_CalendarNoMatchDefersUntilMinDuration(t *testing.T) {
	start := time.Now().UTC().Add(-time.Minute)
	srv := &pendingConferenceTestServer{
		record:         conferenceRecord{Name: "conferenceRecords/rec1", StartTime: &start},
		listed:         true,
		calendarStatus: http.StatusOK,
	}
	srv.install(t)
	p, api, kv, sub := newPendingConferenceTestPlugin(t)

	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "an unscheduled conference should not be announced before the minimum duration")
	require.Len(t, sub.PendingConferences, 1)
	assert.Equal(t, "conferenceRecords/rec1", sub.PendingConferences[0].ConferenceName)
	assert.False(t, sub.PendingConferences[0].CalendarLookupPending)
	assert.NotContains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1")

	// Simulate the minimum duration elapsing while the conference is still running.
	earlier := time.Now().UTC().Add(-unscheduledConferenceMinDuration - time.Minute)
	srv.record.StartTime = &earlier
	srv.listed = false
	p.pollSubscription(kv, sub)

	require.Len(t, api.allPosts, 1, "the conference should be announced once it has run long enough")
	assert.Equal(t, postTypeConference, api.allPosts[0].Type)
	assert.Empty(t, sub.PendingConferences)
	assert.Contains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1")
}

// TestPollSubscription_CalendarLookupErrorRetriedThenMatched verifies that a transient calendar
// failure no longer falls straight back to the cooldown heuristic: the lookup is retried on the
// next poll and, once it succeeds, the calendar-anchored path takes over.
func TestPollSubscription_CalendarLookupErrorRetriedThenMatched(t *testing.T) {
	now := time.Now().UTC()
	scheduledStart := now.Add(10 * time.Minute)
	srv := &pendingConferenceTestServer{
		record:         conferenceRecord{Name: "conferenceRecords/rec1", StartTime: &now},
		listed:         true,
		calendarStatus: http.StatusInternalServerError,
	}
	srv.install(t)
	p, api, kv, sub := newPendingConferenceTestPlugin(t)

	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "a calendar lookup failure should not post immediately")
	require.Len(t, sub.PendingConferences, 1)
	assert.True(t, sub.PendingConferences[0].CalendarLookupPending)
	assert.Equal(t, 1, sub.PendingConferences[0].CalendarLookupAttempts)

	srv.listed = false
	srv.calendarStatus = http.StatusOK
	srv.calendarItems = []calendarEvent{calendarEventForCode("evt1", "abc-mnop-xyz", "Weekly sync", scheduledStart, scheduledStart.Add(30*time.Minute))}
	p.pollSubscription(kv, sub)

	assert.Empty(t, api.allPosts, "the matched conference should be deferred until its scheduled start")
	assert.Empty(t, sub.PendingConferences)
	require.Len(t, sub.ScheduledAnnouncements, 1)
	assert.Equal(t, "conferenceRecords/rec1", sub.ScheduledAnnouncements[0].ConferenceName)
}

// TestPollSubscription_CalendarLookupErrorRetriesExhausted verifies that after
// maxCalendarLookupAttempts failures the conference is treated as unscheduled, posting only
// then (it has already run past the minimum duration).
func TestPollSubscription_CalendarLookupErrorRetriesExhausted(t *testing.T) {
	start := time.Now().UTC().Add(-unscheduledConferenceMinDuration - time.Minute)
	srv := &pendingConferenceTestServer{
		record:         conferenceRecord{Name: "conferenceRecords/rec1", StartTime: &start},
		listed:         true,
		calendarStatus: http.StatusInternalServerError,
	}
	srv.install(t)
	p, api, kv, sub := newPendingConferenceTestPlugin(t)

	for attempt := 1; attempt < maxCalendarLookupAttempts; attempt++ {
		p.pollSubscription(kv, sub)
		srv.listed = false
		assert.Empty(t, api.allPosts, "no post while calendar lookup retries remain (attempt %d)", attempt)
		require.Len(t, sub.PendingConferences, 1)
		assert.Equal(t, attempt, sub.PendingConferences[0].CalendarLookupAttempts)
	}

	p.pollSubscription(kv, sub)

	require.Len(t, api.allPosts, 1, "the conference should fall back once retries are exhausted")
	assert.Equal(t, postTypeConference, api.allPosts[0].Type)
	assert.Empty(t, sub.PendingConferences)
	assert.Contains(t, sub.ActiveConferenceIDs, "conferenceRecords/rec1")
}

// TestPollSubscription_NonRecurringSubscriptionKeepsImmediateFallback verifies that the
// minimum-duration gate and calendar-error retries only apply to subscriptions known to back a
// recurring meeting: otherwise (never matched, or last recurring match outside
// recurringMatchWindow) conferences are announced immediately, as before.
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
			start := time.Now().UTC().Add(-2 * time.Minute)
			end := start.Add(20 * time.Second)
			srv := &pendingConferenceTestServer{
				record:         conferenceRecord{Name: "conferenceRecords/rec1", StartTime: &start, EndTime: &end},
				listed:         true,
				calendarStatus: tc.calendarStatus,
			}
			srv.install(t)
			p, api, kv, sub := newPendingConferenceTestPlugin(t)
			sub.LastRecurringMatchAt = tc.lastRecurringMatchAt

			p.pollSubscription(kv, sub)

			require.Len(t, api.allPosts, 1, "a non-recurring subscription should announce immediately")
			assert.Equal(t, postTypeConference, api.allPosts[0].Type)
			assert.Empty(t, sub.PendingConferences)
		})
	}
}

// TestPollSubscription_RecurringCalendarMatchMarksSubscription verifies that matching a conference
// to an occurrence of a recurring calendar event marks the subscription as backing a recurring
// meeting, while a one-off event does not.
func TestPollSubscription_RecurringCalendarMatchMarksSubscription(t *testing.T) {
	for _, recurringID := range []string{"", "series1"} {
		t.Run("recurringEventId="+recurringID, func(t *testing.T) {
			now := time.Now().UTC()
			scheduledStart := now.Add(-time.Minute)
			event := calendarEventForCode("evt1", "abc-mnop-xyz", "Weekly sync", scheduledStart, scheduledStart.Add(30*time.Minute))
			event.RecurringEventID = recurringID
			srv := &pendingConferenceTestServer{
				record:         conferenceRecord{Name: "conferenceRecords/rec1", StartTime: &now},
				listed:         true,
				calendarStatus: http.StatusOK,
				calendarItems:  []calendarEvent{event},
			}
			srv.install(t)
			p, api, kv, sub := newPendingConferenceTestPlugin(t)
			sub.LastRecurringMatchAt = time.Time{}

			p.pollSubscription(kv, sub)

			require.Len(t, api.allPosts, 1)
			if recurringID == "" {
				assert.True(t, sub.LastRecurringMatchAt.IsZero())
				return
			}
			assert.True(t, sub.LastRecurringMatchAt.Equal(scheduledStart))
			assert.True(t, isRecurringMeetingSubscription(sub))
		})
	}
}

func TestProcessPendingConferences_DropsExpiredRefreshFailures(t *testing.T) {
	withCalendarPollServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	api := &mockPluginAPI{siteURL: "http://localhost:8065"}
	kv := newMockKVStore()
	p := pollTestPlugin(t, api, kv)
	now := time.Now()
	sub := &kvstore.Subscription{
		PendingConferences: []kvstore.PendingConference{
			{ConferenceName: "conferenceRecords/expired", FirstSeenAt: now.Add(-pendingConferenceRetention - time.Hour)},
			{ConferenceName: "conferenceRecords/retry", FirstSeenAt: now},
		},
	}

	changed := p.processPendingConferences(kv, sub, newTestToken())
	assert.True(t, changed)
	require.Len(t, sub.PendingConferences, 1)
	assert.Equal(t, "conferenceRecords/retry", sub.PendingConferences[0].ConferenceName)
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
