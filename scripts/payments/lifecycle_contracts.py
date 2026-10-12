"""Sections 4–6 contracts use schemas reflected from the actual Go DTOs."""
import json
import subprocess

def extend(root, schemas, routes, obj, ref, array, text, uuid):
    data = subprocess.check_output(["go", "run", "./cmd/contracts"], cwd=root / "services/payment-service", text=True)
    dto_schemas = json.loads(data)
    dto_schemas["ReleaseEvidence"] = dto_schemas.pop("Evidence")
    schemas.update(dto_schemas)
    recorded=obj(dict(recorded={"type":"boolean"}))
    definitions=[
        ("GET","/financial-policy-families",None,array(ref("Family")),"All 18 disabled blueprint templates; no tariff or legal deadline is supplied."),
        ("POST","/financial-policies","Policy",ref("Policy"),"Admin only; immutable approved jurisdiction/contract/product/effective version. Deactivated versions remain historical."),
        ("POST","/financial-policies/resolve","PolicySelector",ref("Policy"),"Signed caller; resolve an approved effective policy for exact jurisdiction, currency, contract and product."),
        ("POST","/financial-policy-deactivations","PolicyDeactivation",recorded,"Admin only; append an evidenced effective deactivation without editing accepted policy history."),
        ("GET","/financial-policies/{id}",None,ref("Policy"),"Signed caller; historical policy version."),
        ("POST","/beneficiary-authorities","Authority",ref("Authority"),"Authoritative domain; ownership percentages verified by owner/business; manager fee mandates issued by owner/business only; platform requires Admin."),
        ("GET","/beneficiary-authorities/{id}",None,ref("AuthorityStatus"),"Original issuer, Admin/Core or the authenticated service owning a locked quote for the exact business reference; read-only existing entitlement and expiry/revocation state."),
        ("POST","/beneficiary-authorities/{id}/revoke","Evidence",recorded,"Original issuer or Admin; irreversible evidenced revocation."),
        ("POST","/locked-quotes","Quote",ref("Quote"),"Original business domain; immutable full accepted quote, payer consent and components. Idempotency-Key required."),
        ("GET","/locked-quotes/{id}",None,ref("Quote"),"Original source or delegated payer only."),
        ("GET","/locked-quotes/{id}/receipt",None,ref("Receipt"),"Original source or payer; component classification and current financial facts. Deposits remain separate."),
        ("POST","/allocations","AllocationInput",ref("Allocation"),"Original canonical quote source only; exact reconciliation, verified current beneficiary authority, immutable version. Unreleased corrections compensate the previous journal."),
        ("GET","/allocations/{id}",None,ref("Allocation"),"Original source or owned beneficiary lines only; unrelated allocations are filtered."),
        ("POST","/allocation-lines/{id}/hold","Hold",recorded,"Original source or Admin; independent share hold. Clearing requires two distinct approvers. Idempotency-Key required."),
        ("POST","/release-evidence","ReleaseEvidence",recorded,"Policy-permitted authenticated evidence authority; bound to allocation/condition/version and expiry. HTTP source cannot be supplied by user data."),
        ("POST","/allocation-releases","Release",obj(dict(journal_id=uuid)),"Original source or Admin; required evidence, actual matched funding, restrictions and custody requirements; capped atomic partial release. Idempotency-Key required."),
        ("POST","/transaction-funding","FundingInput",recorded,"Admin with two finance approvers; provider settlement membership and liquidity readback plus reconciled cash attestation. Test mode only; merchant balance is not lawful custody proof."),
        ("POST","/payout-destination-bindings","Destination",recorded,"Verification only; bank owner and payout reference attested, recipient readback validates provider/merchant/mode/currency. Immutable binding."),
        ("POST","/withdrawal-approvals","WithdrawalApproval",recorded,"Admin only; exact beneficiary/amount/destination, authoritative step-up and risk references, threshold approvals, expiry; single-use."),
        ("POST","/withdrawals","Withdrawal",ref("Payout"),"Delegated beneficiary domain only; AVAILABLE-only, atomic reservation, approved test payout capability, one original-provider attempt. Idempotency-Key required."),
        ("GET","/withdrawals/{id}",None,ref("Payout"),"Delegated wallet owner or Admin; unconfirmed external results remain reserved."),
        ("POST","/withdrawals/{id}/resolve",None,ref("Payout"),"Delegated owner or Admin; original-reference provider readback. Unknown never releases money or resubmits; definitive no-effect failure compensates safely."),
        ("POST","/rent-agreements","Agreement",ref("Agreement"),"Owner/business authority only; immutable accepted agreement/version, fixed or continuing term, separately contracted owner commission/manager fee and components."),
        ("POST","/rent-obligations","RentRequest",ref("Obligation"),"Owner/business authority; manually commanded exact period/due date; unique non-overlapping obligation, eligible recurrence only, separate deposit stream. No automatic charge."),
        ("GET","/rent-obligations/{id}",None,ref("Obligation"),"Original source or payer; immutable terms plus current payment status."),
        ("POST","/rent-obligations/{id}/allocate","RentAllocate",ref("Allocation"),"Original source; configured owner-side commission and mandate-specific fee, exact co-owner arithmetic, deposit separated."),
    ]
    schemas["RentAllocate"]=obj(dict(intent_id=uuid))
    routes.extend(definitions)
    schemas["IntentInput"]["properties"]["locked_quote_id"]=uuid
    return [r for r in definitions if r[0]=="GET" and r[1] in ["/allocations/{id}","/withdrawals/{id}","/rent-obligations/{id}","/locked-quotes/{id}/receipt"]]
