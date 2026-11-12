// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import path from 'path';

import {expect, test as base} from '@mattermost/playwright-lib';

import {fakeGoogle} from './fake_google';
import {clearPluginKV} from './kv';

/**
 * The `pw` test plus, around every test:
 * - a reset of the fake Google's state (spaces, tokens, injected failures) and of the plugin's KV
 *   store (tokens, subscriptions, ad-hoc meetings), so nothing a test sets up leaks into the next
 *   one, whatever the order;
 * - a check of the fake's contract: traffic that breaks Google's published contract fails the test
 *   that sent it. Traffic that arrives after a test was checked (the plugin's poller runs on its own
 *   goroutine) is kept and fails the run at global teardown.
 * Specs import `test` and `expect` from here (enforced by eslint.config.mjs). Adapted from
 * mattermost-plugin-google-drive e2e/playwright/lib/test.ts.
 */
export const test = base.extend<{fakeGoogleContract: void}>({
    fakeGoogleContract: [
        async ({}, use, testInfo) => {
            // Anything recorded since the previous test's check arrived late. Keep it for global
            // teardown rather than failing this test.
            const late = await fakeGoogle.deferViolations();
            for (const message of new Set(late.value)) {
                testInfo.annotations.push({type: 'fake-google late violation', description: message});
            }
            await fakeGoogle.reset(true);
            await clearPluginKV();

            await use();

            const contract = await fakeGoogle.takeViolations(`${path.basename(testInfo.file)} › ${testInfo.title}`);
            if (contract.value.length > 0) {
                throw new Error(`Fake Google saw traffic that breaks the Google API contract:\n${[...new Set(contract.value)].join('\n')}`);
            }
        },
        {auto: true},
    ],
});

export {expect};
