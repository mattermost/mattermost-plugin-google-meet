// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

const authUserParameterName = 'authuser';
const authUserPattern = /^\d+$/;

export interface MeetingLinks {
    displayURL: string;
    targetURL: string;
}

function parseGoogleMeetURL(rawURL: string): URL | undefined {
    let url: URL;
    try {
        url = new URL(rawURL);
    } catch {
        return undefined;
    }

    if (
        url.protocol !== 'https:' ||
        url.hostname !== 'meet.google.com' ||
        url.port !== '' ||
        url.username !== '' ||
        url.password !== ''
    ) {
        return undefined;
    }

    return url;
}

function normalizeAuthUser(value: string): string {
    return authUserPattern.test(value) ? value : '';
}

export function isAllowedGoogleMeetURL(rawURL: string): boolean {
    return Boolean(parseGoogleMeetURL(rawURL));
}

export function getMeetingLinks(rawURL: string, authUser: string): MeetingLinks {
    const url = parseGoogleMeetURL(rawURL);
    if (!url) {
        return {
            displayURL: rawURL,
            targetURL: rawURL,
        };
    }

    for (const key of Array.from(url.searchParams.keys())) {
        if (key.toLowerCase() === authUserParameterName) {
            url.searchParams.delete(key);
        }
    }

    const displayURL = url.toString();
    const normalizedAuthUser = normalizeAuthUser(authUser);
    if (normalizedAuthUser) {
        url.searchParams.set(authUserParameterName, normalizedAuthUser);
    }

    return {
        displayURL,
        targetURL: url.toString(),
    };
}
