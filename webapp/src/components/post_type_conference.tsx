// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React from 'react';

import ExternalLink from 'components/external_link';

type Props = {
    post: {
        message: string;
        props: {
            meeting_code?: string;
            description?: string;
        };
    };
};

const PostTypeConference = ({post}: Props) => {
    const {meeting_code: meetingCode = '', description = ''} = post.props || {};
    const meetURL = meetingCode ? `https://meet.google.com/${meetingCode}` : '';
    const title = description.trim() || 'Google Meet Conference';

    return (
        <div className='attachment attachment--pretext'>
            <div className='attachment__content'>
                <div
                    className='clearfix attachment__container'
                    style={{borderLeftColor: '#00832d'}}
                >
                    <h5 className='mt-1'>{title}</h5>
                    <div>{post.message}</div>
                    {meetURL && (
                        <div style={{marginTop: '8px'}}>
                            <ExternalLink href={meetURL}>{meetURL}</ExternalLink>
                        </div>
                    )}
                </div>
            </div>
        </div>
    );
};

export default PostTypeConference;
