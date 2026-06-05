// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/mattermost/mattermost-plugin-google-meet/server/command"
)

func (p *Plugin) initRouter() *mux.Router {
	router := mux.NewRouter()

	apiRouter := router.PathPrefix("/api/v1").Subrouter()

	// OAuth callback does NOT require Mattermost auth (Google redirects here)
	apiRouter.HandleFunc("/oauth/callback", p.handleOAuthCallback).Methods(http.MethodGet)

	// Protected routes require Mattermost auth
	protectedRouter := apiRouter.PathPrefix("").Subrouter()
	protectedRouter.Use(p.MattermostAuthorizationRequired)
	protectedRouter.HandleFunc("/oauth/connect", p.handleOAuthConnect).Methods(http.MethodGet)
	protectedRouter.HandleFunc("/meeting", p.handleCreateMeeting).Methods(http.MethodPost)
	protectedRouter.HandleFunc("/config/status", p.handleConfigStatus).Methods(http.MethodGet)

	// Admin-only debug routes
	adminRouter := protectedRouter.PathPrefix("/admin").Subrouter()
	adminRouter.Use(p.AdminAuthorizationRequired)
	adminRouter.HandleFunc("/poll-now", p.handlePollNow).Methods(http.MethodPost)

	return router
}

func (p *Plugin) ServeHTTP(c *plugin.Context, w http.ResponseWriter, r *http.Request) {
	p.router.ServeHTTP(w, r)
}

func (p *Plugin) MattermostAuthorizationRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("Mattermost-User-ID")
		if userID == "" {
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (p *Plugin) AdminAuthorizationRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("Mattermost-User-ID")
		isAdmin, err := p.IsUserAdmin(userID)
		if err != nil || !isAdmin {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (p *Plugin) handlePollNow(w http.ResponseWriter, _ *http.Request) {
	if !p.getConfiguration().EnableConferenceArtifactPosts {
		writeJSONResponse(w, http.StatusOK, map[string]string{"status": "feature_disabled"}, p.API)
		return
	}
	go p.runPollCycle()
	writeJSONResponse(w, http.StatusOK, map[string]string{"status": "poll triggered"}, p.API)
}

// handleError logs the internal error and sends a generic 500 JSON response.
func (p *Plugin) handleError(w http.ResponseWriter, internalErr error) {
	p.handleErrorWithCode(w, http.StatusInternalServerError, "An internal error has occurred. Check app server logs for details.", internalErr)
}

// handleErrorWithCode logs the internal error and sends the public facing error
// message as JSON in a response with the provided code.
func (p *Plugin) handleErrorWithCode(w http.ResponseWriter, code int, publicErrorMsg string, internalErr error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	details := ""
	if internalErr != nil {
		details = internalErr.Error()
	}

	p.API.LogError(fmt.Sprintf("public error message: %v; internal details: %v", publicErrorMsg, details))

	responseMsg, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{
		Error: publicErrorMsg,
	})
	_, _ = w.Write(responseMsg)
}

func addConfigureDetails(resp map[string]any, configureURL string) {
	if configureURL != "" {
		resp["configure_url"] = configureURL
		return
	}

	resp["configure_help"] = "Mattermost Site URL must be configured before the System Console link is available."
}

func writeJSONResponse(w http.ResponseWriter, code int, resp any, api plugin.API) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		api.LogError("Failed to encode response", "error", err.Error())
	}
}

func (p *Plugin) sendMeetingEphemeralResponse(w http.ResponseWriter, userID, channelID, message, reason string) {
	postUserID := userID
	if p.botID != "" {
		postUserID = p.botID
	}

	p.API.SendEphemeralPost(userID, &model.Post{
		UserId:    postUserID,
		ChannelId: channelID,
		Message:   message,
		Type:      model.PostTypeEphemeral,
	})

	writeJSONResponse(w, http.StatusOK, map[string]string{
		"status": "handled",
		"reason": reason,
	}, p.API)
}

func (p *Plugin) handleOAuthConnect(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")

	config := p.getConfiguration()
	if err := config.IsValid(); err != nil {
		p.handleError(w, err)
		return
	}

	store, err := p.getOAuthKVStore()
	if err != nil {
		p.handleError(w, err)
		return
	}

	state, err := generateState()
	if err != nil {
		p.handleError(w, fmt.Errorf("failed to generate state: %w", err))
		return
	}

	if err := store.StoreOAuth2State(state, userID); err != nil {
		p.handleError(w, fmt.Errorf("failed to store state: %w", err))
		return
	}

	authURL := p.buildAuthURL(state)
	if authURL == "" {
		p.handleError(w, errors.New("mattermost site URL is not configured"))
		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

func (p *Plugin) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		p.handleErrorWithCode(w, http.StatusBadRequest, "Missing code or state parameter.", nil)
		return
	}

	store, err := p.getOAuthKVStore()
	if err != nil {
		p.handleError(w, err)
		return
	}

	userID, err := store.GetAndDeleteOAuth2State(state)
	if err != nil {
		p.handleErrorWithCode(w, http.StatusBadRequest, "Invalid or expired state. Please try connecting again.", err)
		return
	}

	token, err := p.exchangeCodeForToken(code)
	if err != nil {
		p.handleError(w, fmt.Errorf("failed to exchange code for token: %w", err))
		return
	}

	if err := store.StoreOAuth2Token(userID, token); err != nil {
		p.handleError(w, fmt.Errorf("failed to store token: %w", err))
		return
	}

	html := `
<!DOCTYPE html>
<html>
<head><title>Google Meet - Connected</title></head>
<body>
<h2>Successfully connected to Google!</h2>
<p>You can close this window and return to Mattermost. Use <code>/meet start</code> to start a meeting.</p>
<script>
setTimeout(function() { window.close(); }, 3000);
</script>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html")
	if _, err := w.Write([]byte(html)); err != nil {
		p.API.LogError("Failed to write response", "error", err.Error())
	}
}

type createMeetingRequest struct {
	ChannelID    string `json:"channel_id"`
	Topic        string `json:"topic"`
	ConnectionID string `json:"connection_id"`
}

func (p *Plugin) handleConfigStatus(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")

	isAdmin, err := p.IsUserAdmin(userID)
	if err != nil {
		// Degrade to non-admin so the status endpoint still works for regular users.
		p.API.LogError("Failed to check admin status for config status", "user_id", userID, "error", err.Error())
		isAdmin = false
	}
	configured := p.IsPluginConfigured()

	resp := map[string]any{
		"configured": configured,
		"is_admin":   isAdmin,
	}
	if isAdmin && !configured {
		addConfigureDetails(resp, p.GetPluginConfigureURL())
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		p.API.LogError("Failed to encode response", "error", err.Error())
	}
}

func (p *Plugin) handleCreateMeeting(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")

	var req createMeetingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.handleErrorWithCode(w, http.StatusBadRequest, "Invalid request body.", err)
		return
	}

	if req.ChannelID == "" {
		p.handleErrorWithCode(w, http.StatusBadRequest, "channel_id is required.", nil)
		return
	}

	if !p.IsPluginConfigured() {
		isAdmin, err := p.IsUserAdmin(userID)
		if err != nil {
			p.API.LogError("Failed to check admin status for meeting creation", "user_id", userID, "error", err.Error())
			p.sendMeetingEphemeralResponse(w, userID, req.ChannelID, "The Google Meet plugin is not configured. Please contact your system administrator.", "not_configured")
			return
		}

		message := "The Google Meet plugin is not configured. Please contact your system administrator."
		if isAdmin {
			if configureURL := p.GetPluginConfigureURL(); configureURL != "" {
				message = fmt.Sprintf("The Google Meet plugin is not configured. [Configure it in the System Console](%s).", configureURL)
			} else {
				message = "The Google Meet plugin is not configured. Mattermost Site URL must be configured before the System Console link is available."
			}
		}

		p.sendMeetingEphemeralResponse(w, userID, req.ChannelID, message, "not_configured")
		return
	}

	connected, err := p.IsUserConnected(userID)
	if err != nil {
		p.API.LogError("Failed to check connection status", "user_id", userID, "channel_id", req.ChannelID, "error", err.Error())
		p.sendMeetingEphemeralResponse(w, userID, req.ChannelID, "Failed to check Google connection status. Please try again.", "connection_check_failed")
		return
	}

	if !connected {
		connectURL := p.GetConnectURL()
		message := "You need to connect your Google account first."
		if connectURL != "" {
			message = fmt.Sprintf("You need to connect your Google account first. [Click here to connect](%s).", connectURL)
		}
		p.sendMeetingEphemeralResponse(w, userID, req.ChannelID, message, "not_connected")
		return
	}

	meetURL, err := p.StartMeeting(userID, req.ChannelID, req.Topic, req.ConnectionID)
	if err != nil {
		if errors.Is(err, command.ErrNeedsReconnect) {
			connectURL := p.GetConnectURL()
			message := "Your Google account needs to be reconnected."
			if connectURL != "" {
				message = fmt.Sprintf("Your Google account needs to be reconnected. [Click here to reconnect](%s).", connectURL)
			}
			p.sendMeetingEphemeralResponse(w, userID, req.ChannelID, message, "needs_reconnect")
			return
		}
		if errors.Is(err, ErrNoChannelPermission) {
			p.sendMeetingEphemeralResponse(w, userID, req.ChannelID, "You do not have permission to create posts in this channel.", "permission_denied")
			return
		}
		if errors.Is(err, command.ErrPublicChannelRestricted) {
			p.sendMeetingEphemeralResponse(w, userID, req.ChannelID, "Meeting creation is restricted in public channels.", "public_channel_restricted")
			return
		}
		p.API.LogError("Failed to create meeting", "user_id", userID, "channel_id", req.ChannelID, "error", err.Error())
		p.sendMeetingEphemeralResponse(w, userID, req.ChannelID, "Failed to create meeting. Please try again or check the server logs.", "meeting_failed")
		return
	}

	writeJSONResponse(w, http.StatusOK, map[string]string{
		"status":      "ok",
		"meeting_url": meetURL,
	}, p.API)
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
