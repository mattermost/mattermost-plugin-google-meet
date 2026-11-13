// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import type {ChannelsPage} from '@mattermost/playwright-lib';

/**
 * Waits until the plugin's webapp has initialized. Its initialize (webapp/src/index.tsx) awaits
 * GET config/status before registering anything, so the channel can be visible while the Start
 * Google Meet button and the meeting_started websocket handler (which opens the meeting tab) do not
 * exist yet. Everything after that await is registered synchronously, so once the meeting post type
 * is in the store, the rest is too. Call it after the channels page is visible.
 */
export async function waitForMeetWebapp(channelsPage: ChannelsPage) {
    await channelsPage.page.waitForFunction(() => {
        const store = (window as any).store;
        return Boolean(store?.getState()?.plugins?.postTypes?.custom_google_meet);
    });
}

/**
 * Replaces window.open in the tab with a recorder, so a test can tell which tab the meeting_started
 * websocket handler (webapp/src/websocket/meeting_started.ts) opened the meeting from. It opens with
 * noopener, so the new tab's opener can't tell. Returns a function that reads the URLs opened so far.
 */
export async function recordWindowOpen(channelsPage: ChannelsPage): Promise<() => Promise<string[]>> {
    await channelsPage.page.evaluate(() => {
        const opened: string[] = [];
        (window as any).meetOpened = opened;
        window.open = (url?: string | URL) => {
            opened.push(String(url));
            return null;
        };
    });
    return () => channelsPage.page.evaluate(() => (window as any).meetOpened as string[]);
}
