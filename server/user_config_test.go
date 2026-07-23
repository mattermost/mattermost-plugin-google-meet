// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"net/http"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type preferenceAPI struct {
	plugin.API
	userID      string
	preferences []model.Preference
	err         *model.AppError
}

func (a *preferenceAPI) UpdatePreferencesForUser(userID string, preferences []model.Preference) *model.AppError {
	a.userID = userID
	a.preferences = preferences
	return a.err
}

func TestSetAuthUser(t *testing.T) {
	api := &preferenceAPI{}
	p := &Plugin{}
	p.API = api

	err := p.SetAuthUser("user1", 1)
	require.NoError(t, err)
	assert.Equal(t, "user1", api.userID)
	require.Len(t, api.preferences, 1)
	assert.Equal(t, model.Preference{
		UserId:   "user1",
		Category: "pp_com.mattermost.google-meet",
		Name:     "authuser",
		Value:    "1",
	}, api.preferences[0])
}

func TestSetAuthUserError(t *testing.T) {
	api := &preferenceAPI{
		err: model.NewAppError("UpdatePreferencesForUser", "test.error", nil, "", http.StatusInternalServerError),
	}
	p := &Plugin{}
	p.API = api

	err := p.SetAuthUser("user1", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update authuser preference")
}
