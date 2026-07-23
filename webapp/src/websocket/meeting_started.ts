// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {getMeetingLinks, isAllowedGoogleMeetURL} from 'utils/meet_url';

import type {WebSocketMessage} from '@mattermost/client';

type MeetingStartedPayload = {
    meeting_url?: string;
};

export function handleMeetingStarted(msg: WebSocketMessage<MeetingStartedPayload>, authUser = ''): void {
    const url = msg.data?.meeting_url;
    if (!url || typeof url !== 'string' || !isAllowedGoogleMeetURL(url)) {
        return;
    }

    const {targetURL} = getMeetingLinks(url, authUser);
    window.open(targetURL, '_blank', 'noopener,noreferrer');
}
