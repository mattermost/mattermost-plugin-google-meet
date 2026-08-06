// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

// ErrInsufficientScopes indicates the token lacks the required OAuth scope.
var ErrInsufficientScopes = errors.New("insufficient authentication scopes")

// ErrMissingMandatoryScopes indicates the user declined, via Google's granular consent
// screen, one or more scopes the plugin cannot function without.
var ErrMissingMandatoryScopes = errors.New("missing mandatory oauth scopes")

const (
	googleAuthURL      = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenRefreshBuffer = 5 * time.Minute

	// meetScopeCreated grants creating meeting spaces; sufficient for /meet start.
	meetScopeCreated = "https://www.googleapis.com/auth/meetings.space.created"
	// meetScopeReadonly grants reading conferenceRecords, recordings, transcripts, and smart notes.
	// Only requested when conference artifact polling is enabled.
	meetScopeReadonly = "https://www.googleapis.com/auth/meetings.space.readonly"
	// calendarScopeEventsReadonly grants reading calendar events so the poller can anchor
	// conference-started posts to the organizer's scheduled start time. Only requested when
	// calendar schedule sync is enabled.
	calendarScopeEventsReadonly = "https://www.googleapis.com/auth/calendar.events.readonly"
)

// These are vars so tests can override them with httptest servers.
var (
	// #nosec G101 -- OAuth endpoint URLs are public constants, not credentials.
	googleTokenURL = "https://oauth2.googleapis.com/token"
	googleMeetURL  = "https://meet.googleapis.com/v2"
	httpClient     = &http.Client{Timeout: 30 * time.Second}
)

func (p *Plugin) getOAuth2CallbackURL() string {
	siteURL := p.getSiteURL()
	if siteURL == "" {
		return ""
	}

	return fmt.Sprintf("%s/plugins/%s/api/v1/oauth/callback", siteURL, manifestID())
}

func (p *Plugin) getOAuth2ConnectURL() string {
	siteURL := p.getSiteURL()
	if siteURL == "" {
		return ""
	}

	return fmt.Sprintf("%s/plugins/%s/api/v1/oauth/connect", siteURL, manifestID())
}

func (p *Plugin) buildAuthURL(state string) string {
	config := p.getConfiguration()
	redirectURI := p.getOAuth2CallbackURL()
	if redirectURI == "" {
		return ""
	}

	scope := meetScopeCreated
	if config.EnableConferenceArtifactPosts {
		scope += " " + meetScopeReadonly
	}
	if config.EnableCalendarScheduleSync {
		scope += " " + calendarScopeEventsReadonly
	}

	params := url.Values{
		"client_id":     {config.GoogleClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {scope},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
		"state":         {state},
	}

	return googleAuthURL + "?" + params.Encode()
}

// mandatoryScopes returns the OAuth scopes the plugin cannot operate without, based on
// current configuration. calendarScopeEventsReadonly is deliberately excluded: Google's
// granular consent screen lets users decline it independently of the others, and the
// poller already falls back to the cooldown heuristic when it's missing.
func mandatoryScopes(config *configuration) []string {
	scopes := []string{meetScopeCreated}
	if config.EnableConferenceArtifactPosts {
		scopes = append(scopes, meetScopeReadonly)
	}
	return scopes
}

// missingScopes returns which of the required scopes are absent from the space-delimited
// granted scope string Google returns in the token response.
func missingScopes(granted string, required []string) []string {
	grantedSet := make(map[string]bool, len(required))
	for _, s := range strings.Fields(granted) {
		grantedSet[s] = true
	}

	var missing []string
	for _, r := range required {
		if !grantedSet[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func (p *Plugin) exchangeCodeForToken(code string) (*kvstore.OAuth2Token, error) {
	config := p.getConfiguration()
	redirectURI := p.getOAuth2CallbackURL()
	if redirectURI == "" {
		return nil, errors.New("mattermost site URL is not configured")
	}

	data := url.Values{
		"code":          {code},
		"client_id":     {config.GoogleClientID},
		"client_secret": {config.GoogleClientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}

	resp, err := httpClient.PostForm(googleTokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code for token: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && p.API != nil {
			p.API.LogWarn("Failed to close response body", "description", "token exchange response", "error", closeErr.Error())
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if missing := missingScopes(tokenResp.Scope, mandatoryScopes(config)); len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrMissingMandatoryScopes, strings.Join(missing, ", "))
	}

	token := &kvstore.OAuth2Token{
		AccessToken:  tokenResp.AccessToken,
		TokenType:    tokenResp.TokenType,
		RefreshToken: tokenResp.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
	}

	return token, nil
}

func (p *Plugin) refreshToken(token *kvstore.OAuth2Token) (*kvstore.OAuth2Token, error) {
	config := p.getConfiguration()

	data := url.Values{
		"client_id":     {config.GoogleClientID},
		"client_secret": {config.GoogleClientSecret},
		"refresh_token": {token.RefreshToken},
		"grant_type":    {"refresh_token"},
	}

	resp, err := httpClient.PostForm(googleTokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && p.API != nil {
			p.API.LogWarn("Failed to close response body", "description", "token refresh response", "error", closeErr.Error())
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}

	newToken := &kvstore.OAuth2Token{
		AccessToken:  tokenResp.AccessToken,
		TokenType:    tokenResp.TokenType,
		RefreshToken: token.RefreshToken, // keep existing refresh token
		Expiry:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
	}

	if tokenResp.RefreshToken != "" {
		newToken.RefreshToken = tokenResp.RefreshToken
	}

	return newToken, nil
}

func (p *Plugin) getValidToken(userID string) (*kvstore.OAuth2Token, error) {
	store, err := p.getOAuthKVStore()
	if err != nil {
		return nil, err
	}

	token, err := store.GetOAuth2Token(userID)
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}

	// Refresh early to avoid edge-of-expiry failures in slower environments.
	if time.Until(token.Expiry) < tokenRefreshBuffer {
		token, err = p.refreshToken(token)
		if err != nil {
			return nil, fmt.Errorf("failed to refresh expired token: %w", err)
		}
		if err := store.StoreOAuth2Token(userID, token); err != nil {
			p.API.LogError("Failed to store refreshed token", "error", err.Error())
		}
	}

	return token, nil
}

// meetSpaceResponse is kept as an alias so existing tests compile.
type meetSpaceResponse = meetSpace

type meetSpace struct {
	Name        string `json:"name"`
	MeetingURI  string `json:"meetingUri"`
	MeetingCode string `json:"meetingCode"`
}

type conferenceRecord struct {
	Name      string     `json:"name"`
	StartTime *time.Time `json:"startTime,omitempty"`
	EndTime   *time.Time `json:"endTime,omitempty"`
	Space     string     `json:"space"` // resource name of the parent space, e.g. "spaces/abc123"
}

type driveDestination struct {
	File      string `json:"file"`
	ExportURI string `json:"exportUri"`
}

type docsDestination struct {
	Document  string `json:"document"`
	ExportURI string `json:"exportUri"`
}

type meetRecording struct {
	Name             string            `json:"name"`
	State            string            `json:"state"`
	StartTime        *time.Time        `json:"startTime,omitempty"`
	EndTime          *time.Time        `json:"endTime,omitempty"`
	DriveDestination *driveDestination `json:"driveDestination,omitempty"`
}

type meetTranscript struct {
	Name            string           `json:"name"`
	State           string           `json:"state"`
	StartTime       *time.Time       `json:"startTime,omitempty"`
	EndTime         *time.Time       `json:"endTime,omitempty"`
	DocsDestination *docsDestination `json:"docsDestination,omitempty"`
}

type meetSmartNote struct {
	Name            string           `json:"name"`
	State           string           `json:"state"`
	StartTime       *time.Time       `json:"startTime,omitempty"`
	EndTime         *time.Time       `json:"endTime,omitempty"`
	DocsDestination *docsDestination `json:"docsDestination,omitempty"`
}

type transcriptEntry struct {
	Name              string `json:"name"`
	ParticipantDevice struct {
		DisplayName string `json:"displayName"`
	} `json:"participantDevice"`
	Text      string    `json:"text"`
	StartTime time.Time `json:"startTime"`
	EndTime   time.Time `json:"endTime"`
}

// meetAPIState enumerates artifact lifecycle states used by the Meet API.
const (
	meetStateFileGenerated = "FILE_GENERATED"
)

// createMeeting creates a new Google Meet space and returns both the meeting URI and the stable space name.
func (p *Plugin) createMeeting(token *kvstore.OAuth2Token, _ string) (meetingURI, spaceName string, err error) {
	reqURL := googleMeetURL + "/spaces"
	req, reqErr := http.NewRequest(http.MethodPost, reqURL, strings.NewReader("{}"))
	if reqErr != nil {
		return "", "", fmt.Errorf("failed to create request: %w", reqErr)
	}

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, doErr := httpClient.Do(req)
	if doErr != nil {
		return "", "", fmt.Errorf("failed to create meeting space: %w", doErr)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && p.API != nil {
			p.API.LogWarn("Failed to close response body", "description", "meeting creation response", "error", closeErr.Error())
		}
	}()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return "", "", fmt.Errorf("failed to read response: %w", readErr)
	}

	if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "ACCESS_TOKEN_SCOPE_INSUFFICIENT") {
		return "", "", ErrInsufficientScopes
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("meet API returned status %d: %s", resp.StatusCode, string(body))
	}

	var space meetSpace
	if err := json.Unmarshal(body, &space); err != nil {
		return "", "", fmt.Errorf("failed to parse Meet API response: %w", err)
	}

	if space.MeetingURI == "" {
		return "", "", errors.New("no meeting URI in Meet API response")
	}

	return space.MeetingURI, space.Name, nil
}

// doGet performs an authenticated GET against the Meet REST API and returns the raw body.
func (p *Plugin) meetGet(token *kvstore.OAuth2Token, path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, googleMeetURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && p.API != nil {
			p.API.LogWarn("Failed to close response body", "path", path, "error", closeErr.Error())
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "ACCESS_TOKEN_SCOPE_INSUFFICIENT") {
		return nil, ErrInsufficientScopes
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("resource not found: %s", path)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meet API status %d for %s: %s", resp.StatusCode, path, string(body))
	}

	return body, nil
}

// getSpace resolves a meeting code or space ID to a Space resource.
func (p *Plugin) getSpace(token *kvstore.OAuth2Token, meetingCodeOrID string) (*meetSpace, error) {
	body, err := p.meetGet(token, "/spaces/"+url.PathEscape(meetingCodeOrID))
	if err != nil {
		return nil, err
	}
	var space meetSpace
	if err := json.Unmarshal(body, &space); err != nil {
		return nil, fmt.Errorf("failed to parse space response: %w", err)
	}
	return &space, nil
}

// getConferenceRecord fetches a single conference record by its resource name (e.g. "conferenceRecords/abc").
func (p *Plugin) getConferenceRecord(token *kvstore.OAuth2Token, name string) (*conferenceRecord, error) {
	body, err := p.meetGet(token, "/"+name)
	if err != nil {
		return nil, err
	}
	var record conferenceRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return nil, fmt.Errorf("failed to parse conference record: %w", err)
	}
	return &record, nil
}

// listConferenceRecords returns conference records for the given space that started strictly after `since`.
// The filter is pushed to the API (start_time>=) to avoid fetching full history, and re-checked
// client-side with strict-after because our watermark semantics require excluding the equal timestamp.
func (p *Plugin) listConferenceRecords(token *kvstore.OAuth2Token, spaceName string, since time.Time) ([]conferenceRecord, error) {
	filterParts := []string{fmt.Sprintf(`space.name="%s"`, spaceName)}
	if !since.IsZero() {
		filterParts = append(filterParts, fmt.Sprintf(`start_time>="%s"`, since.UTC().Format(time.RFC3339Nano)))
	}
	filter := strings.Join(filterParts, " AND ")
	basePath := "/conferenceRecords?" + url.Values{"filter": {filter}}.Encode()
	all, err := p.listConferenceRecordsPaged(token, basePath)
	if err != nil {
		return nil, err
	}
	if since.IsZero() {
		return all, nil
	}
	filtered := all[:0]
	for _, r := range all {
		if r.StartTime != nil && r.StartTime.After(since) {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

func (p *Plugin) listConferenceRecordsPaged(token *kvstore.OAuth2Token, basePath string) ([]conferenceRecord, error) {
	type listResp struct {
		ConferenceRecords []conferenceRecord `json:"conferenceRecords"`
		NextPageToken     string             `json:"nextPageToken"`
	}

	var all []conferenceRecord
	pageToken := ""
	for {
		path := basePath
		if pageToken != "" {
			path += "&pageToken=" + url.QueryEscape(pageToken)
		}
		body, err := p.meetGet(token, path)
		if err != nil {
			return nil, err
		}
		var resp listResp
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse conference records: %w", err)
		}
		all = append(all, resp.ConferenceRecords...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return all, nil
}

// listRecordings returns all recordings for the given conference record that have reached FILE_GENERATED.
func (p *Plugin) listRecordings(token *kvstore.OAuth2Token, conferenceRecordName string) ([]meetRecording, error) {
	type listResp struct {
		Recordings    []meetRecording `json:"recordings"`
		NextPageToken string          `json:"nextPageToken"`
	}

	var all []meetRecording
	pageToken := ""
	for {
		path := "/" + conferenceRecordName + "/recordings"
		if pageToken != "" {
			path += "?pageToken=" + url.QueryEscape(pageToken)
		}
		body, err := p.meetGet(token, path)
		if err != nil {
			return nil, err
		}
		var resp listResp
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse recordings: %w", err)
		}
		all = append(all, resp.Recordings...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return all, nil
}

// listTranscripts returns all transcripts for the given conference record.
func (p *Plugin) listTranscripts(token *kvstore.OAuth2Token, conferenceRecordName string) ([]meetTranscript, error) {
	type listResp struct {
		Transcripts   []meetTranscript `json:"transcripts"`
		NextPageToken string           `json:"nextPageToken"`
	}

	var all []meetTranscript
	pageToken := ""
	for {
		path := "/" + conferenceRecordName + "/transcripts"
		if pageToken != "" {
			path += "?pageToken=" + url.QueryEscape(pageToken)
		}
		body, err := p.meetGet(token, path)
		if err != nil {
			return nil, err
		}
		var resp listResp
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse transcripts: %w", err)
		}
		all = append(all, resp.Transcripts...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return all, nil
}

// listSmartNotes returns all smart notes for the given conference record.
func (p *Plugin) listSmartNotes(token *kvstore.OAuth2Token, conferenceRecordName string) ([]meetSmartNote, error) {
	type listResp struct {
		SmartNotes    []meetSmartNote `json:"smartNotes"`
		NextPageToken string          `json:"nextPageToken"`
	}

	var all []meetSmartNote
	pageToken := ""
	for {
		path := "/" + conferenceRecordName + "/smartNotes"
		if pageToken != "" {
			path += "?pageToken=" + url.QueryEscape(pageToken)
		}
		body, err := p.meetGet(token, path)
		if err != nil {
			return nil, err
		}
		var resp listResp
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse smart notes: %w", err)
		}
		all = append(all, resp.SmartNotes...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return all, nil
}

// listTranscriptEntries returns all transcript entries for the given transcript resource name.
func (p *Plugin) listTranscriptEntries(token *kvstore.OAuth2Token, transcriptName string) ([]transcriptEntry, error) {
	type listResp struct {
		TranscriptEntries []transcriptEntry `json:"transcriptEntries"`
		NextPageToken     string            `json:"nextPageToken"`
	}

	var all []transcriptEntry
	pageToken := ""
	for {
		path := "/" + transcriptName + "/entries"
		if pageToken != "" {
			path += "?pageToken=" + url.QueryEscape(pageToken)
		}
		body, err := p.meetGet(token, path)
		if err != nil {
			return nil, err
		}
		var resp listResp
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse transcript entries: %w", err)
		}
		all = append(all, resp.TranscriptEntries...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return all, nil
}
