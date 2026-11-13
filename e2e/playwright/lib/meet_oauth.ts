// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// The browser half of the Google OAuth flow. The plugin's server reaches the fake through the
// proxy (docker-compose.yml); the browser is not behind it, so these route Google's browser-only
// hosts to the fake. Adapted from mattermost-plugin-google-drive e2e/playwright/lib/drive_oauth.ts.

import type {APIRequestContext, BrowserContext} from '@playwright/test';

import {PLUGIN_ID} from './meet_plugin';
import {fakeGoogleURL} from './server';

export const GOOGLE_AUTH_URL = 'https://accounts.google.com/o/oauth2/v2/auth';

export const SCOPES = {
    created: 'https://www.googleapis.com/auth/meetings.space.created',
    readonly: 'https://www.googleapis.com/auth/meetings.space.readonly',
    calendar: 'https://www.googleapis.com/auth/calendar.events.readonly',
};

export const oauthConnectPath = `/plugins/${PLUGIN_ID}/api/v1/oauth/connect`;
export const oauthCallbackPath = `/plugins/${PLUGIN_ID}/api/v1/oauth/callback`;

/**
 * Captures where the plugin's connect endpoint redirects the browser, without contacting Google.
 *
 * Playwright's route() does not see requests that result from a redirect, so intercepting
 * accounts.google.com would miss the 302 from the plugin. Instead this intercepts the plugin's
 * /oauth/connect request, sends it to the server without following redirects, records the
 * Location header and answers the browser with a blank page.
 */
export async function captureGoogleConsentRedirect(context: BrowserContext): Promise<() => Promise<URL>> {
    let resolveUrl: (url: URL) => void;
    let rejectUrl: (err: Error) => void;
    const captured = new Promise<URL>((resolve, reject) => {
        resolveUrl = resolve;
        rejectUrl = reject;
    });
    await context.route(`**${oauthConnectPath}`, async (route) => {
        const response = await route.fetch({maxRedirects: 0});
        const location = response.headers().location;
        if (response.status() !== 302 || !location) {
            rejectUrl(new Error(`Expected a 302 from ${oauthConnectPath}, got ${response.status()}`));
            await route.fulfill({response});
            return;
        }
        resolveUrl(new URL(location));
        await route.fulfill({status: 200, contentType: 'text/html', body: '<html><body>Google consent intercepted by e2e test</body></html>'});
    });
    return () => captured;
}

/**
 * Serves Google's sign-in and consent pages from the fake, so the browser can complete the whole
 * OAuth flow: the plugin's /oauth/connect redirect is answered with the fake's account chooser, and
 * the consent form posts go to the fake, whose final 302 sends the browser to the plugin's
 * /oauth/callback with a code the fake will exchange. The pages stay on the Mattermost origin,
 * which is why their relative form posts (/o/oauth2/consent) are routed too.
 */
export async function routeGoogleSignInToFake(context: BrowserContext) {
    await context.route(`**${oauthConnectPath}`, async (route) => {
        const response = await route.fetch({maxRedirects: 0});
        const location = response.headers().location;
        if (response.status() !== 302 || !location?.startsWith(GOOGLE_AUTH_URL)) {
            await route.fulfill({response});
            return;
        }
        const consent = new URL(location);
        await route.fulfill({response: await route.fetch({url: `${fakeGoogleURL()}${consent.pathname}${consent.search}`, maxRedirects: 0})});
    });
    await context.route('**/o/oauth2/consent', async (route) => {
        await route.fulfill({response: await route.fetch({url: `${fakeGoogleURL()}/o/oauth2/consent`, maxRedirects: 0})});
    });
}

/**
 * Answers every https://meet.google.com page the browser opens (the auto-opened meeting tab and
 * the join links) with a stub, so tests never load real Google. Returns the URLs it served.
 */
export async function stubMeetJoinPages(context: BrowserContext): Promise<string[]> {
    const opened: string[] = [];
    await context.route('https://meet.google.com/**', async (route) => {
        opened.push(route.request().url());
        await route.fulfill({status: 200, contentType: 'text/html', body: '<html><head><title>Google Meet (stub)</title></head><body>Google Meet stub</body></html>'});
    });
    return opened;
}

/** Calls the plugin's OAuth callback the way Google's redirect would. Returns the HTTP status and body text. */
export async function callOAuthCallback(request: APIRequestContext, params: {code?: string; state?: string}): Promise<{status: number; body: string}> {
    const query = new URLSearchParams();
    if (params.code !== undefined) {
        query.set('code', params.code);
    }
    if (params.state !== undefined) {
        query.set('state', params.state);
    }
    const response = await request.get(`${oauthCallbackPath}?${query.toString()}`, {maxRedirects: 0});
    return {status: response.status(), body: (await response.text()).trim()};
}

/** Where the plugin's connect endpoint sends the browser, read with the session of `request`. */
export async function connectRedirect(request: APIRequestContext): Promise<URL> {
    const response = await request.get(oauthConnectPath, {maxRedirects: 0});
    const location = response.headers().location;
    if (response.status() !== 302 || !location) {
        throw new Error(`Expected a 302 from ${oauthConnectPath}, got ${response.status()}: ${await response.text()}`);
    }
    return new URL(location);
}

/** The scopes a Google consent URL asks for, sorted. */
export const requestedScopes = (consentUrl: URL) => (consentUrl.searchParams.get('scope') ?? '').split(' ').filter(Boolean).sort();
