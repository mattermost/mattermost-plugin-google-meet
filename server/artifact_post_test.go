// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

func TestMarkMeetingEndedRemovesJoinLink(t *testing.T) {
	meetURL := "https://meet.google.com/abc-defg-hij"
	endTime := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	startTime := endTime.Add(-30 * time.Minute)

	api := &mockPluginAPI{
		post: &model.Post{
			Id:       "post1",
			CreateAt: startTime.UnixMilli(),
			Message:  "I have started a meeting",
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
	assert.Contains(t, attachments[0].Text, "The meeting has ended.")
	assert.Contains(t, attachments[0].Text, "Meeting Summary")
	assert.Contains(t, attachments[0].Text, "Date:")
	assert.Contains(t, attachments[0].Text, "Meeting Length: 30 minute(s)")
	assert.NotContains(t, attachments[0].Text, meetURL)
}

func TestMarkMeetingEndedWithoutAttachments(t *testing.T) {
	endTime := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	startTime := endTime.Add(-45 * time.Minute)

	api := &mockPluginAPI{
		post: &model.Post{
			Id:       "post2",
			CreateAt: startTime.UnixMilli(),
			Message:  "A new Google Meet conference has started.",
			Props: model.StringInterface{
				"meeting_code": "abc-defg-hij",
				"description":  "Weekly sync",
			},
		},
	}
	p := &Plugin{}
	p.SetAPI(api)

	err := p.markMeetingEnded("post2", &endTime)
	require.NoError(t, err)

	assert.Equal(t, true, api.post.Props["meeting_ended"])
	attachments, ok := api.post.Props["attachments"].([]*model.SlackAttachment)
	require.True(t, ok, "summary attachment should always be created")
	require.Len(t, attachments, 1)
	assert.Equal(t, "Weekly sync", attachments[0].Title)
	assert.Contains(t, attachments[0].Text, "Meeting Summary")
	assert.Contains(t, attachments[0].Text, "Meeting Length: 45 minute(s)")
}

func TestMarkMeetingEndedIncludesExistingArtifactLinks(t *testing.T) {
	endTime := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	recordingURL := "https://drive.google.com/file/d/rec1"

	api := &mockPluginAPI{
		post: &model.Post{
			Id:       "post3",
			CreateAt: endTime.Add(-10 * time.Minute).UnixMilli(),
			Props: model.StringInterface{
				"meeting_topic": "Demo",
				"artifact_links": []any{
					map[string]any{"label": artifactLabelRecording, "url": recordingURL},
				},
			},
		},
	}
	p := &Plugin{}
	p.SetAPI(api)

	err := p.markMeetingEnded("post3", &endTime)
	require.NoError(t, err)

	attachments, ok := api.post.Props["attachments"].([]*model.SlackAttachment)
	require.True(t, ok)
	require.Len(t, attachments, 1)
	assert.Contains(t, attachments[0].Text, "- [Recording link]("+recordingURL+")")
	assert.Contains(t, attachments[0].Fallback, "- Recording link: "+recordingURL)
}

func TestAppendMeetingArtifactLinkUpdatesEndedMeeting(t *testing.T) {
	endTime := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	recordingURL := "https://drive.google.com/file/d/rec1"

	api := &mockPluginAPI{
		post: &model.Post{
			Id:       "post4",
			CreateAt: endTime.Add(-20 * time.Minute).UnixMilli(),
			Props: model.StringInterface{
				"meeting_topic":    "Demo",
				"meeting_ended":    true,
				"meeting_end_time": endTime.UnixMilli(),
				"attachments": []*model.SlackAttachment{{
					Title:    "Demo",
					Text:     "The meeting has ended.",
					Fallback: "The meeting has ended.",
				}},
			},
		},
	}
	p := &Plugin{}
	p.SetAPI(api)

	err := p.appendMeetingArtifactLink("post4", artifactLabelRecording, recordingURL)
	require.NoError(t, err)

	links, ok := api.post.Props["artifact_links"].([]any)
	require.True(t, ok)
	require.Len(t, links, 1)

	attachments, ok := api.post.Props["attachments"].([]*model.SlackAttachment)
	require.True(t, ok)
	require.Len(t, attachments, 1)
	assert.Contains(t, attachments[0].Text, "- [Recording link]("+recordingURL+")")
	assert.Contains(t, attachments[0].Fallback, "- Recording link: "+recordingURL)
}

func TestAppendMeetingArtifactLinkDedupesByURL(t *testing.T) {
	recordingURL := "https://drive.google.com/file/d/rec1"

	api := &mockPluginAPI{
		post: &model.Post{
			Id: "post5",
			Props: model.StringInterface{
				"meeting_ended": true,
				"artifact_links": []any{
					map[string]any{"label": artifactLabelRecording, "url": recordingURL},
				},
			},
		},
	}
	p := &Plugin{}
	p.SetAPI(api)

	err := p.appendMeetingArtifactLink("post5", artifactLabelRecording, recordingURL)
	require.NoError(t, err)

	links, ok := api.post.Props["artifact_links"].([]any)
	require.True(t, ok)
	assert.Len(t, links, 1)
}

func TestAppendMeetingArtifactLinkSameURLDifferentLabels(t *testing.T) {
	docURL := "https://docs.google.com/document/d/shared-doc"

	api := &mockPluginAPI{
		post: &model.Post{
			Id:       "post7",
			CreateAt: time.Now().Add(-10 * time.Minute).UnixMilli(),
			Props: model.StringInterface{
				"meeting_ended":    true,
				"meeting_end_time": time.Now().UnixMilli(),
				"artifact_links": []any{
					map[string]any{"label": artifactLabelTranscript, "url": docURL},
				},
			},
		},
	}
	p := &Plugin{}
	p.SetAPI(api)

	err := p.appendMeetingArtifactLink("post7", artifactLabelSmartNote, docURL)
	require.NoError(t, err)

	links, ok := api.post.Props["artifact_links"].([]any)
	require.True(t, ok)
	require.Len(t, links, 2, "same URL with different labels should both be kept")

	attachments, ok := api.post.Props["attachments"].([]*model.SlackAttachment)
	require.True(t, ok)
	require.Len(t, attachments, 1)
	assert.Contains(t, attachments[0].Text, "- [Transcript link]("+docURL+")")
	assert.Contains(t, attachments[0].Text, "- [Smart notes link]("+docURL+")")
}

func TestAppendMeetingArtifactLinkBeforeMeetingEnded(t *testing.T) {
	meetURL := "https://meet.google.com/abc-defg-hij"
	recordingURL := "https://drive.google.com/file/d/rec1"

	api := &mockPluginAPI{
		post: &model.Post{
			Id: "post6",
			Props: model.StringInterface{
				"meeting_link": meetURL,
				"attachments": []*model.SlackAttachment{{
					Title: "Standup",
					Text:  "Meeting URL: " + meetURL,
				}},
			},
		},
	}
	p := &Plugin{}
	p.SetAPI(api)

	err := p.appendMeetingArtifactLink("post6", artifactLabelRecording, recordingURL)
	require.NoError(t, err)

	links, ok := api.post.Props["artifact_links"].([]any)
	require.True(t, ok)
	require.Len(t, links, 1)

	attachments, ok := api.post.Props["attachments"].([]*model.SlackAttachment)
	require.True(t, ok)
	require.Len(t, attachments, 1)
	assert.Contains(t, attachments[0].Text, meetURL, "active meeting attachment should remain unchanged")
}

func TestPostRecordingIncludesAttachmentFallback(t *testing.T) {
	recordingURL := "https://drive.google.com/file/d/rec1"
	api := &mockPluginAPI{}
	p := &Plugin{botID: "bot1"}
	p.SetAPI(api)

	err := p.postRecording("chan1", "root1", &meetRecording{
		Name:  "recordings/rec1",
		State: meetStateFileGenerated,
		DriveDestination: &driveDestination{
			ExportURI: recordingURL,
		},
	})
	require.NoError(t, err)

	require.NotNil(t, api.post)
	attachments, ok := api.post.Props["attachments"].([]*model.SlackAttachment)
	require.True(t, ok)
	require.Len(t, attachments, 1)
	assert.Contains(t, attachments[0].Text, recordingURL)
	assert.Contains(t, attachments[0].Fallback, recordingURL)
	assert.Contains(t, attachments[0].Fallback, "View recording in Google Drive")
}

func TestPostSmartNoteIncludesAttachmentFallback(t *testing.T) {
	docURL := "https://docs.google.com/document/d/note1"
	api := &mockPluginAPI{}
	p := &Plugin{botID: "bot1"}
	p.SetAPI(api)

	err := p.postSmartNote("chan1", "root1", &meetSmartNote{
		Name:  "smartNotes/note1",
		State: meetStateFileGenerated,
		DocsDestination: &docsDestination{
			ExportURI: docURL,
		},
	})
	require.NoError(t, err)

	attachments, ok := api.post.Props["attachments"].([]*model.SlackAttachment)
	require.True(t, ok)
	require.Len(t, attachments, 1)
	assert.Contains(t, attachments[0].Fallback, docURL)
}

func TestPostTranscriptSkipsLinkAttachmentWithoutExportURI(t *testing.T) {
	api := &mockPluginAPI{}
	p := &Plugin{botID: "bot1"}
	p.SetAPI(api)

	err := p.postTranscript(&kvstore.OAuth2Token{}, "chan1", "root1", &meetTranscript{
		Name:  "transcripts/tr1",
		State: meetStateFileGenerated,
	})
	require.NoError(t, err)

	_, hasAttachments := api.post.Props["attachments"]
	assert.False(t, hasAttachments, "transcript without export URI should not add link attachment")
}

func TestPostTranscriptIncludesAttachmentFallback(t *testing.T) {
	docURL := "https://docs.google.com/document/d/tr1"
	api := &mockPluginAPI{}
	p := &Plugin{botID: "bot1"}
	p.SetAPI(api)

	err := p.postTranscript(&kvstore.OAuth2Token{}, "chan1", "root1", &meetTranscript{
		Name:  "transcripts/tr1",
		State: meetStateFileGenerated,
		DocsDestination: &docsDestination{
			ExportURI: docURL,
		},
	})
	require.NoError(t, err)

	attachments, ok := api.post.Props["attachments"].([]*model.SlackAttachment)
	require.True(t, ok)
	require.Len(t, attachments, 1)
	assert.Contains(t, attachments[0].Fallback, docURL)
}
