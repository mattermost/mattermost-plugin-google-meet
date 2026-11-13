// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// The compose stack (docker-compose.yml) as the suite sees it from the host. Adapted from
// mattermost-plugin-google-drive e2e/playwright/lib/server_mode.ts, without its external-server and
// real-Google modes: this suite only runs against the compose stack and its fake Google.

/** Postgres of the compose stack, published on the host. */
export function databaseURL(): string {
    return process.env.E2E_DATABASE_URL ?? `postgres://mmuser:mostest@localhost:${process.env.E2E_DB_PORT || '5435'}/mattermost_test`;
}

/** Control API of the compose stack's fake Google, published on the host. */
export function fakeGoogleURL(): string {
    return process.env.FAKE_GOOGLE_URL ?? `http://127.0.0.1:${process.env.FAKE_GOOGLE_PORT || '8100'}`;
}

/**
 * Fails fast when the server is down (not started, or crashed earlier in the run), instead of every
 * test hanging until its timeout.
 */
export async function assertServerAlive(siteURL: string) {
    try {
        const response = await fetch(`${siteURL}/api/v4/system/ping`, {signal: AbortSignal.timeout(10_000)});
        if (response.ok) {
            return;
        }
        throw new Error(`HTTP ${response.status}`);
    } catch (err) {
        throw new Error(
            `Mattermost at ${siteURL} is not responding (${String(err)}). Start it with \`npm run server:up\` ` +
                '(or `make e2e-env-up` from the repo root). If it crashed, see `npm run server:logs`.',
        );
    }
}

/** True when something answers on the fake Google's control API. */
export async function fakeGoogleIsRunning(url: string): Promise<boolean> {
    return fetch(`${url}/__control/health`, {signal: AbortSignal.timeout(2_000)})
        .then((r) => r.ok)
        .catch(() => false);
}
