# Backend Test Gates

No service is considered complete because it compiles.

## Every Go service

Required:
- gofmt/gofmt check
- go vet
- static analysis
- dependency/vulnerability scan
- unit tests
- PostgreSQL integration tests for repositories/transactions
- migration validation from empty schema to current
- API contract tests
- authorization tests
- error-envelope tests
- health/readiness/version tests
- race detector on concurrency-sensitive packages
- graceful shutdown test
- observability/request-context propagation tests

## Internal API consumers

Required:
- caller authentication tests
- allowed-caller/denied-caller tests
- timeout behavior
- downstream failure mapping
- no cross-database dependency

## NATS/event consumers

Required:
- duplicate delivery
- reordered/non-relevant event handling where applicable
- poison event behavior
- retry/recovery
- expired worker lease recovery
- outbox publish recovery after process crash

## Idempotent commands

Required:
- repeated identical request
- same key with different payload
- concurrent duplicate request
- crash between DB commit and response
- crash between outbox claim and broker ack

## Payment-sensitive code

Before production:
- unknown provider outcome
- webhook duplication/out-of-order
- concurrent refund/payout
- reconciliation mismatch
- provider timeout/read-back
- no duplicate money movement under retry

## Security

Required:
- invalid/expired/wrong-audience JWT
- cross-domain token misuse
- service-token wrong audience
- unauthorized service caller
- privilege escalation attempt
- rate-limit behavior on exposed auth routes
- secrets absent from logs

## Release gate

development -> staging requires all affected CI gates green.

staging -> main requires:
- deployed staging migration success;
- service health/readiness green;
- E2E critical journey pass;
- rollback/recovery procedure validated for the release class;
- no unresolved critical/high security finding.
