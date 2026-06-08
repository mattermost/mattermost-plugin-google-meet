import React from 'react';

type Props = React.AnchorHTMLAttributes<HTMLAnchorElement>;

const ExternalLink = ({children, rel, target, ...props}: Props) => {
    const resolvedTarget = target || '_blank';
    let resolvedRel = rel;

    if (resolvedTarget === '_blank') {
        const relTokens = new Set((rel || '').split(/\s+/).filter(Boolean));
        relTokens.add('noopener');
        relTokens.add('noreferrer');
        resolvedRel = Array.from(relTokens).join(' ');
    }

    return (
        <a
            {...props}
            rel={resolvedRel}
            target={resolvedTarget}
        >
            {children}
        </a>
    );
};

export default ExternalLink;
