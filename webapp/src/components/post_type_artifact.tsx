// Copyright (c) 2026-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React from 'react';

import ExternalLink from 'components/external_link';

type ArtifactPost = {
    message: string;
    props?: Record<string, unknown>;
};

type ArtifactLinkPostProps = {
    post: ArtifactPost;
    linkText: string;
};

const ArtifactLinkPost = ({post, linkText}: ArtifactLinkPostProps) => {
    const uri = post.props?.export_uri;
    const href = typeof uri === 'string' ? uri : '';

    return (
        <div>
            <div>{post.message}</div>
            {href && (
                <div style={{marginTop: '8px'}}>
                    <ExternalLink href={href}>{linkText}</ExternalLink>
                </div>
            )}
        </div>
    );
};

type WrapperProps = {post: ArtifactPost};

export const PostTypeRecording = ({post}: WrapperProps) => (
    <ArtifactLinkPost
        post={post}
        linkText='View recording in Google Drive'
    />
);

export const PostTypeSmartNote = ({post}: WrapperProps) => (
    <ArtifactLinkPost
        post={post}
        linkText='View smart notes in Google Docs'
    />
);

export const PostTypeTranscript = ({post}: WrapperProps) => (
    <ArtifactLinkPost
        post={post}
        linkText='View transcript in Google Docs'
    />
);
