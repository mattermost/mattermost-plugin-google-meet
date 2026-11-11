// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import crypto from 'crypto';
import fs from 'fs';
import path from 'path';

const NONCE_BYTES = 12;
const TAG_BYTES = 16;

/**
 * The plugin's token key (server/store/kvstore/oauth.go deriveKey): PBKDF2-SHA256 of the
 * EncryptionKey setting with a fixed, plugin-specific salt, 100,000 iterations, 32 bytes.
 */
function deriveKey(encryptionKey: string): Buffer {
    return crypto.pbkdf2Sync(encryptionKey, 'com.mattermost.google-meet/oauth-token', 100_000, 32, 'sha256');
}

/**
 * Encrypts like server/store/kvstore/oauth.go encrypt: AES-256-GCM, output = nonce || ciphertext ||
 * tag as raw bytes (stored in the KV row as is).
 *
 * `nonce` is only for the fixed test vector; leave it unset otherwise.
 */
export function encrypt(encryptionKey: string, plaintext: string, nonce: Buffer = crypto.randomBytes(NONCE_BYTES)): Buffer {
    const cipher = crypto.createCipheriv('aes-256-gcm', deriveKey(encryptionKey), nonce);
    return Buffer.concat([nonce, cipher.update(plaintext, 'utf8'), cipher.final(), cipher.getAuthTag()]);
}

export function decrypt(encryptionKey: string, sealed: Buffer): string {
    const nonce = sealed.subarray(0, NONCE_BYTES);
    const tag = sealed.subarray(sealed.length - TAG_BYTES);
    const ciphertext = sealed.subarray(NONCE_BYTES, sealed.length - TAG_BYTES);
    const decipher = crypto.createDecipheriv('aes-256-gcm', deriveKey(encryptionKey), nonce);
    decipher.setAuthTag(tag);
    return Buffer.concat([decipher.update(ciphertext), decipher.final()]).toString('utf8');
}

/**
 * Fixed vector shared with server/store/kvstore/e2e_crypto_vector_test.go, which decrypts it with
 * the plugin's own decrypt and unmarshals it into the plugin's token type. Both read the same file,
 * server/store/kvstore/testdata/e2e_crypto_vector.json. `npm run check` verifies this file still
 * produces it. If either side changes format, one of the two checks fails before any e2e run (a
 * seeded token the plugin cannot decrypt is silently deleted, which would surface only as a
 * confusing "not connected").
 */
export const CRYPTO_TEST_VECTOR: {key: string; nonceHex: string; plaintext: string; ciphertextHex: string} = JSON.parse(
    fs.readFileSync(path.join(__dirname, '../../../server/store/kvstore/testdata/e2e_crypto_vector.json'), 'utf8'),
);
