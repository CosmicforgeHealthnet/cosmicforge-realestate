# Financial schema and invariants

Payment migrations 00001–00004 operate only on payment_db. Individual Owner migration 00019 adds its own registration-delivery queue and backfills non-closed accounts. Existing Owner banking, tenancy and financial workflow tables are unchanged.

| Entity | Ownership and protection |
|---|---|
| financial_identities | Stable person/organization and ACTIVE/RESTRICTED/CLOSED state; no financial-history delete API |
| identity_bindings | Immutable domain/account association; unique domain/account and domain/identity; linkage evidence |
| wallets | Immutable identity/domain/role/entity/currency dimensions, unique provisioning |
| provider_accounts; custody_pools | Scoped provider/merchant/entity/mode; one pool currency; restrictive default, reserve and custody approval |
| ledger_accounts | Immutable wallet/platform, pool, currency/entity, category and dimension_ref; database dimension/normal-side validation |
| journals; journal_entries | Unique operation fingerprint, authorization/policy, origin, reversal reference, timestamp; positive bigint entries; deferred balance/dimension/nonnegative checks |
| account_balances | Numeric SQL aggregation of immutable entries, not writable money; JSON aggregates are exact strings |
| payment_intents | Durable source/idempotency key, immutable amount/currency/route/evidence snapshot; separate collection/settlement states |
| provider_inbox | Selected merchant, raw-body digest, normalized type/reference, retry/lease/token/dead state; no raw payload |
| provider_observations | Immutable redacted operation result/metadata; collection amount/currency/fees can be retained |
| settlement_observations | Immutable batch evidence, amount/fees/status, unique evidence fingerprint; changing provider statuses append |
| reconciliation_exceptions | Pool, expected/observed minor units and reason; release-risk restriction |
| verification_refs | Authoritative verifier status, reference, expiry/revocation; audited updates |
| virtual_accounts | Wallet/provider, consent version/times, KYC, unique customer/external/bank digest mapping, masked account and durable states |
| collection_receipts | Immutable verified DVA funding receipt and matched journal; merchant/reference deduplication |
| payout_account_refs | Immutable verified bank reference/jurisdiction/expiry; no raw bank account or executable entitlement |
| financial_audit | Immutable authenticated action and evidence |
| outbox_events | Financial event in same transaction; broker-acknowledged delivery, leased retries and dead letters |

Posting categories: PAYMENT_PENDING, PENDING_EARNINGS, PROTECTED, AVAILABLE, FROZEN, WITHDRAWAL_PENDING, REFUND_PENDING and DEPOSIT are credit-normal liabilities. PROVIDER_RECEIVABLE and SETTLED_CASH are debit-normal assets; PROCESSING_EXPENSE is debit-normal. REVENUE, RESERVE and SETTLEMENT_CONTROL are credit-normal; TREASURY reserves internal reconciliation accounting. Platform-only categories must be created without a public wallet. Source property/project/branch/job/beneficiary dimensions are opaque authoritative references; the foundation does not invent entitlements.

Each journal balances within one currency, legal entity and provider/merchant pool. No inter-pool funding equivalence exists. Deferred PostgreSQL checks prohibit committing a one-sided or dimension-mismatched journal and negative controlled assets/liabilities. Positive per-entry int64 amounts and numeric aggregate sums prevent float and summation overflow. Posted header/entries/accounts/wallets cannot be updated/deleted; the seal prevents later insertion into a posted journal. Reversal reads the original entries, flips sides and links exactly one compensation to its original journal.

The engine takes an operation idempotency lock, a pool row lock, sorted account locks and identity locks. Competing reservations cannot overspend AVAILABLE. AVAILABLE postings reject restricted pools or identities; new AVAILABLE credits require settled cash less reserve to cover **all** pool liabilities. This conservative coverage rule also reserves pending liabilities. The user-facing withdrawable flag remains false for every view in this release because approved withdrawal capability is absent.

A verified collection posts debit PROVIDER_RECEIVABLE and credit PAYMENT_PENDING (FROZEN if the identity is restricted). A matched DVA top-up uses the same pending accounting and a provider-scoped receipt. Neither is settled cash. Settlement observation and merchant-balance comparison do not post or release money. A mismatch/reversal restricts the pool; it cannot silently edit prior entries. Full settlement-to-charge matching and downstream release remain later lifecycle work.

Seven views come from account_balances; DEPOSIT adds to PROTECTED exactly once. Pools remain separate. A newly provisioned wallet with no assigned pool returns seven zero views, using the nil pool ID as an unassigned sentinel. Transaction history uses entry_id cursors, so two wallet entries in one journal are not skipped between pages.

Clean migration upgrade/down/up passes on real PostgreSQL. Rollback refuses removal of journals, wallet registrations, financial audit, collected receipts or verified payout references requiring retention. Empty reference tables can be rolled back; migration 00002 also prevents dropping the financial outbox while journals or outbox records exist. Goose migration transactions and the migration role must be retained for controlled deployment. Recovery of a funded database uses audited forward changes or approved backups, not a destructive rollback command.
