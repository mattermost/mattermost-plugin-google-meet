// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {Client4} from '@mattermost/client';
import {baseGlobalSetup, testConfig} from '@mattermost/playwright-lib';

import {fakeGoogle} from './lib/fake_google';
import {findPluginBundle, uploadPlugin} from './lib/meet_plugin';
import {assertLocalTargets} from './lib/safety';
import {assertServerAlive, fakeGoogleIsRunning, fakeGoogleURL} from './lib/server';

export default async function globalSetup() {
    // The suite wipes plugin data and rewrites plugin settings, so refuse anything but a local stack.
    assertLocalTargets();

    // Fail before touching the server if the bundle is missing or would be rejected.
    const bundle = findPluginBundle();

    // The stack is started outside the run (npm run server:up), so this only checks it is there.
    await assertServerAlive(testConfig.baseURL);
    if (!(await fakeGoogleIsRunning(fakeGoogleURL()))) {
        throw new Error(`Fake Google is not answering at ${fakeGoogleURL()}. Start the stack with \`npm run server:up\`.`);
    }
    // The compose stack outlives a run, so this is what isolates one run from the previous one.
    await fakeGoogle.reset();

    // Creates the sysadmin (on a fresh server) and default team, and sets default preferences.
    await baseGlobalSetup();

    const adminClient = new Client4();
    adminClient.setUrl(testConfig.baseURL);
    await adminClient.login(testConfig.adminUsername, testConfig.adminPassword);
    await uploadPlugin(adminClient, bundle);
    console.log(`Uploaded plugin bundle ${bundle}`);
}
