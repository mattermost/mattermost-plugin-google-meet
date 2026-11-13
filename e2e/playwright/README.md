# Google Meet plugin Playwright tests

A hermetic e2e suite: Mattermost and Postgres run in Docker, Google is replaced by `fake-google/`,
and nothing reaches the internet (the fake's proxy refuses every host it does not fake).
No Google account or secrets are needed, locally or in CI, so the suite runs on every PR, including
PRs from forks. The harness is adapted from the Google Drive plugin's suite
(mattermost-plugin-google-drive `e2e/playwright`).

```sh
make e2e-env-up     # creates the fake's TLS certificate, starts Postgres, fake Google and Mattermost on :8085
make e2e            # builds the plugin (make dist), installs it, runs the checks and the suite
make e2e-env-down
```

From `e2e/playwright`, `npm test` runs the suite against an already started stack (`npm run server:up`)
and an already built bundle in `../../dist`. `npm run test:headed` and `npm run test:ui` help when writing
tests; `PW_SLOWMO=500` slows a headed run down.

The stack's default ports differ from the other plugins' e2e stacks, so they can run side by side:

| Variable | Default | Purpose |
|---|---|---|
| `MM_IMAGE` | `mattermost/mattermost-enterprise-edition:12.0.2` | Server image (amd64 only; on Apple silicon use Docker Desktop's Rosetta emulation) |
| `MM_PORT` | `8085` | Mattermost port, on the host and in the container; also the SiteURL port |
| `FAKE_GOOGLE_PORT` | `8100` | Fake Google's control API and browser pages, on the host |
| `E2E_FAKE_HTTPS_PORT` | `8463` | Fake Google's HTTPS, on the host (443 in the Cursor agent environment, where the browser maps `accounts.google.com` to it) |
| `E2E_DB_PORT` | `5435` | Postgres on the host, for the KV helpers |

Set them in the shell for both `npm run server:up` and `npm test`.

## How Google is faked

- The plugin's server calls `oauth2.googleapis.com`, `meet.googleapis.com` and `www.googleapis.com` with
  Go's default HTTP transport. Mattermost runs with `HTTPS_PROXY` pointing at the fake, which tunnels
  those hosts to itself over TLS with a throwaway CA (`fake-google/gen-certs.sh`) that Mattermost trusts
  through `SSL_CERT_FILE`. The bundle under test is the normal `make dist` build. The proxy refuses
  every other host with a 403, so the stack never reaches the internet (compose also turns off
  notices, push and the marketplace, so Mattermost does not keep retrying them). An unfaked Google
  host is also a contract violation, so a new endpoint fails clearly instead of reaching Google.
- The browser is not behind the proxy. `lib/meet_oauth.ts` routes Google's sign-in pages to the fake,
  and answers every `https://meet.google.com` page (the auto-opened meeting tab and join links) with a
  stub.
- The fake keeps Google-side state: spaces, conference records and their recordings, transcripts and
  smart notes, Calendar events, tokens and their scopes. It enforces each method's OAuth scopes, so a
  token without `meetings.space.created` gets the same `ACCESS_TOKEN_SCOPE_INSUFFICIENT` error as from
  Google. Like Google, it returns a refresh token only for `access_type=offline` (on a repeat grant,
  only with `prompt=consent`), revokes every token of a grant together, and rejects page tokens that
  are malformed or replayed with a different query.
- Tests drive it through `lib/fake_google.ts`: `issueToken()`, `revokeToken()`, `failNext(method,
  status)` for error paths, `createSpace()`, `startConference()`, `addArtifact()`, `addEvent()`, and
  `requests()` (a log of every call the plugin made).
- Most tests connect users with `connectUser()` (`lib/kv.ts`), which stores a fake token in the
  plugin's KV store exactly as the OAuth callback would. Only `connect_account.spec.ts` goes through
  the sign-in and consent pages. The token storage format is pinned on both sides by one test vector,
  `server/store/kvstore/testdata/e2e_crypto_vector.json`, which `npm run verify:crypto` and the Go test
  `e2e_crypto_vector_test.go` both read.
- The plugin polls Google instead of receiving webhooks. Its poll interval is set to a day, so the
  poller never runs on its own during a test; tests that need a cycle call `runPollCycle()`
  (`lib/meet_api.ts`), which triggers `POST /admin/poll-now` and waits until the fake has gone quiet.
  Conferences, artifacts and Calendar events come from `fakeGoogle.startConference()`,
  `addArtifact()` and `addEvent()`, with times relative to now, so tests backdate instead of waiting.
- Before every test, `lib/test.ts` resets the fake and clears the plugin's KV store, so no test
  depends on what an earlier one left behind. Because of that, the suite refuses to run unless
  Mattermost, Postgres and the fake are on localhost (`lib/safety.ts`); set
  `E2E_ALLOW_REMOTE_SERVER=1` only for a disposable remote test server.
- The webapp registers its button and websocket handler only after it fetches `config/status`, so
  specs call `waitForMeetWebapp()` (`pages/meet_webapp.ts`) after the channel loads.

## Keeping the fake honest

- Every request the plugin sends and every response the fake returns is validated against Google's
  published Discovery documents, trimmed to the methods and fields in
  `fake-google/contract/operations.json` (closed schemas: a field Google does not document fails), plus
  the response fields the plugin reads (`fake-google/contract/required.ts`). The fixture in
  `lib/test.ts` fails the test that caused a violation; global teardown fails the run on violations
  that arrived after their test was checked.
- `npm run test:contract` tests the validator itself, so a bug in it cannot pass everything, and
  smoke-tests the fake (`fake-google/smoke.test.ts`): it drives OAuth and every Meet and Calendar
  route the plugin calls, empty and populated, and fails if any of the fake's own responses, error
  ones included, break the contract. It also checks that an API called on the wrong Google host is
  caught (through the proxy, every faked host reaches the same server).
- New Google method or field: add it to `operations.json`, run `npm run contract:update`, and commit
  `fake-google/contract/specs/`.
- Nightly, `.github/workflows/e2e-contract-drift.yml` runs `npm run contract:check`, which fails on
  breaking changes to those methods in Google's published specs and opens an issue (a failed
  download only fails the run).
- There is no real-Google lane yet. The Google Drive plugin has an opt-in one (`npm run test:real`);
  here, responses with conference data cannot come from real Google without a live meeting, so they
  are checked only against the published spec. Connecting an account and creating a space could get
  an opt-in real lane later.

## Screenshot baselines

`specs/visual.spec.ts` (tagged `@visual`, skipped by `npm test`) compares the meeting post, the
conference post, the ended meeting's summary and the artifact thread with PNGs in
`specs/visual.spec.ts-snapshots/`. `npm run test:visual` runs them on a separate compose project
(`script/visual.sh`: Mattermost 12.0.2 on :8078, other ports for the rest) with a Playwright browser in
Docker, so the screenshots match on any machine; the normal stack can keep running. After an
intended UI change, run `npm run test:visual-update-snapshots` and review the new PNGs. In CI this is
the non-blocking `visual` job. Copied from the Google Calendar suite.

## Writing tests

- Specs import `test` and `expect` from `lib/test.ts`, not from Playwright (ESLint enforces it).
- Specs hold no locators or raw page interactions; those go in `pages/`.
- Every test has a JSDoc block with `@objective` (and `@precondition` where it needs one), a tag,
  and `// #` action and `// *` verification comments (`npm run lint:test-docs`).
- `npm run check` runs the type check, ESLint, the doc lint, the contract self-tests and the crypto
  vector.
