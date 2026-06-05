// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

const (
	// Custom post types for conference artifacts.
	postTypeConference = "custom_gmeet_conference"
	postTypeRecording  = "custom_gmeet_recording"
	postTypeTranscript = "custom_gmeet_transcript"
	postTypeSmartNote  = "custom_gmeet_smartnote"
)

// postConferenceStarted creates a top-level post in the channel announcing the new conference
// and returns the created post ID and channel ID.
func (p *Plugin) postConferenceStarted(sub *kvstore.Subscription, record *conferenceRecord) (string, error) {
	if p.botID == "" {
		return "", fmt.Errorf("bot is not initialised yet")
	}
	startedAt := time.Now()
	if record.StartTime != nil {
		startedAt = *record.StartTime
	}

	message := "A new Google Meet conference has started."

	post := &model.Post{
		UserId:    p.botID,
		ChannelId: sub.ChannelID,
		Message:   message,
		Type:      postTypeConference,
		Props: model.StringInterface{
			"meeting_code":      sub.MeetingCode,
			"space_id":          sub.SpaceID,
			"description":       sub.Description,
			"conference_record": record.Name,
			"conference_start":  startedAt.UTC().Format(time.RFC3339),
		},
	}

	created, appErr := p.API.CreatePost(post)
	if appErr != nil {
		return "", fmt.Errorf("failed to create conference post: %w", appErr)
	}
	return created.Id, nil
}

// postRecording creates a reply post in the thread for a recording artifact.
// The recording is linked rather than downloaded so Google Drive's own ACLs continue
// to gate who can view it, independent of channel membership.
func (p *Plugin) postRecording(channelID, rootPostID string, rec *meetRecording) error {
	exportURI := ""
	if rec.DriveDestination != nil {
		exportURI = rec.DriveDestination.ExportURI
	}

	// Keep the message plain text; the component renders the link from export_uri prop.
	message := "A recording is now available."

	post := &model.Post{
		UserId:    p.botID,
		ChannelId: channelID,
		RootId:    rootPostID,
		Message:   message,
		Type:      postTypeRecording,
		Props: model.StringInterface{
			"recording_name": rec.Name,
			"export_uri":     exportURI,
		},
	}

	_, appErr := p.API.CreatePost(post)
	if appErr != nil {
		return fmt.Errorf("failed to create recording post: %w", appErr)
	}
	return nil
}

// postTranscript creates a reply post for a transcript, uploading a .txt file built from entries.
func (p *Plugin) postTranscript(token *kvstore.OAuth2Token, channelID, rootPostID string, tr *meetTranscript) error {
	entries, err := p.listTranscriptEntries(token, tr.Name)
	if err != nil {
		p.API.LogWarn("Failed to list transcript entries; posting link only", "transcript", tr.Name, "error", err.Error())
		entries = nil
	}

	var fileIDs []string
	if len(entries) > 0 {
		content := buildTranscriptText(entries)
		info, appErr := p.API.UploadFile([]byte(content), channelID, "transcript.vtt")
		if appErr != nil {
			p.API.LogWarn("Failed to upload transcript file", "transcript", tr.Name, "error", appErr.Error())
		} else {
			fileIDs = []string{info.Id}
		}
	}

	// Keep the message plain text; the component renders the link from export_uri prop.
	message := "A transcript is now available."

	post := &model.Post{
		UserId:    p.botID,
		ChannelId: channelID,
		RootId:    rootPostID,
		Message:   message,
		Type:      postTypeTranscript,
		FileIds:   fileIDs,
	}
	if len(fileIDs) > 0 {
		// Match the captions prop shape the mattermost-ai plugin expects.
		post.AddProp("captions", []any{map[string]any{"file_id": fileIDs[0]}})
	}
	if tr.DocsDestination != nil {
		post.AddProp("export_uri", tr.DocsDestination.ExportURI)
	}

	_, appErr := p.API.CreatePost(post)
	if appErr != nil {
		return fmt.Errorf("failed to create transcript post: %w", appErr)
	}
	return nil
}

// postSmartNote creates a reply post for a smart note artifact.
func (p *Plugin) postSmartNote(channelID, rootPostID string, sn *meetSmartNote) error {
	exportURI := ""
	if sn.DocsDestination != nil {
		exportURI = sn.DocsDestination.ExportURI
	}

	// Keep the message plain text; the component renders the link from export_uri prop.
	message := "Smart notes are now available."

	post := &model.Post{
		UserId:    p.botID,
		ChannelId: channelID,
		RootId:    rootPostID,
		Message:   message,
		Type:      postTypeSmartNote,
		Props: model.StringInterface{
			"smart_note_name": sn.Name,
			"export_uri":      exportURI,
		},
	}

	_, appErr := p.API.CreatePost(post)
	if appErr != nil {
		return fmt.Errorf("failed to create smart note post: %w", appErr)
	}
	return nil
}

// buildTranscriptText renders transcript entries as a WebVTT file.
// The mattermost-ai plugin parses transcript attachments with astisub.ReadFromWebVTT,
// so the output must be valid WebVTT.
func buildTranscriptText(entries []transcriptEntry) string {
	var buf bytes.Buffer
	fmt.Fprintln(&buf, "WEBVTT")
	fmt.Fprintln(&buf)
	for _, e := range entries {
		text := strings.TrimSpace(e.Text)
		if text == "" {
			continue
		}
		endTime := e.EndTime
		if endTime.IsZero() || !endTime.After(e.StartTime) {
			endTime = e.StartTime.Add(3 * time.Second)
		}
		fmt.Fprintf(&buf, "%s --> %s\n", vttTimestamp(e.StartTime), vttTimestamp(endTime))
		speaker := e.ParticipantDevice.DisplayName
		if speaker != "" {
			fmt.Fprintf(&buf, "%s: %s\n\n", speaker, text)
		} else {
			fmt.Fprintf(&buf, "%s\n\n", text)
		}
	}
	return buf.String()
}

// vttTimestamp formats a time.Time as a WebVTT timestamp (HH:MM:SS.mmm).
func vttTimestamp(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%02d:%02d:%02d.%03d", t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1_000_000)
}
