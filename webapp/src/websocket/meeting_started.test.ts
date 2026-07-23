// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {handleMeetingStarted} from './meeting_started';

function meetingStartedMessage(url: string): Parameters<typeof handleMeetingStarted>[0] {
    return {
        data: {
            meeting_url: url,
        },
    } as Parameters<typeof handleMeetingStarted>[0];
}

describe('handleMeetingStarted', () => {
    afterEach(() => {
        jest.restoreAllMocks();
    });

    test('opens the viewer-specific Meet URL', () => {
        const open = jest.spyOn(window, 'open').mockImplementation(() => null);

        handleMeetingStarted(meetingStartedMessage('https://meet.google.com/abc-defg-hij'), '1');

        expect(open).toHaveBeenCalledWith(
            'https://meet.google.com/abc-defg-hij?authuser=1',
            '_blank',
            'noopener,noreferrer',
        );
    });

    test('opens the canonical URL when authuser is unset', () => {
        const open = jest.spyOn(window, 'open').mockImplementation(() => null);

        handleMeetingStarted(meetingStartedMessage('https://meet.google.com/abc-defg-hij'));

        expect(open).toHaveBeenCalledWith(
            'https://meet.google.com/abc-defg-hij',
            '_blank',
            'noopener,noreferrer',
        );
    });

    test('rejects non-Meet URLs', () => {
        const open = jest.spyOn(window, 'open').mockImplementation(() => null);

        handleMeetingStarted(meetingStartedMessage('https://example.com/abc-defg-hij'), '1');

        expect(open).not.toHaveBeenCalled();
    });
});
