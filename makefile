# Quality gates. `make check` must pass before a change is done (see AGENTS.md).
# Tools come from `nix develop` (go, golangci-lint, govulncheck, bun) or from CI.

.PHONY: check check-go check-web check-site check-repo check-tofu fuzz notices

check: check-go check-web check-site check-repo check-tofu

check-go:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go vet -tags dev ./...
	golangci-lint run ./...
	golangci-lint run --build-tags dev ./...
	govulncheck ./...
	scripts/check-coverage.sh
	go test -tags dev ./cmd/permaily-enclave

check-web:
	cd web && bun install --frozen-lockfile && bun run build && bun run test

check-site:
	cd site && bun install --frozen-lockfile && bun run check && bun run build && bun run test

check-repo:
	bun install --frozen-lockfile
	bun run lint
	bun scripts/check-design-system.ts
	bun scripts/check-skills.ts
	bun scripts/third-party-notices.ts --check
	bun test scripts/
	cd packages/mail && bun install --frozen-lockfile && bun run check && bun run test && bun run typecheck
	node scripts/check-release-notes.mjs

# tofu fmt and validate of deploy/tofu, here and in the drift check's layout
# (only deploy/); downloads the locked providers, touches no account.
check-tofu:
	scripts/tofu-validate.sh

# Every fuzz target, with the number of executions CI runs (scripts/fuzz.sh,
# one group per leg of the fuzz job in CI).
fuzz:
	scripts/fuzz.sh

notices:
	bun scripts/third-party-notices.ts
