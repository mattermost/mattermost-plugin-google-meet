// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {fakeGoogle} from './lib/fake_google';

// The compose stack keeps running after the run (npm run server:down removes it).
export default async function globalTeardown() {
    // lib/test.ts fails each test on its own violations and takes them. What is left came from
    // outside a test (global setup, or the poller after the last test), and `late` holds what
    // arrived after the test that caused it was checked. Both still fail the run. A poll cycle the
    // last test triggered runs in the background (server/api.go), so wait until the fake has seen no
    // new request for a second first.
    await waitUntilQuiet();
    const contract = await fakeGoogle.violations();
    const afterLast = contract.lastChecked ? `after "${contract.lastChecked}"` : 'before any test';
    const unreported = [...new Set([...contract.late, ...contract.value.map((v) => `[${afterLast}] ${v}`)])];
    if (unreported.length > 0) {
        throw new Error(`Fake Google saw traffic outside a test's check that breaks the Google API contract:\n${unreported.join('\n')}`);
    }
}

async function waitUntilQuiet(quietMs = 1_000, timeoutMs = 15_000) {
    const deadline = Date.now() + timeoutMs;
    let seen = (await fakeGoogle.requests()).length;
    while (Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, quietMs));
        const now = (await fakeGoogle.requests()).length;
        if (now === seen) {
            return;
        }
        seen = now;
    }
    throw new Error(`Fake Google was still receiving requests ${timeoutMs / 1000}s after the last test, so late contract violations could be missed.`);
}
