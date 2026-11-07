package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Guest merge follows Section 11: identities are matched only on authenticated
// source evidence or verified ownership, never on email/phone. A merge marks
// the source MERGED, re-points open pre-arrival booking guests with a recorded
// reference, and resolves every immutable record through lineage.
const mergePreviewTTL = 30 * time.Minute

type CRMGuestMergePreviewCommand struct {
	TargetGuestID              ids.ID `json:"target_guest_id"`
	MatchBasis                 string `json:"match_basis"`
	OwnershipEvidenceReference string `json:"ownership_evidence_reference"`
	Reason                     string `json:"reason"`
}
type CRMGuestMergeExecute struct {
	Version       int64  `json:"version"`
	PreviewDigest string `json:"preview_digest"`
	ApprovalID    ids.ID `json:"approval_id,omitempty"`
	Reason        string `json:"reason"`
}
type CRMGuestMergeReverse struct {
	Version    int64  `json:"version"`
	ApprovalID ids.ID `json:"approval_id"`
	Reason     string `json:"reason"`
}
type CRMMergeConflict struct {
	Code     string `json:"code"`
	Blocking bool   `json:"blocking"`
	Detail   string `json:"detail"`
}
type CRMMergeCounts struct {
	OpenReservationGuests       int64 `json:"open_reservation_guests"`
	HistoricalReservationGuests int64 `json:"historical_reservation_guests"`
	Stays                       int64 `json:"stays"`
	LoyaltyAccounts             int64 `json:"loyalty_accounts"`
	RecoveryCases               int64 `json:"recovery_cases"`
	LegalHolds                  int64 `json:"legal_holds"`
	PrivacyRequests             int64 `json:"privacy_requests"`
	VIPClassifications          int64 `json:"vip_classifications"`
	SegmentMemberships          int64 `json:"segment_memberships"`
	IdentityLinks               int64 `json:"identity_links"`
}
type CRMConsentRestriction struct {
	PropertyID ids.ID `json:"property_id"`
	Purpose    string `json:"purpose"`
	TargetFrom string `json:"target_from"`
	Result     string `json:"result"`
}

// CRMMergePreview is digest-bound: execution recomputes it and refuses any
// change since the reviewed preview.
type CRMMergePreview struct {
	SourceGuestID         ids.ID                  `json:"source_guest_id"`
	TargetGuestID         ids.ID                  `json:"target_guest_id"`
	SourceVersion         int64                   `json:"source_version"`
	TargetVersion         int64                   `json:"target_version"`
	MatchBasis            string                  `json:"match_basis"`
	SharedSourceIdentity  int64                   `json:"shared_source_identities"`
	SourceProperties      []ids.ID                `json:"source_properties"`
	TargetProperties      []ids.ID                `json:"target_properties"`
	ScopeAdditions        []ids.ID                `json:"scope_additions"`
	ConsentRestrictions   []CRMConsentRestriction `json:"consent_restrictions"`
	Conflicts             []CRMMergeConflict      `json:"conflicts"`
	Counts                CRMMergeCounts          `json:"counts"`
	Risk                  string                  `json:"risk"`
	Executable            bool                    `json:"executable"`
	ApprovalRequired      bool                    `json:"approval_required"`
	ProtectedDataExcluded []string                `json:"protected_data_excluded"`
}
type CRMGuestMerge struct {
	ID                         ids.ID          `json:"id"`
	OrganisationID             ids.ID          `json:"organisation_id"`
	PropertyID                 ids.ID          `json:"property_id"`
	SourceGuestID              ids.ID          `json:"source_guest_id"`
	TargetGuestID              ids.ID          `json:"target_guest_id"`
	MatchBasis                 string          `json:"match_basis"`
	OwnershipEvidenceReference string          `json:"ownership_evidence_reference"`
	Risk                       string          `json:"risk"`
	State                      string          `json:"state"`
	Preview                    CRMMergePreview `json:"preview"`
	PreviewDigest              string          `json:"preview_digest"`
	PreviewExpiresAt           time.Time       `json:"preview_expires_at"`
	ApprovalID                 *ids.ID         `json:"approval_id"`
	MergedAt                   *time.Time      `json:"merged_at"`
	ReversedAt                 *time.Time      `json:"reversed_at"`
	ReversalReason             *string         `json:"reversal_reason"`
	Version                    int64           `json:"version"`
	CreatedAt                  time.Time       `json:"created_at"`
}
type CRMGuestMergeDetail struct {
	Merge      CRMGuestMerge     `json:"merge"`
	References []json.RawMessage `json:"references"`
}
type CRMGuestMergeResult struct {
	Merge            CRMGuestMerge `json:"merge"`
	CanonicalGuestID ids.ID        `json:"canonical_guest_id"`
	Reassigned       int           `json:"reassigned"`
	Restored         int           `json:"restored"`
	Retained         []string      `json:"retained"`
	ReviewRequired   []string      `json:"review_required"`
}

func (h *Handler) registerCRMMerge(r chi.Router) {
	r.Post("/guests/{id}/merge-previews", h.crmGuestMergePreview)
	r.Get("/guests/{id}/merges", h.crmGuestMerges)
	r.Get("/guest-merges/{id}", h.crmGuestMergeGet)
	r.Post("/guest-merges/{id}/execute", h.crmGuestMergeExecute)
	r.Post("/guest-merges/{id}/cancel", h.crmGuestMergeCancel)
	r.Post("/guest-merges/{id}/reverse", h.crmGuestMergeReverse)
}

// guestCanonical follows executed merges only. Reads that must include merged
// history call guestFamily for the canonical record and all merged members.
func guestCanonical(ctx context.Context, q database.Querier, org, guest ids.ID) (ids.ID, error) {
	var id ids.ID
	e := q.QueryRow(ctx, `SELECT business_guest_canonical($1,$2)`, org, guest).Scan(&id)
	return id, e
}
func guestFamily(ctx context.Context, q database.Querier, org, guest ids.ID) ([]ids.ID, error) {
	rows, e := q.Query(ctx, `WITH RECURSIVE fam(id,depth) AS (SELECT business_guest_canonical($1,$2),0 UNION SELECT g.id,f.depth+1 FROM guest_profiles g JOIN fam f ON g.organisation_id=$1 AND g.merged_into=f.id AND g.state='MERGED' WHERE f.depth<32) SELECT DISTINCT id FROM fam ORDER BY id`, org, guest)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func guestScopes(ctx context.Context, q database.Querier, org, guest ids.ID) ([]ids.ID, error) {
	rows, e := q.Query(ctx, `SELECT property_id FROM guest_property_scopes WHERE organisation_id=$1 AND guest_id=$2 AND state='ACTIVE' ORDER BY property_id`, org, guest)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type mergeConsent struct {
	state    string
	version  int64
	captured time.Time
	expires  *time.Time
}

func latestConsents(ctx context.Context, q database.Querier, org, guest, property ids.ID) (map[string]mergeConsent, error) {
	rows, e := q.Query(ctx, `SELECT DISTINCT ON (purpose) purpose,state,version,captured_at,expires_at FROM consents WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY purpose,version DESC`, org, guest, property)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]mergeConsent{}
	for rows.Next() {
		var purpose string
		var c mergeConsent
		if e = rows.Scan(&purpose, &c.state, &c.version, &c.captured, &c.expires); e != nil {
			return nil, e
		}
		out[purpose] = c
	}
	return out, rows.Err()
}
func consentCurrent(c mergeConsent, ok bool, now time.Time) bool {
	return ok && c.state == "GRANTED" && !c.captured.After(now) && (c.expires == nil || c.expires.After(now))
}

// buildMergePreview reads only owned metadata. Protected contact/preference
// payloads are never decrypted, compared or copied.
func (h *Handler) buildMergePreview(ctx context.Context, q database.Querier, org ids.ID, source, target crm.Guest, basis string) (CRMMergePreview, error) {
	now := h.Auth.Service.Now().UTC()
	p := CRMMergePreview{SourceGuestID: source.ID, TargetGuestID: target.ID, SourceVersion: source.Version, TargetVersion: target.Version, MatchBasis: basis, ConsentRestrictions: []CRMConsentRestriction{}, Conflicts: []CRMMergeConflict{}, ScopeAdditions: []ids.ID{}, ProtectedDataExcluded: []string{"contact_details", "room_preferences", "accessibility"}}
	conflict := func(code string, blocking bool, detail string) {
		p.Conflicts = append(p.Conflicts, CRMMergeConflict{Code: code, Blocking: blocking, Detail: detail})
	}
	if source.State != "ACTIVE" {
		conflict("SOURCE_NOT_ACTIVE", true, "Only an active guest record can be merged; current state "+source.State+".")
	}
	if target.State != "ACTIVE" {
		conflict("TARGET_NOT_ACTIVE", true, "The surviving guest must be active; current state "+target.State+".")
	}
	var e error
	if p.SourceProperties, e = guestScopes(ctx, q, org, source.ID); e != nil {
		return p, e
	}
	if p.TargetProperties, e = guestScopes(ctx, q, org, target.ID); e != nil {
		return p, e
	}
	for _, property := range p.SourceProperties {
		if !slices.Contains(p.TargetProperties, property) {
			p.ScopeAdditions = append(p.ScopeAdditions, property)
		}
	}
	if len(p.ScopeAdditions) > 0 {
		conflict("SCOPE_EXPANSION", false, "The surviving guest gains property scopes held by the source; independent review required.")
	}
	// Source-backed matching: identical external source identities (kind,
	// authority and opaque hash) on both records. Local registrations never match.
	if e = q.QueryRow(ctx, `SELECT count(*) FROM guest_identity_links s JOIN guest_identity_links t ON t.organisation_id=s.organisation_id AND t.guest_id=$3 AND t.state='ACTIVE' AND t.source_kind=s.source_kind AND t.source_authority=s.source_authority AND t.source_reference_hash=s.source_reference_hash WHERE s.organisation_id=$1 AND s.guest_id=$2 AND s.state='ACTIVE' AND s.proof_kind='APPROVED_SOURCE_LINK' AND t.proof_kind='APPROVED_SOURCE_LINK'`, org, source.ID, target.ID).Scan(&p.SharedSourceIdentity); e != nil {
		return p, e
	}
	if basis == "SHARED_SOURCE_IDENTITY" && p.SharedSourceIdentity == 0 {
		conflict("SOURCE_MATCH_REQUIRED", true, "No approved, authenticated source identity is shared; contact details never establish a match.")
	}
	if basis == "VERIFIED_OWNERSHIP" {
		conflict("VERIFIED_OWNERSHIP_REVIEW", false, "Ownership evidence without a shared authenticated source identity requires independent review.")
	}
	var differing bool
	if e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guest_identity_links s JOIN guest_identity_links t ON t.organisation_id=s.organisation_id AND t.guest_id=$3 AND t.state='ACTIVE' AND t.source_kind=s.source_kind AND t.source_authority=s.source_authority AND t.connection_id IS NOT DISTINCT FROM s.connection_id AND t.source_reference_hash<>s.source_reference_hash WHERE s.organisation_id=$1 AND s.guest_id=$2 AND s.state='ACTIVE' AND s.source_kind IN ('MARKETPLACE','OTA'))`, org, source.ID, target.ID).Scan(&differing); e != nil {
		return p, e
	}
	if differing {
		conflict("DIFFERENT_VERIFIED_SOURCE_IDENTITY", true, "Both records carry different authenticated identities from the same external source; they describe different source accounts.")
	}
	var sourceErasure, targetErasure, inHouse, loyaltyConflict, preferences bool
	if e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM privacy_requests WHERE organisation_id=$1 AND guest_id=$2 AND kind='ERASURE' AND state IN ('RECEIVED','APPROVED','BLOCKED_BY_LEGAL_HOLD')),EXISTS(SELECT 1 FROM privacy_requests WHERE organisation_id=$1 AND guest_id=$3 AND kind='ERASURE' AND state IN ('RECEIVED','APPROVED','BLOCKED_BY_LEGAL_HOLD')),EXISTS(SELECT 1 FROM stay_records WHERE organisation_id=$1 AND guest_id=$2 AND state='ACTIVE'),EXISTS(SELECT 1 FROM loyalty_accounts s JOIN loyalty_accounts t ON t.organisation_id=s.organisation_id AND t.program_key=s.program_key AND t.guest_id=$3 AND t.state<>'CLOSED' WHERE s.organisation_id=$1 AND s.guest_id=$2 AND s.state<>'CLOSED'),EXISTS(SELECT 1 FROM guest_preferences WHERE organisation_id=$1 AND guest_id=$2)`, org, source.ID, target.ID).Scan(&sourceErasure, &targetErasure, &inHouse, &loyaltyConflict, &preferences); e != nil {
		return p, e
	}
	if sourceErasure || targetErasure {
		conflict("ACTIVE_ERASURE_REQUEST", true, "A pending erasure request must complete or be rejected before identities change.")
	}
	if inHouse {
		conflict("SOURCE_IN_HOUSE", true, "The source guest has an active in-house stay; merge after departure.")
	}
	if loyaltyConflict {
		conflict("LOYALTY_PROGRAM_CONFLICT", true, "Both guests hold accounts in the same loyalty programme; balances are never combined automatically. Reconcile or close one account first.")
	}
	if preferences {
		conflict("PROTECTED_PREFERENCES_RETAINED", false, "Source protected preferences stay on the source record and are not copied.")
	}
	c := &p.Counts
	if e = q.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM reservation_guests g JOIN reservations r ON r.organisation_id=g.organisation_id AND r.id=g.reservation_id WHERE g.organisation_id=$1 AND g.guest_id=$2 AND r.state IN ('INQUIRY','HOLD','PENDING_PAYMENT','PENDING_APPROVAL','CONFIRMED')),
	 (SELECT count(*) FROM reservation_guests g JOIN reservations r ON r.organisation_id=g.organisation_id AND r.id=g.reservation_id WHERE g.organisation_id=$1 AND g.guest_id=$2 AND r.state NOT IN ('INQUIRY','HOLD','PENDING_PAYMENT','PENDING_APPROVAL','CONFIRMED')),
	 (SELECT count(*) FROM stay_records WHERE organisation_id=$1 AND guest_id=$2),
	 (SELECT count(*) FROM loyalty_accounts WHERE organisation_id=$1 AND guest_id=$2 AND state<>'CLOSED'),
	 (SELECT count(*) FROM service_recovery_cases WHERE organisation_id=$1 AND guest_id=$2),
	 (SELECT count(*) FROM legal_holds WHERE organisation_id=$1 AND guest_id=$2 AND state='ACTIVE'),
	 (SELECT count(*) FROM privacy_requests WHERE organisation_id=$1 AND guest_id=$2),
	 (SELECT count(*) FROM vip_classifications WHERE organisation_id=$1 AND guest_id=$2),
	 (SELECT count(*) FROM guest_segment_memberships WHERE organisation_id=$1 AND guest_id=$2),
	 (SELECT count(*) FROM guest_identity_links WHERE organisation_id=$1 AND guest_id=$2 AND state='ACTIVE')`, org, source.ID).Scan(&c.OpenReservationGuests, &c.HistoricalReservationGuests, &c.Stays, &c.LoyaltyAccounts, &c.RecoveryCases, &c.LegalHolds, &c.PrivacyRequests, &c.VIPClassifications, &c.SegmentMemberships, &c.IdentityLinks); e != nil {
		return p, e
	}
	if c.OpenReservationGuests > 200 || c.HistoricalReservationGuests+c.Stays+c.RecoveryCases+c.LegalHolds+c.PrivacyRequests+c.LoyaltyAccounts > 2000 {
		conflict("MERGE_BATCH_TOO_LARGE", true, "Large records require a bounded staged reconciliation.")
	}
	if c.LegalHolds > 0 {
		conflict("LEGAL_HOLD_PRESENT", false, "Active legal holds stay attached to the original record and also block erasure of the merged identity.")
	}
	var targetHolds int64
	if e = q.QueryRow(ctx, `SELECT count(*) FROM legal_holds WHERE organisation_id=$1 AND guest_id=$2 AND state='ACTIVE'`, org, target.ID).Scan(&targetHolds); e != nil {
		return p, e
	}
	if targetHolds > 0 {
		conflict("TARGET_LEGAL_HOLD_PRESENT", false, "The surviving record is under legal hold; the merge preserves it.")
	}
	if c.LoyaltyAccounts > 0 && !loyaltyConflict {
		conflict("LOYALTY_ACCOUNT_ASSOCIATION", false, "Source loyalty accounts keep their ledger and become associated with the canonical guest through lineage.")
	}
	var openCases int64
	if e = q.QueryRow(ctx, `SELECT count(*) FROM service_recovery_cases WHERE organisation_id=$1 AND guest_id=$2 AND state NOT IN ('CLOSED','REJECTED','CANCELLED')`, org, source.ID).Scan(&openCases); e != nil {
		return p, e
	}
	if openCases > 0 {
		conflict("OPEN_SERVICE_RECOVERY", false, "Open recovery cases remain on the source record and resolve to the canonical guest.")
	}
	// Consent is never widened. A later source withdrawal restricts the
	// surviving record; a source grant is never copied.
	for _, property := range p.SourceProperties {
		src, e := latestConsents(ctx, q, org, source.ID, property)
		if e != nil {
			return p, e
		}
		var tgt map[string]mergeConsent
		if slices.Contains(p.TargetProperties, property) {
			if tgt, e = latestConsents(ctx, q, org, target.ID, property); e != nil {
				return p, e
			}
		} else {
			tgt = map[string]mergeConsent{}
		}
		purposes := []string{"OPERATIONAL_CONTACT", "MARKETING", "PREFERENCES", "ACCESSIBILITY", "LOYALTY"}
		for _, purpose := range purposes {
			s, sok := src[purpose]
			t, tok := tgt[purpose]
			sCur, tCur := consentCurrent(s, sok, now), consentCurrent(t, tok, now)
			if sok && tok && sCur == tCur {
				continue
			}
			if tCur && sok && !sCur && s.captured.After(t.captured) {
				p.ConsentRestrictions = append(p.ConsentRestrictions, CRMConsentRestriction{PropertyID: property, Purpose: purpose, TargetFrom: "GRANTED", Result: "WITHDRAWN"})
				continue
			}
			if sCur && !tCur {
				p.ConsentRestrictions = append(p.ConsentRestrictions, CRMConsentRestriction{PropertyID: property, Purpose: purpose, TargetFrom: "NOT_GRANTED", Result: "NOT_GRANTED"})
			}
		}
	}
	if len(p.ConsentRestrictions) > 0 {
		conflict("CONSENT_CONFLICT", false, "Consent differs; the surviving record keeps the most restrictive current decision and source grants are not copied.")
	}
	p.Executable = true
	high := false
	for _, v := range p.Conflicts {
		if v.Blocking {
			p.Executable = false
		} else if v.Code != "PROTECTED_PREFERENCES_RETAINED" {
			high = true
		}
	}
	p.Risk = "STANDARD"
	if high {
		p.Risk = "HIGH"
	}
	p.ApprovalRequired = high
	return p, nil
}

// crmRecord maps a missing tenant-scoped row to 404, never revealing whether
// the identifier exists in another organisation.
func crmRecord[T any](ctx context.Context, q database.Querier, query string, args ...any) (T, error) {
	v, e := typedJSON[T](ctx, q, query, args...)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, apperrors.NotFound("CRM_RECORD_NOT_FOUND", "CRM record not found.")
	}
	return v, e
}

func loadGuestMerge(ctx context.Context, q database.Querier, org, id ids.ID, lock bool) (CRMGuestMerge, error) {
	query := `SELECT to_jsonb(m) FROM guest_merges m WHERE organisation_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return crmRecord[CRMGuestMerge](ctx, q, query, org, id)
}

// mergeAuthorize requires authority in every property either record touches.
func (h *Handler) mergeAuthorize(ctx context.Context, q database.Querier, capability string, properties []ids.ID, mfa bool) (tenant, error) {
	var t tenant
	var e error
	if len(properties) == 0 {
		return t, apperrors.PermissionDenied("Guest property scope required.")
	}
	for _, property := range properties {
		if t, _, e = h.authorize(ctx, q, capability, "PROPERTY", property, mfa); e != nil {
			return t, e
		}
	}
	return t, nil
}
func unionProperties(a, b []ids.ID, extra ...ids.ID) []ids.ID {
	out := append([]ids.ID{}, extra...)
	for _, id := range append(append([]ids.ID{}, a...), b...) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(x, y ids.ID) int {
		if x.String() < y.String() {
			return -1
		}
		if x == y {
			return 0
		}
		return 1
	})
	return out
}

func (h *Handler) crmGuestMergePreview(w http.ResponseWriter, r *http.Request) {
	var in CRMGuestMergePreviewCommand
	if !decode(w, r, &in) {
		return
	}
	if in.TargetGuestID.ValidateV7() != nil || !slices.Contains([]string{"SHARED_SOURCE_IDENTITY", "VERIFIED_OWNERSHIP"}, in.MatchBasis) || !crm.Text(in.OwnershipEvidenceReference, 5, 300) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Target guest, source-backed match basis, ownership evidence and reason required."))
		return
	}
	_, source, property, e := h.crmGuestTarget(r, "guests:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if source.ID == in.TargetGuestID {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A guest cannot be merged into itself."))
		return
	}
	h.execute(w, r, "crm:merge-preview:"+source.ID.String()+":"+in.TargetGuestID.String(), in, "guests:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		src, e := crm.GetGuest(ctx, tx, t.Organisation, source.ID)
		if e != nil {
			return 0, nil, e
		}
		// Another organisation's guest is invisible: tenant-scoped lookup only.
		tgt, e := crm.GetGuest(ctx, tx, t.Organisation, in.TargetGuestID)
		if e != nil {
			return 0, nil, e
		}
		p, e := h.buildMergePreview(ctx, tx, t.Organisation, src, tgt, in.MatchBasis)
		if e != nil {
			return 0, nil, e
		}
		if _, e = h.mergeAuthorize(ctx, tx, "guests:read", unionProperties(p.SourceProperties, p.TargetProperties, tgt.PropertyID), false); e != nil {
			return 0, nil, e
		}
		id := ids.New()
		raw := marshalCommercial(p)
		digest := hashCommercialPayload(raw)
		now := h.Auth.Service.Now().UTC()
		if _, e = tx.Exec(ctx, `INSERT INTO guest_merges(id,organisation_id,property_id,source_guest_id,target_guest_id,match_basis,ownership_evidence_reference,risk,preview,preview_digest,source_version,target_version,preview_expires_at,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, t.Organisation, tgt.PropertyID, src.ID, tgt.ID, in.MatchBasis, in.OwnershipEvidenceReference, p.Risk, raw, digest, src.Version, tgt.Version, now.Add(mergePreviewTTL), in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, tgt.PropertyID, id, "guest_merge", "GUEST_MERGE_PREVIEWED", "guest.merge.updated.v1", 1, map[string]any{"source_guest_id": src.ID, "target_guest_id": tgt.ID, "risk": p.Risk, "executable": p.Executable, "preview_digest": digest}); e != nil {
			return 0, nil, e
		}
		m, e := loadGuestMerge(ctx, tx, t.Organisation, id, false)
		return 201, m, e
	})
}

func (h *Handler) crmGuestMerges(w http.ResponseWriter, r *http.Request) {
	t, g, _, e := h.crmGuestTarget(r, "guests:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	canonical, e := guestCanonical(r.Context(), h.Auth.Service.Pool, t.Organisation, g.ID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(m) FROM guest_merges m WHERE organisation_id=$1 AND (source_guest_id=$2 OR target_guest_id=$2) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, g.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "canonical_guest_id": canonical, "limit": limit, "offset": offset}, e)
}

func (h *Handler) mergeTarget(r *http.Request, capability string, mfa bool) (tenant, CRMGuestMerge, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, CRMGuestMerge{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, CRMGuestMerge{}, e
	}
	m, e := loadGuestMerge(r.Context(), h.Auth.Service.Pool, t.Organisation, id, false)
	if e != nil {
		return t, m, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", m.PropertyID, mfa)
	return t, m, e
}

func (h *Handler) crmGuestMergeGet(w http.ResponseWriter, r *http.Request) {
	t, m, e := h.mergeTarget(r, "guests:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	refs, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(x) FROM guest_merge_references x WHERE organisation_id=$1 AND merge_id=$2 ORDER BY created_at,id LIMIT 500`, t.Organisation, m.ID)
	reply(w, r, 200, CRMGuestMergeDetail{Merge: m, References: refs}, e)
}

func (h *Handler) crmGuestMergeCancel(w http.ResponseWriter, r *http.Request) {
	var in CRMVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current merge version and reason required."))
		return
	}
	_, pre, e := h.mergeTarget(r, "guests:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:merge-cancel:"+pre.ID.String(), in, "guests:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guests:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		m, e := loadGuestMerge(ctx, tx, t.Organisation, pre.ID, true)
		if e != nil {
			return 0, nil, e
		}
		if m.State != "PREVIEWED" || m.Version != in.Version {
			return 0, nil, apperrors.Conflict("GUEST_MERGE_STATE", "Only the current unexecuted preview can be cancelled.")
		}
		if _, e = tx.Exec(ctx, `UPDATE guest_merges SET state='CANCELLED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, m.ID); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, m.PropertyID, m.ID, "guest_merge", "GUEST_MERGE_CANCELLED", "guest.merge.updated.v1", m.Version+1, map[string]any{"reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := loadGuestMerge(ctx, tx, t.Organisation, m.ID, false)
		return 200, out, e
	})
}

func (h *Handler) mergeRef(ctx context.Context, tx pgx.Tx, t tenant, m CRMGuestMerge, property ids.ID, entity string, id, from, to ids.ID, operation string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	_, e := tx.Exec(ctx, `INSERT INTO guest_merge_references(id,organisation_id,property_id,merge_id,entity_type,entity_id,from_guest_id,to_guest_id,operation,details,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(merge_id,entity_type,entity_id,operation) DO NOTHING`, ids.New(), t.Organisation, property, m.ID, entity, id, from, to, operation, marshalCommercial(details), t.Account)
	return e
}

type mergeRow struct {
	id, property ids.ID
}

func queryMergeRows(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]mergeRow, error) {
	rows, e := tx.Query(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []mergeRow{}
	for rows.Next() {
		var v mergeRow
		if e = rows.Scan(&v.id, &v.property); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (h *Handler) crmGuestMergeExecute(w http.ResponseWriter, r *http.Request) {
	var in CRMGuestMergeExecute
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || len(in.PreviewDigest) != 64 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current merge version, reviewed preview digest and reason required."))
		return
	}
	_, pre, e := h.mergeTarget(r, "guest_privacy:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:merge-execute:"+pre.ID.String(), in, "guest_privacy:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		m, e := loadGuestMerge(ctx, tx, t.Organisation, pre.ID, true)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if m.State != "PREVIEWED" || m.Version != in.Version || m.PreviewDigest != in.PreviewDigest {
			return 0, nil, apperrors.Conflict("GUEST_MERGE_STATE", "The current reviewed preview and its exact digest are required.")
		}
		if !now.Before(m.PreviewExpiresAt) {
			return 0, nil, apperrors.Conflict("GUEST_MERGE_PREVIEW_EXPIRED", "The merge preview expired; create a fresh preview.")
		}
		// Lock both records in a stable order before recomputing the preview.
		first, second := m.SourceGuestID, m.TargetGuestID
		if second.String() < first.String() {
			first, second = second, first
		}
		for _, id := range []ids.ID{first, second} {
			if _, e = tx.Exec(ctx, `SELECT id FROM guest_profiles WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id); e != nil {
				return 0, nil, e
			}
		}
		src, e := crm.GetGuest(ctx, tx, t.Organisation, m.SourceGuestID)
		if e != nil {
			return 0, nil, e
		}
		tgt, e := crm.GetGuest(ctx, tx, t.Organisation, m.TargetGuestID)
		if e != nil {
			return 0, nil, e
		}
		p, e := h.buildMergePreview(ctx, tx, t.Organisation, src, tgt, m.MatchBasis)
		if e != nil {
			return 0, nil, e
		}
		if hashCommercialPayload(marshalCommercial(p)) != m.PreviewDigest {
			return 0, nil, apperrors.Conflict("GUEST_MERGE_PREVIEW_STALE", "Guest records changed since the reviewed preview; preview again.")
		}
		if !p.Executable {
			return 0, nil, apperrors.Conflict("GUEST_MERGE_BLOCKED", "Blocking merge conflicts must be resolved first.")
		}
		properties := unionProperties(p.SourceProperties, p.TargetProperties, tgt.PropertyID)
		if _, e = h.mergeAuthorize(ctx, tx, "guests:write", properties, false); e != nil {
			return 0, nil, e
		}
		if _, e = h.mergeAuthorize(ctx, tx, "guest_privacy:write", properties, true); e != nil {
			return 0, nil, e
		}
		var approval any
		if p.ApprovalRequired {
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_GUEST_MERGE", target.Scope, in.ApprovalID, map[string]any{"merge_id": m.ID, "source_guest_id": m.SourceGuestID, "target_guest_id": m.TargetGuestID, "preview_digest": m.PreviewDigest, "command": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		}
		if _, e = tx.Exec(ctx, `UPDATE guest_merges SET state='MERGED',approval_id=$3,merged_by=$4,merged_at=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, m.ID, approval, t.Account, now); e != nil {
			return 0, nil, e
		}
		// 1. Scope: the surviving guest gains the source's property scopes.
		for _, property := range p.ScopeAdditions {
			var scope ids.ID
			var state string
			e = tx.QueryRow(ctx, `SELECT id,state FROM guest_property_scopes WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 FOR UPDATE`, t.Organisation, tgt.ID, property).Scan(&scope, &state)
			switch {
			case errors.Is(e, pgx.ErrNoRows):
				scope = ids.New()
				_, e = tx.Exec(ctx, `INSERT INTO guest_property_scopes(id,organisation_id,guest_id,property_id,state,approval_id,created_by) VALUES($1,$2,$3,$4,'ACTIVE',$5,$6)`, scope, t.Organisation, tgt.ID, property, approval, t.Account)
				state = "NONE"
			case e == nil:
				_, e = tx.Exec(ctx, `UPDATE guest_property_scopes SET state='ACTIVE',approval_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, scope, approval)
			}
			if e != nil {
				return 0, nil, e
			}
			if e = h.mergeRef(ctx, tx, t, m, property, "guest_property_scope", scope, src.ID, tgt.ID, "SCOPE_ADDED", map[string]any{"prior_state": state}); e != nil {
				return 0, nil, e
			}
		}
		// 2. Consent: append the more restrictive later decision; never copy grants.
		for _, c := range p.ConsentRestrictions {
			if c.Result != "WITHDRAWN" {
				continue
			}
			var version int64
			if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM consents WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 AND purpose=$4`, t.Organisation, tgt.ID, c.PropertyID, c.Purpose).Scan(&version); e != nil {
				return 0, nil, e
			}
			consent := ids.New()
			if _, e = tx.Exec(ctx, `INSERT INTO consents(id,organisation_id,property_id,guest_id,purpose,version,state,source,evidence_reference,captured_at,created_by) VALUES($1,$2,$3,$4,$5,$6,'WITHDRAWN','STAFF_RECORDED',$7,$8,$9)`, consent, t.Organisation, c.PropertyID, tgt.ID, c.Purpose, version+1, "guest-merge:"+m.ID.String(), now, t.Account); e != nil {
				return 0, nil, e
			}
			if e = h.mergeRef(ctx, tx, t, m, c.PropertyID, "consent", consent, src.ID, tgt.ID, "CONSENT_RESTRICTED", map[string]any{"purpose": c.Purpose}); e != nil {
				return 0, nil, e
			}
		}
		// 3. Open pre-arrival bookings follow the canonical guest.
		open, e := queryMergeRows(ctx, tx, `SELECT g.id,g.property_id FROM reservation_guests g JOIN reservations r ON r.organisation_id=g.organisation_id AND r.id=g.reservation_id WHERE g.organisation_id=$1 AND g.guest_id=$2 AND r.state IN ('INQUIRY','HOLD','PENDING_PAYMENT','PENDING_APPROVAL','CONFIRMED') ORDER BY g.id FOR UPDATE OF g`, t.Organisation, src.ID)
		if e != nil {
			return 0, nil, e
		}
		reservationsTouched := []ids.ID{}
		for _, row := range open {
			var reservation ids.ID
			if e = tx.QueryRow(ctx, `UPDATE reservation_guests SET guest_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 RETURNING reservation_id`, t.Organisation, row.id, tgt.ID).Scan(&reservation); e != nil {
				return 0, nil, e
			}
			if !slices.Contains(reservationsTouched, reservation) {
				reservationsTouched = append(reservationsTouched, reservation)
			}
			if e = h.mergeRef(ctx, tx, t, m, row.property, "reservation_guest", row.id, src.ID, tgt.ID, "REASSIGNED", map[string]any{"reservation_id": reservation}); e != nil {
				return 0, nil, e
			}
		}
		// 4. Immutable evidence keeps its original key and resolves via lineage.
		for _, spec := range []struct{ entity, operation, query string }{
			{"loyalty_account", "LOYALTY_ASSOCIATED", `SELECT id,property_id FROM loyalty_accounts WHERE organisation_id=$1 AND guest_id=$2 AND state<>'CLOSED' ORDER BY id`},
			{"stay_record", "RESOLVED_VIA_LINEAGE", `SELECT id,property_id FROM stay_records WHERE organisation_id=$1 AND guest_id=$2 ORDER BY id`},
			{"service_recovery_case", "RESOLVED_VIA_LINEAGE", `SELECT id,property_id FROM service_recovery_cases WHERE organisation_id=$1 AND guest_id=$2 ORDER BY id`},
			{"legal_hold", "RESOLVED_VIA_LINEAGE", `SELECT id,property_id FROM legal_holds WHERE organisation_id=$1 AND guest_id=$2 AND state='ACTIVE' ORDER BY id`},
			{"identity_link", "RESOLVED_VIA_LINEAGE", `SELECT id,property_id FROM guest_identity_links WHERE organisation_id=$1 AND guest_id=$2 AND state='ACTIVE' ORDER BY id`},
		} {
			rows, e := queryMergeRows(ctx, tx, spec.query, t.Organisation, src.ID)
			if e != nil {
				return 0, nil, e
			}
			for _, row := range rows {
				if e = h.mergeRef(ctx, tx, t, m, row.property, spec.entity, row.id, src.ID, tgt.ID, spec.operation, nil); e != nil {
					return 0, nil, e
				}
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE guest_profiles SET state='MERGED',merged_into=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, src.ID, tgt.ID); e != nil {
			return 0, nil, e
		}
		if e = h.mergeRef(ctx, tx, t, m, src.PropertyID, "guest_profile", src.ID, src.ID, tgt.ID, "PROFILE_MERGED", map[string]any{"source_version": src.Version}); e != nil {
			return 0, nil, e
		}
		projector := crm.HistoryProjector{Pool: h.Auth.Service.Pool, Outbox: h.Auth.Service.Outbox, Now: h.Auth.Service.Now}
		for _, reservation := range reservationsTouched {
			if e = projector.RebuildTx(ctx, tx, t.Organisation, reservation, nil); e != nil {
				return 0, nil, e
			}
		}
		change := map[string]any{"merge_id": m.ID, "target_guest_id": tgt.ID, "approval_id": approval, "reason": in.Reason}
		if e = h.crmGuestCommit(ctx, tx, t, src.PropertyID, src.ID, src.ID, "guest_profile", src.Version+1, "GUEST_MERGED", change); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, m.PropertyID, m.ID, "guest_merge", "GUEST_MERGE_EXECUTED", "guest.merge.updated.v1", m.Version+1, map[string]any{"source_guest_id": src.ID, "target_guest_id": tgt.ID, "reassigned": len(open), "approval_id": approval}); e != nil {
			return 0, nil, e
		}
		out, e := loadGuestMerge(ctx, tx, t.Organisation, m.ID, false)
		return 200, CRMGuestMergeResult{Merge: out, CanonicalGuestID: tgt.ID, Reassigned: len(open), Retained: []string{"stays", "consent_history", "loyalty_ledgers", "recovery_cases", "legal_holds", "privacy_requests", "identity_evidence", "protected_preferences"}, ReviewRequired: []string{}}, e
	})
}

// crmGuestMergeReverse recovers an incorrect merge from its recorded lineage.
// References changed or used since the merge are reported, never forced back.
func (h *Handler) crmGuestMergeReverse(w http.ResponseWriter, r *http.Request) {
	var in CRMGuestMergeReverse
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.ApprovalID.ValidateV7() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current merge version, independent reversal approval and reason required."))
		return
	}
	_, pre, e := h.mergeTarget(r, "guest_privacy:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:merge-reverse:"+pre.ID.String(), in, "guest_privacy:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		m, e := loadGuestMerge(ctx, tx, t.Organisation, pre.ID, true)
		if e != nil {
			return 0, nil, e
		}
		if m.State != "MERGED" || m.Version != in.Version {
			return 0, nil, apperrors.Conflict("GUEST_MERGE_STATE", "Only the current executed merge can be reversed.")
		}
		src, e := crm.GetGuest(ctx, tx, t.Organisation, m.SourceGuestID)
		if e != nil {
			return 0, nil, e
		}
		if src.State != "MERGED" || src.MergedInto == nil || *src.MergedInto != m.TargetGuestID {
			return 0, nil, apperrors.Conflict("GUEST_MERGE_LINEAGE_CHANGED", "The source record no longer resolves through this merge.")
		}
		scopes, e := guestScopes(ctx, tx, t.Organisation, m.TargetGuestID)
		if e != nil {
			return 0, nil, e
		}
		if _, e = h.mergeAuthorize(ctx, tx, "guest_privacy:write", unionProperties(scopes, nil, m.PropertyID), true); e != nil {
			return 0, nil, e
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_GUEST_UNMERGE", target.Scope, in.ApprovalID, map[string]any{"merge_id": m.ID, "source_guest_id": m.SourceGuestID, "target_guest_id": m.TargetGuestID, "command": op}, "", nil, ""); e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if _, e = tx.Exec(ctx, `UPDATE guest_merges SET state='REVERSED',reversal_reason=$3,reversal_approval_id=$4,reversed_by=$5,reversed_at=$6,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, m.ID, in.Reason, in.ApprovalID, t.Account, now); e != nil {
			return 0, nil, e
		}
		review := []string{}
		restored := 0
		refs, e := queryMergeRows(ctx, tx, `SELECT entity_id,property_id FROM guest_merge_references WHERE organisation_id=$1 AND merge_id=$2 AND entity_type='reservation_guest' AND operation='REASSIGNED' ORDER BY entity_id`, t.Organisation, m.ID)
		if e != nil {
			return 0, nil, e
		}
		touched := []ids.ID{}
		for _, ref := range refs {
			var reservation ids.ID
			var guest *ids.ID
			var state string
			if e = tx.QueryRow(ctx, `SELECT g.reservation_id,g.guest_id,r.state FROM reservation_guests g JOIN reservations r ON r.organisation_id=g.organisation_id AND r.id=g.reservation_id WHERE g.organisation_id=$1 AND g.id=$2 FOR UPDATE OF g`, t.Organisation, ref.id).Scan(&reservation, &guest, &state); e != nil {
				return 0, nil, e
			}
			if guest == nil || *guest != m.TargetGuestID || !slices.Contains([]string{"INQUIRY", "HOLD", "PENDING_PAYMENT", "PENDING_APPROVAL", "CONFIRMED"}, state) {
				review = append(review, "reservation_guest:"+ref.id.String())
				continue
			}
			if _, e = tx.Exec(ctx, `UPDATE reservation_guests SET guest_id=$3,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, ref.id, m.SourceGuestID); e != nil {
				return 0, nil, e
			}
			if e = h.mergeRef(ctx, tx, t, m, ref.property, "reservation_guest", ref.id, m.TargetGuestID, m.SourceGuestID, "RESTORED", map[string]any{"reservation_id": reservation}); e != nil {
				return 0, nil, e
			}
			restored++
			if !slices.Contains(touched, reservation) {
				touched = append(touched, reservation)
			}
		}
		added, e := queryMergeRows(ctx, tx, `SELECT entity_id,property_id FROM guest_merge_references WHERE organisation_id=$1 AND merge_id=$2 AND entity_type='guest_property_scope' AND operation='SCOPE_ADDED' ORDER BY entity_id`, t.Organisation, m.ID)
		if e != nil {
			return 0, nil, e
		}
		for _, scope := range added {
			var used bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_guests WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3) OR EXISTS(SELECT 1 FROM stay_records WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3) OR EXISTS(SELECT 1 FROM loyalty_accounts WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3) OR EXISTS(SELECT 1 FROM service_recovery_cases WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3)`, t.Organisation, m.TargetGuestID, scope.property).Scan(&used); e != nil {
				return 0, nil, e
			}
			if used {
				review = append(review, "guest_property_scope:"+scope.id.String())
				continue
			}
			if _, e = tx.Exec(ctx, `UPDATE guest_property_scopes SET state='REVOKED',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND state='ACTIVE'`, t.Organisation, scope.id); e != nil {
				return 0, nil, e
			}
			if e = h.mergeRef(ctx, tx, t, m, scope.property, "guest_property_scope", scope.id, m.TargetGuestID, m.SourceGuestID, "SCOPE_REVOKED", nil); e != nil {
				return 0, nil, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE guest_profiles SET state='ACTIVE',merged_into=NULL,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, src.ID); e != nil {
			return 0, nil, e
		}
		if e = h.mergeRef(ctx, tx, t, m, src.PropertyID, "guest_profile", src.ID, m.TargetGuestID, src.ID, "PROFILE_RESTORED", map[string]any{"approval_id": in.ApprovalID}); e != nil {
			return 0, nil, e
		}
		projector := crm.HistoryProjector{Pool: h.Auth.Service.Pool, Outbox: h.Auth.Service.Outbox, Now: h.Auth.Service.Now}
		for _, reservation := range touched {
			if e = projector.RebuildTx(ctx, tx, t.Organisation, reservation, nil); e != nil {
				return 0, nil, e
			}
		}
		if e = h.crmGuestCommit(ctx, tx, t, src.PropertyID, src.ID, src.ID, "guest_profile", src.Version+1, "GUEST_MERGE_REVERSED", map[string]any{"merge_id": m.ID, "approval_id": in.ApprovalID, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, m.PropertyID, m.ID, "guest_merge", "GUEST_MERGE_REVERSED", "guest.merge.updated.v1", m.Version+1, map[string]any{"source_guest_id": m.SourceGuestID, "target_guest_id": m.TargetGuestID, "restored": restored, "review_required": review}); e != nil {
			return 0, nil, e
		}
		out, e := loadGuestMerge(ctx, tx, t.Organisation, m.ID, false)
		// Consent restrictions appended during the merge are never re-widened.
		return 200, CRMGuestMergeResult{Merge: out, CanonicalGuestID: src.ID, Restored: restored, Retained: []string{"consent_restrictions", "merge_lineage"}, ReviewRequired: review}, e
	})
}
