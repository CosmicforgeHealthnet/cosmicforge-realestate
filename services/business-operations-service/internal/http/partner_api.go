package businesshttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// HeaderPartnerKey carries a partner API key. It is separate from
// Authorization so the Gateway never evaluates it as a user token.
const HeaderPartnerKey = "X-CF-Partner-Key"

// Partner API environments (Annex D.36). SANDBOX credentials only ever see
// deterministic synthetic fixtures; PRODUCTION credentials see the owning
// organisation's records limited to the client's scopes.
const (
	PartnerSandbox    = "SANDBOX"
	PartnerProduction = "PRODUCTION"
)

// PartnerIdentity is the authenticated partner context.
type PartnerIdentity struct {
	OrganisationID     ids.ID   `json:"organisation_id"`
	IntegrationClient  ids.ID   `json:"integration_client_id"`
	CredentialID       ids.ID   `json:"credential_id"`
	Environment        string   `json:"environment"`
	Scopes             []string `json:"scopes"`
	RateLimitPerMinute int      `json:"rate_limit_per_minute"`
	RemainingInWindow  int      `json:"remaining_in_window"`
	CredentialExpires  string   `json:"credential_expires_at"`
}

// PartnerReservation is the partner-safe reservation projection: no guest
// identity, contact or payment instrument data.
type PartnerReservation struct {
	ID             ids.ID    `json:"reservation_id"`
	PropertyID     ids.ID    `json:"property_id"`
	Source         string    `json:"source"`
	State          string    `json:"state"`
	FinancialState string    `json:"financial_state"`
	Arrival        string    `json:"arrival"`
	Departure      string    `json:"departure"`
	AmountMinor    int64     `json:"amount_minor"`
	Currency       string    `json:"currency"`
	UpdatedAt      time.Time `json:"updated_at"`
	Sandbox        bool      `json:"sandbox"`
}

// PartnerReservationList is a keyset page ordered by updated_at, id.
type PartnerReservationList struct {
	Items       []PartnerReservation `json:"items"`
	Environment string               `json:"environment"`
	NextSince   *time.Time           `json:"next_updated_since"`
}

func (h *Handler) registerPartnerAPI(r chi.Router) {
	r.Get("/partner/v1/me", h.partnerMe)
	r.Get("/partner/v1/reservations", h.partnerReservations)
}

func partnerFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// partnerAuthenticate resolves the presented key to an ACTIVE integration
// client and credential, then charges one request to the credential's
// current one-minute window.
func (h *Handler) partnerAuthenticate(w http.ResponseWriter, r *http.Request, scope string) (PartnerIdentity, bool) {
	key := r.Header.Get(HeaderPartnerKey)
	if len(key) < 32 || len(key) > 256 {
		reply(w, r, 0, nil, apperrors.AuthenticationRequired())
		return PartnerIdentity{}, false
	}
	now := h.Auth.Service.Now().UTC()
	fp := partnerFingerprint(key)
	var p PartnerIdentity
	var attrs []byte
	var expires time.Time
	err := h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT c.organisation_id,c.parent_id,c.id,c.ends_at,i.attributes FROM api_credentials c JOIN integration_clients i ON i.organisation_id=c.organisation_id AND i.id=c.parent_id
WHERE c.kind='API_KEY_REF' AND c.ends_at>$2 AND (c.starts_at IS NULL OR c.starts_at<=$2) AND i.state='ACTIVE'
AND ((c.state IN ('ACTIVE','ROTATING') AND c.attributes->>'fingerprint_sha256'=$1) OR (c.state='ROTATING' AND c.attributes->>'next_fingerprint_sha256'=$1))
ORDER BY c.created_at DESC LIMIT 1`, fp, now).Scan(&p.OrganisationID, &p.IntegrationClient, &p.CredentialID, &expires, &attrs)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			reply(w, r, 0, nil, apperrors.Internal(err))
			return p, false
		}
		reply(w, r, 0, nil, apperrors.AuthenticationRequired())
		return p, false
	}
	var m map[string]any
	_ = json.Unmarshal(attrs, &m)
	p.Environment = attrString(m, "environment")
	if p.Environment == "" {
		p.Environment = PartnerSandbox
	}
	if rl, ok := m["rate_limit_per_minute"].(float64); ok {
		p.RateLimitPerMinute = int(rl)
	}
	if list, ok := m["scopes"].([]any); ok {
		for _, s := range list {
			if v, ok := s.(string); ok {
				p.Scopes = append(p.Scopes, v)
			}
		}
	}
	p.CredentialExpires = expires.UTC().Format(time.RFC3339)
	if p.RateLimitPerMinute < 1 || p.Environment != PartnerSandbox && p.Environment != PartnerProduction {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Partner client configuration is invalid."))
		return p, false
	}
	window := now.Truncate(time.Minute)
	count, err := h.partnerCharge(r.Context(), p, window)
	if err != nil {
		reply(w, r, 0, nil, apperrors.Internal(err))
		return p, false
	}
	if count > p.RateLimitPerMinute {
		retry := int(window.Add(time.Minute).Sub(now).Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		reply(w, r, 0, nil, apperrors.RateLimited(retry))
		return p, false
	}
	p.RemainingInWindow = p.RateLimitPerMinute - count
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(p.RateLimitPerMinute))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(p.RemainingInWindow))
	if scope != "" && !slices.Contains(p.Scopes, scope) {
		reply(w, r, 0, nil, apperrors.PermissionDenied("The partner credential lacks the required scope."))
		return p, false
	}
	return p, true
}

func (h *Handler) partnerCharge(ctx context.Context, p PartnerIdentity, window time.Time) (int, error) {
	var count int
	if err := h.Auth.Service.Pool.QueryRow(ctx, `INSERT INTO partner_api_rate_windows(organisation_id,credential_id,window_start,request_count) VALUES($1,$2,$3,1)
ON CONFLICT(credential_id,window_start) DO UPDATE SET request_count=partner_api_rate_windows.request_count+1 RETURNING request_count`, p.OrganisationID, p.CredentialID, window).Scan(&count); err != nil {
		return 0, err
	}
	_, err := h.Auth.Service.Pool.Exec(ctx, `DELETE FROM partner_api_rate_windows WHERE credential_id=$1 AND window_start<$2`, p.CredentialID, window.Add(-time.Hour))
	return count, err
}

func (h *Handler) partnerMe(w http.ResponseWriter, r *http.Request) {
	p, ok := h.partnerAuthenticate(w, r, "")
	if !ok {
		return
	}
	reply(w, r, http.StatusOK, p, nil)
}

func (h *Handler) partnerReservations(w http.ResponseWriter, r *http.Request) {
	p, ok := h.partnerAuthenticate(w, r, "reservations:read")
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 200 {
			reply(w, r, 0, nil, apperrors.InvalidRequest("limit must be between 1 and 200."))
			return
		}
		limit = n
	}
	since := time.Unix(0, 0).UTC()
	if v := q.Get("updated_since"); v != "" {
		t, e := time.Parse(time.RFC3339Nano, v)
		if e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("updated_since must be RFC 3339."))
			return
		}
		since = t.UTC()
	}
	var property *ids.ID
	if v := q.Get("property_id"); v != "" {
		id, e := ids.ParseV7(v)
		if e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid property_id."))
			return
		}
		property = &id
	}
	out := PartnerReservationList{Items: []PartnerReservation{}, Environment: p.Environment}
	if p.Environment == PartnerSandbox {
		out.Items = partnerSandboxReservations(since, property, limit)
	} else {
		rows, e := h.Auth.Service.Pool.Query(r.Context(), `SELECT id,property_id,source,state,financial_state,date_from::text,date_to::text,amount_minor,currency,updated_at FROM reservations
WHERE organisation_id=$1 AND ($2::uuid IS NULL OR property_id=$2) AND updated_at>$3 ORDER BY updated_at,id LIMIT $4`, p.OrganisationID, property, since, limit)
		if e != nil {
			reply(w, r, 0, nil, apperrors.Internal(e))
			return
		}
		for rows.Next() {
			var x PartnerReservation
			if e = rows.Scan(&x.ID, &x.PropertyID, &x.Source, &x.State, &x.FinancialState, &x.Arrival, &x.Departure, &x.AmountMinor, &x.Currency, &x.UpdatedAt); e != nil {
				rows.Close()
				reply(w, r, 0, nil, apperrors.Internal(e))
				return
			}
			out.Items = append(out.Items, x)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			reply(w, r, 0, nil, apperrors.Internal(e))
			return
		}
	}
	if len(out.Items) == limit {
		last := out.Items[len(out.Items)-1].UpdatedAt
		out.NextSince = &last
	}
	reply(w, r, http.StatusOK, out, nil)
}

// partnerSandboxReservations returns fixed synthetic records; sandbox
// credentials never read tenant data.
func partnerSandboxReservations(since time.Time, property *ids.ID, limit int) []PartnerReservation {
	prop := ids.MustParse("01900000-0000-7000-8000-00000000a001")
	if property != nil && *property != prop {
		return []PartnerReservation{}
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fixtures := []PartnerReservation{
		{ID: ids.MustParse("01900000-0000-7000-8000-00000000b001"), PropertyID: prop, Source: "MARKETPLACE", State: "CONFIRMED", FinancialState: "VERIFIED", Arrival: "2026-02-01", Departure: "2026-02-04", AmountMinor: 15000000, Currency: "NGN", UpdatedAt: base, Sandbox: true},
		{ID: ids.MustParse("01900000-0000-7000-8000-00000000b002"), PropertyID: prop, Source: "DIRECT", State: "PENDING_PAYMENT", FinancialState: "PENDING", Arrival: "2026-03-10", Departure: "2026-03-12", AmountMinor: 9000000, Currency: "NGN", UpdatedAt: base.Add(time.Hour), Sandbox: true},
		{ID: ids.MustParse("01900000-0000-7000-8000-00000000b003"), PropertyID: prop, Source: "OTA", State: "CANCELLED", FinancialState: "UNVERIFIED", Arrival: "2026-04-05", Departure: "2026-04-06", AmountMinor: 4500000, Currency: "NGN", UpdatedAt: base.Add(2 * time.Hour), Sandbox: true},
	}
	out := []PartnerReservation{}
	for _, f := range fixtures {
		if f.UpdatedAt.After(since) && len(out) < limit {
			out = append(out, f)
		}
	}
	return out
}
