// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React from 'react';

import {makeStyleFromTheme} from 'mattermost-redux/utils/theme_utils';

import ExternalLink from 'components/external_link';

import {VideoCameraIcon} from './icons';

const getStyle = makeStyleFromTheme((theme: Record<string, string>) => {
    return {
        body: {
            overflowX: 'auto' as const,
            overflowY: 'hidden' as const,
            paddingRight: '5px',
            width: '100%',
        },
        title: {
            fontWeight: '600',
        },
        button: {
            fontFamily: 'Open Sans',
            fontSize: '12px',
            fontWeight: 'bold',
            letterSpacing: '1px',
            lineHeight: '19px',
            marginTop: '12px',
            borderRadius: '4px',
            color: theme.buttonColor,
        },
        summary: {
            fontFamily: 'Open Sans',
            fontSize: '14px',
            fontWeight: '600',
            lineHeight: '26px',
            margin: '0',
            padding: '14px 0 0 0',
        },
        summaryItem: {
            fontFamily: 'Open Sans',
            fontSize: '14px',
            lineHeight: '26px',
        },
    };
});

interface PostTypeMeetingProps {
    post: {
        message: string;
        create_at: number;
        props: {
            meeting_link?: string;
            meeting_code?: string;
            meeting_topic?: string;
            description?: string;
            meeting_ended?: boolean;
            meeting_end_time?: number;
        };
    };
    theme: Record<string, string>;
}

function formatDuration(startMs: number, endMs: number): string {
    const durationMs = endMs > startMs ? endMs - startMs : 0;
    return `${Math.ceil(durationMs / 60000)} minute(s)`;
}

function formatDate(date: Date): string {
    return date.toLocaleString(undefined, {
        weekday: 'short',
        year: 'numeric',
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
    });
}

const PostTypeMeeting = ({post, theme}: PostTypeMeetingProps) => {
    const style = getStyle(theme);
    const props = post.props || {};

    const meetingLink = props.meeting_link || (props.meeting_code ? `https://meet.google.com/${props.meeting_code}` : '');
    const meetingEnded = Boolean(props.meeting_ended);
    const title = props.meeting_topic || (props.description?.trim()) || 'Google Meet';

    const {formatText, messageHtmlToComponent} = (window as any).PostUtils || {};

    const renderMarkdown = (text: string): React.ReactNode => {
        if (formatText && messageHtmlToComponent) {
            return messageHtmlToComponent(formatText(text, {atMentions: true}), false);
        }
        return text;
    };

    const preText = renderMarkdown(post.message);
    const renderedTitle = renderMarkdown(title);

    let subtitle: React.ReactNode = null;
    let content: React.ReactNode = null;

    if (meetingEnded) {
        const startDate = new Date(post.create_at);
        const endMs = props.meeting_end_time ?? Date.now();

        if (meetingLink) {
            subtitle = (
                <span>
                    {'Meeting URL: '}
                    <ExternalLink href={meetingLink}>{meetingLink}</ExternalLink>
                </span>
            );
        }

        content = (
            <div>
                <h2 style={style.summary}>{'Meeting Summary'}</h2>
                <span style={style.summaryItem}>{'Date: ' + formatDate(startDate)}</span>
                <br/>
                <span style={style.summaryItem}>{'Meeting Length: ' + formatDuration(startDate.getTime(), endMs)}</span>
            </div>
        );
    } else if (meetingLink) {
        subtitle = (
            <span>
                {'Meeting URL: '}
                <ExternalLink href={meetingLink}>{meetingLink}</ExternalLink>
            </span>
        );

        content = (
            <ExternalLink
                className='btn btn-primary'
                style={style.button}
                href={meetingLink}
                aria-label='Join meeting'
            >
                <VideoCameraIcon/>
                {'JOIN MEETING'}
            </ExternalLink>
        );
    }

    return (
        <div className='attachment attachment--pretext'>
            <div className='attachment__thumb-pretext'>
                {preText}
            </div>
            <div className='attachment__content'>
                <div
                    className='clearfix attachment__container'
                    style={{borderLeftColor: '#00832d'}}
                >
                    <h5
                        className='mt-1'
                        style={style.title}
                    >
                        {renderedTitle}
                    </h5>
                    {subtitle}
                    <div>
                        <div style={style.body}>
                            {content}
                        </div>
                    </div>
                </div>
            </div>
        </div>
    );
};

export default PostTypeMeeting;
