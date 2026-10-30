package businesshttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Identifier fragments below come only from this closed server-owned catalogue.
// Client input is always passed as a PostgreSQL parameter.
type hierarchyKind struct{ path, table, kind, parentKind, parentColumn string }

var hierarchyKinds = []hierarchyKind{{"regions", "regions", "REGION", "COMPANY", ""}, {"branches", "branches", "BRANCH", "REGION", "region_id"}, {"departments", "departments", "DEPARTMENT", "BRANCH", "branch_id"}, {"teams", "teams", "TEAM", "DEPARTMENT", "department_id"}}

func (h *Handler) registerHierarchy(r chi.Router) {
	for _, kind := range hierarchyKinds {
		r.Get("/"+kind.path, func(w http.ResponseWriter, r *http.Request) { h.listHierarchy(w, r, kind) })
		r.Post("/"+kind.path, func(w http.ResponseWriter, r *http.Request) { h.writeHierarchy(w, r, kind, false) })
		r.Get("/"+kind.path+"/{id}", func(w http.ResponseWriter, r *http.Request) { h.getHierarchy(w, r, kind) })
		r.Patch("/"+kind.path+"/{id}", func(w http.ResponseWriter, r *http.Request) { h.writeHierarchy(w, r, kind, true) })
	}
}

type hierarchyCommand struct {
	ApprovalID   ids.ID  `json:"approval_id,omitempty"`
	Name         *string `json:"name,omitempty"`
	RegionID     *ids.ID `json:"region_id,omitempty"`
	BranchID     *ids.ID `json:"branch_id,omitempty"`
	DepartmentID *ids.ID `json:"department_id,omitempty"`
	Timezone     *string `json:"timezone,omitempty"`
	Currency     *string `json:"currency,omitempty"`
	Status       *string `json:"status,omitempty"`
	Version      int64   `json:"version,omitempty"`
}

func pagination(r *http.Request) (int, int, error) {
	limit, offset := 50, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, apperrors.InvalidRequest("Invalid pagination.")
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, apperrors.InvalidRequest("Invalid pagination.")
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return 0, 0, apperrors.InvalidRequest("Invalid pagination.")
	}
	return limit, offset, nil
}
func (h *Handler) listHierarchy(w http.ResponseWriter, r *http.Request, kind hierarchyKind) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if !t.Membership.RoleCapabilities.Has(permissions.Permission("hierarchy:read")) {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Capability denied."))
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "ACTIVE" && status != "SUSPENDED" && status != "ARCHIVED" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid lifecycle filter."))
		return
	}
	query := fmt.Sprintf(`SELECT x.id,to_jsonb(x) FROM (SELECT * FROM %s WHERE organisation_id=$1 AND ($2='' OR status=$2) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4) x`, kind.table)
	rows, err := h.Auth.Service.Pool.Query(r.Context(), query, t.Organisation, status, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	// Release the result connection before permission lookups (works with a
	// single-connection pool and avoids accidental pool starvation).
	type candidate struct {
		id  ids.ID
		raw json.RawMessage
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.raw); err != nil {
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
	items := []json.RawMessage{}
	for _, c := range candidates {
		if _, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "hierarchy:read", kind.kind, c.id, false); err == nil {
			items = append(items, c.raw)
		} else {
			if _, ok := err.(*apperrors.Error); !ok {
				reply(w, r, 0, nil, err)
				return
			}
		}
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "next_offset": offset + len(candidates)}, nil)
}
func (h *Handler) getHierarchy(w http.ResponseWriter, r *http.Request, kind hierarchyKind) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid identifier."))
		return
	}
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "hierarchy:read", kind.kind, id, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	out, err := oneJSON(r.Context(), h.Auth.Service.Pool, fmt.Sprintf(`SELECT to_jsonb(x) FROM (SELECT * FROM %s WHERE organisation_id=$1 AND id=$2) x`, kind.table), t.Organisation, id)
	reply(w, r, 200, out, err)
}
func (h *Handler) writeHierarchy(w http.ResponseWriter, r *http.Request, kind hierarchyKind, update bool) {
	var in hierarchyCommand
	if !decode(w, r, &in) {
		return
	}
	if !update && (in.Version != 0 || in.Status != nil && *in.Status != "ACTIVE") {
		reply(w, r, 0, nil, apperrors.InvalidRequest("New hierarchy records start active at version one."))
		return
	}
	if in.Name != nil && (len(strings.TrimSpace(*in.Name)) < 1 || len(*in.Name) > 200 || strings.ContainsRune(*in.Name, 0)) || !update && in.Name == nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A valid name is required."))
		return
	}
	if in.Timezone != nil {
		if _, err := time.LoadLocation(*in.Timezone); err != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid timezone."))
			return
		}
	}
	if in.Currency != nil {
		if _, err := money.ParseCurrency(*in.Currency); err != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid currency."))
			return
		}
	}
	if in.Status != nil && *in.Status != "ACTIVE" && *in.Status != "ARCHIVED" && !(kind.kind == "BRANCH" && *in.Status == "SUSPENDED") {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid lifecycle status."))
		return
	}
	if kind.kind != "BRANCH" && (in.Timezone != nil || in.Currency != nil || in.RegionID != nil) || kind.kind != "DEPARTMENT" && in.BranchID != nil || kind.kind != "TEAM" && in.DepartmentID != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Fields do not match this hierarchy type."))
		return
	}
	var parent *ids.ID
	switch kind.kind {
	case "BRANCH":
		parent = in.RegionID
	case "DEPARTMENT":
		parent = in.BranchID
	case "TEAM":
		parent = in.DepartmentID
	}
	if parent != nil && parent.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid parent identifier."))
		return
	}
	if !update && (kind.kind == "DEPARTMENT" || kind.kind == "TEAM") && parent == nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A parent is required."))
		return
	}
	id := ids.New()
	if update {
		var err error
		id, err = ids.ParseV7(chi.URLParam(r, "id"))
		if err != nil || in.Version < 1 {
			reply(w, r, 0, nil, apperrors.InvalidRequest("A valid identifier and version are required."))
			return
		}
	}
	var result json.RawMessage
	run := func(ctx context.Context, tx pgx.Tx) error {
		var t tenant
		var err error
		if update {
			t, _, err = h.authorize(ctx, tx, "hierarchy:write", kind.kind, id, in.Status != nil || parent != nil)
		} else {
			targetKind, targetID := "COMPANY", ids.Nil
			if parent != nil {
				targetKind = kind.parentKind
				targetID = *parent
			}
			t, _, err = h.authorize(ctx, tx, "hierarchy:write", targetKind, targetID, false)
		}
		if err != nil {
			return err
		}
		if parent != nil {
			if _, _, err := h.authorize(ctx, tx, "hierarchy:write", kind.parentKind, *parent, update); err != nil {
				return err
			}
		}
		if update && parent != nil {
			operation := map[string]any{"resource": "hierarchy_parent", "kind": kind.kind, "id": id, "changes": in}
			if err := h.requirePrivilegeChange(ctx, tx, t, "STAFF_SCOPE_CHANGE", workforce.Scope{Organisation: t.Organisation, Kind: kind.kind, ID: id}, in.ApprovalID, operation); err != nil {
				return err
			}
		}
		// Lifecycle changes require authority over the parent as well as the child.
		if update && in.Status != nil {
			if _, _, err := h.authorize(ctx, tx, "hierarchy:write", "COMPANY", ids.Nil, true); err != nil {
				return err
			}
		}
		if !update {
			switch kind.kind {
			case "REGION":
				_, err = tx.Exec(ctx, `INSERT INTO regions(id,organisation_id,name) VALUES($1,$2,$3)`, id, t.Organisation, *in.Name)
			case "BRANCH":
				_, err = tx.Exec(ctx, `INSERT INTO branches(id,organisation_id,region_id,name,timezone,currency) SELECT $1,id,$2,$3,coalesce($4,timezone),coalesce($5,currency) FROM organisations WHERE id=$6`, id, parent, *in.Name, in.Timezone, in.Currency, t.Organisation)
			case "DEPARTMENT":
				_, err = tx.Exec(ctx, `INSERT INTO departments(id,organisation_id,branch_id,name) VALUES($1,$2,$3,$4)`, id, t.Organisation, *parent, *in.Name)
			case "TEAM":
				_, err = tx.Exec(ctx, `INSERT INTO teams(id,organisation_id,department_id,name) VALUES($1,$2,$3,$4)`, id, t.Organisation, *parent, *in.Name)
			}
		} else {
			var fields string
			args := []any{t.Organisation, id, in.Version, in.Name, in.Status}
			switch kind.kind {
			case "REGION":
				fields = `name=coalesce($4,name),status=coalesce($5,status)`
			case "BRANCH":
				fields = `name=coalesce($4,name),status=coalesce($5,status),region_id=coalesce($6,region_id),timezone=coalesce($7,timezone),currency=coalesce($8,currency)`
				args = append(args, parent, in.Timezone, in.Currency)
			case "DEPARTMENT", "TEAM":
				fields = fmt.Sprintf(`name=coalesce($4,name),status=coalesce($5,status),%s=coalesce($6,%s)`, kind.parentColumn, kind.parentColumn)
				args = append(args, parent)
			}
			query := fmt.Sprintf(`UPDATE %s SET %s,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND version=$3`, kind.table, fields)
			tag, updateErr := tx.Exec(ctx, query, args...)
			err = updateErr
			if err == nil && tag.RowsAffected() != 1 {
				return apperrors.Conflict("VERSION_CONFLICT", "Hierarchy version has changed.")
			}
		}
		if err != nil {
			return err
		}
		if err := audit(ctx, tx, t, "HIERARCHY_UPDATED", strings.ToLower(kind.kind), id, in); err != nil {
			return err
		}
		result, err = oneJSON(ctx, tx, fmt.Sprintf(`SELECT to_jsonb(x) FROM (SELECT * FROM %s WHERE organisation_id=$1 AND id=$2) x`, kind.table), t.Organisation, id)
		return err
	}
	status := 201
	if update {
		status = 200
	}
	targetKind, targetID := kind.kind, id
	if !update {
		targetKind, targetID = "COMPANY", ids.Nil
		if parent != nil {
			targetKind, targetID = kind.parentKind, *parent
		}
	}
	h.execute(w, r, "hierarchy:"+kind.path+":"+chi.URLParam(r, "id"), in, "hierarchy:write", targetKind, targetID, update && (in.Status != nil || parent != nil), func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		err := run(ctx, tx)
		return status, result, err
	})
}
