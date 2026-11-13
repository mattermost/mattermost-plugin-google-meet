// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import type {ChannelsPage} from '@mattermost/playwright-lib';

/** The `/meet` slash command as typed into the channel message box (server/command/command.go). */
export class MeetSlashCommand {
    static readonly trigger = '/meet';

    constructor(private readonly channelsPage: ChannelsPage) {}

    /**
     * Sends `/meet [...args]` and waits for the server to finish handling it (same approach as
     * mattermost-server's demo plugin specs, sendDemoSlashCommand).
     */
    async run(...args: string[]) {
        const page = this.channelsPage.page;
        const executed = page.waitForResponse((r) => r.url().includes('/api/v4/commands/execute') && r.request().method() === 'POST');
        const postCreate = this.channelsPage.centerView.postCreate;
        await postCreate.input.fill([MeetSlashCommand.trigger, ...args].join(' '));
        await postCreate.sendMessage();
        await executed;
    }
}
