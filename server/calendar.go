// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

// calendarMatchLead and calendarMatchTrail bound how far a conference's actual start can drift
// from a calendar event's scheduled start and still be considered the same meeting: an early
// join up to calendarMatchLead before the scheduled start, or a late join up to calendarMatchTrail
// after it (covering someone joining slightly after the meeting was due to start).
const (
	calendarMatchLead  = 30 * time.Minute
	calendarMatchTrail = 30 * time.Minute
)

// googleCalendarURL is a var so tests can override it with an httptest server.
var googleCalendarURL = "https://www.googleapis.com/calendar/v3"

// calendarEventDateTime models the Calendar API's EventDateTime resource. Only timed events
// (dateTime set) can back a Meet conference; all-day events use "date" instead and are ignored.
type calendarEventDateTime struct {
	DateTime *time.Time `json:"dateTime,omitempty"`
	Date     string     `json:"date,omitempty"`
}

type calendarConferenceData struct {
	ConferenceID string `json:"conferenceId,omitempty"`
}

type calendarEvent struct {
	ID             string                  `json:"id"`
	Status         string                  `json:"status"`
	Summary        string                  `json:"summary"`
	Start          calendarEventDateTime   `json:"start"`
	End            calendarEventDateTime   `json:"end"`
	ConferenceData *calendarConferenceData `json:"conferenceData,omitempty"`
}

// calendarInstance is the resolved, single-occurrence calendar event backing a Meet conference.
// InstanceID is unique per occurrence (Calendar API returns singleEvents=true), so it doubles as
// the dedupe key for repeat conference records within the same scheduled meeting.
type calendarInstance struct {
	InstanceID string
	Summary    string
	Start      time.Time
	End        time.Time
}

// calendarGet performs an authenticated GET against the Calendar REST API and returns the raw body.
func (p *Plugin) calendarGet(token *kvstore.OAuth2Token, path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, googleCalendarURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && p.API != nil {
			p.API.LogWarn("Failed to close response body", "path", path, "error", closeErr.Error())
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "ACCESS_TOKEN_SCOPE_INSUFFICIENT") {
		return nil, ErrInsufficientScopes
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("calendar API status %d for %s: %s", resp.StatusCode, path, string(body))
	}

	return body, nil
}

// listCalendarEvents returns all single-occurrence events on the user's primary calendar whose
// window overlaps [timeMin, timeMax).
func (p *Plugin) listCalendarEvents(token *kvstore.OAuth2Token, timeMin, timeMax time.Time) ([]calendarEvent, error) {
	type listResp struct {
		Items         []calendarEvent `json:"items"`
		NextPageToken string          `json:"nextPageToken"`
	}

	query := url.Values{
		"timeMin":      {timeMin.UTC().Format(time.RFC3339Nano)},
		"timeMax":      {timeMax.UTC().Format(time.RFC3339Nano)},
		"singleEvents": {"true"},
		"orderBy":      {"startTime"},
	}

	var all []calendarEvent
	pageToken := ""
	for {
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		path := "/calendars/primary/events?" + query.Encode()
		body, err := p.calendarGet(token, path)
		if err != nil {
			return nil, err
		}
		var resp listResp
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse calendar events: %w", err)
		}
		all = append(all, resp.Items...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return all, nil
}

// findScheduledInstance returns the calendar event instance whose conference ID matches
// meetingCode and whose scheduled window brackets at, or nil when no such instance exists
// (the conference is unscheduled, or the creator isn't an organizer/invitee on it). Among
// overlapping candidates it picks the one whose scheduled start is closest to at.
func (p *Plugin) findScheduledInstance(token *kvstore.OAuth2Token, meetingCode string, at time.Time) (*calendarInstance, error) {
	events, err := p.listCalendarEvents(token, at.Add(-calendarMatchLead), at.Add(calendarMatchTrail))
	if err != nil {
		return nil, err
	}

	var best *calendarInstance
	var bestDelta time.Duration
	for i := range events {
		instance := calendarInstanceFromEvent(&events[i], meetingCode)
		if instance == nil {
			continue
		}
		delta := instance.Start.Sub(at)
		if delta < 0 {
			delta = -delta
		}
		if best == nil || delta < bestDelta {
			best = instance
			bestDelta = delta
		}
	}
	return best, nil
}

// calendarInstanceFromEvent converts a raw calendar event into a calendarInstance, or returns
// nil if the event is cancelled, all-day, or not backed by a Meet conference matching meetingCode.
func calendarInstanceFromEvent(event *calendarEvent, meetingCode string) *calendarInstance {
	if event.Status == "cancelled" {
		return nil
	}
	if event.ConferenceData == nil || event.ConferenceData.ConferenceID != meetingCode {
		return nil
	}
	if event.Start.DateTime == nil || event.End.DateTime == nil {
		// All-day event: no meaningful start/end time to anchor against.
		return nil
	}
	return &calendarInstance{
		InstanceID: event.ID,
		Summary:    event.Summary,
		Start:      *event.Start.DateTime,
		End:        *event.End.DateTime,
	}
}
