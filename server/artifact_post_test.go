// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkMeetingEndedRemovesJoinLink(t *testing.T) {
	meetURL := "https://meet.google.com/abc-defg-hij"
	endTime := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	api := &mockPluginAPI{
		post: &model.Post{
			Id:      "post1",
			Message: "I have started a meeting",
			Props: model.StringInterface{
				"meeting_link":  meetURL,
				"meeting_topic": "Standup",
				"attachments": []*model.SlackAttachment{{
					Fallback: "Meeting started.\n\nMeeting URL: " + meetURL,
					Title:    "Standup",
					Text:     "Meeting URL: " + meetURL,
				}},
			},
		},
	}
	p := &Plugin{}
	p.SetAPI(api)

	err := p.markMeetingEnded("post1", &endTime)
	require.NoError(t, err)

	require.NotNil(t, api.post)
	assert.Equal(t, "The meeting has ended.", api.post.Message)
	assert.Equal(t, true, api.post.Props["meeting_ended"])
	assert.Equal(t, endTime.UnixMilli(), api.post.Props["meeting_end_time"])
	_, hasLink := api.post.Props["meeting_link"]
	assert.False(t, hasLink, "meeting_link should be removed")

	attachments, ok := api.post.Props["attachments"].([]*model.SlackAttachment)
	require.True(t, ok)
	require.Len(t, attachments, 1)
	assert.Equal(t, "Standup", attachments[0].Title)
	assert.Equal(t, "The meeting has ended.", attachments[0].Text)
	assert.Equal(t, "The meeting has ended.", attachments[0].Fallback)
	assert.NotContains(t, attachments[0].Text, meetURL)
	assert.NotContains(t, attachments[0].Fallback, meetURL)
}

func TestMarkMeetingEndedWithoutAttachments(t *testing.T) {
	api := &mockPluginAPI{
		post: &model.Post{
			Id:      "post2",
			Message: "A new Google Meet conference has started.",
			Props: model.StringInterface{
				"meeting_code": "abc-defg-hij",
				"description":  "Weekly sync",
			},
		},
	}
	p := &Plugin{}
	p.SetAPI(api)

	err := p.markMeetingEnded("post2", nil)
	require.NoError(t, err)

	assert.Equal(t, true, api.post.Props["meeting_ended"])
	_, hasAttachments := api.post.Props["attachments"]
	assert.False(t, hasAttachments, "should not invent attachments")
}
