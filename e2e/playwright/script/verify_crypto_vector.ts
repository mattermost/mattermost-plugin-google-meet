// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Checks that e2e/playwright/lib/crypto.ts still produces the vector that the plugin's Go decrypt
// accepts (server/store/kvstore/e2e_crypto_vector_test.go). Run by `npm run check`.

import {CRYPTO_TEST_VECTOR, decrypt, encrypt} from '../lib/crypto';

const {key, nonceHex, plaintext, ciphertextHex} = CRYPTO_TEST_VECTOR;
const produced = encrypt(key, plaintext, Buffer.from(nonceHex, 'hex')).toString('hex');

if (produced !== ciphertextHex) {
    console.error(`crypto vector mismatch:\n  expected ${ciphertextHex}\n  produced ${produced}`);
    process.exit(1);
}
if (decrypt(key, Buffer.from(ciphertextHex, 'hex')) !== plaintext) {
    console.error('crypto vector does not round-trip');
    process.exit(1);
}
console.log('crypto vector OK');
