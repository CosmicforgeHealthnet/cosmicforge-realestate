package workers

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Lifecycle struct {
	Pool   *pgxpool.Pool
	Outbox *outbox.Store
	Now    func() time.Time
}
type expiryTask struct{ query, action, event, aggregate string }

var expiryTasks = []expiryTask{
	{`WITH due AS (SELECT id FROM staff_memberships WHERE status='ACTIVE' AND expires_at<=$1 ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE staff_memberships m SET status='TERMINATED',version=version+1,updated_at=$1 FROM due WHERE m.id=due.id RETURNING m.id,m.organisation_id`, "STAFF_MEMBERSHIP_EXPIRED", "business.staff.updated.v1", "staff_membership"},
	{`WITH due AS (SELECT id FROM staff_scope_grants WHERE revoked_at IS NULL AND expires_at<=$1 ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE staff_scope_grants g SET revoked_at=$1 FROM due WHERE g.id=due.id RETURNING g.id,g.organisation_id`, "STAFF_SCOPE_EXPIRED", "business.staff.updated.v1", "staff_scope_grant"},
	{`WITH due AS (SELECT id FROM staff_invitations WHERE status='PENDING' AND expires_at<=$1 ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE staff_invitations i SET status='EXPIRED',encrypted_delivery=''::bytea FROM due WHERE i.id=due.id RETURNING i.id,i.organisation_id`, "STAFF_INVITATION_EXPIRED", "business.staff.invitation_expired.v1", "staff_invitation"},
	{`WITH due AS (SELECT id FROM delegation_grants WHERE revoked_at IS NULL AND expires_at<=$1 ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE delegation_grants g SET revoked_at=$1 FROM due WHERE g.id=due.id RETURNING g.id,g.organisation_id`, "DELEGATION_EXPIRED", "business.delegation.expired.v1", "delegation"},
	{`WITH due AS (SELECT id FROM break_glass_grants WHERE revoked_at IS NULL AND expires_at<=$1 ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE break_glass_grants g SET revoked_at=$1 FROM due WHERE g.id=due.id RETURNING g.id,g.organisation_id`, "BREAK_GLASS_EXPIRED", "business.security.break_glass_expired.v1", "break_glass"},
	{`WITH due AS (SELECT id FROM approval_requests WHERE state IN ('PENDING','DRAFT') AND deadline<=$1 ORDER BY deadline,id LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE approval_requests a SET state='EXPIRED',version=version+1,updated_at=$1 FROM due WHERE a.id=due.id RETURNING a.id,a.organisation_id`, "APPROVAL_EXPIRED", "business.approval.expired.v1", "approval_request"},
	{`WITH due AS (SELECT id FROM organisation_capacities WHERE status IN ('ACTIVE','PENDING','DECLARED') AND expires_at IS NOT NULL AND expires_at<=$1 ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE organisation_capacities c SET status='EXPIRED',version=version+1,updated_at=$1 FROM due WHERE c.id=due.id RETURNING c.id,c.organisation_id`, "CAPACITY_EXPIRED", "business.capacity.changed.v1", "capacity"},
	{`WITH due AS (SELECT o.id FROM organisations o WHERE o.state='ACTIVE' AND EXISTS(SELECT 1 FROM verification_links v WHERE v.organisation_id=o.id AND v.purpose='KYB' AND v.expires_at IS NOT NULL AND v.expires_at<=$1) ORDER BY o.id LIMIT 100 FOR UPDATE OF o SKIP LOCKED) UPDATE organisations o SET state='RESTRICTED',restriction_source='KYB',version=version+1,updated_at=$1 FROM due WHERE o.id=due.id RETURNING o.id,o.id`, "KYB_REFERENCE_EXPIRED", "business.restricted.v1", "organisation"},
}

// Tick runs inside the existing worker process maintenance loop. Access checks
// also evaluate expiry synchronously, independently of maintenance availability.
func (w *Lifecycle) Tick(ctx context.Context) error {
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	for _, task := range expiryTasks {
		err := database.WithTx(ctx, w.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, task.query, now)
			if err != nil {
				return err
			}
			type expiredRow struct{ id, org ids.ID }
			expired := []expiredRow{}
			for rows.Next() {
				var row expiredRow
				if err := rows.Scan(&row.id, &row.org); err != nil {
					rows.Close()
					return err
				}
				expired = append(expired, row)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, row := range expired {
				if task.aggregate == "staff_membership" {
					if _, err := tx.Exec(ctx, `INSERT INTO staff_membership_history(id,organisation_id,membership_id,from_status,to_status,reason) VALUES($1,$2,$3,'ACTIVE','TERMINATED','MEMBERSHIP_EXPIRED')`, ids.New(), row.org, row.id); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `UPDATE business_sessions SET revoked_at=coalesce(revoked_at,$3),revoke_reason='MEMBERSHIP_EXPIRED' WHERE organisation_id=$1 AND account_id=(SELECT account_id FROM staff_memberships WHERE organisation_id=$1 AND id=$2)`, row.org, row.id, now); err != nil {
						return err
					}
				}

				change, _ := json.Marshal(map[string]any{"expired_at": now, "reason": task.action})
				if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,organisation_id,action,target_type,target_id,correlation_id,change_context,occurred_at) VALUES($1,$2,$3,$4,$5,'business-lifecycle-worker',$6,$7)`, ids.New(), row.org, task.action, task.aggregate, row.id, change, now); err != nil {
					return err
				}
				payload := map[string]any{"organisation_id": row.org, "target_id": row.id, "reason": task.action, "expired_at": now}
				if task.aggregate == "capacity" {
					var code, status string
					if err := tx.QueryRow(ctx, `SELECT code,status FROM organisation_capacities WHERE organisation_id=$1 AND id=$2`, row.org, row.id).Scan(&code, &status); err != nil {
						return err
					}
					payload["capacity_id"] = row.id
					payload["code"] = code
					payload["status"] = status
					payload["expires_at"] = now
				}
				if task.event == "business.staff.updated.v1" {
					member := row.id
					if task.aggregate == "staff_scope_grant" {
						if err := tx.QueryRow(ctx, `SELECT membership_id FROM staff_scope_grants WHERE organisation_id=$1 AND id=$2`, row.org, row.id).Scan(&member); err != nil {
							return err
						}
					}
					var account ids.ID
					var status string
					var version int64
					if err := tx.QueryRow(ctx, `SELECT account_id,status,version FROM staff_memberships WHERE organisation_id=$1 AND id=$2`, row.org, member).Scan(&account, &status, &version); err != nil {
						return err
					}
					payload["staff_id"] = member
					payload["account_id"] = account
					payload["status"] = status
					payload["version"] = version
				}
				if task.aggregate == "organisation" {
					payload["state"] = "RESTRICTED"
				}
				if task.aggregate == "approval_request" {
					payload["approval_id"] = row.id
					payload["state"] = "EXPIRED"
					var action string
					var policy ids.ID
					var version int64
					if err := tx.QueryRow(ctx, `SELECT action,policy_id,(policy_snapshot->>'version')::bigint FROM approval_requests WHERE organisation_id=$1 AND id=$2`, row.org, row.id).Scan(&action, &policy, &version); err != nil {
						return err
					}
					payload["action"] = action
					payload["policy_id"] = policy
					payload["policy_version"] = version
					completed, err := events.New(ctx, platform.BusinessOperationsService, "business.approval.completed.v1", task.aggregate, row.id, payload)
					if err != nil {
						return err
					}
					if err = w.Outbox.Add(ctx, tx, completed); err != nil {
						return err
					}
				}
				event, err := events.New(ctx, platform.BusinessOperationsService, task.event, task.aggregate, row.id, payload)
				if err != nil {
					return err
				}
				if err := w.Outbox.Add(ctx, tx, event); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return database.WithTx(ctx, w.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		for _, query := range []string{
			`DELETE FROM business_mfa_one_time_codes WHERE expires_at<$1::timestamptz-interval '90 days'`,
			`UPDATE business_sessions SET revoked_at=$1,revoke_reason='EXPIRED' WHERE revoked_at IS NULL AND expires_at<=$1`,
			`UPDATE business_mfa_methods SET status='REMOVED',encrypted_secret=''::bytea,removed_at=$1 WHERE status='PENDING' AND expires_at<=$1`,
			`DELETE FROM business_mfa_challenges WHERE expires_at<$1::timestamptz-interval '90 days'`,
			`DELETE FROM business_refresh_tokens t USING business_sessions s WHERE t.session_id=s.id AND s.expires_at<$1::timestamptz-interval '90 days'`,
			`DELETE FROM business_sessions WHERE expires_at<$1::timestamptz-interval '90 days'`,
			`DELETE FROM business_auth_limits WHERE window_start<$1::timestamptz-interval '2 days'`,
			`DELETE FROM idempotency_records WHERE expires_at<$1 AND status<>'IN_PROGRESS'`,
		} {
			if _, err := tx.Exec(ctx, query, now); err != nil {
				return err
			}
		}
		return nil
	})
}
