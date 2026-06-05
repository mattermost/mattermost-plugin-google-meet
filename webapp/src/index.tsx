import {createMeeting, getConfigStatus} from 'client/client';
import manifest from 'manifest';
import React from 'react';
import type {Store} from 'redux';
import {postEphemeralMessage} from 'utils/ephemeral';
import {handleMeetingStarted} from 'websocket/meeting_started';

import type {Channel} from '@mattermost/types/channels';
import type {GlobalState} from '@mattermost/types/store';

import {GoogleMeetIcon} from 'components/icons';
import {PostTypeRecording, PostTypeSmartNote, PostTypeTranscript} from 'components/post_type_artifact';
import PostTypeConference from 'components/post_type_conference';
import PostTypeGoogleMeet from 'components/post_type_google_meet';

import type {PluginRegistry} from 'types/mattermost-webapp';

export default class Plugin {
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    public async initialize(registry: PluginRegistry, store: Store<GlobalState>) {
        let configured = false;
        let isAdmin = false;

        try {
            const status = await getConfigStatus();
            configured = status.configured;
            isAdmin = status.is_admin;
        } catch {
            // Keep the safe default when config status cannot be determined.
        }

        registry.registerPostTypeComponent('custom_google_meet', PostTypeGoogleMeet);
        registry.registerPostTypeComponent('custom_gmeet_conference', PostTypeConference);
        registry.registerPostTypeComponent('custom_gmeet_transcript', PostTypeTranscript);
        registry.registerPostTypeComponent('custom_gmeet_recording', PostTypeRecording);
        registry.registerPostTypeComponent('custom_gmeet_smartnote', PostTypeSmartNote);
        registry.registerWebSocketEventHandler(`custom_${manifest.id}_meeting_started`, handleMeetingStarted);

        if (!configured && !isAdmin) {
            return;
        }

        registry.registerChannelHeaderButtonAction(
            <GoogleMeetIcon/>,
            (channel: Channel) => {
                const startMeeting = async () => {
                    try {
                        const connectionId = store.getState().websocket?.connectionId || '';
                        const data = await createMeeting(channel.id, connectionId);
                        if (data.status !== 'ok' && data.status !== 'handled') {
                            const serverContext = data.message || data.error || data.reason;
                            const message = serverContext ?
                                `Received an unexpected response from the server while starting a Google Meet meeting: ${serverContext}` :
                                'Received an unexpected response from the server while starting a Google Meet meeting.';
                            postEphemeralMessage(
                                store,
                                channel.id,
                                message,
                            );
                        }
                    } catch (error) {
                        postEphemeralMessage(
                            store,
                            channel.id,
                            error instanceof Error ? error.message : 'Unable to start a Google Meet meeting. Please try again.',
                        );
                    }
                };

                startMeeting();
            },
            'Start Google Meet',
            'Start a Google Meet meeting',
        );
    }
}

declare global {
    interface Window {
        registerPlugin(pluginId: string, plugin: Plugin): void;
    }
}

window.registerPlugin(manifest.id, new Plugin());
