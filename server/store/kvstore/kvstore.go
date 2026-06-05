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
	// ActiveConferenceIDs are conference records we are still monitoring for artifacts.
	ActiveConferenceIDs []string `json:"active_conference_ids,omitempty"`
}

// ConferencePostState tracks what artifacts have been posted for one conferenceRecord.
type ConferencePostState struct {
	RootPostID          string   `json:"root_post_id"`
	ChannelID           string   `json:"channel_id"`
	PostedRecordingIDs  []string `json:"posted_recording_ids"`
	PostedTranscriptIDs []string `json:"posted_transcript_ids"`
	PostedSmartNoteIDs  []string `json:"posted_smart_note_ids"`
}

// AdHocMeetingPost is stored when a user starts an ad-hoc meeting via /meet start.
// It binds the meeting space to the Mattermost post and channel so the polling loop
// can post recording/transcript/smart-note artifacts without an explicit subscription.
type AdHocMeetingPost struct {
	RootPostID string    `json:"root_post_id"`
	ChannelID  string    `json:"channel_id"`
	UserID     string    `json:"user_id"` // used to obtain the OAuth token for Meet API calls
	CreatedAt  time.Time `json:"created_at"`
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
}
