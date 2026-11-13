// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import type {Client4} from '@mattermost/client';

import {fakeGoogle} from '../lib/fake_google';
import type {FakeTranscriptEntry} from '../lib/fake_google';
import {connectUser} from '../lib/kv';
import {clientFor, executeCommand, fileText, recentPosts, runPollCycle, threadReplies} from '../lib/meet_api';
import {ensureMeetPlugin} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';

const VTT_CUE = /^\d\d:\d\d:\d\d\.\d{3} --> \d\d:\d\d:\d\d\.\d{3}$/;

/** The transcript.vtt's lines after the header: each cue's timing line, then its text line. */
const cues = (...texts: string[]) => ['WEBVTT', '', ...texts.flatMap((text, i) => [...(i ? [''] : []), expect.stringMatching(VTT_CUE), text])];

/**
 * Starts a meeting with /meet start, holds a finished conference in its space with a transcript of
 * `entries`, runs a poll cycle, and returns the lines of the transcript.vtt posted in the meeting's thread.
 */
async function postTranscript(adminClient: Client4, client: Client4, channelId: string, entries: FakeTranscriptEntry[], beforePoll = async () => {}) {
    await executeCommand(client, channelId, '/meet start Planning');
    const [space] = await fakeGoogle.spaces();
    const [meetingPost] = await recentPosts(client, channelId);
    const conference = await fakeGoogle.startConference(space.name);
    await fakeGoogle.addArtifact(conference, 'transcript', {entries});
    await fakeGoogle.endConference(conference);
    await beforePoll();
    await runPollCycle(adminClient);

    const [reply] = await threadReplies(client, meetingPost.id);
    expect(reply.type).toBe('custom_gmeet_transcript');
    expect(reply.metadata.files?.[0].name).toBe('transcript.vtt');
    expect(reply.props.captions).toEqual([{file_id: reply.file_ids![0]}]);
    return (await fileText(client, reply.file_ids![0])).trimEnd().split('\n');
}

test.describe('transcript speakers', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that each line of the posted transcript.vtt names its speaker, whether they joined signed in, anonymously or by phone, and that each participant is fetched from Google only once
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in); the conference, its transcript and participants are created through the fake's control API, and the poll cycle is triggered with POST /admin/poll-now
     */
    test('names the speaker of every transcript line', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and start a meeting in Town Square
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const client = await clientFor(user);
        const channel = await client.getChannelByName(team.id, 'town-square');

        // # Hold a conference with a transcript spoken by a signed-in user, an anonymous guest and a phone participant, and run a poll cycle
        const vtt = await postTranscript(adminClient, client, channel.id, [
            {text: 'Welcome, everyone.', speaker: 'alice', speakerName: 'Alice Example'},
            {text: 'Thanks for having me.', speaker: 'guest', speakerName: 'Guest Visitor', speakerKind: 'anonymousUser'},
            {text: 'I can hear you.', speaker: 'phone', speakerName: '+1 555-***-**12', speakerKind: 'phoneUser'},
            {text: 'First item: the roadmap.', speaker: 'alice', speakerName: 'Alice Example'},
        ]);

        // * Verify every cue is prefixed with its speaker's display name
        expect(vtt).toEqual(cues(
            'Alice Example: Welcome, everyone.',
            'Guest Visitor: Thanks for having me.',
            '+1 555-***-**12: I can hear you.',
            'Alice Example: First item: the roadmap.',
        ));

        // * Verify the plugin fetched each of the three participants once
        const lookups = (await fakeGoogle.requests()).filter((r) => r.methodId === 'meet.conferenceRecords.participants.get');
        expect(lookups.map((r) => r.path.split('/').pop()).sort()).toEqual(['alice', 'guest', 'phone']);
        expect(lookups.every((r) => r.status === 200)).toBe(true);
    });

    /**
     * @objective Verify that when Google fails to return a participant, their lines are posted without a name and the rest of the transcript is unaffected
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in); the first participant lookup is made to fail with the fake's failNext
     */
    test('posts lines without a name when a participant lookup fails', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and start a meeting in Town Square
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const client = await clientFor(user);
        const channel = await client.getChannelByName(team.id, 'town-square');

        // # Hold a conference with a transcript by two speakers, make the first participant lookup fail, and run a poll cycle
        const vtt = await postTranscript(adminClient, client, channel.id, [
            {text: 'Can everyone hear me?', speaker: 'alice', speakerName: 'Alice Example'},
            {text: 'Loud and clear.', speaker: 'bob', speakerName: 'Bob Example'},
            {text: 'Great, let us start.', speaker: 'alice', speakerName: 'Alice Example'},
        ], () => fakeGoogle.failNext('meet.conferenceRecords.participants.get', 500));

        // * Verify the first speaker's lines have no name, the second speaker is still named, and the failed participant was not fetched again
        expect(vtt).toEqual(cues('Can everyone hear me?', 'Bob Example: Loud and clear.', 'Great, let us start.'));
        const lookups = (await fakeGoogle.requests()).filter((r) => r.methodId === 'meet.conferenceRecords.participants.get');
        expect(lookups.map((r) => [r.path.split('/').pop(), r.status])).toEqual([['alice', 500], ['bob', 200]]);
    });
});
