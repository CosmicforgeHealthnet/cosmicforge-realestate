-- name: GetInventoryNode :one
SELECT * FROM inventory_nodes WHERE organisation_id=$1 AND id=$2;
-- name: CreateInventoryNode :exec
INSERT INTO inventory_nodes(id,organisation_id,branch_id,department_id,team_id,kind,code,name,timezone,currency,policy_code,policy_version,capacity_code,attributes,room_type_id,created_by,updated_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16);
-- name: InventoryPhysicalParent :one
SELECT parent_id FROM inventory_edges WHERE organisation_id=$1 AND child_id=$2 AND relation='CONTAINS';
-- name: InventoryAncestry :many
WITH RECURSIVE ancestry(id) AS (
 SELECT sqlc.arg(node_id)::uuid
 UNION
 SELECT e.parent_id FROM inventory_edges e JOIN ancestry a ON a.id=e.child_id WHERE e.organisation_id=sqlc.arg(organisation_id) AND e.relation IN ('CONTAINS','MEMBERSHIP')
) SELECT n.id,n.kind,n.branch_id,n.department_id,n.team_id,n.state FROM inventory_nodes n JOIN ancestry a ON a.id=n.id WHERE n.organisation_id=sqlc.arg(organisation_id) ORDER BY n.id;
-- name: InventoryDescendants :many
WITH RECURSIVE descendants(id) AS (
 SELECT sqlc.arg(node_id)::uuid UNION SELECT e.child_id FROM inventory_edges e JOIN descendants d ON e.parent_id=d.id WHERE e.organisation_id=sqlc.arg(organisation_id) AND e.relation IN ('CONTAINS','MEMBERSHIP')
) SELECT n.* FROM inventory_nodes n JOIN descendants d ON n.id=d.id WHERE n.organisation_id=sqlc.arg(organisation_id) ORDER BY n.id LIMIT 10001;
-- name: FindInventoryExternalMapping :one
SELECT * FROM inventory_external_mappings WHERE organisation_id=$1 AND provider=$2 AND external_id=$3 AND object_type=$4;
-- name: ListInventoryPage :many
SELECT id FROM inventory_nodes WHERE organisation_id=$1 AND kind=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4;
-- name: FindInventoryCode :many
SELECT id FROM inventory_nodes WHERE organisation_id=$1 AND kind=$2 AND lower(code)=lower($3) ORDER BY id LIMIT 3;
-- name: ListImportRecords :many
SELECT * FROM inventory_import_records WHERE job_id=$1 ORDER BY row_number LIMIT $2 OFFSET $3;
-- name: ImportCheckpoint :execrows
UPDATE inventory_import_jobs SET checkpoint=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND checkpoint=$4;
