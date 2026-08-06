// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package kvstore

import (
	"time"

	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"
)

const (
	// #nosec G101 -- KV key prefix is an identifier, not a secret.
	calendarReconnectNoticePrefix = "calendar_reconnect_notice_"
	// calendarReconnectNoticeTTL bounds how long the one-time DM is suppressed for, so a
	// user whose token is still missing the calendar scope is reminded again after a day
	// rather than staying silent forever.
	calendarReconnectNoticeTTL = 24 * time.Hour
)

func calendarReconnectNoticeKey(userID string) string {
	return calendarReconnectNoticePrefix + userID
}

// StoreCalendarReconnectNoticeSent records that userID was already notified about a missing
// calendar OAuth scope, so the poller doesn't send the same DM on every cycle.
func (kv *Client) StoreCalendarReconnectNoticeSent(userID string) error {
	if _, err := kv.client.KV.Set(
		calendarReconnectNoticeKey(userID),
		[]byte("1"),
		pluginapi.SetExpiry(calendarReconnectNoticeTTL),
	); err != nil {
		return errors.Wrap(err, "failed to store calendar reconnect notice state")
	}
	return nil
}

// HasCalendarReconnectNoticeSent reports whether userID was already notified within the TTL window.
func (kv *Client) HasCalendarReconnectNoticeSent(userID string) (bool, error) {
	var data []byte
	if err := kv.client.KV.Get(calendarReconnectNoticeKey(userID), &data); err != nil {
		return false, errors.Wrap(err, "failed to get calendar reconnect notice state")
	}
	return len(data) > 0, nil
}
