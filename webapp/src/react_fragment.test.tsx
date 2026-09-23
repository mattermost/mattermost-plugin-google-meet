// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import React from 'react';

function generateSomething() {
    return <div>{'something'}</div>;
}

test('Can test React fragments', () => {
    expect(React.version).toEqual('19.2.8');
    expect(generateSomething()).toBeDefined();
});
