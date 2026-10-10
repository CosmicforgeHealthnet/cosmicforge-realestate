# Sections 4–6 implementation register

Authorized by the user-supplied implementation prompt at C:/Users/favou/.codex/attachments/358cded3-c720-45b9-b21d-6f205d012f34/Pasted text.txt. Authority: retained master PDF v1.0, 10 October 2026, SHA-256 fd1cfd14fb2f78ed7601fbafc49113f7d03049d3ef8041f6f08f0342d964e7f1. Document requirements guide implementation; they do not activate production financial operations.

Starting branch codex/payment-sections-1-3, SHA 789411c220bcccdb5301b90ff7d0e28e61b77353, clean tree, remote fetched. Work branch codex/payment-sections-4-6. Existing core/finance/provider/auth/outbox primitives are reused. No cross-service database access or competing ledger. Original eight v5 feature documents remain unavailable; existing Owner contracts remain authoritative integration references.

1. Section 4: immutable accepted quote/components and authority references, exact versioned allocation, independent holds, transaction-level settled evidence, partial release, compensated corrections, available-only withdrawal, one durable provider attempt and original-reference readback. Real PostgreSQL race/recovery tests.
2. Section 5: all 18 separate category templates and immutable jurisdiction/contract/product/effective policy versions. Templates are disabled; activation requires signed Admin approval and explicit cancellation/refund/dispute references. No invented tariff or statutory time limits.
3. Section 6: immutable agreement versions, manually commanded obligations by period, recurrence classification, owner-side configured commission and mandate-specific manager fee, separate deposit obligations, receipt projection. No due-date scheduler or automatic debit.
4. Contracts/docs: signed internal commands and safe public reads, OpenAPI/Postman/event schemas, baseline regression, vet/staticcheck/SQLC/build/architecture gates. Commit locally; export exact-SHA A–K closure to tmp/payments/SECTIONS_4_6_CLOSURE_REPORT.md. No push. Stop after Section 6.

Live transfer/custody gates remain disabled. Test-mode provider payout adapters require explicit capability/approval configuration. Merchant balance alone cannot attest settled transaction cash or lawful safeguarding; release needs matched settlement evidence and policy-required custody evidence. External sandbox/provider/legal/staging approvals are reported separately.
