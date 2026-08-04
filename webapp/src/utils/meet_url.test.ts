// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {getMeetingLinks, isAllowedGoogleMeetURL} from './meet_url';

describe('getMeetingLinks', () => {
    test('adds authuser only to the target URL', () => {
        expect(getMeetingLinks('https://meet.google.com/abc-defg-hij', '1')).toEqual({
            displayURL: 'https://meet.google.com/abc-defg-hij',
            targetURL: 'https://meet.google.com/abc-defg-hij?authuser=1',
        });
    });

    test('removes existing authuser values from the display URL', () => {
        expect(getMeetingLinks(
            'https://meet.google.com/abc-defg-hij?foo=bar&authUser=3&authuser=2#section',
            '1',
        )).toEqual({
            displayURL: 'https://meet.google.com/abc-defg-hij?foo=bar#section',
            targetURL: 'https://meet.google.com/abc-defg-hij?foo=bar&authuser=1#section',
        });
    });

    test('leaves authuser unset for an invalid preference', () => {
        expect(getMeetingLinks('https://meet.google.com/abc-defg-hij?authuser=2', '-1')).toEqual({
            displayURL: 'https://meet.google.com/abc-defg-hij',
            targetURL: 'https://meet.google.com/abc-defg-hij',
        });
    });

    test('leaves non-Meet URLs unchanged', () => {
        const url = 'https://example.com/abc?authuser=2';
        expect(getMeetingLinks(url, '1')).toEqual({
            displayURL: url,
            targetURL: url,
        });
    });
});

describe('isAllowedGoogleMeetURL', () => {
    test.each([
        'https://meet.google.com/abc-defg-hij',
        'https://meet.google.com/abc-defg-hij?authuser=1',
    ])('allows %s', (url) => {
        expect(isAllowedGoogleMeetURL(url)).toBe(true);
    });

    test.each([
        'http://meet.google.com/abc-defg-hij',
        'https://meet.google.com.example.com/abc-defg-hij',
        'https://user@meet.google.com/abc-defg-hij',
        'https://meet.google.com:444/abc-defg-hij',
        'not a url',
    ])('rejects %s', (url) => {
        expect(isAllowedGoogleMeetURL(url)).toBe(false);
    });
});
