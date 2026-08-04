// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import manifest from 'manifest';

import type {GlobalState} from '@mattermost/types/store';

import {get as getPreference} from 'mattermost-redux/selectors/entities/preferences';

const authUserPreferenceCategory = `pp_${manifest.id}`;
const authUserPreferenceName = 'authuser';
const authUserPattern = /^\d+$/;

export function getAuthUserPreference(state: GlobalState): string {
    const value = getPreference(state, authUserPreferenceCategory, authUserPreferenceName, '');
    return authUserPattern.test(value) ? value : '';
}
