// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Direct access to the plugin's rows in PluginKeyValueStore. Mattermost has no REST or mmctl path
// to write another plugin's KV, so tests seed it in Postgres, as mattermost-server's
// specs/functional/system_console/abac/masking/masking_db_setup.ts does. Adapted from
// mattermost-plugin-google-drive e2e/playwright/lib/kv.ts.

import {Client} from 'pg';

import {decrypt, encrypt} from './crypto';
import {fakeGoogle, FakeGoogleUserKey, FakeToken} from './fake_google';
import {PLUGIN_ID, TEST_ENCRYPTION_KEY} from './meet_plugin';
import {assertLocalTargets} from './safety';
import {databaseURL} from './server';

async function withDb<T>(fn: (db: Client) => Promise<T>): Promise<T> {
    assertLocalTargets();
    const db = new Client({connectionString: databaseURL()});
    await db.connect();
    try {
        return await fn(db);
    } finally {
        await db.end();
    }
}

export async function kvGet(key: string): Promise<Buffer | null> {
    return withDb(async (db) => {
        const {rows} = await db.query('SELECT pvalue FROM pluginkeyvaluestore WHERE pluginid = $1 AND pkey = $2', [PLUGIN_ID, key]);
        return rows.length ? (rows[0].pvalue as Buffer) : null;
    });
}

export async function kvSet(key: string, value: Buffer) {
    await withDb((db) =>
        db.query(
            `INSERT INTO pluginkeyvaluestore (pluginid, pkey, pvalue, expireat) VALUES ($1, $2, $3, 0)
             ON CONFLICT (pluginid, pkey) DO UPDATE SET pvalue = EXCLUDED.pvalue, expireat = 0`,
            [PLUGIN_ID, key, value],
        ),
    );
}

/** The KV key of a user's Google token (server/store/kvstore/oauth.go oauthTokenPrefix). */
export const tokenKey = (mmUserId: string) => `oauth_token_${mmUserId}`;

/** The token as the plugin stores it (server/store/kvstore/kvstore.go OAuth2Token). */
export type StoredToken = {access_token: string; token_type: string; refresh_token: string; expiry: string};

/**
 * Connects the Mattermost user to a fake Google account without the OAuth flow: the fake issues a
 * token pair, and it is stored exactly as the plugin's OAuth callback would store it
 * (server/api.go handleOAuthCallback). Returns the fake's token.
 */
export async function connectUser(
    mmUserId: string,
    options: {user?: FakeGoogleUserKey; scopes?: string[]; expiresInSeconds?: number} = {},
): Promise<FakeToken> {
    const token = await fakeGoogle.issueToken(options);
    await seedToken(mmUserId, {access_token: token.access_token, token_type: token.token_type, refresh_token: token.refresh_token, expiry: token.expiry});
    return token;
}

export async function seedToken(mmUserId: string, token: StoredToken) {
    await kvSet(tokenKey(mmUserId), encrypt(TEST_ENCRYPTION_KEY, JSON.stringify(token)));
}

/** The user's stored token, or null when the user is not connected. */
export async function storedToken(mmUserId: string): Promise<StoredToken | null> {
    const value = await kvGet(tokenKey(mmUserId));
    return value ? (JSON.parse(decrypt(TEST_ENCRYPTION_KEY, value)) as StoredToken) : null;
}

export async function isConnected(mmUserId: string): Promise<boolean> {
    return (await kvGet(tokenKey(mmUserId))) !== null;
}

// Subscriptions and conference post states are stored as plain JSON (server/store/kvstore/subscription.go),
// keyed by url.PathEscape of the Google resource name.

/** The KV key of a channel subscription to a Meet space (subscriptionKey). */
export const subscriptionKey = (spaceName: string) => `subscription_${encodeURIComponent(spaceName)}`;

/** The KV key of the plugin's state for one conference record (conferencePostKey). */
export const conferencePostKey = (conferenceName: string) => `conference_post_${encodeURIComponent(conferenceName)}`;

/** A subscription as the plugin stores it (server/store/kvstore/kvstore.go Subscription), with the fields tests use. */
export type StoredSubscription = {
    space_id: string;
    meeting_code: string;
    channel_id: string;
    last_seen_conference_start: string;
    last_recurring_match_at?: string;
    pending_conferences?: Array<{conference_name: string; conference_start: string; first_seen_at: string}>;
    scheduled_announcements?: Array<{conference_name: string; event_instance_id: string; due_at: string}>;
    [field: string]: unknown;
};

/** The plugin's state for one conference record (ConferencePostState). */
export type StoredConferencePostState = {meeting_post_id: string; channel_id: string; suppressed?: boolean};

async function kvGetJSON<T>(key: string): Promise<T | null> {
    const value = await kvGet(key);
    return value ? (JSON.parse(value.toString('utf8')) as T) : null;
}

export const storedSubscription = (spaceName: string) => kvGetJSON<StoredSubscription>(subscriptionKey(spaceName));

/** Changes a stored subscription in place, e.g. to backdate its conference watermark. */
export async function updateSubscription(spaceName: string, change: (subscription: StoredSubscription) => void) {
    const subscription = await storedSubscription(spaceName);
    if (!subscription) {
        throw new Error(`No subscription stored for ${spaceName}`);
    }
    change(subscription);
    await kvSet(subscriptionKey(spaceName), Buffer.from(JSON.stringify(subscription)));
}

/** The plugin's state for a conference record, or null when the poller has not decided on it yet. */
export const conferencePostState = (conferenceName: string) => kvGetJSON<StoredConferencePostState>(conferencePostKey(conferenceName));

/**
 * Deletes the plugin's KV rows: tokens, OAuth states, subscriptions, ad-hoc meetings and posted
 * conferences. The plugin keeps ad-hoc meetings for 24 hours and polls each one, so without this a
 * test's poll cycle would also poll every earlier test's meetings. The cluster mutex rows (mutex_*)
 * are kept, so a poll cycle still running is not unlocked under it.
 */
export async function clearPluginKV() {
    await withDb((db) => db.query("DELETE FROM pluginkeyvaluestore WHERE pluginid = $1 AND pkey NOT LIKE 'mutex\\_%'", [PLUGIN_ID]));
}
