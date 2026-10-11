"""Sections 11–14 additive contracts; existing finance events keep their names."""

def extend(schemas, internal, public, obj, ref, array, text, uuid, date, money, boolean):
    schemas['Provision']['properties']['purpose']={'type':'string','enum':['PERSONAL','EARNINGS']}
    schemas['WalletAccount']=obj(dict(account_id=uuid,pool_id=uuid,kind=text))
    schemas['WalletProfile']=obj(dict(wallet=ref('Wallet'),canonical_party_ref=text,purpose=text,verification_state={'enum':['PENDING','VERIFIED','REVOKED','EXPIRED']},roles=array(text),accounts=array(ref('WalletAccount')),created_at=date))
    schemas['WalletTransferPolicy']=obj(dict(policy_id=uuid,pool_id=uuid,legal_entity=text,currency=text,jurisdiction=text,version=text,source_domains=array(text),max_minor=money,daily_minor=money,local_test_only={'const':True},evidence_ref=text,approvers=array(text),effective_at=date,expires_at=date))
    inp=dict(transfer_id=uuid,source_wallet_id=uuid,destination_wallet_id=uuid,pool_id=uuid,policy_id=uuid,currency=text,amount_minor=money,purpose={'const':'CUSTOMER_REMITTANCE'},consent_ref=text)
    schemas['WalletTransferInput']=obj(inp)
    schemas['WalletTransfer']=obj(dict(**inp,actor_id=uuid,actor_domain=text,source_service=text,source_identity_id=uuid,destination_identity_id=uuid,legal_entity=text,policy_version=text,authorization_decision=text,risk_decision=text,state={'enum':['REQUESTED','POSTED']},journal_id=uuid,created_at=date,posted_at=date),list(inp)+['actor_id','actor_domain','source_service','source_identity_id','destination_identity_id','legal_entity','policy_version','authorization_decision','risk_decision','state','created_at'])
    schemas['CapabilityEvidence']=obj(dict(evidence_id=uuid,capability=text,stage={'enum':['IMPLEMENTATION','LOCAL_TEST','PROVIDER_TEST','ACCOUNT','COMMERCIAL','REGULATORY','SAFEGUARDING','JURISDICTION','STAGING','PRODUCTION']},legal_entity=text,provider_account_id=uuid,jurisdiction=text,version=text,status={'enum':['NOT_RUN','CERTIFIED','APPROVED','BLOCKED','REVOKED']},evidence_ref=text,issuer_ref=text,approvers=array(text),effective_at=date,expires_at=date),['evidence_id','capability','stage','legal_entity','jurisdiction','version','status','evidence_ref','issuer_ref','approvers','effective_at','expires_at'])
    schemas['CapabilityRequest']=obj(dict(capability=text,wallet_id=uuid,provider_account_id=uuid,jurisdiction=text,policy_ref=text))
    schemas['CapabilityDecision']=obj(dict(capability=text,environment=text,allowed=boolean,reason=text,stages={'type':'object','additionalProperties':{'type':'string'}}))
    schemas['Invoice']=obj(dict(invoice_id=uuid,kind={'const':'ACCEPTED_TERMS'},revision=text,agreement_ref=text,agreement_version=text,business_ref=text,payer_ref=text,currency=text,amount_minor=money,policy_id=uuid,components=array(ref('Component'))))
    routes=[
      ('GET','/locked-quotes/{id}/invoice',None,ref('Invoice'),'Immutable accepted-terms invoice with explicit tax/fee components and quote/agreement revisions. No calculated statutory tax, payment proof or invented payee. Beneficiaries come from authorized allocation readback; lawful statutory issuance remains a jurisdiction approval gate.'),
      ('GET','/wallets/{id}/profile',None,ref('WalletProfile'),'Owned wallet purpose, canonical party, verification and ledger references; structural activation is not KYC/custody approval.'),
      ('POST','/wallet-transfers','WalletTransferInput',ref('WalletTransfer'),'Explicit authenticated source owner, Idempotency-Key, independent local-only approved policy and verified parties. Request does not debit/reserve money. Rent/refund/release use their existing authorized workflows.'),
      ('POST','/wallet-transfers/{id}/confirm',None,ref('WalletTransfer'),'Original source actor only. Test-mode pool and live hard-denial; policy/current identity/verification/limits/coverage revalidated. Atomic two-entry journal; duplicates return canonical posting. No external transfer.'),
      ('GET','/wallet-transfers/{id}',None,ref('WalletTransfer'),'Source or destination wallet owner; no cross-user access.'),
      ('GET','/wallets/{id}/transfers',None,array(ref('WalletTransfer')),'Owned outgoing/incoming history, UUIDv7 after cursor and limit 1–100.')]
    internal.extend(routes);public.extend(routes)
    internal.append(('GET','/wallets/me',None,array(ref('Wallet')),'Authenticated source account wallets, including restricted wallets. No account identifier accepted from a body.'))
    internal.extend([
      ('PUT','/wallets/{id}/state','IdentityControl',obj(dict(recorded=boolean)),'Admin only, evidence/policy audit. Restoration requires maker-checker; closed wallets cannot reopen. No ledger balance edits.'),
      ('POST','/wallet-transfer-policies','WalletTransferPolicy',obj(dict(recorded=boolean)),'Admin dual evidence; immutable local/test-only policy. Cannot authorize production stored-value transfers.'),
      ('POST','/financial-capability-evidence','CapabilityEvidence',obj(dict(recorded=boolean)),'Admin maker-checker; immutable independent stage evidence. Recorded claims do not activate production or replace workflow authority.'),
      ('POST','/financial-capabilities/evaluate','CapabilityRequest',ref('CapabilityDecision'),'Owned actor/provider/entity/market eligibility readback; always requires concrete workflow authorization and funds checks. Production hard-denial cannot be overridden by recorded approval.')])

def events(payloads, schemas, obj, ref, uuid, text):
    payloads['finance.wallet.transfer_requested.v1']=ref('WalletTransfer')
    payloads['finance.wallet.transfer_posted.v1']=ref('WalletTransfer')
    payloads['finance.wallet.state_changed.v1']=obj(dict(wallet_id=uuid,state={'enum':['ACTIVE','RESTRICTED','CLOSED']},evidence_ref=text,policy_version=text))
