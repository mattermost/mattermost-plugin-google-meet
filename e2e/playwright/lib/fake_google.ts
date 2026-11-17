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

export type FakeArtifactKind = 'recording' | 'transcript' | 'smartNote';

/** A recording, transcript or smart note. `exportUri` is the Drive or Docs link Google returns. */
export type FakeArtifact = {name: string; state: 'STARTED' | 'ENDED' | 'FILE_GENERATED'; startTime: string; endTime?: string; resource: string; exportUri: string};

/**
 * A transcript entry. `speaker` is the participant ID (default p1); the fake creates that
 * participant the first time it speaks, as a `speakerKind` (default signedinUser) named
 * `speakerName` (default "Participant <id>").
 */
export type FakeTranscriptEntry = {text: string; speaker?: string; speakerName?: string; speakerKind?: 'signedinUser' | 'anonymousUser' | 'phoneUser'};

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

    /**
     * A conference in `space` (a space name or meeting code). Offsets are seconds from now, so a test
     * backdates a conference instead of waiting; leave `endOffsetSeconds` out for one still running.
     */
    startConference: (space: string, times: {startOffsetSeconds?: number; endOffsetSeconds?: number} = {}) =>
        control<FakeConference>('POST', 'conferences', {space, ...times}),
    /** Ends a conference, `endOffsetSeconds` from now (default: now). */
    endConference: (conference: FakeConference, endOffsetSeconds = 0) =>
        control<FakeConference>('PATCH', `conferences/${conference.name.split('/')[1]}`, {endOffsetSeconds}),

    /**
     * Adds an artifact to a conference, by default already generated (FILE_GENERATED). Passing the
     * `name` of an existing one changes its state instead. Transcript entries are five seconds apart
     * from the conference start.
     */
    addArtifact: (
        conference: FakeConference,
        kind: FakeArtifactKind,
        options: {state?: FakeArtifact['state']; name?: string; entries?: FakeTranscriptEntry[]} = {},
    ) => control<FakeArtifact>('POST', 'artifacts', {conference: conference.name, kind, ...options}),

    requests: async () => (await control<{value: FakeRequest[]}>('GET', 'requests')).value,

    /** Everything recorded so far, including `late`: violations deferred past the test that caused them. */
    violations: () => control<{value: string[]; late: string[]; lastChecked: string}>('GET', 'violations'),
    /** Returns and clears the current violations in one step; `test` is what later late violations are labelled after. */
    takeViolations: (test: string) => control<{value: string[]}>('POST', 'violations/take', {test}),
    /** Moves the current violations to `late`, labelled with the last checked test, which global teardown fails the run on. */
    deferViolations: () => control<{value: string[]}>('POST', 'violations/defer'),
};

/**
 * Resolves once the fake has seen no new request for `quietMs`. Throws if requests keep coming for
 * `timeoutMs`, so a caller never acts on a request log that is still growing.
 */
export async function waitUntilQuiet(quietMs = 1_000, timeoutMs = 15_000) {
    const deadline = Date.now() + timeoutMs;
    let seen = (await fakeGoogle.requests()).length;
    while (Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, quietMs));
        const now = (await fakeGoogle.requests()).length;
        if (now === seen) {
            return;
        }
        seen = now;
    }
    throw new Error(`Fake Google was still receiving requests after ${timeoutMs / 1000}s.`);
}
