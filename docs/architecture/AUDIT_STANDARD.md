# Audit and Security Standard

## Audit events

Security-sensitive and business-authority transitions must record:
- audit_id UUIDv7
- occurred_at
- service
- action
- actor_type
- actor_domain where applicable
- actor_id where applicable
- target_type
- target_id
- request_id
- trace_id
- source metadata where lawful/appropriate
- before/after summary or changed fields where safe
- result

## Mandatory audit categories

- registration/security setting changes
- password/MFA/session changes
- role/permission changes
- organization/staff membership changes
- verification decisions/manual reviews
- property authority decisions
- listing publication/suspension/withdrawal
- admin moderation actions
- payment/refund/payout actions
- changes to payout destinations
- media access-policy changes
- privileged document downloads
- service configuration changes affecting authorization or money

## Sensitive-data rules

Do not place secrets or raw regulated documents into audit payloads.

Use stable references/hashes where evidence linkage is required.

## Admin

Admin actions are attributable to a specific admin account and cannot use generic shared operator identities in production.
