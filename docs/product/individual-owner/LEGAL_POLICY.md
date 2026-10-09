# Individual Owner — legal timelines and jurisdiction policy

Status: engineering control in place (LOCAL PASS). **No jurisdiction policy has
been legally approved.** Until business and legal approve a version per
jurisdiction, every consequential automatic decision listed below stays disabled
and goes to manual review.

## How policy works

- `jurisdiction_policies` (migration 00015) holds versioned rules per country,
  optionally per region (`state_region` on the property address, upper-cased).
- A version is `DRAFT` → `APPROVED` → `RETIRED`. Approval needs a recorded
  `legal_approval_ref` and a second operator: the drafter cannot approve.
  Approving a version retires the one it supersedes. A database trigger makes
  approved rules immutable; only drafts can be deleted.
- Resolution for a property: approved region version, else approved
  country-wide version, else **none** (`approved=false`).
- Owners see the policy in force at `GET /properties/{property_id}/legal-policy`.
  Operators manage versions under `/internal/v1/owner/admin/jurisdiction-policies`.
- Every decision that applies a policy records `legal_policy`,
  `legal_policy_id` and `legal_policy_version` (or `NONE_APPROVED`) in the audit
  metadata. Earlier records and audit history are left unchanged.

## Inventory of timelines and automatic decisions

| Area | Rule | Without an approved policy | With an approved policy | Source |
|---|---|---|---|---|
| Damage claims (§13.20) | Guest response window | 72 h (operational) | `damage_claim_response_hours` | `RaiseDamageClaim` |
| Damage claims | Acceptance on guest silence | **Removed.** A lapsed claim moves to `RESPONSE_OVERDUE`. An operator decides it with `POST /admin/damage-claims/{id}/resolution`. The guest may still answer, and the owner may withdraw. | Not configurable: silence is never acceptance | `RunShortletJobs` |
| Deposits (§13.15) | Tenant response window for deductions | 14 days (operational) | `deposit_response_days` | `ProposeDepositSettlement` |
| Deposits | Deductions accepted on tenant silence | **Disabled.** The deposit moves to `DISPUTED` with `tenant_response=NO_RESPONSE` and the whole deduction frozen. The undisputed refund still proceeds. The owner can concede, or an operator decides through the existing deposit-dispute routes. | Applied only when `deposit_silence_acceptance=true` | `RunDeposits` |
| Deposits | Settlement deadline after move-out | Agreement value, 1–180 days | Capped by `deposit_release_days_max` before signature | `checkJurisdictionTerms` |
| Late fees (§13.14) | Automatic charge when rent is overdue | **Disabled.** An audit entry `rent.late_fee.manual_review` is recorded. The owner decides with `POST /rent-obligations/{id}/late-fee`, which is within the cap and charged at most once per period. | Charged automatically when `automatic_late_fees=true` and within the cap | `markOverdue`, `AssessLateFee` |
| Late fees | Cap as a share of periodic rent | Platform cap of 1000 bps | `max_late_fee_bps`, applied at drafting and before signature (0 forbids late fees) | `validateTerms`, `checkJurisdictionTerms` |
| Short-let holds | Approval wait / payment hold | 24 h / 30 min (operational) | `shortlet_approval_hours` / `shortlet_payment_hold_minutes` | `CustomerRequestBooking`, `ActOnBooking` |
| Notices (§13.15) | Minimum notice period | Per-country, versioned admin notice templates (`notice_templates.min_notice_days`). `TERMINATION`, `RENT_REVIEW`, `ARREARS`, `BREACH` and `ENTRY` notices need a template. | Same | `checkNoticeTemplate`, `NoticeEffectiveDate` |
| Short-let refunds (§13.20) | Guest-cancellation refund schedule | FLEXIBLE / MODERATE / STRICT schedules disclosed at booking. Owner cancellations are always refunded in full. | Not yet policy-driven | `domain.RefundBps` |

## Needs business or legal approval

1. An approved policy version for each launch jurisdiction (at minimum NG, and
   any states that differ), including whether deposit silence acceptance or
   automatic late fees are lawful, with their windows and caps.
2. The short-let refund schedules. These are contractual terms the guest
   accepts at booking. They are not yet checked against consumer-protection
   law, so they must be legally reviewed. Once they are, they can move into
   `LegalRules`.
3. Default operational timers (72 h claim window, 14-day deposit window,
   short-let holds). These only start manual review or release inventory; they
   never decide money on their own.

## Evidence

Integration tests run against real PostgreSQL 16, in
`services/individual-owner-service/tests/integration/legal_policy_test.go`:

- `TestJurisdictionPolicyAdministration`: covers drafts, the second-approver
  rule, immutability, region precedence, supersession, fallback and audit.
- `TestDamageClaimSilenceGoesToReview`: checks the policy window, that a lapsed
  claim becomes `RESPONSE_OVERDUE` with no award, and that only an operator
  resolves it.
- `TestDepositSilenceNeedsApprovedPolicy`: with no policy, a lapsed deposit goes
  to manual review with the deductions frozen. With an approved policy, silence
  acceptance is applied and audited.
- `TestAutomaticLateFeesNeedApprovedPolicy`: an approved policy charges late
  fees automatically and caps the deposit deadline.
- `TestRentArrearsLateFeesAndWaivers`: with no policy, the owner assesses the
  late fee manually.
