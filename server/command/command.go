// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package command

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	expcommand "github.com/mattermost/mattermost/server/public/pluginapi/experimental/command"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

const autocompleteIconPath = "assets/icon.svg"

// SubscriptionInfo holds display info about a subscription for /meet subscription list.
type SubscriptionInfo struct {
	MeetingCode string
	ChannelID   string
	ChannelName string // human-readable channel name, falls back to ChannelID if unavailable
	Description string
}

// MeetingStarter is the interface the command handler uses to start meetings and manage subscriptions.
type MeetingStarter interface {
	StartMeeting(userID, channelID, topic, connectionID, rootPostID string) (string, error)
	GetConnectURL() string
	DisconnectUser(userID string) error
	IsUserConnected(userID string) (bool, error)
	IsPluginConfigured() bool
	IsUserAdmin(userID string) (bool, error)
	GetPluginConfigureURL() string
	SetAuthUser(userID string, authUser int) error
	// AreSubscriptionsEnabled reports whether the conference polling/artifact features
	// are turned on. When false, /meet subscription commands are refused because the
	// readonly OAuth scope they rely on is not requested at connect time.
	AreSubscriptionsEnabled() bool
	// Subscription methods
	AddSubscription(userID, channelID, meetingCodeOrURL, description string) (*kvstore.Subscription, error)
	RemoveSubscription(userID, channelID, meetingCodeOrURL string) error
	ListSubscriptions(userID string) ([]*SubscriptionInfo, error)
}

type Handler struct {
	client         *pluginapi.Client
	meetingStarter MeetingStarter
}

type Command interface {
	Handle(args *model.CommandArgs) (*model.CommandResponse, error)
}

const meetCommandTrigger = "meet"

func NewCommandHandler(client *pluginapi.Client, meetingStarter MeetingStarter) Command {
	autocompleteData := model.NewAutocompleteData(meetCommandTrigger, "", "Google Meet commands")
	autocompleteData.AddCommand(model.NewAutocompleteData("start", "[topic]", "Start a Google Meet meeting with an optional topic"))
	autocompleteData.AddCommand(model.NewAutocompleteData("connect", "", "Connect or reconnect your Google account"))
	autocompleteData.AddCommand(model.NewAutocompleteData("disconnect", "", "Disconnect your Google account"))
	autocompleteData.AddCommand(model.NewAutocompleteData("help", "", "Show Google Meet command help"))

	configCmd := model.NewAutocompleteData("config", "", "Configure your Google Meet preferences")
	configCmd.AddCommand(model.NewAutocompleteData("authuser", "<number>", "Set the Google account index used by Meet links"))
	autocompleteData.AddCommand(configCmd)

	subCmd := model.NewAutocompleteData("subscription", "", "Manage channel subscriptions to Google Meet spaces")
	subCmd.AddCommand(model.NewAutocompleteData("add", "<meeting-code-or-URL> [description]", "Subscribe this channel to a Google Meet space"))
	subCmd.AddCommand(model.NewAutocompleteData("remove", "<meeting-code-or-URL>", "Unsubscribe this channel from a Google Meet space"))
	subCmd.AddCommand(model.NewAutocompleteData("list", "", "List your active subscriptions"))
	autocompleteData.AddCommand(subCmd)

	iconData, iconErr := expcommand.GetIconData(&client.System, autocompleteIconPath)
	if iconErr != nil {
		client.Log.Warn("Failed to load slash command icon", "error", iconErr.Error())
	}

	err := client.SlashCommand.Register(&model.Command{
		Trigger:              meetCommandTrigger,
		AutoComplete:         true,
		AutoCompleteDesc:     "Google Meet commands",
		AutoCompleteHint:     "help | start [topic] | connect | disconnect | config authuser <number> | subscription",
		AutocompleteData:     autocompleteData,
		AutocompleteIconData: iconData,
	})
	if err != nil {
		client.Log.Error("Failed to register command", "error", err)
	}
	return &Handler{
		client:         client,
		meetingStarter: meetingStarter,
	}
}

func (c *Handler) Handle(args *model.CommandArgs) (*model.CommandResponse, error) {
	fields := strings.Fields(args.Command)
	if len(fields) == 0 {
		return &model.CommandResponse{
			ResponseType: model.CommandResponseTypeEphemeral,
			Text:         "Empty command",
		}, nil
	}
	trigger := strings.TrimPrefix(fields[0], "/")
	switch trigger {
	case meetCommandTrigger:
		if len(fields) == 1 {
			return c.executeMeetHelpCommand(), nil
		}
		switch fields[1] {
		case "help":
			return c.executeMeetHelpCommand(), nil
		case "start":
			return c.executeMeetStartCommand(args, fields[2:]), nil
		case "connect":
			return c.executeMeetConnectCommand(args), nil
		case "disconnect":
			return c.executeMeetDisconnectCommand(args), nil
		case "config":
			return c.executeConfigCommand(args, fields[2:]), nil
		case "subscription":
			return c.executeSubscriptionCommand(args, fields[2:]), nil
		default:
			return c.executeMeetHelpCommand(), nil
		}
	default:
		return &model.CommandResponse{
			ResponseType: model.CommandResponseTypeEphemeral,
			Text:         fmt.Sprintf("Unknown command: %s", args.Command),
		}, nil
	}
}

func (c *Handler) executeMeetHelpCommand() *model.CommandResponse {
	return &model.CommandResponse{
		ResponseType: model.CommandResponseTypeEphemeral,
		Text: strings.Join([]string{
			"Google Meet commands:",
			"- `/meet start [topic]` starts a meeting with an optional topic.",
			"- `/meet connect` opens the Google account connection flow.",
			"- `/meet disconnect` removes your saved Google connection.",
			"- `/meet config authuser <number>` sets the Google account index used when you open Meet links.",
			"- `/meet subscription add <meeting-code-or-URL> [description]` subscribes this channel to a meeting space.",
			"- `/meet subscription remove <meeting-code-or-URL>` unsubscribes this channel from a meeting space.",
			"- `/meet subscription list` lists your active subscriptions.",
			"- `/meet help` shows this help message.",
		}, "\n"),
	}
}

func (c *Handler) ephemeral(text string) *model.CommandResponse {
	return &model.CommandResponse{
		ResponseType: model.CommandResponseTypeEphemeral,
		Text:         text,
	}
}

func (c *Handler) needsConnectResponse() *model.CommandResponse {
	connectURL := c.meetingStarter.GetConnectURL()
	text := "You need to connect your Google account first. Run `/meet connect` to get started."
	if connectURL != "" {
		text = fmt.Sprintf("You need to connect your Google account first. [Click here to connect](%s).", connectURL)
	}
	return c.ephemeral(text)
}

func (c *Handler) needsReconnectResponse() *model.CommandResponse {
	connectURL := c.meetingStarter.GetConnectURL()
	text := "Your Google account needs to be reconnected. Run `/meet connect` to reconnect."
	if connectURL != "" {
		text = fmt.Sprintf("Your Google account needs to be reconnected. [Click here to reconnect](%s).", connectURL)
	}
	return c.ephemeral(text)
}

func (c *Handler) requireConnected(userID string) *model.CommandResponse {
	connected, err := c.meetingStarter.IsUserConnected(userID)
	if err != nil {
		return c.ephemeral("Failed to check Google connection status. Please try again.")
	}
	if !connected {
		return c.needsConnectResponse()
	}
	return nil
}

func (c *Handler) executeConfigCommand(args *model.CommandArgs, subFields []string) *model.CommandResponse {
	if len(subFields) == 0 {
		return c.ephemeral("Usage: `/meet config authuser <number>`.")
	}

	if subFields[0] != "authuser" {
		return c.ephemeral(fmt.Sprintf("Unknown config setting: %s. Use `authuser`.", subFields[0]))
	}

	if len(subFields) != 2 {
		return c.ephemeral("Usage: `/meet config authuser <number>`.")
	}

	authUser, err := strconv.Atoi(subFields[1])
	if err != nil || authUser < 0 {
		return c.ephemeral("The authuser value must be a non-negative whole number.")
	}

	if err := c.meetingStarter.SetAuthUser(args.UserId, authUser); err != nil {
		if c.client != nil {
			c.client.Log.Error("Failed to save authuser preference", "user_id", args.UserId, "error", err.Error())
		}
		return c.ephemeral("Failed to save your Google account preference. Please try again.")
	}

	return c.ephemeral(fmt.Sprintf("Google Meet links will now use account index **%d**.", authUser))
}

func (c *Handler) executeSubscriptionCommand(args *model.CommandArgs, subFields []string) *model.CommandResponse {
	if resp := c.pluginNotConfiguredResponse(args.UserId); resp != nil {
		return resp
	}

	if !c.meetingStarter.AreSubscriptionsEnabled() {
		return c.ephemeral("Channel subscriptions are disabled. Ask your system administrator to enable **Post Recordings, Transcripts and Smart Notes** in the plugin settings.")
	}

	if c.client == nil || !c.client.User.HasPermissionToChannel(args.UserId, args.ChannelId, model.PermissionManageChannelRoles) {
		return c.ephemeral("Managing Google Meet subscriptions requires channel admin or system admin permissions. Please ask a channel or system administrator to run this command for you.")
	}

	if resp := c.requireConnected(args.UserId); resp != nil {
		return resp
	}

	if len(subFields) == 0 {
		return c.ephemeral("Usage: `/meet subscription add <meeting-code>`, `/meet subscription remove <meeting-code>`, or `/meet subscription list`.")
	}

	switch subFields[0] {
	case "add":
		if len(subFields) < 2 {
			return c.ephemeral("Usage: `/meet subscription add <meeting-code-or-URL> [description]`.")
		}
		description := strings.Join(subFields[2:], " ")
		return c.executeSubscriptionAdd(args, subFields[1], description)
	case "remove":
		if len(subFields) < 2 {
			return c.ephemeral("Usage: `/meet subscription remove <meeting-code-or-URL>`.")
		}
		return c.executeSubscriptionRemove(args, subFields[1])
	case "list":
		return c.executeSubscriptionList(args)
	default:
		return c.ephemeral(fmt.Sprintf("Unknown subscription subcommand: %s. Use `add`, `remove`, or `list`.", subFields[0]))
	}
}

func (c *Handler) executeSubscriptionAdd(args *model.CommandArgs, meetingInput, description string) *model.CommandResponse {
	if c.client != nil {
		ch, chErr := c.client.Channel.Get(args.ChannelId)
		if chErr != nil {
			return c.ephemeral("Failed to check channel type. Please try again.")
		}
		if ch.Type == model.ChannelTypeDirect || ch.Type == model.ChannelTypeGroup {
			return c.ephemeral("Subscriptions can only be added to public or private channels, not direct messages or group chats.")
		}
	}

	sub, err := c.meetingStarter.AddSubscription(args.UserId, args.ChannelId, meetingInput, description)
	if err != nil {
		if errors.Is(err, ErrNeedsReconnect) {
			return c.needsReconnectResponse()
		}
		if c.client != nil {
			c.client.Log.Error("Failed to add subscription", "user_id", args.UserId, "error", err.Error())
		}
		return c.ephemeral(fmt.Sprintf("Failed to subscribe: %s", err.Error()))
	}
	return c.ephemeral(fmt.Sprintf(
		"This channel is now subscribed to Google Meet space **%s**. Recordings, transcripts, and smart notes will be posted here after each meeting.",
		sub.MeetingCode,
	))
}

func (c *Handler) executeSubscriptionRemove(args *model.CommandArgs, meetingInput string) *model.CommandResponse {
	if err := c.meetingStarter.RemoveSubscription(args.UserId, args.ChannelId, meetingInput); err != nil {
		if errors.Is(err, ErrNeedsReconnect) {
			return c.needsReconnectResponse()
		}
		return c.ephemeral(fmt.Sprintf("Failed to remove subscription: %s", err.Error()))
	}
	return c.ephemeral(fmt.Sprintf("Unsubscribed from **%s**.", meetingInput))
}

func (c *Handler) executeSubscriptionList(args *model.CommandArgs) *model.CommandResponse {
	subs, err := c.meetingStarter.ListSubscriptions(args.UserId)
	if err != nil {
		return c.ephemeral("Failed to list subscriptions. Please try again.")
	}
	if len(subs) == 0 {
		return c.ephemeral("You have no active Google Meet subscriptions. Use `/meet subscription add <meeting-code>` to subscribe.")
	}

	lines := []string{
		"Your active Google Meet subscriptions:",
		"",
		"| Meeting | Channel | Description |",
		"|:--------|:--------|:------------|",
	}
	for _, sub := range subs {
		channelRef := sub.ChannelName
		if channelRef == "" {
			channelRef = sub.ChannelID
		}
		lines = append(lines, fmt.Sprintf(
			"| [%s](https://meet.google.com/%s) | ~%s | %s |",
			sub.MeetingCode, sub.MeetingCode, channelRef, sub.Description,
		))
	}
	return c.ephemeral(strings.Join(lines, "\n"))
}

func (c *Handler) pluginNotConfiguredResponse(userID string) *model.CommandResponse {
	if c.meetingStarter.IsPluginConfigured() {
		return nil
	}

	isAdmin, err := c.meetingStarter.IsUserAdmin(userID)
	if err != nil {
		return c.ephemeral("Failed to check permissions. Please try again.")
	}
	if !isAdmin {
		return c.ephemeral("The Google Meet plugin is not configured. Please contact your system administrator.")
	}

	configURL := c.meetingStarter.GetPluginConfigureURL()
	if configURL == "" {
		return c.ephemeral("The Google Meet plugin is not configured. Mattermost Site URL must be configured before the System Console link is available.")
	}
	return c.ephemeral(fmt.Sprintf("The Google Meet plugin is not configured. [Configure it in the System Console](%s).", configURL))
}

func (c *Handler) executeMeetConnectCommand(args *model.CommandArgs) *model.CommandResponse {
	if resp := c.pluginNotConfiguredResponse(args.UserId); resp != nil {
		return resp
	}

	connectURL := c.meetingStarter.GetConnectURL()
	if connectURL == "" {
		return c.ephemeral("The Google account connection link is unavailable. Please contact your system administrator.")
	}

	return c.ephemeral(fmt.Sprintf("Connect or reconnect your Google account. [Click here to continue](%s).", connectURL))
}

func (c *Handler) executeMeetDisconnectCommand(args *model.CommandArgs) *model.CommandResponse {
	if err := c.meetingStarter.DisconnectUser(args.UserId); err != nil {
		return c.ephemeral("Failed to disconnect your Google account. Please try again.")
	}
	return c.ephemeral("Your Google account has been disconnected.")
}

func (c *Handler) executeMeetStartCommand(args *model.CommandArgs, topicFields []string) *model.CommandResponse {
	if resp := c.pluginNotConfiguredResponse(args.UserId); resp != nil {
		return resp
	}
	if resp := c.requireConnected(args.UserId); resp != nil {
		return resp
	}

	topic := strings.Join(topicFields, " ")

	if _, err := c.meetingStarter.StartMeeting(args.UserId, args.ChannelId, topic, "", args.RootId); err != nil {
		if errors.Is(err, ErrNeedsReconnect) {
			return c.needsReconnectResponse()
		}
		if errors.Is(err, ErrPublicChannelRestricted) {
			return c.ephemeral("Meeting creation is restricted in public channels. Try a private channel or direct message instead.")
		}
		// NewCommandHandler always sets c.client, but tests may construct Handler directly.
		if c.client != nil {
			c.client.Log.Error("Failed to create meeting", "user_id", args.UserId, "error", err.Error())
		}
		return c.ephemeral("Failed to create meeting. Please try again or check the server logs.")
	}

	return &model.CommandResponse{}
}
