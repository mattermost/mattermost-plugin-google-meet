// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConfiguration_PollInterval(t *testing.T) {
	cases := []struct {
		name string
		set  int
		want int
	}{
		{"unset falls back to default", 0, defaultPollIntervalSeconds},
		{"negative falls back to default", -10, defaultPollIntervalSeconds},
		{"positive below minimum is clamped to minimum", minPollIntervalSeconds - 1, minPollIntervalSeconds},
		{"exact minimum is honoured", minPollIntervalSeconds, minPollIntervalSeconds},
		{"larger value is honoured", 300, 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &configuration{PollIntervalSeconds: tc.set}
			assert.Equal(t, tc.want, c.pollInterval())
		})
	}
}

func TestConfiguration_ConferenceStartCooldown(t *testing.T) {
	cases := []struct {
		name string
		set  int
		want time.Duration
	}{
		{"unset disables the guard", 0, 0},
		{"negative disables the guard", -5, 0},
		{"positive value converts to hours", 12, 12 * time.Hour},
		{"single hour", 1, time.Hour},
		{"maximum safe value converts without overflow", maxConferenceStartCooldownHours, time.Duration(maxConferenceStartCooldownHours) * time.Hour},
		{"overflowing value is clamped to maximum", maxConferenceStartCooldownHours + 1, time.Duration(maxConferenceStartCooldownHours) * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &configuration{ConferenceStartCooldownHours: tc.set}
			assert.Equal(t, tc.want, c.conferenceStartCooldown())
		})
	}
}

func TestConfiguration_IsValid(t *testing.T) {
	cases := []struct {
		name    string
		cfg     configuration
		wantErr string
	}{
		{"missing client id", configuration{GoogleClientSecret: "s", EncryptionKey: "k"}, "Client ID"},
		{"missing client secret", configuration{GoogleClientID: "id", EncryptionKey: "k"}, "Client Secret"},
		{"missing encryption key", configuration{GoogleClientID: "id", GoogleClientSecret: "s"}, "Encryption Key"},
		{"valid", configuration{GoogleClientID: "id", GoogleClientSecret: "s", EncryptionKey: "k"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.IsValid()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}
