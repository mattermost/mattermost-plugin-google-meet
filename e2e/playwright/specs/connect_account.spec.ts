// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {fakeGoogle} from '../lib/fake_google';
import {connectUser, isConnected, storedToken} from '../lib/kv';
import {
    callOAuthCallback,
    captureGoogleConsentRedirect,
    connectRedirect,
    GOOGLE_AUTH_URL,
    oauthCallbackPath,
    requestedScopes,
    routeGoogleSignInToFake,
    SCOPES,
} from '../lib/meet_oauth';
import {ensureMeetPlugin, FAKE_OAUTH_CLIENT, UNCONFIGURED, updateMeetPluginSettings} from '../lib/meet_plugin';
import {expect, test} from '../lib/test';
import {FakeGoogleSignIn} from '../pages/fake_google_sign_in';
import {MeetPosts} from '../pages/meet_posts';
import {MeetSlashCommand} from '../pages/meet_slash_command';
import {waitForMeetWebapp} from '../pages/meet_webapp';

const OWNER_EMAIL = 'meet-owner@fake-google.test';

test.describe('connect a Google account', {tag: '@contract-fake'}, () => {
    /**
     * @objective Verify that the connect link sends the user to Google consent with the plugin's client and callback, offline access, forced consent, the Meet scopes and a random state
     */
    test('redirects to Google consent with offline access and the Meet scopes when connecting', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and log in as a regular user
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        const {context, channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);

        // # Run the connect command and follow its link, stopping before Google loads
        const consentRedirect = await captureGoogleConsentRedirect(context);
        await new MeetSlashCommand(channelsPage).run('connect');
        await new MeetPosts(channelsPage).openConnectLink();
        const consentUrl = await consentRedirect();
        const params = consentUrl.searchParams;

        // * Verify the redirect targets Google's OAuth endpoint with the configured client and the plugin's callback
        expect(`${consentUrl.origin}${consentUrl.pathname}`).toBe(GOOGLE_AUTH_URL);
        expect(params.get('client_id')).toBe(FAKE_OAUTH_CLIENT.id);
        expect(params.get('response_type')).toBe('code');
        expect(params.get('redirect_uri')).toBe(`${process.env.PW_BASE_URL}${oauthCallbackPath}`);

        // * Verify offline access with forced consent, so Google returns a refresh token
        expect(params.get('access_type')).toBe('offline');
        expect(params.get('prompt')).toBe('consent');

        // * Verify the default settings request creating spaces and reading conference records, and no calendar access
        expect(requestedScopes(consentUrl)).toEqual([SCOPES.created, SCOPES.readonly].sort());

        // * Verify the state is 16 random bytes in hex
        expect(params.get('state')).toMatch(/^[0-9a-f]{32}$/);
    });

    /**
     * @objective Verify that the requested scopes follow the settings: no readonly scope without artifact posts, and the calendar scope with schedule sync
     */
    test('requests only the scopes the enabled features need', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin with artifact posts off and log in as a regular user
        const {adminClient, user} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, {enableConferenceArtifactPosts: false});
        const {page} = await pw.testBrowser.login(user);

        // * Verify only meetings.space.created is requested
        expect(requestedScopes(await connectRedirect(page.request))).toEqual([SCOPES.created]);

        // # Turn artifact posts and calendar schedule sync on
        await updateMeetPluginSettings(adminClient, {enableConferenceArtifactPosts: true, enableCalendarScheduleSync: true});

        // * Verify the readonly and calendar scopes are requested too
        expect(requestedScopes(await connectRedirect(page.request))).toEqual([SCOPES.created, SCOPES.readonly, SCOPES.calendar].sort());
    });

    /**
     * @objective Verify that signing in and allowing access connects the account: the plugin exchanges the code and stores Google's token pair, encrypted
     *
     * @precondition
     * The browser's Google sign-in and consent pages are served by the fake Google
     */
    test('connects a Google account through sign-in and consent', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and log in as a user with no Google account connected
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        const {context, channelsPage} = await pw.testBrowser.login(user);
        await routeGoogleSignInToFake(context);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);

        // # Run the connect command, follow the link, pick the owner's Google account and allow access
        await new MeetSlashCommand(channelsPage).run('connect');
        const signIn = new FakeGoogleSignIn(await new MeetPosts(channelsPage).openConnectLink());
        await signIn.signIn(OWNER_EMAIL);

        // * Verify the plugin confirms the connection
        await expect(signIn.connected).toBeVisible();

        // * Verify the plugin exchanged the authorization code once
        const exchanges = (await fakeGoogle.requests()).filter((r) => r.path === '/token');
        expect(exchanges.map((r) => [r.methodId, r.status])).toEqual([['authorization_code', 200]]);

        // * Verify the stored token is the fake's token pair, with an expiry about an hour away
        // (the server sets it from its own clock, so the bounds allow a minute of skew with this host)
        const token = await storedToken(user.id);
        expect(token?.access_token).toMatch(/^ya29\.fake-/);
        expect(token?.refresh_token).toMatch(/^1\/\/fake-/);
        const expiresInMinutes = (Date.parse(token!.expiry) - Date.now()) / 60_000;
        expect(expiresInMinutes).toBeGreaterThan(54);
        expect(expiresInMinutes).toBeLessThanOrEqual(61);
    });

    /**
     * @objective Verify that declining a scope the plugin cannot work without fails the connection, while declining the optional calendar scope still connects
     *
     * @precondition
     * Calendar schedule sync is on, so the consent screen offers the calendar scope too
     */
    test('rejects a consent missing a mandatory scope and accepts one without the calendar scope', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin with calendar schedule sync and log in
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, {enableCalendarScheduleSync: true});
        const {context, channelsPage} = await pw.testBrowser.login(user);
        await routeGoogleSignInToFake(context);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        const slashCommand = new MeetSlashCommand(channelsPage);
        const posts = new MeetPosts(channelsPage);

        // # Connect, clearing the readonly scope on the consent screen
        await slashCommand.run('connect');
        const declined = new FakeGoogleSignIn(await posts.openConnectLink());
        await declined.signIn(OWNER_EMAIL, {decline: [SCOPES.readonly]});

        // * Verify the plugin refuses the connection and says why
        await expect(declined.callbackBody).toContainText('Google connection requires granting all requested Meet permissions.');
        expect(await isConnected(user.id)).toBe(false);

        // # Connect again, clearing only the calendar scope
        await slashCommand.run('connect');
        const withoutCalendar = new FakeGoogleSignIn(await posts.openConnectLink());
        await withoutCalendar.signIn(OWNER_EMAIL, {decline: [SCOPES.calendar]});

        // * Verify the account is connected
        await expect(withoutCalendar.connected).toBeVisible();
        expect(await isConnected(user.id)).toBe(true);
    });

    /**
     * @objective Verify that the OAuth callback rejects a missing code or state, an unknown state, and a state that was already used
     */
    test('rejects OAuth callbacks with a missing parameter, an unknown state or a reused state', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and log in as a regular user
        const {adminClient, user} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        const {page} = await pw.testBrowser.login(user);

        // # Call the callback without a code, and without a state
        const noCode = await callOAuthCallback(page.request, {state: 'abc'});
        const noState = await callOAuthCallback(page.request, {code: 'abc'});

        // * Verify both are rejected as bad requests
        for (const response of [noCode, noState]) {
            expect(response.status).toBe(400);
            expect(response.body).toContain('Missing code or state parameter.');
        }

        // # Call the callback with a state the plugin never issued
        const unknown = await callOAuthCallback(page.request, {code: 'abc', state: '0'.repeat(32)});

        // * Verify it is rejected
        expect(unknown.status).toBe(400);
        expect(unknown.body).toContain('Invalid or expired state. Please try connecting again.');

        // # Start connecting to get a real state, then use it twice with a code Google never issued
        const state = (await connectRedirect(page.request)).searchParams.get('state')!;
        const first = await callOAuthCallback(page.request, {code: 'not-issued', state});
        const second = await callOAuthCallback(page.request, {code: 'not-issued', state});

        // * Verify the first use reaches the token exchange, which Google refuses
        expect(first.status).toBe(500);
        expect((await fakeGoogle.requests()).filter((r) => r.path === '/token').map((r) => r.status)).toEqual([400]);

        // * Verify the state cannot be used again
        expect(second.status).toBe(400);
        expect(second.body).toContain('Invalid or expired state.');

        // * Verify the user is still not connected
        expect(await isConnected(user.id)).toBe(false);
    });

    /**
     * @objective Verify that disconnecting removes the stored Google token, and that disconnecting again still succeeds
     *
     * @precondition
     * The user's Google token is seeded directly into the plugin KV store (no Google sign-in)
     */
    test('disconnects a connected Google account', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin and connect a regular user
        const {adminClient, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient);
        await connectUser(user.id);
        const {channelsPage} = await pw.testBrowser.login(user);
        await channelsPage.goto(team.name);
        await channelsPage.toBeVisible();
        await waitForMeetWebapp(channelsPage);
        const slashCommand = new MeetSlashCommand(channelsPage);
        const posts = new MeetPosts(channelsPage);

        // # Disconnect the account
        await slashCommand.run('disconnect');

        // * Verify the plugin confirms and removes the stored token
        await expect(posts.disconnected).toBeVisible();
        expect(await isConnected(user.id)).toBe(false);

        // # Disconnect again
        await slashCommand.run('disconnect');

        // * Verify the plugin confirms again
        await expect(posts.postsWithText('Your Google account has been disconnected.')).toHaveCount(2);
    });

    /**
     * @objective Verify that connecting while the plugin has no OAuth client tells a regular user to contact an admin and gives an admin a link to the System Console
     */
    test('explains how to configure the plugin when connecting before it is configured', {tag: '@google_meet'}, async ({pw}) => {
        // # Enable the plugin without an OAuth client
        const {adminClient, adminUser, user, team} = await pw.initSetup();
        await ensureMeetPlugin(adminClient, UNCONFIGURED);

        // # Run the connect command as a regular user
        const asUser = await pw.testBrowser.login(user);
        await asUser.channelsPage.goto(team.name);
        await asUser.channelsPage.toBeVisible();
        await waitForMeetWebapp(asUser.channelsPage);
        await new MeetSlashCommand(asUser.channelsPage).run('connect');

        // * Verify the user is told to contact an administrator
        await expect(new MeetPosts(asUser.channelsPage).notConfiguredForUser).toBeVisible();

        // # Run the connect command as a system admin
        const asAdmin = await pw.testBrowser.login(adminUser!);
        await asAdmin.channelsPage.goto(team.name);
        await asAdmin.channelsPage.toBeVisible();
        await waitForMeetWebapp(asAdmin.channelsPage);
        await new MeetSlashCommand(asAdmin.channelsPage).run('connect');

        // * Verify the admin gets a link to the plugin's System Console page
        const adminPosts = new MeetPosts(asAdmin.channelsPage);
        await expect(adminPosts.configureLink).toHaveAttribute('href', /\/admin_console\/plugins\/plugin_com\.mattermost\.google-meet$/);
    });
});
