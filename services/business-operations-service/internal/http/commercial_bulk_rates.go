package businesshttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type CommercialBulkRateCommand struct {
	BranchID     *ids.ID            `json:"branch_id,omitempty"`
	ProductIDs   []ids.ID           `json:"product_ids,omitempty"`
	PropertyID   ids.ID             `json:"property_id,omitempty"`
	PlanVersions map[string]int64   `json:"plan_versions"`
	DateFrom     string             `json:"date_from"`
	DateTo       string             `json:"date_to"`
	Adjustment   revenue.Adjustment `json:"adjustment"`
	Reason       string             `json:"reason"`
}
type CommercialBulkSegment struct {
	PropertyID       ids.ID                 `json:"property_id"`
	PlanID           ids.ID                 `json:"plan_id"`
	PlanVersion      int64                  `json:"plan_version"`
	DateFrom         string                 `json:"date_from"`
	DateTo           string                 `json:"date_to"`
	Previous         revenue.RateVersion    `json:"previous"`
	Proposed         revenue.RateDefinition `json:"proposed"`
	AppliedVersionID *ids.ID                `json:"applied_version_id,omitempty"`
}

func (c CommercialBulkRateCommand) scope() (string, ids.ID) {
	if c.BranchID != nil {
		return "BRANCH", *c.BranchID
	}
	return "PROPERTY", c.PropertyID
}

type CommercialBulkSnapshot struct {
	Segments            []CommercialBulkSegment `json:"segments"`
	Conflicts           []string                `json:"conflicts"`
	ApprovalAmountMinor int64                   `json:"approval_amount_minor"`
	Currency            string                  `json:"currency"`
	Digest              string                  `json:"digest"`
	AppliedPlanVersions map[string]int64        `json:"applied_plan_versions"`
	RollbackOf          *ids.ID                 `json:"rollback_of,omitempty"`
}
type CommercialBulkView struct {
	ID                 ids.ID                    `json:"id"`
	OrganisationID     ids.ID                    `json:"organisation_id"`
	PropertyID         ids.ID                    `json:"property_id"`
	State              string                    `json:"state"`
	Version            int64                     `json:"version"`
	Command            CommercialBulkRateCommand `json:"command"`
	Snapshot           CommercialBulkSnapshot    `json:"snapshot"`
	CreatedBy          ids.ID                    `json:"created_by"`
	ExpiresAt          time.Time                 `json:"expires_at"`
	ApprovalID         *ids.ID                   `json:"approval_id,omitempty"`
	ExecutionSessionID *ids.ID                   `json:"execution_session_id,omitempty"`
	ExecutionAction    string                    `json:"execution_action,omitempty"`
	LastError          *string                   `json:"last_error,omitempty"`
}

func loadCommercialBulk(ctx context.Context, q database.Querier, org, id ids.ID) (CommercialBulkView, error) {
	return typedJSON[CommercialBulkView](ctx, q, `SELECT to_jsonb(b) FROM bulk_rate_commands b WHERE organisation_id=$1 AND id=$2`, org, id)
}
func bulkSnapshotDigest(s CommercialBulkSnapshot) (string, error) {
	s.Digest = ""
	s.AppliedPlanVersions = nil
	s.Segments = append([]CommercialBulkSegment{}, s.Segments...)
	for i := range s.Segments {
		s.Segments[i].AppliedVersionID = nil
	}
	raw, e := json.Marshal(s)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (h *Handler) bulkRatePreview(w http.ResponseWriter, r *http.Request) {
	var in CommercialBulkRateCommand
	if !decode(w, r, &in) {
		return
	}
	days, e := (inventory.DateRange{DateFrom: in.DateFrom, DateTo: in.DateTo}).Days("UTC")
	if e != nil || (in.BranchID == nil && in.PropertyID.ValidateV7() != nil || in.BranchID != nil && in.BranchID.ValidateV7() != nil) || len(in.ProductIDs) > 100 || len(in.PlanVersions) < 1 || len(in.PlanVersions) > 20 || in.Adjustment.Validate() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A property, 1–20 expected plan versions, civil-date range, exact adjustment and reason are required."))
		return
	}
	scopeKind, scopeID := in.scope()
	selectedProperty := in.PropertyID
	h.execute(w, r, "commercial:bulk-rate:preview", in, "rates:write", scopeKind, scopeID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rates:write", scopeKind, scopeID, true)
		if e != nil {
			return 0, nil, e
		}
		if len(in.ProductIDs) > 0 {
			selected := map[ids.ID]bool{}
			seenProducts := map[ids.ID]bool{}
			for _, id := range in.ProductIDs {
				if id.ValidateV7() != nil || seenProducts[id] {
					return 0, nil, apperrors.InvalidRequest("Selected products must be distinct canonical UUIDv7 values.")
				}
				seenProducts[id] = true
				p, e := inventory.GetProduct(ctx, tx, t.Organisation, id)
				if e != nil {
					return 0, nil, e
				}
				if p.RatePlanID == nil || in.PlanVersions[p.RatePlanID.String()] < 1 {
					return 0, nil, apperrors.InvalidRequest("Every selected product must pin an explicitly selected rate-plan version.")
				}
				selected[*p.RatePlanID] = true
			}
			if len(selected) != len(in.PlanVersions) {
				return 0, nil, apperrors.InvalidRequest("Selected products and rate-plan filters must identify the same reviewed rate set.")
			}
		}
		snapshot := CommercialBulkSnapshot{Segments: []CommercialBulkSegment{}, Conflicts: []string{}, AppliedPlanVersions: map[string]int64{}}
		keys := commercialSortedKeys(in.PlanVersions)
		for _, raw := range keys {
			id, e := ids.ParseV7(raw)
			if e != nil || in.PlanVersions[raw] < 1 {
				return 0, nil, apperrors.InvalidRequest("Plan identifiers and expected versions must be valid.")
			}
			plan, e := revenue.GetPlan(ctx, tx, t.Organisation, id)
			if e != nil {
				return 0, nil, e
			}
			if _, _, e = h.authorize(ctx, tx, "rates:write", "PROPERTY", plan.PropertyID, true); e != nil {
				return 0, nil, e
			}
			if in.BranchID != nil {
				if !selectedProperty.IsZero() && plan.PropertyID != selectedProperty {
					return 0, nil, apperrors.PermissionDenied("Plan selection differs from its explicit property filter.")
				}
				property, e := portfolio.Get(ctx, tx, t.Organisation, plan.PropertyID)
				if e != nil {
					return 0, nil, e
				}
				if property.BranchID != *in.BranchID {
					return 0, nil, apperrors.PermissionDenied("Bulk selection cannot cross its reviewed branch boundary.")
				}
				if in.PropertyID.IsZero() {
					in.PropertyID = plan.PropertyID
				}
			} else if plan.PropertyID != in.PropertyID {
				return 0, nil, apperrors.PermissionDenied("Property-scoped bulk selection cannot cross canonical property boundaries.")
			}
			if plan.Version != in.PlanVersions[raw] || plan.State == "ARCHIVED" {
				snapshot.Conflicts = append(snapshot.Conflicts, "PLAN_VERSION_OR_STATE:"+raw)
				continue
			}
			var current *CommercialBulkSegment
			for _, day := range days {
				v, e := revenue.VersionForDay(ctx, tx, t.Organisation, id, day)
				if e != nil {
					snapshot.Conflicts = append(snapshot.Conflicts, "UNCOVERED_RATE_DATE:"+raw+":"+day)
					continue
				}
				next := v.Definition
				base, e := commercialMoney(next.AmountMinor, next.Currency)
				if e != nil {
					return 0, nil, e
				}
				base, e = revenue.Adjust(base, []revenue.Adjustment{in.Adjustment})
				if e != nil {
					return 0, nil, e
				}
				next.AmountMinor = base.AmountMinor
				if next.Validate() != nil {
					snapshot.Conflicts = append(snapshot.Conflicts, "PRICE_BOUNDS:"+raw+":"+day)
					continue
				}
				date, _ := time.Parse(time.DateOnly, day)
				end := date.AddDate(0, 0, 1).Format(time.DateOnly)
				if current != nil && current.Previous.ID == v.ID && current.DateTo == day {
					current.DateTo = end
					snapshot.Segments[len(snapshot.Segments)-1] = *current
					continue
				}
				segment := CommercialBulkSegment{PropertyID: plan.PropertyID, PlanID: id, PlanVersion: plan.Version, DateFrom: day, DateTo: end, Previous: v, Proposed: next}
				snapshot.Segments = append(snapshot.Segments, segment)
				current = &snapshot.Segments[len(snapshot.Segments)-1]
				if snapshot.Currency != "" && snapshot.Currency != next.Currency {
					return 0, nil, apperrors.InvalidRequest("A bulk approval must use one explicit currency; split mixed-currency work.")
				}
				snapshot.Currency = next.Currency
				snapshot.ApprovalAmountMinor, e = addMinor(snapshot.ApprovalAmountMinor, next.CeilingMinor, next.Currency)
				if e != nil {
					return 0, nil, e
				}
				if len(snapshot.Segments) > 500 {
					return 0, nil, apperrors.InvalidRequest("Consolidate the command to at most 500 immutable rate segments.")
				}
			}
		}
		return h.storeBulkPreview(ctx, tx, t, in, snapshot)
	})
}
func commercialSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (h *Handler) storeBulkPreview(ctx context.Context, tx pgx.Tx, t tenant, in CommercialBulkRateCommand, snapshot CommercialBulkSnapshot) (int, any, error) {
	var e error
	snapshot.Digest, e = bulkSnapshotDigest(snapshot)
	if e != nil {
		return 0, nil, e
	}
	id := ids.New()
	command, _ := json.Marshal(in)
	raw, _ := json.Marshal(snapshot)
	expiry := h.Auth.Service.Now().UTC().Add(30 * time.Minute)
	if _, e = tx.Exec(ctx, `INSERT INTO bulk_rate_commands(id,organisation_id,property_id,command,snapshot,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, in.PropertyID, command, raw, expiry, t.Account); e != nil {
		return 0, nil, e
	}
	properties := map[ids.ID]bool{}
	for _, segment := range snapshot.Segments {
		if !properties[segment.PropertyID] {
			properties[segment.PropertyID] = true
			if _, e = tx.Exec(ctx, `INSERT INTO bulk_rate_scope_links(organisation_id,command_id,property_id) VALUES($1,$2,$3)`, t.Organisation, id, segment.PropertyID); e != nil {
				return 0, nil, e
			}
		}
	}
	value, e := loadCommercialBulk(ctx, tx, t.Organisation, id)
	if e != nil {
		return 0, nil, e
	}
	e = h.commercialCommit(ctx, tx, t, in.PropertyID, id, "bulk_rate_command", "BULK_RATE_PREVIEW_CREATED", "", 1, map[string]any{"preview_id": id, "digest": snapshot.Digest, "conflicts": snapshot.Conflicts, "approval_amount_minor": snapshot.ApprovalAmountMinor, "currency": snapshot.Currency})
	return 201, value, e
}

type CommercialBulkApply struct {
	PreviewID      ids.ID `json:"preview_id"`
	Version        int64  `json:"version"`
	SnapshotDigest string `json:"snapshot_digest"`
	ApprovalID     ids.ID `json:"approval_id"`
	Reason         string `json:"reason"`
}

func (h *Handler) bulkRateApply(w http.ResponseWriter, r *http.Request) {
	var in CommercialBulkApply
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	view, e := loadCommercialBulk(r.Context(), h.Auth.Service.Pool, t.Organisation, in.PreviewID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || len(in.SnapshotDigest) != 64 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Expected preview version, immutable snapshot digest and reason required."))
		return
	}
	scopeKind, scopeID := view.Command.scope()
	h.execute(w, r, "commercial:bulk-rate:apply:"+in.PreviewID.String(), in, "rates:write", scopeKind, scopeID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "rates:write", scopeKind, scopeID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := loadCommercialBulk(ctx, tx, t.Organisation, in.PreviewID)
		if e != nil {
			return 0, nil, e
		}
		if current.CreatedBy != t.Account || current.State != "PREVIEW" || current.Version != in.Version || current.Snapshot.Digest != in.SnapshotDigest || len(current.Snapshot.Conflicts) > 0 || len(current.Snapshot.Segments) == 0 || !h.Auth.Service.Now().Before(current.ExpiresAt) {
			return 0, nil, apperrors.Conflict("BULK_PREVIEW_STALE", "Only the requesting actor's current conflict-free unexpired snapshot may be queued.")
		}
		action := "COMMERCIAL_BULK_RATE_CHANGE"
		if current.Snapshot.RollbackOf != nil {
			action = "COMMERCIAL_BULK_RATE_ROLLBACK"
		}
		command := in
		command.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, action, target.Scope, in.ApprovalID, map[string]any{"command": command}, "", &current.Snapshot.ApprovalAmountMinor, current.Snapshot.Currency); e != nil {
			return 0, nil, e
		}
		principal, ok := businessauth.PrincipalFrom(ctx)
		if !ok {
			return 0, nil, apperrors.PermissionDenied("Current authenticated session required.")
		}
		if _, e = tx.Exec(ctx, `UPDATE bulk_rate_commands SET state='QUEUED',approval_id=$3,execution_session_id=$4,execution_action=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, current.ID, in.ApprovalID, principal.SessionID, action); e != nil {
			return 0, nil, e
		}
		value, e := loadCommercialBulk(ctx, tx, t.Organisation, current.ID)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, current.PropertyID, current.ID, "bulk_rate_command", "BULK_RATE_QUEUED", "", value.Version, map[string]any{"preview_id": current.ID, "snapshot_digest": current.Snapshot.Digest, "approval_id": in.ApprovalID})
		value.ExecutionSessionID = nil
		return 202, value, e
	})
}

type CommercialBulkRollback struct {
	CommandID ids.ID `json:"command_id"`
	Version   int64  `json:"version"`
	Reason    string `json:"reason"`
}

func (h *Handler) bulkRateRollback(w http.ResponseWriter, r *http.Request) {
	var in CommercialBulkRollback
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	old, e := loadCommercialBulk(r.Context(), h.Auth.Service.Pool, t.Organisation, in.CommandID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Rollback review reason required."))
		return
	}
	scopeKind, scopeID := old.Command.scope()
	h.execute(w, r, "commercial:bulk-rate:rollback-preview:"+in.CommandID.String(), in, "rates:write", scopeKind, scopeID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rates:write", scopeKind, scopeID, true)
		if e != nil {
			return 0, nil, e
		}
		old, e := loadCommercialBulk(ctx, tx, t.Organisation, in.CommandID)
		if e != nil {
			return 0, nil, e
		}
		if old.Version != in.Version || old.State != "APPLIED" || old.Snapshot.RollbackOf != nil {
			return 0, nil, apperrors.Conflict("BULK_ROLLBACK_CONFLICT", "Only a current applied original command can produce a rollback preview.")
		}
		next := old.Command
		next.Reason = in.Reason
		next.PlanVersions = map[string]int64{}
		s := CommercialBulkSnapshot{Segments: []CommercialBulkSegment{}, Conflicts: []string{}, Currency: old.Snapshot.Currency, RollbackOf: &old.ID, AppliedPlanVersions: map[string]int64{}}
		for _, segment := range old.Snapshot.Segments {
			p, e := revenue.GetPlan(ctx, tx, t.Organisation, segment.PlanID)
			if e != nil {
				return 0, nil, e
			}
			if p.Version != old.Snapshot.AppliedPlanVersions[p.ID.String()] || segment.AppliedVersionID == nil {
				return 0, nil, apperrors.Conflict("BULK_ROLLBACK_INTERVENING_CHANGE", "Rollback cannot overwrite any intervening canonical rate change.")
			}
			applied, e := revenue.GetVersion(ctx, tx, t.Organisation, *segment.AppliedVersionID)
			if e != nil {
				return 0, nil, e
			}
			s.Segments = append(s.Segments, CommercialBulkSegment{PropertyID: p.PropertyID, PlanID: p.ID, PlanVersion: p.Version, DateFrom: segment.DateFrom, DateTo: segment.DateTo, Previous: applied, Proposed: segment.Previous.Definition})
			next.PlanVersions[p.ID.String()] = p.Version
			s.ApprovalAmountMinor, e = addMinor(s.ApprovalAmountMinor, segment.Previous.Definition.CeilingMinor, s.Currency)
			if e != nil {
				return 0, nil, e
			}
		}
		return h.storeBulkPreview(ctx, tx, t, next, s)
	})
}
func (h *Handler) bulkRateGet(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	v, e := loadCommercialBulk(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		scopeKind, scopeID := v.Command.scope()
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "rates:read", scopeKind, scopeID, false)
	}
	v.ExecutionSessionID = nil
	reply(w, r, 200, v, e)
}

// ProcessCommercialRates is called by the existing worker loop. The organisation
// lock plus row claim fences concurrent workers. A crash rolls back the entire
// price/outbox/checkpoint transaction and leaves the durable queued command.
func (h *Handler) ProcessCommercialRates(ctx context.Context, limit int) error {
	if limit < 1 || limit > 20 {
		limit = 20
	}
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM bulk_rate_commands WHERE state='QUEUED' AND available_at<=now() ORDER BY created_at,id LIMIT $1`, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.id); e != nil {
			break
		}
		refs = append(refs, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	for _, ref := range refs {
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, ref.org); e != nil {
				return e
			}
			var state string
			if e := tx.QueryRow(ctx, `SELECT state FROM bulk_rate_commands WHERE organisation_id=$1 AND id=$2 FOR UPDATE SKIP LOCKED`, ref.org, ref.id).Scan(&state); errors.Is(e, pgx.ErrNoRows) {
				return nil
			} else if e != nil {
				return e
			}
			if state != "QUEUED" {
				return nil
			}
			view, e := loadCommercialBulk(ctx, tx, ref.org, ref.id)
			if e != nil {
				return e
			}
			if view.ExecutionSessionID == nil || view.ApprovalID == nil || !h.Auth.Service.Now().Before(view.ExpiresAt) {
				return apperrors.Conflict("BULK_EXECUTION_EXPIRED", "Execution session, approval or preview expired.")
			}
			ctx = businessauth.StoredJobContext(ctx, view.CreatedBy, view.OrganisationID, *view.ExecutionSessionID, view.ID)
			scopeKind, scopeID := view.Command.scope()
			t, _, e := h.authorize(ctx, tx, "rates:write", scopeKind, scopeID, true)
			if e != nil {
				return e
			}
			if e = h.commercialGate(ctx, tx, t, view.PropertyID); e != nil {
				return e
			}
			var consumedBy *ids.ID
			var deadline time.Time
			var approvalState string
			if e = tx.QueryRow(ctx, `SELECT consumed_by_account_id,deadline,state FROM approval_requests WHERE organisation_id=$1 AND id=$2 FOR SHARE`, ref.org, *view.ApprovalID).Scan(&consumedBy, &deadline, &approvalState); e != nil {
				return e
			}
			if consumedBy == nil || *consumedBy != view.CreatedBy || approvalState != "APPROVED" || !h.Auth.Service.Now().Before(deadline) {
				return apperrors.PermissionDenied("Queued authorization is no longer usable.")
			}
			digest, e := bulkSnapshotDigest(view.Snapshot)
			if e != nil {
				return e
			}
			if digest != view.Snapshot.Digest {
				return apperrors.Conflict("BULK_SNAPSHOT_CORRUPT", "Bulk snapshot digest changed.")
			}
			plans := map[string]int64{}
			properties := map[string]ids.ID{}
			if view.Snapshot.AppliedPlanVersions == nil {
				view.Snapshot.AppliedPlanVersions = map[string]int64{}
			}
			for i, segment := range view.Snapshot.Segments {
				plan, e := revenue.GetPlan(ctx, tx, ref.org, segment.PlanID)
				if e != nil {
					return e
				}
				if plan.PropertyID != segment.PropertyID || plan.State == "ARCHIVED" || plan.Version != segment.PlanVersion {
					return apperrors.Conflict("BULK_RATE_VERSION_CONFLICT", "A previewed canonical rate changed before execution.")
				}
				if _, _, e = h.authorize(ctx, tx, "rates:write", "PROPERTY", plan.PropertyID, true); e != nil {
					return e
				}
				if view.Command.BranchID != nil {
					node, e := portfolio.Get(ctx, tx, ref.org, plan.PropertyID)
					if e != nil {
						return e
					}
					if node.BranchID != *view.Command.BranchID {
						return apperrors.Conflict("BULK_BRANCH_SCOPE_CHANGED", "Canonical property moved outside the approved branch.")
					}
				}
				if e = h.commercialGate(ctx, tx, t, plan.PropertyID); e != nil {
					return e
				}
				dates, e := (inventory.DateRange{DateFrom: segment.DateFrom, DateTo: segment.DateTo}).Days("UTC")
				if e != nil {
					return e
				}
				for _, day := range dates {
					v, e := revenue.VersionForDay(ctx, tx, ref.org, plan.ID, day)
					if e != nil {
						return e
					}
					if v.ID != segment.Previous.ID {
						return apperrors.Conflict("BULK_RATE_VERSION_CONFLICT", "An effective rate changed before execution.")
					}
				}
				v, e := h.writeRateVersion(ctx, tx, t, plan.PropertyID, plan.ID, segment.Proposed, segment.DateFrom, segment.DateTo, view.Command.Reason, *view.ApprovalID)
				if e != nil {
					return e
				}
				view.Snapshot.Segments[i].AppliedVersionID = &v.ID
				plans[plan.ID.String()] = plan.Version
				properties[plan.ID.String()] = plan.PropertyID
			}
			for _, raw := range commercialSortedKeys(plans) {
				id, _ := ids.ParseV7(raw)
				if _, e = tx.Exec(ctx, `UPDATE rate_plans SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, ref.org, id); e != nil {
					return e
				}
				view.Snapshot.AppliedPlanVersions[raw] = plans[raw] + 1
				if e = h.commercialCommit(ctx, tx, t, properties[raw], id, "rate_plan", "BULK_RATE_APPLIED", businessEvents.RatePlanChanged, plans[raw]+1, map[string]any{"bulk_command_id": view.ID, "approval_id": view.ApprovalID, "snapshot_digest": view.Snapshot.Digest}); e != nil {
					return e
				}
			}
			raw, e := json.Marshal(view.Snapshot)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE bulk_rate_commands SET state='APPLIED',snapshot=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, ref.org, ref.id, raw); e != nil {
				return e
			}
			if view.Snapshot.RollbackOf != nil {
				tag, e := tx.Exec(ctx, `UPDATE bulk_rate_commands SET state='ROLLED_BACK',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state='APPLIED'`, ref.org, *view.Snapshot.RollbackOf)
				if e != nil {
					return e
				}
				if tag.RowsAffected() != 1 {
					return apperrors.Conflict("BULK_ROLLBACK_CONFLICT", "Original bulk command was already rolled back.")
				}
			}
			return h.commercialCommit(ctx, tx, t, view.PropertyID, ref.id, "bulk_rate_command", "BULK_RATE_EXECUTED", "", view.Version+1, map[string]any{"snapshot_digest": view.Snapshot.Digest, "applied_plan_versions": view.Snapshot.AppliedPlanVersions, "rollback_of": view.Snapshot.RollbackOf})
		})
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			code := commercialErrorCode(e)
			if failure := database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, ref.org); err != nil {
					return err
				}
				view, err := loadCommercialBulk(ctx, tx, ref.org, ref.id)
				if err != nil {
					return err
				}
				tag, err := tx.Exec(ctx, `UPDATE bulk_rate_commands SET state='QUARANTINED',last_error=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state='QUEUED'`, ref.org, ref.id, code)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					return nil
				}
				return businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, ref.org, view.PropertyID, ref.id, ids.Nil, "bulk_rate_command", "BULK_RATE_QUARANTINED", "", view.Version+1, map[string]any{"classification": code})
			}); failure != nil {
				return failure
			}
		}
	}
	return nil
}
