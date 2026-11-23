// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {fakeGoogle} from '../lib/fake_google';
import {connectUser} from '../lib/kv';
import {commandReply, createOwnChannel} from '../lib/meet_api';
import {ensureMeetPlugin} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';

// Replies from server/command/command.go and the errors from server/subscription.go they wrap.
const subscribed = (code: string) =>
    `This channel is now subscribed to Google Meet space **${code}**. Recordings, transcripts, and smart notes will be posted here after each meeting.`;
const NO_SUBSCRIPTIONS = 'You have no active Google Meet subscriptions. Use `/meet subscription add <meeting-code>` to subscribe.';

test.describe('channel subscriptions', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that a channel admin can subscribe a channel to a Meet space by URL or code, that re-adding it is harmless, and that list and remove reflect it
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in), and the space is created in the fake as if the user had made it in Google Meet
     */
    test('adds, lists and removes a subscription', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user, and create a space and a channel the user administers
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const space = await fakeGoogle.createSpace();
        const channel = await createOwnChannel(userClient, team.id);

        // # Subscribe the channel by the space's meeting URL, with a description
        const added = await commandReply(userClient, channel.id, `/meet subscription add ${space.meetingUri} Team standup`);

        // * Verify the channel is subscribed to the space's meeting code
        expect(added).toBe(subscribed(space.meetingCode));

        // # Subscribe the same channel again, by the bare code
        const again = await commandReply(userClient, channel.id, `/meet subscription add ${space.meetingCode}`);

        // * Verify it succeeds without a second subscription
        expect(again).toBe(subscribed(space.meetingCode));

        // # List the user's subscriptions
        const listed = await commandReply(userClient, channel.id, '/meet subscription list');

        // * Verify the one subscription is listed with its meeting link, channel and description
        expect(listed.split('\n')).toEqual([
            'Your active Google Meet subscriptions:',
            '',
            '| Meeting | Channel | Description |',
            '|:--------|:--------|:------------|',
            `| [${space.meetingCode}](https://meet.google.com/${space.meetingCode}) | ~${channel.name} | Team standup |`,
        ]);

        // # Remove the subscription by its code
        const removed = await commandReply(userClient, channel.id, `/meet subscription remove ${space.meetingCode}`);

        // * Verify it is removed and no longer listed
        expect(removed).toBe(`Unsubscribed from **${space.meetingCode}**.`);
        expect(await commandReply(userClient, channel.id, '/meet subscription list')).toBe(NO_SUBSCRIPTIONS);

        // # Remove it again
        const removedAgain = await commandReply(userClient, channel.id, `/meet subscription remove ${space.meetingCode}`);

        // * Verify there is nothing to remove
        expect(removedAgain).toBe(`Failed to remove subscription: no subscription to "${space.meetingCode}" found in this channel`);
    });

    /**
     * @objective Verify that subscribing to a space Google doesn't know, or to a space another channel is already subscribed to, is refused
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in), and the space is created in the fake as if the user had made it in Google Meet
     */
    test('refuses unknown spaces and spaces subscribed in another channel', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user, and create a space and two channels the user administers
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const space = await fakeGoogle.createSpace();
        const first = await createOwnChannel(userClient, team.id);
        const second = await createOwnChannel(userClient, team.id);

        // # Subscribe to a meeting code Google has no space for
        const unknown = await commandReply(userClient, first.id, '/meet subscription add zzz-zzzz-zzz');

        // * Verify the space is not found
        expect(unknown).toMatch(/^Failed to subscribe: could not find meeting space "zzz-zzzz-zzz": /);

        // # Subscribe the first channel, then the second, to the same space
        expect(await commandReply(userClient, first.id, `/meet subscription add ${space.meetingCode}`)).toBe(subscribed(space.meetingCode));
        const elsewhere = await commandReply(userClient, second.id, `/meet subscription add ${space.meetingCode}`);

        // * Verify the second channel is refused, and removing it there finds nothing
        expect(elsewhere).toBe(`Failed to subscribe: meeting space "${space.meetingCode}" is already subscribed to another channel`);
        expect(await commandReply(userClient, second.id, `/meet subscription remove ${space.meetingCode}`)).toBe(
            `Failed to remove subscription: no subscription to "${space.meetingCode}" found in this channel`,
        );
    });

    /**
     * @objective Verify that only channel or system admins can manage subscriptions
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('requires channel admin permissions', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and connect a regular user, who is a member but not an admin of Town Square
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const space = await fakeGoogle.createSpace();
        const townSquare = await userClient.getChannelByName(team.id, 'town-square');

        // # Subscribe Town Square as the user
        const refused = await commandReply(userClient, townSquare.id, `/meet subscription add ${space.meetingCode}`);

        // * Verify the user is told to ask an admin, and Google is not asked for the space
        expect(refused).toBe(
            'Managing Google Meet subscriptions requires channel admin or system admin permissions. Please ask a channel or system administrator to run this command for you.',
        );
        expect((await fakeGoogle.requests()).filter((r) => r.methodId === 'meet.spaces.get')).toEqual([]);
    });

    /**
     * @objective Verify that subscriptions can't be added to direct or group messages
     *
     * @precondition
     * The admin's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('refuses direct and group messages', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect the system admin, and open a DM and a group message
        const {adminClient, adminUser, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(adminUser.id);
        const space = await fakeGoogle.createSpace();
        const otherUser = await pw.createNewUserProfile(adminClient, {prefix: 'gmeet'});
        const dm = await adminClient.createDirectChannel([adminUser.id, user.id]);
        const gm = await adminClient.createGroupChannel([adminUser.id, user.id, otherUser.id]);

        for (const channel of [dm, gm]) {
            // # Subscribe the conversation as the system admin
            const refused = await commandReply(adminClient, channel.id, `/meet subscription add ${space.meetingCode}`, team.id);

            // * Verify it is refused
            expect(refused).toBe('Subscriptions can only be added to public or private channels, not direct messages or group chats.');
        }
    });

    /**
     * @objective Verify that subscription commands are refused while conference artifact posts are turned off
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('refuses subscriptions when artifact posts are off', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin with Post Recordings, Transcripts and Smart Notes off, and connect a regular user
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, {enableConferenceArtifactPosts: false});
        await connectUser(user.id);
        const space = await fakeGoogle.createSpace();
        const channel = await createOwnChannel(userClient, team.id);

        // # Run add and list
        const add = await commandReply(userClient, channel.id, `/meet subscription add ${space.meetingCode}`);
        const list = await commandReply(userClient, channel.id, '/meet subscription list');

        // * Verify both point to the setting
        const disabled =
            'Channel subscriptions are disabled. Ask your system administrator to enable **Post Recordings, Transcripts and Smart Notes** in the plugin settings.';
        expect(add).toBe(disabled);
        expect(list).toBe(disabled);
    });
});
