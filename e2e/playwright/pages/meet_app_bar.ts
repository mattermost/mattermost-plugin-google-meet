// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import type {ChannelsPage} from '@mattermost/playwright-lib';
import type {Locator} from '@playwright/test';

import {PLUGIN_ID} from '../lib/meet_plugin';

/**
 * The Start Google Meet button. The plugin registers a channel header button action
 * (webapp/src/index.tsx); Mattermost 12 shows it in the app bar on the right. The app bar icon has
 * no accessible name, so it is found by the id the app bar gives it.
 */
export class MeetAppBar {
    constructor(private readonly channelsPage: ChannelsPage) {}

    get startMeetingButton(): Locator {
        return this.channelsPage.page.locator(`[id="app-bar-icon-${PLUGIN_ID}"]`);
    }

    /** Clicks Start Google Meet and waits for the plugin's POST /meeting to return. */
    async startMeeting() {
        const page = this.channelsPage.page;
        const created = page.waitForResponse((r) => r.url().endsWith(`/plugins/${PLUGIN_ID}/api/v1/meeting`) && r.request().method() === 'POST');
        await this.startMeetingButton.click();
        await created;
    }
}
