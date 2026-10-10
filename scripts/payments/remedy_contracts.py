"""Sections 7–10 implemented routes; schemas come from Go DTO reflection."""
def extend(schemas,routes,obj,ref,array,text):
    recorded=obj(dict(recorded={"type":"boolean"}))
    schemas["RemedyReference"]=obj(dict(authority_ref=text,approvers=array(text)))
    schemas["ReadbackReplay"]=obj(dict(kind={"enum":["COLLECTION","PAYOUT","REFUND"]},authority_ref=text))
    defs=[
      ("POST","/readback-jobs/{id}/replay","ReadbackReplay",recorded,"Admin-authorized dead-letter replay retains the original provider/reference and immutable retry history. Never submits money."),
      ("POST","/recoverable-actions","RecoverableAction",recorded,"Admin dual authority/current evidence. Assignment binds original beneficiary; recovery uses only approved internal reserve or liable-party retained proceeds. Original debt and history remain immutable. Idempotency-Key required."),
      ("POST","/financial-case-overrides","CaseOverride",recorded,"Privileged Admin dual approval, reason/evidence and policy-bounded expiry; review authority only, no ledger or closed-case rewrite."),
      ("POST","/reconciliation-resolutions","ReconciliationResolution",recorded,"Admin dual authority; follow-up reconciliation must clear the exact finding. Append resolution without deleting original exceptions or correcting money."),
      ("POST","/intent-cancellations","Cancellation",ref("Intent"),"Original canonical quote source or Admin plus current cancellation policy/evidence. Only unsubmitted REQUESTED intents may become CANCELLED. Collected money requires approved case/refund. Idempotency-Key required."),
      ("POST","/remedy-policies","RemedyPolicy",recorded,"Admin-approved immutable jurisdiction deadlines, remedy strategies, cooling and customer funding/spend controls."),
      ("POST","/remedy-evidence","RemedyEvidence",recorded,"Authenticated business evidence source; immutable version/hash/actor/time and expiry. No primary inspection document is stored."),
      ("POST","/remedy-evidence/{id}/revoke","RemedyReference",recorded,"Original evidence source or Admin; irreversible audited revocation."),
      ("POST","/refundable-deposits","Deposit",ref("Deposit"),"Original transaction source; canonical refundable liability and inspection references. Generated financial context overrides body projections."),
      ("GET","/refundable-deposits/{id}",None,ref("Deposit"),"Authorized transaction parties only; current protected, contested, refundable and refund facts."),
      ("POST","/financial-cases","CaseRequest",ref("FinancialCase"),"Authorized parties; deposit claims require source authority. Freeze only exact contested amount. Idempotency-Key required."),
      ("GET","/financial-cases/{id}",None,ref("FinancialCase"),"Authorized case parties and Admin only; retained case state."),
      ("GET","/financial-cases/{id}/facts",None,array(ref("CaseHistoryFact")),"Authorized case parties; immutable proposed claims, challenges, reviews and decisions."),
      ("POST","/financial-case-facts","CaseFact",recorded,"Challenges require payer ownership; reviews/notices require Admin. Current authoritative evidence and policy deadlines. Idempotency-Key required."),
      ("POST","/financial-case-decisions","CaseDecision",recorded,"Admin only; independent recorded review, separation of duties, dual approval, current evidence. Awards remain protected; returns remain reserved for refund. Idempotency-Key required."),
      ("POST","/refunds","RefundRequest",ref("RefundAttempt"),"Admin only; approved case decision, payer/rail policy, cumulative confirmed plus reserved cap and original payment serialization. Unsupported rails fail closed. Idempotency-Key required."),
      ("GET","/refunds/{id}",None,ref("RefundAttempt"),"Authorized parties or Payment; immutable original reference and attempt, projected later reversal."),
      ("POST","/refunds/{id}/submit",None,ref("RefundAttempt"),"Admin only; one durable submission CAS. Submission success is not financial completion. Production disabled."),
      ("POST","/refunds/{id}/resolve",None,ref("RefundAttempt"),"Admin/Payment only; original-attempt readback, context validation and exactly one terminal journal. Unknown retains reservation."),
      ("POST","/refunds/{id}/cancel","RemedyReference",ref("RefundAttempt"),"Admin only; only a still-unsubmitted RESERVED attempt may be cancelled."),
      ("POST","/collection-reversals","ReversalRequest",ref("ReversalRecord"),"Admin/Payment plus authoritative original-provider reversal readback. Compensating journal, explicit recoverable/payable, no negative fake cash."),
      ("POST","/payout-destination-versions","DestinationChange",ref("DestinationChange"),"Verification only; owned bank/current KYC, step-up, risk and notification references, cooling, predecessor and high-risk dual approval."),
      ("POST","/payout-destination-versions/{id}/revoke","RemedyReference",recorded,"Verification/Admin; append revocation without rewriting bank history."),
      ("POST","/customer-topups","TopupRequest",ref("Topup"),"Owned marketplace CUSTOMER only; explicit funding policy, consent, expiry and original PSP or mapped DVA receipt. Test mode. Idempotency-Key required."),
      ("GET","/customer-topups/{id}",None,ref("Topup"),"Owned customer only; settlement pending versus once-funded availability."),
      ("POST","/wallet-spends","WalletSpend",ref("Allocation"),"Owned CUSTOMER; exact accepted quote/consent/currency/current available pool. Debit, protected allocation, canonical internal funding, audit and events commit atomically. Idempotency-Key required."),
      ("GET","/wallets/{id}/availability/{pool}",None,ref("Availability"),"Wallet owner; available, withdrawable now, protected, settlement/payout pending, rail unavailable and restricted are distinct. Customer cash-out disabled."),
      ("GET","/recurring-design",None,ref("RecurringDesign"),"DESIGN RESERVED / EXECUTION DEFERRED. No charging endpoint, provider mandate execution or recurring worker exists."),
      ("POST","/customer-topup-funding","TopupFunding",recorded,"Admin with dual cash attestation; original settlement membership, KYC, provider context, fee policy and coverage. One economic customer credit."),
      ("POST","/unmatched-dva-credits/{id}/resolve","RemedyReference",recorded,"Admin dual review; only normal authoritative DVA mapping may resolve. No administrator-selected wallet or sender-name inference."),
      ("POST","/reconciliation-runs","ReconciliationRequest",ref("ReconciliationRun"),"Admin manual/configurable job; ledger invariants, provider original-reference readbacks and preserved exceptions. No silent correction."),
      ("GET","/financial-metrics",None,{"type":"object","additionalProperties":{"type":"string"}},"Admin operational financial metrics; no identity/card/bank/secrets in labels."),
    ]
    routes.extend(defs)
    public_paths={"/customer-topups","/customer-topups/{id}","/wallet-spends","/wallets/{id}/availability/{pool}","/recurring-design","/financial-cases","/financial-cases/{id}","/financial-cases/{id}/facts","/financial-case-facts","/refunds/{id}","/refundable-deposits/{id}"}
    public=[v for v in defs if v[1] in public_paths]
    public.append(("POST","/withdrawals","Withdrawal",ref("Payout"),"Owned beneficiary through signed gateway delegation. Current canonical bank/cooling, KYC, authority and approval required; no customer cash-out. Idempotency-Key required."))
    return public
