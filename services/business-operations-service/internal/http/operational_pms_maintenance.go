package businesshttp

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

// ProcessHospitalityMaintenance uses the existing worker cadence. Each owning
// transaction locks its organisation, rechecks facts and commits history/outbox
// atomically. It never initiates collection or fabricates a human principal.
func (h *Handler) ProcessHospitalityMaintenance(ctx context.Context, limit int) error {
	if e := h.ProcessFolioFiscalMaintenance(ctx, limit); e != nil {
		return e
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT f.organisation_id,f.id FROM folios f JOIN stay_records s ON s.organisation_id=f.organisation_id AND s.id=f.stay_id LEFT JOIN hospitality_maintenance_cursors c ON c.organisation_id=f.organisation_id AND c.folio_id=f.id WHERE (c.next_check_at IS NULL OR c.next_check_at<=$2) AND (f.state IN ('CLOSED','SETTLED') OR s.state='DEPARTED' AND EXISTS(SELECT 1 FROM folio_close_reviews r WHERE r.organisation_id=f.organisation_id AND r.folio_id=f.id AND r.automatic_close)) ORDER BY coalesce(c.next_check_at,f.created_at),f.id LIMIT $1`, limit, h.Auth.Service.Now().UTC())
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	var refs []ref
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.id); e != nil {
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
	for _, v := range refs {
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, v.org); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			f, e := hospitality.GetFolio(ctx, tx, v.org, v.id)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO hospitality_maintenance_cursors(organisation_id,folio_id,next_check_at) VALUES($1,$2,$3) ON CONFLICT(organisation_id,folio_id) DO UPDATE SET next_check_at=excluded.next_check_at`, v.org, v.id, h.Auth.Service.Now().UTC().Add(time.Minute)); e != nil {
				return e
			}
			t := tenant{Organisation: v.org}
			now := h.Auth.Service.Now().UTC()
			balance, e := hospitality.Balance(ctx, tx, v.org, v.id, now)
			if e != nil {
				return e
			}
			if e = h.queuePMSFinancialReadback(ctx, tx, t, f); e != nil {
				return e
			}
			digest, e := folioPostingDigest(ctx, tx, v.org, v.id)
			if e != nil {
				return e
			}
			if balance != 0 && (f.State == "CLOSED" || f.State == "SETTLED") {
				if _, e = tx.Exec(ctx, `UPDATE folios SET state='SETTLEMENT_PENDING',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, v.org, v.id); e != nil {
					return e
				}
				if e = h.pmsCommit(ctx, tx, t, f.PropertyID, f.ID, "folio", "FOLIO_BACKING_REVIEW_REQUIRED", "hospitality.folio.updated.v1", f.Version+1, map[string]any{"balance_minor": balance, "actor_service": serviceActor(t), "reason": "Current owning Payment backing is insufficient, expired or restricted"}); e != nil {
					return e
				}
				financialDigest, e := folioFinancialDigest(ctx, tx, t.Organisation, f.ID)
				if e != nil {
					return e
				}
				return h.pmsOperationException(ctx, tx, t, f, financialDigest, "PAYMENT_BACKING_REVIEW_REQUIRED", map[string]any{"balance_minor": balance})
			}
			review, e := hospitality.LatestFolioCloseReview(ctx, tx, v.org, v.id)
			var app *apperrors.Error
			if errors.As(e, &app) && app.Status == 404 {
				return nil
			}
			if e != nil {
				return e
			}
			if !review.AutomaticClose {
				return nil
			}
			var revoked bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folio_close_review_revocations WHERE organisation_id=$1 AND review_id=$2)`, v.org, review.ID).Scan(&revoked); e != nil {
				return e
			}
			if revoked || !now.Before(review.ExpiresAt) {
				return nil
			}
			if review.PostingDigest != digest {
				return h.pmsOperationException(ctx, tx, t, f, security.SHA256Hex([]byte(digest+review.ID.String())), "FOLIO_POSTINGS_CHANGED", map[string]any{"review_id": review.ID})
			}
			allowed, e := h.currentFolioReviewAuthority(ctx, tx, v.org, f.PropertyID, review)
			if e != nil {
				return e
			}
			if !allowed {
				return h.pmsOperationException(ctx, tx, t, f, security.SHA256Hex([]byte(digest+review.ID.String())), "FOLIO_REVIEW_AUTHORITY_REVOKED", map[string]any{"review_id": review.ID})
			}
			if balance != 0 {
				return nil
			}
			// Already closed roots need no additional history. A previously departed
			// debt may now be settled; use exactly the same completion implementation.
			if _, e = h.closePMSFolio(ctx, tx, t, f, "Execute bounded reviewed folio closure using current verified settlement"); e != nil {
				if errors.As(e, &app) && app.Status == 409 {
					return nil
				}
				return e
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return nil
}

func (h *Handler) pmsOperationException(ctx context.Context, tx pgx.Tx, t tenant, f hospitality.Folio, digest, code string, evidence any) error {
	id := ids.New()
	tag, e := tx.Exec(ctx, `INSERT INTO hospitality_operation_exceptions(id,organisation_id,property_id,source_type,source_id,source_digest,code,state,evidence,created_by) VALUES($1,$2,$3,'folio',$4,$5,$6,'OPEN',$7,$8) ON CONFLICT(organisation_id,source_type,source_id,source_digest,code) DO NOTHING`, id, t.Organisation, f.PropertyID, f.ID, digest, code, marshalCommercial(evidence), t.Account)
	if e != nil || tag.RowsAffected() == 0 {
		return e
	}
	return h.pmsCommit(ctx, tx, t, f.PropertyID, id, "hospitality_exception", "HOSPITALITY_EXCEPTION_OPENED", "hospitality.operation.exception.v1", 1, map[string]any{"folio_id": f.ID, "code": code, "evidence": evidence, "actor_service": serviceActor(t)})
}

// Safe maintenance reuses the existing readback queue, lease, retry and fencing.
// It cannot create a new charge, alter its payer or manufacture provider success.
func (h *Handler) queuePMSFinancialReadback(ctx context.Context, tx pgx.Tx, t tenant, f hospitality.Folio) error {
	now := h.Auth.Service.Now().UTC()
	rows, e := tx.Query(ctx, `SELECT DISTINCT b.financial_group_id,b.intent_id FROM folio_payment_references a JOIN reservation_payment_bindings b ON b.organisation_id=a.organisation_id AND b.financial_group_id=a.financial_group_id AND b.intent_id=a.intent_id WHERE a.organisation_id=$1 AND a.folio_id=$2 AND NOT EXISTS(SELECT 1 FROM reservation_payment_projections p WHERE p.organisation_id=b.organisation_id AND p.financial_group_id=b.financial_group_id AND p.intent_id=b.intent_id AND p.id=(SELECT latest.id FROM reservation_payment_projections latest WHERE latest.organisation_id=b.organisation_id AND latest.financial_group_id=b.financial_group_id ORDER BY observed_at DESC,id DESC LIMIT 1) AND p.expires_at>$3::timestamptz+interval '2 minutes') AND NOT EXISTS(SELECT 1 FROM reservation_financial_requests j WHERE j.organisation_id=b.organisation_id AND j.financial_group_id=b.financial_group_id AND j.kind='READBACK' AND j.state<>'SUCCEEDED')`, t.Organisation, f.ID, now)
	if e != nil {
		return e
	}
	type ref struct{ group, intent ids.ID }
	var refs []ref
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.group, &v.intent); e != nil {
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
	for _, v := range refs {
		id := ids.New()
		raw := marshalCommercial(map[string]any{"intent_id": v.intent})
		key := "pms-readback:" + v.group.String() + ":" + strconv.FormatInt(now.Unix()/60, 10)
		tag, e := tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,kind,request,request_hash,idempotency_key,provider_reference,created_by) VALUES($1,$2,$3,$4,$5,'READBACK',$6,$7,$8,$9,NULL) ON CONFLICT(organisation_id,idempotency_key) DO NOTHING`, id, t.Organisation, f.PropertyID, f.ReservationID, v.group, raw, hashCommercialPayload(raw), key, v.intent.String())
		if e != nil {
			return e
		}
		if tag.RowsAffected() > 0 {
			if e = h.pmsCommit(ctx, tx, t, f.PropertyID, id, "reservation_financial_request", "PMS_PAYMENT_READBACK_QUEUED", "", 1, map[string]any{"folio_id": f.ID, "financial_group_id": v.group, "actor_service": serviceActor(t)}); e != nil {
				return e
			}
		}
	}
	return nil
}

func folioFinancialDigest(ctx context.Context, q database.Querier, org, id ids.ID) (string, error) {
	var raw []byte
	e := q.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_array(a.id,p.id,p.expires_at) ORDER BY a.id),'[]'::jsonb) FROM folio_payment_references a LEFT JOIN LATERAL(SELECT id,expires_at FROM reservation_payment_projections p WHERE p.organisation_id=a.organisation_id AND p.financial_group_id=a.financial_group_id AND p.intent_id=a.intent_id ORDER BY observed_at DESC,id DESC LIMIT 1)p ON true WHERE a.organisation_id=$1 AND a.folio_id=$2`, org, id).Scan(&raw)
	return security.SHA256Hex(raw), e
}
