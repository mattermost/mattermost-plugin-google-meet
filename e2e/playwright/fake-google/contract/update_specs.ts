// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Copied from mattermost-plugin-google-drive e2e/playwright/fake-google/contract/update_specs.ts, with
// parameter drift (type, location, format, repeated, enum) added to --check.
//
// Trims Google's published Discovery documents down to the methods and fields in operations.json
// and converts their schemas to closed JSON Schema (a field Google does not document, or that
// operations.json does not list, fails).
//   node fake-google/contract/update_specs.ts          rewrite specs/*.json from Google
//   node fake-google/contract/update_specs.ts --check  report drift; exit 1 only on breaking drift,
//                                                      exit 2 when a document could not be downloaded
//
// Google changes these documents almost daily (revision, etag, descriptions), and most schema
// changes are additions. So the committed copies drop descriptions, and --check fails only on
// changes that can break the fake or the plugin: a removed method, field or enum value, a changed
// type or path, or a newly required parameter. Additions are reported so they can be pulled in.
//
// "fields" lists, per schema, the properties the plugin sends or the fake returns; everything else
// Google documents is dropped, so drift in fields we never use is not reported. Every object schema
// the methods reach must be listed. Nested inline objects use dotted paths (capabilities.canEdit);
// a path without children keeps the whole property.

/* eslint-disable no-console */

import fs from 'node:fs';
import path from 'node:path';

import type {ApiSpec, JsonSchema, MethodSpec, ParameterSpec} from './types.ts';

type DiscoverySchema = {
    $ref?: string;
    type?: string;
    format?: string;
    enum?: string[];
    items?: DiscoverySchema;
    properties?: Record<string, DiscoverySchema>;
    additionalProperties?: DiscoverySchema;
};

type DiscoveryMethod = {
    id: string;
    httpMethod: string;
    flatPath?: string;
    path: string;
    parameters?: Record<string, {type: string; location: string; required?: boolean; enum?: string[]; repeated?: boolean; format?: string}>;
    request?: {$ref: string};
    response?: {$ref: string};
    mediaUpload?: {protocols?: {simple?: {path: string}}};
};

type DiscoveryResource = {methods?: Record<string, DiscoveryMethod>; resources?: Record<string, DiscoveryResource>};

type Discovery = DiscoveryResource & {
    name: string;
    version: string;
    servicePath: string;
    schemas: Record<string, DiscoverySchema>;
};

const dir = import.meta.dirname;
const specsDir = path.join(dir, 'specs');
const check = process.argv.includes('--check');
const manifest: Record<string, {url: string; methods: string[]; fields: Record<string, string[]>}> = JSON.parse(fs.readFileSync(path.join(dir, 'operations.json'), 'utf8'));

let breaking = 0;
let additive = 0;

for (const [name, {url, methods, fields}] of Object.entries(manifest)) {
    // A download failure says nothing about drift, so it gets its own exit code (the nightly
    // workflow only opens a drift issue on exit 1).
    let response: Response;
    try {
        response = await fetch(url);
    } catch (err) {
        console.error(`Failed to download ${url}: ${String(err)}`);
        process.exit(2);
    }
    if (!response.ok) {
        console.error(`Failed to download ${url}: ${response.status}`);
        process.exit(2);
    }
    const problems: string[] = [];
    const next = trim(await response.json() as Discovery, methods, fields, problems);
    const outPath = path.join(specsDir, `${name}.json`);
    if (problems.length && !check) {
        throw new Error(`${name}:\n  ${problems.join('\n  ')}`);
    }

    if (check) {
        for (const line of problems) {
            console.error(`${name}: BREAKING ${line}`);
        }
        breaking += problems.length;
        const current: ApiSpec | null = fs.existsSync(outPath) ? JSON.parse(fs.readFileSync(outPath, 'utf8')) : null;
        if (!current) {
            console.error(`${name}: no committed spec; run contract:update`);
            breaking++;
            continue;
        }
        const drift = compare(current, next);
        for (const line of drift.breaking) {
            console.error(`${name}: BREAKING ${line}`);
        }
        for (const line of drift.additive) {
            console.log(`${name}: additive ${line}`);
        }
        breaking += drift.breaking.length;
        additive += drift.additive.length;
        continue;
    }

    fs.mkdirSync(specsDir, {recursive: true});
    fs.writeFileSync(outPath, `${JSON.stringify(next, null, 2)}\n`);
    console.log(`${name}: ${Object.keys(next.methods).length} methods, ${Object.keys(next.schemas).length} schemas`);
}

if (check) {
    if (breaking > 0) {
        console.error(`\n${breaking} breaking change(s). Run \`npm run contract:update\`, review the diff, and update the fake and the plugin to match.`);
        process.exit(1);
    }
    console.log(additive > 0 ? `\n${additive} additive change(s); run \`npm run contract:update\` to pull them in.` : 'No drift.');
}

function trim(discovery: Discovery, methodIds: string[], fields: Record<string, string[]>, problems: string[]): ApiSpec {
    const all = new Map<string, DiscoveryMethod>();
    collectMethods(discovery, all);

    const methods: Record<string, MethodSpec> = {};
    const roots: string[] = [];
    for (const id of methodIds) {
        const method = all.get(id);
        if (!method) {
            problems.push(`${id} is not in the published Discovery document`);
            continue;
        }
        const parameters: Record<string, ParameterSpec> = {};
        for (const [param, spec] of Object.entries(method.parameters ?? {})) {
            parameters[param] = {
                type: spec.type,
                location: spec.location,
                ...(spec.required ? {required: true} : {}),
                ...(spec.enum ? {enum: spec.enum} : {}),
                ...(spec.repeated ? {repeated: true} : {}),
                ...(spec.format ? {format: spec.format} : {}),
            };
        }
        const uploadPath = method.mediaUpload?.protocols?.simple?.path;
        methods[id] = {
            httpMethod: method.httpMethod,
            path: `/${discovery.servicePath}${method.flatPath ?? method.path}`,
            ...(uploadPath ? {uploadPath} : {}),
            parameters,
            ...(method.request ? {request: method.request.$ref} : {}),
            ...(method.response ? {response: method.response.$ref} : {}),
        };
        roots.push(...[method.request?.$ref, method.response?.$ref].filter((r): r is string => Boolean(r)));
    }

    const schemas: Record<string, JsonSchema> = {};
    const queue = [...roots];
    while (queue.length) {
        const schemaName = queue.pop()!;
        if (schemas[schemaName]) {
            continue;
        }
        const schema = discovery.schemas[schemaName];
        if (!schema) {
            problems.push(`schema ${schemaName} is referenced but not defined`);
            continue;
        }
        let converted = convert(schema);
        if (fields[schemaName]) {
            converted = pick(converted, fields[schemaName], schemaName, problems);
        } else if (Object.keys(converted.properties ?? {}).length) {
            problems.push(`${schemaName} is reachable but has no "fields" entry in operations.json`);
        }
        schemas[schemaName] = converted;
        queue.push(...refs(converted));
    }
    for (const schemaName of Object.keys(fields)) {
        if (!schemas[schemaName]) {
            problems.push(`"fields" lists ${schemaName}, which none of the methods reach`);
        }
    }

    return {
        api: discovery.name,
        version: discovery.version,
        methods: Object.fromEntries(Object.entries(methods).sort(([a], [b]) => a.localeCompare(b))),
        schemas: Object.fromEntries(Object.entries(schemas).sort(([a], [b]) => a.localeCompare(b))),
    };
}

function collectMethods(resource: DiscoveryResource, out: Map<string, DiscoveryMethod>) {
    for (const method of Object.values(resource.methods ?? {})) {
        out.set(method.id, method);
    }
    for (const child of Object.values(resource.resources ?? {})) {
        collectMethods(child, out);
    }
}

// Discovery's schema dialect to closed JSON Schema. 64-bit integers travel as strings.
function convert(schema: DiscoverySchema): JsonSchema {
    if (schema.$ref) {
        return {$ref: schema.$ref};
    }
    if (schema.type === 'any') {
        return {};
    }
    const out: JsonSchema = {};
    if (schema.type) {
        out.type = schema.type;
    }
    if (schema.enum) {
        out.enum = schema.enum;
    }
    if (schema.format === 'int64' || schema.format === 'uint64') {
        out.pattern = '^-?[0-9]+$';
    } else if (schema.format === 'date-time' || schema.format === 'google-datetime') {
        out.format = 'date-time';
    }
    if (schema.items) {
        out.items = convert(schema.items);
    }
    if (schema.type === 'object') {
        if (schema.properties) {
            out.properties = Object.fromEntries(Object.entries(schema.properties).map(([k, v]) => [k, convert(v)]));
        }
        // Closed: a field the fake invents fails. Map-typed objects keep their value schema.
        out.additionalProperties = schema.additionalProperties ? convert(schema.additionalProperties) : false;
    }
    return out;
}

// Keeps only the listed property paths. A path without children keeps the whole property.
function pick(schema: JsonSchema, paths: string[], at: string, problems: string[]): JsonSchema {
    if (schema.items) {
        return {...schema, items: pick(schema.items, paths, at, problems)};
    }
    const children = new Map<string, string[]>();
    for (const [head, ...rest] of paths.map((p) => p.split('.'))) {
        children.set(head, [...(children.get(head) ?? []), ...(rest.length ? [rest.join('.')] : [])]);
    }
    const properties: Record<string, JsonSchema> = {};
    for (const [head, rest] of [...children].sort(([a], [b]) => a.localeCompare(b))) {
        const property = schema.properties?.[head];
        if (!property) {
            problems.push(`${at}.${head} removed (listed in operations.json "fields")`);
        } else {
            properties[head] = rest.length ? pick(property, rest, `${at}.${head}`, problems) : property;
        }
    }
    return {...schema, properties};
}

function refs(node: unknown, found: string[] = []): string[] {
    if (Array.isArray(node)) {
        node.forEach((item) => refs(item, found));
    } else if (node && typeof node === 'object') {
        for (const [key, value] of Object.entries(node)) {
            if (key === '$ref' && typeof value === 'string') {
                found.push(value);
            } else {
                refs(value, found);
            }
        }
    }
    return found;
}

function compare(current: ApiSpec, next: ApiSpec): {breaking: string[]; additive: string[]} {
    const out = {breaking: [] as string[], additive: [] as string[]};

    for (const [id, method] of Object.entries(current.methods)) {
        const updated = next.methods[id];
        if (!updated) {
            out.breaking.push(`${id} removed`);
            continue;
        }
        for (const key of ['httpMethod', 'path', 'uploadPath', 'request', 'response'] as const) {
            if (method[key] !== updated[key]) {
                out.breaking.push(`${id} ${key} ${method[key]} -> ${updated[key]}`);
            }
        }
        for (const [param, spec] of Object.entries(updated.parameters)) {
            const old = method.parameters[param];
            if (!old) {
                (spec.required ? out.breaking : out.additive).push(`${id} parameter ${param} added${spec.required ? ' (required)' : ''}`);
                continue;
            }
            if (spec.required && !old.required) {
                out.breaking.push(`${id} parameter ${param} became required`);
            }
            // A changed type, location or format, a lost `repeated`, or a removed enum value can
            // make a value the plugin sends invalid, so they count as breaking.
            for (const key of ['type', 'location', 'format'] as const) {
                if (old[key] !== spec[key]) {
                    out.breaking.push(`${id} parameter ${param} ${key} ${old[key]} -> ${spec[key]}`);
                }
            }
            if (old.repeated && !spec.repeated) {
                out.breaking.push(`${id} parameter ${param} is no longer repeatable`);
            }
            if (old.enum || spec.enum) {
                const removed = (old.enum ?? []).filter((v) => !(spec.enum ?? []).includes(v));
                if (removed.length) {
                    out.breaking.push(`${id} parameter ${param} enum values removed: ${removed.join(', ')}`);
                } else if (!old.enum) {
                    out.breaking.push(`${id} parameter ${param} is now restricted to: ${spec.enum!.join(', ')}`);
                }
                const added = (spec.enum ?? []).filter((v) => !(old.enum ?? []).includes(v));
                if (added.length && old.enum) {
                    out.additive.push(`${id} parameter ${param} enum values added: ${added.join(', ')}`);
                }
            }
        }
        for (const param of Object.keys(method.parameters)) {
            if (!updated.parameters[param]) {
                out.breaking.push(`${id} parameter ${param} removed`);
            }
        }
    }

    for (const [schemaName, schema] of Object.entries(current.schemas)) {
        const updated = next.schemas[schemaName];
        if (!updated) {
            out.breaking.push(`schema ${schemaName} removed`);
            continue;
        }
        compareSchema(schema, updated, schemaName, out);
    }
    for (const schemaName of Object.keys(next.schemas)) {
        if (!current.schemas[schemaName]) {
            out.additive.push(`schema ${schemaName} added`);
        }
    }
    return out;
}

function compareSchema(old: JsonSchema, next: JsonSchema, at: string, out: {breaking: string[]; additive: string[]}) {
    for (const key of ['$ref', 'type', 'pattern', 'format'] as const) {
        if (old[key] !== next[key]) {
            out.breaking.push(`${at} ${key} ${old[key]} -> ${next[key]}`);
        }
    }
    if (old.enum || next.enum) {
        const removed = (old.enum ?? []).filter((v) => !(next.enum ?? []).includes(v));
        const added = (next.enum ?? []).filter((v) => !(old.enum ?? []).includes(v));
        if (removed.length) {
            out.breaking.push(`${at} enum values removed: ${removed.join(', ')}`);
        }
        if (added.length) {
            out.additive.push(`${at} enum values added: ${added.join(', ')}`);
        }
    }
    if (old.items || next.items) {
        compareSchema(old.items ?? {}, next.items ?? {}, `${at}[]`, out);
    }
    for (const [prop, schema] of Object.entries(old.properties ?? {})) {
        const updated = next.properties?.[prop];
        if (updated) {
            compareSchema(schema, updated, `${at}.${prop}`, out);
        } else {
            out.breaking.push(`${at}.${prop} removed`);
        }
    }
    for (const prop of Object.keys(next.properties ?? {})) {
        if (!old.properties?.[prop]) {
            out.additive.push(`${at}.${prop} added`);
        }
    }
    if (typeof old.additionalProperties === 'object' && typeof next.additionalProperties === 'object') {
        compareSchema(old.additionalProperties, next.additionalProperties, `${at}{}`, out);
    } else if (typeof old.additionalProperties === 'object' && next.additionalProperties === false) {
        out.breaking.push(`${at} no longer allows additional properties`);
    }
}
