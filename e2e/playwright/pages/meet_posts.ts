// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import type {ChannelsPage} from '@mattermost/playwright-lib';
import type {Locator, Page} from '@playwright/test';

/**
 * The plugin's posts in the center channel: the replies to `/meet` and to the Start Google Meet
 * button (ephemeral posts), and the meeting post (webapp/src/components/post_type_meeting.tsx).
 * Texts from server/command/command.go and server/api.go.
 */
export class MeetPosts {
    constructor(private readonly channelsPage: ChannelsPage) {}

    private get page(): Page {
        return this.channelsPage.page;
    }

    /** Every post in the current channel that contains the given text. */
    postsWithText(text: string | RegExp): Locator {
        return this.channelsPage.centerView.container.getByRole('listitem').filter({hasText: text});
    }

    /** Last post in the current channel that contains the given text. */
    postWithText(text: string | RegExp): Locator {
        return this.postsWithText(text).last();
    }

    // Account connection (/meet connect, /meet disconnect)

    get connectLink(): Locator {
        return this.postWithText('Connect or reconnect your Google account.').getByRole('link', {name: 'Click here to continue'});
    }

    /** Follows the link `/meet connect` posts, which opens the Google OAuth flow in a new tab. */
    async openConnectLink(): Promise<Page> {
        const [tab] = await Promise.all([this.page.context().waitForEvent('page'), this.connectLink.click()]);
        await tab.waitForLoadState();
        return tab;
    }

    get disconnected(): Locator {
        return this.postWithText('Your Google account has been disconnected.');
    }

    // Replies that stop a command or a button press

    get notConfiguredForUser(): Locator {
        return this.postWithText('The Google Meet plugin is not configured. Please contact your system administrator.');
    }

    get notConfiguredForAdmin(): Locator {
        return this.postWithText('The Google Meet plugin is not configured. Configure it in the System Console.');
    }

    get configureLink(): Locator {
        return this.notConfiguredForAdmin.getByRole('link', {name: 'Configure it in the System Console'});
    }

    get needsConnect(): Locator {
        return this.postWithText('You need to connect your Google account first.');
    }

    /** Every "connect first" reply in the channel, to count them. */
    get allNeedsConnect(): Locator {
        return this.postsWithText('You need to connect your Google account first.');
    }

    get needsConnectLink(): Locator {
        return this.needsConnect.getByRole('link', {name: 'Click here to connect'});
    }

    get needsReconnect(): Locator {
        return this.postWithText('Your Google account needs to be reconnected.');
    }

    /** `/meet start` in a public channel with Restrict Meeting Creation on. */
    get restrictedFromCommand(): Locator {
        return this.postWithText('Meeting creation is restricted in public channels. Try a private channel or direct message instead.');
    }

    /** The Start Google Meet button in a public channel with Restrict Meeting Creation on. */
    get restrictedFromButton(): Locator {
        // The button's reply (server/api.go) is a prefix of the command's (server/command/command.go).
        return this.postsWithText('Meeting creation is restricted in public channels.').filter({hasNotText: 'Try a private channel'}).last();
    }

    get meetingFailed(): Locator {
        return this.postWithText('Failed to create meeting. Please try again or check the server logs.');
    }

    // /meet config authuser

    authUserSaved(index: number): Locator {
        return this.postWithText(`Google Meet links will now use account index ${index}.`);
    }

    get authUserInvalid(): Locator {
        return this.postWithText('The authuser value must be a non-negative whole number.');
    }

    get configUsage(): Locator {
        return this.postWithText('Usage: /meet config authuser <number>.');
    }

    unknownConfigSetting(name: string): Locator {
        return this.postWithText(`Unknown config setting: ${name}. Use authuser.`);
    }

    // /meet help

    get help(): Locator {
        return this.postWithText('Google Meet commands:');
    }

    // The meeting post

    /** The newest meeting post, by the text the plugin posts for the user who started it. */
    get meetingPost(): Locator {
        return this.postWithText('I have started a meeting');
    }

    meetingTitle(post: Locator = this.meetingPost): Locator {
        return post.getByRole('heading', {level: 5});
    }

    /** The "Meeting URL:" link; its text is the URL without authuser. */
    meetingUrlLink(post: Locator = this.meetingPost): Locator {
        return post.getByRole('link', {name: /^https:\/\/meet\.google\.com\//});
    }

    joinButton(post: Locator = this.meetingPost): Locator {
        return post.getByRole('link', {name: 'Join meeting'});
    }
}
