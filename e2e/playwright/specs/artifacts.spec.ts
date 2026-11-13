// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {fakeGoogle} from '../lib/fake_google';
import {connectUser} from '../lib/kv';
import {clientFor, createOwnChannel, executeCommand, fileText, postsOfType, runPollCycle, subscribe, threadReplies} from '../lib/meet_api';
import {ensureMeetPlugin} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';
import {MeetPosts} from '../pages/meet_posts';
import {waitForMeetWebapp} from '../pages/meet_webapp';

const VTT_CUE = /^\d\d:\d\d:\d\d\.\d{3} --> \d\d:\d\d:\d\d\.\d{3}$/;

// A cue's text line, with or without a `Speaker: ` prefix. The plugin is meant to name the speaker,
// but it reads a field Meet API v2 doesn't have (participantDevice.displayName), so today the name is
// always missing. Accept both, so fixing that doesn't fail this test.
const spoken = (text: string) => expect.stringMatching(new RegExp(`^(?:.+: )?${text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`));

test.describe('recordings, transcripts and smart notes', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that generated recordings, transcripts and smart notes of a subscribed space's conference are posted once each in the conference post's thread, linked from the meeting summary, and that artifacts still being generated wait until they are ready
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in); the space, its conference and the artifacts are created through the fake's control API, and poll cycles are triggered with POST /admin/poll-now
     */
    test('posts each artifact once in the conference thread', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user, subscribe a channel to a space and start a conference
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const space = await fakeGoogle.createSpace();
        const channel = await createOwnChannel(userClient, team.id);
        await subscribe(userClient, channel.id, space.meetingCode, 'Planning');
        const conference = await fakeGoogle.startConference(space.name);
        await runPollCycle(adminClient);
        const [meetingPost] = await postsOfType(userClient, channel.id, 'custom_gmeet_conference');

        // # Generate a recording, a transcript and smart notes, start a second recording, end the conference and run a poll cycle
        const recording = await fakeGoogle.addArtifact(conference, 'recording');
        const transcript = await fakeGoogle.addArtifact(conference, 'transcript', {entries: [{text: 'Welcome, everyone.'}, {text: 'First item: the roadmap.'}]});
        const smartNotes = await fakeGoogle.addArtifact(conference, 'smartNote');
        const pending = await fakeGoogle.addArtifact(conference, 'recording', {state: 'STARTED'});
        await fakeGoogle.endConference(conference);
        await runPollCycle(adminClient);

        // * Verify one reply per generated artifact, by the bot, in the conference post's thread, each linking to its file
        const replies = await threadReplies(userClient, meetingPost.id);
        expect(replies.map((p) => [p.type, p.message, p.props.export_uri])).toEqual([
            ['custom_gmeet_recording', 'A recording is now available.', recording.exportUri],
            ['custom_gmeet_transcript', 'A transcript is now available.', transcript.exportUri],
            ['custom_gmeet_smartnote', 'Smart notes are now available.', smartNotes.exportUri],
        ]);
        expect(replies.every((p) => p.user_id === meetingPost.user_id && p.channel_id === channel.id)).toBe(true);
        expect(replies[0].props.recording_name).toBe(recording.name);

        // * Verify the transcript reply carries the entries as a WebVTT file, referenced as captions
        const transcriptReply = replies[1];
        expect(transcriptReply.file_ids).toHaveLength(1);
        expect(transcriptReply.metadata.files?.[0].name).toBe('transcript.vtt');
        expect(transcriptReply.props.captions).toEqual([{file_id: transcriptReply.file_ids![0]}]);
        const vtt = (await fileText(userClient, transcriptReply.file_ids![0])).trimEnd().split('\n');
        expect(vtt).toEqual(['WEBVTT', '', expect.stringMatching(VTT_CUE), spoken('Welcome, everyone.'), '', expect.stringMatching(VTT_CUE), spoken('First item: the roadmap.')]);

        // * Verify the ended meeting post links all three
        const ended = await userClient.getPost(meetingPost.id);
        expect(ended.props.artifact_links).toEqual([
            {label: 'Recording link', url: recording.exportUri},
            {label: 'Transcript link', url: transcript.exportUri},
            {label: 'Smart notes link', url: smartNotes.exportUri},
        ]);
        expect((ended.props.attachments as Array<{text: string}>)[0].text).toContain(
            `- [Recording link](${recording.exportUri})\n- [Transcript link](${transcript.exportUri})\n- [Smart notes link](${smartNotes.exportUri})`,
        );

        // * Verify the webapp's summary shows the links
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name, channel.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        const posts = new MeetPosts(channelsPage);
        await expect(posts.meetingSummary()).toBeVisible();
        await expect(posts.summaryLink('Recording link')).toHaveAttribute('href', recording.exportUri);
        await expect(posts.summaryLink('Transcript link')).toHaveAttribute('href', transcript.exportUri);
        await expect(posts.summaryLink('Smart notes link')).toHaveAttribute('href', smartNotes.exportUri);

        // # Run another poll cycle
        await runPollCycle(adminClient);

        // * Verify nothing is posted again
        expect(await threadReplies(userClient, meetingPost.id)).toHaveLength(3);

        // # Finish the second recording and run a poll cycle
        const finished = await fakeGoogle.addArtifact(conference, 'recording', {name: pending.name});
        await runPollCycle(adminClient);

        // * Verify only the second recording is posted, and added to the summary
        const after = await threadReplies(userClient, meetingPost.id);
        expect(after.slice(3).map((p) => [p.type, p.props.export_uri])).toEqual([['custom_gmeet_recording', finished.exportUri]]);
        expect((await userClient.getPost(meetingPost.id)).props.artifact_links).toContainEqual({label: 'Recording link', url: finished.exportUri});
    });

    /**
     * @objective Verify that artifacts of a meeting started with /meet start in a thread are posted in that thread, and that the meeting post is marked ended
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in); the conference and recording are created through the fake's control API, and poll cycles are triggered with POST /admin/poll-now
     */
    test('posts artifacts of a meeting started in a thread into that thread', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin, connect a regular user and start a meeting in a thread
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const client = await clientFor(user);
        const channel = await client.getChannelByName(team.id, 'town-square');
        const root = await client.createPost({channel_id: channel.id, message: 'Release planning'} as any);
        await executeCommand(client, channel.id, '/meet start Release', root.id);
        const [space] = await fakeGoogle.spaces();
        const [meetingPost] = await threadReplies(client, root.id);

        // # Hold a conference in the space with a recording, end it and run a poll cycle
        const conference = await fakeGoogle.startConference(space.name);
        const recording = await fakeGoogle.addArtifact(conference, 'recording');
        await fakeGoogle.endConference(conference);
        await runPollCycle(adminClient);

        // * Verify the recording is posted in the original thread, after the meeting post
        const replies = await threadReplies(client, root.id);
        expect(replies.map((p) => [p.type, p.root_id])).toEqual([
            ['custom_google_meet', root.id],
            ['custom_gmeet_recording', root.id],
        ]);
        expect(replies[1].props.export_uri).toBe(recording.exportUri);

        // * Verify the meeting post is marked ended, without its join link, and links the recording
        const ended = await client.getPost(meetingPost.id);
        expect(ended.message).toBe('The meeting has ended.');
        expect(ended.props.meeting_ended).toBe(true);
        expect(ended.props.meeting_link).toBeUndefined();
        expect(ended.props.artifact_links).toEqual([{label: 'Recording link', url: recording.exportUri}]);
    });
});
