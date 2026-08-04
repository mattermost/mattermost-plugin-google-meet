// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"fmt"
	"strconv"

	"github.com/mattermost/mattermost/server/public/model"
)

const authUserPreferenceName = "authuser"

func authUserPreferenceCategory() string {
	return "pp_" + manifestID()
}

func (p *Plugin) SetAuthUser(userID string, authUser int) error {
	preference := model.Preference{
		UserId:   userID,
		Category: authUserPreferenceCategory(),
		Name:     authUserPreferenceName,
		Value:    strconv.Itoa(authUser),
	}

	if appErr := p.API.UpdatePreferencesForUser(userID, []model.Preference{preference}); appErr != nil {
		return fmt.Errorf("failed to update authuser preference: %w", appErr)
	}

	return nil
}
