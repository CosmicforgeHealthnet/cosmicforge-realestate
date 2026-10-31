package businesshttp

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"strings"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type InventoryImportRow struct {
	PortfolioKeys   []string        `json:"portfolio_keys,omitempty"`
	SourceKey       string          `json:"source_key"`
	Kind            portfolio.Kind  `json:"kind"`
	ParentKey       string          `json:"parent_key,omitempty"`
	RoomTypeKey     string          `json:"room_type_key,omitempty"`
	ExternalID      string          `json:"external_id,omitempty"`
	CanonicalID     ids.ID          `json:"canonical_id,omitempty"`
	ExpectedVersion int64           `json:"expected_version,omitempty"`
	Create          InventoryCreate `json:"create"`
}
type InventoryImportInput struct {
	BranchID ids.ID               `json:"branch_id"`
	Provider string               `json:"provider"`
	Format   string               `json:"format"`
	Records  []InventoryImportRow `json:"records,omitempty"`
	CSV      string               `json:"csv,omitempty"`
}
type ImportControl struct {
	Version      int64  `json:"version"`
	AllowPartial bool   `json:"allow_partial"`
	Reason       string `json:"reason"`
}
type importJob struct {
	id, org, actor, member, session, branch ids.ID
	provider, status                        string
	checkpoint, count                       int
	version                                 int64
	allowPartial                            bool
}

func (h *Handler) registerImports(r chi.Router) {
	r.Post("/imports", h.importCreate)
	r.Get("/imports", h.importList)
	r.Get("/imports/{id}", h.importGet)
	r.Get("/imports/{id}/preview", h.importGet)
	r.Get("/imports/{id}/records", h.importRecords)
	for _, action := range []string{"commit", "pause", "resume", "cancel"} {
		r.Post("/imports/{id}/"+action, func(w http.ResponseWriter, r *http.Request) { h.importControl(w, r, action) })
	}
	r.Get("/imports/{id}/recovery-plan", h.importRecoveryPlan)
	r.Post("/imports/{id}/rollback", h.importRollback)
}
func parseImport(in InventoryImportInput) ([]InventoryImportRow, error) {
	if in.BranchID.ValidateV7() != nil || !inventoryProvider(in.Provider) {
		return nil, apperrors.InvalidRequest("A valid branch and source provider are required.")
	}
	rows := in.Records
	switch in.Format {
	case "JSON":
		if in.CSV != "" {
			return nil, apperrors.InvalidRequest("Choose one bounded import format.")
		}
	case "CSV":
		if len(in.Records) > 0 || len(in.CSV) > 1<<20 {
			return nil, apperrors.InvalidRequest("CSV exceeds the bounded input contract.")
		}
		reader := csv.NewReader(strings.NewReader(in.CSV))
		header, err := reader.Read()
		if err != nil {
			return nil, apperrors.InvalidRequest("Invalid CSV header.")
		}
		allowed := map[string]bool{}
		for _, key := range []string{"source_key", "kind", "code", "name", "parent_key", "room_type_key", "external_id", "portfolio_keys", "parent_id", "canonical_id", "classification", "timezone", "currency"} {
			allowed[key] = true
		}
		seen := map[string]bool{}
		for _, key := range header {
			if !allowed[key] || seen[key] {
				return nil, apperrors.InvalidRequest("Unknown or repeated CSV column.")
			}
			seen[key] = true
		}
		for _, key := range []string{"source_key", "kind", "code", "name"} {
			if !seen[key] {
				return nil, apperrors.InvalidRequest("Required CSV columns are missing.")
			}
		}
		for {
			record, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil || len(rows) >= 1000 {
				return nil, apperrors.InvalidRequest("Invalid CSV record or excessive row count.")
			}
			data := map[string]string{}
			for i, key := range header {
				data[key] = record[i]
			}
			row := InventoryImportRow{SourceKey: data["source_key"], Kind: portfolio.Kind(data["kind"]), ParentKey: data["parent_key"], RoomTypeKey: data["room_type_key"], ExternalID: data["external_id"], Create: InventoryCreate{Code: data["code"], Name: data["name"], Timezone: data["timezone"], Currency: data["currency"], Attributes: portfolio.Attributes{Classification: data["classification"]}}}
			if data["parent_id"] != "" {
				row.Create.ParentID, err = ids.ParseV7(data["parent_id"])
				if err != nil {
					return nil, apperrors.InvalidRequest("Invalid canonical parent in CSV.")
				}
			}
			if data["canonical_id"] != "" {
				row.CanonicalID, err = ids.ParseV7(data["canonical_id"])
				if err != nil {
					return nil, apperrors.InvalidRequest("Invalid canonical ID in CSV.")
				}
			}
			if data["portfolio_keys"] != "" {
				row.PortfolioKeys = strings.Split(data["portfolio_keys"], "|")
			}
			rows = append(rows, row)
		}
	default:
		return nil, apperrors.InvalidRequest("Use JSON or CSV, without file URLs or arbitrary decoders.")
	}
	if len(rows) < 1 || len(rows) > 1000 {
		return nil, apperrors.InvalidRequest("Imports require one to one thousand records.")
	}
	seen := map[string]bool{}
	for i := range rows {
		row := &rows[i]
		if len(row.PortfolioKeys) > 20 || row.SourceKey == "" || len(row.SourceKey) > 200 || seen[row.SourceKey] || len(row.ParentKey) > 200 || len(row.RoomTypeKey) > 200 || len(row.ExternalID) > 200 {
			return nil, apperrors.InvalidRequest("Source keys must be unique, stable and bounded.")
		}
		seen[row.SourceKey] = true
		if row.ExternalID == "" {
			row.ExternalID = row.SourceKey
		}
		if row.Create.BranchID.IsZero() {
			row.Create.BranchID = in.BranchID
		}
		if row.Create.BranchID != in.BranchID {
			return nil, apperrors.PermissionDenied("Every row must stay inside its declared operational branch.")
		}
	}
	return rows, nil
}

// Topological order is deterministic and includes room-category dependencies.
// Cyclic/unknown dependencies remain rows for asynchronous validation reports.
func importOrder(rows []InventoryImportRow) []int {
	keys := map[string]int{}
	for i, row := range rows {
		keys[row.SourceKey] = i
	}
	visited := map[int]int{}
	order := []int{}
	var visit func(int)
	visit = func(i int) {
		if visited[i] != 0 {
			return
		}
		visited[i] = 1
		dependencies := append([]string{rows[i].ParentKey, rows[i].RoomTypeKey}, rows[i].PortfolioKeys...)
		for _, key := range dependencies {
			if p, ok := keys[key]; ok && key != "" {
				visit(p)
			}
		}
		visited[i] = 2
		order = append(order, i)
	}
	for i := range rows {
		visit(i)
	}
	return order
}
func (h *Handler) importCreate(w http.ResponseWriter, r *http.Request) {
	var in InventoryImportInput
	if err := httpx.DecodeJSON(w, r, &in, 1<<20); err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	records, err := parseImport(in)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	order := importOrder(records)
	// Fingerprint is based on parsed structured records. No uploaded bytes/URL
	// or raw CSV is retained in the service database or idempotency response.
	payload := map[string]any{"branch_id": in.BranchID, "provider": in.Provider, "records": records, "format": in.Format}
	h.execute(w, r, "inventory:import:create", payload, "imports:write", "BRANCH", in.BranchID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "imports:write", "BRANCH", in.BranchID, true)
		if err != nil {
			return 0, nil, err
		}
		principal, ok := businessauth.PrincipalFrom(ctx)
		if !ok {
			return 0, nil, apperrors.AuthenticationRequired()
		}
		id := ids.New()
		if _, err = tx.Exec(ctx, `INSERT INTO inventory_import_jobs(id,organisation_id,actor_id,membership_id,session_id,branch_id,provider,format,record_count) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, t.Organisation, t.Account, t.Member, principal.SessionID, in.BranchID, in.Provider, in.Format, len(records)); err != nil {
			return 0, nil, err
		}
		for position, index := range order {
			row := records[index]
			raw, e := json.Marshal(row)
			if e != nil {
				return 0, nil, e
			}
			node := row.CanonicalID
			if node.IsZero() {
				node = ids.New()
			}
			if _, err = tx.Exec(ctx, `INSERT INTO inventory_import_records(job_id,row_number,source_row_number,source_key,node_id,command,parent_key) VALUES($1,$2,$3,$4,$5,$6,nullif($7,''))`, id, position+1, index+1, row.SourceKey, node, raw, row.ParentKey); err != nil {
				return 0, nil, err
			}
		}
		if err = h.accessEvent(ctx, tx, t, "INVENTORY_IMPORT_QUEUED", "business.inventory.import.changed.v1", "inventory_import", id, map[string]any{"status": "QUEUED", "record_count": len(records), "provider": in.Provider}); err != nil {
			return 0, nil, err
		}
		return 202, map[string]any{"import_id": id, "status": "QUEUED", "record_count": len(records), "version": 1}, nil
	})
}
func loadImport(ctx context.Context, q database.Querier, org, id ids.ID, lock bool) (importJob, error) {
	var j importJob
	query := `SELECT id,organisation_id,actor_id,membership_id,session_id,branch_id,provider,status,checkpoint,record_count,version,allow_partial FROM inventory_import_jobs WHERE organisation_id=$1 AND id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, query, org, id).Scan(&j.id, &j.org, &j.actor, &j.member, &j.session, &j.branch, &j.provider, &j.status, &j.checkpoint, &j.count, &j.version, &j.allowPartial)
	if errors.Is(err, pgx.ErrNoRows) {
		err = apperrors.NotFound("IMPORT_NOT_FOUND", "Import job not found.")
	}
	return j, err
}
func (h *Handler) importTarget(r *http.Request, capability string, mfa bool) (tenant, importJob, error) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		return t, importJob{}, err
	}
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		return t, importJob{}, apperrors.InvalidRequest("Invalid import ID.")
	}
	j, err := loadImport(r.Context(), h.Auth.Service.Pool, t.Organisation, id, false)
	if err != nil {
		return t, j, err
	}
	t, _, err = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "BRANCH", j.branch, mfa)
	return t, j, err
}
func importSummary(ctx context.Context, q database.Querier, j importJob) (json.RawMessage, error) {
	return oneJSON(ctx, q, `SELECT jsonb_build_object('import_id',id,'provider',provider,'format',format,'branch_id',branch_id,'status',status,'record_count',record_count,'checkpoint',checkpoint,'version',version,'last_error',last_error,'created_at',created_at,'updated_at',updated_at,'outcomes',(SELECT coalesce(jsonb_object_agg(s.status,s.cnt),'{}') FROM (SELECT status,count(*) cnt FROM inventory_import_records WHERE job_id=$2 GROUP BY status)s)) FROM inventory_import_jobs WHERE organisation_id=$1 AND id=$2`, j.org, j.id)
}
func (h *Handler) importGet(w http.ResponseWriter, r *http.Request) {
	_, j, err := h.importTarget(r, "imports:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	out, err := importSummary(r.Context(), h.Auth.Service.Pool, j)
	reply(w, r, 200, out, err)
}
func (h *Handler) importList(w http.ResponseWriter, r *http.Request) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT id,branch_id FROM inventory_import_jobs WHERE organisation_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, t.Organisation, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	refs := []importJob{}
	for rows.Next() {
		j := importJob{org: t.Organisation}
		if err = rows.Scan(&j.id, &j.branch); err != nil {
			break
		}
		refs = append(refs, j)
	}
	e := rows.Err()
	rows.Close()
	if err == nil {
		err = e
	}
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	items := []json.RawMessage{}
	for _, j := range refs {
		if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "imports:read", "BRANCH", j.branch, false); e != nil {
			continue
		}
		summary, e := importSummary(r.Context(), h.Auth.Service.Pool, j)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		items = append(items, summary)
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, nil)
}
func (h *Handler) importRecords(w http.ResponseWriter, r *http.Request) {
	_, j, err := h.importTarget(r, "imports:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT jsonb_build_object('row_number',source_row_number,'source_key',source_key,'node_id',node_id,'action',action,'status',status,'kind',command->>'kind','code',command#>>'{create,code}','parent_key',command->>'parent_key','parent_id',command#>'{create,parent_id}','portfolio_ids',command#>'{create,portfolio_ids}','validation_errors',validation_errors,'error_code',error_code,'applied_version',applied_version) FROM inventory_import_records WHERE job_id=$1 ORDER BY row_number LIMIT $2 OFFSET $3`, j.id, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		items = append(items, raw)
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, rows.Err())
}
func (h *Handler) importControl(w http.ResponseWriter, r *http.Request, action string) {
	var in ImportControl
	if !decode(w, r, &in) {
		return
	}
	_, j, err := h.importTarget(r, "imports:write", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current job version and reason required."))
		return
	}
	h.execute(w, r, "inventory:import:"+action+":"+j.id.String(), in, "imports:write", "BRANCH", j.branch, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "imports:write", "BRANCH", j.branch, true)
		if err != nil {
			return 0, nil, err
		}
		current, err := loadImport(ctx, tx, t.Organisation, j.id, true)
		if err != nil {
			return 0, nil, err
		}
		if current.version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Import job version changed.")
		}
		next := ""
		var execute bool
		if err = tx.QueryRow(ctx, `SELECT execute_requested FROM inventory_import_jobs WHERE id=$1`, j.id).Scan(&execute); err != nil {
			return 0, nil, err
		}
		partial := current.allowPartial
		if action == "commit" {
			partial = in.AllowPartial
		} else if action == "resume" {
			partial = partial || in.AllowPartial
		}
		switch action {
		case "commit":
			if current.status == "PREVIEW_READY" {
				var invalid int
				if err = tx.QueryRow(ctx, `SELECT count(*) FROM inventory_import_records WHERE job_id=$1 AND status='INVALID'`, j.id).Scan(&invalid); err != nil {
					return 0, nil, err
				}
				if invalid > 0 && !in.AllowPartial {
					return 0, nil, apperrors.Conflict("IMPORT_VALIDATION_ERRORS", "Explicit partial execution is required for invalid rows.")
				}
				next = "RUNNING"
				execute = true
			}
		case "pause":
			if current.status == "RUNNING" || current.status == "VALIDATING" || current.status == "QUEUED" {
				next = "PAUSED"
			}
		case "resume":
			if current.status == "PAUSED" {
				var quarantined int
				if err = tx.QueryRow(ctx, `SELECT count(*) FROM inventory_import_records WHERE job_id=$1 AND status='QUARANTINED'`, j.id).Scan(&quarantined); err != nil {
					return 0, nil, err
				}
				if quarantined > 0 && !partial {
					return 0, nil, apperrors.Conflict("IMPORT_PARTIAL_REVIEW_REQUIRED", "Review quarantined rows and explicitly permit partial execution before resuming.")
				}
				if err = tx.QueryRow(ctx, `SELECT execute_requested FROM inventory_import_jobs WHERE id=$1`, j.id).Scan(&execute); err != nil {
					return 0, nil, err
				}
				if execute {
					next = "RUNNING"
				} else {
					next = "VALIDATING"
				}
			}
		case "cancel":
			if current.status != "COMPLETED" && current.status != "COMPLETED_WITH_ERRORS" && current.status != "CANCELLED" {
				next = "CANCELLED"
			}
		}
		if next == "" {
			return 0, nil, apperrors.Conflict("IMPORT_STATE_CONFLICT", "Import cannot perform this lifecycle operation.")
		}
		principal, _ := businessauth.PrincipalFrom(ctx)
		if _, err = tx.Exec(ctx, `UPDATE inventory_import_jobs SET status=$3,execute_requested=$4,allow_partial=$5,actor_id=$6,membership_id=$7,session_id=$8,lease_owner=NULL,lease_expires_at=NULL,version=version+1,last_error='',updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, j.id, next, execute, partial, t.Account, t.Member, principal.SessionID); err != nil {
			return 0, nil, err
		}
		if err = h.accessEvent(ctx, tx, t, "INVENTORY_IMPORT_"+strings.ToUpper(action), "business.inventory.import.changed.v1", "inventory_import", j.id, map[string]any{"status": next, "reason": in.Reason}); err != nil {
			return 0, nil, err
		}
		summary, err := importSummary(ctx, tx, j)
		return 200, summary, err
	})
}
