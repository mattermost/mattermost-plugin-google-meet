// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {fakeGoogle} from '../lib/fake_google';
import {connectUser} from '../lib/kv';
import {createOwnChannel, postsOfType, runPollCycle, subscribe} from '../lib/meet_api';
import {ensureMeetPlugin, updateMeetPluginSettings} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';
import {MeetPosts} from '../pages/meet_posts';
import {waitForMeetWebapp} from '../pages/meet_webapp';

const CONFERENCE_POST_TYPE = 'custom_gmeet_conference';

test.describe('conference posts for subscribed spaces', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that the poller posts when a conference starts in a subscribed space, ignores conferences from before the subscription, and turns the post into a meeting summary when the conference ends
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in); the space and its conferences are created through the fake's control API, and poll cycles are triggered with POST /admin/poll-now
     */
    test('posts a started conference and summarises it when it ends', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user, and subscribe a channel the user administers to a space
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const space = await fakeGoogle.createSpace();
        const channel = await createOwnChannel(userClient, team.id);
        await fakeGoogle.startConference(space.name, {startOffsetSeconds: -3600, endOffsetSeconds: -1800});
        await subscribe(userClient, channel.id, space.meetingCode, 'Team standup');

        // # Start a conference in the space and run a poll cycle
        const conference = await fakeGoogle.startConference(space.name);
        await runPollCycle(adminClient);

        // * Verify one conference post, by the bot, for the new conference only
        const [post, ...others] = await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE);
        expect(others).toEqual([]);
        expect(post.message).toBe('A new Google Meet conference has started.');
        expect(post.user_id).not.toBe(user.id);
        expect(post.props).toMatchObject({
            meeting_code: space.meetingCode,
            space_id: space.name,
            description: 'Team standup',
            conference_record: conference.name,
            conference_start: conference.startTime.replace(/\.\d+Z$/, 'Z'),
        });

        // * Verify the post shows the description as its title and a join button for the space
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name, channel.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        const posts = new MeetPosts(channelsPage);
        await expect(posts.meetingTitle(posts.conferencePost)).toHaveText('Team standup');
        await expect(posts.joinButton(posts.conferencePost)).toHaveAttribute('href', space.meetingUri);

        // # End the conference and run a poll cycle
        await fakeGoogle.endConference(conference);
        await runPollCycle(adminClient);

        // * Verify the same post now says the meeting ended, with a summary attachment and no join link
        const [ended] = await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE);
        expect(ended.id).toBe(post.id);
        expect(ended.message).toBe('The meeting has ended.');
        expect(ended.props.meeting_ended).toBe(true);
        expect(ended.props.meeting_end_time).toBeGreaterThan(post.create_at);
        expect(ended.props.attachments).toEqual([
            expect.objectContaining({title: 'Team standup', text: expect.stringMatching(/^The meeting has ended\.\n\nMeeting Summary\nDate: .+\nMeeting Length: 1 minute\(s\)$/)}),
        ]);

        // * Verify the webapp shows the summary instead of the join button
        await expect(posts.meetingSummary()).toBeVisible();
        await expect(posts.endedMeetingPost).toContainText('Meeting Length: 1 minute(s)');
        await expect(posts.joinButton(posts.endedMeetingPost)).toHaveCount(0);
    });

    /**
     * @objective Verify that a conference starting within the cooldown after the previous one ended is not posted, and that a cooldown of 0 turns this off
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in); the space and its conferences are created through the fake's control API, and poll cycles are triggered with POST /admin/poll-now
     */
    test('skips a conference that starts within the cooldown', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin (12-hour cooldown), connect a regular user and subscribe a channel to a space
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, {conferenceStartCooldownHours: 12});
        await connectUser(user.id);
        const space = await fakeGoogle.createSpace();
        const channel = await createOwnChannel(userClient, team.id);
        await subscribe(userClient, channel.id, space.meetingCode);

        // # Hold a conference that has already ended, and run a poll cycle
        const first = await fakeGoogle.startConference(space.name, {endOffsetSeconds: 0});
        await runPollCycle(adminClient);

        // * Verify it is posted
        expect((await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE)).map((p) => p.props.conference_record)).toEqual([first.name]);

        // # Start another conference in the space right away, and run a poll cycle
        await fakeGoogle.startConference(space.name);
        await runPollCycle(adminClient);

        // * Verify it is not posted
        expect(await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE)).toHaveLength(1);

        // # Turn the cooldown off, start a third conference and run a poll cycle
        await updateMeetPluginSettings(adminClient, {conferenceStartCooldownHours: 0});
        const third = await fakeGoogle.startConference(space.name);
        await runPollCycle(adminClient);

        // * Verify the third conference is posted
        expect((await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE)).map((p) => p.props.conference_record)).toEqual([first.name, third.name]);
    });
});
