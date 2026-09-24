// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React, {act} from 'react';
import {createRoot} from 'react-dom/client';
import type {Root} from 'react-dom/client';
import {Provider} from 'react-redux';

import PostTypeMeeting from './post_type_meeting';

const store = {
    getState: () => ({
        entities: {
            general: {config: {}},
            preferences: {myPreferences: {}},
            users: {currentUserId: 'user1', profiles: {user1: {id: 'user1'}}},
        },
    }),
    subscribe: () => () => {},
    dispatch: () => {},
};

describe('PostTypeMeeting', () => {
    let container: HTMLDivElement;
    let root: Root;

    beforeEach(() => {
        container = document.createElement('div');
        document.body.appendChild(container);
        root = createRoot(container);
    });

    const cleanup = async () => {
        await act(async () => {
            root.unmount();
        });
        container.remove();
    };

    afterEach(cleanup);

    const theme = {
        buttonColor: '#ffffff',
        sidebarHeaderBg: '#192a4d',
    };

    const post = {
        message: 'I have started a meeting',
        create_at: 1758600000000,
        props: {
            meeting_link: 'https://meet.google.com/duk-ttxn-yeg',
            meeting_topic: '',
        },
    };

    const renderPost = async () => {
        await act(async () => {
            root.render(
                <Provider store={store as never}>
                    <PostTypeMeeting
                        post={post as never}
                        theme={theme as never}
                    />
                </Provider>,
            );
        });

        return container.querySelector('.attachment__container') as HTMLElement;
    };

    // The webapp already colours this bar from the active theme. Setting it inline overrode
    // that and left the bar out of step with the rest of the post, which is MM-70888.
    test('leaves the attachment bar colour to the webapp', async () => {
        const attachment = await renderPost();

        expect(attachment).not.toBeNull();
        expect(attachment.style.borderLeftColor).toBe('');
    });
});
