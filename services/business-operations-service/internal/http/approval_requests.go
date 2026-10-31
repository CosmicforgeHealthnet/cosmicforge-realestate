package businesshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type approvalCommand struct {
	Channel     string          `json:"channel,omitempty"`
	Action      string          `json:"action"`
	Risk        string          `json:"risk"`
	ScopeKind   string          `json:"scope_kind"`
	ScopeID     ids.ID          `json:"scope_id,omitempty"`
	AmountMinor *int64          `json:"amount_minor,omitempty"`
	Currency    string          `json:"currency,omitempty"`
	Operation   json.RawMessage `json:"operation"`
	Reason      string          `json:"reason"`
	Deadline    time.Time       `json:"deadline,omitempty"`
	Draft       bool            `json:"draft,omitempty"`
}

func validateApproval(in *approvalCommand) error {
	if in.ScopeKind == "" {
		in.ScopeKind = "COMPANY"
	}
	if in.Risk == "" {
		in.Risk = "HIGH"
	}
	if len(in.Channel) > 64 {
		return apperrors.InvalidRequest("Invalid channel context.")
	}
	if !highRiskAction(in.Action) || in.Risk != "HIGH" && in.Risk != "CRITICAL" || len(in.Reason) < 3 || len(in.Reason) > 2000 || len(in.Operation) == 0 || len(in.Operation) > 32768 || !json.Valid(in.Operation) {
		return apperrors.InvalidRequest("Invalid approval action, reason or operation.")
	}
	var operation map[string]json.RawMessage
	if json.Unmarshal(in.Operation, &operation) != nil || operation == nil {
		return apperrors.InvalidRequest("The operation must be an object.")
	}
	if containsCredentialField(operation) {
		return apperrors.InvalidRequest("Approval operations must contain references, not credentials or secret values.")
	}
	if in.AmountMinor != nil {
		if *in.AmountMinor < 0 {
			return apperrors.InvalidRequest("Amounts use nonnegative integer minor units.")
		}
		if _, err := money.ParseCurrency(in.Currency); err != nil {
			return apperrors.InvalidRequest("Invalid currency.")
		}
	}
	return nil
}

func containsCredentialField(value any) bool {
	switch typed := value.(type) {
	case map[string]json.RawMessage:
		for key, raw := range typed {
			switch strings.ToLower(key) {
			case "password", "current_password", "new_password", "access_token", "refresh_token", "secret", "private_key", "totp_secret", "mfa_token", "invitation_token", "recovery_code", "recovery_codes", "bank_account_number":
				return true
			}
			var nested any
			if json.Unmarshal(raw, &nested) == nil && containsCredentialField(nested) {
				return true
			}
		}
	case map[string]any:
		for key, nested := range typed {
			switch strings.ToLower(key) {
			case "password", "current_password", "new_password", "access_token", "refresh_token", "secret", "private_key", "totp_secret", "mfa_token", "invitation_token", "recovery_code", "recovery_codes", "bank_account_number":
				return true
			}
			if containsCredentialField(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if containsCredentialField(nested) {
				return true
			}
		}
	}
	return false
}
func operationDigest(action string, operation any) (string, error) {
	raw, err := normalizedOperation(operation)
	if err != nil {
		return "", err
	}
	return idempotency.Fingerprint("COMMAND", action, raw)
}

// Canonicalize the public operation intent without binding the authorization's
// own ID or representation-only defaults. Required entity IDs and versions are
// still validated by the executing command before any state change.
func normalizedOperation(operation any) (json.RawMessage, error) {
	raw, err := json.Marshal(operation)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var normalize func(any)
	normalize = func(value any) {
		switch object := value.(type) {
		case map[string]any:
			if command, ok := object["command"].(map[string]any); ok && object["node_id"] != nil {
				for _, key := range []string{"parent_id", "previous_parent_id", "branch_id", "room_type_id", "expected_node_id"} {
					if command[key] == nil || command[key] == ids.Nil.String() {
						delete(command, key)
					}
				}
				for _, key := range []string{"detach", "cascade", "remove", "clear_organisational_scopes"} {
					if command[key] == false {
						delete(command, key)
					}
				}
			}

			delete(object, "approval_id")
			for key, item := range object {
				if (key == "scope_id" || key == "previous_role_id") && item == nil {
					delete(object, key)
					continue
				}
				if text, ok := item.(string); ok {
					if (key == "scope_id" || key == "previous_role_id") && text == ids.Nil.String() {
						delete(object, key)
						continue
					}
					if key == "email" {
						object[key] = strings.ToLower(strings.TrimSpace(text))
					}
					if key == "effective_at" || key == "expires_at" {
						if date, err := time.Parse(time.RFC3339Nano, text); err == nil {
							if date.IsZero() {
								delete(object, key)
								continue
							}
							object[key] = date.UTC().Format(time.RFC3339Nano)
						}
					}
				}
				if key == "version" && item == json.Number("0") {
					delete(object, key)
					continue
				}
				if (key == "clear_assignment" || key == "clear_expiry") && item == false {
					delete(object, key)
					continue
				}
				normalize(item)
			}
			if object["resource"] == "role_template" {
				if _, ok := object["senior_emergency_eligible"]; !ok {
					object["senior_emergency_eligible"] = false
				}
			}
			if _, ok := object["email"]; ok {
				if _, invite := object["role_id"]; invite {
					if _, ok := object["scope_kind"]; !ok {
						object["scope_kind"] = "COMPANY"
					}
					if _, ok := object["kind"]; !ok {
						object["kind"] = "EMPLOYEE"
					}
				}
			}
			if _, grant := object["capability"]; grant {
				for _, key := range []string{"inherit", "deny", "require_mfa"} {
					if _, ok := object[key]; !ok {
						object[key] = false
					}
				}
				if _, ok := object["scope_kind"]; !ok {
					object["scope_kind"] = ""
				}
			}
			if list, ok := object["capabilities"].([]any); ok {
				sort.Slice(list, func(i, j int) bool { a, _ := list[i].(string); b, _ := list[j].(string); return a < b })
			}
		case []any:
			for _, item := range object {
				normalize(item)
			}
		}
	}
	normalize(value)
	return json.Marshal(value)
}
func (h *Handler) evaluateApproval(w http.ResponseWriter, r *http.Request) {
	var in approvalCommand
	if !decode(w, r, &in) {
		return
	}
	if err := validateApproval(&in); err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	t, target, err := h.authorize(r.Context(), h.Auth.Service.Pool, "approvals:create", in.ScopeKind, in.ScopeID, true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	policy, err := h.findPolicy(r.Context(), h.Auth.Service.Pool, t, target, in.Action, in.Risk, in.AmountMinor, in.Currency, in.Channel)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	digest, err := operationDigest(in.Action, in.Operation)
	reply(w, r, 200, map[string]any{"required": true, "policy": policy, "operation_digest": digest, "scope": target.Scope}, err)
}
func (h *Handler) createApproval(w http.ResponseWriter, r *http.Request) {
	var in approvalCommand
	if !decode(w, r, &in) {
		return
	}
	if err := validateApproval(&in); err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	h.execute(w, r, "approval:create", in, "approvals:create", in.ScopeKind, in.ScopeID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "approvals:create", in.ScopeKind, in.ScopeID, true)
		if err != nil {
			return 0, nil, err
		}
		p, err := h.findPolicy(ctx, tx, t, target, in.Action, in.Risk, in.AmountMinor, in.Currency, in.Channel)
		if err != nil {
			return 0, nil, err
		}
		now := h.Auth.Service.Now().UTC()
		deadline := in.Deadline
		if deadline.IsZero() {
			deadline = now.Add(24 * time.Hour)
		}
		if !deadline.After(now) || deadline.After(now.Add(7*24*time.Hour)) {
			return 0, nil, apperrors.InvalidRequest("Approval deadlines must be within seven days.")
		}
		request, err := approvals.NewRequest(p, t.Account, target.Scope, in.AmountMinor, in.Currency, now, deadline)
		if err != nil {
			return 0, nil, apperrors.InvalidRequest("Invalid approval request.")
		}
		view := request.Snapshot()
		if in.Draft {
			view.State = "DRAFT"
		}
		snapshot, err := json.Marshal(p)
		if err != nil {
			return 0, nil, err
		}
		digest, err := operationDigest(in.Action, in.Operation)
		if err != nil {
			return 0, nil, err
		}
		operationContext, err := normalizedOperation(in.Operation)
		if err != nil {
			return 0, nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO approval_requests(id,organisation_id,policy_id,requester_id,action,scope_kind,scope_id,amount_minor,currency,policy_snapshot,state,deadline,reason,idempotency_key,request_hash,operation_digest,operation_context,channel) VALUES($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,''),$10,$11,$12,$13,$14,$15,$15,$16,$17)`, view.ID, t.Organisation, p.ID, t.Account, in.Action, target.Scope.Kind, target.Scope.ID, in.AmountMinor, in.Currency, snapshot, view.State, deadline, in.Reason, r.Header.Get("Idempotency-Key"), digest, []byte(operationContext), in.Channel)
		if err != nil {
			return 0, nil, err
		}
		for i, level := range p.Levels {
			if _, err := tx.Exec(ctx, `INSERT INTO approval_steps(id,organisation_id,request_id,level,capability,required_count,require_mfa) VALUES($1,$2,$3,$4,$5,$6,$7)`, ids.New(), t.Organisation, view.ID, i, string(level.Capability), level.Count, level.RequireMFA); err != nil {
				return 0, nil, err
			}
		}
		if err := audit(ctx, tx, t, "APPROVAL_REQUEST_CREATED", "approval_request", view.ID, map[string]any{"action": in.Action, "policy_id": p.ID, "version": p.Version, "operation_digest": digest, "state": view.State}); err != nil {
			return 0, nil, err
		}
		if !in.Draft {
			if err := h.approvalEvent(ctx, tx, view, "business.approval.requested.v1"); err != nil {
				return 0, nil, err
			}
		}
		return 201, map[string]any{"approval": view, "version": int64(1), "operation_digest": digest}, nil
	})
}
func loadApproval(ctx context.Context, q database.Querier, org, id ids.ID, lock bool) (*approvals.Request, int64, string, error) {
	query := `SELECT id,requester_id,policy_snapshot,scope_kind,scope_id,state,current_level,deadline,amount_minor,currency,version,operation_digest FROM approval_requests WHERE organisation_id=$1 AND id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	var v approvals.View
	v.Scope.Organisation = org
	var raw []byte
	var amount *int64
	var currency *string
	var version int64
	var digest string
	err := q.QueryRow(ctx, query, org, id).Scan(&v.ID, &v.RequesterID, &raw, &v.Scope.Kind, &v.Scope.ID, &v.State, &v.Level, &v.Deadline, &amount, &currency, &version, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, "", apperrors.NotFound("APPROVAL_NOT_FOUND", "Approval request not found.")
	}
	if err != nil {
		return nil, 0, "", err
	}
	if err := json.Unmarshal(raw, &v.Policy); err != nil {
		return nil, 0, "", err
	}
	rows, err := q.Query(ctx, `SELECT id,actor_id,level,outcome,reason,evidence_ids,created_at,idempotency_key FROM approval_decisions WHERE organisation_id=$1 AND request_id=$2 ORDER BY created_at,id`, org, id)
	if err != nil {
		return nil, 0, "", err
	}
	for rows.Next() {
		var d approvals.Decision
		if err := rows.Scan(&d.ID, &d.ActorID, &d.Level, &d.Outcome, &d.Reason, &d.Evidence, &d.At, &d.Key); err != nil {
			rows.Close()
			return nil, 0, "", err
		}
		v.Decisions = append(v.Decisions, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, "", err
	}
	ccy := ""
	if currency != nil {
		ccy = *currency
	}
	request, err := approvals.Restore(v, amount, ccy)
	return request, version, digest, err
}
func (h *Handler) approvalTarget(r *http.Request, id ids.ID, capability string, step bool) (tenant, workforce.Target, error) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		return t, workforce.Target{}, err
	}
	var kind string
	var scope ids.ID
	err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT scope_kind,scope_id FROM approval_requests WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&kind, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, workforce.Target{}, apperrors.NotFound("APPROVAL_NOT_FOUND", "Approval request not found.")
	}
	if err != nil {
		return t, workforce.Target{}, err
	}
	return h.authorize(r.Context(), h.Auth.Service.Pool, capability, kind, scope, step)
}
func (h *Handler) getApproval(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid approval identifier."))
		return
	}
	t, _, err := h.approvalTarget(r, id, "approvals:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	request, version, digest, err := loadApproval(r.Context(), h.Auth.Service.Pool, t.Organisation, id, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	contextData, err := oneJSON(r.Context(), h.Auth.Service.Pool, `SELECT jsonb_build_object('operation',operation_context,'reason',reason,'amount_minor',amount_minor,'currency',currency,'channel',channel) FROM approval_requests WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	reply(w, r, 200, map[string]any{"approval": request.Snapshot(), "version": version, "operation_digest": digest, "context": contextData}, err)
}
func (h *Handler) listApprovals(w http.ResponseWriter, r *http.Request) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if !t.Membership.RoleCapabilities.Has("approvals:read") {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Capability denied."))
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	state := r.URL.Query().Get("state")
	switch state {
	case "", "DRAFT", "PENDING", "APPROVED", "REJECTED", "CANCELLED", "EXPIRED":
	default:
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid approval state filter."))
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT id,scope_kind,scope_id FROM approval_requests WHERE organisation_id=$1 AND ($2='' OR state=$2) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, state, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	type candidate struct {
		id, scope ids.ID
		kind      string
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.kind, &c.scope); err != nil {
			rows.Close()
			reply(w, r, 0, nil, err)
			return
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	items := []map[string]any{}
	for _, c := range candidates {
		if _, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "approvals:read", c.kind, c.scope, false); err != nil {
			if _, ok := err.(*apperrors.Error); ok {
				continue
			}
			reply(w, r, 0, nil, err)
			return
		}
		request, version, digest, err := loadApproval(r.Context(), h.Auth.Service.Pool, t.Organisation, c.id, false)
		if err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		items = append(items, map[string]any{"approval": request.Snapshot(), "version": version, "operation_digest": digest})
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "next_offset": offset + len(candidates)}, nil)
}

type decisionCommand struct {
	Version  int64    `json:"version"`
	Level    int      `json:"level"`
	Outcome  string   `json:"outcome"`
	Reason   string   `json:"reason"`
	Evidence []ids.ID `json:"evidence,omitempty"`
}

func (h *Handler) decideApproval(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid approval identifier."))
		return
	}
	var in decisionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.Level < 0 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version and level are required."))
		return
	}
	_, target, err := h.approvalTarget(r, id, "approvals:decide", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	h.execute(w, r, "approval:decision:"+id.String(), in, "approvals:decide", target.Scope.Kind, target.Scope.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, resolved, err := h.authorize(ctx, tx, "approvals:decide", target.Scope.Kind, target.Scope.ID, true)
		if err != nil {
			return 0, nil, err
		}
		request, version, _, err := loadApproval(ctx, tx, t.Organisation, id, true)
		if err != nil {
			return 0, nil, err
		}
		before := request.Snapshot()
		if version != in.Version || before.Level != in.Level {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Approval version or level has changed.")
		}
		authorities, err := h.decisionAuthorities(ctx, tx, t, resolved, before)
		if err != nil {
			return 0, nil, err
		}
		err = request.Decide(t.Membership, resolved, in.Outcome, in.Reason, r.Header.Get("Idempotency-Key"), in.Evidence, h.Auth.Service.Now().UTC(), t.MFAAt)
		if errors.Is(err, approvals.ErrDenied) {
			return 0, nil, apperrors.PermissionDenied("Approver capability, MFA, scope or separation of duties denied.")
		}
		if errors.Is(err, approvals.ErrConflict) {
			return 0, nil, apperrors.Conflict("APPROVAL_STATE_CONFLICT", "Approval is terminal, expired or the decision conflicts.")
		}
		if err != nil {
			return 0, nil, apperrors.InvalidRequest("Invalid approval decision.")
		}
		view := request.Snapshot()
		if len(view.Decisions) != len(before.Decisions)+1 {
			return 0, nil, apperrors.Conflict("DUPLICATE_DECISION", "Decision already exists.")
		}
		d := view.Decisions[len(view.Decisions)-1]
		if d.Evidence == nil {
			d.Evidence = []ids.ID{}
		}
		_, err = tx.Exec(ctx, `INSERT INTO approval_decisions(id,organisation_id,request_id,level,actor_id,outcome,reason,evidence_ids,idempotency_key,created_at,authority_account_ids) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, d.ID, t.Organisation, id, d.Level, d.ActorID, d.Outcome, d.Reason, d.Evidence, d.Key, d.At, authorities)
		if err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE approval_requests SET state=$3,current_level=$4,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, view.State, view.Level); err != nil {
			return 0, nil, err
		}
		if err := audit(ctx, tx, t, "APPROVAL_DECISION_RECORDED", "approval_request", id, d); err != nil {
			return 0, nil, err
		}
		if err := h.approvalEvent(ctx, tx, view, "business.approval.decided.v1"); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"approval": view, "version": version + 1}, nil
	})
}

type approvalLifecycleCommand struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

func (h *Handler) cancelApproval(w http.ResponseWriter, r *http.Request) {
	h.approvalLifecycle(w, r, false)
}
func (h *Handler) submitApproval(w http.ResponseWriter, r *http.Request) {
	h.approvalLifecycle(w, r, true)
}
func (h *Handler) approvalLifecycle(w http.ResponseWriter, r *http.Request, submit bool) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid approval identifier."))
		return
	}
	var in approvalLifecycleCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || len(in.Reason) < 3 || len(in.Reason) > 2000 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A current version and reason are required."))
		return
	}
	_, target, err := h.approvalTarget(r, id, "approvals:create", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	operation := "approval:cancel:"
	if submit {
		operation = "approval:submit:"
	}
	h.execute(w, r, operation+id.String(), in, "approvals:create", target.Scope.Kind, target.Scope.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "approvals:create", target.Scope.Kind, target.Scope.ID, true)
		if err != nil {
			return 0, nil, err
		}
		request, version, _, err := loadApproval(ctx, tx, t.Organisation, id, true)
		if err != nil {
			return 0, nil, err
		}
		view := request.Snapshot()
		if version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Approval version has changed.")
		}
		if view.RequesterID != t.Account {
			return 0, nil, apperrors.PermissionDenied("Only the requester may submit or cancel this request.")
		}
		if submit {
			if view.State != "DRAFT" || !h.Auth.Service.Now().Before(view.Deadline) {
				return 0, nil, apperrors.Conflict("APPROVAL_STATE_CONFLICT", "The draft is not available for submission.")
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT active FROM approval_policies WHERE organisation_id=$1 AND id=$2`, t.Organisation, view.Policy.ID).Scan(&active); err != nil {
				return 0, nil, err
			}
			if !active {
				return 0, nil, apperrors.Conflict("POLICY_INACTIVE", "The governing policy has been deactivated.")
			}
			view.State = "PENDING"
		} else {
			if err := request.Cancel(t.Account, in.Reason, h.Auth.Service.Now().UTC()); err != nil {
				return 0, nil, apperrors.Conflict("APPROVAL_STATE_CONFLICT", "Approval cannot be cancelled.")
			}
			view = request.Snapshot()
			d := view.Decisions[len(view.Decisions)-1]
			if _, err := tx.Exec(ctx, `INSERT INTO approval_decisions(id,organisation_id,request_id,level,actor_id,outcome,reason,evidence_ids,idempotency_key,created_at) VALUES($1,$2,$3,$4,$5,'CANCELLED',$6,'{}',$7,$8)`, d.ID, t.Organisation, id, d.Level, t.Account, d.Reason, r.Header.Get("Idempotency-Key"), d.At); err != nil {
				return 0, nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE approval_requests SET state=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, view.State); err != nil {
			return 0, nil, err
		}
		if err := audit(ctx, tx, t, "APPROVAL_LIFECYCLE_CHANGED", "approval_request", id, map[string]any{"state": view.State, "reason": in.Reason}); err != nil {
			return 0, nil, err
		}
		eventType := "business.approval.cancelled.v1"
		if submit {
			eventType = "business.approval.requested.v1"
		}
		if err := h.approvalEvent(ctx, tx, view, eventType); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"approval": view, "version": version + 1}, nil
	})
}
func (h *Handler) approvalEvent(ctx context.Context, tx pgx.Tx, view approvals.View, typ string) error {
	event, err := events.New(ctx, platform.BusinessOperationsService, typ, "approval_request", view.ID, map[string]any{"approval_id": view.ID, "organisation_id": view.Scope.Organisation, "requester_id": view.RequesterID, "state": view.State, "level": view.Level, "policy_id": view.Policy.ID, "policy_version": view.Policy.Version})
	if err != nil {
		return err
	}
	if err = h.Auth.Service.Outbox.Add(ctx, tx, event); err != nil {
		return err
	}
	if view.State == "APPROVED" || view.State == "REJECTED" || view.State == "CANCELLED" {
		completed, err := events.New(ctx, platform.BusinessOperationsService, "business.approval.completed.v1", "approval_request", view.ID, map[string]any{"approval_id": view.ID, "organisation_id": view.Scope.Organisation, "state": view.State, "action": view.Policy.Action, "policy_id": view.Policy.ID, "policy_version": view.Policy.Version})
		if err != nil {
			return err
		}
		if err = h.Auth.Service.Outbox.Add(ctx, tx, completed); err != nil {
			return err
		}
	}
	if err = h.queueNotice(ctx, tx, view.RequesterID, "Approval update", "Approval "+view.ID.String()+" is "+string(view.State)+".", false); err != nil {
		return err
	}
	if view.State != "PENDING" || view.Level >= len(view.Policy.Levels) {
		return nil
	}
	level := view.Policy.Levels[view.Level]
	var amount *int64
	var currency string
	if err := tx.QueryRow(ctx, `SELECT amount_minor,coalesce(currency,'') FROM approval_requests WHERE organisation_id=$1 AND id=$2`, view.Scope.Organisation, view.ID).Scan(&amount, &currency); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT m.id FROM staff_memberships m JOIN role_permissions p ON p.role_id=m.role_id WHERE m.organisation_id=$1 AND m.status='ACTIVE' AND p.permission=$2 AND m.account_id<>$3 ORDER BY m.id`, view.Scope.Organisation, string(level.Capability), view.RequesterID)
	if err != nil {
		return err
	}
	members := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		members = append(members, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	target, err := resolve(ctx, tx, view.Scope.Organisation, view.Scope.Kind, view.Scope.ID)
	if err != nil {
		return err
	}
	for _, id := range members {
		member, err := directMember(ctx, tx, view.Scope.Organisation, id)
		if err != nil {
			return err
		}
		for i := range member.Grants {
			member.Grants[i].RequireMFA = false
		}
		if workforce.Authorize(member, workforce.DecisionInput{AccountID: member.AccountID, Target: target, Capability: level.Capability, Now: h.Auth.Service.Now().UTC(), AmountMinor: amount, Currency: currency}) != nil {
			continue
		}
		if err = h.queueNotice(ctx, tx, member.AccountID, "Approval required", "A scoped approval request awaits your decision: "+view.ID.String()+".", false); err != nil {
			return err
		}
	}
	return nil
}

// requirePrivilegeChange permits initial representative setup only before the
// first activation, with recent MFA and immutable audit. After activation an
// approved, unexpired, unused authorization must bind the exact operation.
func (h *Handler) requirePrivilegeChange(ctx context.Context, tx pgx.Tx, t tenant, action string, target workforce.Scope, approvalID ids.ID, operation any) error {
	return h.requirePrivilegeChangeBound(ctx, tx, t, action, target, approvalID, operation, "", nil, "", false)
}

// Commercial amounts and channels come from owning records/calculations. An
// approval request cannot select a weaker threshold by omitting its exposure.
func (h *Handler) requireCommercialApproval(ctx context.Context, tx pgx.Tx, t tenant, action string, target workforce.Scope, approvalID ids.ID, operation any, channel string, amount *int64, currency string) error {
	return h.requirePrivilegeChangeBound(ctx, tx, t, action, target, approvalID, operation, channel, amount, currency, true)
}
func (h *Handler) requirePrivilegeChangeBound(ctx context.Context, tx pgx.Tx, t tenant, action string, target workforce.Scope, approvalID ids.ID, operation any, expectedChannel string, expectedAmount *int64, expectedCurrency string, commercial bool) error {
	now := h.Auth.Service.Now().UTC()
	if !workforce.RecentMFA(t.MFAAt, now, 5*time.Minute) {
		return apperrors.PermissionDenied("Recent MFA is required.")
	}
	var activated *time.Time
	if err := tx.QueryRow(ctx, `SELECT activated_at FROM organisations WHERE id=$1`, t.Organisation).Scan(&activated); err != nil {
		return err
	}
	if action != approvals.ActionInventoryRestructure && action != approvals.ActionInventoryMapping && action != approvals.ActionExceptionalRateChange && action != approvals.ActionVerifiedPropertyPublication && !strings.HasPrefix(action, "COMMERCIAL_") && activated == nil && (t.State == "DRAFT" || t.State == "KYB_PENDING") && t.Account == t.Representative {
		var governed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM approval_policies WHERE organisation_id=$1 AND action=$2)`, t.Organisation, action).Scan(&governed); err != nil {
			return err
		}
		// Creating a governing policy seals this action even if that policy is
		// later deactivated; toggling policy state must never reopen bootstrap.
		if !governed {
			return audit(ctx, tx, t, "ONBOARDING_PRIVILEGE_CONFIGURATION", "organisation", t.Organisation, map[string]any{"action": action, "scope": target})
		}
	}
	if approvalID.ValidateV7() != nil {
		return apperrors.PermissionDenied("A completed independent approval is required.")
	}
	digest, err := operationDigest(action, operation)
	if err != nil {
		return err
	}
	var approvedAction, kind, storedDigest, state, channel string
	var requester, scope ids.ID
	var deadline time.Time
	var consumed *time.Time
	var amount *int64
	var currency string
	var policyRaw []byte
	err = tx.QueryRow(ctx, `SELECT action,scope_kind,scope_id,requester_id,operation_digest,state,deadline,consumed_at,channel,amount_minor,coalesce(currency,''),policy_snapshot FROM approval_requests WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, approvalID).Scan(&approvedAction, &kind, &scope, &requester, &storedDigest, &state, &deadline, &consumed, &channel, &amount, &currency, &policyRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.PermissionDenied("Approval authorization denied.")
	}
	if err != nil {
		return err
	}
	if channel != expectedChannel || approvedAction != action || requester != t.Account || kind != target.Kind || scope != target.ID || storedDigest != digest || state != "APPROVED" || consumed != nil || !now.Before(deadline) {
		return apperrors.PermissionDenied("Approval authorization does not match this operation or has expired.")
	}
	if commercial {
		if (amount == nil) != (expectedAmount == nil) || amount != nil && *amount != *expectedAmount || currency != expectedCurrency {
			return apperrors.PermissionDenied("Approval amount/currency must match server-derived commercial exposure.")
		}
		var policy approvals.Policy
		if err = json.Unmarshal(policyRaw, &policy); err != nil {
			return err
		}
		required := commercialApprovalCapability(action)
		found := false
		for _, level := range policy.Levels {
			if string(level.Capability) == required && level.RequireMFA {
				found = true
			}
		}
		if required == "" || !found || !policy.SeparationOfDuties || !policy.DistinctAcrossLevels {
			return apperrors.PermissionDenied("Independent commercial approval capability and MFA are required in the frozen policy.")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE approval_requests SET consumed_at=$3,consumed_by_account_id=$4 WHERE organisation_id=$1 AND id=$2`, t.Organisation, approvalID, now, t.Account); err != nil {
		return err
	}
	return audit(ctx, tx, t, "APPROVAL_AUTHORIZATION_CONSUMED", "approval_request", approvalID, map[string]any{"action": action, "operation_digest": digest})
}

// Decisions record both the human account and any direct delegating authority.
// A requester cannot appoint a proxy to self-approve, and two proxies cannot
// satisfy independent requirements using the same underlying authority.
func (h *Handler) decisionAuthorities(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target, v approvals.View) ([]ids.ID, error) {
	if v.Level < 0 || v.Level >= len(v.Policy.Levels) {
		return nil, apperrors.Conflict("APPROVAL_STATE_CONFLICT", "No pending approval level.")
	}
	var amount *int64
	var currency string
	if err := tx.QueryRow(ctx, `SELECT amount_minor,coalesce(currency,'') FROM approval_requests WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID).Scan(&amount, &currency); err != nil {
		return nil, err
	}
	used := map[ids.ID]bool{v.RequesterID: true}
	rows, err := tx.Query(ctx, `SELECT actor_id,authority_account_ids FROM approval_decisions WHERE organisation_id=$1 AND request_id=$2 AND outcome='APPROVED'`, t.Organisation, v.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var actor ids.ID
		var roots []ids.ID
		if err := rows.Scan(&actor, &roots); err != nil {
			rows.Close()
			return nil, err
		}
		used[actor] = true
		for _, id := range roots {
			used[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if used[t.Account] {
		return nil, apperrors.PermissionDenied("Independent approval authority required.")
	}
	roots := map[ids.ID]bool{t.Account: true}
	for _, cap := range []permissions.Permission{"approvals:decide", v.Policy.Levels[v.Level].Capability} {
		input := workforce.DecisionInput{AccountID: t.Account, Target: target, Capability: cap, Now: h.Auth.Service.Now().UTC(), MFAAt: t.MFAAt, StepUpWindow: 5 * time.Minute, AmountMinor: amount, Currency: currency}
		if workforce.Authorize(t.Direct, input) == nil {
			continue
		}
		found := false
		for _, d := range t.Delegated {
			if d.Grant.Capability != cap || used[d.Source] {
				continue
			}
			m := t.Direct
			m.Grants = append(append([]workforce.Grant(nil), t.Direct.Grants...), d.Grant)
			if workforce.Authorize(m, input) == nil {
				roots[d.Source] = true
				found = true
				break
			}
		}
		if !found {
			return nil, apperrors.PermissionDenied("Delegated approval would violate separation of duties or current authority.")
		}
	}
	result := []ids.ID{}
	for id := range roots {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result, nil
}
