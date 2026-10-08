# Event Architecture

NATS JetStream is the initial durable event transport.

Where a database state change and emitted event must be atomic from the producer's perspective, services use a transactional outbox.

Events are versioned, for example:
- listing.published.v1
- listing.updated.v1
- verification.completed.v1
- payment.confirmed.v1

Consumers must be idempotent.
