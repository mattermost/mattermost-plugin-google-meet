// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {fakeGoogle} from '../lib/fake_google';
import {connectUser} from '../lib/kv';
import {stubMeetJoinPages} from '../lib/meet_oauth';
import {ensureMeetPlugin} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';
import {MeetAppBar} from '../pages/meet_app_bar';
import {MeetPosts} from '../pages/meet_posts';
import {MeetSlashCommand} from '../pages/meet_slash_command';
import {waitForMeetWebapp} from '../pages/meet_webapp';

test.describe('choose the Google account index for Meet links', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that /meet config authuser makes the join button and the auto-opened tab use that Google account index, while the displayed URL stays clean
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('opens Meet links with the configured Google account index', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and log in
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const {context, channelsPage} = await pw.testBrowser.login(user);
        const opened = await stubMeetJoinPages(context);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        const posts = new MeetPosts(channelsPage);

        // # Set the account index to 2
        await new MeetSlashCommand(channelsPage).run('config', 'authuser', '2');

        // * Verify the plugin confirms the setting
        await expect(posts.authUserSaved(2)).toBeVisible();

        // # Start a meeting from the button
        const newTab = context.waitForEvent('page');
        await new MeetAppBar(channelsPage).startMeeting();
        await newTab;

        // * Verify the join button and the new tab add authuser=2, and the displayed URL does not
        const [space] = await fakeGoogle.spaces();
        await expect(posts.joinButton()).toHaveAttribute('href', `${space.meetingUri}?authuser=2`);
        await expect(posts.meetingUrlLink()).toHaveText(space.meetingUri);
        expect(opened).toEqual([`${space.meetingUri}?authuser=2`]);
    });

    /**
     * @objective Verify that /meet config rejects a missing, negative or non-numeric account index and an unknown setting
     */
    test('rejects invalid /meet config input', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and log in as a regular user
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        const slashCommand = new MeetSlashCommand(channelsPage);
        const posts = new MeetPosts(channelsPage);

        // # Run /meet config with no setting and with no value
        await slashCommand.run('config');
        await slashCommand.run('config', 'authuser');

        // * Verify both show the usage
        await expect(posts.postsWithText('Usage: /meet config authuser <number>.')).toHaveCount(2);

        // # Run /meet config authuser with a negative and a non-numeric value
        await slashCommand.run('config', 'authuser', '-1');
        await slashCommand.run('config', 'authuser', 'work');

        // * Verify both are rejected
        await expect(posts.postsWithText('The authuser value must be a non-negative whole number.')).toHaveCount(2);

        // # Run /meet config with an unknown setting
        await slashCommand.run('config', 'theme', 'dark');

        // * Verify the plugin names the only supported setting
        await expect(posts.unknownConfigSetting('theme')).toBeVisible();
    });
});
