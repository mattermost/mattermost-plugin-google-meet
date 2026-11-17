// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import type {Client4} from '@mattermost/client';

import {fakeGoogle} from '../lib/fake_google';
import {conferencePostState, connectUser, storedSubscription, updateSubscription} from '../lib/kv';
import {clientFor, executeCommand, pluginRequest, recentPosts} from '../lib/meet_api';
import {SCOPES} from '../lib/meet_oauth';
import {ensureMeetPlugin} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';

const CONFERENCE_POST_TYPE = 'custom_gmeet_conference';
const HOUR = 3600;

type Setup = Awaited<ReturnType<typeof subscribeChannel>>;

/**
 * Enables calendar sync with the cooldown off (so only the calendar rule can stop a post),
 * connects a user with `scopes`, and subscribes Town Square to a new Meet space. The
 * subscription's conference watermark is moved back two days so tests can backdate conferences.
 */
async function subscribeChannel(pw: any, scopes = [SCOPES.created, SCOPES.readonly, SCOPES.calendar]) {
    const {adminClient, user, team} = await pw.initSetup();
    await ensureMeetPlugin(adminClient, {enableCalendarScheduleSync: true, conferenceStartCooldownHours: 0});
    await connectUser(user.id, {scopes});
    const space = await fakeGoogle.createSpace();
    const client = await clientFor(user);
    const channel = await client.getChannelByName(team.id, 'town-square');
    await adminClient.updateChannelMemberSchemeRoles(channel.id, user.id, true, true);
    const reply = await executeCommand(client, channel.id, `/meet subscription add ${space.meetingCode}`, '', team.id);
    expect(reply.text).toContain('now subscribed');
    await updateSubscription(space.name, (subscription) => {
        subscription.last_seen_conference_start = new Date(Date.now() - 48 * HOUR * 1000).toISOString();
    });
    return {adminClient: adminClient as Client4, channel, space};
}

/** Runs one poll cycle and waits until the plugin has stored its decision for `conferenceName`. */
async function pollUntilDecided(setup: Setup, conferenceName: string) {
    await pluginRequest(setup.adminClient, 'POST', '/admin/poll-now');
    await expect.poll(() => conferencePostState(conferenceName), {message: `the poller should decide on ${conferenceName}`}).not.toBeNull();
    return (await conferencePostState(conferenceName))!;
}

/** The plugin stores scheduled starts to the second. */
const toSeconds = (time: unknown) => Math.floor(Date.parse(time as string) / 1000);

async function conferencePosts(setup: Setup) {
    return (await recentPosts(setup.adminClient, setup.channel.id)).filter((p) => (p.type as string) === CONFERENCE_POST_TYPE);
}

/**
 * Creates an occurrence of the "PDE Meeting" series yesterday and its conference, polls, and
 * returns the announced post. This is how a subscription learns it backs a recurring meeting.
 */
async function holdYesterdaysOccurrence(setup: Setup, recurringEventId: string | undefined) {
    const occurrence = await fakeGoogle.createCalendarEvent({
        summary: 'PDE Meeting',
        startOffsetSeconds: -20 * HOUR,
        endOffsetSeconds: -19 * HOUR,
        conferenceId: setup.space.meetingCode,
        recurringEventId,
    });
    const conference = await fakeGoogle.createConference({space: setup.space.name, startOffsetSeconds: -20 * HOUR + 60, endOffsetSeconds: -20 * HOUR + 48 * 60});
    const state = await pollUntilDecided(setup, conference.name);
    expect(state.suppressed).toBeFalsy();
    return {occurrence, conference};
}

test.describe('calendar-synced subscriptions', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that once a subscription backs a recurring calendar meeting, someone opening the meeting link hours away from any occurrence posts nothing, replaying the reported misfire
     *
     * @precondition
     * Calendar sync is on and the conference start cooldown is off, so only the calendar rule can stop the post. The user's token, with the calendar scope, is seeded into the plugin KV store
     */
    test('does not announce a stray join on a recurring calendar meeting', {tag: '@google_meet'}, async ({pw}) => {
        // # Subscribe a channel and hold yesterday's occurrence of a daily recurring meeting
        const setup = await subscribeChannel(pw);
        const {occurrence} = await holdYesterdaysOccurrence(setup, 'pdeseries');
        await fakeGoogle.createCalendarEvent({summary: 'PDE Meeting', startOffsetSeconds: 4 * HOUR, endOffsetSeconds: 5 * HOUR, conferenceId: setup.space.meetingCode, recurringEventId: 'pdeseries'});

        // * Verify yesterday's occurrence was announced with its calendar title and scheduled start
        const [announced] = await conferencePosts(setup);
        expect(announced.props.meeting_topic).toBe('PDE Meeting');
        expect(toSeconds(announced.props.scheduled_start)).toBe(toSeconds(occurrence.start.dateTime));

        // * Verify the subscription now knows it backs a recurring calendar meeting
        expect((await storedSubscription(setup.space.name))?.last_recurring_match_at).toBeTruthy();

        // # Someone opens the link now, hours before today's occurrence, and leaves at once
        const stray = await fakeGoogle.createConference({space: setup.space.name, startOffsetSeconds: -5, endOffsetSeconds: 0});
        const state = await pollUntilDecided(setup, stray.name);

        // * Verify the stray conference was checked against the calendar and suppressed
        expect(state.suppressed).toBe(true);
        const calendarLookups = (await fakeGoogle.requests()).filter((r) => r.methodId === 'calendar.events.list');
        expect(calendarLookups.length).toBeGreaterThanOrEqual(2);

        // * Verify the channel still only has yesterday's announcement
        expect((await conferencePosts(setup)).map((p) => p.id)).toEqual([announced.id]);
    });

    /**
     * @objective Verify that a calendar outage on a recurring calendar meeting holds the announcement back instead of posting it, and that it is posted for the occurrence once the calendar answers
     *
     * @precondition
     * The fake Google answers the first calendar lookup for today's conference with a 503
     */
    test('retries the calendar instead of posting during an outage', {tag: '@google_meet'}, async ({pw}) => {
        // # Subscribe a channel, hold yesterday's occurrence, and start today's occurrence while the calendar is down
        const setup = await subscribeChannel(pw);
        await holdYesterdaysOccurrence(setup, 'pdeseries');
        const today = await fakeGoogle.createCalendarEvent({summary: 'PDE Meeting', startOffsetSeconds: -120, endOffsetSeconds: HOUR, conferenceId: setup.space.meetingCode, recurringEventId: 'pdeseries'});
        const conference = await fakeGoogle.createConference({space: setup.space.name, startOffsetSeconds: -60});
        await fakeGoogle.failNext('calendar.events.list', 503);

        // # Run a poll cycle
        await pluginRequest(setup.adminClient, 'POST', '/admin/poll-now');

        // * Verify the conference is held for a calendar retry and nothing new is posted
        await expect.poll(async () => (await storedSubscription(setup.space.name))?.pending_conferences?.map((c) => c.conference_name)).toEqual([conference.name]);
        expect(await conferencePostState(conference.name)).toBeNull();
        expect(await conferencePosts(setup)).toHaveLength(1);

        // # Run another poll cycle with the calendar back
        const state = await pollUntilDecided(setup, conference.name);

        // * Verify today's occurrence is announced with its scheduled start and nothing is left pending
        expect(state.suppressed).toBeFalsy();
        const posts = await conferencePosts(setup);
        expect(posts).toHaveLength(2);
        expect(toSeconds(posts[0].props.scheduled_start)).toBe(toSeconds(today.start.dateTime));
        expect((await storedSubscription(setup.space.name))?.pending_conferences ?? []).toEqual([]);
    });

    /**
     * @objective Verify that a subscription backing only a one-off calendar event keeps announcing unscheduled conferences right away
     */
    test('still announces unscheduled conferences when the meeting is not recurring', {tag: '@google_meet'}, async ({pw}) => {
        // # Subscribe a channel and hold yesterday's one-off calendar event
        const setup = await subscribeChannel(pw);
        await holdYesterdaysOccurrence(setup, undefined);

        // * Verify the subscription is not treated as a recurring calendar meeting
        expect((await storedSubscription(setup.space.name))?.last_recurring_match_at).toBeUndefined();

        // # Start an unscheduled conference now
        const unscheduled = await fakeGoogle.createConference({space: setup.space.name, startOffsetSeconds: -5});
        const state = await pollUntilDecided(setup, unscheduled.name);

        // * Verify it is announced at once
        expect(state.suppressed).toBeFalsy();
        const posts = await conferencePosts(setup);
        expect(posts).toHaveLength(2);
        expect(posts[0].props.conference_record).toBe(unscheduled.name);
    });

    /**
     * @objective Verify that the calendar-only rule needs the calendar scope: without it, a recurring meeting's unscheduled conference falls back to the cooldown heuristic and is announced
     *
     * @precondition
     * The user's seeded token lacks the calendar scope, and the subscription is marked as backing a recurring calendar meeting in the plugin KV store
     */
    test('falls back to announcing when the token lacks the calendar scope', {tag: '@google_meet'}, async ({pw}) => {
        // # Subscribe a channel with a token that cannot read the calendar, marked as a recurring calendar meeting
        const setup = await subscribeChannel(pw, [SCOPES.created, SCOPES.readonly]);
        await updateSubscription(setup.space.name, (subscription) => {
            subscription.last_recurring_match_at = new Date(Date.now() - 24 * HOUR * 1000).toISOString();
        });

        // # Start a conference and run a poll cycle
        const conference = await fakeGoogle.createConference({space: setup.space.name, startOffsetSeconds: -5});
        const state = await pollUntilDecided(setup, conference.name);

        // * Verify the calendar refused the lookup for lack of scope
        const lookups = (await fakeGoogle.requests()).filter((r) => r.methodId === 'calendar.events.list');
        expect(lookups.map((r) => r.status)).toEqual([403]);

        // * Verify the conference is announced through the fallback
        expect(state.suppressed).toBeFalsy();
        expect(await conferencePosts(setup)).toHaveLength(1);
    });
});
