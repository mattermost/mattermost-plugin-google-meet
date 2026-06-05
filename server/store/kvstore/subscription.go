// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package kvstore

import (
	"encoding/json"
	"net/url"
	"slices"
	"time"

	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"
)

const (
	// #nosec G101 -- KV key prefixes are identifiers, not secrets.
	subscriptionPrefix     = "subscription_"
	subscriptionIndexKey   = "subscription_index"
	subscriptionUserPrefix = "subscription_user_"
	conferencePostPrefix   = "conference_post_"
	adHocPrefix            = "meet_adhoc_"
	adHocIndexKey          = "meet_adhoc_index"

	conferencePostStateTTL = 7 * 24 * time.Hour
	adHocMeetingPostTTL    = 24 * time.Hour
)

func subscriptionKey(spaceID string) string {
	return subscriptionPrefix + url.PathEscape(spaceID)
}

func subscriptionUserKey(userID string) string {
	return subscriptionUserPrefix + userID
}

func conferencePostKey(conferenceRecordName string) string {
	return conferencePostPrefix + url.PathEscape(conferenceRecordName)
}

func adHocKey(spaceID string) string {
	return adHocPrefix + url.PathEscape(spaceID)
}

func (kv *Client) StoreSubscription(sub *Subscription) error {
	data, err := json.Marshal(sub)
	if err != nil {
		return errors.Wrap(err, "failed to marshal subscription")
	}
	if _, err := kv.client.KV.Set(subscriptionKey(sub.SpaceID), data); err != nil {
		return errors.Wrap(err, "failed to store subscription")
	}
	return nil
}

func (kv *Client) GetSubscription(spaceID string) (*Subscription, error) {
	var data []byte
	if err := kv.client.KV.Get(subscriptionKey(spaceID), &data); err != nil {
		return nil, errors.Wrap(err, "failed to get subscription")
	}
	if len(data) == 0 {
		return nil, ErrSubscriptionNotFound
	}
	var sub Subscription
	if err := json.Unmarshal(data, &sub); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal subscription")
	}
	return &sub, nil
}

func (kv *Client) DeleteSubscription(spaceID string) error {
	if err := kv.client.KV.Delete(subscriptionKey(spaceID)); err != nil {
		return errors.Wrap(err, "failed to delete subscription")
	}
	return nil
}

func (kv *Client) ListAllSubscriptionSpaceIDs() ([]string, error) {
	var ids []string
	if err := kv.client.KV.Get(subscriptionIndexKey, &ids); err != nil {
		return nil, errors.Wrap(err, "failed to get subscription index")
	}
	return ids, nil
}

func (kv *Client) AddToUserSubscriptionIndex(userID, spaceID string) error {
	// Update global index.
	if err := kv.addToStringList(subscriptionIndexKey, spaceID); err != nil {
		return err
	}
	// Update per-user index.
	return kv.addToStringList(subscriptionUserKey(userID), spaceID)
}

func (kv *Client) RemoveFromUserSubscriptionIndex(userID, spaceID string) error {
	if err := kv.removeFromStringList(subscriptionIndexKey, spaceID); err != nil {
		return err
	}
	return kv.removeFromStringList(subscriptionUserKey(userID), spaceID)
}

func (kv *Client) ListUserSubscriptionSpaceIDs(userID string) ([]string, error) {
	var ids []string
	if err := kv.client.KV.Get(subscriptionUserKey(userID), &ids); err != nil {
		return nil, errors.Wrap(err, "failed to get user subscription index")
	}
	return ids, nil
}

// addToStringList appends value to the JSON-encoded list at key, retrying on
// concurrent modification via the KV CAS API. Note: this is atomic per key only —
// AddToUserSubscriptionIndex/RemoveFromUserSubscriptionIndex update two keys
// sequentially, and the plugin KV API offers no multi-key transactions, so the
// caller may briefly observe one index updated and not the other on a crash.
func (kv *Client) addToStringList(key, value string) error {
	err := kv.client.KV.SetAtomicWithRetries(key, func(oldValue []byte) (any, error) {
		list, err := unmarshalStringList(oldValue)
		if err != nil {
			return nil, err
		}
		if slices.Contains(list, value) {
			return list, nil
		}
		return append(list, value), nil
	})
	if err != nil {
		return errors.Wrap(err, "failed to atomically update list")
	}
	return nil
}

func (kv *Client) removeFromStringList(key, value string) error {
	err := kv.client.KV.SetAtomicWithRetries(key, func(oldValue []byte) (any, error) {
		list, err := unmarshalStringList(oldValue)
		if err != nil {
			return nil, err
		}
		filtered := list[:0]
		for _, v := range list {
			if v != value {
				filtered = append(filtered, v)
			}
		}
		return filtered, nil
	})
	if err != nil {
		return errors.Wrap(err, "failed to atomically update list")
	}
	return nil
}

func unmarshalStringList(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal existing list")
	}
	return list, nil
}

func (kv *Client) StoreConferencePostState(conferenceRecordName string, state *ConferencePostState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return errors.Wrap(err, "failed to marshal conference post state")
	}
	if _, err := kv.client.KV.Set(
		conferencePostKey(conferenceRecordName),
		data,
		pluginapi.SetExpiry(conferencePostStateTTL),
	); err != nil {
		return errors.Wrap(err, "failed to store conference post state")
	}
	return nil
}

func (kv *Client) GetConferencePostState(conferenceRecordName string) (*ConferencePostState, error) {
	var data []byte
	if err := kv.client.KV.Get(conferencePostKey(conferenceRecordName), &data); err != nil {
		return nil, errors.Wrap(err, "failed to get conference post state")
	}
	if len(data) == 0 {
		return nil, nil
	}
	var state ConferencePostState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal conference post state")
	}
	return &state, nil
}

func (kv *Client) StoreAdHocMeetingPost(spaceID string, entry *AdHocMeetingPost) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return errors.Wrap(err, "failed to marshal ad-hoc meeting post")
	}
	if _, err := kv.client.KV.Set(
		adHocKey(spaceID),
		data,
		pluginapi.SetExpiry(adHocMeetingPostTTL),
	); err != nil {
		return errors.Wrap(err, "failed to store ad-hoc meeting post")
	}
	return nil
}

func (kv *Client) GetAdHocMeetingPost(spaceID string) (*AdHocMeetingPost, error) {
	var data []byte
	if err := kv.client.KV.Get(adHocKey(spaceID), &data); err != nil {
		return nil, errors.Wrap(err, "failed to get ad-hoc meeting post")
	}
	if len(data) == 0 {
		return nil, nil
	}
	var entry AdHocMeetingPost
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal ad-hoc meeting post")
	}
	return &entry, nil
}

func (kv *Client) DeleteAdHocMeetingPost(spaceID string) error {
	if err := kv.client.KV.Delete(adHocKey(spaceID)); err != nil {
		return errors.Wrapf(err, "failed to delete ad-hoc meeting post for space %q", spaceID)
	}
	return nil
}

func (kv *Client) ListAdHocSpaceIDs() ([]string, error) {
	var ids []string
	if err := kv.client.KV.Get(adHocIndexKey, &ids); err != nil {
		return nil, errors.Wrap(err, "failed to get ad-hoc index")
	}
	return ids, nil
}

func (kv *Client) AddToAdHocIndex(spaceID string) error {
	return kv.addToStringList(adHocIndexKey, spaceID)
}

func (kv *Client) RemoveFromAdHocIndex(spaceID string) error {
	return kv.removeFromStringList(adHocIndexKey, spaceID)
}
