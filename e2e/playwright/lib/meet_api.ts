// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// The plugin's own HTTP routes (server/api.go) and slash command execution, called as a given user.

import {Client4} from '@mattermost/client';
import type {Post} from '@mattermost/types/posts';

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

/** The newest posts in the channel, newest first. */
export async function recentPosts(client: Client4, channelId: string): Promise<Post[]> {
    const list = await client.getPosts(channelId, 0, 20);
    return list.order.map((id) => list.posts[id]);
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
