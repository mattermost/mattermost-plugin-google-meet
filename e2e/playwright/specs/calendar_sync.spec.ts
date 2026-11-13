// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import type {Client4} from '@mattermost/client';

import {fakeGoogle} from '../lib/fake_google';
import {connectUser} from '../lib/kv';
import {createOwnChannel, postsOfType, recentPosts, runPollCycle, subscribe} from '../lib/meet_api';
import {SCOPES} from '../lib/meet_oauth';
import {ensureMeetPlugin} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';

const CONFERENCE_POST_TYPE = 'custom_gmeet_conference';
const ALL_SCOPES = [SCOPES.created, SCOPES.readonly, SCOPES.calendar];

/** The Calendar API's dateTime as the plugin stores it: RFC 3339 in UTC, to the second. */
const toSecond = (iso: string) => new Date(iso).toISOString().replace(/\.\d+Z$/, 'Z');

/** The plugin bot's posts in its DM with `userId`. */
async function botDMs(client: Client4, userId: string) {
    const bot = await client.getUserByUsername('google-meet');
    const dm = await client.createDirectChannel([userId, bot.id]);
    return (await recentPosts(client, dm.id)).filter((p) => p.user_id === bot.id);
}

test.describe('calendar schedule sync', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that a conference matching a calendar event is posted with the event's title and scheduled start, and that rejoining during the event reuses that post
     *
     * @precondition
     * The user's Google token, with the calendar scope, is seeded directly into the plugin KV store (no Google sign-in); the space, the calendar event and the conferences are created through the fake's control API, and poll cycles are triggered with POST /admin/poll-now
     */
    test('titles the post after the calendar event and reuses it on rejoin', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin with calendar sync, connect a regular user with the calendar scope, and subscribe a channel to a space
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, {enableCalendarScheduleSync: true});
        await connectUser(user.id, {scopes: ALL_SCOPES});
        const space = await fakeGoogle.createSpace();
        const channel = await createOwnChannel(userClient, team.id);
        await subscribe(userClient, channel.id, space.meetingCode, 'Weekly sync');

        // # Schedule an event for the space that started a minute ago, start a conference and run a poll cycle
        const event = await fakeGoogle.addEvent({summary: 'Design review', startOffsetSeconds: -60, endOffsetSeconds: 3600, conferenceId: space.meetingCode});
        const first = await fakeGoogle.startConference(space.name);
        await runPollCycle(adminClient);

        // * Verify the conference is posted with the event's title and scheduled start
        const [post] = await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE);
        expect(post.props).toMatchObject({
            conference_record: first.name,
            meeting_topic: 'Design review',
            scheduled_start: toSecond(event.start.dateTime!),
        });

        // # End the conference, start another one in the same event and run a poll cycle
        await fakeGoogle.endConference(first);
        await fakeGoogle.startConference(space.name);
        await runPollCycle(adminClient);

        // * Verify there is still one post, not marked ended while the second conference runs
        const posts = await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE);
        expect(posts.map((p) => p.id)).toEqual([post.id]);
        expect(posts[0].props.meeting_ended).toBeUndefined();
    });

    /**
     * @objective Verify that a conference started before its event's scheduled start is posted only once the start has passed, and that one which ended before the start is never posted
     *
     * @precondition
     * The user's Google token, with the calendar scope, is seeded directly into the plugin KV store (no Google sign-in); the spaces, the calendar events and the conferences are created through the fake's control API, and poll cycles are triggered with POST /admin/poll-now
     */
    test('waits for the scheduled start, and drops conferences that ended before it', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin with calendar sync, connect a regular user with the calendar scope, and subscribe a channel to two spaces
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, {enableCalendarScheduleSync: true});
        await connectUser(user.id, {scopes: ALL_SCOPES});
        const early = await fakeGoogle.createSpace();
        const left = await fakeGoogle.createSpace();
        const channel = await createOwnChannel(userClient, team.id);
        await subscribe(userClient, channel.id, early.meetingCode);
        await subscribe(userClient, channel.id, left.meetingCode);

        // # Schedule an event for each space ten seconds from now. Start a conference in each, end the second one, and run a poll cycle
        const startsIn = 10;
        const scheduledAt = Date.now() + startsIn * 1000;
        await fakeGoogle.addEvent({summary: 'Early joiners', startOffsetSeconds: startsIn, endOffsetSeconds: 3600, conferenceId: early.meetingCode});
        await fakeGoogle.addEvent({summary: 'Left early', startOffsetSeconds: startsIn, endOffsetSeconds: 3600, conferenceId: left.meetingCode});
        const waiting = await fakeGoogle.startConference(early.name);
        await fakeGoogle.startConference(left.name, {endOffsetSeconds: 0});
        await runPollCycle(adminClient);

        // * Verify nothing is posted before the scheduled start
        expect(await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE)).toEqual([]);

        // # Wait for the scheduled start and run a poll cycle
        await pw.wait(Math.max(0, scheduledAt - Date.now()) + 1000);
        await runPollCycle(adminClient);

        // * Verify only the conference that is still running is posted, with its event's title
        const posts = await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE);
        expect(posts.map((p) => [p.props.conference_record, p.props.meeting_topic])).toEqual([[waiting.name, 'Early joiners']]);
    });

    /**
     * @objective Verify that cancelled and all-day events are not matched, so the conference is posted without an event title
     *
     * @precondition
     * The user's Google token, with the calendar scope, is seeded directly into the plugin KV store (no Google sign-in); the space, the calendar events and the conference are created through the fake's control API, and poll cycles are triggered with POST /admin/poll-now
     */
    test('ignores cancelled and all-day events', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin with calendar sync, connect a regular user with the calendar scope, and subscribe a channel to a space
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, {enableCalendarScheduleSync: true});
        await connectUser(user.id, {scopes: ALL_SCOPES});
        const space = await fakeGoogle.createSpace();
        const channel = await createOwnChannel(userClient, team.id);
        await subscribe(userClient, channel.id, space.meetingCode, 'Weekly sync');

        // # Add a cancelled event and an all-day event for the space, start a conference and run a poll cycle
        await fakeGoogle.addEvent({summary: 'Cancelled review', startOffsetSeconds: -60, conferenceId: space.meetingCode, status: 'cancelled'});
        await fakeGoogle.addEvent({summary: 'Offsite day', startOffsetSeconds: -60, endOffsetSeconds: 86_400, conferenceId: space.meetingCode, allDay: true});
        const conference = await fakeGoogle.startConference(space.name);
        await runPollCycle(adminClient);

        // * Verify the calendar was checked, and the conference posted with no event title or scheduled start
        expect((await fakeGoogle.requests()).some((r) => r.methodId === 'calendar.events.list' && r.status === 200)).toBe(true);
        const [post] = await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE);
        expect(post.props.conference_record).toBe(conference.name);
        expect(post.props.description).toBe('Weekly sync');
        expect(post.props).not.toHaveProperty('meeting_topic');
        expect(post.props).not.toHaveProperty('scheduled_start');
    });

    /**
     * @objective Verify that when the subscriber's token lacks the calendar scope, the conference is still posted and the bot asks the user once to reconnect
     *
     * @precondition
     * The user's Google token, without the calendar scope, is seeded directly into the plugin KV store (no Google sign-in); the space and the conferences are created through the fake's control API, and poll cycles are triggered with POST /admin/poll-now
     */
    test('asks once to reconnect when the token lacks the calendar scope', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin with calendar sync and no cooldown, connect a regular user without the calendar scope, and subscribe a channel to a space
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, {enableCalendarScheduleSync: true, conferenceStartCooldownHours: 0});
        await connectUser(user.id, {scopes: [SCOPES.created, SCOPES.readonly]});
        const space = await fakeGoogle.createSpace();
        const channel = await createOwnChannel(userClient, team.id);
        await subscribe(userClient, channel.id, space.meetingCode);

        // # Start a conference and run a poll cycle
        await fakeGoogle.startConference(space.name, {endOffsetSeconds: 0});
        await runPollCycle(adminClient);

        // * Verify the conference is posted anyway, and the bot DMs the user to reconnect
        expect(await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE)).toHaveLength(1);
        const dms = await botDMs(userClient, user.id);
        expect(dms.map((p) => p.message)).toEqual([
            'Google Meet subscriptions are configured to sync conference-started posts with your calendar schedule, but your connected Google account is missing the Calendar scope. ' +
                'Run `/meet connect` to reconnect and grant access — until then, conferences will keep using the cooldown fallback instead of the scheduled time.',
        ]);

        // # Start a second conference and run a poll cycle
        await fakeGoogle.startConference(space.name);
        await runPollCycle(adminClient);

        // * Verify the second conference is posted and no second DM is sent
        expect(await postsOfType(userClient, channel.id, CONFERENCE_POST_TYPE)).toHaveLength(2);
        expect(await botDMs(userClient, user.id)).toHaveLength(1);
    });
});
