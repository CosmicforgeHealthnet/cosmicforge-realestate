# Business Operations Section 15 — Enterprise Finance Integration

Migration `00035_finance_controls.sql`. Implementation: `internal/http/operational_finance.go`, `internal/finance/allocation.go`, Payment readback in `internal/integrations/payment/client.go` (`BeneficiaryAuthority` over the documented `GET /internal/v1/beneficiary-authorities/{id}`).

Business Operations never executes money movement. All verified amounts come from Payment Service readback or authoritative signed financial inputs.

| Requirement | Implementation | Evidence |
|---|---|---|
| Cost centres, fiscal periods, budgets, lines | Hierarchical cost centres; non-overlapping fiscal periods (trigger); versioned budgets whose total must equal lines; activation needs exact `COMMERCIAL_BUDGET_ACTIVATION` approval (amount bound) and MFA; one active budget per cost centre/period | `TestBusinessFinanceBudgetControlIndependentApprovalAndObligations` |
| Spend approval | Expense requests checked against remaining line amount; above the line threshold an exact independent `COMMERCIAL_EXPENSE_APPROVAL` (amount, currency, line, counterparty) is consumed; self-approval denied; approved terms immutable (trigger); 30-day expiry worker | same test |
| Obligations | Receivable/payable obligations with coding, source identity uniqueness, exact minor units; approval bound to amount; immutable adjustments with deferred conservation trigger (amount = original + adjustments) | same test, `TestBusinessFinanceObligationReadbackUnknownSettledAndReversal` |
| Payment boundary | Obligation binds a Payment Service reference; readback via Payment `Intent`: lost response → `UNKNOWN_PENDING_READBACK` + `PAYMENT_UNKNOWN` exception; pending → no settlement; SETTLED with exact amount/currency → settled; reversal → DISPUTED + exception | readback test |
| Payout destinations | References to Payment beneficiary authorities; ACTIVE only after Payment readback (active, unexpired, currency, domain) | `TestBusinessFinancePayoutDestinationMFAApprovalCoolingOffAndSplitConservation` |
| Payout change | MFA + Annex F `BUSINESS_PAYOUT_DESTINATION_CHANGE` approval; destination must be Payment-verified; 24-hour cooling-off; worker re-verifies with Payment before EFFECTIVE (else BLOCKED); event drives security notification | same test |
| Revenue splits | Conserving shares (percentages total 10 000 bps; fixed first; largest-remainder rounding); immutable terms; activation requires Payment-verified destinations and exact approval; preview proves conservation; execution remains Payment Service | unit `TestAllocateConservesEveryMinorUnit`, payout test |
| Channel settlement expectations | Conserved expected net (CHECK); reconciliation worker compares with authoritative `channel_financial_inputs`: MATCHED, MISMATCH (+exception), duplicate source (+exception), LATE (+exception); expected amounts never become settled revenue | `TestBusinessFinanceChannelRemittanceReconciliationAndCloseBlocking` |
| Financial close | Period close blocked while exceptions remain open; resolution recorded | reconciliation test |
| Reporting | Expected vs verified vs outstanding vs unknown per currency; channel profitability from expectations and verified inputs; budget commitments and settled actuals | reconciliation test |

External: full collection/refund/payout/settlement execution and live certification require the Payment Service environment and providers. No direct provider integration exists.
