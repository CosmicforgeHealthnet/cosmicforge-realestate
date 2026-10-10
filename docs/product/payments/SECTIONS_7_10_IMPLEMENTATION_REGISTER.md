# Sections 7–10 implementation register

Authorization: attached Sections 7–10 prompt, 10 October 2026. Baseline: `8dfb00a36793392a6ac24073da083e0f4d8508f7`. Branch: `codex/payment-sections-7-10`.

The master blueprint hash is `fd1cfd14fb2f78ed7601fbafc49113f7d03049d3ef8041f6f08f0342d964e7f1`. Its later sections are reference material, not authorization to expand this task.

Implementation extends the existing Financial Core: immutable remedy policies and evidence references; deposit/claim/dispute records with amount-specific freezes; durable refund reservations and original-attempt read-back; compensating reversals/recoverables; destination changes with cooling-off; customer funding and atomic accepted-obligation spend; reconciliation, state history and operational controls.

Section 9.2 reserves design contracts only. No recurring debit, mandate execution, automatic retry, or scheduler is enabled. Customer cash-out and prefunding are disabled. Provider sandbox, real DVA eligibility, custody, staging and production certification require external evidence. Local HTTP simulations do not satisfy those gates.

Forward migrations preserve retained financial history. Policies without configured jurisdiction deadlines fail closed. Accounting, state, audit and financial events commit together. Ambiguous external outcomes retain their reservations and original references.
