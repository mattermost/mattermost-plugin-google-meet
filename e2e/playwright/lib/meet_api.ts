// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// The plugin's own HTTP routes (server/api.go) and slash command execution, called as a given user.

import {Client4} from '@mattermost/client';
import type {Post} from '@mattermost/types/posts';

import {fakeGoogle, waitUntilQuiet} from './fake_google';
import {PLUGIN_ID} from './meet_plugin';

export const pluginApiPath = `/plugins/${PLUGIN_ID}/api/v1`;

/** A Client4 logged in as `user`, for calls a test makes on the user's behalf. */
export async function clientFor(user: {username: string; password: string}): Promise<Client4> {
    const client = new Client4();
    client.setUrl(process.env.PW_BASE_URL as string);
    await client.login(user.username, user.password);
    return client;
}

/** Sends a request to the plugin's API with the client's session. */
export async function pluginRequest(client: Client4, method: 'GET' | 'POST', route: string, body?: unknown): Promise<{status: number; json: any}> {
    const response = await fetch(`${client.getUrl()}${pluginApiPath}${route}`, {
        method,
        headers: {
            Authorization: `Bearer ${client.getToken()}`,
            'X-Requested-With': 'XMLHttpRequest',
            ...(body === undefined ? {} : {'Content-Type': 'application/json'}),
        },
        body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    let json: unknown = text;
    try {
        json = JSON.parse(text);
    } catch {
        // Plain-text error bodies (http.Error) stay as text.
    }
    return {status: response.status, json};
}

/**
 * Runs a slash command as the client's user, e.g. in a thread when `rootId` is set. Commands are
 * registered per team, so a DM or GM needs the team to run it in.
 */
export async function executeCommand(client: Client4, channelId: string, command: string, rootId = '', teamId = '') {
    return client.executeCommand(command, {channel_id: channelId, root_id: rootId, team_id: teamId} as any);
}

/** Runs a /meet command as the client's user and returns the plugin's ephemeral reply. */
export async function commandReply(client: Client4, channelId: string, command: string, teamId = ''): Promise<string> {
    return ((await executeCommand(client, channelId, command, '', teamId)) as {text: string}).text;
}

/** A private channel created by the client's user, which makes them its channel admin. */
export async function createOwnChannel(client: Client4, teamId: string) {
    const suffix = Math.random().toString(36).slice(2, 8);
    return client.createChannel({team_id: teamId, name: `gmeet-sub-${suffix}`, display_name: `Meet sub ${suffix}`, type: 'P'} as any);
}

/** Subscribes the channel to a space with /meet subscription add, failing on any other reply. */
export async function subscribe(client: Client4, channelId: string, meetingCode: string, description = '') {
    const text = await commandReply(client, channelId, `/meet subscription add ${meetingCode} ${description}`.trim());
    if (!text.startsWith('This channel is now subscribed')) {
        throw new Error(`Subscribing to ${meetingCode} failed: ${text}`);
    }
}

/** The newest posts in the channel, newest first. */
export async function recentPosts(client: Client4, channelId: string): Promise<Post[]> {
    const list = await client.getPosts(channelId, 0, 20);
    return list.order.map((id) => list.posts[id]);
}

/** The replies in a thread, oldest first. */
export async function threadReplies(client: Client4, rootId: string): Promise<Post[]> {
    const thread = await client.getPostThread(rootId);
    return thread.order.map((id) => thread.posts[id]).filter((p) => p.id !== rootId).sort((a, b) => a.create_at - b.create_at);
}

/** An uploaded file's content, as text. */
export async function fileText(client: Client4, fileId: string): Promise<string> {
    const response = await fetch(`${client.getUrl()}/api/v4/files/${fileId}`, {headers: {Authorization: `Bearer ${client.getToken()}`}});
    if (!response.ok) {
        throw new Error(`Downloading file ${fileId} failed: ${response.status}`);
    }
    return response.text();
}

/** The channel's posts of one type, oldest first. */
export async function postsOfType(client: Client4, channelId: string, type: string): Promise<Post[]> {
    return (await recentPosts(client, channelId)).filter((p) => p.type === type).reverse();
}

/** Creates a private channel in `teamId` with the given members. */
export async function createPrivateChannel(adminClient: Client4, teamId: string, memberIds: string[]) {
    const suffix = Math.random().toString(36).slice(2, 8);
    const channel = await adminClient.createChannel({team_id: teamId, name: `gmeet-e2e-${suffix}`, display_name: `Meet e2e ${suffix}`, type: 'P'} as any);
    for (const userId of memberIds) {
        await adminClient.addToChannel(userId, channel.id);
    }
    return channel;
}

/**
 * Runs one poll cycle and waits for it to finish. POST /admin/poll-now starts the cycle on a
 * goroutine (server/api.go), so this waits for the cycle's first Google call and then until the
 * fake has been quiet for a second. Needs something to poll: a subscription or an ad-hoc meeting.
 */
export async function runPollCycle(adminClient: Client4) {
    const before = (await fakeGoogle.requests()).length;
    const {status, json} = await pluginRequest(adminClient, 'POST', '/admin/poll-now');
    if (status !== 200 || json?.status !== 'poll triggered') {
        throw new Error(`poll-now did not start a cycle: ${status} ${JSON.stringify(json)}`);
    }
    const deadline = Date.now() + 10_000;
    while ((await fakeGoogle.requests()).length === before) {
        if (Date.now() > deadline) {
            throw new Error('The poll cycle made no Google call within 10s. Is there a subscription or an ad-hoc meeting to poll?');
        }
        await new Promise((resolve) => setTimeout(resolve, 100));
    }
    await waitUntilQuiet();
}
