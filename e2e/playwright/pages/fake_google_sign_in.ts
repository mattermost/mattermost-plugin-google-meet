// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import type {Locator, Page} from '@playwright/test';

/**
 * The fake Google's account chooser and granular consent pages (fake-google/server.ts authorize,
 * consent), served in the browser by lib/meet_oauth.ts routeGoogleSignInToFake, and the plugin's
 * OAuth callback page the flow ends on (server/api.go handleOAuthCallback).
 */
export class FakeGoogleSignIn {
    constructor(private readonly page: Page) {}

    get accountChooser(): Locator {
        return this.page.getByRole('heading', {name: 'Choose an account'});
    }

    get consent(): Locator {
        return this.page.getByRole('heading', {name: /wants access to your Google Account$/});
    }

    /** Picks the account by email, clears the given scopes on the consent screen and continues. */
    async signIn(email: string, options: {decline?: string[]} = {}) {
        await this.accountChooser.waitFor();
        await this.page.getByRole('button', {name: new RegExp(`\\(${email.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\)$`, 'i')}).click();
        await this.consent.waitFor();
        for (const scope of options.decline ?? []) {
            await this.page.getByRole('checkbox', {name: scope}).uncheck();
        }
        await this.page.getByRole('button', {name: 'Continue'}).click();
    }

    /** The plugin's page after a successful connection. */
    get connected(): Locator {
        return this.page.getByRole('heading', {name: 'Successfully connected to Google!'});
    }

    /** The plugin's callback response body (an error is JSON text). */
    get callbackBody(): Locator {
        return this.page.locator('body');
    }
}
