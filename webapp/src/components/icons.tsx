import React from 'react';

export const GoogleMeetIcon = () => {
    const instanceId = React.useId().replace(/:/g, '');
    const gradientAId = `gmeet-a-${instanceId}`;
    const gradientBId = `gmeet-b-${instanceId}`;
    const filterId = `gmeet-c-${instanceId}`;
    const maskId = `gmeet-e-${instanceId}`;
    const gradientFId = `gmeet-f-${instanceId}`;

    return (
        <svg
            xmlns='http://www.w3.org/2000/svg'
            fill='none'
            viewBox='0 0 192 192'
            width='16px'
            height='16px'
        >
            <path
                fill={`url(#${gradientAId})`}
                d='M110.015 108.88c-6.829-4.718-6.921-14.778-.179-19.62L165 49.643c7.94-5.701 19-.038 19 9.737v77.755c0 9.675-10.861 15.359-18.821 9.859z'
            />
            <path
                fill={`url(#${gradientBId})`}
                d='M8 71c0-24.3 19.7-44 44-44h64c11.046 0 20 8.954 20 20v98c0 11.046-8.954 20-20 20H28c-11.046 0-20-8.954-20-20z'
            />
            <mask
                id={maskId}
                width='129'
                height='138'
                x='8'
                y='27'
                maskUnits='userSpaceOnUse'
                style={{maskType: 'luminance'}}
            >
                <path
                    fill='#fff'
                    d='M8 71c0-24.3 19.7-44 44-44h64c11.046 0 20 8.954 20 20v98c0 11.046-8.954 20-20 20H28c-11.046 0-20-8.954-20-20z'
                />
            </mask>
            <g
                filter={`url(#${filterId})`}
                mask={`url(#${maskId})`}
            >
                <path
                    fill={`url(#${gradientFId})`}
                    d='m73.906 99.198 110-63.198v124z'
                />
            </g>
            <circle
                cx='38'
                cy='135'
                r='14'
                fill='#fff'
            />
            <defs>
                <linearGradient
                    id={gradientAId}
                    x1='128.8'
                    x2='227.2'
                    y1='104.44'
                    y2='104.44'
                    gradientUnits='userSpaceOnUse'
                >
                    <stop stopColor='#f6a100'/>
                    <stop
                        offset='1'
                        stopColor='#ffbe00'
                    />
                </linearGradient>
                <linearGradient
                    id={gradientFId}
                    x1='136.22'
                    x2='78.5'
                    y1='91.32'
                    y2='91.19'
                    gradientUnits='userSpaceOnUse'
                >
                    <stop
                        offset='.15'
                        stopColor='#ffb5e8'
                    />
                    <stop
                        offset='1'
                        stopColor='#ffdbf5'
                        stopOpacity='0'
                    />
                </linearGradient>
                <radialGradient
                    id={gradientBId}
                    cx='0'
                    cy='0'
                    r='1'
                    gradientTransform='matrix(-159.725 0 0 -135.852 160.325 96)'
                    gradientUnits='userSpaceOnUse'
                >
                    <stop
                        offset='.15'
                        stopColor='#ffe921'
                    />
                    <stop
                        offset='1'
                        stopColor='#fec700'
                    />
                </radialGradient>
                <filter
                    id={filterId}
                    width='166'
                    height='180'
                    x='45.91'
                    y='8'
                    colorInterpolationFilters='sRGB'
                    filterUnits='userSpaceOnUse'
                >
                    <feFlood
                        floodOpacity='0'
                        result='BackgroundImageFix'
                    />
                    <feBlend
                        in='SourceGraphic'
                        in2='BackgroundImageFix'
                        result='shape'
                    />
                    <feGaussianBlur
                        result='effect1_foregroundBlur_37584_9338'
                        stdDeviation='14'
                    />
                </filter>
            </defs>
        </svg>
    );
};

export const VideoCameraIcon = ({style}: {style?: React.CSSProperties}) => (
    <svg
        xmlns='http://www.w3.org/2000/svg'
        viewBox='0 0 24 24'
        width='16px'
        height='16px'
        fill='currentColor'
        style={style}
    >
        <path d='M17 10.5V7c0-.55-.45-1-1-1H4c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h12c.55 0 1-.45 1-1v-3.5l4 4v-11l-4 4z'/>
    </svg>
);
