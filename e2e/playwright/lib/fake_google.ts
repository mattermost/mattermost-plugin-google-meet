// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Talks to fake-google/'s /__control API. The fake runs in the compose stack (docker-compose.yml).
// Mattermost reaches the fake through the fake's HTTPS proxy, which tunnels Google's real hostnames
// to it, over TLS with a throwaway CA that Go trusts through SSL_CERT_FILE (fake-google/gen-certs.sh).
// No plugin code changes are needed.

import {fakeGoogleURL} from './server';

async function control<T>(method: string, route: string, body?: unknown): Promise<T> {
    const response = await fetch(`${fakeGoogleURL()}/__control/${route}`, {
        method,
        headers: {'content-type': 'application/json'},
        body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) {
        throw new Error(`Fake Google ${method} ${route} failed: ${response.status} ${await response.text()}`);
    }
    return (response.status === 204 ? undefined : await response.json()) as T;
}

export type FakeGoogleUserKey = 'owner' | 'guest';

export type FakeGoogleAccounts = {
    clientId: string;
    clientSecret: string;
    scopes: {created: string; readonly: string; calendar: string};
    users: Array<{key: FakeGoogleUserKey; email: string; displayName: string}>;
};

export type FakeToken = {access_token: string; refresh_token: string; token_type: string; expiry: string; scope: string};

export type FakeRequest = {method: string; host: string; path: string; query: string; status: number; user?: string; methodId?: string};

export type FakeSpace = {name: string; meetingCode: string; meetingUri: string; owner: FakeGoogleUserKey};

export type FakeConference = {name: string; space: string; startTime: string; endTime?: string};

export type FakeCalendarEvent = {id: string; status: string; summary: string; start: {dateTime?: string; date?: string}; end: {dateTime?: string; date?: string}; conferenceId?: string; recurringEventId?: string};

/** Offsets are seconds from now, so tests can backdate instead of waiting. */
export type FakeCalendarEventOptions = {
    user?: FakeGoogleUserKey;
    summary?: string;
    startOffsetSeconds: number;
    endOffsetSeconds: number;
    conferenceId?: string;
    /** Set on every occurrence of a recurring event; create one event per occurrence with the same value. */
    recurringEventId?: string;
    status?: 'confirmed' | 'cancelled';
    allDay?: boolean;
};

export const fakeGoogle = {
    /** Clears everything. `keepViolations` keeps the violation lists (the per-test reset in lib/test.ts). */
    reset: (keepViolations = false) => control('POST', 'reset', {keepViolations}),
    accounts: () => control<FakeGoogleAccounts>('GET', 'accounts'),

    /** A token pair the fake accepts, as if `user` had connected with `scopes`. */
    issueToken: (options: {user?: FakeGoogleUserKey; scopes?: string[]; expiresInSeconds?: number} = {}) =>
        control<FakeToken>('POST', 'tokens', options),
    /** Revokes a token pair, as if the user removed the app's access in their Google account. */
    revokeToken: (token: {access_token?: string; refresh_token?: string}) => control('POST', 'tokens/revoke', token),

    /** The next call to `methodId` (e.g. meet.spaces.create) fails with `status`. */
    failNext: (methodId: string, status: number, reason?: string) => control('POST', 'fail', {methodId, status, reason}),

    spaces: async () => (await control<{value: FakeSpace[]}>('GET', 'spaces')).value,
    /** A space the user created outside Mattermost, e.g. to subscribe a channel to. */
    createSpace: (user: FakeGoogleUserKey = 'owner') => control<FakeSpace>('POST', 'spaces', {user}),
    /** A conference in `space` (its name or meeting code). Offsets are seconds from now; no end offset means still running. */
    createConference: (options: {space: string; startOffsetSeconds: number; endOffsetSeconds?: number}) =>
        control<FakeConference>('POST', 'conferences', options),
    /** An event on the user's primary calendar. */
    createCalendarEvent: (options: FakeCalendarEventOptions) => control<FakeCalendarEvent>('POST', 'events', options),
    requests: async () => (await control<{value: FakeRequest[]}>('GET', 'requests')).value,

    /** Everything recorded so far, including `late`: violations deferred past the test that caused them. */
    violations: () => control<{value: string[]; late: string[]; lastChecked: string}>('GET', 'violations'),
    /** Returns and clears the current violations in one step; `test` is what later late violations are labelled after. */
    takeViolations: (test: string) => control<{value: string[]}>('POST', 'violations/take', {test}),
    /** Moves the current violations to `late`, labelled with the last checked test, which global teardown fails the run on. */
    deferViolations: () => control<{value: string[]}>('POST', 'violations/defer'),
};
