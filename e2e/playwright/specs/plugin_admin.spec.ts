// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {fakeGoogle} from '../lib/fake_google';
import {connectUser, isConnected} from '../lib/kv';
import {clientFor, executeCommand, pluginRequest} from '../lib/meet_api';
import {ensureMeetPlugin, updateMeetPluginSettings} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';
import {MeetPosts} from '../pages/meet_posts';
import {MeetSlashCommand} from '../pages/meet_slash_command';
import {waitForMeetWebapp} from '../pages/meet_webapp';

test.describe('plugin administration and help', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that only a system admin can trigger a poll cycle, and that it reports when conference polling is disabled
     */
    test('lets only system admins trigger a poll cycle', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin
        const {adminClient, user} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);

        // # Trigger a poll cycle as a regular user
        const asUser = await pluginRequest(await clientFor(user), 'POST', '/admin/poll-now');

        // * Verify it is forbidden
        expect(asUser.status).toBe(403);

        // # Trigger a poll cycle as a system admin
        const asAdmin = await pluginRequest(adminClient, 'POST', '/admin/poll-now');

        // * Verify it is accepted
        expect(asAdmin).toEqual({status: 200, json: {status: 'poll triggered'}});

        // # Turn conference artifact posts off and trigger again
        await updateMeetPluginSettings(adminClient, {enableConferenceArtifactPosts: false});
        const disabled = await pluginRequest(adminClient, 'POST', '/admin/poll-now');

        // * Verify the plugin reports the feature is off
        expect(disabled).toEqual({status: 200, json: {status: 'feature_disabled'}});
    });

    /**
     * @objective Verify that a poll cycle looks up conferences of a meeting started from Mattermost with the user's token and a filter Google accepts
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('polls Google for conferences of a meeting started from Mattermost', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and start a meeting
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const client = await clientFor(user);
        const channel = await client.getChannelByName(team.id, 'town-square');
        await executeCommand(client, channel.id, '/meet start');
        const [space] = await fakeGoogle.spaces();

        // # Trigger a poll cycle
        await pluginRequest(adminClient, 'POST', '/admin/poll-now');

        // * Verify the poller lists the space's conference records once, as the user, with a space and start-time filter
        const lists = async () => (await fakeGoogle.requests()).filter((r) => r.methodId === 'meet.conferenceRecords.list');
        await expect.poll(lists).toHaveLength(1);
        const [list] = await lists();
        expect(list.user).toBe('owner');
        expect(list.status).toBe(200);
        const filter = new URLSearchParams(list.query).get('filter');
        expect(filter).toMatch(new RegExp(`^space\\.name="${space.name}" AND start_time>="\\d{4}-\\d\\d-\\d\\dT[\\d:.]+Z"$`));
    });

    /**
     * @objective Verify that /meet with no subcommand, /meet help and an unknown subcommand all show the command help
     */
    test('shows help for /meet, /meet help and unknown subcommands', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and log in as a regular user
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        const slashCommand = new MeetSlashCommand(channelsPage);

        // # Run /meet, /meet help and /meet launch
        await slashCommand.run();
        await slashCommand.run('help');
        await slashCommand.run('launch');

        // * Verify each shows the help, listing every subcommand
        const posts = new MeetPosts(channelsPage);
        await expect(posts.postsWithText('Google Meet commands:')).toHaveCount(3);
        for (const usage of ['/meet start [topic]', '/meet connect', '/meet disconnect', '/meet config authuser <number>', '/meet subscription add', '/meet subscription remove', '/meet subscription list', '/meet help']) {
            await expect(posts.help).toContainText(usage);
        }
    });

    /**
     * @objective Verify that changing the encryption key disconnects users whose tokens were stored with the old key, and that they are asked to connect again
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store, encrypted with the original key
     */
    test('disconnects users when the encryption key changes', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and connect a regular user
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);

        // # Change the encryption key
        await updateMeetPluginSettings(adminClient, {encryptionKey: 'e2e-rotated-encryption-key'});

        // # Run /meet start
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        await new MeetSlashCommand(channelsPage).run('start');

        // * Verify the user is asked to connect, the unreadable token is deleted and Google is not called
        await expect(new MeetPosts(channelsPage).needsConnect).toBeVisible();
        expect(await isConnected(user.id)).toBe(false);
        expect(await fakeGoogle.requests()).toEqual([]);
    });
});
