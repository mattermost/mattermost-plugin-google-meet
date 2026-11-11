// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Guards against pointing the suite at something that isn't a test target. Before every test the
// suite deletes every plugin KV row in the database it is given (lib/kv.ts clearPluginKV), and it
// rewrites the plugin's SiteURL, OAuth client and encryption key on the server it is given
// (lib/meet_plugin.ts). Adapted from mattermost-plugin-google-calendar e2e/playwright/lib/safety.ts.
//
// The Mattermost server, the database and the fake Google must be on localhost, unless
// E2E_ALLOW_REMOTE_SERVER=1 (for a disposable remote test server only).

import {testConfig} from '@mattermost/playwright-lib';

import {databaseURL, fakeGoogleURL} from './server';

const LOCAL_HOSTS = new Set(['localhost', '127.0.0.1', '[::1]', '::1']);

export function isLocal(url: string): boolean {
    try {
        const {hostname} = new URL(url);
        return LOCAL_HOSTS.has(hostname) || hostname.endsWith('.localhost');
    } catch {
        return false;
    }
}

/** Throws unless the server, the database and the fake are on localhost (or remote is explicitly allowed). */
export function assertLocalTargets() {
    if (process.env.E2E_ALLOW_REMOTE_SERVER === '1') {
        return;
    }
    const targets: Array<[string, string]> = [
        ['PW_BASE_URL', testConfig.baseURL],
        ['E2E_DATABASE_URL', databaseURL()],
        ['FAKE_GOOGLE_URL', fakeGoogleURL()],
    ];
    for (const [name, value] of targets) {
        if (!isLocal(value)) {
            let where = 'an unparseable URL';
            try {
                where = new URL(value).host;
            } catch {
                // keep the generic description; the value may hold a password
            }
            throw new Error(
                `${name} points at ${where}, which is not localhost. The suite deletes plugin data and rewrites ` +
                    'plugin settings on its target, so it only runs against a local stack. For a disposable remote ' +
                    'test server, set E2E_ALLOW_REMOTE_SERVER=1.',
            );
        }
    }
}
