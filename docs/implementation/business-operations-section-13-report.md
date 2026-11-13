# Business Operations Section 13 — Reviews, Reputation and Quality Loop

Migration `00033_reputation_quality.sql`. Implementation: `internal/http/operational_reputation.go`, `operational_reputation_worker.go`, `internal/reputation/reputation.go`, `internal/integrations/messaging/reviews.go`.

| Requirement | Implementation | Evidence |
|---|---|---|
| Verified Marketplace intake | Signed `POST /internal/v1/business-supply/reviews` (Marketplace identity + organisation delegation, idempotency); verified-stay only if the canonical reservation is CHECKED_OUT/COMPLETED in the property; otherwise 403 | `TestBusinessReviewsVerifiedIntakeDedupHistoryQualityLoopAndMetrics` |
| Provider-permitted external import | Leased `review_sync_cursors` per connection with `supports_reviews`; adapter required to connect; reservation linked only through canonical channel reservation references | `TestJourneyJ12ExternalReviewImportDedupAndTombstone` |
| Direct feedback / legacy references | Staff may record only `DIRECT_FEEDBACK` (departed stay required) or `LEGACY_IMPORT` (`reviews:moderate`, `LEGACY_REFERENCE`) | verified intake test |
| Unsupported source fails closed | Validation rejects unknown sources (400) | verified intake test |
| Uniqueness, versions, history | Unique (organisation, source, external id); equal/older versions are no-ops; each newer version appends immutable `review_versions`; database guard forbids same-version evidence rewrite and version regression | verified intake test, J12 |
| Tombstones and legal hold | Provider deletion → `TOMBSTONED` with history retained; excerpt preserved under legal hold; tombstoned reviews excluded from metrics | J12 |
| Responses | Provider policy (`ReplyPolicy`: support, length, no contact data/links); negative reviews require exact `COMMERCIAL_REVIEW_RESPONSE` approval bound to body digest; worker submit/readback; ambiguous → `UNKNOWN_PENDING_READBACK`; retract only where provider supports | `TestBusinessReviewResponsesPolicyApprovalAmbiguousReadbackAndDisputes` |
| Topic intelligence | Deterministic taxonomy with evidence terms, confidence, engine/model version; idempotent by (review, version, topic, engine, model); operator confirm/correct/reject; originals never modified | unit `TestClassifyIsSourceBackedAndExplainable`, verified intake test |
| Quality loop | Negative signals link once to a live `quality_issues` row; repeat threshold within window (rule-configurable) creates exactly one open `review_action_tasks` per issue/team; links to housekeeping tasks, work orders, recovery or support cases are verified; verification moves the issue to monitoring; monitoring closes automatically | verified intake test (three negative inputs → one HOUSEKEEPING task) |
| Disputes | Media evidence references, provider/Admin route, acknowledgement reference, decision with MFA, appeal; live dispute sets legal hold; review evidence unchanged by decisions | responses/disputes test |
| Reputation analytics | Property and branch views: average, dimensions, count, response rate/time, topic frequency, trend, repeated issues, recovery/cancellation/repeat correlations; conversion null with reason unless sourced funnel data exists; snapshots immutable with digest | verified intake test |

Events: `review.received.v1`, `review.response.updated.v1`, `review.dispute.updated.v1`, `quality.task.updated.v1`, `reputation.snapshot.ready.v1`.

External: Booking.com/Airbnb review and reply partner contracts and a Marketplace reply contract are unavailable; adapters default to `DisabledReviews` (responses fail as `PROVIDER_REPLY_UNSUPPORTED_OR_REJECTED`, imports do not run).
