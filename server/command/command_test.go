// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package command

import (
	"errors"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-google-meet/server/store/kvstore"
)

type minimalAPI struct {
	plugin.API
	ch              *model.Channel
	chErr           *model.AppError
	channelPermDeny bool
}

func (m *minimalAPI) GetChannel(channelID string) (*model.Channel, *model.AppError) {
	if m.ch != nil || m.chErr != nil {
		return m.ch, m.chErr
	}
	return &model.Channel{Id: channelID, Type: model.ChannelTypeOpen}, nil
}

func (m *minimalAPI) HasPermissionToChannel(_, _ string, _ *model.Permission) bool {
	return !m.channelPermDeny
}
func (m *minimalAPI) LogError(string, ...any) {}
func (m *minimalAPI) LogWarn(string, ...any)  {}
func (m *minimalAPI) LogInfo(string, ...any)  {}
func (m *minimalAPI) LogDebug(string, ...any) {}

func permissiveClient() *pluginapi.Client {
	return pluginapi.NewClient(&minimalAPI{}, nil)
}

type mockMeetingStarter struct {
	configured            bool
	connected             bool
	isAdmin               bool
	subscriptionsDisabled bool
	connectURL            string
	configureURL          string
	startErr              error
	connectedErr          error
	adminErr              error
	disconnectErr         error
	disconnectedID        string
	setAuthUserErr        error
	authUserID            string
	authUser              int
	startedMeeting        struct {
		userID     string
		channelID  string
		topic      string
		rootPostID string
	}
	addSubErr      error
	addedSub       *kvstore.Subscription
	removeSubErr   error
	listSubsResult []*SubscriptionInfo
	listSubsErr    error
}

func (m *mockMeetingStarter) StartMeeting(userID, channelID, topic, _, rootPostID string) (string, error) {
	m.startedMeeting.userID = userID
	m.startedMeeting.channelID = channelID
	m.startedMeeting.topic = topic
	m.startedMeeting.rootPostID = rootPostID
	return "", m.startErr
}

func (m *mockMeetingStarter) GetConnectURL() string { return m.connectURL }
func (m *mockMeetingStarter) DisconnectUser(userID string) error {
	m.disconnectedID = userID
	return m.disconnectErr
}
func (m *mockMeetingStarter) IsPluginConfigured() bool      { return m.configured }
func (m *mockMeetingStarter) GetPluginConfigureURL() string { return m.configureURL }

func (m *mockMeetingStarter) IsUserConnected(_ string) (bool, error) {
	return m.connected, m.connectedErr
}

func (m *mockMeetingStarter) IsUserAdmin(_ string) (bool, error) {
	return m.isAdmin, m.adminErr
}

func (m *mockMeetingStarter) SetAuthUser(userID string, authUser int) error {
	m.authUserID = userID
	m.authUser = authUser
	return m.setAuthUserErr
}

func (m *mockMeetingStarter) AreSubscriptionsEnabled() bool { return !m.subscriptionsDisabled }

func (m *mockMeetingStarter) AddSubscription(_, _, _, _ string) (*kvstore.Subscription, error) {
	return m.addedSub, m.addSubErr
}

func (m *mockMeetingStarter) RemoveSubscription(_, _, _ string) error {
	return m.removeSubErr
}

func (m *mockMeetingStarter) ListSubscriptions(_ string) ([]*SubscriptionInfo, error) {
	return m.listSubsResult, m.listSubsErr
}

func TestHandle_UnknownCommand(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{Command: "/unknown"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Unknown command")
}

func TestHandle_EmptyCommand(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{Command: ""})
	require.NoError(t, err)
	assert.Equal(t, "Empty command", resp.Text)
}

func TestExecuteMeetCommand_NotConfigured_Admin(t *testing.T) {
	mock := &mockMeetingStarter{
		configured:   false,
		isAdmin:      true,
		configureURL: "http://localhost/admin",
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not configured")
	assert.Contains(t, resp.Text, "http://localhost/admin")
	assert.Equal(t, model.CommandResponseTypeEphemeral, resp.ResponseType)
}

func TestExecuteMeetCommand_NotConfigured_AdminWithoutConfigureURL(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: false,
		isAdmin:    true,
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Mattermost Site URL")
	assert.NotContains(t, resp.Text, "Configure it in the System Console")
}

func TestExecuteMeetCommand_NotConfigured_NonAdmin(t *testing.T) {
	mock := &mockMeetingStarter{configured: false, isAdmin: false}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not configured")
	assert.Contains(t, resp.Text, "system administrator")
}

func TestExecuteMeetCommand_NotConfigured_AdminCheckError(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: false,
		adminErr:   errors.New("api error"),
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Failed to check permissions")
}

func TestExecuteMeetCommand_NotConnected(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connected:  false,
		connectURL: "http://localhost/connect",
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "connect your Google account")
	assert.Contains(t, resp.Text, "http://localhost/connect")
}

func TestExecuteMeetCommand_ConnectionCheckError(t *testing.T) {
	mock := &mockMeetingStarter{
		configured:   true,
		connectedErr: errors.New("kv error"),
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Failed to check Google connection status")
}

func TestExecuteMeetCommand_Help(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command: "/meet help",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "/meet start [topic]")
	assert.Contains(t, resp.Text, "/meet connect")
	assert.Contains(t, resp.Text, "/meet disconnect")
	assert.Contains(t, resp.Text, "/meet config authuser <number>")
	assert.Contains(t, resp.Text, "/meet help")
	assert.Empty(t, mock.startedMeeting.userID)
}

func TestExecuteMeetCommand_WithoutSubcommandShowsHelp(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "/meet start [topic]")
	assert.Empty(t, mock.startedMeeting.userID)
}

func TestExecuteMeetCommand_StartSubcommandSuccess(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start Sprint Planning",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Equal(t, "", resp.Text)
	assert.Equal(t, "Sprint Planning", mock.startedMeeting.topic)
	assert.Empty(t, mock.startedMeeting.rootPostID)
}

func TestExecuteMeetCommand_StartSubcommandInThread(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start Sprint Planning",
		UserId:    "user1",
		ChannelId: "chan1",
		RootId:    "thread-root-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "", resp.Text)
	assert.Equal(t, "Sprint Planning", mock.startedMeeting.topic)
	assert.Equal(t, "thread-root-1", mock.startedMeeting.rootPostID)
}

func TestExecuteMeetCommand_Connect(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connectURL: "http://localhost/connect",
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet connect",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Connect or reconnect your Google account")
	assert.Contains(t, resp.Text, "http://localhost/connect")
	assert.Empty(t, mock.startedMeeting.userID)
}

func TestExecuteMeetCommand_Disconnect(t *testing.T) {
	mock := &mockMeetingStarter{}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet disconnect",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "has been disconnected")
	assert.Equal(t, "user1", mock.disconnectedID)
}

func TestExecuteMeetCommand_DisconnectError(t *testing.T) {
	mock := &mockMeetingStarter{disconnectErr: errors.New("kv error")}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet disconnect",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Failed to disconnect")
	assert.Equal(t, "user1", mock.disconnectedID)
}

func TestConfigCommand_AuthUserSuccess(t *testing.T) {
	mock := &mockMeetingStarter{}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command: "/meet config authuser 1",
		UserId:  "user1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "account index **1**")
	assert.Equal(t, "user1", mock.authUserID)
	assert.Equal(t, 1, mock.authUser)
}

func TestConfigCommand_AuthUserZero(t *testing.T) {
	mock := &mockMeetingStarter{}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command: "/meet config authuser 0",
		UserId:  "user1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "account index **0**")
	assert.Equal(t, "user1", mock.authUserID)
	assert.Equal(t, 0, mock.authUser)
}

func TestConfigCommand_InvalidAuthUser(t *testing.T) {
	for _, commandText := range []string{
		"/meet config",
		"/meet config authuser",
		"/meet config authuser -1",
		"/meet config authuser 1.5",
		"/meet config authuser one",
		"/meet config authuser 1 extra",
	} {
		t.Run(commandText, func(t *testing.T) {
			mock := &mockMeetingStarter{}
			handler := &Handler{meetingStarter: mock}

			resp, err := handler.Handle(&model.CommandArgs{
				Command: commandText,
				UserId:  "user1",
			})
			require.NoError(t, err)
			assert.NotEmpty(t, resp.Text)
			assert.Empty(t, mock.authUserID)
		})
	}
}

func TestConfigCommand_UnknownSetting(t *testing.T) {
	mock := &mockMeetingStarter{}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command: "/meet config timezone 1",
		UserId:  "user1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Unknown config setting")
	assert.Empty(t, mock.authUserID)
}

func TestConfigCommand_AuthUserStorageError(t *testing.T) {
	mock := &mockMeetingStarter{setAuthUserErr: errors.New("preference error")}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command: "/meet config authuser 1",
		UserId:  "user1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Failed to save")
	assert.NotContains(t, resp.Text, "preference error")
}

func TestExecuteMeetCommand_UnknownSubcommandShowsHelp(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet Sprint Planning",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "/meet start [topic]")
	assert.Empty(t, mock.startedMeeting.userID)
}

func TestExecuteMeetCommand_NeedsReconnect(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connected:  true,
		startErr:   ErrNeedsReconnect,
		connectURL: "http://localhost/connect",
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "reconnected")
	assert.Contains(t, resp.Text, "http://localhost/connect")
}

func TestExecuteMeetCommand_PublicChannelRestricted(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connected:  true,
		startErr:   ErrPublicChannelRestricted,
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "restricted in public channels")
	assert.Contains(t, resp.Text, "private channel or direct message")
}

func TestExecuteMeetCommand_StartMeetingError(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connected:  true,
		startErr:   errors.New("some internal error"),
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet start",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Failed to create meeting")
	// Must NOT contain the internal error message
	assert.NotContains(t, resp.Text, "some internal error")
}

func TestSubscriptionCommand_NotConnected(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connected:  false,
		connectURL: "http://localhost/connect",
	}
	handler := &Handler{meetingStarter: mock, client: permissiveClient()}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription add abc-mnop-xyz",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "connect your Google account")
}

func TestSubscriptionCommand_FeatureDisabled(t *testing.T) {
	mock := &mockMeetingStarter{
		configured:            true,
		connected:             true,
		subscriptionsDisabled: true,
	}
	handler := &Handler{meetingStarter: mock}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription add abc-mnop-xyz",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "subscriptions are disabled")
}

func TestSubscriptionCommand_PermissionDenied(t *testing.T) {
	for _, sub := range []string{"add abc-mnop-xyz", "remove abc-mnop-xyz", "list"} {
		t.Run(sub, func(t *testing.T) {
			mock := &mockMeetingStarter{
				configured: true,
				connected:  false,
				connectURL: "http://localhost/connect",
			}
			api := &minimalAPI{channelPermDeny: true}
			handler := &Handler{
				meetingStarter: mock,
				client:         pluginapi.NewClient(api, nil),
			}

			resp, err := handler.Handle(&model.CommandArgs{
				Command:   "/meet subscription " + sub,
				UserId:    "regular-user",
				ChannelId: "chan1",
			})
			require.NoError(t, err)
			assert.Contains(t, resp.Text, "channel admin or system admin")
			assert.NotContains(t, resp.Text, "connect your Google account", "must not leak the connect prompt to non-admins")
		})
	}
}

func TestSubscriptionCommand_NoSubcommand(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock, client: permissiveClient()}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage:")
}

func TestSubscriptionCommand_Add_Success(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connected:  true,
		addedSub: &kvstore.Subscription{
			MeetingCode: "abc-mnop-xyz",
			SpaceID:     "spaces/abc123",
		},
	}
	handler := &Handler{meetingStarter: mock, client: permissiveClient()}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription add abc-mnop-xyz",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "subscribed")
	assert.Contains(t, resp.Text, "abc-mnop-xyz")
}

func TestSubscriptionCommand_Add_MissingArg(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock, client: permissiveClient()}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription add",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage:")
}

func TestSubscriptionCommand_Add_NeedsReconnect(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connected:  true,
		addSubErr:  ErrNeedsReconnect,
		connectURL: "http://localhost/connect",
	}
	handler := &Handler{meetingStarter: mock, client: permissiveClient()}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription add abc-mnop-xyz",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "reconnected")
}

func TestSubscriptionCommand_Remove_Success(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock, client: permissiveClient()}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription remove abc-mnop-xyz",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Unsubscribed")
}

func TestSubscriptionCommand_List_Empty(t *testing.T) {
	mock := &mockMeetingStarter{
		configured:     true,
		connected:      true,
		listSubsResult: nil,
	}
	handler := &Handler{meetingStarter: mock, client: permissiveClient()}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription list",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "no active")
}

func TestSubscriptionCommand_List_WithEntries(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connected:  true,
		listSubsResult: []*SubscriptionInfo{
			{MeetingCode: "abc-mnop-xyz", ChannelID: "chan1", ChannelName: "town-square", Description: "Weekly Standup"},
		},
	}
	handler := &Handler{meetingStarter: mock, client: permissiveClient()}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription list",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "abc-mnop-xyz")
	assert.Contains(t, resp.Text, "meet.google.com/abc-mnop-xyz")
	assert.Contains(t, resp.Text, "town-square")
	assert.Contains(t, resp.Text, "Weekly Standup")
	// Verify markdown table header is present.
	assert.Contains(t, resp.Text, "| Meeting |")
}

func TestSubscriptionCommand_UnknownSubcommand(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	handler := &Handler{meetingStarter: mock, client: permissiveClient()}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription foo",
		UserId:    "user1",
		ChannelId: "chan1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Unknown subscription subcommand")
}

func TestSubscriptionCommand_Add_RejectedInDM(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	api := &minimalAPI{ch: &model.Channel{Type: model.ChannelTypeDirect}}
	handler := &Handler{
		meetingStarter: mock,
		client:         pluginapi.NewClient(api, nil),
	}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription add abc-mnop-xyz",
		UserId:    "user1",
		ChannelId: "dm1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "direct messages")
	// AddSubscription should never have been called.
	assert.Nil(t, mock.addedSub)
}

func TestSubscriptionCommand_Add_RejectedInGroupChat(t *testing.T) {
	mock := &mockMeetingStarter{configured: true, connected: true}
	api := &minimalAPI{ch: &model.Channel{Type: model.ChannelTypeGroup}}
	handler := &Handler{
		meetingStarter: mock,
		client:         pluginapi.NewClient(api, nil),
	}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription add abc-mnop-xyz",
		UserId:    "user1",
		ChannelId: "gm1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "group chats")
	assert.Nil(t, mock.addedSub)
}

func TestSubscriptionCommand_Add_AllowedInPrivateChannel(t *testing.T) {
	mock := &mockMeetingStarter{
		configured: true,
		connected:  true,
		addedSub:   &kvstore.Subscription{MeetingCode: "abc-mnop-xyz"},
	}
	api := &minimalAPI{ch: &model.Channel{Type: model.ChannelTypePrivate}}
	handler := &Handler{
		meetingStarter: mock,
		client:         pluginapi.NewClient(api, nil),
	}

	resp, err := handler.Handle(&model.CommandArgs{
		Command:   "/meet subscription add abc-mnop-xyz",
		UserId:    "user1",
		ChannelId: "priv1",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "subscribed")
}
