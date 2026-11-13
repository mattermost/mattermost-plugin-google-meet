// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"net/http"
	"sync"
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

	err := p.postTranscript(&kvstore.OAuth2Token{}, p.newParticipantNames(&kvstore.OAuth2Token{}), "chan1", "root1", &meetTranscript{
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

	err := p.postTranscript(&kvstore.OAuth2Token{}, p.newParticipantNames(&kvstore.OAuth2Token{}), "chan1", "root1", &meetTranscript{
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

func TestPollConferenceArtifactsNamesTranscriptSpeakersFetchingEachParticipantOnce(t *testing.T) {
	// Two transcripts of one conference. Alice and Bob speak in both, the lookup of the phone
	// participant fails, one entry has no participant and carol's only entry is blank.
	lists := map[string]string{
		"/v2/conferenceRecords/rec1/recordings": `{}`,
		"/v2/conferenceRecords/rec1/smartNotes": `{}`,
		"/v2/conferenceRecords/rec1/transcripts": `{"transcripts": [
			{"name": "conferenceRecords/rec1/transcripts/t1", "state": "FILE_GENERATED"},
			{"name": "conferenceRecords/rec1/transcripts/t2", "state": "FILE_GENERATED"}
		]}`,
		"/v2/conferenceRecords/rec1/transcripts/t1/entries": `{"transcriptEntries": [
			{"name": "conferenceRecords/rec1/transcripts/t1/entries/1", "participant": "conferenceRecords/rec1/participants/alice", "text": "Welcome, everyone.", "startTime": "2026-01-15T09:30:00Z", "endTime": "2026-01-15T09:30:04Z"},
			{"name": "conferenceRecords/rec1/transcripts/t1/entries/2", "participant": "conferenceRecords/rec1/participants/bob", "text": "Thanks.", "startTime": "2026-01-15T09:30:05Z", "endTime": "2026-01-15T09:30:09Z"},
			{"name": "conferenceRecords/rec1/transcripts/t1/entries/3", "participant": "conferenceRecords/rec1/participants/phone", "text": "Can you hear me?", "startTime": "2026-01-15T09:30:10Z", "endTime": "2026-01-15T09:30:14Z"},
			{"name": "conferenceRecords/rec1/transcripts/t1/entries/4", "participant": "conferenceRecords/rec1/participants/carol", "text": " ", "startTime": "2026-01-15T09:30:15Z", "endTime": "2026-01-15T09:30:16Z"},
			{"name": "conferenceRecords/rec1/transcripts/t1/entries/5", "text": "Who said that?", "startTime": "2026-01-15T09:30:17Z", "endTime": "2026-01-15T09:30:19Z"},
			{"name": "conferenceRecords/rec1/transcripts/t1/entries/6", "participant": "conferenceRecords/rec1/participants/alice", "text": "Yes.", "startTime": "2026-01-15T09:30:20Z", "endTime": "2026-01-15T09:30:24Z"}
		]}`,
		"/v2/conferenceRecords/rec1/transcripts/t2/entries": `{"transcriptEntries": [
			{"name": "conferenceRecords/rec1/transcripts/t2/entries/1", "participant": "conferenceRecords/rec1/participants/bob", "text": "Round two.", "startTime": "2026-01-15T10:00:00Z", "endTime": "2026-01-15T10:00:04Z"},
			{"name": "conferenceRecords/rec1/transcripts/t2/entries/2", "participant": "conferenceRecords/rec1/participants/phone", "text": "Still here.", "startTime": "2026-01-15T10:00:05Z", "endTime": "2026-01-15T10:00:09Z"}
		]}`,
	}
	participants := map[string]string{
		"/v2/conferenceRecords/rec1/participants/alice": `{"name": "conferenceRecords/rec1/participants/alice", "signedinUser": {"user": "users/1", "displayName": "Alice Example"}}`,
		"/v2/conferenceRecords/rec1/participants/bob":   `{"name": "conferenceRecords/rec1/participants/bob", "anonymousUser": {"displayName": "Bob Guest"}}`,
	}
	// Every request other than the artifact lists is counted, so a repeated or stray lookup shows up.
	var mu sync.Mutex
	lookups := map[string]int{}
	withMeetServer(t, func(w http.ResponseWriter, r *http.Request) {
		if body, ok := lists[r.URL.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		mu.Lock()
		lookups[r.URL.Path]++
		mu.Unlock()
		if body, ok := participants[r.URL.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"code": 500, "message": "backend error", "status": "INTERNAL"}}`))
	})

	api := &mockPluginAPI{captureAllPosts: true}
	kv := newMockKVStore()
	require.NoError(t, kv.StoreConferencePostState("conferenceRecords/rec1", &kvstore.ConferencePostState{
		MeetingPostID: "root1",
		ThreadRootID:  "root1",
		ChannelID:     "chan1",
	}))
	p := pollTestPlugin(t, api, kv)

	p.pollConferenceArtifacts(kv, newTestToken(), "conferenceRecords/rec1")

	require.Len(t, api.uploads, 2)
	assert.Equal(t, "WEBVTT\n\n"+
		"09:30:00.000 --> 09:30:04.000\nAlice Example: Welcome, everyone.\n\n"+
		"09:30:05.000 --> 09:30:09.000\nBob Guest: Thanks.\n\n"+
		"09:30:10.000 --> 09:30:14.000\nCan you hear me?\n\n"+
		"09:30:17.000 --> 09:30:19.000\nWho said that?\n\n"+
		"09:30:20.000 --> 09:30:24.000\nAlice Example: Yes.\n\n", string(api.uploads[0]))
	assert.Equal(t, "WEBVTT\n\n"+
		"10:00:00.000 --> 10:00:04.000\nBob Guest: Round two.\n\n"+
		"10:00:05.000 --> 10:00:09.000\nStill here.\n\n", string(api.uploads[1]))

	// Each transcript is still posted in the thread, with its file as captions.
	require.Len(t, api.allPosts, 2)
	for _, post := range api.allPosts {
		assert.Equal(t, postTypeTranscript, post.Type)
		assert.Equal(t, "root1", post.RootId)
		require.Len(t, post.FileIds, 1)
		assert.Equal(t, []any{map[string]any{"file_id": post.FileIds[0]}}, post.GetProp("captions"))
	}

	// One lookup per speaking participant across both transcripts, including the one that failed;
	// none for the entry without a participant or for carol's blank entry.
	assert.Equal(t, map[string]int{
		"/v2/conferenceRecords/rec1/participants/alice": 1,
		"/v2/conferenceRecords/rec1/participants/bob":   1,
		"/v2/conferenceRecords/rec1/participants/phone": 1,
	}, lookups)
}
