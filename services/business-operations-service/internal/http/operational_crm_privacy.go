package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type CRMConsentCommand struct {
	ExpectedVersion   int64      `json:"expected_version"`
	Purpose           string     `json:"purpose"`
	State             string     `json:"state"`
	EvidenceReference string     `json:"evidence_reference"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	Reason            string     `json:"reason"`
}
type CRMPrivateCommand struct {
	ExpectedVersion int64              `json:"expected_version"`
	Preferences     crm.PrivateProfile `json:"preferences"`
	Reason          string             `json:"reason"`
}

func (h *Handler) crmGuestConsents(w http.ResponseWriter, r *http.Request) {
	t, g, property, e := h.crmGuestTarget(r, "guests:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(c) FROM consents c WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 ORDER BY created_at DESC,id DESC LIMIT $4 OFFSET $5`, t.Organisation, g.ID, property, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) crmGuestConsent(w http.ResponseWriter, r *http.Request) {
	var in CRMConsentCommand
	if !decode(w, r, &in) {
		return
	}
	if in.State == "WITHDRAWN" {
		in.ExpiresAt = nil
		var e error
		r, e = h.crmWithdrawalRequest(r)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
	}
	now := h.Auth.Service.Now().UTC()
	if in.ExpectedVersion < 0 || !crm.ConsentPurpose(in.Purpose) || in.State != "GRANTED" && in.State != "WITHDRAWN" || !crm.Text(in.EvidenceReference, 5, 300) || !validStaffReason(in.Reason) || in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid explicit consent purpose, state, evidence and current version required."))
		return
	}
	_, g, property, e := h.crmGuestTarget(r, "guest_privacy:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "crm:consent:"+g.ID.String()+":"+property.String()+":"+in.Purpose, in, "guest_privacy:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		if e = crm.RequireGuest(ctx, tx, t.Organisation, property, g.ID); e != nil && in.State != "WITHDRAWN" {
			return 0, nil, e
		}
		var current int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM consents WHERE organisation_id=$1 AND guest_id=$2 AND property_id=$3 AND purpose=$4`, t.Organisation, g.ID, property, in.Purpose).Scan(&current); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("CONSENT_VERSION", "Consent changed.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO consents(id,organisation_id,property_id,guest_id,purpose,version,state,source,evidence_reference,captured_at,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,'STAFF_RECORDED',$8,$9,$10,$11)`, id, t.Organisation, property, g.ID, in.Purpose, current+1, in.State, in.EvidenceReference, now, in.ExpiresAt, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, property, g.ID, id, "guest_consent", current+1, "GUEST_CONSENT_RECORDED", map[string]any{"purpose": in.Purpose, "state": in.State, "source": "STAFF_RECORDED", "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": id, "guest_id": g.ID, "property_id": property, "purpose": in.Purpose, "state": in.State, "version": current + 1}, nil
	})
}
func (h *Handler) crmGuestPrivateSave(w http.ResponseWriter, r *http.Request) {
	var in CRMPrivateCommand
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 0 || in.Preferences.Validate() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid protected preferences, expected version and reason required."))
		return
	}
	_, g, property, e := h.crmGuestTarget(r, "guest_private:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if h.Auth.Service.Sealer == nil {
		reply(w, r, 0, nil, apperrors.Unavailable("Protected guest storage is unavailable."))
		return
	}
	h.execute(w, r, "crm:private:"+g.ID.String()+":"+property.String(), in, "guest_private:write", "PROPERTY", property, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "guest_private:write", "PROPERTY", property, false)
		if e != nil {
			return 0, nil, e
		}
		if e = crm.RequireGuest(ctx, tx, t.Organisation, property, g.ID); e != nil {
			return 0, nil, e
		}
		purposes := []string{}
		if in.Preferences.Email != "" || in.Preferences.Phone != "" || in.Preferences.ContactPreference != "" && in.Preferences.ContactPreference != "NONE" {
			purposes = append(purposes, "OPERATIONAL_CONTACT")
		}
		if len(in.Preferences.RoomPreferences) > 0 {
			purposes = append(purposes, "PREFERENCES")
		}
		if len(in.Preferences.Accessibility) > 0 {
			purposes = append(purposes, "ACCESSIBILITY")
		}
		for _, purpose := range purposes {
			allowed, e := crm.ConsentAllowed(ctx, tx, t.Organisation, g.ID, property, purpose, h.Auth.Service.Now().UTC())
			if e != nil {
				return 0, nil, e
			}
			if !allowed {
				return 0, nil, apperrors.PermissionDenied("Current purpose-specific recorded consent is required for these protected fields.")
			}
		}
		var current int64
		var old *ids.ID
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0),(array_agg(id))[1] FROM guest_preferences WHERE organisation_id=$1 AND property_id=$2 AND guest_id=$3`, t.Organisation, property, g.ID).Scan(&current, &old); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("GUEST_PREFERENCE_VERSION", "Protected preferences changed.")
		}
		id := ids.New()
		cipher := h.Auth.Service.Sealer.Seal(marshalCommercial(in.Preferences), crm.PreferencesAAD(t.Organisation, g.ID, property))
		if old == nil {
			_, e = tx.Exec(ctx, `INSERT INTO guest_preferences(id,organisation_id,property_id,guest_id,encrypted_preferences,preference_key_id,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, t.Organisation, property, g.ID, cipher, h.Auth.Service.Sealer.KeyID(), t.Account)
		} else {
			id = *old
			_, e = tx.Exec(ctx, `UPDATE guest_preferences SET encrypted_preferences=$3,preference_key_id=$4,updated_by=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, cipher, h.Auth.Service.Sealer.KeyID(), t.Account)
		}
		if e != nil {
			return 0, nil, e
		}
		if e = h.crmGuestCommit(ctx, tx, t, property, g.ID, id, "guest_preferences", current+1, "GUEST_PROTECTED_PREFERENCES_UPDATED", map[string]any{"purpose_categories": purposes, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 200, map[string]any{"id": id, "guest_id": g.ID, "property_id": property, "version": current + 1}, nil
	})
}
func (h *Handler) crmGuestPrivateGet(w http.ResponseWriter, r *http.Request) {
	_, g, property, e := h.crmGuestTarget(r, "guest_private:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	purpose := r.URL.Query().Get("purpose")
	reason := r.URL.Query().Get("reason")
	reservationRef := r.URL.Query().Get("reservation_id")
	if !validStaffReason(reason) || purpose != "RECORD_REVIEW" && purpose != "RESERVATION_SERVICE" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Explicit supported access purpose and reason required."))
		return
	}
	var out crm.PrivateProfile
	var version int64
	e = database.WithTx(r.Context(), h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		t, _, e := h.authorize(ctx, tx, "guest_private:read", "PROPERTY", property, false)
		if e != nil {
			return e
		}
		if e = crm.RequireGuestScope(ctx, tx, t.Organisation, property, g.ID); e != nil {
			return e
		}
		if purpose == "RECORD_REVIEW" {
			if _, _, e = h.authorize(ctx, tx, "guest_privacy:write", "PROPERTY", property, true); e != nil {
				return e
			}
		} else {
			id, e := ids.ParseV7(reservationRef)
			if e != nil {
				return apperrors.InvalidRequest("Current owning reservation reference required.")
			}
			var owns bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservations r JOIN reservation_guests g ON g.organisation_id=r.organisation_id AND g.reservation_id=r.id WHERE r.organisation_id=$1 AND r.id=$2 AND r.property_id=$3 AND g.guest_id=$4 AND r.state IN ('INQUIRY','HOLD','PENDING_PAYMENT','PENDING_APPROVAL','CONFIRMED','CHECKED_IN'))`, t.Organisation, id, property, g.ID).Scan(&owns); e != nil {
				return e
			}
			if !owns {
				return apperrors.PermissionDenied("Guest access purpose does not match a current owning reservation.")
			}
		}
		var key string
		var cipher []byte
		e = tx.QueryRow(ctx, `SELECT encrypted_preferences,preference_key_id,version FROM guest_preferences WHERE organisation_id=$1 AND property_id=$2 AND guest_id=$3`, t.Organisation, property, g.ID).Scan(&cipher, &key, &version)
		if e == pgx.ErrNoRows {
			e = nil
		} else if e != nil {
			return e
		} else {
			if h.Auth.Service.Sealer == nil {
				return apperrors.Unavailable("Protected guest storage is unavailable.")
			}
			plain, err := h.Auth.Service.Sealer.Open(key, cipher, crm.PreferencesAAD(t.Organisation, g.ID, property))
			if err != nil {
				return apperrors.Unavailable("Protected preferences cannot be read.")
			}
			if e = json.Unmarshal(plain, &out); e != nil {
				return e
			}
		}
		// Withdrawal takes effect at read/dispatch time; an old protected blob is
		// never treated as continuing communication or accessibility permission.
		if purpose != "RECORD_REVIEW" {
			for _, category := range []string{"OPERATIONAL_CONTACT", "PREFERENCES", "ACCESSIBILITY"} {
				allowed, err := crm.ConsentAllowed(ctx, tx, t.Organisation, g.ID, property, category, h.Auth.Service.Now())
				if err != nil {
					return err
				}
				if !allowed {
					switch category {
					case "OPERATIONAL_CONTACT":
						out.Email = ""
						out.Phone = ""
						out.ContactPreference = "NONE"
					case "PREFERENCES":
						out.RoomPreferences = nil
					case "ACCESSIBILITY":
						out.Accessibility = nil
					}
				}
			}
		}
		return audit(ctx, tx, t, "GUEST_PROTECTED_FIELDS_READ", "guest", g.ID, map[string]any{"purpose": purpose, "property_id": property, "reservation_reference": reservationRef, "reason": reason})
	})
	w.Header().Set("Cache-Control", "no-store")
	reply(w, r, 200, map[string]any{"guest_id": g.ID, "property_id": property, "version": version, "preferences": out}, e)
}
