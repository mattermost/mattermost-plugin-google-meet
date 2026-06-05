# Mattermost Google Meet Plugin


#### Warning: This is a beta plugin and not covered by any kind of Mattermost Enterprise support entitlements!

Start and join Google Meet meetings from Mattermost.

This plugin adds `/meet` slash commands and a channel header button that let users create a Google Meet link from within Mattermost. Meetings are created using the Google account that each user connects through OAuth.

<img width="1519" height="1138" alt="image" src="https://github.com/user-attachments/assets/a96a9b40-a2b7-4aae-a280-ec14c222dda9" />


## Features

### Starting meetings
- Start a Google Meet meeting with `/meet start [topic]` or the channel header button.
- Connect or reconnect a Google account with `/meet connect`.
- Disconnect a Google account with `/meet disconnect`.
- Create meetings as the currently connected Google user.
- Post a rich Mattermost message with a join link back into the channel.
- Optionally block meeting creation in public channels.
- Prompt users to connect or reconnect their Google account when OAuth is missing or stale.
- Hide the meeting button for non-admin users until the plugin is fully configured.

### Channel subscriptions
- Subscribe a channel to a recurring Meet space with `/meet subscription add <meeting-code-or-URL> [description]`.
- Unsubscribe with `/meet subscription remove <meeting-code-or-URL>`.
- See your active subscriptions with `/meet subscription list`.
- The plugin polls subscribed spaces and posts a "conference started" message in the channel whenever a new meeting begins there, so anyone watching the channel sees the meeting and can join.

### Conference artifact posts
- When recordings, transcripts, or smart notes become available after a meeting ends, the plugin posts each one as a reply in the meeting's thread. Links go directly to Google Drive / Google Docs so Google's own ACLs continue to gate access independent of channel membership.
- Transcripts are also attached as a WebVTT file so the [Mattermost Agents plugin](https://github.com/mattermost/mattermost-plugin-agents) can pick them up for summarization.
- Works for both subscribed channels and ad-hoc meetings started via `/meet start` — in the ad-hoc case, artifacts are posted as replies to the original `/meet start` post.
- Gated by the `Post Recordings, Transcripts and Smart Notes` setting. When disabled, the plugin only requests the minimum OAuth scope needed for creating meetings and the subscription commands return a clear admin-facing message.

## User Experience

After the plugin is configured by a Mattermost administrator:

1. A user runs `/meet start` or clicks the Google Meet channel header button.
2. If the user has not connected Google yet, the plugin returns a connect link.
3. Once connected, the plugin creates a Meet link and posts it into the current channel.
4. `/meet start` optionally accepts a topic, for example:

```text
/meet start Sprint planning
```

The created post includes the meeting URL and an obvious join action in the Mattermost UI.

`/meet help` shows the available commands.

### Subscribing a channel to a Meet space

Subscriptions are useful for recurring Meet spaces — for example a team's standing weekly that always uses the same Meet link.

```text
/meet subscription add abc-mnop-xyz Weekly engineering sync
```

The channel will now receive a "conference started" message whenever someone starts a meeting in that space, and (if recordings/transcripts/smart notes are enabled in the Meet space and in the plugin settings) those artifacts will appear as replies in the same thread once Google finishes processing them.

You can also pass a full meeting URL — `https://meet.google.com/abc-mnop-xyz` works in place of the bare code.

### Ad-hoc meeting tracking

For meetings started with `/meet start`, the plugin automatically tracks the resulting Meet space for the duration of its conference record TTL and posts any recording/transcript/smart note as replies to the original message. No explicit subscription is needed.

## Admin Setup

The plugin depends on Mattermost having a public `SiteURL` and on Google OAuth credentials for the Google Meet REST API.

### 1. Install and enable the plugin

Build the bundle locally:

```bash
make dist
```

This produces a plugin archive under `dist/`, typically:

```text
dist/com.mattermost.google-meet-<version>.tar.gz
```

Upload that archive in the Mattermost System Console and enable the plugin.

### 2. Ensure Mattermost Site URL is configured

Set `SiteURL` in Mattermost before configuring OAuth. The plugin uses it to build:

- the Google OAuth callback URL
- the Google connect URL
- the System Console configuration link shown to admins

If `SiteURL` is missing, the plugin intentionally reports itself as not fully configured.

### 3. Configure Google Cloud

In Google Cloud:

1. Enable the [Google Meet REST API](https://console.cloud.google.com/apis/library/meet.googleapis.com).
2. Create an OAuth 2.0 Client ID of type `Web application`.
3. Add the redirect URI (`https://your-mattermost-server.com/plugins/com.mattermost.google-meet/api/v1/oauth/callback`).

The plugin displays the expected redirect URI in the System Console plugin settings header after activation.

### 4. Fill in plugin settings

In the Mattermost System Console, configure:

- `Google OAuth Client ID`: the Google OAuth client ID.
- `Google OAuth Client Secret`: the Google OAuth client secret.
- `Encryption Key`: the generated secret used to encrypt OAuth tokens at rest in Mattermost's KV store.
- `Restrict Meeting Creation`: when enabled, users can only create meetings in private channels, group messages, and direct messages.
- `Post Recordings, Transcripts and Smart Notes`: enables the background poller that watches subscribed and ad-hoc meetings and posts artifacts as thread replies. Default: enabled.
- `Polling Interval (seconds)`: how often the poller checks Google for new conferences and artifacts. Default: 60 seconds. Minimum: 30 seconds.

Important notes:

- Rotating the `Encryption Key` invalidates existing stored Google connections.
- If Google scopes change or a token becomes unusable, the plugin asks the user to reconnect.

### OAuth scopes requested

The plugin requests the minimum scopes required by the features that are enabled:

- `https://www.googleapis.com/auth/meetings.space.created` — always requested. Lets the plugin create new Meet spaces on the user's behalf for `/meet start`.
- `https://www.googleapis.com/auth/meetings.space.readonly` — only requested when `Post Recordings, Transcripts and Smart Notes` is enabled. Lets the plugin read conference records, recordings, transcripts, and smart notes for subscribed and ad-hoc meetings.

If you toggle `Post Recordings, Transcripts and Smart Notes` from disabled to enabled after users have already connected, existing tokens will not carry the readonly scope. Affected users will be prompted to reconnect the next time they hit a feature that needs it.

## Configuration Reference

### `GoogleClientID`

OAuth client ID used when sending users to Google for authorization.

### `GoogleClientSecret`

OAuth client secret used during token exchange and refresh. This field is marked secret in the plugin manifest and is masked in the System Console.

### `EncryptionKey`

Generated secret used to encrypt OAuth tokens before storing them in Mattermost. The plugin will not enable OAuth token operations until this is configured.

### `RestrictMeetingCreation`

When enabled, meeting creation is blocked in public channels. Users can still start meetings in private channels and direct-message surfaces.

### `EnableConferenceArtifactPosts`

When enabled, the plugin runs a background poller that watches subscribed Meet spaces and ad-hoc meetings, and posts conference-started messages plus recordings/transcripts/smart notes as thread replies. Also controls whether the `meetings.space.readonly` OAuth scope is requested at connect time. When disabled, `/meet subscription` commands are refused with a message pointing back to this setting. Default: enabled.

### `PollIntervalSeconds`

How often (in seconds) the plugin polls Google for new conferences and artifacts. Minimum 30. Default 60. Only used when `EnableConferenceArtifactPosts` is enabled.

## Development

### Requirements

- Go `1.25`
- Node.js `24.13.1` from `.nvmrc`
- npm compatible with that Node version

Use the repo's Node version with:

```bash
nvm use
```

Install webapp dependencies:

```bash
cd webapp && npm install
```

### Common commands

- `make test`: run server and webapp unit tests
- `make check-style`: run eslint, type checking, `go vet`, and `golangci-lint`
- `make dist`: build and bundle the plugin
- `make deploy`: build and deploy the plugin to a Mattermost server
- `make watch`: watch and rebuild webapp assets for development

### Local deployment

If Mattermost local mode is enabled, `make deploy` can deploy directly to your local server.

You can also deploy with API credentials or a personal access token, for example:

```bash
export MM_SERVICESETTINGS_SITEURL=http://localhost:8065
export MM_ADMIN_TOKEN=YOUR_TOKEN
make deploy
```

For watch mode:

```bash
export MM_SERVICESETTINGS_SITEURL=http://localhost:8065
export MM_ADMIN_TOKEN=YOUR_TOKEN
make watch
```

If `MM_SERVICESETTINGS_ENABLEDEVELOPER` is set, the server build is limited to the current platform architecture to speed up local iteration.

## Testing

Run the full validation suite:

```bash
make test
make check-style
```

Server tests cover OAuth flow, token storage behavior, and meeting creation paths. Webapp tests cover the bundle manifest and basic React rendering.

## Release

The plugin version is derived at build time from git tags unless you explicitly maintain the manifest version yourself.

Release helpers:

- `make patch`
- `make minor`
- `make major`
- `make patch-rc`
- `make minor-rc`
- `make major-rc`

These targets create and push signed tags, so make sure you are on the correct branch and up to date before using them.

## Troubleshooting

### The plugin says it is not configured

Check all of the following:

- Mattermost `SiteURL` is set
- Google Meet REST API is enabled
- OAuth redirect URI in Google Cloud matches the one shown by the plugin
- `GoogleClientID`, `GoogleClientSecret`, and `EncryptionKey` are populated

### Users are asked to reconnect Google

This usually means one of:

- the token no longer has the required Google Meet scope
- the encryption key changed
- the stored token became unreadable or expired

Reconnecting refreshes the user's stored OAuth state.

### Users cannot start meetings in a channel

The plugin only posts meeting links into channels where the user has permission to create posts.

If `RestrictMeetingCreation` is enabled, public channels are also blocked even when the user has permission to post there.

### `/meet subscription` says "Channel subscriptions are disabled"

`EnableConferenceArtifactPosts` is turned off in the System Console. Enable it to bring the background poller back online — note that users connected prior to enabling it will need to reconnect so their token picks up the `meetings.space.readonly` scope.

### Subscribed channel isn't receiving conference-started or artifact posts

Check each of the following in order:

- The subscriber's Google account is still connected (`/meet subscription list` runs as the subscriber and will fail if not).
- `EnableConferenceArtifactPosts` is enabled and the user reconnected after it was turned on.
- The meeting actually used the subscribed Meet space — ad-hoc meetings post under the originating `/meet start` thread, not under a channel subscription.
- For recordings/transcripts/smart notes specifically, the Google Meet space settings need to have the corresponding feature enabled. The plugin only relays what Google produces; it cannot retroactively enable recording on a meeting that did not opt in to it.
- Wait at least one poll interval (default 60s) after a meeting starts/ends. Artifacts in particular can take several minutes after a meeting before Google finishes processing them.

### Subscription was added but seems to apply to the wrong channel

Subscriptions are bound to the channel the `/meet subscription add` command was run in. Re-run `add` from the correct channel to create a new binding, then `remove` from the old channel.
