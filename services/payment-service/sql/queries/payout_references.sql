-- name: ListPayoutReferences :many
SELECT r.payout_account_id,r.wallet_id,r.beneficiary_ref,r.verification_ref,r.jurisdiction,r.expires_at,
 (r.expires_at>now() AND EXISTS(SELECT 1 FROM wallets w JOIN financial_identities i USING(identity_id)
 JOIN verification_refs v USING(identity_id) WHERE w.wallet_id=r.wallet_id AND i.state='ACTIVE'
 AND v.verification_ref=r.verification_ref AND v.state='VERIFIED' AND v.expires_at>now()))::boolean AS eligible
FROM payout_account_refs r WHERE r.wallet_id=$1 ORDER BY r.payout_account_id;
