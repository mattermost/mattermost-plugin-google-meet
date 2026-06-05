// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mattermost/mattermost-plugin-google-meet/server/command"
	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

// extractMeetingCode returns the bare meeting code from any of these formats:
//   - "abc-mnop-xyz"
//   - "https://meet.google.com/abc-mnop-xyz"
//   - "https://meet.google.com/abc-mnop-xyz?authuser=0"
//   - "meet.google.com/abc-mnop-xyz"
func extractMeetingCode(input string) string {
	input = strings.TrimSpace(input)

	// Normalise schemeless URLs so url.Parse can handle them.
	normalised := input
	if !strings.Contains(input, "://") && strings.HasPrefix(input, "meet.google.com") {
		normalised = "https://" + input
	}

	if u, err := url.Parse(normalised); err == nil && u.Host == "meet.google.com" {
		// Strip leading slash and any trailing slashes from the path.
		return strings.Trim(u.Path, "/")
	}

	return input
}

// AddSubscription validates and creates a subscription binding the current channel to a Meet space.
func (p *Plugin) AddSubscription(userID, channelID, meetingCodeOrURL, description string) (*kvstore.Subscription, error) {
	store, err := p.getOAuthKVStore()
	if err != nil {
		return nil, err
	}

	token, err := p.getValidToken(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}
	if token == nil {
		return nil, errors.New("user not connected to Google")
	}

	meetingCode := extractMeetingCode(meetingCodeOrURL)

	space, err := p.getSpace(token, meetingCode)
	if err != nil {
		if errors.Is(err, ErrInsufficientScopes) {
			if delErr := store.DeleteOAuth2Token(userID); delErr != nil {
				p.API.LogWarn("Failed to delete token after insufficient scopes", "user_id", userID, "error", delErr.Error())
			}
			return nil, command.ErrNeedsReconnect
		}
		return nil, fmt.Errorf("could not find meeting space %q: %w", meetingCode, err)
	}

	spaceID := space.Name

	existing, err := store.GetSubscription(spaceID)
	if err != nil && !errors.Is(err, kvstore.ErrSubscriptionNotFound) {
		return nil, fmt.Errorf("failed to check existing subscriptions: %w", err)
	}
	if existing != nil {
		if existing.ChannelID == channelID {
			// Already subscribed to this channel; treat as idempotent.
			return existing, nil
		}
		return nil, fmt.Errorf("meeting space %q is already subscribed to another channel", meetingCode)
	}

	sub := &kvstore.Subscription{
		SpaceID:                 spaceID,
		MeetingCode:             space.MeetingCode,
		ChannelID:               channelID,
		Description:             description,
		CreatedBy:               userID,
		CreatedAt:               time.Now(),
		LastSeenConferenceStart: time.Now(),
	}

	if err := store.StoreSubscription(sub); err != nil {
		return nil, fmt.Errorf("failed to store subscription: %w", err)
	}

	if err := store.AddToUserSubscriptionIndex(userID, spaceID); err != nil {
		p.API.LogWarn("Failed to update subscription index", "user_id", userID, "space_id", spaceID, "error", err.Error())
	}

	return sub, nil
}

// RemoveSubscription removes a channel subscription for the given meeting code.
// The subscription must belong to channelID (no cross-channel removal).
// Resolution is local — no Google API call required.
func (p *Plugin) RemoveSubscription(userID, channelID, meetingCodeOrURL string) error {
	store, err := p.getOAuthKVStore()
	if err != nil {
		return err
	}

	meetingCode := extractMeetingCode(meetingCodeOrURL)

	spaceIDs, err := store.ListAllSubscriptionSpaceIDs()
	if err != nil {
		return fmt.Errorf("failed to list subscriptions: %w", err)
	}

	var match *kvstore.Subscription
	for _, spaceID := range spaceIDs {
		sub, subErr := store.GetSubscription(spaceID)
		if subErr != nil || sub == nil {
			continue
		}
		if sub.MeetingCode == meetingCode && sub.ChannelID == channelID {
			match = sub
			break
		}
	}
	if match == nil {
		return fmt.Errorf("no subscription to %q found in this channel", meetingCode)
	}

	if err := store.DeleteSubscription(match.SpaceID); err != nil {
		return fmt.Errorf("failed to delete subscription: %w", err)
	}

	if err := store.RemoveFromUserSubscriptionIndex(match.CreatedBy, match.SpaceID); err != nil {
		p.API.LogWarn("Failed to update subscription index on remove", "user_id", match.CreatedBy, "space_id", match.SpaceID, "error", err.Error())
	}

	p.API.LogInfo("Subscription removed", "removed_by", userID, "channel_id", channelID, "space_id", match.SpaceID, "meeting_code", meetingCode)
	return nil
}

// ListSubscriptions returns display info about subscriptions the user created.
func (p *Plugin) ListSubscriptions(userID string) ([]*command.SubscriptionInfo, error) {
	store, err := p.getOAuthKVStore()
	if err != nil {
		return nil, err
	}

	spaceIDs, err := store.ListUserSubscriptionSpaceIDs(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list subscription IDs: %w", err)
	}

	var result []*command.SubscriptionInfo
	for _, spaceID := range spaceIDs {
		sub, subErr := store.GetSubscription(spaceID)
		if subErr != nil || sub == nil {
			continue
		}
		channelName := sub.ChannelID
		if ch, chErr := p.client.Channel.Get(sub.ChannelID); chErr == nil && ch != nil {
			channelName = ch.Name
		}
		result = append(result, &command.SubscriptionInfo{
			MeetingCode: sub.MeetingCode,
			ChannelID:   sub.ChannelID,
			ChannelName: channelName,
			Description: sub.Description,
		})
	}
	return result, nil
}
