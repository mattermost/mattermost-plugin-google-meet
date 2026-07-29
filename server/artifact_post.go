// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"bytes"
	"fmt"
	"math"
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

	artifactLabelRecording  = "Recording link"
	artifactLabelTranscript = "Transcript link"
	artifactLabelSmartNote  = "Smart notes link"
)

type meetingArtifactLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

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

// markMeetingEnded edits the meeting post to set "meeting_ended": true so the webapp
// shows a summary instead of the Join button and meeting link.
func (p *Plugin) markMeetingEnded(postID string, endTime *time.Time) error {
	post, appErr := p.API.GetPost(postID)
	if appErr != nil {
		return fmt.Errorf("failed to get post %s: %w", postID, appErr)
	}
	if post.Props == nil {
		post.Props = model.StringInterface{}
	}
	post.Props["meeting_ended"] = true
	if endTime != nil {
		post.Props["meeting_end_time"] = endTime.UnixMilli()
	}
	// Remove the join URL so clients (including mobile attachment fallback) cannot
	// accidentally open an ended meeting.
	delete(post.Props, "meeting_link")

	links := artifactLinksFromProps(post)
	post.Props["attachments"] = []*model.SlackAttachment{
		buildMeetingSummaryAttachment(post, endTime, links),
	}
	post.Message = "The meeting has ended."
	if _, appErr = p.API.UpdatePost(post); appErr != nil {
		return fmt.Errorf("failed to update post %s: %w", postID, appErr)
	}
	return nil
}

// appendMeetingArtifactLink stores an artifact link on the meeting post and rebuilds the
// native summary attachment when the meeting has already ended.
func (p *Plugin) appendMeetingArtifactLink(meetingPostID, label, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil
	}

	post, appErr := p.API.GetPost(meetingPostID)
	if appErr != nil {
		return fmt.Errorf("failed to get post %s: %w", meetingPostID, appErr)
	}
	if post.Props == nil {
		post.Props = model.StringInterface{}
	}

	links := appendArtifactLink(artifactLinksFromProps(post), label, url)
	setArtifactLinksOnPost(post, links)

	if propAsBool(post.Props["meeting_ended"]) {
		endTime := meetingEndTimeFromPost(post)
		post.Props["attachments"] = []*model.SlackAttachment{
			buildMeetingSummaryAttachment(post, endTime, links),
		}
		post.Message = "The meeting has ended."
	}

	if _, appErr = p.API.UpdatePost(post); appErr != nil {
		return fmt.Errorf("failed to update post %s: %w", meetingPostID, appErr)
	}
	return nil
}

func buildMeetingSummaryAttachment(post *model.Post, endTime *time.Time, links []meetingArtifactLink) *model.SlackAttachment {
	text, fallback := buildMeetingSummaryBody(post, endTime, links)
	return &model.SlackAttachment{
		Title:    meetingAttachmentTitle(post),
		Text:     text,
		Fallback: fallback,
	}
}

func buildMeetingSummaryBody(post *model.Post, endTime *time.Time, links []meetingArtifactLink) (string, string) {
	startMs := post.CreateAt
	endMs := meetingEndMillis(post, endTime)

	var textBody, fallbackBody strings.Builder
	writeMeetingSummaryHeader := func(body *strings.Builder) {
		fmt.Fprintln(body, "The meeting has ended.")
		fmt.Fprintln(body)
		fmt.Fprintln(body, "Meeting Summary")
		fmt.Fprintf(body, "Date: %s\n", formatMeetingDate(time.UnixMilli(startMs)))
		fmt.Fprintf(body, "Meeting Length: %s", formatMeetingDuration(startMs, endMs))
	}
	writeMeetingSummaryHeader(&textBody)
	writeMeetingSummaryHeader(&fallbackBody)

	if len(links) > 0 {
		fmt.Fprintln(&textBody)
		fmt.Fprintln(&textBody)
		fmt.Fprintln(&fallbackBody)
		fmt.Fprintln(&fallbackBody)
		for _, link := range links {
			fmt.Fprintf(&textBody, "- [%s](%s)\n", link.Label, link.URL)
			fmt.Fprintf(&fallbackBody, "- %s: %s\n", link.Label, link.URL)
		}
	}

	return strings.TrimSpace(textBody.String()), strings.TrimSpace(fallbackBody.String())
}

func formatMeetingDate(t time.Time) string {
	return t.Format("Mon, Jan 2, 2006, 3:04 PM")
}

func formatMeetingDuration(startMs, endMs int64) string {
	durationMs := endMs - startMs
	if durationMs <= 0 {
		return "0 minute(s)"
	}
	minutes := int(math.Ceil(float64(durationMs) / float64(time.Minute/time.Millisecond)))
	return fmt.Sprintf("%d minute(s)", minutes)
}

func meetingEndMillis(post *model.Post, endTime *time.Time) int64 {
	if endTime != nil {
		return endTime.UnixMilli()
	}
	if stored := meetingEndTimeFromPost(post); stored != nil {
		return stored.UnixMilli()
	}
	return time.Now().UnixMilli()
}

func meetingEndTimeFromPost(post *model.Post) *time.Time {
	if post == nil || post.Props == nil {
		return nil
	}
	switch v := post.Props["meeting_end_time"].(type) {
	case float64:
		t := time.UnixMilli(int64(v))
		return &t
	case int64:
		t := time.UnixMilli(v)
		return &t
	case int:
		t := time.UnixMilli(int64(v))
		return &t
	default:
		return nil
	}
}

func artifactLinksFromProps(post *model.Post) []meetingArtifactLink {
	if post == nil || post.Props == nil {
		return nil
	}
	raw, ok := post.Props["artifact_links"]
	if !ok {
		return nil
	}
	return parseArtifactLinks(raw)
}

func parseArtifactLinks(raw any) []meetingArtifactLink {
	switch items := raw.(type) {
	case []meetingArtifactLink:
		return items
	case []any:
		links := make([]meetingArtifactLink, 0, len(items))
		for _, item := range items {
			if link, ok := artifactLinkFromMap(item); ok {
				links = append(links, link)
			}
		}
		return links
	default:
		return nil
	}
}

func artifactLinkFromMap(raw any) (meetingArtifactLink, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return meetingArtifactLink{}, false
	}
	label, _ := m["label"].(string)
	url, _ := m["url"].(string)
	url = strings.TrimSpace(url)
	if url == "" {
		return meetingArtifactLink{}, false
	}
	return meetingArtifactLink{Label: label, URL: url}, true
}

func appendArtifactLink(links []meetingArtifactLink, label, url string) []meetingArtifactLink {
	url = strings.TrimSpace(url)
	label = strings.TrimSpace(label)
	if url == "" {
		return links
	}
	for _, link := range links {
		if link.URL == url && link.Label == label {
			return links
		}
	}
	return append(links, meetingArtifactLink{Label: label, URL: url})
}

func propAsBool(val any) bool {
	switch v := val.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func setArtifactLinksOnPost(post *model.Post, links []meetingArtifactLink) {
	serialized := make([]any, len(links))
	for i, link := range links {
		serialized[i] = map[string]any{
			"label": link.Label,
			"url":   link.URL,
		}
	}
	post.Props["artifact_links"] = serialized
}

func artifactLinkAttachment(message, linkLabel, url string) *model.SlackAttachment {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil
	}
	text := fmt.Sprintf("%s\n\n[%s](%s)", message, linkLabel, url)
	fallback := fmt.Sprintf("%s\n\n%s: %s", message, linkLabel, url)
	return &model.SlackAttachment{
		Fallback: fallback,
		Text:     text,
	}
}

func meetingAttachmentTitle(post *model.Post) string {
	if topic, ok := post.Props["meeting_topic"].(string); ok && strings.TrimSpace(topic) != "" {
		return topic
	}
	if desc, ok := post.Props["description"].(string); ok && strings.TrimSpace(desc) != "" {
		return desc
	}
	if attachments, ok := post.Props["attachments"].([]*model.SlackAttachment); ok {
		for _, a := range attachments {
			if a != nil && strings.TrimSpace(a.Title) != "" {
				return a.Title
			}
		}
	}
	// After GetPost, attachments are often []any of map[string]any.
	if raw, ok := post.Props["attachments"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				if title, ok := m["title"].(string); ok && strings.TrimSpace(title) != "" {
					return title
				}
			}
		}
	}
	return "Google Meet"
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
	if attachment := artifactLinkAttachment(message, "View recording in Google Drive", exportURI); attachment != nil {
		post.Props["attachments"] = []*model.SlackAttachment{attachment}
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
	exportURI := ""
	if tr.DocsDestination != nil {
		exportURI = tr.DocsDestination.ExportURI
		post.AddProp("export_uri", exportURI)
	}
	if attachment := artifactLinkAttachment(message, "View transcript in Google Docs", exportURI); attachment != nil {
		post.AddProp("attachments", []*model.SlackAttachment{attachment})
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
	if attachment := artifactLinkAttachment(message, "View smart notes in Google Docs", exportURI); attachment != nil {
		post.Props["attachments"] = []*model.SlackAttachment{attachment}
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
