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
)

// withCalendarServer points googleCalendarURL/httpClient at an httptest server for the
// duration of the test and restores the originals on cleanup.
func withCalendarServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	origURL := googleCalendarURL
	origClient := httpClient
	googleCalendarURL = server.URL
	httpClient = server.Client()
	t.Cleanup(func() {
		googleCalendarURL = origURL
		httpClient = origClient
	})
}

func calendarEventsResponse(t *testing.T, events []calendarEvent) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"items": events})
	require.NoError(t, err)
	return body
}

func timedEvent(id, meetingCode string, start, end time.Time) calendarEvent {
	return calendarEvent{
		ID:             id,
		Summary:        "Weekly engineering sync",
		Start:          calendarEventDateTime{DateTime: &start},
		End:            calendarEventDateTime{DateTime: &end},
		ConferenceData: &calendarConferenceData{ConferenceID: meetingCode},
	}
}

func TestFindScheduledInstance_MatchesConferenceID(t *testing.T) {
	start := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	event := timedEvent("evt1", "abc-mnop-xyz", start, end)

	withCalendarServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/calendars/primary/events", r.URL.Path)
		assert.Equal(t, "true", r.URL.Query().Get("singleEvents"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(calendarEventsResponse(t, []calendarEvent{event}))
	})

	p := &Plugin{}
	instance, err := p.findScheduledInstance(newTestToken(), "abc-mnop-xyz", start.Add(-5*time.Minute))
	require.NoError(t, err)
	require.NotNil(t, instance)
	assert.Equal(t, "evt1", instance.InstanceID)
	assert.Equal(t, "Weekly engineering sync", instance.Summary)
	assert.True(t, instance.Start.Equal(start))
	assert.True(t, instance.End.Equal(end))
}

func TestFindScheduledInstance_NoMatchingConferenceID(t *testing.T) {
	start := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	event := timedEvent("evt1", "different-code", start, start.Add(30*time.Minute))

	withCalendarServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(calendarEventsResponse(t, []calendarEvent{event}))
	})

	p := &Plugin{}
	instance, err := p.findScheduledInstance(newTestToken(), "abc-mnop-xyz", start)
	require.NoError(t, err)
	assert.Nil(t, instance, "an event with a different conferenceId must not match")
}

func TestFindScheduledInstance_NoEventsAtAll(t *testing.T) {
	withCalendarServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(calendarEventsResponse(t, nil))
	})

	p := &Plugin{}
	instance, err := p.findScheduledInstance(newTestToken(), "abc-mnop-xyz", time.Now())
	require.NoError(t, err)
	assert.Nil(t, instance, "an off-hours conference with no calendar activity must not match")
}

func TestFindScheduledInstance_SkipsCancelledEvent(t *testing.T) {
	start := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	cancelled := timedEvent("evt1", "abc-mnop-xyz", start, start.Add(30*time.Minute))
	cancelled.Status = "cancelled"

	withCalendarServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(calendarEventsResponse(t, []calendarEvent{cancelled}))
	})

	p := &Plugin{}
	instance, err := p.findScheduledInstance(newTestToken(), "abc-mnop-xyz", start)
	require.NoError(t, err)
	assert.Nil(t, instance, "a cancelled instance must not match")
}

func TestFindScheduledInstance_SkipsAllDayEvent(t *testing.T) {
	allDay := calendarEvent{
		ID:             "evt1",
		Start:          calendarEventDateTime{Date: "2026-08-03"},
		End:            calendarEventDateTime{Date: "2026-08-04"},
		ConferenceData: &calendarConferenceData{ConferenceID: "abc-mnop-xyz"},
	}

	withCalendarServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(calendarEventsResponse(t, []calendarEvent{allDay}))
	})

	p := &Plugin{}
	instance, err := p.findScheduledInstance(newTestToken(), "abc-mnop-xyz", time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Nil(t, instance, "an all-day event has no meaningful start/end and must not match")
}

// TestFindScheduledInstance_PicksClosestAmongOverlapping verifies that when two back-to-back
// instances of the same recurring meeting both fall inside the match window, the one closest to
// the conference's actual start is preferred.
func TestFindScheduledInstance_PicksClosestAmongOverlapping(t *testing.T) {
	earlier := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	events := []calendarEvent{
		timedEvent("evt-earlier", "abc-mnop-xyz", earlier, earlier.Add(30*time.Minute)),
		timedEvent("evt-later", "abc-mnop-xyz", later, later.Add(30*time.Minute)),
	}

	withCalendarServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(calendarEventsResponse(t, events))
	})

	p := &Plugin{}
	// Actual conference start lands just after the later instance's scheduled start.
	instance, err := p.findScheduledInstance(newTestToken(), "abc-mnop-xyz", later.Add(2*time.Minute))
	require.NoError(t, err)
	require.NotNil(t, instance)
	assert.Equal(t, "evt-later", instance.InstanceID)
}

func TestFindScheduledInstance_InsufficientScopes(t *testing.T) {
	withCalendarServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}}`))
	})

	p := &Plugin{}
	_, err := p.findScheduledInstance(newTestToken(), "abc-mnop-xyz", time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientScopes)
}

func TestListCalendarEvents_Pagination(t *testing.T) {
	callCount := 0
	withCalendarServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		if callCount == 1 {
			_, _ = w.Write([]byte(`{"items":[{"id":"evt1"}],"nextPageToken":"page2"}`))
			return
		}
		assert.Equal(t, "page2", r.URL.Query().Get("pageToken"))
		_, _ = w.Write([]byte(`{"items":[{"id":"evt2"}]}`))
	})

	p := &Plugin{}
	events, err := p.listCalendarEvents(newTestToken(), time.Now(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Len(t, events, 2)
	assert.Equal(t, 2, callCount)
}
