# CI/CD workflows

| Workflow | Trigger | Gates |
|---|---|---|
| `structure.yml` | PR, push | Locked service roots exist; no identity service; Postman artifacts are valid JSON |
| `backend.yml` | PR, push | Per module (`shared/go`, `gateway/api-gateway`, `services/{verification,media-document,notification,individual-owner}-service`, `tests/e2e`): `gofmt`, `go mod tidy` clean, `go vet`, `staticcheck`; architecture boundary check + self-test; `sqlc diff` (generated code current); `go test -race` with real PostgreSQL 16, Redis 7 and (media) MinIO — `CF_REQUIRE_INTEGRATION=1` turns missing infrastructure into failures; migration up/down round-trips; OpenAPI drift tests; black-box end-to-end run of all foundation components as processes |
| `contracts.yml` | PR, push | OpenAPI 3.1 documents valid; JSON Schemas valid against draft 2020-12; Postman JSON valid |
| `security.yml` | PR, push, weekly | `govulncheck` per module; gitleaks over full git history and working tree; Trivy IaC/Dockerfile misconfiguration scan (HIGH/CRITICAL) |
| `images.yml` | PR (build only), push to `development`/`staging`/`main` | Multi-stage distroless non-root images for `api-gateway`, `verification-service`, `media-document-service`, `notification-service`, `individual-owner-service`; PR smoke checks; push to GHCR with provenance + SBOM; Trivy image scan (HIGH/CRITICAL, fixed) on the pushed digest; `image-digests` artifact |
| `promote.yml` | manual, `production` environment approval | Verifies each digest exists and equals the digest certified on `:staging`, tags `production-<run>`, emits the production digest block — never rebuilds |

Branch model: `development` → `staging` → `main`. Images are tagged by commit SHA and branch;
deployments (`infrastructure/docker/{staging,production}`) reference digests only.

Toolchain pins: Go 1.26.9, staticcheck 2026.2.1, sqlc 1.31.1, govulncheck 1.8.0,
gitleaks 8.28.0, Trivy 0.57.0 (container pinned by digest; no third-party scan action).

Reviewed false positives for secret scanning are listed in `/.gitleaksignore`.
