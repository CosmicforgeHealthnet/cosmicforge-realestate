# Section 5 — database catalogue and schema implementation

Baseline: `a051e150f7fde5456d3568bd3481ca54b3d02b53`, branch `staging`, fetched and compared before edits. The authoritative specification is `docs/specifications/CosmicForge_Business_Property_Operations_Full_Backend_Implementation_Spec_v1.0.md` (all sections and Annexes D–J reviewed). Sections 1–4 are retained. Delivery SHA and Actions evidence are recorded in the combined certificate; this report does not certify live staging connectivity.

## Catalogue and ownership

The complete [human catalogue](business-operations-database-catalogue.md) and [machine catalogue](business-operations-database-catalogue.json) contain **279 decisions: 122 implemented tables and 157 explicitly deferred future-domain tables**. Every Section 5 name and Annex D shorthand is reconciled, including aliases such as credentials → business_credentials, holds → inventory_holds, restrictions → rate_restrictions and source_refs → channel_reservation_refs. Supporting tables are also included. The catalogue records exact owner package, originating migration, keys, named indexes, uniqueness/check constraints, tenant columns and future dependency. A present table does not certify unrelated future workflows.

There were 70 existing tables. Additive Goose migration `00006_commercial_inventory_distribution.sql` adds 52 tables:

`add_ons`, `bulk_rate_commands`, `bulk_rate_scope_links`, `channel_capabilities`, `channel_commission_rules`, `channel_conflict_decisions`, `channel_conflicts`, `channel_connections`, `channel_content_overrides`, `channel_event_delivery_keys`, `channel_event_receipts`, `channel_field_authority`, `channel_financial_inputs`, `channel_mapping_previews`, `channel_metrics_snapshots`, `channel_product_mappings`, `channel_property_mappings`, `channel_provider_dispatch`, `channel_providers`, `channel_rate_mappings`, `channel_reservation_refs`, `channel_sync_cursors`, `channel_sync_failures`, `channel_sync_jobs`, `commercial_history`, `compliance_holds`, `content_quality_scores`, `currency_policies`, `inventory_allotment_days`, `inventory_allotments`, `inventory_calendar`, `inventory_hold_days`, `inventory_holds`, `inventory_pool_members`, `inventory_pools`, `listing_approvals`, `listing_media_links`, `listing_publication`, `listing_templates`, `listing_versions`, `listings`, `package_components`, `packages`, `price_quotes`, `promotions`, `rate_plan_versions`, `rate_plans`, `rate_relations`, `rate_restrictions`, `rate_rules`, `sellable_products`, `tax_profiles`.

No replacement physical property graph or cross-service foreign keys were introduced. Inventory products reference the Section 4 canonical nodes and property anchors. Future reservations/PMS/ledger/RMS tables are deliberate deferrals to their owners, not scaffold-only completion claims.

## Engineering controls

UUIDv7 checks protect new primary IDs. Composite organisation/property foreign keys prevent mismatched tenant references. Physical pool membership is exclusive; deferred capacity and stock constraints reject manufactured capacity and overselling. Civil dates are checkout-exclusive. Exact financial values are integer minor units plus ISO currency. Histories, quotes, rate versions, mapping previews, financial source facts and approval references remain immutable; generic mutable records require version increments. Source history has an increasing sequence for restart-safe distribution checkpoints. Provider dispatch uses leases, not network calls under database locks.

The migration appends eligible initial Owner role versions and explicit capability grants. Existing role permissions/history are preserved; limited/custom staff retain their authority. A populated version-5 upgrade test verifies that only the unrestricted initial representative receives the new role, with one immutable migration audit. Repeated `up` is safe. No destructive down migration is supplied.

`sql/queries/commercial.sql` and generated `internal/repositories/db/commercial.sql.go` provide typed tenant reads, ordered calendar locks, expiry work and SKIP LOCKED sync claims. Domain repositories use the shared Querier/transaction interface for composed reads and owning transaction writes. SQLC 1.31.1 generation and drift are checked by Backend CI.

Runtime grants reserve schema/migration ownership and provider certification to the authorised operator. Immutable evidence is not updateable/deletable by runtime. Grants are applied after migrations through the existing container certificate/provisioning scripts. They do not provision or inspect another service database.

## Database dependency matrix

| Dependency | Local data owned here | External authority |
|---|---|---|
| Canonical property/physical graph | Existing nodes, edges, property anchors | Verification decides property authority |
| Inventory/revenue | Pools, products, calendars, holds, allotments, rate/fiscal/offer versions | Capacity assertion and authoritative fiscal provenance required |
| Content/distribution | Content versions, Media IDs, mapping proofs, jobs, acknowledgements, conflicts | Media owns bytes; providers own remote delivery outcomes |
| Reservation | Holds and dependency-blocked external references | Section 9 owns actual acceptance/commitment |
| Finance | Contractual commission assumptions and signed source inputs | Payment owns execution/settlement/ledger; Admin owns allocated cost facts |
| Platform | Shared audit/outbox/inbox/idempotency | Shared signed service transport and JetStream |

## Executed evidence

`TestBusinessCompleteCatalogueMatchesActualPostgreSQL` verifies all implemented tables, foreign keys and declared indexes against real PostgreSQL, rejects unexpected public tables, and confirms deferred tables do not exist. `TestBusinessCommercialMigrationPreservesPopulatedSection4Database` runs fresh/repeated upgrades and proves old graph IDs/content are preserved. `TestBusinessCommercialMigrationUpgradesOnlyUnrestrictedInitialOwner` verifies populated role migration. The complete service suite runs all prior migration tests too. Final counts, CI runtime grants and scan results are in the combined certificate.

Status: repository schema/catalogue implemented and disposable PostgreSQL certified. **Live staging database, role ACLs, real pooling/connectivity and operator rollout remain PARTIAL/unverified.**

Connection authority hardening: forward-only migration `00007_channel_connection_authority.sql` persists the operation list from the signed provider credential proof. Every capability requires both provider certification and a grant to that particular connection. Existing connections default to no grants until real revalidation. `TestBusinessCommercialProviderCertificationCannotExpandCredentialAuthority` proves registry certification cannot authorize polling/webhook intake with a narrower credential. Current Goose schema version is **7**; migration 00006 remains unchanged.
