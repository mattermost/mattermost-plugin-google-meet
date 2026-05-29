// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

func newTestToken() *kvstore.OAuth2Token {
	return &kvstore.OAuth2Token{
		AccessToken: "test-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Hour),
	}
}

func TestGetSpace_Success(t *testing.T) {
	space := meetSpace{
		Name:        "spaces/abc123",
		MeetingURI:  "https://meet.google.com/abc-mnop-xyz",
		MeetingCode: "abc-mnop-xyz",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/v2/spaces/")
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(space))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	p := &Plugin{}
	got, err := p.getSpace(newTestToken(), "abc-mnop-xyz")
	require.NoError(t, err)
	assert.Equal(t, "spaces/abc123", got.Name)
	assert.Equal(t, "abc-mnop-xyz", got.MeetingCode)
}

func TestGetSpace_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	p := &Plugin{}
	_, err := p.getSpace(newTestToken(), "bad-code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestListConferenceRecords_Success(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	records := []conferenceRecord{
		{Name: "conferenceRecords/rec1", StartTime: &now, Space: "spaces/abc123"},
		{Name: "conferenceRecords/rec2", StartTime: &now, Space: "spaces/abc123"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/v2/conferenceRecords")
		assert.Contains(t, r.URL.RawQuery, "filter")
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"conferenceRecords": records,
		}))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	p := &Plugin{}
	got, err := p.listConferenceRecords(newTestToken(), "spaces/abc123", time.Time{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "conferenceRecords/rec1", got[0].Name)
}

func TestListConferenceRecords_WithSinceFilter(t *testing.T) {
	since := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	before := since.Add(-time.Hour)
	after := since.Add(time.Hour)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawFilter, err := url.QueryUnescape(r.URL.RawQuery)
		require.NoError(t, err)
		assert.Contains(t, rawFilter, `start_time>="2024-01-15T12:00:00Z"`)
		assert.Contains(t, rawFilter, `space.name="spaces/abc123"`)
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"conferenceRecords": []conferenceRecord{
				{Name: "conferenceRecords/old", StartTime: &before},
				{Name: "conferenceRecords/new", StartTime: &after},
			},
		}))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	p := &Plugin{}
	got, err := p.listConferenceRecords(newTestToken(), "spaces/abc123", since)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "conferenceRecords/new", got[0].Name)
}

func TestListConferenceRecords_Pagination(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"conferenceRecords": []conferenceRecord{{Name: "conferenceRecords/rec1"}},
				"nextPageToken":     "page2token",
			}))
			return
		}
		assert.Contains(t, r.URL.RawQuery, "page2token")
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"conferenceRecords": []conferenceRecord{{Name: "conferenceRecords/rec2"}},
		}))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	p := &Plugin{}
	got, err := p.listConferenceRecords(newTestToken(), "spaces/abc123", time.Time{})
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, 2, callCount)
}

func TestListRecordings_Success(t *testing.T) {
	exportURI := "https://drive.google.com/file/d/abc/view"
	recordings := []meetRecording{
		{
			Name:             "conferenceRecords/rec1/recordings/r1",
			State:            meetStateFileGenerated,
			DriveDestination: &driveDestination{ExportURI: exportURI},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/recordings")
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"recordings": recordings,
		}))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	p := &Plugin{}
	got, err := p.listRecordings(newTestToken(), "conferenceRecords/rec1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, meetStateFileGenerated, got[0].State)
	assert.Equal(t, exportURI, got[0].DriveDestination.ExportURI)
}

func TestListTranscriptEntries_Success(t *testing.T) {
	now := time.Now().UTC()
	entries := []transcriptEntry{
		{
			Name:      "conferenceRecords/rec1/transcripts/t1/entries/e1",
			Text:      "Hello world",
			StartTime: now,
			EndTime:   now.Add(2 * time.Second),
		},
	}
	entries[0].ParticipantDevice.DisplayName = "Alice"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/entries")
		w.WriteHeader(http.StatusOK)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"transcriptEntries": entries,
		}))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	p := &Plugin{}
	got, err := p.listTranscriptEntries(newTestToken(), "conferenceRecords/rec1/transcripts/t1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Alice", got[0].ParticipantDevice.DisplayName)
	assert.Equal(t, "Hello world", got[0].Text)
}

func TestBuildTranscriptText(t *testing.T) {
	ts := time.Date(2024, 1, 15, 9, 30, 5, 0, time.UTC)
	entries := []transcriptEntry{
		{Text: "Hello", StartTime: ts, EndTime: ts.Add(3 * time.Second)},
		{Text: "How are you?", StartTime: ts.Add(5 * time.Second), EndTime: ts.Add(8 * time.Second)},
		{Text: "", StartTime: ts.Add(10 * time.Second)}, // blank — should be skipped
	}
	entries[0].ParticipantDevice.DisplayName = "Alice"
	entries[1].ParticipantDevice.DisplayName = "Bob"

	result := buildTranscriptText(entries)

	// Must be valid WebVTT (the mattermost-ai plugin parses it with astisub.ReadFromWebVTT).
	assert.True(t, strings.HasPrefix(result, "WEBVTT\n"), "output must start with WEBVTT header")
	assert.Contains(t, result, "09:30:05.000 --> 09:30:08.000")
	assert.Contains(t, result, "Alice: Hello")
	assert.Contains(t, result, "09:30:10.000 --> 09:30:13.000")
	assert.Contains(t, result, "Bob: How are you?")
	// Blank entry must be omitted.
	assert.NotContains(t, result, "09:30:15")
}

func TestMeetGet_InsufficientScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}}`))
	}))
	defer server.Close()

	origURL := googleMeetURL
	origClient := httpClient
	googleMeetURL = server.URL + "/v2"
	httpClient = server.Client()
	defer func() { googleMeetURL = origURL; httpClient = origClient }()

	p := &Plugin{}
	_, err := p.meetGet(newTestToken(), "/spaces/abc")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientScopes)
}
