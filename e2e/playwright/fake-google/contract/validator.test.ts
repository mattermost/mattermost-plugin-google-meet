// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Self-tests for the contract checker, so a bug in it cannot quietly pass every fake response
// (same idea as mattermost-plugin-zoom e2e/playwright/tests/contract.spec.ts). Run by
// `npm run test:contract`, part of `npm run check`.

import assert from 'node:assert/strict';
import {test} from 'node:test';

import {missingPaths} from './required.ts';
import {ContractValidator} from './validator.ts';

const contract = new ContractValidator();

test('matches the Meet and Calendar routes the plugin calls to their documented methods', () => {
    const cases: Array<[string, string, string]> = [
        ['POST', '/v2/spaces', 'meet.spaces.create'],
        ['GET', '/v2/spaces/abc-mnop-xyz', 'meet.spaces.get'],
        ['GET', '/v2/conferenceRecords', 'meet.conferenceRecords.list'],
        ['GET', '/v2/conferenceRecords/rec1', 'meet.conferenceRecords.get'],
        ['GET', '/v2/conferenceRecords/rec1/participants/p1', 'meet.conferenceRecords.participants.get'],
        ['GET', '/v2/conferenceRecords/rec1/recordings', 'meet.conferenceRecords.recordings.list'],
        ['GET', '/v2/conferenceRecords/rec1/transcripts', 'meet.conferenceRecords.transcripts.list'],
        ['GET', '/v2/conferenceRecords/rec1/transcripts/t1/entries', 'meet.conferenceRecords.transcripts.entries.list'],
        ['GET', '/v2/conferenceRecords/rec1/smartNotes', 'meet.conferenceRecords.smartNotes.list'],
        ['GET', '/calendar/v3/calendars/primary/events', 'calendar.events.list'],
    ];
    for (const [method, path, id] of cases) {
        assert.equal(contract.match(method, path)?.id, id, `${method} ${path}`);
    }
});

test('flags routes missing from the contract', () => {
    assert.equal(contract.match('DELETE', '/v2/spaces/abc'), null);
    assert.equal(contract.match('GET', '/v2/conferenceRecords/rec1/participants'), null);
});

test('accepts a well-formed space and rejects fields Google does not document', () => {
    const match = contract.match('POST', '/v2/spaces')!;
    const ok = {name: 'spaces/abc', meetingUri: 'https://meet.google.com/abc-mnop-xyz', meetingCode: 'abc-mnop-xyz'};
    assert.deepEqual(contract.validateResponse(match, new URLSearchParams(), 200, ok), []);

    const invented = contract.validateResponse(match, new URLSearchParams(), 200, {...ok, joinUrl: 'x'});
    assert.match(invented.join('\n'), /has field "joinUrl"/);
});

test('rejects fields with the wrong type or a malformed timestamp', () => {
    const match = contract.match('GET', '/v2/conferenceRecords/rec1')!;
    const errors = contract.validateResponse(match, new URLSearchParams(), 200, {name: 42, space: 'spaces/abc', startTime: 'yesterday'});
    assert.match(errors.join('\n'), /\/name must be string/);
    assert.match(errors.join('\n'), /\/startTime must be an RFC 3339 date-time/);
});

test('rejects a speaker name on transcript entries, which Google keeps on the participant', () => {
    const match = contract.match('GET', '/v2/conferenceRecords/rec1/transcripts/t1/entries')!;
    const entry = {name: 'e1', participant: 'conferenceRecords/rec1/participants/p1', text: 'Hi', startTime: '2026-01-01T00:00:00Z', endTime: '2026-01-01T00:00:01Z'};
    assert.deepEqual(contract.validateResponse(match, new URLSearchParams(), 200, {transcriptEntries: [entry]}), []);
    const invented = contract.validateResponse(match, new URLSearchParams(), 200, {transcriptEntries: [{...entry, participantDevice: {displayName: 'Alice'}}]});
    assert.match(invented.join('\n'), /has field "participantDevice"/);
});

test('rejects artifact states outside the documented enum', () => {
    const match = contract.match('GET', '/v2/conferenceRecords/rec1/recordings')!;
    const errors = contract.validateResponse(match, new URLSearchParams(), 200, {recordings: [{name: 'r', state: 'DONE'}]});
    assert.match(errors.join('\n'), /must be one of .*FILE_GENERATED/);
});

test('reports response fields the plugin reads when they are missing', () => {
    const match = contract.match('POST', '/v2/spaces')!;
    const errors = contract.validateResponse(match, new URLSearchParams(), 200, {name: 'spaces/abc'});
    assert.match(errors.join('\n'), /missing meetingUri/);
});

test('treats an omitted list as empty, since Google leaves out empty repeated fields', () => {
    const match = contract.match('GET', '/v2/conferenceRecords')!;
    assert.deepEqual(contract.validateResponse(match, new URLSearchParams(), 200, {}), []);
    assert.deepEqual(missingPaths({}, ['items[].id']), []);
    assert.deepEqual(missingPaths({}, ['name']), ['name']);
});

test('rejects undocumented query parameters and values outside a parameter enum', () => {
    const match = contract.match('GET', '/calendar/v3/calendars/primary/events')!;
    const errors = contract.validateRequest(match, new URLSearchParams({orderBy: 'created', bogus: '1'}), undefined);
    assert.match(errors.join('\n'), /undocumented query parameter "bogus"/);
    assert.match(errors.join('\n'), /"orderBy" must be one of/);
});

test('checks error responses for the google.rpc.Status fields', () => {
    const match = contract.match('POST', '/v2/spaces')!;
    assert.deepEqual(contract.validateResponse(match, new URLSearchParams(), 403, {error: {code: 403, message: 'x', status: 'PERMISSION_DENIED'}}), []);
    assert.match(contract.validateResponse(match, new URLSearchParams(), 403, {message: 'x'}).join('\n'), /missing error.code/);
});
