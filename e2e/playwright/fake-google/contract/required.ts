// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Hand-written contracts for what Discovery cannot express. Adapted from
// mattermost-plugin-google-drive e2e/playwright/fake-google/contract/required.ts.
//
// Discovery never marks response fields as required, so a fake that leaves out a field the plugin
// reads would still pass the schema. These lists name those fields, per method. Each path uses `[]`
// to mean "every element".
//
// The OAuth token endpoint has no Discovery document; its shape is modelled on RFC 6749 and Google's
// OAuth 2.0 web-server guide.

type Rule = {when: (query: URLSearchParams) => boolean; paths: string[]; why: string};

const always = () => true;

export const REQUIRED_RESPONSE_FIELDS: Record<string, Rule[]> = {
    'meet.spaces.create': [{
        when: always,
        paths: ['name', 'meetingUri', 'meetingCode'],
        why: 'meeting post link and ad-hoc space tracking (google.go createMeeting, meeting.go)',
    }],
    'meet.spaces.get': [{when: always, paths: ['name', 'meetingCode'], why: 'subscription add (subscription.go)'}],
    'meet.conferenceRecords.list': [{
        when: always,
        paths: ['conferenceRecords[].name', 'conferenceRecords[].startTime', 'conferenceRecords[].space'],
        why: 'poller (poll.go)',
    }],
    'meet.conferenceRecords.get': [{when: always, paths: ['name', 'startTime', 'space'], why: 'poller (poll.go)'}],
    // The display name is on whichever of signedinUser, anonymousUser or phoneUser is set, which a
    // path list cannot express; smoke.test.ts checks the fake sets exactly one.
    'meet.conferenceRecords.participants.get': [{when: always, paths: ['name'], why: 'transcript.vtt speaker names (artifact_post.go)'}],
    'meet.conferenceRecords.recordings.list': [{when: always, paths: ['recordings[].name', 'recordings[].state'], why: 'artifact posts (artifact_post.go)'}],
    'meet.conferenceRecords.transcripts.list': [{when: always, paths: ['transcripts[].name', 'transcripts[].state'], why: 'artifact posts (artifact_post.go)'}],
    'meet.conferenceRecords.smartNotes.list': [{when: always, paths: ['smartNotes[].name', 'smartNotes[].state'], why: 'artifact posts (artifact_post.go)'}],
    'meet.conferenceRecords.transcripts.entries.list': [{
        when: always,
        paths: ['transcriptEntries[].text', 'transcriptEntries[].startTime', 'transcriptEntries[].endTime'],
        why: 'transcript.vtt (artifact_post.go)',
    }],
    'calendar.events.list': [{when: always, paths: ['items[].id', 'items[].status'], why: 'calendar schedule sync (calendar.go)'}],
};

/** RFC 6749 token response fields; refresh_token only for the authorization_code grant. */
export function tokenResponseFields(grantType: string): string[] {
    const fields = ['access_token', 'token_type', 'expires_in', 'scope'];
    return grantType === 'authorization_code' ? [...fields, 'refresh_token'] : fields;
}

/** Paths in `paths` that are missing or null in `value`. `[]` checks every element of an array. */
export function missingPaths(value: unknown, paths: string[]): string[] {
    return paths.filter((p) => !present(value, p.split('.')));
}

function present(value: unknown, parts: string[]): boolean {
    if (parts.length === 0) {
        return value !== undefined && value !== null;
    }
    const [head, ...rest] = parts;
    const isArray = head.endsWith('[]');
    const key = isArray ? head.slice(0, -2) : head;
    if (value === null || typeof value !== 'object') {
        return false;
    }
    if (!(key in value)) {
        // Google leaves empty repeated fields out of its JSON, so a missing array has no elements
        // to check. A missing scalar or object is a violation.
        return isArray;
    }
    const child = (value as Record<string, unknown>)[key];
    if (!isArray) {
        return present(child, rest);
    }
    return Array.isArray(child) && child.every((item) => present(item, rest));
}
