// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"fmt"
	"reflect"

	"github.com/pkg/errors"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

const (
	defaultPollIntervalSeconds = 60
	minPollIntervalSeconds     = 30
)

type configuration struct {
	GoogleClientID                string `json:"GoogleClientID"`
	GoogleClientSecret            string `json:"GoogleClientSecret"`
	EncryptionKey                 string `json:"EncryptionKey"`
	RestrictMeetingCreation       bool   `json:"RestrictMeetingCreation"`
	EnableConferenceArtifactPosts bool   `json:"EnableConferenceArtifactPosts"`
	PollIntervalSeconds           int    `json:"PollIntervalSeconds"`
}

func (c *configuration) pollInterval() int {
	// Non-positive values are treated as "unset" and get the default. Positive
	// values below the floor are clamped to minPollIntervalSeconds so admin
	// intent isn't silently inflated (e.g. 25 -> 30, not 60).
	if c.PollIntervalSeconds <= 0 {
		return defaultPollIntervalSeconds
	}
	if c.PollIntervalSeconds < minPollIntervalSeconds {
		return minPollIntervalSeconds
	}
	return c.PollIntervalSeconds
}

func (c *configuration) Clone() *configuration {
	clone := *c
	return &clone
}

func (c *configuration) IsValid() error {
	if c.GoogleClientID == "" {
		return errors.New("Google Client ID is not configured")
	}
	if c.GoogleClientSecret == "" {
		return errors.New("Google Client Secret is not configured")
	}
	if c.EncryptionKey == "" {
		return errors.New("Encryption Key is not configured")
	}
	return nil
}

func (p *Plugin) getConfiguration() *configuration {
	p.configurationLock.RLock()
	defer p.configurationLock.RUnlock()

	if p.configuration == nil {
		return &configuration{}
	}

	return p.configuration
}

func (p *Plugin) setConfiguration(configuration *configuration) {
	p.configurationLock.Lock()
	defer p.configurationLock.Unlock()

	if configuration != nil && p.configuration == configuration {
		if reflect.ValueOf(*configuration).NumField() == 0 {
			return
		}

		panic("setConfiguration called with the existing configuration")
	}

	p.configuration = configuration
}

func (p *Plugin) OnConfigurationChange() error {
	configuration := new(configuration)

	if err := p.API.LoadPluginConfiguration(configuration); err != nil {
		return errors.Wrap(err, "failed to load plugin configuration")
	}

	p.setConfiguration(configuration)
	if p.client != nil {
		if configuration.IsValid() == nil {
			p.setKVStore(kvstore.NewKVStore(p.client, configuration.EncryptionKey))
		} else {
			p.setKVStore(nil)
		}
		// Restart the poller with the potentially updated interval/enabled flag.
		p.stopPoller()
		p.startPoller()
	}
	p.updateSettingsHeader()

	return nil
}

func (p *Plugin) IsPluginConfigured() bool {
	return p.pluginReadinessError() == nil
}

func (p *Plugin) AreSubscriptionsEnabled() bool {
	return p.getConfiguration().EnableConferenceArtifactPosts
}

func (p *Plugin) GetPluginConfigureURL() string {
	siteURL := p.getSiteURL()
	if siteURL == "" {
		return ""
	}

	return fmt.Sprintf("%s/admin_console/plugins/plugin_%s", siteURL, manifestID())
}
