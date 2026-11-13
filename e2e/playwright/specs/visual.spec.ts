// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Screenshot baselines of what the plugin renders: the meeting post, the conference post, the
// ended meeting's summary and the artifact replies in its thread. Run with `npm run test:visual`;
// after an intended UI change, run `npm run test:visual-update-snapshots` and review the PNGs in
// visual.spec.ts-snapshots/. Meeting codes are random, so the meeting URL is masked, and so is the
// summary's date.

import type {ChannelsPage, PlaywrightExtended} from '@mattermost/playwright-lib';

import {fakeGoogle} from '../lib/fake_google';
import {connectUser} from '../lib/kv';
import {executeCommand, postsOfType, runPollCycle, subscribe} from '../lib/meet_api';
import {ensureMeetPlugin} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';
import {routeExternalRequests, settle} from '../lib/visual';
import {MeetPosts} from '../pages/meet_posts';
import {waitForMeetWebapp} from '../pages/meet_webapp';

test.describe('visual', {tag: '@visual'}, () => {
    test.skip(!process.env.PW_VISUAL_BROWSER_WS, 'Baselines only match the dockerized browser (npm run test:visual).');

    /** Logs the user in and opens the channel, with every request that leaves the server blocked. */
    async function openChannel(pw: PlaywrightExtended, user: Parameters<PlaywrightExtended['testBrowser']['login']>[0], teamName: string, channelName: string): Promise<ChannelsPage> {
        const {page, channelsPage} = await pw.testBrowser.login(user);
        await routeExternalRequests(page);
        await channelsPage.goto(teamName, channelName);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        return channelsPage;
    }

    /**
     * @objective Verify the meeting post of /meet start renders like the baseline
     *
     * @precondition
     * Run through `npm run test:visual`, which provides the dockerized browser the baselines come from; the user's Google token is seeded directly into the plugin KV store
     */
    test('matches the baseline for the meeting post', {tag: '@google_meet'}, async ({pw}) => {
        // # Start a meeting with a topic in Town Square, then open it
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const townSquare = await userClient.getChannelByName(team.id, 'town-square');
        await executeCommand(userClient, townSquare.id, '/meet start Weekly sync');
        const channelsPage = await openChannel(pw, user, team.name, 'town-square');
        const posts = new MeetPosts(channelsPage);
        const card = posts.card(posts.meetingPost);
        await settle(pw, card);

        // * Verify the meeting card matches the baseline
        await expect(card).toHaveScreenshot('meeting-post.png', {mask: [posts.meetingUrlLink()]});
    });

    /**
     * @objective Verify the conference post, the ended meeting's summary with artifact links, and the artifact replies in its thread render like the baselines
     *
     * @precondition
     * Run through `npm run test:visual`, which provides the dockerized browser the baselines come from; the user's Google token is seeded directly into the plugin KV store, the conference and artifacts come from the fake's control API, and poll cycles are triggered with POST /admin/poll-now
     */
    test('matches the baselines for conference and artifact posts', {tag: '@google_meet'}, async ({pw}) => {
        // # Subscribe a channel to a space, start a conference, run a poll cycle and open the channel
        const {adminClient, user, userClient, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const space = await fakeGoogle.createSpace();
        const channel = await userClient.createChannel({team_id: team.id, name: 'standups', display_name: 'Standups', type: 'P'} as any);
        await subscribe(userClient, channel.id, space.meetingCode, 'Team standup');
        const conference = await fakeGoogle.startConference(space.name);
        await runPollCycle(adminClient);
        const channelsPage = await openChannel(pw, user, team.name, channel.name);
        const posts = new MeetPosts(channelsPage);
        await settle(pw, posts.conferencePost);

        // * Verify the conference post matches the baseline
        await expect(posts.conferencePost).toHaveScreenshot('conference-post.png', {mask: [posts.meetingUrlLink(posts.conferencePost)]});

        // # Add a recording, a transcript and smart notes, end the conference two seconds after the post was created (a fixed meeting length) and run a poll cycle
        const [meetingPost] = await postsOfType(userClient, channel.id, 'custom_gmeet_conference');
        await fakeGoogle.addArtifact(conference, 'recording');
        await fakeGoogle.addArtifact(conference, 'transcript', {entries: [{text: 'Welcome, everyone.'}, {text: 'First item: the roadmap.'}]});
        await fakeGoogle.addArtifact(conference, 'smartNote');
        await fakeGoogle.endConferenceAt(conference, meetingPost.create_at + 2000);
        await runPollCycle(adminClient);
        await expect(posts.summaryLink('Smart notes link')).toBeVisible();
        await settle(pw, posts.endedMeetingPost);

        // * Verify the ended meeting's summary matches the baseline
        await expect(posts.endedMeetingPost).toHaveScreenshot('meeting-ended-summary.png', {mask: [posts.summaryDate()]});

        // # Open the meeting's thread
        await (await channelsPage.centerView.getPostById(meetingPost.id)).openAThread();
        const rhs = channelsPage.sidebarRight;
        await rhs.toBeVisible();
        await rhs.toContainText('Smart notes are now available.');
        await settle(pw, rhs.container);

        // * Verify the thread with the three artifact replies matches the baseline
        await expect(rhs.container).toHaveScreenshot('artifact-thread.png', {mask: [posts.summaryDate(rhs.container)]});
    });
});
