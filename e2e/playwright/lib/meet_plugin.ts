// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import fs from 'fs';
import path from 'path';
import zlib from 'zlib';

import type {Client4} from '@mattermost/client';
import {expect} from '@playwright/test';

import manifest from '../../../plugin.json';

import {assertLocalTargets} from './safety';
import {assertServerAlive} from './server';

export const PLUGIN_ID = manifest.id;

const PLUGIN_RUNNING = 2;

// Tests that seed tokens encrypt with it (lib/kv.ts). The plugin derives its AES key from any value.
export const TEST_ENCRYPTION_KEY = 'e2e-test-encryption-key';

// The fake Google's OAuth client (fake-google/server.ts CLIENT_ID, CLIENT_SECRET).
export const FAKE_OAUTH_CLIENT = {
    id: 'fake-client-id.apps.googleusercontent.com',
    secret: 'fake-client-secret',
};

/** The plugin's settings (plugin.json settings_schema), as stored in the server config. */
export type MeetPluginSettings = {
    googleClientId: string;
    googleClientSecret: string;
    encryptionKey: string;
    restrictMeetingCreation: boolean;
    enableConferenceArtifactPosts: boolean;
    enableCalendarScheduleSync: boolean;
    pollIntervalSeconds: number;
    conferenceStartCooldownHours: number;
};

export const DEFAULT_SETTINGS: MeetPluginSettings = {
    googleClientId: FAKE_OAUTH_CLIENT.id,
    googleClientSecret: FAKE_OAUTH_CLIENT.secret,
    encryptionKey: TEST_ENCRYPTION_KEY,
    restrictMeetingCreation: false,
    enableConferenceArtifactPosts: true,
    enableCalendarScheduleSync: false,
    // The poller's first tick comes one full interval after the plugin starts. A day keeps it out of
    // every test; tests that need a poll cycle trigger one with POST /admin/poll-now (lib/meet_api.ts).
    pollIntervalSeconds: 86_400,
    conferenceStartCooldownHours: 12,
};

/** Settings that leave the plugin unconfigured (no OAuth client). */
export const UNCONFIGURED: Partial<MeetPluginSettings> = {googleClientId: '', googleClientSecret: ''};

/** Returns the single plugin bundle in the repo's dist/, failing with a fix-it message otherwise. */
export function findPluginBundle(): string {
    const distDir = path.join(__dirname, '..', '..', '..', 'dist');
    const bundles = fs.existsSync(distDir) ? fs.readdirSync(distDir).filter((f) => f.endsWith('.tar.gz')) : [];
    if (bundles.length !== 1) {
        throw new Error(
            `Expected exactly one plugin bundle in ${distDir}, found ${bundles.length}. ` +
                "Run 'make dist' from the repository root (on macOS, with GNU tar on PATH).",
        );
    }

    const bundle = path.join(distDir, bundles[0]);
    assertSingleRootBundle(bundle);
    return bundle;
}

/**
 * Mattermost only finds plugin.json when the archive has exactly one top-level directory.
 * macOS bsdtar adds AppleDouble `._*` entries (hidden by macOS `tar -t`), which breaks that and
 * makes the upload fail with "unable to find manifest". Copied from mattermost-plugin-google-drive
 * e2e/playwright/lib/drive_plugin.ts.
 */
function assertSingleRootBundle(bundle: string) {
    const tar = zlib.gunzipSync(fs.readFileSync(bundle));
    const roots = new Set<string>();
    const appleDouble: string[] = [];

    for (let offset = 0; offset + 512 <= tar.length; ) {
        const header = tar.subarray(offset, offset + 512);
        const name = header.subarray(0, 100).toString('utf8').replace(/\0.*$/s, '');
        if (!name) {
            break;
        }
        const prefix = header.subarray(345, 500).toString('utf8').replace(/\0.*$/s, '');
        const fullName = (prefix ? `${prefix}/${name}` : name).replace(/^\.\//, '');
        const typeFlag = String.fromCharCode(header[156]);
        const size = parseInt(header.subarray(124, 136).toString('utf8').replace(/\0.*$/s, '').trim() || '0', 8);

        // Skip pax/GNU metadata records; they describe the next entry rather than being one.
        if (!['x', 'g', 'L', 'K'].includes(typeFlag)) {
            roots.add(fullName.split('/')[0]);
            if (path.basename(fullName).startsWith('._')) {
                appleDouble.push(fullName);
            }
        }
        offset += 512 + Math.ceil(size / 512) * 512;
    }

    if (appleDouble.length > 0 || roots.size !== 1) {
        throw new Error(
            `Plugin bundle ${path.basename(bundle)} is not a single-directory archive ` +
                `(top-level entries: ${[...roots].join(', ')}; AppleDouble entries: ${appleDouble.length}). ` +
                "Mattermost will reject it with 'unable to find manifest'. On macOS rebuild with GNU tar, " +
                'e.g. `PATH="$(brew --prefix gnu-tar)/libexec/gnubin:$PATH" make dist`.',
        );
    }
}

/** Uploads the plugin bundle (replacing any existing version). Does not enable it. */
export async function uploadPlugin(adminClient: Client4, bundlePath: string) {
    const file = new File([fs.readFileSync(bundlePath)], path.basename(bundlePath), {type: 'application/gzip'});
    await adminClient.uploadPlugin(file, true);
}

/**
 * Enables the plugin with DEFAULT_SETTINGS plus `overrides`, and waits until it runs.
 *
 * Call it after pw.initSetup(): initSetup() replaces the whole server config, which wipes plugin
 * settings and enable states. That also means no setting a test changes survives into the next test.
 */
export async function ensureMeetPlugin(adminClient: Client4, overrides: Partial<MeetPluginSettings> = {}) {
    assertLocalTargets();
    const siteURL = process.env.PW_BASE_URL as string;
    await assertServerAlive(siteURL);
    applied = {...DEFAULT_SETTINGS, ...overrides};
    await adminClient.patchConfig({
        ServiceSettings: {SiteURL: siteURL},
        PluginSettings: {
            PluginStates: {[PLUGIN_ID]: {Enable: true}},
            Plugins: {[PLUGIN_ID]: pluginConfig(applied)},
        },
    } as any);
    await waitForPluginRunning(adminClient);
}

// What the current test set with ensureMeetPlugin. Reading the config back would not work: the
// server masks secret plugin settings in it.
let applied: MeetPluginSettings = DEFAULT_SETTINGS;

/** Changes plugin settings on a running plugin (OnConfigurationChange), keeping the others. */
export async function updateMeetPluginSettings(adminClient: Client4, changes: Partial<MeetPluginSettings>) {
    applied = {...applied, ...changes};
    await adminClient.patchConfig({PluginSettings: {Plugins: {[PLUGIN_ID]: pluginConfig(applied)}}} as any);
    await waitForPluginRunning(adminClient);
}

// The server stores plugin setting keys in lower case.
function pluginConfig(settings: Partial<MeetPluginSettings>): Record<string, unknown> {
    return Object.fromEntries(Object.entries(settings).map(([key, value]) => [key.toLowerCase(), value]));
}

async function waitForPluginRunning(adminClient: Client4) {
    await expect
        .poll(
            async () => {
                const statuses = await adminClient.getPluginStatuses();
                return statuses.find((s) => s.plugin_id === PLUGIN_ID)?.state;
            },
            {message: `plugin ${PLUGIN_ID} should be running`, timeout: 30_000},
        )
        .toBe(PLUGIN_RUNNING);
}
