# Quality gates. `make check` must pass before a change is done (see AGENTS.md).
# Tools come from `nix develop` (go, golangci-lint, govulncheck, bun) or from CI.

.PHONY: check check-go check-web check-site check-repo fuzz notices

check: check-go check-web check-site check-repo

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
	cd web && bun install --frozen-lockfile && bun run build

check-site:
	cd site && bun install --frozen-lockfile && bun run check && bun run build && bun run test

check-repo:
	bun install --frozen-lockfile
	bun run lint
	bun scripts/check-design-system.ts
	bun scripts/check-skills.ts
	bun scripts/third-party-notices.ts --check
	node scripts/check-release-notes.mjs

# Short fuzzing run of the attestation parser (CI runs it too).
fuzz:
	go test -run='^$$' -fuzz=FuzzVerify -fuzztime=500000x ./pkg/attestation
	go test -run='^$$' -fuzz=FuzzParsePCRFile -fuzztime=400000x ./pkg/attestation
	go test -run='^$$' -fuzz=FuzzAnalyze -fuzztime=1000000x ./pkg/kmspolicy
	go test -run='^$$' -fuzz='^FuzzVerify$$' -fuzztime=300000x ./pkg/provenance
	go test -run='^$$' -fuzz='^FuzzVerifyRekor2$$' -fuzztime=200000x ./pkg/provenance
	go test -run='^$$' -fuzz='^FuzzTimestamp$$' -fuzztime=300000x ./pkg/provenance
	go test -run='^$$' -fuzz='^FuzzTrustUpdate$$' -fuzztime=200000x ./pkg/provenance
	go test -run='^$$' -fuzz=FuzzCMS -fuzztime=500000x ./internal/keys
	go test -run='^$$' -fuzz=FuzzParse -fuzztime=150000x ./internal/inbox

notices:
	bun scripts/third-party-notices.ts
