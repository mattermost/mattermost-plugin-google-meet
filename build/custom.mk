# Include custom targets and environment variables here

E2E_DIR = e2e/playwright

$(E2E_DIR)/node_modules: $(E2E_DIR)/package.json $(E2E_DIR)/package-lock.json
	cd $(E2E_DIR) && npm ci --ignore-scripts && npx playwright install chromium
	touch $@

## Starts the disposable Mattermost, Postgres and fake Google stack used by the e2e suite.
.PHONY: e2e-env-up
e2e-env-up:
	cd $(E2E_DIR) && docker compose up -d --wait

## Stops the e2e stack and wipes its data.
.PHONY: e2e-env-down
e2e-env-down:
	cd $(E2E_DIR) && docker compose down -v

## Builds the plugin and runs the e2e suite against the fake Google (start the stack first with e2e-env-up).
.PHONY: e2e
e2e: dist $(E2E_DIR)/node_modules
	cd $(E2E_DIR) && npm run check && npm test
