# Payment ownership, architecture and authorization

Authority: master PDF v1.0, 10 October 2026, Sections 1–3. See IMPLEMENTATION_PLAN.md for discovery and source checksum, ADR-0012/0013 for decisions, and IMPLEMENTATION_COVERAGE.md for evidence.

## Reconciliation with the repository

Before work: development at 18cf3fab112827926c150813a70c983449b13a5f, clean tree, remote fetched and matching. Payment was a scaffold. Individual Owner is the first implemented public domain. Each domain already owns identity/authentication data; a central identity service would conflict with the repository and was not introduced. Shared Ed25519 service JWTs, gateway delegation, PostgreSQL, Goose, pgx, UUIDv7, ISO money, events, outbox, health, HTTP and testing packages are reused. Payment needs PostgreSQL; its relay worker also needs JetStream. Redis remains the gateway's shared distributed rate-limit dependency. Notification and verification are authenticated peer boundaries, not Payment-owned databases.

| Authority | Business facts retained there | Financial integration |
|---|---|---|
| Marketplace | Applications, selections, all-in accepted quote, agreements, customer consent/disputes | Own registered wallets, evidence-linked intents |
| Individual Owner | Property/ownership, leases, offers, signed agreements, handover, prices, banking change requests | Durable automatic wallet registration; own wallets/intents; later beneficiary/release/refund API stays deferred |
| Business Operations | Corporate ownership, branches, staff approvals, projects, hospitality records | Organization roles, project/branch dimensions and signed evidence |
| Property Manager | Owner mandates, limits, assigned properties and earned fees | Own fee wallet only; no owner proceeds or bank-redirection authority |
| Artisan | Quotes, assignments, materials, milestones and completion | Own individual/company roles and job references |
| Professional Services | Appointments, conflict checks, reports, fees, deliverables | Own professional/firm roles and appointment references |
| Admin/Disputes | Approved finance policy, restrictions, dispute decisions and dual approval | Audited identity controls, inbox replay and reconciliation; no arbitrary balance credit |
| Verification | Identity/business/bank verification, expiry and revocation | Signed status and payout-reference recording; verified cross-domain linkage |
| Payment | Financial identities/bindings, wallets, journals, intents, receipts, provider registry and financial history | Sole payment_db access, authenticated APIs and durable finance events |

## Permission matrix

| Operation | Permitted signed caller | Additional checks |
|---|---|---|
| Ensure wallet | Owning public domain service | Its domain, valid role/legal-party kind, registered account ID and evidence |
| Wallet/intent/DVA reads | Source domain + delegated owner; gateway on public reads | Signed actor domain/account; related wallet belongs to that binding |
| Create/verify intent | Original domain + delegated owner | Accepted evidence/policy, own active wallet, exact scoped provider result |
| DVA request/sync/deactivate | Source domain + delegated owner | Consent/KYC/merchant eligibility for issuance; deactivation does not require renewed KYC |
| Link account; verification; payout reference | Verification service | Valid party/account references, verification status and expiry; no existing-history merge |
| Identity restrict/restore/close | Admin service | Evidence, policy; two distinct approvers on restore; no outstanding obligations on close |
| Inbox replay; coverage; settlement observation/reconciliation | Admin service | Replay evidence and lease fence; exact merchant/pool; durable financial audit |
| Webhook ingest | Gateway service | Selected merchant secret HMAC over raw body plus gateway service JWT |
| Journal post/reverse and worker actions | Trusted Payment code only | Balanced entries, policy/evidence, pool/account/identity locks; no network posting API |

Internal JWT audience is payment-service. Public reads require gateway-injected X-Service-Authorization carrying signed delegation; client-supplied actor headers do not grant ownership. TLS, trusted key rotation and broker publisher ACLs are deployment requirements. NATS envelopes are versioned/source-bound; this foundation consumes authoritative financial inputs through signed HTTP rather than accepting arbitrary business events from a broker.

## Role and identity readiness

CUSTOMER; OWNER and CO_OWNER; CORPORATE_OWNER, DEVELOPER and HOSPITALITY; MANAGER and MANAGEMENT_COMPANY; ARTISAN and SERVICE_COMPANY; PROFESSIONAL and PROFESSIONAL_FIRM are supported. Company/corporate roles require organizations, individual roles require persons. Platform revenue, expense, reserve, settlement-control and treasury accounts are internal accounting accounts, not admin public wallets.

Multi-role wallets share a verified identity while remaining domain/role/entity/currency-isolated. Cross-domain linkage must precede provisioning of the second account. Unknown legal-party links are not inferred from email. A verified payout account reference is bank verification evidence, not ownership entitlement; eligible remains false until later approved payout policy exists.

Owner deployment must set PAYMENT_WALLET_SERVICE_URL and trust its signing key in Payment to activate automatic delivery. Without that configuration registrations remain durable and retryable; provider credentials are unnecessary. No future domain is required for Payment API startup. Future domains must use canonical account/party details and approved evidence from their own business/verification authorities, not forward unverified frontend claims.

The eight original feature documents v5.0 are not present. The repository Individual Owner implementation specification and existing auth/payment/banking code informed compatibility. Additional product/legal rules await their authoritative sources. Future-service absence is an integration dependency, not a defect in implemented Payment functionality.
