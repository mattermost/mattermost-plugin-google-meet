// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {ChannelsPage} from '@mattermost/playwright-lib';

import {fakeGoogle} from '../lib/fake_google';
import {connectUser, isConnected, storedToken} from '../lib/kv';
import {clientFor, createPrivateChannel, executeCommand, recentPosts} from '../lib/meet_api';
import {SCOPES, stubMeetJoinPages} from '../lib/meet_oauth';
import {ensureMeetPlugin, UNCONFIGURED} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';
import {MeetAppBar} from '../pages/meet_app_bar';
import {MeetPosts} from '../pages/meet_posts';
import {MeetSlashCommand} from '../pages/meet_slash_command';
import {recordWindowOpen, waitForMeetWebapp} from '../pages/meet_webapp';

const MEETING_POST_TYPE = 'custom_google_meet';

test.describe('start a meeting', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that /meet start with a topic creates a Meet space, posts it as the user with a join button, and opens the meeting in a new tab
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('starts a meeting with a topic from the slash command', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and log in
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const {context, channelsPage} = await pw.testBrowser.login(user);
        const opened = await stubMeetJoinPages(context);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);

        // # Start a meeting with a topic
        const newTab = context.waitForEvent('page');
        await new MeetSlashCommand(channelsPage).run('start', 'Weekly', 'sync');

        // * Verify Google created exactly one space, for the owner's account
        const spaces = await fakeGoogle.spaces();
        expect(spaces).toHaveLength(1);
        expect(spaces[0].owner).toBe('owner');
        const {meetingUri} = spaces[0];

        // * Verify the meeting post shows the topic, the meeting URL and a join button for that space
        const posts = new MeetPosts(channelsPage);
        await expect(posts.meetingPost).toContainText('I have started a meeting: Weekly sync');
        await expect(posts.meetingTitle()).toHaveText('Weekly sync');
        await expect(posts.meetingUrlLink()).toHaveText(meetingUri);
        await expect(posts.joinButton()).toHaveAttribute('href', meetingUri);

        // * Verify the meeting opened in a new tab
        await newTab;
        expect(opened).toEqual([meetingUri]);

        // * Verify the post is the user's own meeting post
        const townSquare = await adminClient.getChannelByName(team.id, 'town-square');
        const [post] = await recentPosts(adminClient, townSquare.id);
        expect(post.user_id).toBe(user.id);
        expect(post.type).toBe(MEETING_POST_TYPE);
        expect(post.props.meeting_link).toBe(meetingUri);
    });

    /**
     * @objective Verify that the Start Google Meet button creates a meeting without a topic and opens it in a new tab
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('starts a meeting from the Start Google Meet button', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and log in
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const {context, channelsPage} = await pw.testBrowser.login(user);
        const opened = await stubMeetJoinPages(context);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);

        // # Click Start Google Meet
        const newTab = context.waitForEvent('page');
        await new MeetAppBar(channelsPage).startMeeting();

        // * Verify the meeting post has no topic and uses the default title
        const posts = new MeetPosts(channelsPage);
        await expect(posts.meetingTitle()).toHaveText('Google Meet');
        const townSquare = await adminClient.getChannelByName(team.id, 'town-square');
        const [post] = await recentPosts(adminClient, townSquare.id);
        expect(post.message).toBe('I have started a meeting');
        expect(post.props.meeting_topic).toBe('');

        // * Verify the post's join button and the new tab both go to the space Google created
        const [space] = await fakeGoogle.spaces();
        await expect(posts.joinButton()).toHaveAttribute('href', space.meetingUri);
        await newTab;
        expect(opened).toEqual([space.meetingUri]);
    });

    /**
     * @objective Verify that only the browser tab that clicked Start Google Meet opens the meeting, while the user's other tabs just show the post
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in); window.open is recorded in both tabs instead of opening Google Meet
     */
    test('opens the meeting only in the tab that started it', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and open Town Square in two tabs of the same session
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const {context, channelsPage} = await pw.testBrowser.login(user);
        const otherTab = new ChannelsPage(await context.newPage());
        const opened = [];
        for (const tab of [channelsPage, otherTab]) {
            await tab.goto(team.name);
            await tab.toBeVisible();
            await waitForMeetWebapp(tab);
            opened.push(await recordWindowOpen(tab));
        }
        const [openedInFirst, openedInOther] = opened;

        // # Click Start Google Meet in the first tab
        await new MeetAppBar(channelsPage).startMeeting();

        // * Verify the first tab opens the space Google created
        const [space] = await fakeGoogle.spaces();
        await expect.poll(openedInFirst).toEqual([space.meetingUri]);

        // * Verify the second tab shows the meeting post but opens nothing
        await expect(new MeetPosts(otherTab).joinButton()).toHaveAttribute('href', space.meetingUri);
        await pw.wait(2000);
        expect(await openedInOther()).toEqual([]);
    });

    /**
     * @objective Verify that /meet start in a thread posts the meeting as a reply in that thread
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('posts the meeting as a thread reply when started in a thread', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and post a thread root
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const client = await clientFor(user);
        const channel = await client.getChannelByName(team.id, 'town-square');
        const root = await client.createPost({channel_id: channel.id, message: 'Planning thread'} as any);

        // # Run /meet start in the thread
        await executeCommand(client, channel.id, '/meet start Planning', root.id);

        // * Verify the meeting post is a reply to the thread root
        const [post] = await recentPosts(client, channel.id);
        expect(post.type).toBe(MEETING_POST_TYPE);
        expect(post.root_id).toBe(root.id);
        expect(post.message).toBe('I have started a meeting: **Planning**');
    });

    /**
     * @objective Verify that starting a meeting without a connected Google account offers the connect link, from both the slash command and the button, and creates no space
     */
    test('asks a user without a connected account to connect first', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and log in as a regular user who never connected
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        const posts = new MeetPosts(channelsPage);

        // # Run /meet start
        await new MeetSlashCommand(channelsPage).run('start');

        // * Verify the plugin offers the connect link
        await expect(posts.needsConnectLink).toHaveAttribute('href', /\/plugins\/com\.mattermost\.google-meet\/api\/v1\/oauth\/connect$/);

        // # Click Start Google Meet
        await new MeetAppBar(channelsPage).startMeeting();

        // * Verify the plugin offers the connect link again
        await expect(posts.allNeedsConnect).toHaveCount(2);

        // * Verify Google was never asked to create a space
        expect(await fakeGoogle.spaces()).toEqual([]);
    });

    /**
     * @objective Verify that Restrict Meeting Creation blocks meetings in public channels from both entry points, and still allows private channels and direct messages
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('restricts meeting creation to private channels and direct messages when configured', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin with meeting creation restricted, connect a regular user and log in
        const {adminClient, adminUser, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, {restrictMeetingCreation: true});
        await connectUser(user.id);
        const privateChannel = await createPrivateChannel(adminClient, team.id, [user.id]);
        const client = await clientFor(user);
        const dm = await client.createDirectChannel([user.id, adminUser!.id]);
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        const posts = new MeetPosts(channelsPage);

        // # Run /meet start and click Start Google Meet in Town Square
        await new MeetSlashCommand(channelsPage).run('start');
        await new MeetAppBar(channelsPage).startMeeting();

        // * Verify both are refused, each with its own explanation, and no space is created
        await expect(posts.restrictedFromCommand).toBeVisible();
        await expect(posts.restrictedFromButton).toBeVisible();
        expect(await fakeGoogle.spaces()).toEqual([]);

        // # Start meetings in a private channel and in a DM
        await executeCommand(client, privateChannel.id, '/meet start Private');
        await executeCommand(client, dm.id, '/meet start Direct', '', team.id);

        // * Verify both meetings are posted
        expect((await recentPosts(client, privateChannel.id))[0].type).toBe(MEETING_POST_TYPE);
        expect((await recentPosts(client, dm.id))[0].type).toBe(MEETING_POST_TYPE);
        expect(await fakeGoogle.spaces()).toHaveLength(2);
    });

    /**
     * @objective Verify that a Meet API error while creating the space is reported to the user and posts nothing
     *
     * @precondition
     * The fake Google answers the next spaces.create with a 500
     */
    test('reports a failure when Google cannot create the meeting space', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and make the next space creation fail
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        await fakeGoogle.failNext('meet.spaces.create', 500);
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);

        // # Run /meet start
        await new MeetSlashCommand(channelsPage).run('start', 'Retro');

        // * Verify the user is told the meeting could not be created
        await expect(new MeetPosts(channelsPage).meetingFailed).toBeVisible();

        // * Verify Google was asked once and failed, and the plugin posted nothing to the channel
        const creates = (await fakeGoogle.requests()).filter((r) => r.methodId === 'meet.spaces.create');
        expect(creates.map((r) => r.status)).toEqual([500]);
        const client = await clientFor(user);
        const channel = await client.getChannelByName(team.id, 'town-square');
        expect((await recentPosts(client, channel.id)).map((p) => p.type)).not.toContain(MEETING_POST_TYPE);
    });

    /**
     * @objective Verify that a token without the meetings.space.created scope is discarded and the user is asked to reconnect
     *
     * @precondition
     * The user's seeded token only has the meetings.space.readonly scope, so Google refuses spaces.create with ACCESS_TOKEN_SCOPE_INSUFFICIENT
     */
    test('asks the user to reconnect when the token lacks the scope to create meetings', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and connect a regular user with a token that cannot create spaces
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id, {scopes: [SCOPES.readonly]});
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);

        // # Run /meet start
        await new MeetSlashCommand(channelsPage).run('start');

        // * Verify Google refused the request for lack of scope
        const creates = (await fakeGoogle.requests()).filter((r) => r.methodId === 'meet.spaces.create');
        expect(creates.map((r) => r.status)).toEqual([403]);

        // * Verify the plugin asks the user to reconnect and drops the stored token
        await expect(new MeetPosts(channelsPage).needsReconnect).toBeVisible();
        expect(await isConnected(user.id)).toBe(false);
    });

    /**
     * @objective Verify that a token about to expire is refreshed before the meeting is created, and that the new access token is stored with the same refresh token
     *
     * @precondition
     * The user's seeded access token expires in one minute, inside the plugin's five-minute refresh window
     */
    test('refreshes an expiring access token before creating the meeting', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and connect a regular user with an access token that expires in a minute
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        const seeded = await connectUser(user.id, {expiresInSeconds: 60});

        // # Start a meeting
        const client = await clientFor(user);
        const channel = await client.getChannelByName(team.id, 'town-square');
        await executeCommand(client, channel.id, '/meet start');

        // * Verify the plugin refreshed the token once, then created the space with the new token
        const calls = (await fakeGoogle.requests()).map((r) => r.methodId);
        expect(calls).toEqual(['refresh_token', 'meet.spaces.create']);
        expect((await recentPosts(client, channel.id))[0].type).toBe(MEETING_POST_TYPE);

        // * Verify the stored token has a new access token, about an hour of life (allowing a minute of
        // clock skew with the server) and the same refresh token
        const stored = await storedToken(user.id);
        expect(stored?.access_token).not.toBe(seeded.access_token);
        expect(stored?.refresh_token).toBe(seeded.refresh_token);
        expect((Date.parse(stored!.expiry) - Date.now()) / 60_000).toBeGreaterThan(54);
    });

    /**
     * @objective Verify what happens when the refresh token was revoked: the meeting fails with the generic error and the stored token is kept
     *
     * Characterization test of current behaviour, not the intended one: a revoked refresh token
     * (invalid_grant) should drop the token and ask the user to reconnect, as an insufficient scope
     * does. When the plugin is fixed, flip the last two checks to needsReconnect and not connected.
     *
     * @precondition
     * The user's seeded access token is about to expire and its refresh token was revoked at Google
     */
    test('reports a failure when the refresh token was revoked at Google', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user with an expiring token, and revoke it at Google
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        const seeded = await connectUser(user.id, {expiresInSeconds: 60});
        await fakeGoogle.revokeToken(seeded);
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);

        // # Run /meet start
        await new MeetSlashCommand(channelsPage).run('start');

        // * Verify Google refused the refresh and no space was created
        const calls = (await fakeGoogle.requests()).map((r) => [r.methodId, r.status]);
        expect(calls).toEqual([['refresh_token', 400]]);

        // * Verify the user gets the generic failure, and the dead token stays stored (known gap: the plugin does not ask to reconnect)
        await expect(new MeetPosts(channelsPage).meetingFailed).toBeVisible();
        expect(await isConnected(user.id)).toBe(true);
    });

    /**
     * @objective Verify that the Start Google Meet button is hidden from regular users while the plugin is not configured, and that an admin who clicks it gets a link to the System Console
     *
     * @precondition
     * The plugin is enabled with an empty Google client ID and secret
     */
    test('hides the button from regular users and points admins to the System Console before the plugin is configured', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin without an OAuth client
        const {adminClient, adminUser, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, UNCONFIGURED);

        // # Log in as a regular user
        const asUser = await pw.testBrowser.login(user);
        await asUser.channelsPage.goto(team.name);
        await asUser.channelsPage.toBeVisible();
        await waitForMeetWebapp(asUser.channelsPage);

        // * Verify there is no Start Google Meet button
        await expect(new MeetAppBar(asUser.channelsPage).startMeetingButton).toHaveCount(0);

        // # Log in as a system admin and click Start Google Meet
        const asAdmin = await pw.testBrowser.login(adminUser!);
        await asAdmin.channelsPage.goto(team.name);
        await asAdmin.channelsPage.toBeVisible();
        await waitForMeetWebapp(asAdmin.channelsPage);
        await new MeetAppBar(asAdmin.channelsPage).startMeeting();

        // * Verify the admin gets a link to the plugin's System Console page
        await expect(new MeetPosts(asAdmin.channelsPage).configureLink).toHaveAttribute('href', /\/admin_console\/plugins\/plugin_com\.mattermost\.google-meet$/);
    });
});
