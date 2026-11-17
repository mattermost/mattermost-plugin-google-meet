// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// A small fake of the Google APIs the Meet plugin calls: OAuth (accounts.google.com consent and
// oauth2.googleapis.com/token), the Meet REST API v2 (meet.googleapis.com) and Calendar API v3
// events.list (www.googleapis.com). The proxy, TLS, OAuth and contract plumbing is adapted from
// mattermost-plugin-google-drive e2e/playwright/fake-google/server.ts.
//
// It listens on HTTPS 443 for Mattermost, which sends all its outbound HTTPS through the fake's
// proxy on 3128 (HTTPS_PROXY in docker-compose.yml) and trusts its throwaway CA through
// SSL_CERT_FILE, so the plugin bundle under test is the normal production build. The proxy
// tunnels the Google hosts in FAKE_GOOGLE_HOSTS to the fake, refuses any other Google host as a
// contract violation (so a new endpoint fails clearly instead of reaching real Google), and passes
// everything else through. The browser and the tests reach the same routes on plain HTTP 8080,
// along with a /__control API for test setup and assertions.
//
// Every request it receives and every response it sends is checked against the contract in
// contract/ (Google's published Discovery documents plus the fields the plugin reads). Violations
// are collected; lib/test.ts fails the test that caused them, and global teardown fails the run
// on any sent outside a test.
//
// Meet behaviour the fake reproduces, from Google's Meet REST API reference:
// - Empty repeated fields are left out of the JSON (an empty list is `{}`).
// - conferenceRecords.list is ordered by startTime, newest first, and accepts the filter fields
//   space.name, space.meeting_code, start_time and end_time joined with AND.
// - Errors use the google.rpc.Status shape. A token without the method's scope gets 403
//   PERMISSION_DENIED with an ErrorInfo detail whose reason is ACCESS_TOKEN_SCOPE_INSUFFICIENT.
// - A space is addressed as spaces/{id} or by its meeting code.

import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import path from 'node:path';

import {tokenResponseFields, missingPaths} from './contract/required.ts';
import {ContractValidator, DATE_TIME} from './contract/validator.ts';
import type {MethodMatch} from './contract/validator.ts';

type Json = Record<string, any>;
type Result = {status: number; body?: unknown; headers?: Record<string, string>; html?: string};

const HTTP_PORT = Number(process.env.FAKE_HTTP_PORT || 8080);
const HTTPS_PORT = Number(process.env.FAKE_HTTPS_PORT || 443);
const PROXY_PORT = Number(process.env.FAKE_PROXY_PORT || 3128);
const CERT_DIR = process.env.FAKE_CERT_DIR || '/certs';
// The Google hosts the fake answers for (its certificate covers the same list; see docker-compose.yml).
const FAKE_HOSTS = new Set((process.env.FAKE_GOOGLE_HOSTS ?? '').split(/\s+/).filter(Boolean));
const GOOGLE_DOMAIN = /(^|\.)(google\.com|googleapis\.com|googleusercontent\.com)$/;

const CLIENT_ID = 'fake-client-id.apps.googleusercontent.com';
const CLIENT_SECRET = 'fake-client-secret';
const ACCESS_TOKEN_TTL_SECONDS = 3599;

const SCOPES = {
    created: 'https://www.googleapis.com/auth/meetings.space.created',
    readonly: 'https://www.googleapis.com/auth/meetings.space.readonly',
    calendar: 'https://www.googleapis.com/auth/calendar.events.readonly',
};
const KNOWN_SCOPES = new Set(Object.values(SCOPES));

const USERS = [
    {key: 'owner', email: 'meet-owner@fake-google.test', displayName: 'Meet Owner'},
    {key: 'guest', email: 'meet-guest@fake-google.test', displayName: 'Meet Guest'},
] as const;
type User = typeof USERS[number];

type Space = {name: string; meetingCode: string; meetingUri: string; owner: string};
type ConferenceRecord = {name: string; space: string; startTime: string; endTime?: string};
type Artifact = {name: string; state: string; startTime: string; endTime?: string; exportUri?: string; resource: string};
type TranscriptEntry = {name: string; participant: string; text: string; languageCode: string; startTime: string; endTime: string};
type CalendarEvent = {id: string; status: string; summary: string; start: Json; end: Json; conferenceId?: string; recurringEventId?: string};
type Failure = {methodId: string; status: number; reason?: string};
type LoggedRequest = {method: string; host: string; path: string; query: string; status: number; user?: string; methodId?: string};

const contract = new ContractValidator();

const state = {
    spaces: new Map<string, Space>(),
    conferences: new Map<string, ConferenceRecord>(),
    recordings: new Map<string, Artifact[]>(),
    transcripts: new Map<string, Artifact[]>(),
    smartNotes: new Map<string, Artifact[]>(),
    entries: new Map<string, TranscriptEntry[]>(),
    events: new Map<string, CalendarEvent[]>(),
    codes: new Map<string, {user: string; scope: string; redirectUri: string; offline: boolean; forceConsent: boolean}>(),
    // Every token belongs to a grant (one consent); revoking the grant revokes all its tokens.
    accessTokens: new Map<string, {user: string; scope: string; expiresAt: number; grant: string}>(),
    refreshTokens: new Map<string, {user: string; scope: string; grant: string}>(),
    // Users who have already granted offline access. Google returns a refresh token only on a user's
    // first offline grant, or when prompt=consent asks again.
    offlineGrants: new Set<string>(),
    pendingConsent: new Map<string, URLSearchParams>(),
    failures: [] as Failure[],
    requests: [] as LoggedRequest[],
    violations: [] as string[],
    // Violations that arrived after the test that caused them had been checked (the poller runs
    // asynchronously). The next test moves them here; global teardown fails the run on them.
    late: [] as string[],
    // Title of the last test whose violations were taken, so late ones can say what they followed.
    lastChecked: '',
};

/**
 * Clears Google-side state. `keepViolations` is for the per-test reset (lib/test.ts): it keeps the
 * violation lists, so a late violation from the previous test is still reported.
 */
function reset(keepViolations = false) {
    state.spaces.clear();
    state.conferences.clear();
    state.recordings.clear();
    state.transcripts.clear();
    state.smartNotes.clear();
    state.entries.clear();
    state.events.clear();
    state.codes.clear();
    state.accessTokens.clear();
    state.refreshTokens.clear();
    state.offlineGrants.clear();
    state.pendingConsent.clear();
    state.failures = [];
    state.requests = [];
    if (keepViolations) {
        return;
    }
    state.violations = [];
    state.late = [];
    state.lastChecked = '';
}

const randomId = (length = 33) => crypto.randomBytes(48).toString('base64url').replace(/[-_]/g, '').slice(0, length);
const letters = (n: number) => Array.from(crypto.randomBytes(n), (b) => String.fromCharCode(97 + (b % 26))).join('');
const userByKey = (key: string) => USERS.find((u) => u.key === key) as User;
const html = (s: string) => s.replace(/[&<>"]/g, (c) => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;'}[c] as string));
const isoOffset = (seconds: number) => new Date(Date.now() + seconds * 1000).toISOString();

function violation(message: string) {
    state.violations.push(message);
    console.warn(`CONTRACT VIOLATION: ${message}`);
}

// ---------------------------------------------------------------------------------------------
// Errors in the google.rpc.Status shape the Meet and Calendar APIs use.

function apiError(status: number, grpcStatus: string, message: string, reason?: string): Result {
    const details = reason ? [{'@type': 'type.googleapis.com/google.rpc.ErrorInfo', reason, domain: 'googleapis.com'}] : undefined;
    return {status, body: {error: {code: status, message, status: grpcStatus, ...(details ? {details} : {})}}};
}

function unauthenticated(): Result {
    return apiError(401, 'UNAUTHENTICATED', 'Request had invalid authentication credentials. Expected OAuth 2 access token, login cookie or other valid authentication credential.', 'ACCESS_TOKEN_TYPE_UNSUPPORTED');
}

function insufficientScope(): Result {
    return apiError(403, 'PERMISSION_DENIED', 'Request had insufficient authentication scopes.', 'ACCESS_TOKEN_SCOPE_INSUFFICIENT');
}

// The google.rpc.Code Google pairs with each HTTP status (cloud.google.com/apis/design/errors).
const GRPC_STATUS: Record<number, string> = {
    400: 'INVALID_ARGUMENT', 401: 'UNAUTHENTICATED', 403: 'PERMISSION_DENIED', 404: 'NOT_FOUND', 409: 'ABORTED',
    429: 'RESOURCE_EXHAUSTED', 499: 'CANCELLED', 500: 'INTERNAL', 501: 'UNIMPLEMENTED', 503: 'UNAVAILABLE', 504: 'DEADLINE_EXCEEDED',
};

const notFound = () => apiError(404, 'NOT_FOUND', 'Requested entity was not found.');

// ---------------------------------------------------------------------------------------------
// OAuth: consent page (browser, accounts.google.com) and token endpoint (oauth2.googleapis.com).

const allowedRedirect = (uri: string) => /^http:\/\/localhost:\d+\/plugins\/com\.mattermost\.google-meet\/api\/v1\/oauth\/callback$/.test(uri);

function authorize(query: URLSearchParams): Result {
    if (query.get('client_id') !== CLIENT_ID) {
        return errorPage(401, 'Error 401: invalid_client', 'The OAuth client was not found.');
    }
    const redirectUri = query.get('redirect_uri') ?? '';
    if (!allowedRedirect(redirectUri)) {
        return errorPage(400, 'Error 400: redirect_uri_mismatch', `${redirectUri} is not a registered redirect URI.`);
    }
    if (query.get('response_type') !== 'code') {
        return errorPage(400, 'Error 400: unsupported_response_type', 'Only response_type=code is supported.');
    }
    const accessType = query.get('access_type');
    if (accessType !== null && accessType !== 'online' && accessType !== 'offline') {
        return errorPage(400, 'Error 400: invalid_request', `Invalid access_type: ${accessType}`);
    }
    const unknown = (query.get('scope') ?? '').split(' ').filter((s) => s && !KNOWN_SCOPES.has(s));
    if (unknown.length) {
        return errorPage(400, 'Error 400: invalid_scope', `Some requested scopes were invalid: ${unknown.join(', ')}`);
    }
    const sid = randomId(16);
    state.pendingConsent.set(sid, query);
    const accounts = USERS.map((u) => `<button type="submit" name="account" value="${u.key}">${html(u.displayName)} (${html(u.email)})</button>`).join('');
    return page('Choose an account', `<h1>Choose an account</h1><p>to continue to Mattermost Google Meet (fake)</p>
        <form method="post" action="/o/oauth2/consent"><input type="hidden" name="sid" value="${sid}">${accounts}</form>`);
}

// Google's granular consent: every requested scope is a checkbox the user can clear.
function consent(form: URLSearchParams): Result {
    const sid = form.get('sid') ?? '';
    const query = state.pendingConsent.get(sid);
    if (!query) {
        return errorPage(400, 'Error 400: invalid_request', 'The sign-in session expired.');
    }
    const account = form.get('account');
    if (account && !userByKey(account)) {
        return errorPage(400, 'Error 400: invalid_request', 'Unknown account.');
    }
    if (account && !form.get('decision')) {
        const scopes = (query.get('scope') ?? '').split(' ').filter(Boolean)
            .map((s) => `<li><label><input type="checkbox" name="scope" value="${html(s)}" checked> ${html(s)}</label></li>`).join('');
        return page('Grant access', `<h1>Mattermost Google Meet wants access to your Google Account</h1><p>${html(userByKey(account).email)}</p>
            <form method="post" action="/o/oauth2/consent"><input type="hidden" name="sid" value="${sid}"><input type="hidden" name="account" value="${html(account)}">
            <ul>${scopes}</ul><button type="submit" name="decision" value="deny">Cancel</button><button type="submit" name="decision" value="allow">Continue</button></form>`);
    }
    state.pendingConsent.delete(sid);
    const redirect = new URL(query.get('redirect_uri')!);
    if (form.get('decision') !== 'allow' || !account) {
        redirect.searchParams.set('error', 'access_denied');
    } else {
        const granted = form.getAll('scope').join(' ');
        const code = `4/fake-${randomId(40)}`;
        state.codes.set(code, {
            user: account,
            scope: granted,
            redirectUri: query.get('redirect_uri')!,
            offline: query.get('access_type') === 'offline',
            forceConsent: (query.get('prompt') ?? '').split(' ').includes('consent'),
        });
        redirect.searchParams.set('code', code);
        redirect.searchParams.set('scope', granted);
    }
    if (query.get('state') !== null) {
        redirect.searchParams.set('state', query.get('state')!);
    }
    return {status: 302, headers: {location: redirect.toString()}};
}

function page(title: string, body: string): Result {
    return {status: 200, html: `<!doctype html><html><head><meta charset="utf-8"><title>${html(title)} - Fake Google</title></head><body>${body}</body></html>`};
}

function errorPage(status: number, title: string, message: string): Result {
    return {status, html: `<!doctype html><html><head><title>${html(title)}</title></head><body><h1>${html(title)}</h1><p>${html(message)}</p></body></html>`};
}

function issueAccessToken(user: string, scope: string, grant: string, ttlSeconds = ACCESS_TOKEN_TTL_SECONDS) {
    const accessToken = `ya29.fake-${randomId(60)}`;
    state.accessTokens.set(accessToken, {user, scope, grant, expiresAt: Date.now() + ttlSeconds * 1000});
    return accessToken;
}

function issueRefreshToken(user: string, scope: string, grant: string) {
    const refreshToken = `1//fake-${randomId(40)}`;
    state.refreshTokens.set(refreshToken, {user, scope, grant});
    state.offlineGrants.add(user);
    return refreshToken;
}

/** Revokes a grant, as when the user removes the app's access in their Google account. */
function revokeGrant(grant: string) {
    for (const [key, value] of [...state.refreshTokens, ...state.accessTokens]) {
        if (value.grant === grant) {
            state.refreshTokens.delete(key);
            state.accessTokens.delete(key);
        }
    }
}

function tokenEndpoint(form: URLSearchParams, authorization: string): Result {
    let clientId = form.get('client_id');
    let clientSecret = form.get('client_secret');
    if (authorization.startsWith('Basic ')) {
        const [id, secret] = Buffer.from(authorization.slice(6), 'base64').toString().split(':').map(decodeURIComponent);
        clientId = id;
        clientSecret = secret;
    }
    if (clientId !== CLIENT_ID || clientSecret !== CLIENT_SECRET) {
        return {status: 401, body: {error: 'invalid_client', error_description: 'The OAuth client was not found.'}};
    }

    const grantType = form.get('grant_type') ?? '';
    let user: string;
    let scope: string;
    let refreshToken: string | undefined;
    let grant: string;
    if (grantType === 'authorization_code') {
        const code = state.codes.get(form.get('code') ?? '');
        state.codes.delete(form.get('code') ?? '');
        if (!code || code.redirectUri !== form.get('redirect_uri')) {
            return {status: 400, body: {error: 'invalid_grant', error_description: 'Bad Request'}};
        }
        user = code.user;
        scope = code.scope;
        grant = randomId(12);
        // Without access_type=offline Google issues no refresh token, and the connection dies with
        // the first access token; it also skips it on a repeat grant unless prompt=consent.
        if (code.offline && (code.forceConsent || !state.offlineGrants.has(user))) {
            refreshToken = issueRefreshToken(user, scope, grant);
        }
    } else if (grantType === 'refresh_token') {
        const found = state.refreshTokens.get(form.get('refresh_token') ?? '');
        if (!found) {
            return {status: 400, body: {error: 'invalid_grant', error_description: 'Token has been expired or revoked.'}};
        }
        user = found.user;
        scope = found.scope;
        grant = found.grant;
    } else {
        return {status: 400, body: {error: 'unsupported_grant_type', error_description: `Invalid grant_type: ${grantType}`}};
    }

    const body = {
        access_token: issueAccessToken(user, scope, grant),
        expires_in: ACCESS_TOKEN_TTL_SECONDS,
        ...(refreshToken ? {refresh_token: refreshToken} : {}),
        scope,
        token_type: 'Bearer',
    };
    for (const missing of missingPaths(body, tokenResponseFields(grantType))) {
        // A missing refresh token is real Google behaviour, but the plugin needs one, so it means the
        // plugin's authorization request was wrong.
        const why = missing === 'refresh_token' ? ' (Google only returns one for access_type=offline, and on a repeat grant only with prompt=consent; the plugin needs it to stay connected)' : '';
        violation(`token response (${grantType}): missing ${missing}${why}`);
    }
    return {status: 200, body};
}

function authenticate(authorization: string): {user: User; scopes: Set<string>} | null {
    const token = state.accessTokens.get(authorization.replace(/^Bearer /, ''));
    if (!token || token.expiresAt <= Date.now()) {
        return null;
    }
    return {user: userByKey(token.user), scopes: new Set(token.scope.split(' ').filter(Boolean))};
}

// Scopes each method accepts (developers.google.com/workspace/meet/api/reference/rest and
// developers.google.com/workspace/calendar/api/v3/reference/events/list). Only the ones the plugin
// requests are listed.
const METHOD_SCOPES: Record<string, string[]> = {
    'meet.spaces.create': [SCOPES.created],
    'meet.spaces.get': [SCOPES.created, SCOPES.readonly],
    'meet.conferenceRecords.list': [SCOPES.created, SCOPES.readonly],
    'meet.conferenceRecords.get': [SCOPES.created, SCOPES.readonly],
    'meet.conferenceRecords.recordings.list': [SCOPES.created, SCOPES.readonly],
    'meet.conferenceRecords.transcripts.list': [SCOPES.created, SCOPES.readonly],
    'meet.conferenceRecords.transcripts.entries.list': [SCOPES.created, SCOPES.readonly],
    'meet.conferenceRecords.smartNotes.list': [SCOPES.created, SCOPES.readonly],
    'calendar.events.list': [SCOPES.calendar],
};

// ---------------------------------------------------------------------------------------------
// Meet resources.

function createSpace(owner: string): Space {
    const meetingCode = `${letters(3)}-${letters(4)}-${letters(3)}`;
    const space = {name: `spaces/${randomId(12)}`, meetingCode, meetingUri: `https://meet.google.com/${meetingCode}`, owner};
    state.spaces.set(space.name, space);
    return space;
}

/** A space by its `{id}` or meeting code, as spaces.get takes it in `spaces/{id}`. */
function findSpace(id: string): Space | undefined {
    return state.spaces.get(`spaces/${id}`) ?? [...state.spaces.values()].find((s) => s.meetingCode === id);
}

/** For the control API: a space by `spaces/{id}`, `{id}` or meeting code. */
const findSpaceForControl = (ref: string) => findSpace(ref.replace(/^spaces\//, ''));

const renderSpace = (s: Space) => ({name: s.name, meetingUri: s.meetingUri, meetingCode: s.meetingCode});

function renderConference(c: ConferenceRecord) {
    // Google sets expireTime (30 days after the end) only once the conference has ended.
    const ended = c.endTime ? {endTime: c.endTime, expireTime: new Date(Date.parse(c.endTime) + 30 * 24 * 3600 * 1000).toISOString()} : {};
    return {name: c.name, space: c.space, startTime: c.startTime, ...ended};
}

function renderArtifact(a: Artifact, destination: 'driveDestination' | 'docsDestination') {
    const exportUri = a.state === 'FILE_GENERATED' ? a.exportUri : undefined;
    const dest = exportUri ? {[destination]: {[destination === 'driveDestination' ? 'file' : 'document']: a.resource, exportUri}} : {};
    return {name: a.name, state: a.state, startTime: a.startTime, ...(a.endTime ? {endTime: a.endTime} : {}), ...dest};
}

// conferenceRecords.list filter grammar (developers.google.com/workspace/meet/api/reference/rest/v2/conferenceRecords/list):
// space.meeting_code, space.name, start_time and end_time, combined with AND.
const FILTER_TERM = /^(space\.name|space\.meeting_code)\s*=\s*"([^"]*)"$|^(start_time|end_time)\s*(>=|<=|>|<|=)\s*"([^"]*)"$/;

function parseFilter(filter: string): {ok: true; test: (c: ConferenceRecord) => boolean} | {ok: false; message: string} {
    if (!filter.trim()) {
        return {ok: true, test: () => true};
    }
    const tests: Array<(c: ConferenceRecord) => boolean> = [];
    for (const term of filter.split(/\s+AND\s+/)) {
        const m = FILTER_TERM.exec(term.trim());
        if (!m) {
            return {ok: false, message: `Invalid filter term: ${term}`};
        }
        if (m[1] === 'space.name') {
            tests.push((c) => c.space === m[2]);
        } else if (m[1] === 'space.meeting_code') {
            tests.push((c) => state.spaces.get(c.space)?.meetingCode === m[2]);
        } else {
            const at = Date.parse(m[5]);
            if (!DATE_TIME.test(m[5]) || Number.isNaN(at)) {
                return {ok: false, message: `Invalid timestamp in filter: ${m[5]}`};
            }
            const field = m[3] === 'start_time' ? 'startTime' : 'endTime';
            const op = m[4];
            tests.push((c) => {
                const value = c[field];
                if (!value) {
                    return false;
                }
                const t = Date.parse(value);
                return op === '>=' ? t >= at : op === '<=' ? t <= at : op === '>' ? t > at : op === '<' ? t < at : t === at;
            });
        }
    }
    return {ok: true, test: (c) => tests.every((t) => t(c))};
}

function canSee(user: User, scopes: Set<string>, spaceName: string): boolean {
    // meetings.space.created reaches the spaces this app created for the user; readonly reaches any
    // space the user can see, which the fake approximates as every space.
    return scopes.has(SCOPES.readonly) || state.spaces.get(spaceName)?.owner === user.key;
}

type Page<T> = {page: T[]; nextPageToken?: string; error?: undefined} | {error: Result};

/**
 * Offset pagination. Google's page tokens are opaque and only valid for the query that returned
 * them, so a token is bound to the method, parent and every parameter except the page ones; a
 * malformed token or one replayed with a different query gets Google's 400. A page size of 0 (or
 * none) means the method's default.
 */
function paginate<T>(items: T[], match: MethodMatch, query: URLSearchParams, defaultSize: number, maxSize: number, sizeParam = 'pageSize'): Page<T> {
    const scope = new URLSearchParams([...query].filter(([k]) => k !== 'pageToken' && k !== sizeParam).sort(([a], [b]) => a.localeCompare(b)));
    const key = crypto.createHash('sha256').update(`${match.id} ${JSON.stringify(match.pathParams)} ${scope}`).digest('base64url').slice(0, 12);
    let start = 0;
    const token = query.get('pageToken');
    if (token) {
        const [offset, tokenKey] = Buffer.from(token, 'base64url').toString().split(':');
        if (!/^\d+$/.test(offset ?? '') || tokenKey !== key) {
            violation(`${match.id}: the plugin sent a page token that does not belong to this query`);
            return {error: apiError(400, 'INVALID_ARGUMENT', 'Invalid page token.')};
        }
        start = Number(offset);
    }
    const requested = Number(query.get(sizeParam) || 0);
    const size = Math.min(requested > 0 ? requested : defaultSize, maxSize);
    const end = start + size;
    return {page: items.slice(start, end), ...(end < items.length ? {nextPageToken: Buffer.from(`${end}:${key}`).toString('base64url')} : {})};
}

function listOf(key: string, items: unknown[], nextPageToken?: string): Json {
    // Google leaves empty repeated fields out of its JSON.
    return {...(items.length ? {[key]: items} : {}), ...(nextPageToken ? {nextPageToken} : {})};
}

// ---------------------------------------------------------------------------------------------
// API routes. `match` is the documented method the request was matched to.

function route(match: MethodMatch, user: User, scopes: Set<string>, query: URLSearchParams): Result {
    const p = match.pathParams;
    const conference = (id: string) => {
        const record = state.conferences.get(`conferenceRecords/${id}`);
        return record && canSee(user, scopes, record.space) ? record : null;
    };
    const artifactList = (store: Map<string, Artifact[]>, key: string, destination: 'driveDestination' | 'docsDestination') => {
        const record = conference(p.conferenceRecordsId);
        if (!record) {
            return notFound();
        }
        const paged = paginate(store.get(record.name) ?? [], match, query, 10, 100);
        if (paged.error) {
            return paged.error;
        }
        return {status: 200, body: listOf(key, paged.page.map((a) => renderArtifact(a, destination)), paged.nextPageToken)};
    };

    switch (match.id) {
    case 'meet.spaces.create':
        return {status: 200, body: renderSpace(createSpace(user.key))};
    case 'meet.spaces.get': {
        const space = findSpace(p.spacesId);
        return space && canSee(user, scopes, space.name) ? {status: 200, body: renderSpace(space)} : notFound();
    }
    case 'meet.conferenceRecords.list': {
        const filter = parseFilter(query.get('filter') ?? '');
        if (!filter.ok) {
            violation(`meet.conferenceRecords.list: Google rejects the filter "${query.get('filter')}" (${filter.message})`);
            return apiError(400, 'INVALID_ARGUMENT', filter.message);
        }
        const records = [...state.conferences.values()]
            .filter((c) => canSee(user, scopes, c.space) && filter.test(c))
            .sort((a, b) => Date.parse(b.startTime) - Date.parse(a.startTime));
        const paged = paginate(records, match, query, 25, 100);
        if (paged.error) {
            return paged.error;
        }
        return {status: 200, body: listOf('conferenceRecords', paged.page.map(renderConference), paged.nextPageToken)};
    }
    case 'meet.conferenceRecords.get': {
        const record = conference(p.conferenceRecordsId);
        return record ? {status: 200, body: renderConference(record)} : notFound();
    }
    case 'meet.conferenceRecords.recordings.list':
        return artifactList(state.recordings, 'recordings', 'driveDestination');
    case 'meet.conferenceRecords.transcripts.list':
        return artifactList(state.transcripts, 'transcripts', 'docsDestination');
    case 'meet.conferenceRecords.smartNotes.list':
        return artifactList(state.smartNotes, 'smartNotes', 'docsDestination');
    case 'meet.conferenceRecords.transcripts.entries.list': {
        if (!conference(p.conferenceRecordsId)) {
            return notFound();
        }
        const name = `conferenceRecords/${p.conferenceRecordsId}/transcripts/${p.transcriptsId}`;
        const paged = paginate(state.entries.get(name) ?? [], match, query, 10, 100);
        if (paged.error) {
            return paged.error;
        }
        return {status: 200, body: listOf('transcriptEntries', paged.page, paged.nextPageToken)};
    }
    case 'calendar.events.list':
        return listEvents(match, user, p.calendarId, query);
    default:
        violation(`${match.id} is in the contract but the fake does not implement it`);
        return apiError(501, 'UNIMPLEMENTED', 'Not implemented by the fake');
    }
}

function listEvents(match: MethodMatch, user: User, calendarId: string, query: URLSearchParams): Result {
    if (calendarId !== 'primary') {
        return notFound();
    }
    if (query.get('orderBy') === 'startTime' && query.get('singleEvents') !== 'true') {
        // Calendar rejects this combination.
        return apiError(400, 'INVALID_ARGUMENT', 'The requested ordering is not available for the particular query.');
    }
    const min = query.get('timeMin') ? Date.parse(query.get('timeMin')!) : -Infinity;
    const max = query.get('timeMax') ? Date.parse(query.get('timeMax')!) : Infinity;
    const startOf = (e: CalendarEvent) => Date.parse(e.start.dateTime ?? e.start.date);
    const endOf = (e: CalendarEvent) => Date.parse(e.end.dateTime ?? e.end.date);
    // Cancelled events are left out unless showDeleted=true.
    const showDeleted = query.get('showDeleted') === 'true';
    const events = (state.events.get(user.key) ?? [])
        .filter((e) => (showDeleted || e.status !== 'cancelled') && endOf(e) > min && startOf(e) < max)
        .sort((a, b) => startOf(a) - startOf(b));
    const paged = paginate(events, match, query, 250, 2500, 'maxResults');
    if (paged.error) {
        return paged.error;
    }
    const {page: items, nextPageToken} = paged;
    return {
        status: 200,
        body: {
            kind: 'calendar#events',
            summary: user.email,
            timeZone: 'UTC',
            ...(nextPageToken ? {nextPageToken} : {}),
            items: items.map((e) => ({
                kind: 'calendar#event',
                id: e.id,
                status: e.status,
                summary: e.summary,
                start: e.start,
                end: e.end,
                ...(e.recurringEventId ? {recurringEventId: e.recurringEventId} : {}),
                ...(e.conferenceId ? {conferenceData: {conferenceId: e.conferenceId}} : {}),
            })),
        },
    };
}

// ---------------------------------------------------------------------------------------------
// Test control API: setup and assertions that have no Google API equivalent.

function control(method: string, segments: string[], body: Json): Result {
    const [section, action] = segments;
    if (section === 'health') {
        return {status: 200, body: {ok: true}};
    }
    if (section === 'reset' && method === 'POST') {
        reset(Boolean(body.keepViolations));
        return {status: 204};
    }
    if (section === 'accounts' && method === 'GET') {
        return {status: 200, body: {clientId: CLIENT_ID, clientSecret: CLIENT_SECRET, scopes: SCOPES, users: USERS}};
    }
    if (section === 'tokens' && method === 'POST' && !action) {
        // A token pair as if the user had connected, for tests that seed the plugin's KV store.
        const user = userByKey(body.user ?? 'owner');
        if (!user) {
            return {status: 400, body: {error: `unknown user ${body.user}`}};
        }
        const scope = (body.scopes ?? [SCOPES.created, SCOPES.readonly]).join(' ');
        const ttl = Number(body.expiresInSeconds ?? ACCESS_TOKEN_TTL_SECONDS);
        const grant = randomId(12);
        const accessToken = issueAccessToken(user.key, scope, grant, ttl);
        const refreshToken = issueRefreshToken(user.key, scope, grant);
        return {status: 200, body: {access_token: accessToken, refresh_token: refreshToken, token_type: 'Bearer', expiry: isoOffset(ttl), scope}};
    }
    if (section === 'tokens' && action === 'revoke' && method === 'POST') {
        // As if the user removed the app's access in their Google account settings: the whole grant,
        // every access token issued from it included, stops working.
        const grant = state.refreshTokens.get(body.refresh_token)?.grant ?? state.accessTokens.get(body.access_token)?.grant;
        if (!grant) {
            return {status: 404, body: {error: 'unknown token'}};
        }
        revokeGrant(grant);
        return {status: 204};
    }
    if (section === 'tokens' && action === 'refreshes' && method === 'GET') {
        return {status: 200, body: {value: state.requests.filter((r) => r.path === '/token' && r.methodId === 'refresh_token').length}};
    }
    if (section === 'fail' && method === 'POST') {
        // The next call to `methodId` fails with `status` (and ErrorInfo `reason`, if given).
        state.failures.push({methodId: body.methodId, status: Number(body.status), reason: body.reason});
        return {status: 204};
    }
    if (section === 'spaces' && method === 'GET') {
        return {status: 200, body: {value: [...state.spaces.values()]}};
    }
    if (section === 'spaces' && method === 'POST') {
        // A space the user created outside Mattermost (e.g. for /meet subscription add).
        const user = userByKey(body.user ?? 'owner');
        return user ? {status: 200, body: createSpace(user.key)} : {status: 400, body: {error: `unknown user ${body.user}`}};
    }
    if (section === 'conferences' && method === 'POST') {
        // A conference in a space. Times are seconds from now, so tests can backdate instead of waiting.
        const space = findSpaceForControl(body.space ?? '');
        if (!space) {
            return {status: 400, body: {error: `unknown space ${body.space}`}};
        }
        const record: ConferenceRecord = {
            name: `conferenceRecords/${randomId(20)}`,
            space: space.name,
            startTime: isoOffset(Number(body.startOffsetSeconds ?? 0)),
            ...(body.endOffsetSeconds === undefined ? {} : {endTime: isoOffset(Number(body.endOffsetSeconds))}),
        };
        state.conferences.set(record.name, record);
        return {status: 200, body: record};
    }
    if (section === 'conferences' && action && method === 'PATCH') {
        // Ends (or re-times) a conference: {endOffsetSeconds}.
        const record = state.conferences.get(`conferenceRecords/${action}`);
        if (!record) {
            return {status: 404, body: {error: `unknown conference ${action}`}};
        }
        record.endTime = isoOffset(Number(body.endOffsetSeconds ?? 0));
        return {status: 200, body: record};
    }
    if (section === 'artifacts' && method === 'POST') {
        // A recording, transcript or smart note of a conference, or a state change of an existing
        // one (same name). Transcripts take `entries` [{text, speaker?}].
        const record = state.conferences.get(body.conference);
        const store = {recording: state.recordings, transcript: state.transcripts, smartNote: state.smartNotes}[body.kind as string];
        if (!record || !store) {
            return {status: 400, body: {error: 'need an existing conference and kind recording|transcript|smartNote'}};
        }
        const collection = body.kind === 'smartNote' ? 'smartNotes' : `${body.kind}s`;
        const list = store.get(record.name) ?? [];
        let artifact = list.find((a) => a.name === body.name);
        if (!artifact) {
            const id = randomId(16);
            const isDoc = body.kind !== 'recording';
            artifact = {
                name: `${record.name}/${collection}/${id}`,
                state: 'STARTED',
                startTime: record.startTime,
                // Google's driveDestination.file and docsDestination.document are bare IDs.
                resource: id,
                exportUri: isDoc ? `https://docs.google.com/document/d/${id}/edit` : `https://drive.google.com/file/d/${id}/view`,
            };
            list.push(artifact);
            store.set(record.name, list);
        }
        artifact.state = body.state ?? 'FILE_GENERATED';
        if (artifact.state !== 'STARTED') {
            artifact.endTime ??= record.endTime ?? isoOffset(0);
        }
        if (body.kind === 'transcript' && Array.isArray(body.entries)) {
            const start = Date.parse(record.startTime);
            state.entries.set(artifact.name, body.entries.map((e: Json, i: number) => ({
                name: `${artifact!.name}/entries/${i + 1}`,
                participant: `${record.name}/participants/${e.speaker ?? 'p1'}`,
                text: e.text,
                languageCode: 'en-US',
                startTime: new Date(start + i * 5000).toISOString(),
                endTime: new Date(start + i * 5000 + 4000).toISOString(),
            })));
        }
        return {status: 200, body: artifact};
    }
    if (section === 'events' && method === 'POST') {
        // A primary-calendar event: {user, summary, startOffsetSeconds, endOffsetSeconds, conferenceId?, status?, allDay?, recurringEventId?}.
        // With singleEvents=true (what the plugin asks for) Google returns each occurrence of a
        // recurring event as its own event carrying the series' recurringEventId, so tests create
        // one event per occurrence with the same recurringEventId.
        const user = userByKey(body.user ?? 'owner');
        if (!user) {
            return {status: 400, body: {error: `unknown user ${body.user}`}};
        }
        const start = new Date(Date.now() + Number(body.startOffsetSeconds ?? 0) * 1000);
        const end = new Date(Date.now() + Number(body.endOffsetSeconds ?? 3600) * 1000);
        const day = (d: Date) => d.toISOString().slice(0, 10);
        const event: CalendarEvent = {
            id: randomId(26).toLowerCase(),
            status: body.status ?? 'confirmed',
            summary: body.summary ?? 'Meeting',
            start: body.allDay ? {date: day(start)} : {dateTime: start.toISOString(), timeZone: 'UTC'},
            end: body.allDay ? {date: day(end)} : {dateTime: end.toISOString(), timeZone: 'UTC'},
            ...(body.conferenceId ? {conferenceId: body.conferenceId} : {}),
            ...(body.recurringEventId ? {recurringEventId: body.recurringEventId} : {}),
        };
        state.events.set(user.key, [...(state.events.get(user.key) ?? []), event]);
        return {status: 200, body: event};
    }
    if (section === 'requests') {
        if (method === 'DELETE') {
            state.requests = [];
            return {status: 204};
        }
        return {status: 200, body: {value: state.requests}};
    }
    if (section === 'violations') {
        const current = {value: state.violations};
        if (method === 'POST' && action === 'take') {
            // Read and clear in one step, so nothing recorded in between is lost.
            state.lastChecked = typeof body?.test === 'string' ? body.test : '';
            state.violations = [];
            return {status: 200, body: current};
        }
        if (method === 'POST' && action === 'defer') {
            // Late traffic is asynchronous, so this names the test it followed, not necessarily its cause.
            const after = state.lastChecked ? `after "${state.lastChecked}"` : 'before the first test';
            const deferred = state.violations.map((v) => `[${after}] ${v}`);
            state.late.push(...deferred);
            state.violations = [];
            return {status: 200, body: {...current, value: deferred}};
        }
        return {status: 200, body: {...current, late: state.late, lastChecked: state.lastChecked}};
    }
    return {status: 404, body: {error: `unknown control route ${method} /${segments.join('/')}`}};
}

// ---------------------------------------------------------------------------------------------
// HTTP plumbing.

async function readBody(req: http.IncomingMessage): Promise<Buffer> {
    const chunks: Buffer[] = [];
    for await (const chunk of req) {
        chunks.push(chunk as Buffer);
    }
    return Buffer.concat(chunks);
}

function send(res: http.ServerResponse, result: Result) {
    if (result.html !== undefined) {
        res.writeHead(result.status, {'content-type': 'text/html; charset=utf-8', ...result.headers});
        res.end(result.html);
        return;
    }
    if (result.body === undefined) {
        res.writeHead(result.status, result.headers);
        res.end();
        return;
    }
    res.writeHead(result.status, {'content-type': 'application/json; charset=UTF-8', ...result.headers});
    res.end(JSON.stringify(result.body));
}

async function handle(req: http.IncomingMessage, res: http.ServerResponse) {
    const url = new URL(req.url ?? '/', 'http://fake');
    const method = req.method ?? 'GET';
    const host = connectHost(req.headers.host ?? '');
    const raw = await readBody(req);
    const log: LoggedRequest = {method, host, path: url.pathname, query: url.search, status: 0};

    let result: Result;
    try {
        if (url.pathname.startsWith('/__control/')) {
            result = control(method, url.pathname.split('/').slice(2), raw.length ? JSON.parse(raw.toString()) : {});
            send(res, result);
            return;
        }
        // Through the proxy every faked host reaches this server, so check the OAuth endpoints are
        // called on their own hosts (the browser and the tests use the plain HTTP port, no Google host).
        const oauthHost = OAUTH_HOSTS[url.pathname];
        if (oauthHost && GOOGLE_DOMAIN.test(host) && host !== oauthHost) {
            violation(`${url.pathname} was called on ${host}; Google serves it on ${oauthHost}`);
        }
        if (url.pathname === '/o/oauth2/v2/auth') {
            result = authorize(url.searchParams);
        } else if (url.pathname === '/o/oauth2/consent' && method === 'POST') {
            result = consent(new URLSearchParams(raw.toString()));
        } else if (url.pathname === '/token' && method === 'POST') {
            const form = new URLSearchParams(raw.toString());
            log.methodId = form.get('grant_type') ?? undefined;
            result = tokenEndpoint(form, req.headers.authorization ?? '');
        } else {
            result = api(method, url, raw, req.headers.authorization ?? '', log);
        }
    } catch (err) {
        console.error(err);
        violation(`${method} ${url.pathname}: the fake crashed: ${String(err)}`);
        result = apiError(500, 'INTERNAL', 'Internal error encountered.');
    }
    log.status = result.status;
    state.requests.push(log);
    send(res, result);
}

function api(method: string, url: URL, raw: Buffer, authorization: string, log: LoggedRequest): Result {
    const match = contract.match(method, url.pathname);
    if (!match) {
        violation(`${method} ${url.pathname} is not covered by fake-google/contract/operations.json`);
        return notFound();
    }
    log.methodId = match.id;

    let body: Json | undefined;
    if (raw.length) {
        try {
            body = JSON.parse(raw.toString('utf8'));
        } catch {
            violation(`${match.id}: request body is not JSON`);
            return apiError(400, 'INVALID_ARGUMENT', 'Invalid JSON payload received.');
        }
    }
    for (const problem of contract.validateRequest(match, url.searchParams, body)) {
        violation(problem);
    }
    // The proxy sends every faked Google host here, so check the API was called on its own host
    // (the tests' own calls on the published HTTP port have no Google host to check).
    const expectedHost = API_HOSTS[match.api];
    if (GOOGLE_DOMAIN.test(log.host) && expectedHost && log.host !== expectedHost) {
        violation(`${match.id} was called on ${log.host}; Google serves it on ${expectedHost}`);
    }

    // Every response, error ones included, is checked against the contract.
    const result = respondTo(match, url, authorization, log);
    for (const problem of contract.validateResponse(match, url.searchParams, result.status, result.body)) {
        violation(problem);
    }
    return result;
}

const OAUTH_HOSTS: Record<string, string> = {'/o/oauth2/v2/auth': 'accounts.google.com', '/o/oauth2/consent': 'accounts.google.com', '/token': 'oauth2.googleapis.com'};

// The host each API in contract/specs answers on.
const API_HOSTS: Record<string, string> = {meet_v2: 'meet.googleapis.com', calendar_v3: 'www.googleapis.com'};

function respondTo(match: MethodMatch, url: URL, authorization: string, log: LoggedRequest): Result {
    const auth = authenticate(authorization);
    if (!auth) {
        return unauthenticated();
    }
    log.user = auth.user.key;

    const failureIndex = state.failures.findIndex((f) => f.methodId === match.id);
    if (failureIndex >= 0) {
        const [failure] = state.failures.splice(failureIndex, 1);
        return failure.reason === 'ACCESS_TOKEN_SCOPE_INSUFFICIENT' ? insufficientScope() :
            apiError(failure.status, GRPC_STATUS[failure.status] ?? (failure.status >= 500 ? 'INTERNAL' : 'FAILED_PRECONDITION'), 'Injected failure (fake Google control API).', failure.reason);
    }
    if (!(METHOD_SCOPES[match.id] ?? []).some((s) => auth.scopes.has(s))) {
        return insufficientScope();
    }
    return route(match, auth.user, auth.scopes, url.searchParams);
}

const handler = (req: http.IncomingMessage, res: http.ServerResponse) => {
    handle(req, res).catch((err) => {
        console.error(err);
        res.writeHead(500);
        res.end();
    });
};

// Port 0 picks a free port (fake-google/smoke.test.ts); the log line reports the one in use.
const boundPort = (server: net.Server) => (server.address() as net.AddressInfo).port;
const httpServer = http.createServer(handler).listen(HTTP_PORT, () => console.log(`fake Google on http://0.0.0.0:${boundPort(httpServer)}`));

const certFile = path.join(CERT_DIR, 'fake.crt');
const keyFile = path.join(CERT_DIR, 'fake.key');
if (fs.existsSync(certFile) && fs.existsSync(keyFile)) {
    https.createServer({cert: fs.readFileSync(certFile), key: fs.readFileSync(keyFile)}, handler)
        .listen(HTTPS_PORT, () => console.log(`fake Google on https://0.0.0.0:${HTTPS_PORT}`));
} else if (FAKE_HOSTS.size) {
    // In the compose stack the plugin can only reach the fake over HTTPS, so fail loudly instead of
    // leaving every Google call to time out.
    console.error(`No certificate in ${CERT_DIR}, but FAKE_GOOGLE_HOSTS is set. The certs service must run first.`);
    process.exit(1);
} else {
    console.log(`No certificate in ${CERT_DIR}; HTTPS disabled (fine for running the fake on its own).`);
}

const proxy = http.createServer((_req, res) => {
    res.writeHead(405);
    res.end('fake-google proxy: CONNECT only');
});
/** The host of a CONNECT target (`host:port` or `[v6]:port`), lowercased and without a trailing dot. */
function connectHost(target: string): string {
    let host = target;
    try {
        host = new URL(`http://${target}`).hostname;
    } catch {
        // Not a valid authority; compared as given, it matches nothing.
    }
    return host.toLowerCase().replace(/\.$/, '');
}

// The proxy only tunnels the faked Google hosts. Everything else Mattermost tries to reach (notices,
// push, marketplace, ...) is refused at once, so the suite never depends on the internet and an
// offline run does not wait on timeouts. An unfaked Google host is also a contract violation.
proxy.on('connect', (req: http.IncomingMessage, client: net.Socket, head: Buffer) => {
    const host = connectHost(req.url ?? '');
    client.on('error', () => client.destroy());
    if (!FAKE_HOSTS.has(host)) {
        if (GOOGLE_DOMAIN.test(host)) {
            violation(`Mattermost called ${host}, which the fake does not answer for. Add it to FAKE_GOOGLE_HOSTS in e2e/playwright/docker-compose.yml and route it in fake-google/server.ts.`);
        } else {
            console.log(`fake Google proxy: refused ${req.url} (not a faked Google host)`);
        }
        client.end(`HTTP/1.1 403 fake-google proxy only tunnels ${[...FAKE_HOSTS].join(', ')}\r\n\r\n`);
        return;
    }
    const upstream = net.connect(HTTPS_PORT, '127.0.0.1', () => {
        client.write('HTTP/1.1 200 Connection Established\r\n\r\n');
        upstream.write(head);
        upstream.pipe(client);
        client.pipe(upstream);
    });
    upstream.on('error', () => client.destroy());
    client.on('error', () => upstream.destroy());
});
proxy.listen(PROXY_PORT, () => console.log(`fake Google proxy on http://0.0.0.0:${boundPort(proxy)} for ${[...FAKE_HOSTS].join(', ') || 'no hosts'}`));
