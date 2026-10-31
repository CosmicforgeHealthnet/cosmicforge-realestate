# Section 4: canonical enterprise inventory, security and events

## Identity and relationships

`inventory_nodes` is the sole identity/state store. The 14 named tables are typed
one-to-one anchors with the same UUIDv7 and tenant; they never create a second
property identity. `inventory_edges` distinguishes CONTAINS (one physical parent),
MEMBERSHIP (portfolio collection) and ASSOCIATES (parcel/property reference).
The explicit rule matrix in migration 00005 and `portfolio.LegalEdge` enforces
types. Database triggers serialize graph changes by organisation, prevent cycles,
cap depth at 64, validate context and enforce required parents at commit.

Development phases, parcels and properties can express estate structures;
properties support hotel, residential, commercial and mixed-use structures.
Building/block/floor are optional structural levels. Room type is a category
under a property; a physical room has its own parent and mandatory same-property
category reference. Units and rooms have case-insensitive property-scoped codes.
Other codes are unique under their physical parent or operational branch.
Assets can have component assets; UUID-level cycles remain forbidden.

Common areas are immutable COMMON_AREA identities, with derived non_sellable
responses independent of editable classification text. Facilities/common areas
do not acquire a reservation, sale or availability workflow. Attributes contain
bounded structural metadata, decimal-string area, integer millimetres/capacities,
survey/cadastral references and equipment warranty metadata. Parcel GeoJSON is
bounded WGS84 Polygon/MultiPolygon with topology checks; it is not a legal title,
geodesic area computation, PostGIS service or uploaded drawing decoder.

## Authorization and change safety

Canonical paths remain `/api/v1/business/...`; the existing gateway compatibility
prefix shares handlers/security. Existing Ed25519 access/service identities,
current membership/role/grants, explicit denies, date limits, delegation and
step-up MFA are reused. Ancestry is resolved from this service's graph. A grouping
parent's branch cannot provide authority over another operational branch. No
client organisation or ancestry header supplies trust. Hierarchy/summary results
filter every resource by current structure and corresponding entity capability.

Each node has optimistic `version`; `inventory_graph_versions` supplies a
`tree_version` changed by node/edge/reference insertion/removal in the subtree.
Reviewed commands bind the source node/subtree and destination subtree versions.
Changing children after review invalidates approval. Exact Section 3 policy
snapshots and operation digests apply to INVENTORY_RESTRUCTURE and
INVENTORY_MAPPING_CHANGE, without the initial onboarding privilege exception.
Both source/destination scopes and all affected physical descendants are checked.

Moves preserve IDs, update physical context atomically, and record node plus edge
history. Group membership changes do not physically move member properties.
Lifecycle is DRAFT -> ACTIVE -> INACTIVE/RETIRED, with permitted draft/inactive
archival and explicit cascades. Activation requires current KYB, supported verified
capacity, active physical parents/category and applicable current property/parcel
authority. No registration/create operation silently activates or verifies data.

Only never-active DRAFT subtrees can bypass external dependency assertions.
Previously active structures fail closed without complete current reservation,
lease and sale safety evidence. Typed guard requests bind tenant, operation and
node/subtree versions; these later-domain providers are not fabricated here.
Schema rollback never deletes canonical IDs or immutable audit/history.

## Provider references

Property/parcel authority uses the existing Verification PROPERTY_AUTHORITY case
contract with PROPERTY subject and BUSINESS_OPERATIONS domain. Signed read-back
validates case/subject/type/order; API linking binds a reviewed node version.
Worker reconciliation and authenticated broker hints refresh that projection;
stale, expired, restricted or unverified projections cannot authorize activation.
Metadata restructuring invalidates the bound version. Provider availability and
capacity-specific assertion operations remain separate live dependencies.

Media integration uses authenticated `/internal/v1/media/{id}` metadata and
`/download-url` with BUSINESS_INVENTORY purpose. Owner/domain, status and canonical
scope are checked. Access URLs are HTTPS, short-lived, audited, returned no-store
and never persisted. Confirmed space graphs reference existing same-property
canonical IDs and a READY floor-plan/digital-twin link. Immutable versions and
`digital_twin_refs` retain references; no media bytes, rendering, model generation
or competing physical-space identities are introduced.

External mapping identity is `(organisation, provider, object_type, external_id)`.
It retains source provenance and a version. Collisions cannot merge identities.
Explicit remapping retains object type, both scopes, expected mapping version,
current node snapshots and an exact independent approval.

## Durable imports and recovery

JSON/CSV input is bounded to one MiB and 1000 records. Raw uploaded bytes, file
URLs and arbitrary decoders are unsupported. Stable source keys, explicit parent/
room-type/portfolio dependencies, exact provider IDs and optional canonical UUIDs
determine identity. No fuzzy code-based merging is allowed. Preview validates
without creating canonical nodes. Record reports retain source row, resolved IDs,
typed action and coded errors. Existing active objects cannot be silently moved,
remapped or structurally overwritten by import.

The established worker loop claims an organisation/job transaction and processes
at most 20 records per normal batch (hard maximum 50). Every batch checks current
account/session/membership and per-row capabilities/scopes. Savepoints quarantine
row errors; no implicit partial continuation is permitted. Checkpoint, row effect,
mapping, audit/history and outbox commit together. Restart rolls back uncommitted
work and resumes from the last committed checkpoint. Duplicate claims and new-job
replays retain canonical identity. Pause/resume/cancel require version and reason.

Recovery is explicit, MFA-protected and bounded to 20 reverse-order actions per
command. Only unchanged never-active drafts with current authority, unchanged
mappings and no acquired references/live children can be restored/soft-archived.
Savepoint-isolated conflicts create idempotent reconciliation tasks. Valid later
work and mapping provenance are retained; no automated destructive rollback occurs.

## Events and operation

The existing shared envelope, transactional outbox, leases, JetStream deduplication,
durable consumers and processed-event records remain authoritative. All 25 prior
business event schemas remain, with eight graph additions (33 total):

| Envelope type | Transport subject suffix |
|---|---|
| property.created.v1 | business.property.created.v1 |
| property.updated.v1 | business.property.updated.v1 |
| property.authority.changed.v1 | business.property.authority.changed.v1 |
| business.structure.changed.v1 | business.structure.changed.v1 |
| business.inventory.mapping.changed.v1 | business.inventory.mapping.changed.v1 |
| business.inventory.media.changed.v1 | business.inventory.media.changed.v1 |
| business.inventory.space.confirmed.v1 | business.inventory.space.confirmed.v1 |
| business.inventory.import.changed.v1 | business.inventory.import.changed.v1 |

Prefix is the configured existing Business subject prefix (normally realestate).
Property envelope names preserve the specification, while Business transport
avoids Individual Owner's existing `realestate.property.>` stream. Tests run both
stream reservations together and exercise real broker outage/restart/duplicates.
No Owner consumer/stream/database was modified.

The existing maintenance loop now runs bounded import work each second and
reference reconciliation at the established maintenance cadence. No second
scheduler or worker account signing keys are introduced. New immutable history
and rule permissions are protected by the runtime grant script. Apply migration 5
and runtime grants before rollout. Live staging/provider certification is recorded
separately in the implementation report and staging runbook.
