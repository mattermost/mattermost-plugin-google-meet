// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React, {useCallback} from 'react';
import {useSelector} from 'react-redux';

import type {Post} from '@mattermost/types/posts';
import type {GlobalState} from '@mattermost/types/store';

import AIIcon from './ai_icon';

const aiPluginID = 'mattermost-ai';

type PluginState = GlobalState & {
    plugins?: {plugins?: Record<string, unknown>};
    [key: string]: unknown;
};

export const useAIAvailable = () => {
    return useSelector((state: PluginState) => Boolean(state.plugins?.plugins?.[aiPluginID]));
};

export const useCallsPostButtonClicked = () => {
    return useSelector((state: PluginState) => {
        const aiPluginState = state['plugins-' + aiPluginID] as Record<string, unknown> | undefined;
        const handler = aiPluginState?.callsPostButtonClickedTranscription;
        if (typeof handler === 'function') {
            return handler as (post: Post) => void;
        }
        return null;
    });
};

const buttonStyle: React.CSSProperties = {
    display: 'flex',
    border: 'none',
    height: '24px',
    padding: '4px 10px',
    marginTop: '8px',
    marginBottom: '8px',
    alignItems: 'center',
    justifyContent: 'center',
    gap: '6px',
    borderRadius: '4px',
    background: 'rgba(var(--center-channel-color-rgb), 0.08)',
    color: 'rgba(var(--center-channel-color-rgb), 0.64)',
    fontSize: '12px',
    fontWeight: 600,
    lineHeight: '16px',
    cursor: 'pointer',
};

type Props = {
    post: Post;
    label: string;
};

// Currently this is not usable because the Agents plugin needs to
// have a new release with an inclusion of the google meet bot in order for this to work
// Once a new release has been made, we can add this to the post type transcript component
export const AISummaryButton = ({post, label}: Props) => {
    const aiAvailable = useAIAvailable();
    const callsPostButtonClicked = useCallsPostButtonClicked();

    const handleClick = useCallback(() => {
        if (callsPostButtonClicked) {
            callsPostButtonClicked(post);
        }
    }, [callsPostButtonClicked, post]);

    if (!aiAvailable || !callsPostButtonClicked) {
        return null;
    }

    return (
        <button
            type='button'
            style={buttonStyle}
            onClick={handleClick}
        >
            <AIIcon/>
            {label}
        </button>
    );
};
