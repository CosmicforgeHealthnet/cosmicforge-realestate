-- name: ListPayoutReferences :many
SELECT payout_account_id,wallet_id,beneficiary_ref,verification_ref,jurisdiction,expires_at
FROM payout_account_refs WHERE wallet_id=$1 ORDER BY payout_account_id;
