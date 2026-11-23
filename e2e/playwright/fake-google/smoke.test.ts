// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Smoke test for the fake Google itself (part of `npm run test:contract` and `npm run check`), as in
// mattermost-plugin-google-calendar e2e/playwright/fake-google/smoke.test.ts. It starts the fake on
// free ports, drives OAuth and every Meet and Calendar route the plugin calls the way the plugin's
// Go client does (server/google.go, server/calendar.go), empty and populated, and checks that none
// of the fake's own responses, error ones included, break the contract. A fake response that drifts
// from Google's published shape fails here, before any e2e run.

import assert from 'node:assert/strict';
import {spawn, type ChildProcess} from 'node:child_process';
import http from 'node:http';
import path from 'node:path';
import {after, before, beforeEach, test} from 'node:test';

type Json = Record<string, any>;

const SCOPES = {
    created: 'https://www.googleapis.com/auth/meetings.space.created',
    readonly: 'https://www.googleapis.com/auth/meetings.space.readonly',
    calendar: 'https://www.googleapis.com/auth/calendar.events.readonly',
};
const REDIRECT_URI = 'http://localhost:8085/plugins/com.mattermost.google-meet/api/v1/oauth/callback';

let fake: ChildProcess;
let base = '';
let proxyPort = 0;

before(async () => {
    fake = spawn(process.execPath, [path.join(import.meta.dirname, 'server.ts')], {
        env: {...process.env, FAKE_HTTP_PORT: '0', FAKE_PROXY_PORT: '0', FAKE_CERT_DIR: '/nonexistent', FAKE_GOOGLE_HOSTS: ''},
        stdio: ['ignore', 'pipe', 'inherit'],
    });
    let output = '';
    await new Promise<void>((resolve, reject) => {
        fake.stdout!.on('data', (chunk: Buffer) => {
            output += chunk.toString();
            const port = /fake Google on http:\/\/0\.0\.0\.0:(\d+)/.exec(output)?.[1];
            const proxy = /fake Google proxy on http:\/\/0\.0\.0\.0:(\d+)/.exec(output)?.[1];
            if (port && proxy) {
                base = `http://127.0.0.1:${port}`;
                proxyPort = Number(proxy);
                resolve();
            }
        });
        fake.on('exit', (code) => reject(new Error(`fake exited with ${code}`)));
    });
});

after(() => {
    fake.kill();
});

beforeEach(async () => {
    await control('POST', 'reset');
});

async function control(method: string, route: string, body?: unknown): Promise<any> {
    const response = await fetch(`${base}/__control/${route}`, {method, headers: {'content-type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
    assert.ok(response.ok, `control ${method} ${route}: ${response.status}`);
    return response.status === 204 ? undefined : response.json();
}

async function violations(): Promise<string[]> {
    return (await control('GET', 'violations')).value;
}

async function google(token: string, method: string, route: string, query: Record<string, string> = {}, body?: unknown) {
    const qs = new URLSearchParams(query).toString();
    const response = await fetch(`${base}${route}${qs ? `?${qs}` : ''}`, {
        method,
        headers: {Authorization: `Bearer ${token}`, ...(body === undefined ? {} : {'content-type': 'application/json'})},
        body: body === undefined ? undefined : JSON.stringify(body),
    });
    return {status: response.status, body: await response.json() as Json};
}

/**
 * The OAuth flow as the browser and the plugin run it (server/google.go asks for offline access and
 * forced consent); returns the token response. `params` overrides the authorization parameters.
 */
async function connect(grant: string[], params: Record<string, string | null> = {}): Promise<Json> {
    const authorize = new URL(`${base}/o/oauth2/v2/auth`);
    const query: Record<string, string | null> = {
        client_id: 'fake-client-id.apps.googleusercontent.com', redirect_uri: REDIRECT_URI, response_type: 'code',
        scope: [SCOPES.created, SCOPES.readonly, SCOPES.calendar].join(' '), access_type: 'offline', prompt: 'consent', state: 'abc',
        ...params,
    };
    authorize.search = new URLSearchParams(Object.entries(query).filter((e): e is [string, string] => e[1] !== null)).toString();
    const chooser = await (await fetch(authorize)).text();
    const sid = /name="sid" value="([^"]+)"/.exec(chooser)![1];
    const form = (fields: string[][]) => fetch(`${base}/o/oauth2/consent`, {method: 'POST', body: new URLSearchParams(fields), redirect: 'manual'});
    await form([['sid', sid], ['account', 'owner']]);
    const redirect = await form([['sid', sid], ['account', 'owner'], ['decision', 'allow'], ...grant.map((s) => ['scope', s])]);
    assert.equal(redirect.status, 302);
    const callback = new URL(redirect.headers.get('location')!);
    assert.equal(callback.searchParams.get('state'), 'abc');
    const token = await fetch(`${base}/token`, {method: 'POST', body: new URLSearchParams({
        code: callback.searchParams.get('code')!, client_id: 'fake-client-id.apps.googleusercontent.com',
        client_secret: 'fake-client-secret', redirect_uri: REDIRECT_URI, grant_type: 'authorization_code',
    })});
    assert.equal(token.status, 200);
    return token.json() as Promise<Json>;
}

test('issues and refreshes tokens with the scopes the user granted', async () => {
    const token = await connect([SCOPES.created, SCOPES.readonly]);
    assert.equal(token.scope, `${SCOPES.created} ${SCOPES.readonly}`);
    assert.match(token.refresh_token, /^1\/\/fake-/);

    const refreshed = await fetch(`${base}/token`, {method: 'POST', body: new URLSearchParams({
        refresh_token: token.refresh_token, client_id: 'fake-client-id.apps.googleusercontent.com', client_secret: 'fake-client-secret', grant_type: 'refresh_token',
    })});
    const body = await refreshed.json() as Json;
    assert.equal(body.scope, token.scope);
    assert.equal(body.refresh_token, undefined);
    assert.deepEqual(await violations(), []);
});

test('issues a refresh token only for offline access, and again on a repeat grant only with forced consent', async () => {
    assert.equal((await connect([SCOPES.created], {access_type: null})).refresh_token, undefined);
    assert.equal((await connect([SCOPES.created], {access_type: 'online'})).refresh_token, undefined);
    assert.match((await connect([SCOPES.created], {prompt: null})).refresh_token, /^1\/\/fake-/);
    assert.equal((await connect([SCOPES.created], {prompt: null})).refresh_token, undefined);
    assert.match((await connect([SCOPES.created])).refresh_token, /^1\/\/fake-/);

    // The plugin needs a refresh token, so every exchange that returned none is a violation.
    const found = await violations();
    assert.equal(found.length, 3, found.join('\n'));
    assert.ok(found.every((v) => /missing refresh_token \(Google only returns one for access_type=offline/.test(v)));
});

test('revoking a grant also revokes the access tokens issued from it', async () => {
    const first = await connect([SCOPES.created]);
    const other = await connect([SCOPES.created]);
    const refreshed = await (await fetch(`${base}/token`, {method: 'POST', body: new URLSearchParams({
        refresh_token: first.refresh_token, client_id: 'fake-client-id.apps.googleusercontent.com', client_secret: 'fake-client-secret', grant_type: 'refresh_token',
    })})).json() as Json;

    await control('POST', 'tokens/revoke', {refresh_token: first.refresh_token});
    assert.equal((await google(first.access_token, 'POST', '/v2/spaces', {}, {})).status, 401);
    assert.equal((await google(refreshed.access_token, 'POST', '/v2/spaces', {}, {})).status, 401);
    assert.equal((await google(other.access_token, 'POST', '/v2/spaces', {}, {})).status, 200);
});

test('rejects page tokens that are malformed or belong to another query', async () => {
    const {access_token: token} = await connect([SCOPES.created, SCOPES.readonly]);
    const {body: space} = await google(token, 'POST', '/v2/spaces', {}, {});
    const other = (await google(token, 'POST', '/v2/spaces', {}, {})).body;
    for (let i = 0; i < 3; i++) {
        await control('POST', 'conferences', {space: space.meetingCode, startOffsetSeconds: -600 - i, endOffsetSeconds: -60});
    }
    const filter = (s: Json) => `space.name="${s.name}"`;

    const first = await google(token, 'GET', '/v2/conferenceRecords', {filter: filter(space), pageSize: '2'});
    assert.equal(first.body.conferenceRecords.length, 2);
    const second = await google(token, 'GET', '/v2/conferenceRecords', {filter: filter(space), pageSize: '2', pageToken: first.body.nextPageToken});
    assert.equal(second.body.conferenceRecords.length, 1);
    assert.equal(second.body.nextPageToken, undefined);
    assert.deepEqual(await violations(), []);

    // pageSize 0 means the default, not an endless page of nothing.
    assert.equal((await google(token, 'GET', '/v2/conferenceRecords', {filter: filter(space), pageSize: '0'})).body.conferenceRecords.length, 3);

    assert.equal((await google(token, 'GET', '/v2/conferenceRecords', {filter: filter(space), pageToken: 'garbage'})).status, 400);
    assert.equal((await google(token, 'GET', '/v2/conferenceRecords', {filter: filter(other), pageToken: first.body.nextPageToken})).status, 400);
    const found = await violations();
    assert.equal(found.length, 2, found.join('\n'));
    assert.ok(found.every((v) => /page token that does not belong/.test(v)));
});

test('answers every Meet route the plugin calls, empty and populated, within the contract', async () => {
    const {access_token: token} = await connect([SCOPES.created, SCOPES.readonly]);

    const created = await google(token, 'POST', '/v2/spaces', {}, {});
    assert.equal(created.status, 200);
    const space = created.body;
    assert.match(space.meetingUri, /^https:\/\/meet\.google\.com\/[a-z]{3}-[a-z]{4}-[a-z]{3}$/);
    assert.equal((await google(token, 'GET', `/v2/spaces/${space.meetingCode}`)).body.name, space.name);
    assert.equal((await google(token, 'GET', `/v2/${space.name}`)).body.meetingCode, space.meetingCode);

    // The poller's filter (server/google.go listConferenceRecords), before any conference.
    const filter = (since: string) => ({filter: `space.name="${space.name}" AND start_time>="${since}"`});
    const since = new Date(Date.now() - 60_000).toISOString().replace('Z', '123456Z');
    assert.deepEqual((await google(token, 'GET', '/v2/conferenceRecords', filter(since))).body, {});

    // A finished conference with one artifact of each kind, a transcript with entries, and one still in progress.
    const record = await control('POST', 'conferences', {space: space.meetingCode, startOffsetSeconds: -600, endOffsetSeconds: -60});
    await control('POST', 'artifacts', {conference: record.name, kind: 'recording'});
    const transcript = await control('POST', 'artifacts', {conference: record.name, kind: 'transcript', entries: [
        {text: 'Hello'},
        {text: 'Hi', speaker: 'p2', speakerName: 'Guest', speakerKind: 'anonymousUser'},
        {text: 'Hey', speaker: 'p3', speakerName: '+1 555-***-**12', speakerKind: 'phoneUser'},
    ]});
    await control('POST', 'artifacts', {conference: record.name, kind: 'smartNote', state: 'STARTED'});

    const listed = await google(token, 'GET', '/v2/conferenceRecords', filter(new Date(Date.now() - 3_600_000).toISOString()));
    assert.deepEqual(listed.body.conferenceRecords.map((r: Json) => r.name), [record.name]);
    assert.equal((await google(token, 'GET', `/v2/${record.name}`)).body.endTime, record.endTime);
    const recording = (await google(token, 'GET', `/v2/${record.name}/recordings`)).body.recordings[0];
    assert.equal(recording.state, 'FILE_GENERATED');
    // Destination IDs are bare (no files/ or documents/ prefix), as Google returns them.
    assert.match(recording.driveDestination.file, /^[^/]+$/);
    const docs = (await google(token, 'GET', `/v2/${record.name}/transcripts`)).body.transcripts[0].docsDestination;
    assert.match(docs.exportUri, /^https:\/\/docs\.google\.com\//);
    assert.match(docs.document, /^[^/]+$/);
    const notes = (await google(token, 'GET', `/v2/${record.name}/smartNotes`)).body.smartNotes;
    assert.equal(notes[0].state, 'STARTED');
    assert.equal(notes[0].docsDestination, undefined);
    const entries = (await google(token, 'GET', `/v2/${transcript.name}/entries`)).body.transcriptEntries;
    assert.deepEqual(entries.map((e: Json) => e.text), ['Hello', 'Hi', 'Hey']);
    // Entries name only the participant; the display name is on whichever one user kind is set.
    const participants = await Promise.all(entries.map(async (e: Json) => (await google(token, 'GET', `/v2/${e.participant}`)).body));
    assert.deepEqual(participants.map((p: Json) => p.name), entries.map((e: Json) => e.participant));
    assert.deepEqual(participants.map((p: Json) => Object.keys(p).filter((k) => k.endsWith('User'))), [['signedinUser'], ['anonymousUser'], ['phoneUser']]);
    assert.deepEqual(participants.map((p: Json) => (p.signedinUser ?? p.anonymousUser ?? p.phoneUser).displayName), ['Participant p1', 'Guest', '+1 555-***-**12']);
    assert.match(participants[0].signedinUser.user, /^users\//);
    assert.equal((await google(token, 'GET', `/v2/${record.name}/participants/nobody`)).status, 404);

    assert.deepEqual(await violations(), []);
});

test('answers the calendar events query the plugin sends within the contract', async () => {
    const {access_token: token} = await connect([SCOPES.created, SCOPES.readonly, SCOPES.calendar]);
    await control('POST', 'events', {summary: 'Standup', startOffsetSeconds: 300, endOffsetSeconds: 1800, conferenceId: 'abc-defg-hij'});
    await control('POST', 'events', {summary: 'Holiday', allDay: true, startOffsetSeconds: 0, endOffsetSeconds: 86_400});

    // server/calendar.go listCalendarEvents
    const query = {timeMin: new Date(Date.now() - 3_600_000).toISOString(), timeMax: new Date(Date.now() + 3_600_000).toISOString(), singleEvents: 'true', orderBy: 'startTime'};
    const events = await google(token, 'GET', '/calendar/v3/calendars/primary/events', query);
    assert.equal(events.status, 200);
    assert.deepEqual(events.body.items.map((e: Json) => e.summary).sort(), ['Holiday', 'Standup']);
    assert.equal(events.body.items.find((e: Json) => e.summary === 'Standup').conferenceData.conferenceId, 'abc-defg-hij');
    assert.deepEqual(await violations(), []);
});

test('returns Google-shaped errors within the contract', async () => {
    const {access_token: readonlyOnly} = await connect([SCOPES.readonly]);

    assert.equal((await google('not-a-token', 'POST', '/v2/spaces', {}, {})).status, 401);
    const denied = await google(readonlyOnly, 'POST', '/v2/spaces', {}, {});
    assert.equal(denied.status, 403);
    assert.equal(denied.body.error.details[0].reason, 'ACCESS_TOKEN_SCOPE_INSUFFICIENT');
    assert.equal((await google(readonlyOnly, 'GET', '/v2/spaces/xyz-xyzw-xyz')).status, 404);
    assert.equal((await google(readonlyOnly, 'GET', '/calendar/v3/calendars/primary/events')).status, 403);
    await control('POST', 'fail', {methodId: 'meet.conferenceRecords.list', status: 500});
    assert.equal((await google(readonlyOnly, 'GET', '/v2/conferenceRecords')).status, 500);

    assert.deepEqual(await violations(), []);
});

test('records a violation for date-times and paths Google would read differently', async () => {
    const {access_token: token} = await connect([SCOPES.created, SCOPES.readonly, SCOPES.calendar]);
    const {body: space} = await google(token, 'POST', '/v2/spaces', {}, {});

    await google(token, 'GET', '/calendar/v3/calendars/primary/events', {timeMin: '2026-01-01'});
    assert.equal((await google(token, 'GET', '/v2/conferenceRecords', {filter: 'start_time>="2026-01-01 10:00:00"'})).status, 400);
    assert.equal((await google(token, 'GET', `/v2/spaces/${encodeURIComponent(space.name)}`)).status, 404);

    const found = await violations();
    assert.equal(found.length, 3, found.join('\n'));
    assert.match(found[0], /timeMin" must be an RFC 3339 date-time/);
    assert.match(found[1], /rejects the filter/);
    assert.match(found[2], /encoded "\/"/);
});

/** Sends a CONNECT for `target` through the fake's proxy; resolves with the proxy's status line. */
function connectThroughProxy(target: string): Promise<string> {
    return new Promise((resolve, reject) => {
        const req = http.request({host: '127.0.0.1', port: proxyPort, method: 'CONNECT', path: target});
        req.on('connect', (res, socket) => {
            socket.destroy();
            resolve(`${res.statusCode}`);
        });
        req.on('response', (res) => resolve(`${res.statusCode}`));
        req.on('error', reject);
        req.end();
    });
}

test('the proxy refuses every host it does not fake, and flags Google hosts however they are spelled', async () => {
    assert.equal(await connectThroughProxy('notices.mattermost.com:443'), '403');
    assert.equal(await connectThroughProxy('[::1]:443'), '403');
    assert.deepEqual(await violations(), []);

    assert.equal(await connectThroughProxy('Drive.GoogleAPIs.com.:443'), '403');
    const found = await violations();
    assert.equal(found.length, 1, found.join('\n'));
    assert.match(found[0], /Mattermost called drive\.googleapis\.com,/);
});

test('records a violation for a filter Google rejects and for an API called on the wrong host', async () => {
    const {access_token: token} = await connect([SCOPES.created, SCOPES.readonly]);

    assert.equal((await google(token, 'GET', '/v2/conferenceRecords', {filter: 'space="spaces/x"'})).status, 400);

    // Through the proxy every Google host reaches the fake, so the Host header tells them apart.
    const status = await new Promise<number>((resolve, reject) => {
        const url = new URL(`${base}/v2/spaces`);
        const req = http.request({host: url.hostname, port: url.port, path: url.pathname, method: 'POST',
            headers: {host: 'www.googleapis.com', authorization: `Bearer ${token}`, 'content-type': 'application/json'}}, (res) => {
            res.resume();
            resolve(res.statusCode!);
        });
        req.on('error', reject);
        req.end('{}');
    });
    assert.equal(status, 200);

    const found = await violations();
    assert.equal(found.length, 2, found.join('\n'));
    assert.match(found[0], /rejects the filter/);
    assert.match(found[1], /meet\.spaces\.create was called on www\.googleapis\.com/);
});
