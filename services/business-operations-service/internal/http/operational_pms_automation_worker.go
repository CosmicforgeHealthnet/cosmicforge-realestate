package businesshttp

import (
	"context"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

// ProcessPMSDayAutomation runs on the existing maintenance cadence. A policy
// records bounded human authorization, not a human worker principal or session.
// Organisation locks and the rechecked cursor fence concurrent worker replicas.
func (h *Handler) ProcessPMSDayAutomation(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT p.organisation_id,p.property_id,p.id FROM hospitality_day_automation_policies p LEFT JOIN hospitality_day_automation_cursors c ON c.organisation_id=p.organisation_id AND c.property_id=p.property_id WHERE p.mode<>'MANUAL' AND p.effective_at<=$2 AND p.expires_at>$2 AND NOT EXISTS(SELECT 1 FROM hospitality_day_automation_policies later WHERE later.organisation_id=p.organisation_id AND later.property_id=p.property_id AND later.version>p.version) AND NOT EXISTS(SELECT 1 FROM hospitality_day_automation_revocations r WHERE r.organisation_id=p.organisation_id AND r.policy_id=p.id) AND coalesce(c.state,'READY')='READY' AND (c.next_check_at IS NULL OR c.next_check_at<=$2) ORDER BY coalesce(c.next_check_at,p.created_at),p.id LIMIT $1`, limit, now)
	if e != nil {
		return e
	}
	type ref struct{ org, property, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.property, &v.id); e != nil {
			break
		}
		refs = append(refs, v)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	var failures []error
	for _, v := range refs {
		var operationErr error
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, v.org); e != nil {
				return e
			}
			now := h.Auth.Service.Now().UTC()
			var due bool
			if e := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM hospitality_day_automation_cursors WHERE organisation_id=$1 AND property_id=$2 AND (next_check_at>$3 OR state='QUARANTINED'))`, v.org, v.property, now).Scan(&due); e != nil {
				return e
			}
			if !due {
				return nil
			}
			p, e := loadDayAutomation(ctx, tx, v.org, v.property)
			if e != nil {
				return e
			}
			var revoked bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hospitality_day_automation_revocations WHERE organisation_id=$1 AND policy_id=$2)`, v.org, p.ID).Scan(&revoked); e != nil {
				return e
			}
			if p.ID != v.id || p.Mode == "MANUAL" || revoked || now.Before(p.EffectiveAt) || !now.Before(p.ExpiresAt) {
				return nil
			}
			if _, e = tx.Exec(ctx, `INSERT INTO hospitality_day_automation_cursors(organisation_id,property_id,next_check_at) VALUES($1,$2,$3) ON CONFLICT(organisation_id,property_id) DO UPDATE SET next_check_at=excluded.next_check_at`, v.org, v.property, now.Add(time.Minute)); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			t := tenant{Organisation: v.org}
			if _, e = tx.Exec(ctx, `SAVEPOINT pms_day_automation`); e != nil {
				return e
			}
			operationErr = func() error {
				allowed, e := h.currentRecordedPMSAuthority(ctx, tx, v.org, v.property, p.AuthorizingMembershipID, p.AuthorizingRoleID, "night_audit:write")
				if e != nil {
					return e
				}
				var current int64
				var zone, state string
				if e = tx.QueryRow(ctx, `SELECT n.timezone,n.state,coalesce((SELECT max(version) FROM hospitality_policies hp WHERE hp.organisation_id=n.organisation_id AND hp.property_id=n.id),0) FROM inventory_nodes n WHERE n.organisation_id=$1 AND n.id=$2 AND n.kind='PROPERTY'`, v.org, v.property).Scan(&zone, &state, &current); e != nil {
					return e
				}
				stopped := ""
				if current != p.HospitalityPolicyVersion || zone != p.Timezone || state != "ACTIVE" {
					stopped = "POLICY_STALE"
				} else if !allowed {
					stopped = "AUTHORITY_REVOKED"
				}
				if stopped != "" {
					var day ids.ID
					e = tx.QueryRow(ctx, `SELECT id FROM hospitality_business_days WHERE organisation_id=$1 AND property_id=$2 AND state='OPEN'`, v.org, v.property).Scan(&day)
					if errors.Is(e, pgx.ErrNoRows) {
						return nil
					}
					if e != nil {
						return e
					}
					_, e = h.recordPMSDayAutomationRun(ctx, tx, t, p, day, security.SHA256Hex(marshalCommercial(map[string]any{"policy_id": p.ID, "outcome": stopped, "current_cutoff_version": current, "timezone": zone, "property_state": state})), stopped, ids.Nil, ids.Nil, map[string]any{"reason": "Current recorded authority or captured property policy no longer permits unattended execution"})
					return e
				}
				for range p.MaxDaysPerRun {
					if !h.Auth.Service.Now().Before(p.ExpiresAt) {
						return nil
					}
					d, e := h.pmsDay(ctx, tx, t, v.property, false)
					if e != nil {
						return e
					}
					if d.PolicyVersion != p.HospitalityPolicyVersion || d.Timezone != p.Timezone {
						_, e = h.recordPMSDayAutomationRun(ctx, tx, t, p, d.ID, security.SHA256Hex(marshalCommercial(d)), "POLICY_STALE", ids.Nil, ids.Nil, map[string]any{"reason": "The existing open business day captured a different cutoff policy or timezone; manual review is required"})
						return e
					}
					if h.Auth.Service.Now().Before(d.CutoffAt) {
						return nil
					}
					snapshot, e := h.pmsAuditSnapshot(ctx, tx, t, d)
					if e != nil {
						return e
					}
					digest := security.SHA256Hex(marshalCommercial(snapshot))
					outcome := "AUDITED"
					if len(snapshot.Exceptions) > 0 {
						outcome = "BLOCKED"
					} else if p.Mode == "CLOSE_IF_CLEAR" {
						outcome = "CLOSED"
					}
					var exists bool
					if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hospitality_day_automation_runs WHERE policy_id=$1 AND business_day_id=$2 AND source_digest=$3 AND state=$4)`, p.ID, d.ID, digest, outcome).Scan(&exists); e != nil {
						return e
					}
					if exists {
						return nil
					}
					audit, e := h.validatePMSNightAudit(ctx, tx, t, v.property, "Execute independently approved bounded automatic property night audit")
					if e != nil {
						return e
					}
					closeID := ids.Nil
					if outcome == "CLOSED" {
						closed, e := h.closePMSBusinessDay(ctx, tx, t, PMSDayCloseCommand{PropertyID: v.property, BusinessDayID: d.ID, Version: d.Version, AuditID: audit.ID, SnapshotDigest: audit.SnapshotDigest, Reason: "Execute independently approved automatic close with zero unresolved exceptions"})
						if e != nil {
							return e
						}
						closeID = closed.ID
					}
					if _, e = h.recordPMSDayAutomationRun(ctx, tx, t, p, d.ID, digest, outcome, audit.ID, closeID, map[string]any{"mode": p.Mode, "policy_version": p.Version, "hospitality_policy_version": d.PolicyVersion, "snapshot_digest": audit.SnapshotDigest, "exceptions": snapshot.Exceptions, "authorizing_membership_id": p.AuthorizingMembershipID, "approval_id": p.ApprovalID}); e != nil {
						return e
					}
					if outcome != "CLOSED" {
						return nil
					}
				}
				return nil
			}()
			if operationErr == nil {
				if _, e = tx.Exec(ctx, `RELEASE SAVEPOINT pms_day_automation`); e != nil {
					return e
				}
				_, e = tx.Exec(ctx, `UPDATE hospitality_day_automation_cursors SET consecutive_failures=0,last_error_code='' WHERE organisation_id=$1 AND property_id=$2`, v.org, v.property)
				return e
			}
			if _, e = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT pms_day_automation`); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `RELEASE SAVEPOINT pms_day_automation`); e != nil {
				return e
			}
			var attempts int
			if e = tx.QueryRow(ctx, `UPDATE hospitality_day_automation_cursors SET consecutive_failures=consecutive_failures+1,last_error_code='SOURCE_TRANSACTION_FAILED',state=CASE WHEN consecutive_failures+1>=12 THEN 'QUARANTINED' ELSE 'READY' END,next_check_at=$3 WHERE organisation_id=$1 AND property_id=$2 RETURNING consecutive_failures`, v.org, v.property, now.Add(5*time.Minute)).Scan(&attempts); e != nil {
				return e
			}
			_, e = h.recordPMSDayAutomationRun(ctx, tx, t, p, ids.Nil, security.SHA256Hex([]byte(p.ID.String()+":SOURCE_TRANSACTION_FAILED")), "SOURCE_REVIEW_REQUIRED", ids.Nil, ids.Nil, map[string]any{"error_code": "SOURCE_TRANSACTION_FAILED", "initial_attempt": attempts, "reason": "Owning source operation rolled back; bounded retry and operator review required, no closure committed"})
			return e
		})
		if e != nil {
			failures = append(failures, e)
		} else if operationErr != nil {
			failures = append(failures, operationErr)
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) recordPMSDayAutomationRun(ctx context.Context, tx pgx.Tx, t tenant, p PMSDayAutomationPolicy, day ids.ID, digest, state string, audit, close ids.ID, evidence any) (ids.ID, error) {
	id := ids.New()
	tag, e := tx.Exec(ctx, `INSERT INTO hospitality_day_automation_runs(id,organisation_id,property_id,policy_id,business_day_id,source_digest,state,audit_id,close_id,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(policy_id,business_day_id,source_digest,state) DO NOTHING`, id, t.Organisation, p.PropertyID, p.ID, day, digest, state, audit, close, marshalCommercial(evidence))
	if e != nil || tag.RowsAffected() == 0 {
		return ids.Nil, e
	}
	e = h.pmsCommit(ctx, tx, t, p.PropertyID, id, "hospitality_day_automation_run", "DAY_AUTOMATION_EXECUTED", "hospitality.day.automation.executed.v1", 1, map[string]any{"policy_id": p.ID, "business_day_id": day, "source_digest": digest, "state": state, "audit_id": audit, "close_id": close, "evidence": evidence, "actor_service": "business-operations-service"})
	return id, e
}
