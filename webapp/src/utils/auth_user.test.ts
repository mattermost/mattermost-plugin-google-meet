// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import type {GlobalState} from '@mattermost/types/store';

import {getAuthUserPreference} from './auth_user';

function stateWithAuthUser(value: string): GlobalState {
    return {
        entities: {
            general: {config: {}},
            preferences: {
                myPreferences: {
                    'pp_com.mattermost.google-meet--authuser': {
                        category: 'pp_com.mattermost.google-meet',
                        name: 'authuser',
                        value,
                        user_id: 'user1',
                    },
                },
            },
        },
    } as unknown as GlobalState;
}

describe('getAuthUserPreference', () => {
    test.each(['2', 'alice@example.com'])('returns %s', (value) => {
        expect(getAuthUserPreference(stateWithAuthUser(value))).toBe(value);
    });

    test.each(['-1', 'work', 'alice@'])('drops the invalid value %s', (value) => {
        expect(getAuthUserPreference(stateWithAuthUser(value))).toBe('');
    });
});
