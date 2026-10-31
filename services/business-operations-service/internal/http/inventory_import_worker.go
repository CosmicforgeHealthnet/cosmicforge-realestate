package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type importRecord struct {
	number         int
	key            string
	id             ids.ID
	row            InventoryImportRow
	status, action string
	before         *portfolio.Node
}

func readImportRecords(ctx context.Context, tx pgx.Tx, id ids.ID) ([]*importRecord, error) {
	rows, err := tx.Query(ctx, `SELECT row_number,source_key,node_id,command,status,action,before_snapshot FROM inventory_import_records WHERE job_id=$1 ORDER BY row_number`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []*importRecord{}
	for rows.Next() {
		r := &importRecord{}
		var raw, old []byte
		if err = rows.Scan(&r.number, &r.key, &r.id, &raw, &r.status, &r.action, &old); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &r.row); err != nil {
			return nil, err
		}
		if len(old) > 0 && string(old) != "null" {
			r.before = &portfolio.Node{}
			if err = json.Unmarshal(old, r.before); err != nil {
				return nil, err
			}
		}
		records = append(records, r)
	}
	return records, rows.Err()
}
func importError(code string) error { return apperrors.InvalidRequest(code) }
func importErrorCode(err error) string {
	var app *apperrors.Error
	if errors.As(err, &app) {
		if app.Code == "INVALID_REQUEST" {
			return app.Message
		}
		return app.Code
	}
	if state := database.SQLState(err); state != "" {
		switch state {
		case "23505":
			return "DUPLICATE_IDENTITY"
		case "23503":
			return "INVALID_REFERENCE"
		case "23514":
			return "GRAPH_CONSTRAINT"
		}
	}
	return "PROCESSING_FAILURE"
}
func importRecordByID(records []*importRecord, id ids.ID) *importRecord {
	for _, r := range records {
		if r.id == id {
			return r
		}
	}
	return nil
}
func importPropertyID(ctx context.Context, tx pgx.Tx, org, id ids.ID, records []*importRecord, seen map[ids.ID]bool) (ids.ID, error) {
	if id.IsZero() {
		return ids.Nil, nil
	}
	if seen[id] {
		return ids.Nil, importError("CYCLIC_SOURCE_REFERENCE")
	}
	seen[id] = true
	if r := importRecordByID(records, id); r != nil && r.action == "CREATE" {
		if r.row.Kind == portfolio.Property {
			return id, nil
		}
		return importPropertyID(ctx, tx, org, r.row.Create.ParentID, records, seen)
	}
	var property *ids.ID
	if err := tx.QueryRow(ctx, `SELECT business_graph_property($1,$2)`, org, id).Scan(&property); err != nil {
		return ids.Nil, err
	}
	if property != nil {
		return *property, nil
	}
	return ids.Nil, nil
}
func importNode(ctx context.Context, tx pgx.Tx, org, id ids.ID, records []*importRecord) (portfolio.Node, error) {
	if r := importRecordByID(records, id); r != nil && r.action == "CREATE" {
		if r.status != "VALID" && r.status != "APPLIED" {
			return portfolio.Node{}, importError("PARENT_NOT_VALIDATED")
		}
		c := r.row.Create
		return portfolio.Node{ID: r.id, OrganisationID: org, BranchID: c.BranchID, DepartmentID: c.DepartmentID, TeamID: c.TeamID, Kind: r.row.Kind, Code: c.Code, Name: c.Name, State: "DRAFT", Timezone: c.Timezone, Currency: c.Currency, PolicyCode: c.PolicyCode, PolicyVersion: c.PolicyVersion, CapacityCode: c.CapacityCode, Attributes: c.Attributes}, nil
	}
	return portfolio.Get(ctx, tx, org, id)
}
func (h *Handler) importRowAuthority(ctx context.Context, tx pgx.Tx, t tenant, row InventoryImportRow, records []*importRecord) error {
	cap := portfolio.Capability(row.Kind, true)
	cursor := row
	seen := map[ids.ID]bool{}
	for i := 0; i < 65; i++ {
		if !cursor.Create.ParentID.IsZero() {
			id := cursor.Create.ParentID
			if seen[id] {
				return importError("CYCLIC_SOURCE_REFERENCE")
			}
			seen[id] = true
			if r := importRecordByID(records, id); r != nil && r.action == "CREATE" {
				cursor = r.row
				continue
			}
			n, err := portfolio.Get(ctx, tx, t.Organisation, id)
			if err != nil {
				return err
			}
			_, _, err = h.authorize(ctx, tx, cap, string(n.Kind), n.ID, false)
			return err
		}
		if len(cursor.Create.PortfolioIDs) > 0 {
			group := cursor.Create.PortfolioIDs[0]
			if candidate := importRecordByID(records, group); candidate != nil && candidate.action == "CREATE" {
				if seen[group] {
					return importError("CYCLIC_SOURCE_REFERENCE")
				}
				seen[group] = true
				cursor = candidate.row
				continue
			}
			_, _, err := h.authorize(ctx, tx, cap, "PORTFOLIO", group, false)
			return err
		}
		_, _, err := h.authorize(ctx, tx, cap, "BRANCH", cursor.Create.BranchID, false)
		return err
	}
	return importError("SOURCE_DEPTH_LIMIT")
}
func (h *Handler) previewImportRecord(ctx context.Context, tx pgx.Tx, t tenant, j importJob, r *importRecord, records []*importRecord) error {
	row := r.row
	if !row.Kind.Valid() {
		return importError("UNSUPPORTED_ENTITY_TYPE")
	}
	if row.Create.BranchID != j.branch {
		return importError("BRANCH_MISMATCH")
	}
	resolveKey := func(key string) (ids.ID, error) {
		for _, candidate := range records {
			if candidate.key == key {
				if candidate.number >= r.number || candidate.status != "VALID" && candidate.status != "APPLIED" && candidate.status != "SKIPPED" {
					return ids.Nil, importError("CYCLIC_OR_INVALID_SOURCE_DEPENDENCY")
				}
				return candidate.id, nil
			}
		}
		rows, err := tx.Query(ctx, `SELECT node_id FROM inventory_external_mappings WHERE organisation_id=$1 AND provider=$2 AND external_id=$3 ORDER BY object_type LIMIT 2`, j.org, j.provider, key)
		if err != nil {
			return ids.Nil, err
		}
		defer rows.Close()
		matches := []ids.ID{}
		for rows.Next() {
			var id ids.ID
			if err = rows.Scan(&id); err != nil {
				return ids.Nil, err
			}
			matches = append(matches, id)
		}
		if err = rows.Err(); err != nil {
			return ids.Nil, err
		}
		if len(matches) != 1 {
			return ids.Nil, importError("UNKNOWN_OR_AMBIGUOUS_EXTERNAL_PARENT")
		}
		return matches[0], nil
	}
	if row.ParentKey != "" {
		id, err := resolveKey(row.ParentKey)
		if err != nil {
			return err
		}
		if !row.Create.ParentID.IsZero() && row.Create.ParentID != id {
			return importError("AMBIGUOUS_PARENT")
		}
		row.Create.ParentID = id
	}
	if row.RoomTypeKey != "" {
		id, err := resolveKey(row.RoomTypeKey)
		if err != nil {
			return err
		}
		if row.Create.RoomTypeID != nil && *row.Create.RoomTypeID != id {
			return importError("AMBIGUOUS_ROOM_TYPE")
		}
		row.Create.RoomTypeID = &id
	}
	for _, key := range row.PortfolioKeys {
		id, e := resolveKey(key)
		if e != nil {
			return e
		}
		if !slices.Contains(row.Create.PortfolioIDs, id) {
			row.Create.PortfolioIDs = append(row.Create.PortfolioIDs, id)
		}
	}

	for _, other := range records {
		if other.number < r.number && other.row.Kind == row.Kind && other.row.ExternalID == row.ExternalID && other.status == "VALID" {
			return importError("DUPLICATE_EXTERNAL_SOURCE_OBJECT")
		}
	}

	var mapped ids.ID
	err := tx.QueryRow(ctx, `SELECT node_id FROM inventory_external_mappings WHERE organisation_id=$1 AND provider=$2 AND external_id=$3 AND object_type=$4`, j.org, j.provider, row.ExternalID, string(row.Kind)).Scan(&mapped)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !mapped.IsZero() {
		if !row.CanonicalID.IsZero() && row.CanonicalID != mapped {
			return importError("AMBIGUOUS_EXTERNAL_ID")
		}
		row.CanonicalID = mapped
	}
	if !row.CanonicalID.IsZero() {
		existing, err := portfolio.Get(ctx, tx, j.org, row.CanonicalID)
		if err != nil {
			return importError("CANONICAL_ID_NOT_OWNED")
		}
		if existing.Kind != row.Kind || existing.BranchID != j.branch {
			return importError("CANONICAL_TYPE_OR_BRANCH_CONFLICT")
		}
		if _, _, err = h.authorize(ctx, tx, portfolio.Capability(row.Kind, true), string(existing.Kind), existing.ID, false); err != nil {
			return err
		}
		if row.ExpectedVersion > 0 && row.ExpectedVersion != existing.Version {
			return importError("STALE_CANONICAL_VERSION")
		}
		var parent *ids.ID
		if err = tx.QueryRow(ctx, `SELECT (SELECT parent_id FROM inventory_edges WHERE organisation_id=$1 AND child_id=$2 AND relation='CONTAINS')`, j.org, existing.ID).Scan(&parent); err != nil {
			return err
		}
		if !row.Create.ParentID.IsZero() && (parent == nil || *parent != row.Create.ParentID) {
			return importError("EXISTING_PARENT_CHANGE_REQUIRES_RESTRUCTURE")
		}
		if parent != nil {
			row.Create.ParentID = *parent
		}
		if len(row.Create.PortfolioIDs) > 0 {
			existingGroups := []ids.ID{}
			for _, parent := range existing.Parents {
				if parent.Relation == "MEMBERSHIP" {
					existingGroups = append(existingGroups, parent.ParentID)
				}
			}
			if len(existingGroups) != len(row.Create.PortfolioIDs) {
				return importError("EXISTING_MEMBERSHIP_CHANGE_REQUIRES_REVIEW")
			}
			for _, group := range row.Create.PortfolioIDs {
				if !slices.Contains(existingGroups, group) {
					return importError("EXISTING_MEMBERSHIP_CHANGE_REQUIRES_REVIEW")
				}
			}
		}
		if row.Create.Timezone != "" && row.Create.Timezone != existing.Timezone || row.Create.Currency != "" && row.Create.Currency != existing.Currency || row.Create.PolicyCode != "" && row.Create.PolicyCode != existing.PolicyCode || row.Create.PolicyVersion > 0 && row.Create.PolicyVersion != existing.PolicyVersion || row.Create.CapacityCode != "" && row.Create.CapacityCode != existing.CapacityCode {
			return importError("EXISTING_CONTEXT_CHANGE_REQUIRES_RESTRUCTURE")
		}
		row.ExpectedVersion = existing.Version
		r.id = existing.ID
		r.before = &existing
		r.action = "UPDATE"
		if row.Create.Code == existing.Code && row.Create.Name == existing.Name && reflect.DeepEqual(row.Create.Attributes, existing.Attributes) && (row.Create.RoomTypeID == nil || existing.RoomTypeID != nil && *row.Create.RoomTypeID == *existing.RoomTypeID) {
			r.action = "SKIP"
		} else if existing.State != "DRAFT" || existing.ActivatedAt != nil {
			return importError("ACTIVE_UPDATE_REQUIRES_RESTRUCTURE")
		}
	}
	if r.action == "CREATE" {
		if row.Create.ParentID.IsZero() && !row.Kind.RootAllowed() {
			return importError("PHYSICAL_PARENT_REQUIRED")
		}
		if !row.Create.ParentID.IsZero() {
			parent, err := importNode(ctx, tx, j.org, row.Create.ParentID, records)
			if err != nil {
				return err
			}
			if !portfolio.LegalEdge(parent.Kind, row.Kind, "CONTAINS") || parent.BranchID != j.branch || parent.State == "ARCHIVED" || parent.State == "RETIRED" {
				return importError("INVALID_TYPED_PARENT")
			}
			if row.Create.Timezone != "" && row.Create.Timezone != parent.Timezone || row.Create.Currency != "" && row.Create.Currency != parent.Currency {
				return importError("INCONSISTENT_PHYSICAL_CONTEXT")
			}
			row.Create.Timezone = parent.Timezone
			row.Create.Currency = parent.Currency
			if row.Create.PolicyCode == "" {
				row.Create.PolicyCode = parent.PolicyCode
				row.Create.PolicyVersion = parent.PolicyVersion
			}
			if row.Create.CapacityCode == "" {
				row.Create.CapacityCode = parent.CapacityCode
			}
			if parent.DepartmentID != nil {
				row.Create.DepartmentID = parent.DepartmentID
			}
			if parent.TeamID != nil {
				row.Create.TeamID = parent.TeamID
			}
		}
	}
	var tz, currency string
	if err = tx.QueryRow(ctx, `SELECT timezone,currency FROM branches WHERE organisation_id=$1 AND id=$2 AND status='ACTIVE'`, j.org, j.branch).Scan(&tz, &currency); err != nil {
		return err
	}
	if row.Create.Timezone == "" {
		row.Create.Timezone = tz
	}
	if row.Create.Currency == "" {
		row.Create.Currency = currency
	}
	if row.Create.PolicyCode == "" {
		row.Create.PolicyCode = "STANDARD"
	}
	if row.Create.PolicyVersion == 0 {
		row.Create.PolicyVersion = 1
	}
	desired := portfolio.Node{ID: r.id, OrganisationID: j.org, BranchID: j.branch, DepartmentID: row.Create.DepartmentID, TeamID: row.Create.TeamID, Kind: row.Kind, Code: row.Create.Code, Name: row.Create.Name, Timezone: row.Create.Timezone, Currency: row.Create.Currency, PolicyCode: row.Create.PolicyCode, PolicyVersion: row.Create.PolicyVersion, CapacityCode: row.Create.CapacityCode, Attributes: row.Create.Attributes}
	if desired.Validate() != nil {
		return importError("INVALID_STRUCTURAL_FIELDS")
	}
	r.row = row
	if err = h.importRowAuthority(ctx, tx, t, row, records); err != nil {
		return err
	}
	for _, id := range row.Create.PortfolioIDs {
		parent, err := importNode(ctx, tx, j.org, id, records)
		if err != nil || !portfolio.LegalEdge(parent.Kind, row.Kind, "MEMBERSHIP") {
			return importError("INVALID_PORTFOLIO_MEMBERSHIP")
		}
		if candidate := importRecordByID(records, id); candidate != nil && candidate.action == "CREATE" {
			if err = h.importRowAuthority(ctx, tx, t, candidate.row, records); err != nil {
				return err
			}
		} else if _, _, err = h.authorize(ctx, tx, portfolio.Capability(row.Kind, true), "PORTFOLIO", id, false); err != nil {
			return err
		}
	}
	if row.Kind == portfolio.Room {
		if row.Create.RoomTypeID == nil {
			return importError("ROOM_TYPE_REQUIRED")
		}
		roomType, err := importNode(ctx, tx, j.org, *row.Create.RoomTypeID, records)
		if err != nil || roomType.Kind != portfolio.RoomType {
			return importError("INVALID_ROOM_TYPE")
		}
		a, err := importPropertyID(ctx, tx, j.org, r.id, records, map[ids.ID]bool{})
		if err != nil {
			return err
		}
		b, err := importPropertyID(ctx, tx, j.org, roomType.ID, records, map[ids.ID]bool{})
		if err != nil {
			return err
		}
		if a.IsZero() || a != b {
			return importError("ROOM_TYPE_PROPERTY_CONFLICT")
		}
	}
	codeKey := func(id, parent ids.ID, kind portfolio.Kind) (ids.ID, error) {
		if kind == portfolio.Unit || kind == portfolio.Room || kind == portfolio.RoomType {
			return importPropertyID(ctx, tx, j.org, id, records, map[ids.ID]bool{})
		}
		if !parent.IsZero() {
			return parent, nil
		}
		return j.branch, nil
	}
	key, err := codeKey(r.id, row.Create.ParentID, row.Kind)
	if err != nil {
		return err
	}
	for _, other := range records {
		if other.number >= r.number || other.status != "VALID" || other.id == r.id || other.row.Kind != row.Kind || !stringsEqualFold(other.row.Create.Code, row.Create.Code) {
			continue
		}
		otherKey, e := codeKey(other.id, other.row.Create.ParentID, other.row.Kind)
		if e != nil {
			return e
		}
		if otherKey == key {
			return importError("AMBIGUOUS_BATCH_CODE")
		}
	}
	var duplicate bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_nodes n LEFT JOIN inventory_edges e ON e.organisation_id=n.organisation_id AND e.child_id=n.id AND e.relation='CONTAINS' WHERE n.organisation_id=$1 AND n.kind=$2 AND lower(n.code)=lower($3) AND n.id<>$4 AND (CASE WHEN n.kind IN ('UNIT','ROOM','ROOM_TYPE') THEN business_graph_property($1,n.id) ELSE coalesce(e.parent_id,n.branch_id) END)=$5)`, j.org, string(row.Kind), row.Create.Code, r.id, key).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return importError("AMBIGUOUS_EXISTING_CODE")
	}
	return nil
}

// ProcessInventoryImports is called by the existing worker loop. Each bounded
// batch locks organisation then job; a crash rolls back row effects/checkpoint.
// No user signing key or competing scheduler is required.
func (h *Handler) ProcessInventoryImports(ctx context.Context, batch int) error {
	if batch < 1 || batch > 50 {
		batch = 20
	}
	var org, id ids.ID
	err := h.Auth.Service.Pool.QueryRow(ctx, `SELECT organisation_id,id FROM inventory_import_jobs WHERE status IN ('QUEUED','VALIDATING','RUNNING') AND (lease_expires_at IS NULL OR lease_expires_at<now()) ORDER BY updated_at,id LIMIT 1`).Scan(&org, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); err != nil {
			return err
		}
		var claim ids.ID
		if err := tx.QueryRow(ctx, `SELECT id FROM inventory_import_jobs WHERE organisation_id=$1 AND id=$2 AND status IN ('QUEUED','VALIDATING','RUNNING') AND (lease_expires_at IS NULL OR lease_expires_at<now()) FOR UPDATE SKIP LOCKED`, org, id).Scan(&claim); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		j, err := loadImport(ctx, tx, org, id, false)
		if err != nil {
			return err
		}
		jobCtx := businessauth.StoredJobContext(ctx, j.actor, j.org, j.session, j.id)
		var live bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_sessions s JOIN business_accounts a ON a.id=s.account_id JOIN staff_memberships m ON m.account_id=a.id AND m.organisation_id=s.organisation_id WHERE s.id=$1 AND s.account_id=$2 AND s.organisation_id=$3 AND s.revoked_at IS NULL AND s.expires_at>now() AND a.status='ACTIVE' AND m.id=$4 AND m.status='ACTIVE')`, j.session, j.actor, j.org, j.member).Scan(&live); err != nil {
			return err
		}
		var t tenant
		if live {
			t, _, err = h.authorize(jobCtx, tx, "imports:write", "BRANCH", j.branch, false)
		}
		if !live || err != nil {
			_, e := tx.Exec(ctx, `UPDATE inventory_import_jobs SET status='PAUSED',last_error='INITIATOR_AUTHORITY_UNAVAILABLE',lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE id=$1`, id)
			return e
		}
		if _, err = tx.Exec(ctx, `UPDATE inventory_import_jobs SET status=CASE WHEN status='QUEUED' THEN 'VALIDATING' ELSE status END,lease_owner=$2,lease_expires_at=now()+interval '30 seconds',attempts=attempts+1 WHERE id=$1`, id, ids.New()); err != nil {
			return err
		}
		if j.status == "QUEUED" {
			j.status = "VALIDATING"
		}
		records, err := readImportRecords(ctx, tx, id)
		if err != nil {
			return err
		}
		processed := 0
		checkpoint := j.checkpoint
		pausedOnError := false
		for _, record := range records {
			if record.number <= j.checkpoint {
				continue
			}
			if processed >= batch {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			processed++
			checkpoint = record.number
			if j.status == "VALIDATING" {
				err = h.previewImportRecord(jobCtx, tx, t, j, record, records)
				record.status = "VALID"
				errorsJSON := []string{}
				code := ""
				if err != nil {
					record.status = "INVALID"
					record.action = "RECONCILE"
					code = importErrorCode(err)
					errorsJSON = append(errorsJSON, code)
				}
				raw, e := json.Marshal(record.row)
				if e != nil {
					return e
				}
				validation, _ := json.Marshal(errorsJSON)
				var before any
				if record.before != nil {
					before, _ = json.Marshal(record.before)
				}
				if _, e = tx.Exec(ctx, `UPDATE inventory_import_records SET node_id=$3,command=$4,status=$5,action=$6,validation_errors=$7,before_snapshot=$8,error_code=$9 WHERE job_id=$1 AND row_number=$2`, id, record.number, record.id, raw, record.status, record.action, validation, before, code); e != nil {
					return e
				}
				continue
			}
			if record.status != "VALID" {
				continue
			}
			savepoint, e := tx.Begin(jobCtx)
			if e != nil {
				return e
			}
			var result portfolio.Node
			if record.action == "SKIP" {
				result, e = portfolio.Get(jobCtx, savepoint, j.org, record.id)
				if e == nil && (result.Version != record.row.ExpectedVersion || result.Kind != record.row.Kind || result.BranchID != j.branch) {
					e = importError("CANONICAL_VERSION_OR_LIFECYCLE_CHANGED")
				}
				if e == nil {
					_, _, e = h.authorize(jobCtx, savepoint, portfolio.Capability(result.Kind, true), string(result.Kind), result.ID, false)
				}
			} else if record.action == "CREATE" {
				result, e = h.createInventory(jobCtx, savepoint, t, record.row.Kind, record.id, record.row.Create)
			} else {
				result, e = h.applyImportUpdate(jobCtx, savepoint, t, record)
			}
			var mapping ids.ID
			var mappingVersion int64
			mappingCreated := false
			if e == nil {
				lookup := savepoint.QueryRow(jobCtx, `SELECT id,version FROM inventory_external_mappings WHERE organisation_id=$1 AND provider=$2 AND external_id=$3 AND node_id=$4`, j.org, j.provider, record.row.ExternalID, result.ID).Scan(&mapping, &mappingVersion)
				if errors.Is(lookup, pgx.ErrNoRows) {
					mapping = ids.New()
					mappingCreated = true
					mappingVersion = 1
					_, e = savepoint.Exec(jobCtx, `INSERT INTO inventory_external_mappings(id,organisation_id,node_id,provider,external_id,source_reference,created_by,object_type) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, mapping, j.org, result.ID, j.provider, record.row.ExternalID, "import:"+id.String(), j.actor, string(record.row.Kind))
				} else if lookup != nil {
					e = lookup
				}
			}
			if e == nil {
				_, e = savepoint.Exec(jobCtx, `SET CONSTRAINTS inventory_node_integrity,inventory_edge_integrity IMMEDIATE`)
			}
			if e == nil {
				_, e = savepoint.Exec(jobCtx, `SET CONSTRAINTS inventory_node_integrity,inventory_edge_integrity DEFERRED`)
			}
			if e != nil {
				if rollbackErr := savepoint.Rollback(jobCtx); rollbackErr != nil {
					return rollbackErr
				}
				if _, err = tx.Exec(jobCtx, `UPDATE inventory_import_records SET status='QUARANTINED',error_code=$3 WHERE job_id=$1 AND row_number=$2`, id, record.number, importErrorCode(e)); err != nil {
					return err
				}
				if !j.allowPartial {
					pausedOnError = true
					break
				}
				continue
			}
			after, _ := json.Marshal(result)
			state := "APPLIED"
			if record.action == "SKIP" {
				state = "SKIPPED"
			}
			if _, e = savepoint.Exec(jobCtx, `UPDATE inventory_import_records SET status=$3,after_snapshot=$4,applied_version=$5,mapping_id=$6,mapping_created=$7,mapping_version=$8 WHERE job_id=$1 AND row_number=$2`, id, record.number, state, after, result.Version, mapping, mappingCreated, mappingVersion); e != nil {
				_ = savepoint.Rollback(jobCtx)
				return e
			}
			if e = savepoint.Commit(jobCtx); e != nil {
				return e
			}
			record.status = state
		}
		next := j.status
		if pausedOnError {
			next = "PAUSED"
		} else if checkpoint >= j.count {
			if j.status == "VALIDATING" {
				next = "PREVIEW_READY"
				checkpoint = 0
			} else {
				next = "COMPLETED"
				var bad int
				if err = tx.QueryRow(ctx, `SELECT count(*) FROM inventory_import_records WHERE job_id=$1 AND status IN ('INVALID','QUARANTINED')`, id).Scan(&bad); err != nil {
					return err
				}
				if bad > 0 {
					next = "COMPLETED_WITH_ERRORS"
				}
			}
		}
		if _, err = tx.Exec(jobCtx, `UPDATE inventory_import_jobs SET status=$2,checkpoint=$3,last_error=CASE WHEN $4 THEN 'ROW_FAILURE_REQUIRES_PARTIAL_REVIEW' ELSE '' END,lease_owner=NULL,lease_expires_at=NULL,version=version+1,updated_at=now() WHERE id=$1`, id, next, checkpoint, pausedOnError); err != nil {
			return err
		}
		if next != j.status {
			if err = h.accessEvent(jobCtx, tx, t, "INVENTORY_IMPORT_"+next, "business.inventory.import.changed.v1", "inventory_import", id, map[string]any{"status": next, "checkpoint": checkpoint, "record_count": j.count}); err != nil {
				return err
			}
		}
		return nil
	})
}
func (h *Handler) applyImportUpdate(ctx context.Context, tx pgx.Tx, t tenant, record *importRecord) (portfolio.Node, error) {
	old, err := portfolio.Get(ctx, tx, t.Organisation, record.id)
	if err != nil {
		return old, err
	}
	if _, _, err = h.authorize(ctx, tx, portfolio.Capability(old.Kind, true), string(old.Kind), old.ID, false); err != nil {
		return old, err
	}
	if old.State != "DRAFT" || old.ActivatedAt != nil || old.Version != record.row.ExpectedVersion {
		return old, importError("CANONICAL_VERSION_OR_LIFECYCLE_CHANGED")
	}
	attrs, _ := json.Marshal(record.row.Create.Attributes)
	if _, err = tx.Exec(ctx, `UPDATE inventory_nodes SET name=$3,code=$4,attributes=$5,version=version+1,updated_at=now(),updated_by=$6,room_type_id=coalesce($7,room_type_id) WHERE organisation_id=$1 AND id=$2`, t.Organisation, old.ID, record.row.Create.Name, record.row.Create.Code, attrs, t.Account, record.row.Create.RoomTypeID); err != nil {
		return old, err
	}
	if err = (portfolio.Repository{Outbox: h.Auth.Service.Outbox}).Commit(ctx, tx, t.Organisation, old.ID, t.Account, "IMPORT_UPDATED", &old); err != nil {
		return old, err
	}
	if err = audit(ctx, tx, t, "INVENTORY_IMPORT_UPDATED", "inventory_node", old.ID, map[string]any{"source_key": record.row.SourceKey, "previous_version": old.Version}); err != nil {
		return old, err
	}
	return portfolio.Get(ctx, tx, t.Organisation, old.ID)
}

func stringsEqualFold(a, b string) bool { return strings.EqualFold(a, b) }
