// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package kvstore

import (
	"errors"
	"time"
)

// ErrStateNotFound is returned when an OAuth state is not found or expired.
var ErrStateNotFound = errors.New("OAuth state not found or expired")

// ErrSubscriptionNotFound is returned when a subscription does not exist.
var ErrSubscriptionNotFound = errors.New("subscription not found")

// OAuth2Token represents an OAuth2 token.
type OAuth2Token struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

// Subscription represents a channel subscription to a Google Meet space.
type Subscription struct {
	SpaceID     string    `json:"space_id"`
	MeetingCode string    `json:"meeting_code"`
	ChannelID   string    `json:"channel_id"`
	Description string    `json:"description,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	// LastSeenConferenceStart is used to page forward through conferenceRecords.
	LastSeenConferenceStart time.Time `json:"last_seen_conference_start"`
	// LastConferenceEndTime is the most recent conference end observed on this space.
	// It anchors the conference-start cooldown guard (quiet period since last activity).
	LastConferenceEndTime time.Time `json:"last_conference_end_time,omitzero"`
	// ActiveConferenceIDs are conference records we are still monitoring for artifacts.
	ActiveConferenceIDs []string `json:"active_conference_ids,omitempty"`
	// ScheduledAnnouncements are conference-started posts deferred until their calendar
	// event's official start time, so an early join doesn't drift the notification away
	// from the schedule. Only populated when EnableCalendarScheduleSync is on.
	ScheduledAnnouncements []ScheduledAnnouncement `json:"scheduled_announcements,omitempty"`
	// EventPostBindings map an announced calendar event instance to the single post created
	// for it, so repeat conference records within the same instance (e.g. a drop and rejoin)
	// reuse the post instead of creating a duplicate.
	EventPostBindings []EventPostBinding `json:"event_post_bindings,omitempty"`
}

// ScheduledAnnouncement is a conference-started post waiting for its calendar event's
// scheduled start time before it is created.
type ScheduledAnnouncement struct {
	ConferenceName  string `json:"conference_name"`
	EventInstanceID string `json:"event_instance_id"`
	EventSummary    string `json:"event_summary,omitempty"`
	// EventEnd is carried along so the post can be bound to an EventPostBinding once created,
	// without a second calendar lookup at due time.
	EventEnd time.Time `json:"event_end,omitzero"`
	DueAt    time.Time `json:"due_at"`
}

// EventPostBinding records that a calendar event instance has already been announced, and
// which conference records and post it is bound to.
type EventPostBinding struct {
	EventInstanceID string    `json:"event_instance_id"`
	MeetingPostID   string    `json:"meeting_post_id"`
	ConferenceNames []string  `json:"conference_names,omitempty"`
	ScheduledEnd    time.Time `json:"scheduled_end,omitzero"`
	// EndedPosted marks that markMeetingEnded has already run for this binding, so it is not
	// invoked again once every bound conference record is known to have ended.
	EndedPosted bool `json:"ended_posted,omitempty"`
	// ExpiresAt is when the binding can be pruned from the subscription (scheduled end plus
	// the conference post state TTL), so a long-lived subscription's record can't grow forever.
	ExpiresAt time.Time `json:"expires_at"`
}

// ConferencePostState tracks what artifacts have been posted for one conferenceRecord.
type ConferencePostState struct {
	MeetingPostID string `json:"meeting_post_id"`
	// ThreadRootID is the RootId used when posting artifacts. For top-level meetings
	// this matches MeetingPostID; for thread-started meetings it is the parent thread root.
	ThreadRootID        string   `json:"thread_root_id,omitempty"`
	ChannelID           string   `json:"channel_id"`
	PostedRecordingIDs  []string `json:"posted_recording_ids"`
	PostedTranscriptIDs []string `json:"posted_transcript_ids"`
	PostedSmartNoteIDs  []string `json:"posted_smart_note_ids"`
	MeetingEndedPosted  bool     `json:"meeting_ended_posted,omitempty"`
	// Suppressed marks a conference that was recognized but not announced because it started
	// within the cooldown window of the previous conference ending on the same space.
	Suppressed bool `json:"suppressed,omitempty"`
}

// ArtifactThreadRoot returns the RootId to use when posting artifacts.
func (s *ConferencePostState) ArtifactThreadRoot() string {
	if s.ThreadRootID != "" {
		return s.ThreadRootID
	}
	return s.MeetingPostID
}

// AdHocMeetingPost is stored when a user starts an ad-hoc meeting via /meet start.
// It binds the meeting space to the Mattermost post and channel so the polling loop
// can post recording/transcript/smart-note artifacts without an explicit subscription.
type AdHocMeetingPost struct {
	MeetingPostID string `json:"meeting_post_id"`
	// ThreadRootID is the RootId to use when posting artifacts.
	ThreadRootID string    `json:"thread_root_id,omitempty"`
	ChannelID    string    `json:"channel_id"`
	UserID       string    `json:"user_id"` // used to obtain the OAuth token for Meet API calls
	CreatedAt    time.Time `json:"created_at"`
}

type KVStore interface {
	// OAuth
	StoreOAuth2Token(userID string, token *OAuth2Token) error
	GetOAuth2Token(userID string) (*OAuth2Token, error)
	DeleteOAuth2Token(userID string) error
	StoreOAuth2State(state string, userID string) error
	GetAndDeleteOAuth2State(state string) (string, error)

	// Subscriptions
	StoreSubscription(sub *Subscription) error
	GetSubscription(spaceID string) (*Subscription, error)
	DeleteSubscription(spaceID string) error
	ListAllSubscriptionSpaceIDs() ([]string, error)
	AddToUserSubscriptionIndex(userID, spaceID string) error
	RemoveFromUserSubscriptionIndex(userID, spaceID string) error
	ListUserSubscriptionSpaceIDs(userID string) ([]string, error)

	// Conference post state
	StoreConferencePostState(conferenceRecordName string, state *ConferencePostState) error
	GetConferencePostState(conferenceRecordName string) (*ConferencePostState, error)

	// Ad-hoc meeting posts (started via /meet start, no explicit subscription)
	StoreAdHocMeetingPost(spaceID string, entry *AdHocMeetingPost) error
	GetAdHocMeetingPost(spaceID string) (*AdHocMeetingPost, error)
	DeleteAdHocMeetingPost(spaceID string) error
	ListAdHocSpaceIDs() ([]string, error)
	AddToAdHocIndex(spaceID string) error
	RemoveFromAdHocIndex(spaceID string) error

	// Calendar reconnect notices: a one-time-per-window DM telling a subscription creator
	// their token is missing the calendar scope, so the cooldown fallback isn't silent.
	StoreCalendarReconnectNoticeSent(userID string) error
	HasCalendarReconnectNoticeSent(userID string) (bool, error)
}
