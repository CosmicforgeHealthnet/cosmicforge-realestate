# Idempotency and Transactional Outbox

## Idempotency

Mandatory for:
- payment intent creation/confirmation;
- refunds;
- payouts;
- external provider callbacks;
- verification submission;
- listing publication/withdrawal commands;
- media completion/finalization;
- notification commands when duplicate delivery would be harmful.

## Idempotency record

Recommended fields:
- id
- scope
- caller_type
- caller_id
- idempotency_key
- request_hash
- status: IN_PROGRESS | COMPLETED | FAILED_RETRYABLE | FAILED_FINAL
- response_code
- response_body/reference
- created_at
- expires_at

Uniqueness is scoped by operation + caller + key.

Same key + different request hash -> IDEMPOTENCY_CONFLICT.

Concurrent claims must be serialized by a unique constraint/transaction, not an in-memory lock.

## Transactional outbox

For an authoritative state change plus event:
1. begin PostgreSQL transaction;
2. modify authoritative state;
3. insert outbox row;
4. commit;
5. worker claims outbox rows;
6. publish to NATS JetStream;
7. mark published only after broker acknowledgement.

Outbox rows include:
- event_id
- event_type
- aggregate_type
- aggregate_id
- payload
- occurred_at
- attempts
- next_attempt_at
- lease_owner
- lease_expires_at
- published_at

Workers use bounded leasing and expired-lease recovery.

## Consumers

At-least-once delivery is assumed.

Consumers store processed event IDs or enforce domain-specific uniqueness so duplicate deliveries are harmless.
