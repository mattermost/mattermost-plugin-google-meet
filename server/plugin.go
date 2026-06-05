// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"

	"github.com/mattermost/mattermost-plugin-google-meet/server/command"
	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

// Plugin implements the interface expected by the Mattermost server to communicate between the server and plugin processes.
type Plugin struct {
	plugin.MattermostPlugin

	kvstore     kvstore.KVStore
	kvstoreLock sync.RWMutex
	client      *pluginapi.Client

	commandClient command.Command

	router     *mux.Router
	botID      string
	pollerStop chan struct{}

	configurationLock sync.RWMutex
	configuration     *configuration
}

func (p *Plugin) OnActivate() error {
	p.client = pluginapi.NewClient(p.API, p.Driver)

	if err := p.OnConfigurationChange(); err != nil {
		return err
	}

	botID, err := p.client.Bot.EnsureBot(&model.Bot{
		Username:    "google-meet",
		DisplayName: "Google Meet",
		Description: "Created by the Google Meet plugin.",
	}, pluginapi.ProfileImagePath("assets/profile.png"))
	if err != nil {
		return fmt.Errorf("failed to ensure bot: %w", err)
	}
	p.botID = botID

	if err := p.pluginReadinessError(); err != nil {
		p.API.LogWarn("Plugin configuration is incomplete. Google OAuth remains unavailable until plugin setup is completed.", "error", err.Error())
	}

	p.commandClient = command.NewCommandHandler(p.client, p)

	p.router = p.initRouter()

	return nil
}

func (p *Plugin) OnDeactivate() error {
	p.stopPoller()
	return nil
}

func (p *Plugin) setKVStore(store kvstore.KVStore) {
	p.kvstoreLock.Lock()
	defer p.kvstoreLock.Unlock()

	p.kvstore = store
}

func (p *Plugin) getKVStore() kvstore.KVStore {
	p.kvstoreLock.RLock()
	defer p.kvstoreLock.RUnlock()

	return p.kvstore
}

func (p *Plugin) pluginReadinessError() error {
	if err := p.getConfiguration().IsValid(); err != nil {
		return err
	}

	if p.getSiteURL() == "" {
		return errors.New("mattermost site URL is not configured")
	}

	return nil
}

func (p *Plugin) getOAuthKVStore() (kvstore.KVStore, error) {
	if err := p.pluginReadinessError(); err != nil {
		return nil, err
	}

	store := p.getKVStore()
	if store == nil {
		return nil, errors.New("OAuth storage is not initialized")
	}

	return store, nil
}

func (p *Plugin) getSiteURL() string {
	cfg := p.API.GetConfig()
	if cfg == nil || cfg.ServiceSettings.SiteURL == nil || *cfg.ServiceSettings.SiteURL == "" {
		return ""
	}

	return strings.TrimRight(*cfg.ServiceSettings.SiteURL, "/")
}

func (p *Plugin) updateSettingsHeader() {
	redirectURI := p.getOAuth2CallbackURL()
	if redirectURI == "" {
		redirectURI = "Mattermost Site URL must be configured before the redirect URI can be generated."
	}

	header := fmt.Sprintf(
		"**Setup instructions:**\n"+
			"1. Enable the [Google Meet REST API](https://console.cloud.google.com/apis/library/meet.googleapis.com) for your Google Cloud project.\n"+
			"2. Create an [OAuth 2.0 Client ID](https://console.cloud.google.com/apis/credentials) (Web application type).\n"+
			"3. Add the following as an authorized redirect URI: `%s`\n"+
			"4. Enter the Client ID and Client Secret below.",
		redirectURI,
	)

	setManifestSettingsHeader(header)
}

func (p *Plugin) ExecuteCommand(c *plugin.Context, args *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	response, err := p.commandClient.Handle(args)
	if err != nil {
		return nil, model.NewAppError("ExecuteCommand", "plugin.command.execute_command.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return response, nil
}
