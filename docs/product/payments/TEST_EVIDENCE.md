# Local test and certification evidence — 10 October 2026

> Historical Sections 1–3 baseline. The current [Sections 4–6 closure](SECTIONS_4_6_CLOSURE_REPORT.md) and [operations guide](SECTIONS_4_6_OPERATIONS.md) supersede deferred lifecycle descriptions below. Original test evidence and scope are retained.

Final evidence is saved as final-test-results.jsonl, payment-final-test-results.jsonl and test-summary.json. summarize_tests.py rejects a final failure or skipped test. Counts distinguish top-level tests from subtests; repeated runs are not added together.

| Executed command | Environment / scope | Result |
|---|---|---|
| go test -race -count=1 -json ./services/payment-service/... ./gateway/api-gateway/... ./services/individual-owner-service/... | Go 1.26.9 on Windows; real PostgreSQL 18 loopback :55439; real Redis 8.8 :55440; CF_REQUIRE_INTEGRATION=1; CGO_ENABLED=1, checksum-verified portable GCC 16.2 UCRT | PASS: Payment 24 tests + 12 subtests; gateway 22 tests; Owner 122 tests; zero skipped/failed |
| go test -race -count=1 -json ./services/payment-service/... | Final Payment changes, same real DB/toolchain; includes complete module, migrations, financial concurrency, official HTTP simulators, schemas and embedded real JetStream | PASS: 24 tests + 12 subtests; zero skipped/failed |
| go test -race -count=1 ./services/individual-owner-service/tests/integration -run 'Test(AutomaticWallet\|MigrationsRoundTrip\|UpstreamFixtures)' | Final generated Owner registration model; real PostgreSQL | PASS: 3 targeted tests (also covered in the complete Owner suite) |
| go test -count=1 ./services/individual-owner-service/internal/integrations/payment | Real Ed25519 signer/verifier and authenticated HTTP contract simulator | PASS: 1 test; subsequently included in complete race suite |
| go vet ./... and staticcheck ./... | GOWORK=off, independently in Payment, Owner and gateway; staticcheck 2026.2.1 | PASS in all three modules |
| sqlc generate / diff -f sql/sqlc.yaml | sqlc v1.31.1, Payment and Owner | PASS; generated payout-reference query and Owner queue model current |
| go mod tidy | GOWORK=off in Payment | PASS; dependencies committed; no service-to-service module import |
| go build ./cmd/api ./cmd/worker ./cmd/migrate | Independent Payment and Owner modules | PASS; source builds, not a Docker-image certification |
| scripts/ci/check-architecture.sh --self-test; scripts/ci/check-architecture.sh | Git Bash, all repository modules | PASS; cross-service DB/module ownership preserved, Paystack confined to Payment |
| git diff --check; gofmt | Changed source/contracts | PASS |
| Generate contracts; route/Postman/schema parity tests | 20 internal + 6 public + 1 webhook operation; 8 finance.v1 event schemas | CONTRACT PASS |

Payment integration uses real PostgreSQL-created disposable databases and the actual Goose migrations. All user role classes, idempotent registrations, verified multi-role linkage, provider/entity/currency isolation, positive/balanced/immutable/sealed journals, reversal, seven exact balance views, cursor pagination, restriction/retention, valid signatures, mismatched charge context, duplicate callbacks and unknown outcomes are tested. Sixteen contending reservations of ten minor units against one hundred allow exactly ten successes and leave zero available; they never create a negative balance. Twelve concurrent charge verifications produce one journal. DVA receipt matching also produces one economic receipt under concurrency and rejects revoked verification.

Paystack tests use the actual adapter against explicit httptest official-wire simulators. The same signed HTTP intent contract also runs through a neutral adapter and the Paystack implementation. Neither simulator is a real Paystack sandbox transaction. Actual merchant secrets, Manual Payouts approval and DVA eligibility were not configured, so **Paystack sandbox NOT RUN**, **DVA provisioning externally uncertified**, **custody NOT APPROVED** and **production NOT APPROVED**.

The runtime publication test starts the actual Payment application without provider clients, checks readiness/provisioning and relays a committed wallet event over an embedded **real** JetStream network/server. It verifies broker acknowledgement, configured subject prefix and shutdown. This is local transport integration, not production broker credential/ACL/TLS or cross-service process deployment certification. The outbox failure/crash test additionally uses an explicit recording publisher to deterministically force failure and lease recovery.

Initial failures were corrected before final certification: PostgreSQL reserved-word migration syntax, missing scaffold outbox migration, empty rollback view handling, outdated gateway reserved-route expectation, Windows CRLF generated-contract comparison, Owner's former assumption of no Payment schemas, nil UUID history cursor handling and a test fixture's incorrect stream message count. The first race attempt lacked CGO/compiler support; a verified portable compiler enabled the passing race runs. No failed/superseded run is promoted to PASS. Earlier diagnostic logs are retained separately as regression-test-results.jsonl and payment-test-results.jsonl; the final files above control the verdict.

Docker was unavailable locally; image build, published digest scanning, hosted CI, staging deployment, real Paystack sandbox, production broker ACL and live-money/custody E2E gates were **NOT RUN**. Backend/image CI matrices now include Payment and the SQLC drift gate includes it. tests/e2e's existing whole-foundation process suite was not rerun because this change does not certify later Owner payment lifecycle integration or those unrelated media/provider deployments. Their prior evidence is not claimed as evidence for this release.

Reproduce with scripts/payments/certify.ps1 after supplying real PostgreSQL/Redis, Go 1.26.9, a C compiler for Windows race tests, staticcheck 2026.2.1, sqlc v1.31.1 and Git Bash. The script performs local certification only and does not publish, activate providers or push Git.
