// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {defineConfig, devices} from '@playwright/test';

import {VISUAL_STYLE_PATH} from './lib/visual';

// The compose stack (npm run server:up) publishes Mattermost on a fixed host port that matches its
// internal port, so SiteURL http://localhost:8085 resolves to the server from the browser and from
// inside the container. @mattermost/playwright-lib reads PW_BASE_URL when it is first imported, so
// set it here, before any spec loads the library.
process.env.PW_BASE_URL ??= `http://localhost:${process.env.MM_PORT || '8085'}`;

// Visual runs (npm run test:visual) set PW_BASE_URL to their own stack and PW_VISUAL_BROWSER_WS to
// its dockerized browser (script/visual.sh). Without PW_VISUAL_BROWSER_WS the visual specs skip,
// because their baselines only match that browser.

export default defineConfig({
    testDir: './specs',
    outputDir: './results/output',

    // No platform suffix: baselines always come from the dockerized browser.
    snapshotPathTemplate: '{testDir}/{testFilePath}-snapshots/{arg}{ext}',

    // One worker per server (mattermost-server e2e-tests/playwright/AGENTS.md), and one fake Google
    // whose state each test resets.
    workers: 1,
    fullyParallel: false,

    forbidOnly: !!process.env.CI,
    retries: 0,
    timeout: 90_000,
    expect: {timeout: 15_000, toHaveScreenshot: {stylePath: VISUAL_STYLE_PATH}},

    globalSetup: require.resolve('./global_setup'),
    globalTeardown: require.resolve('./global_teardown'),

    reporter: [['list'], ['html', {outputFolder: './results/reporter', open: 'never'}]],

    use: {
        baseURL: process.env.PW_BASE_URL,
        // The suite only ever talks to the fake Google, so traces hold fake tokens and a throwaway
        // server's data. Kept in CI too, so a failing PR run can be debugged from its artifact.
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        // PW_SLOWMO=500 pauses that many ms between browser actions, for watching a headed run.
        launchOptions: {slowMo: Number(process.env.PW_SLOWMO ?? 0)},
        connectOptions: process.env.PW_VISUAL_BROWSER_WS ? {wsEndpoint: process.env.PW_VISUAL_BROWSER_WS} : undefined,
    },

    projects: [
        {
            name: 'chrome',
            use: {...devices['Desktop Chrome'], viewport: {width: 1280, height: 1024}},
        },
    ],
});
