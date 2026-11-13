package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Annex D (D.9-D.42) cross-record rules for the lifecycles added in
// migration 00038, plus the strengthened gates they impose on the existing
// Section 16 domains. Every rule runs inside the owning command transaction,
// so a rejected rule leaves no state, history or outbox behind.

var (
	fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	clockPattern       = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	currencyPattern    = regexp.MustCompile(`^[A-Z]{3}$`)
	decimalRate        = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,12})?$`)
)

// Fields a financing partner may be given; anything else is refused so the
// consent scope is explicit and minimal.
var financingDataScopes = []string{"identity", "income", "employment", "property_details", "transaction_price", "payment_history", "credit_bureau_reference"}

var secretAttributeNames = []string{"secret", "api_key", "apikey", "token", "password", "private_key", "client_secret"}

// chainOpsHook runs the existing hook (if any) before the added rule.
func chainOpsHook(key string, next opsHook) {
	prev := opsHooks[key]
	opsHooks[key] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if prev != nil {
			if e := prev(ctx, tx, h, t, d, rec, stage, cmd); e != nil {
				return e
			}
		}
		return next(ctx, tx, h, t, d, rec, stage, cmd)
	}
}

// mergedAttrs is the record's attributes overlaid with the command's.
func mergedAttrs(rec *OpsRecord, cmd *OpsRecordTransition) map[string]any {
	m := attr(rec)
	if cmd != nil {
		for k, v := range cmd.Attributes {
			m[k] = v
		}
	}
	return m
}

func setAttr(rec *OpsRecord, key string, value any) {
	m := attr(rec)
	m[key] = value
	rec.Attributes = marshalCommercial(m)
}

func setCmdAttr(cmd *OpsRecordTransition, key string, value any) {
	if cmd.Attributes == nil {
		cmd.Attributes = map[string]any{}
	}
	cmd.Attributes[key] = value
}

func attrInt(m map[string]any, key string) (int64, bool) {
	f, ok := m[key].(float64)
	if !ok || f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}

func attrID(m map[string]any, key string) (ids.ID, bool) {
	id, e := ids.ParseV7(attrString(m, key))
	return id, e == nil
}

func opsState(ctx context.Context, tx pgx.Tx, table string, org, id ids.ID) (string, error) {
	var state string
	if e := tx.QueryRow(ctx, `SELECT state FROM `+table+` WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&state); e != nil {
		return "", opsNotFound(e)
	}
	return state, nil
}

func opsCount(ctx context.Context, tx pgx.Tx, query string, args ...any) (int, error) {
	var n int
	e := tx.QueryRow(ctx, query, args...).Scan(&n)
	return n, e
}

// liveContract verifies a tenancy or commercial deal subject can carry
// operational obligations.
func liveContract(ctx context.Context, tx pgx.Tx, org ids.ID, rec *OpsRecord) (string, string, error) {
	table, e := opsSubjectTable(ctx, tx, org, "tenancies|commercial_deals", rec.ScopeID, *rec.SubjectID)
	if e != nil {
		return "", "", e
	}
	state, e := opsState(ctx, tx, table, org, *rec.SubjectID)
	return table, state, e
}

func requireLiveContract(ctx context.Context, tx pgx.Tx, org ids.ID, rec *OpsRecord, allowDraft bool) (string, error) {
	table, state, e := liveContract(ctx, tx, org, rec)
	if e != nil {
		return "", e
	}
	live := map[string][]string{"tenancies": {"ACTIVE", "NOTICE_GIVEN"}, "commercial_deals": {"SIGNED", "ACTIVE"}}[table]
	if allowDraft {
		live = append(live, map[string][]string{"tenancies": {"DRAFT", "PENDING_SIGNATURE"}, "commercial_deals": {"APPROVED"}}[table]...)
	}
	if !slices.Contains(live, state) {
		return "", apperrors.Conflict("CONTRACT_NOT_LIVE", "The tenancy or commercial contract is not in a state that can carry this record.")
	}
	return table, nil
}

// financialObligationFor verifies a Payment-reconciled financial obligation
// created for exactly this record. Settlement truth is never written here:
// it is read from the obligation's Payment readback.
type linkedObligation struct {
	ID       ids.ID
	State    string
	Amount   int64
	Settled  int64
	Currency string
}

func financialObligationFor(ctx context.Context, tx pgx.Tx, org ids.ID, ref string, sourceRef string, amount *int64, currency *string) (linkedObligation, error) {
	var o linkedObligation
	id, e := ids.ParseV7(ref)
	if e != nil {
		return o, apperrors.InvalidRequest("The reference must be the financial obligation identifier.")
	}
	var direction, sourceType, source string
	if e = tx.QueryRow(ctx, `SELECT id,state,amount_minor,settled_minor,currency,direction,source_type,source_reference FROM financial_obligations WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&o.ID, &o.State, &o.Amount, &o.Settled, &o.Currency, &direction, &sourceType, &source); e != nil {
		return o, opsNotFound(e)
	}
	if direction != "RECEIVABLE" || sourceType != "CONTRACT" || source != sourceRef || o.State == "CANCELLED" {
		return o, apperrors.Conflict("OBLIGATION_MISMATCH", "The financial obligation must be a receivable CONTRACT obligation whose source_reference is "+sourceRef+".")
	}
	if amount != nil && (o.Amount != *amount || currency == nil || o.Currency != *currency) {
		return o, apperrors.Conflict("OBLIGATION_AMOUNT_MISMATCH", "The financial obligation amount and currency must equal the scheduled amount.")
	}
	return o, nil
}

func linkedFinancialObligation(ctx context.Context, tx pgx.Tx, org ids.ID, rec *OpsRecord, prefix string) (linkedObligation, error) {
	ref := attrString(attr(rec), "financial_obligation_id")
	if ref == "" {
		return linkedObligation{}, apperrors.Conflict("OBLIGATION_NOT_LINKED", "Link the Payment-reconciled financial obligation first.")
	}
	return financialObligationFor(ctx, tx, org, ref, prefix+rec.ID.String(), nil, nil)
}

func withinDays(h *Handler, at *time.Time, days int) bool {
	return at != nil && !at.After(h.Auth.Service.Now().Add(time.Duration(days)*24*time.Hour))
}

func noSecrets(m map[string]any) error {
	for k := range m {
		if slices.Contains(secretAttributeNames, strings.ToLower(k)) {
			return apperrors.InvalidRequest("Secrets are never stored; provide a secret-manager reference name instead.")
		}
	}
	return nil
}

func stringList(m map[string]any, key string, min, max int, allowed []string) ([]string, bool) {
	raw, ok := m[key].([]any)
	if !ok || len(raw) < min || len(raw) > max {
		return nil, false
	}
	out := []string{}
	for _, v := range raw {
		s, ok := v.(string)
		if !ok || !opsText(s, 1, 200) || allowed != nil && !slices.Contains(allowed, s) || slices.Contains(out, s) {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// supersedeLive moves other live records matching where (with $1=org, $2=id
// of the record being promoted, then extra args) to the target state.
func (h *Handler) supersedeLive(ctx context.Context, tx pgx.Tx, t tenant, key, where, to, action string, args ...any) error {
	d := opsDomains[key]
	rows, e := tx.Query(ctx, `SELECT id FROM `+d.Table+` WHERE organisation_id=$1 AND id<>$2 AND `+where+` FOR UPDATE`, append([]any{t.Organisation}, args...)...)
	if e != nil {
		return e
	}
	list := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		list = append(list, id)
	}
	rows.Close()
	for _, id := range list {
		if e = h.opsSystemTransition(ctx, tx, t, d, id, to, action); e != nil {
			return e
		}
	}
	return nil
}

// registerAnnexDRules is called at the end of the Section 16 hook
// registration so the chained gates wrap the original domain hooks.
func registerAnnexDRules() {
	residentialRules()
	commercialLandSalesRules()
	developmentMaintenanceRules()
	riskAccessFinanceRules()
	platformRules()
	hospitalityRules()
}

func residentialRules() {
	opsHooks["rental_offer"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" && stage != "before:ISSUE" {
			return nil
		}
		state, e := opsState(ctx, tx, "rental_applications", t.Organisation, *rec.ParentID)
		if e != nil {
			return e
		}
		if state != "APPROVED" {
			return apperrors.Conflict("APPLICATION_NOT_APPROVED", "Offers are made only on an approved rental application.")
		}
		if !withinDays(h, rec.EndsAt, 30) {
			return apperrors.InvalidRequest("A rental offer expires within 30 days.")
		}
		return nil
	}
	chainOpsHook("tenancy", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := attr(rec)
		switch stage {
		case "create":
			if raw := attrString(m, "offer_id"); raw != "" {
				offer, ok := attrID(m, "offer_id")
				app, ok2 := attrID(m, "application_id")
				if !ok || !ok2 {
					return apperrors.InvalidRequest("offer_id requires the application_id it was made on.")
				}
				var amount int64
				var currency string
				if e := tx.QueryRow(ctx, `SELECT amount_minor,currency FROM rental_offers WHERE organisation_id=$1 AND id=$2 AND parent_id=$3 AND state='ACCEPTED' FOR UPDATE`, t.Organisation, offer, app).Scan(&amount, &currency); e != nil {
					return apperrors.Conflict("OFFER_NOT_ACCEPTED", "The rental offer must be accepted on the same application.")
				}
				if rec.AmountMinor == nil || *rec.AmountMinor != amount || rec.Currency == nil && currency != "" || rec.Currency != nil && *rec.Currency != currency {
					return apperrors.Conflict("OFFER_TERMS_MISMATCH", "The tenancy rent must equal the accepted offer.")
				}
			}
			if f, ok := m["rent_frequency"]; ok && !slices.Contains([]any{"MONTHLY", "QUARTERLY", "ANNUAL"}, f) {
				return apperrors.InvalidRequest("rent_frequency must be MONTHLY, QUARTERLY or ANNUAL.")
			}
			if g, ok := m["grace_days"]; ok {
				if v, ok := g.(float64); !ok || v < 0 || v > 60 {
					return apperrors.InvalidRequest("grace_days must be 0-60.")
				}
			}
		case "after:CREATE":
			if offer, ok := attrID(m, "offer_id"); ok {
				return h.opsSystemTransition(ctx, tx, t, opsDomains["rental_offer"], offer, "CONVERTED", "CONVERTED_TO_TENANCY")
			}
		case "before:ACTIVATE":
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM agreements WHERE organisation_id=$1 AND subject_id=$2 AND kind='RESIDENTIAL_TENANCY' AND state='SIGNED'`, t.Organisation, rec.ID)
			if e != nil {
				return e
			}
			if n == 0 {
				return apperrors.Conflict("AGREEMENT_NOT_SIGNED", "A signed residential tenancy agreement version is required before activation.")
			}
		case "after:ACTIVATE":
			return h.generateRentSchedule(ctx, tx, t, *rec, 12)
		case "after:END", "after:TERMINATE":
			return h.cancelFutureRent(ctx, tx, t, rec.ID)
		}
		return nil
	})
	opsHooks["agreement"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			table, e := opsSubjectTable(ctx, tx, t.Organisation, strings.TrimPrefix(d.Subject, "RECORD:"), rec.ScopeID, *rec.SubjectID)
			if e != nil {
				return e
			}
			allowed := map[string][]string{"tenancies": {"RESIDENTIAL_TENANCY", "AMENDMENT"}, "commercial_deals": {"COMMERCIAL_LEASE", "SALE_CONTRACT", "AMENDMENT"}, "sale_transactions": {"SALE_CONTRACT", "AMENDMENT"}, "land_transactions": {"LAND_LEASE", "SALE_CONTRACT", "AMENDMENT"}}[table]
			if !slices.Contains(allowed, rec.Kind) {
				return apperrors.InvalidRequest("The agreement kind does not match the contract it governs.")
			}
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM agreements WHERE organisation_id=$1 AND subject_id=$2`, t.Organisation, *rec.SubjectID)
			if e != nil {
				return e
			}
			setAttr(rec, "subject_type", table)
			setAttr(rec, "version_number", n+1)
		case "before:RECORD_SIGNATURE":
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM agreements WHERE organisation_id=$1 AND subject_id=$2 AND state='SIGNED' AND id<>$3`, t.Organisation, *rec.SubjectID, rec.ID)
			if e != nil {
				return e
			}
			if n > 0 {
				return apperrors.Conflict("SIGNED_AGREEMENT_EXISTS", "Supersede the current signed version before signing a new one.")
			}
		}
		return nil
	}
	opsHooks["rent_obligation"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			if _, e := requireLiveContract(ctx, tx, t.Organisation, rec, false); e != nil {
				return e
			}
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM rent_obligations WHERE organisation_id=$1 AND subject_id=$2 AND kind=$3 AND starts_at=$4 AND state NOT IN ('CANCELLED','WAIVED')`, t.Organisation, *rec.SubjectID, rec.Kind, rec.StartsAt)
			if e != nil {
				return e
			}
			if n > 0 {
				return apperrors.Conflict("OBLIGATION_EXISTS", "An obligation of this kind is already scheduled for this due date.")
			}
		case "before:LINK_FINANCIAL_OBLIGATION":
			if _, e := financialObligationFor(ctx, tx, t.Organisation, cmd.ExternalReference, "rent_obligation:"+rec.ID.String(), rec.AmountMinor, rec.Currency); e != nil {
				return e
			}
			setCmdAttr(cmd, "financial_obligation_id", cmd.ExternalReference)
		case "before:RECORD_SETTLEMENT":
			o, e := linkedFinancialObligation(ctx, tx, t.Organisation, rec, "rent_obligation:")
			if e != nil {
				return e
			}
			if o.State != "SETTLED" || o.Settled != o.Amount {
				return apperrors.Conflict("SETTLEMENT_NOT_VERIFIED", "Payment readback does not show this settlement on the linked financial obligation.")
			}
			setCmdAttr(cmd, "verified_settled_minor", o.Settled)
			setCmdAttr(cmd, "settlement_source", "payment-service-readback")
		}
		return nil
	}
	opsHooks["deposit_obligation"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := attr(rec)
		switch stage {
		case "create":
			if _, e := requireLiveContract(ctx, tx, t.Organisation, rec, true); e != nil {
				return e
			}
		case "before:LINK_FINANCIAL_OBLIGATION":
			if _, e := financialObligationFor(ctx, tx, t.Organisation, cmd.ExternalReference, "deposit_obligation:"+rec.ID.String(), rec.AmountMinor, rec.Currency); e != nil {
				return e
			}
			setCmdAttr(cmd, "financial_obligation_id", cmd.ExternalReference)
		case "before:RECORD_HELD":
			o, e := linkedFinancialObligation(ctx, tx, t.Organisation, rec, "deposit_obligation:")
			if e != nil {
				return e
			}
			if o.State != "SETTLED" || o.Settled != o.Amount {
				return apperrors.Conflict("SETTLEMENT_NOT_VERIFIED", "The deposit is held only after Payment readback shows full settlement.")
			}
			setCmdAttr(cmd, "held_minor", o.Settled)
		case "before:REQUEST_RELEASE":
			held, ok := attrInt(m, "held_minor")
			if !ok {
				return apperrors.Conflict("DEPOSIT_NOT_HELD", "The held deposit amount is unknown.")
			}
			refund := held
			if cmd.AmountMinor != nil {
				refund = *cmd.AmountMinor
				if rec.Currency == nil || cmd.Currency != *rec.Currency {
					return apperrors.InvalidRequest("The refund currency must equal the deposit currency.")
				}
			}
			if refund < 0 || refund > held {
				return apperrors.InvalidRequest("The refund cannot exceed the held deposit.")
			}
			if refund < held {
				caseID, ok := attrID(cmd.Attributes, "move_out_case_id")
				if !ok {
					return apperrors.InvalidRequest("Deductions require the settled move-out case.")
				}
				var deduction int64
				if e := tx.QueryRow(ctx, `SELECT amount_minor FROM move_out_cases WHERE organisation_id=$1 AND id=$2 AND subject_id=$3 AND state IN ('SETTLED','CLOSED')`, t.Organisation, caseID, *rec.SubjectID).Scan(&deduction); e != nil {
					return apperrors.Conflict("MOVE_OUT_NOT_SETTLED", "Deductions require a settled move-out case for the same contract.")
				}
				if held-refund != deduction {
					return apperrors.Conflict("DEDUCTION_MISMATCH", "The deduction must equal the agreed move-out settlement.")
				}
				setCmdAttr(cmd, "deduction_minor", deduction)
			}
			setCmdAttr(cmd, "refund_minor", refund)
			amount := refund
			cmd.AmountMinor = &amount
			cmd.Currency = *rec.Currency
		}
		return nil
	}
	opsHooks["notice"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			if _, e := requireLiveContract(ctx, tx, t.Organisation, rec, false); e != nil {
				return e
			}
			if rec.StartsAt == nil || rec.StartsAt.Before(h.Auth.Service.Now()) {
				return apperrors.InvalidRequest("The notice effective date must be in the future.")
			}
		case "after:SERVE":
			table, state, e := liveContract(ctx, tx, t.Organisation, rec)
			if e != nil {
				return e
			}
			if table == "tenancies" && state == "ACTIVE" && strings.HasPrefix(rec.Kind, "TERMINATION_") {
				return h.opsSystemTransition(ctx, tx, t, opsDomains["tenancy"], *rec.SubjectID, "NOTICE_GIVEN", "TERMINATION_NOTICE_SERVED")
			}
		}
		return nil
	}
	opsHooks["renewal_case"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			if _, e := requireLiveContract(ctx, tx, t.Organisation, rec, false); e != nil {
				return e
			}
		case "before:COMPLETE":
			successor, e := ids.ParseV7(cmd.ExternalReference)
			if e != nil || successor == *rec.SubjectID {
				return apperrors.InvalidRequest("The reference must be the successor contract identifier.")
			}
			table, _, e := liveContract(ctx, tx, t.Organisation, rec)
			if e != nil {
				return e
			}
			var state string
			var subject *ids.ID
			if e = tx.QueryRow(ctx, `SELECT state,subject_id FROM `+table+` WHERE organisation_id=$1 AND scope_id=$2 AND id=$3`, t.Organisation, rec.ScopeID, successor).Scan(&state, &subject); e != nil {
				return apperrors.Conflict("SUCCESSOR_NOT_FOUND", "The successor contract must exist in the same property.")
			}
			var prior *ids.ID
			if e = tx.QueryRow(ctx, `SELECT subject_id FROM `+table+` WHERE organisation_id=$1 AND id=$2`, t.Organisation, *rec.SubjectID).Scan(&prior); e != nil {
				return e
			}
			if slices.Contains([]string{"CANCELLED", "LOST", "TERMINATED", "CLOSED"}, state) || prior != nil && (subject == nil || *subject != *prior) {
				return apperrors.Conflict("SUCCESSOR_INVALID", "The successor contract must be live and cover the same unit.")
			}
			setCmdAttr(cmd, "successor_contract_id", successor.String())
		}
		return nil
	}
	opsHooks["move_out_case"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			if _, e := requireLiveContract(ctx, tx, t.Organisation, rec, false); e != nil {
				return e
			}
		case "before:PROPOSE_SETTLEMENT":
			var held *int64
			if e := tx.QueryRow(ctx, `SELECT (attributes->>'held_minor')::bigint FROM deposit_obligations WHERE organisation_id=$1 AND subject_id=$2 AND state IN ('HELD','DISPUTED') ORDER BY created_at DESC LIMIT 1`, t.Organisation, *rec.SubjectID).Scan(&held); e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			if held != nil && cmd.AmountMinor != nil && *cmd.AmountMinor > *held {
				return apperrors.Conflict("DEDUCTION_EXCEEDS_DEPOSIT", "Deductions above the held deposit must be pursued as a separate claim.")
			}
		case "before:CLOSE":
			table, state, e := liveContract(ctx, tx, t.Organisation, rec)
			if e != nil {
				return e
			}
			if table == "tenancies" && state == "ACTIVE" {
				return apperrors.Conflict("NOTICE_REQUIRED", "Serve or record termination notice before closing the move-out.")
			}
		case "after:CLOSE":
			table, state, e := liveContract(ctx, tx, t.Organisation, rec)
			if e != nil {
				return e
			}
			if table == "tenancies" && state == "NOTICE_GIVEN" {
				if e = h.opsSystemTransition(ctx, tx, t, opsDomains["tenancy"], *rec.SubjectID, "ENDED", "MOVE_OUT_CLOSED"); e != nil {
					return e
				}
			}
			return h.cancelFutureRent(ctx, tx, t, *rec.SubjectID)
		}
		return nil
	}
}

// cancelFutureRent cancels scheduled obligations falling due after a
// contract has ended; past-due obligations stay for arrears collection.
func (h *Handler) cancelFutureRent(ctx context.Context, tx pgx.Tx, t tenant, contract ids.ID) error {
	return h.supersedeLive(ctx, tx, t, "rent_obligation", `subject_id=$3 AND state='SCHEDULED' AND starts_at>$4`, "CANCELLED", "CONTRACT_ENDED", contract, contract, h.Auth.Service.Now().UTC())
}

// generateRentSchedule creates due-dated rent obligations for an active
// tenancy: through ends_at for fixed terms, or up to horizon periods ahead
// for periodic tenancies (the worker keeps periodic schedules topped up).
func (h *Handler) generateRentSchedule(ctx context.Context, tx pgx.Tx, t tenant, ten OpsRecord, horizon int) error {
	if ten.AmountMinor == nil || ten.Currency == nil || ten.StartsAt == nil || ten.SubjectID == nil {
		return nil
	}
	m := attr(&ten)
	months := map[string]int{"MONTHLY": 1, "QUARTERLY": 3, "ANNUAL": 12}[attrString(m, "rent_frequency")]
	if months == 0 {
		months = 1
	}
	grace, ok := attrInt(m, "grace_days")
	if !ok {
		grace = 7
	}
	d := opsDomains["rent_obligation"]
	now := h.Auth.Service.Now().UTC()
	created := 0
	for i := 0; i < 600 && created < horizon; i++ {
		due := ten.StartsAt.AddDate(0, i*months, 0)
		if ten.EndsAt != nil && !due.Before(*ten.EndsAt) {
			break
		}
		if due.Before(now.AddDate(0, -months, 0)) {
			continue
		}
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rent_obligations WHERE organisation_id=$1 AND subject_id=$2 AND kind='RENT' AND starts_at=$3)`, t.Organisation, ten.ID, due).Scan(&exists); e != nil {
			return e
		}
		created++
		if exists {
			continue
		}
		id := ids.New()
		attrs := marshalCommercial(map[string]any{"source": "TENANCY_SCHEDULE", "period_index": i, "frequency_months": months})
		if _, e := tx.Exec(ctx, `INSERT INTO rent_obligations(id,organisation_id,scope_kind,scope_id,property_id,subject_id,kind,state,title,counterparty_reference,amount_minor,currency,starts_at,ends_at,attributes,last_actor_id,created_by) VALUES($1,$2,'PROPERTY',$3,$3,$4,'RENT','SCHEDULED',$5,$6,$7,$8,$9,$10,$11,$12,$12)`,
			id, t.Organisation, ten.ScopeID, ten.ID, truncate("Rent due "+due.Format("2006-01-02"), 200), ten.CounterpartyReference, *ten.AmountMinor, *ten.Currency, due, due.Add(time.Duration(grace)*24*time.Hour), attrs, nullableAccount(t.Account)); e != nil {
			return e
		}
		rec, e := opsGet[OpsRecord](ctx, tx, d.Table, t.Organisation, id)
		if e != nil {
			return e
		}
		if e = h.opsDomainCommit(ctx, tx, t, d, rec, "SCHEDULED", "", "system:rent_schedule"); e != nil {
			return e
		}
	}
	return nil
}

func nullableAccount(id ids.ID) any {
	if id.IsZero() {
		return nil
	}
	return id
}

// scheduleRentObligations keeps periodic tenancies' rent schedules three
// periods ahead without any human session.
func (h *Handler) scheduleRentObligations(ctx context.Context, limit int) error {
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM tenancies t WHERE state IN ('ACTIVE','NOTICE_GIVEN') AND ends_at IS NULL AND (SELECT count(*) FROM rent_obligations r WHERE r.organisation_id=t.organisation_id AND r.subject_id=t.id AND r.state='SCHEDULED')<3 ORDER BY id LIMIT $1`, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	list := []ref{}
	for rows.Next() {
		var x ref
		if e = rows.Scan(&x.org, &x.id); e == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	for _, x := range list {
		if e = withServiceTx(ctx, h, func(ctx context.Context, tx pgx.Tx) error {
			ten, e := opsLock[OpsRecord](ctx, tx, "tenancies", x.org, x.id)
			if e != nil {
				return e
			}
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM rent_obligations WHERE organisation_id=$1 AND subject_id=$2 AND kind='RENT' AND starts_at<=$3`, x.org, x.id, h.Auth.Service.Now().UTC())
			if e != nil {
				return e
			}
			return h.generateRentSchedule(ctx, tx, workerTenant(x.org), ten, n+3)
		}); e != nil {
			return e
		}
	}
	return nil
}

func commercialLandSalesRules() {
	opsHooks["commercial_loi"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		state, e := opsState(ctx, tx, "commercial_deals", t.Organisation, *rec.ParentID)
		if e != nil {
			return e
		}
		if !slices.Contains([]string{"LEAD", "LOI", "NEGOTIATION"}, state) {
			return apperrors.Conflict("DEAL_NOT_OPEN", "Letters of intent are issued only before deal approval.")
		}
		if !withinDays(h, rec.EndsAt, 90) {
			return apperrors.InvalidRequest("A letter of intent expires within 90 days.")
		}
		return nil
	}
	opsHooks["commercial_terms"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			state, e := opsState(ctx, tx, "commercial_deals", t.Organisation, *rec.ParentID)
			if e != nil {
				return e
			}
			if slices.Contains([]string{"LOST", "CLOSED"}, state) {
				return apperrors.Conflict("DEAL_CLOSED", "Terms cannot be proposed on a closed or lost deal.")
			}
			m := attr(rec)
			for key, max := range map[string]int64{"rent_free_months": 36, "escalation_bps": 5000, "term_months": 600, "fit_out_contribution_minor": 1_000_000_000_000, "service_charge_minor": 1_000_000_000_000, "break_option_months": 600} {
				if _, present := m[key]; present {
					if v, ok := attrInt(m, key); !ok || v < 0 || v > max {
						return apperrors.InvalidRequest(key + " is out of range.")
					}
				}
			}
		case "before:APPROVE":
			return h.supersedeLive(ctx, tx, t, "commercial_terms", `parent_id=$3 AND state='APPROVED'`, "SUPERSEDED", "TERMS_SUPERSEDED", rec.ID, *rec.ParentID)
		}
		return nil
	}
	chainOpsHook("commercial_deal", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "before:ACTIVATE" || rec.Kind == "SALE" {
			return nil
		}
		n, e := opsCount(ctx, tx, `SELECT count(*) FROM agreements WHERE organisation_id=$1 AND subject_id=$2 AND kind='COMMERCIAL_LEASE' AND state='SIGNED'`, t.Organisation, rec.ID)
		if e != nil {
			return e
		}
		if n == 0 {
			return apperrors.Conflict("AGREEMENT_NOT_SIGNED", "A signed commercial lease agreement version is required before commencement.")
		}
		return nil
	})
	opsHooks["service_charge_budget"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			setAttr(rec, "budget_minor", *rec.AmountMinor)
			if rec.EndsAt.Sub(*rec.StartsAt) > 400*24*time.Hour {
				return apperrors.InvalidRequest("A service-charge budget period is at most one year.")
			}
		case "before:RECORD_RECONCILIATION":
			budget, _ := attrInt(attr(rec), "budget_minor")
			if rec.Currency == nil || cmd.Currency != *rec.Currency {
				return apperrors.InvalidRequest("Actual expenditure must use the budget currency.")
			}
			setCmdAttr(cmd, "actual_minor", *cmd.AmountMinor)
			setCmdAttr(cmd, "variance_minor", *cmd.AmountMinor-budget)
		}
		return nil
	}
	opsHooks["land_terms"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			state, e := opsState(ctx, tx, "land_transactions", t.Organisation, *rec.ParentID)
			if e != nil {
				return e
			}
			if state != "DRAFT" && state != "DUE_DILIGENCE" {
				return apperrors.Conflict("LAND_TRANSACTION_NOT_OPEN", "Terms are negotiated before the land transaction is approved.")
			}
		case "before:APPROVE":
			return h.supersedeLive(ctx, tx, t, "land_terms", `parent_id=$3 AND state='APPROVED'`, "SUPERSEDED", "TERMS_SUPERSEDED", rec.ID, *rec.ParentID)
		}
		return nil
	}
	opsHooks["land_allocation"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			if !withinDays(h, rec.EndsAt, 30) {
				return apperrors.InvalidRequest("A provisional allocation expires within 30 days.")
			}
		case "before:CONFIRM":
			// Title authority is Verification's: a verification reference is
			// mandatory and never inferred from the business user's claim.
			if !opsText(attrString(mergedAttrs(rec, cmd), "title_verification_reference"), 3, 300) {
				return apperrors.InvalidRequest("A Verification Service title/authority reference is required to confirm an allocation.")
			}
		}
		return nil
	}
	dealOpen := []string{"CANCELLED", "COMPLETED", "WITHDRAWN", "FALLEN_THROUGH", "LOST", "CLOSED"}
	subjectDealState := func(ctx context.Context, tx pgx.Tx, org ids.ID, rec *OpsRecord) (string, error) {
		table, e := opsSubjectTable(ctx, tx, org, "sale_transactions|land_transactions|commercial_deals", rec.ScopeID, *rec.SubjectID)
		if e != nil {
			return "", e
		}
		return opsState(ctx, tx, table, org, *rec.SubjectID)
	}
	opsHooks["due_diligence_item"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		state, e := subjectDealState(ctx, tx, t.Organisation, rec)
		if e != nil {
			return e
		}
		if slices.Contains(dealOpen, state) {
			return apperrors.Conflict("TRANSACTION_CLOSED", "Due diligence is recorded only on an open transaction.")
		}
		return nil
	}
	opsHooks["closing_milestone"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			state, e := subjectDealState(ctx, tx, t.Organisation, rec)
			if e != nil {
				return e
			}
			if slices.Contains(dealOpen, state) {
				return apperrors.Conflict("TRANSACTION_CLOSED", "Closing milestones are planned only on an open transaction.")
			}
		case "before:SUBMIT_EVIDENCE":
			// Payment milestones are proven only by Payment-reconciled
			// settlement, never by an uploaded receipt alone.
			if rec.Kind == "DEPOSIT_PAID" || rec.Kind == "BALANCE_PAID" {
				o, e := financialObligationFor(ctx, tx, t.Organisation, cmd.ExternalReference, "closing_milestone:"+rec.ID.String(), nil, nil)
				if e != nil {
					return e
				}
				if o.State != "SETTLED" || o.Settled != o.Amount {
					return apperrors.Conflict("SETTLEMENT_NOT_VERIFIED", "Payment readback does not show the milestone payment as settled.")
				}
				setCmdAttr(cmd, "verified_settled_minor", o.Settled)
				setCmdAttr(cmd, "financial_obligation_id", o.ID.String())
			}
		}
		return nil
	}
	gate := func(ctx context.Context, tx pgx.Tx, org, subject ids.ID, dd, closing bool) error {
		if dd {
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM due_diligence_items WHERE organisation_id=$1 AND subject_id=$2 AND state IN ('OPEN','IN_REVIEW','FAILED')`, org, subject)
			if e != nil {
				return e
			}
			if n > 0 {
				return apperrors.Conflict("DUE_DILIGENCE_OUTSTANDING", "All due-diligence conditions must be satisfied or independently waived.")
			}
		}
		if closing {
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM closing_milestones WHERE organisation_id=$1 AND subject_id=$2 AND state IN ('PENDING','EVIDENCED','REJECTED')`, org, subject)
			if e != nil {
				return e
			}
			if n > 0 {
				return apperrors.Conflict("CLOSING_MILESTONES_OUTSTANDING", "All closing milestones must be independently confirmed or waived.")
			}
		}
		return nil
	}
	chainOpsHook("land_transaction", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "before:APPROVE":
			return gate(ctx, tx, t.Organisation, rec.ID, true, false)
		case "before:COMPLETE":
			return gate(ctx, tx, t.Organisation, rec.ID, true, true)
		}
		return nil
	})
	chainOpsHook("sale_transaction", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "before:CONTRACT":
			return gate(ctx, tx, t.Organisation, rec.ID, true, false)
		case "before:COMPLETE":
			return gate(ctx, tx, t.Organisation, rec.ID, true, true)
		case "after:WITHDRAW", "after:FALL_THROUGH":
			return h.supersedeLive(ctx, tx, t, "sale_allocation", `parent_id=$3 AND state IN ('HELD','CONVERTED')`, "RELEASED", "SALE_ENDED", rec.ID, rec.ID)
		}
		return nil
	})
	opsHooks["sale_allocation"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" && stage != "before:CONVERT" {
			return nil
		}
		var state string
		var unit *ids.ID
		if e := tx.QueryRow(ctx, `SELECT state,subject_id FROM sale_transactions WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, *rec.ParentID).Scan(&state, &unit); e != nil {
			return opsNotFound(e)
		}
		if !slices.Contains([]string{"LISTED", "UNDER_OFFER", "APPROVED", "CONTRACTED"}, state) {
			return apperrors.Conflict("SALE_NOT_OPEN", "Unit holds require an open sale.")
		}
		if unit != nil && *unit != *rec.SubjectID {
			return apperrors.Conflict("UNIT_MISMATCH", "The hold must be on the unit being sold.")
		}
		if stage == "create" && !withinDays(h, rec.EndsAt, 14) {
			return apperrors.InvalidRequest("A unit hold expires within 14 days.")
		}
		// Inventory conservation across sales and off-plan allocations.
		n, e := opsCount(ctx, tx, `SELECT count(*) FROM development_allocations WHERE organisation_id=$1 AND subject_id=$2 AND state IN ('HELD','RESERVED','ALLOCATED','CONTRACTED','HANDED_OVER')`, t.Organisation, *rec.SubjectID)
		if e != nil {
			return e
		}
		if n > 0 {
			return apperrors.Conflict("UNIT_ALREADY_ALLOCATED", "The unit is already held or allocated in a development allocation.")
		}
		return nil
	}
	chainOpsHook("development_allocation", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		n, e := opsCount(ctx, tx, `SELECT count(*) FROM sale_allocations WHERE organisation_id=$1 AND subject_id=$2 AND state IN ('HELD','CONVERTED')`, t.Organisation, *rec.SubjectID)
		if e != nil {
			return e
		}
		if n > 0 {
			return apperrors.Conflict("UNIT_ALREADY_ALLOCATED", "The unit is held by a live sale.")
		}
		return nil
	})
}

func developmentMaintenanceRules() {
	opsHooks["progress_certification"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		m := attr(rec)
		milestone, ok := attrID(m, "milestone_id")
		if !ok {
			return apperrors.InvalidRequest("milestone_id is required.")
		}
		if pct, ok := attrInt(m, "percent_complete"); !ok || pct < 0 || pct > 100 {
			return apperrors.InvalidRequest("percent_complete must be 0-100.")
		}
		var state string
		if e := tx.QueryRow(ctx, `SELECT state FROM construction_milestones WHERE organisation_id=$1 AND scope_id=$2 AND id=$3`, t.Organisation, rec.ScopeID, milestone).Scan(&state); e != nil {
			return apperrors.NotFound("MILESTONE_NOT_FOUND", "The milestone must belong to the same project.")
		}
		if state == "CERTIFIED" {
			return apperrors.Conflict("MILESTONE_CERTIFIED", "The milestone is already certified.")
		}
		return nil
	}
	chainOpsHook("construction_milestone", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "before:CERTIFY" {
			return nil
		}
		// A business user's claim alone never certifies progress.
		n, e := opsCount(ctx, tx, `SELECT count(*) FROM progress_certifications WHERE organisation_id=$1 AND scope_id=$2 AND attributes->>'milestone_id'=$3 AND state='CERTIFIED'`, t.Organisation, rec.ScopeID, rec.ID.String())
		if e != nil {
			return e
		}
		if n == 0 {
			return apperrors.Conflict("PROGRESS_NOT_CERTIFIED", "A professionally certified progress certification is required.")
		}
		return nil
	})
	opsHooks["project_variation"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage == "create" && !opsText(attrString(attr(rec), "justification"), 5, 2000) {
			return apperrors.InvalidRequest("A justification is required for a project variation.")
		}
		return nil
	}
	opsHooks["vendor_quote"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" && stage != "before:ACCEPT" {
			return nil
		}
		state, e := opsState(ctx, tx, "work_orders", t.Organisation, *rec.ParentID)
		if e != nil {
			return e
		}
		if !slices.Contains([]string{"DRAFT", "QUOTE_REQUESTED", "QUOTED"}, state) {
			return apperrors.Conflict("WORK_ORDER_NOT_QUOTING", "Quotes are collected before work-order approval.")
		}
		return nil
	}
	opsHooks["provider_assignment"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		state, e := opsState(ctx, tx, "work_orders", t.Organisation, *rec.ParentID)
		if e != nil {
			return e
		}
		if !slices.Contains([]string{"APPROVED", "ASSIGNED", "IN_PROGRESS", "REWORK"}, state) {
			return apperrors.Conflict("WORK_ORDER_NOT_APPROVED", "Providers are assigned only to approved work.")
		}
		return nil
	}
	chainOpsHook("work_order", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "before:APPROVE":
			var quotes int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM vendor_quote_refs WHERE organisation_id=$1 AND parent_id=$2 AND state<>'WITHDRAWN'`, t.Organisation, rec.ID).Scan(&quotes); e != nil {
				return e
			}
			if quotes == 0 {
				return nil
			}
			var amount int64
			var currency string
			if e := tx.QueryRow(ctx, `SELECT amount_minor,currency FROM vendor_quote_refs WHERE organisation_id=$1 AND parent_id=$2 AND state='ACCEPTED'`, t.Organisation, rec.ID).Scan(&amount, &currency); e != nil {
				return apperrors.Conflict("NO_ACCEPTED_QUOTE", "An independently accepted vendor quote is required before approval.")
			}
			// The approval binds the accepted quote, not a typed amount.
			cmd.AmountMinor, cmd.Currency = &amount, currency
		case "before:COMPLETE":
			var assigned, completed int
			if e := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state NOT IN ('DECLINED','CANCELLED')),count(*) FILTER (WHERE state='COMPLETED') FROM provider_assignment_refs WHERE organisation_id=$1 AND parent_id=$2`, t.Organisation, rec.ID).Scan(&assigned, &completed); e != nil {
				return e
			}
			if assigned > 0 && completed == 0 {
				return apperrors.Conflict("PROVIDER_NOT_COMPLETE", "The assigned provider must record completion first.")
			}
		}
		return nil
	})
	opsHooks["warranty"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		if wo, ok := attrID(attr(rec), "work_order_id"); ok {
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM work_orders WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state IN ('COMPLETED','VERIFIED')`, t.Organisation, rec.ScopeID, wo)
			if e != nil {
				return e
			}
			if n == 0 {
				return apperrors.Conflict("WORK_ORDER_NOT_COMPLETE", "A workmanship warranty references completed work in the same property.")
			}
		}
		return nil
	}
	opsHooks["manager_performance"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		state, e := opsState(ctx, tx, "manager_assignment_refs", t.Organisation, *rec.ParentID)
		if e != nil {
			return e
		}
		if state == "PROPOSED" || state == "REVOKED" {
			return apperrors.Conflict("ASSIGNMENT_NOT_ACTIVE", "Performance is recorded for an activated management assignment.")
		}
		return nil
	}
	opsHooks["professional_finding"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			state, e := opsState(ctx, tx, "professional_appointments", t.Organisation, *rec.ParentID)
			if e != nil {
				return e
			}
			if state != "IN_PROGRESS" && state != "REPORT_SUBMITTED" {
				return apperrors.Conflict("APPOINTMENT_NOT_ACTIVE", "Findings come from an appointment in progress or with a submitted report.")
			}
			if len(cmd.EvidenceRefs) == 0 {
				return apperrors.InvalidRequest("Findings require the professional's Media evidence reference.")
			}
		case "before:LINK_CONDITION":
			item, e := ids.ParseV7(cmd.ExternalReference)
			if e != nil {
				return apperrors.InvalidRequest("The reference must be the due-diligence condition identifier.")
			}
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM due_diligence_items WHERE organisation_id=$1 AND scope_id=$2 AND id=$3 AND state NOT IN ('WAIVED','SATISFIED')`, t.Organisation, rec.ScopeID, item)
			if e != nil {
				return e
			}
			if n == 0 {
				return apperrors.Conflict("CONDITION_NOT_OPEN", "The finding must link to an open due-diligence condition in the same property.")
			}
		}
		return nil
	}
}

func riskAccessFinanceRules() {
	opsHooks["risk_signal"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := mergedAttrs(rec, cmd)
		for k := range m {
			if slices.Contains(protectedRiskAttributes, strings.ToLower(k)) {
				return apperrors.InvalidRequest("Protected personal characteristics may not be used as risk signals.")
			}
		}
		switch stage {
		case "create":
			if !opsText(attrString(m, "source_service"), 3, 100) || !opsText(attrString(m, "source_reference"), 3, 300) {
				return apperrors.InvalidRequest("Risk signals must be source-backed (source_service and source_reference).")
			}
		case "before:LINK_CASE":
			id, e := ids.ParseV7(cmd.ExternalReference)
			if e != nil {
				return apperrors.InvalidRequest("The reference must be the risk case identifier.")
			}
			n, e := opsCount(ctx, tx, `SELECT count(*) FROM risk_cases WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state NOT IN ('CLOSED')`, t.Organisation, rec.ScopeID, id)
			if e != nil {
				return e
			}
			if n == 0 {
				return apperrors.Conflict("RISK_CASE_NOT_OPEN", "Signals link only to an open risk case in the same property.")
			}
		}
		return nil
	}
	opsHooks["protection_case"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		state, e := opsState(ctx, tx, "risk_cases", t.Organisation, *rec.ParentID)
		if e != nil {
			return e
		}
		if state == "CLOSED" || state == "CLEARED" {
			return apperrors.Conflict("RISK_CASE_CLOSED", "Protection cases are opened from a live risk case.")
		}
		return nil
	}
	opsHooks["access_event"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		grant := opsDomains["access_grant"]
		switch stage {
		case "create":
			state, e := opsState(ctx, tx, "access_grants", t.Organisation, *rec.ParentID)
			if e != nil {
				return e
			}
			if rec.Kind == "ISSUE_COMMAND" && state != "REQUESTED" || rec.Kind == "REVOKE_COMMAND" && state != "ACTIVE" && state != "REQUESTED" {
				return apperrors.Conflict("GRANT_STATE", "The command does not match the grant's current state.")
			}
		case "after:RECORD_CONFIRMED", "after:MANUALLY_RESOLVED", "after:RESOLVE_MANUALLY":
			// Only provider-confirmed (or independently resolved) outcomes
			// change the grant; unknown results never imply a lock opened.
			state, e := opsState(ctx, tx, "access_grants", t.Organisation, *rec.ParentID)
			if e != nil {
				return e
			}
			if rec.Kind == "ISSUE_COMMAND" && state == "REQUESTED" {
				return h.opsSystemTransition(ctx, tx, t, grant, *rec.ParentID, "ACTIVE", "PROVIDER_CONFIRMED_ISSUE")
			}
			if rec.Kind == "REVOKE_COMMAND" && (state == "ACTIVE" || state == "REQUESTED") {
				return h.opsSystemTransition(ctx, tx, t, grant, *rec.ParentID, "REVOKED", "PROVIDER_CONFIRMED_REVOKE")
			}
		}
		return nil
	}
	opsHooks["device_health_snapshot"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "after:CREATE" || rec.Kind != "OFFLINE" && rec.Kind != "TAMPER" {
			return nil
		}
		state, e := opsState(ctx, tx, "access_devices", t.Organisation, *rec.ParentID)
		if e != nil {
			return e
		}
		if state == "ACTIVE" {
			return h.opsSystemTransition(ctx, tx, t, opsDomains["access_device"], *rec.ParentID, "OFFLINE", "HEALTH_"+rec.Kind)
		}
		return nil
	}
	opsHooks["financing_consent"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		state, e := opsState(ctx, tx, "finance_offer_refs", t.Organisation, *rec.ParentID)
		if e != nil {
			return e
		}
		if !slices.Contains([]string{"REQUESTED", "CONSENTED", "SUBMITTED"}, state) {
			return apperrors.Conflict("FINANCING_CLOSED", "Consent is recorded only for an open financing request.")
		}
		if _, ok := stringList(attr(rec), "data_scope", 1, len(financingDataScopes), financingDataScopes); !ok {
			return apperrors.InvalidRequest("An explicit data_scope from the supported field list is required.")
		}
		if !withinDays(h, rec.EndsAt, 365) || len(cmd.EvidenceRefs) == 0 {
			return apperrors.InvalidRequest("Consent evidence and an expiry within one year are required.")
		}
		return nil
	}
	chainOpsHook("financing_offer", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "before:SUBMIT" {
			return nil
		}
		n, e := opsCount(ctx, tx, `SELECT count(*) FROM financing_consents WHERE organisation_id=$1 AND parent_id=$2 AND kind='DATA_SHARING' AND state='GRANTED' AND ends_at>$3`, t.Organisation, rec.ID, h.Auth.Service.Now().UTC())
		if e != nil {
			return e
		}
		if n == 0 {
			return apperrors.Conflict("CONSENT_REQUIRED", "A current data-sharing consent is required before sharing with a financing partner.")
		}
		return nil
	})
	opsHooks["insurance_compliance_check"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "after:CREATE" {
			return nil
		}
		required, _ := attrInt(attr(rec), "required_cover_minor")
		var policy *ids.ID
		e := tx.QueryRow(ctx, `SELECT id FROM insurance_policy_refs WHERE organisation_id=$1 AND property_id=$2 AND kind=$3 AND state IN ('ACTIVE','EXPIRING') AND ends_at>=$4 AND ($5=0 OR coalesce(amount_minor,0)>=$5) ORDER BY ends_at DESC LIMIT 1`, t.Organisation, rec.ScopeID, rec.Kind, rec.EndsAt, required).Scan(&policy)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		to, action := "NON_COMPLIANT", "COVERAGE_GAP_DETECTED"
		if policy != nil {
			to, action = "COMPLIANT", "COVERAGE_VERIFIED"
			if _, e = tx.Exec(ctx, `UPDATE insurance_compliance_checks SET attributes=attributes||jsonb_build_object('matched_policy_id',$3::text),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, rec.ID, policy.String()); e != nil {
				return e
			}
		}
		if e = h.opsSystemTransition(ctx, tx, t, d, rec.ID, to, action); e != nil {
			return e
		}
		out, e := opsGet[OpsRecord](ctx, tx, d.Table, t.Organisation, rec.ID)
		if e == nil {
			*rec = out
		}
		return e
	}
}

func platformRules() {
	opsHooks["api_credential"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := mergedAttrs(rec, cmd)
		if e := noSecrets(m); e != nil {
			return e
		}
		switch stage {
		case "create":
			state, e := opsState(ctx, tx, "integration_clients", t.Organisation, *rec.ParentID)
			if e != nil {
				return e
			}
			if state == "REVOKED" {
				return apperrors.Conflict("INTEGRATION_REVOKED", "Credentials cannot be issued for a revoked integration.")
			}
			ref := attrString(m, "credential_reference")
			if ref == "" || !credentialRef.MatchString(ref) || !fingerprintPattern.MatchString(attrString(m, "fingerprint_sha256")) {
				return apperrors.InvalidRequest("A secret-manager credential_reference and a SHA-256 fingerprint are required; secrets are never stored.")
			}
			if !withinDays(h, rec.EndsAt, 366) {
				return apperrors.InvalidRequest("API credentials expire within one year.")
			}
		case "before:START_ROTATION":
			if !credentialRef.MatchString(cmd.ExternalReference) || !fingerprintPattern.MatchString(attrString(cmd.Attributes, "next_fingerprint_sha256")) {
				return apperrors.InvalidRequest("Rotation requires the successor credential reference and fingerprint.")
			}
		case "before:COMPLETE_ROTATION":
			setCmdAttr(cmd, "credential_reference", rec.ExternalReference)
			setCmdAttr(cmd, "fingerprint_sha256", attrString(m, "next_fingerprint_sha256"))
			setCmdAttr(cmd, "rotated_at", h.Auth.Service.Now().UTC())
		}
		return nil
	}
	opsHooks["webhook_subscription"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := mergedAttrs(rec, cmd)
		if e := noSecrets(m); e != nil {
			return e
		}
		if stage != "create" && stage != "before:VERIFY" && stage != "before:RESUME" {
			return nil
		}
		state, e := opsState(ctx, tx, "integration_clients", t.Organisation, *rec.ParentID)
		if e != nil {
			return e
		}
		if state == "REVOKED" || stage != "create" && state != "ACTIVE" {
			return apperrors.Conflict("INTEGRATION_NOT_ACTIVE", "Webhook delivery requires an active integration.")
		}
		if stage != "create" {
			return nil
		}
		if e = safeWebhookURL(attrString(m, "url")); e != nil {
			return e
		}
		if ref := attrString(m, "signing_secret_reference"); ref == "" || !credentialRef.MatchString(ref) {
			return apperrors.InvalidRequest("A signing secret-manager reference is required (HMAC-SHA256 signatures, replay window enforced).")
		}
		if _, ok := stringList(m, "event_types", 1, 50, nil); !ok {
			return apperrors.InvalidRequest("At least one event type is required.")
		}
		return nil
	}
	chainOpsHook("integration_client", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "after:REVOKE":
			if e := h.supersedeLive(ctx, tx, t, "api_credential", `parent_id=$3 AND state IN ('ACTIVE','ROTATING')`, "REVOKED", "INTEGRATION_REVOKED", rec.ID, rec.ID); e != nil {
				return e
			}
			return h.supersedeLive(ctx, tx, t, "webhook_subscription", `parent_id=$3 AND state IN ('PENDING_VERIFICATION','ACTIVE','PAUSED')`, "REVOKED", "INTEGRATION_REVOKED", rec.ID, rec.ID)
		case "after:SUSPEND":
			return h.supersedeLive(ctx, tx, t, "webhook_subscription", `parent_id=$3 AND state='ACTIVE'`, "PAUSED", "INTEGRATION_SUSPENDED", rec.ID, rec.ID)
		}
		return nil
	})
	opsHooks["fx_rate_ref"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := attr(rec)
		switch stage {
		case "create":
			base, quote, rate := attrString(m, "base_currency"), attrString(m, "quote_currency"), attrString(m, "rate")
			r, ok := new(big.Rat).SetString(rate)
			if !currencyPattern.MatchString(base) || !currencyPattern.MatchString(quote) || base == quote || !decimalRate.MatchString(rate) || !ok || r.Sign() <= 0 {
				return apperrors.InvalidRequest("base_currency, quote_currency and a positive decimal rate string are required.")
			}
			if !opsText(attrString(m, "source_reference"), 3, 300) {
				return apperrors.InvalidRequest("FX rates must be source-backed; rates are never invented.")
			}
			if rec.EndsAt.Sub(*rec.StartsAt) > 7*24*time.Hour {
				return apperrors.InvalidRequest("An FX rate freshness window is at most seven days.")
			}
		case "before:ACTIVATE":
			if !rec.EndsAt.After(h.Auth.Service.Now()) {
				return apperrors.Conflict("FX_RATE_STALE", "A stale rate cannot be activated.")
			}
			return h.supersedeLive(ctx, tx, t, "fx_rate_ref", `kind=$3 AND state='ACTIVE' AND attributes->>'base_currency'=$4 AND attributes->>'quote_currency'=$5`, "SUPERSEDED", "FX_RATE_SUPERSEDED", rec.ID, rec.Kind, attrString(m, "base_currency"), attrString(m, "quote_currency"))
		}
		return nil
	}
	opsHooks["organisation_change_case"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := attr(rec)
		required := map[string]string{"NAME_CHANGE": "new_legal_name", "BENEFICIAL_OWNER_CHANGE": "beneficial_owner_reference", "REPRESENTATIVE_CHANGE": "new_representative_reference", "MERGER": "counterparty_organisation_reference", "ACQUISITION": "counterparty_organisation_reference", "REGISTERED_ADDRESS_CHANGE": "new_registered_address"}[rec.Kind]
		switch stage {
		case "create":
			if !opsText(attrString(m, required), 2, 300) {
				return apperrors.InvalidRequest(required + " is required for " + rec.Kind + ".")
			}
		case "before:APPLY":
			// Identity changes are applied only after Verification's
			// reference and an independent approval are both recorded.
			if !opsText(rec.ExternalReference, 3, 300) || rec.ApprovalID == nil {
				return apperrors.Conflict("CHANGE_NOT_VERIFIED", "Verification and independent approval must precede applying a change.")
			}
			if rec.Kind == "NAME_CHANGE" {
				var previous string
				if e := tx.QueryRow(ctx, `SELECT registered_name FROM organisations WHERE id=$1 FOR UPDATE`, t.Organisation).Scan(&previous); e != nil {
					return e
				}
				if _, e := tx.Exec(ctx, `UPDATE organisations SET registered_name=$2 WHERE id=$1`, t.Organisation, attrString(m, required)); e != nil {
					return e
				}
				setCmdAttr(cmd, "previous_registered_name", previous)
			}
		}
		return nil
	}
	opsHooks["closure_case"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "before:APPROVE", "before:CLOSE":
			// Continuity: contracts, money and disputes must be resolved or
			// transferred before closure can be approved or completed.
			var open struct{ Tenancies, Deposits, Reservations, Obligations, Disputes int }
			if e := tx.QueryRow(ctx, `SELECT
			 (SELECT count(*) FROM tenancies WHERE organisation_id=$1 AND state IN ('ACTIVE','NOTICE_GIVEN')),
			 (SELECT count(*) FROM deposit_obligations WHERE organisation_id=$1 AND state IN ('HELD','RELEASE_REQUESTED','DISPUTED','COLLECTION_PENDING')),
			 (SELECT count(*) FROM reservations WHERE organisation_id=$1 AND state IN ('HOLD','PENDING_PAYMENT','PENDING_APPROVAL','CONFIRMED','CHECKED_IN','DISPUTED')),
			 (SELECT count(*) FROM financial_obligations WHERE organisation_id=$1 AND state IN ('APPROVED','PAYMENT_REQUESTED','UNKNOWN_PENDING_READBACK','DISPUTED')),
			 (SELECT count(*) FROM disputes WHERE organisation_id=$1 AND state NOT IN ('RESOLVED','WITHDRAWN'))`, t.Organisation).Scan(&open.Tenancies, &open.Deposits, &open.Reservations, &open.Obligations, &open.Disputes); e != nil {
				return e
			}
			if open.Tenancies+open.Deposits+open.Reservations+open.Obligations+open.Disputes > 0 {
				return apperrors.Conflict("CLOSURE_BLOCKED", "Open tenancies, deposits, reservations, financial obligations or disputes must be resolved or transferred first.")
			}
		case "before:ENTER_RETENTION":
			until, e := time.Parse(time.RFC3339, attrString(cmd.Attributes, "retain_until"))
			if e != nil || until.Before(h.Auth.Service.Now().AddDate(1, 0, 0)) {
				return apperrors.InvalidRequest("retain_until (RFC3339, at least one year ahead) is required for record retention.")
			}
		case "after:ENTER_RETENTION":
			_, e := tx.Exec(ctx, `UPDATE organisations SET state='CLOSURE_PENDING' WHERE id=$1 AND state IN ('ACTIVE','RESTRICTED','SUSPENDED')`, t.Organisation)
			return e
		case "after:CLOSE":
			_, e := tx.Exec(ctx, `UPDATE organisations SET state='CLOSED' WHERE id=$1 AND state='CLOSURE_PENDING'`, t.Organisation)
			return e
		}
		return nil
	}
}

// safeWebhookURL accepts only public HTTPS endpoints (no loopback, private
// or link-local literals, no credentials in the URL).
func safeWebhookURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(raw) > 500 {
		return apperrors.InvalidRequest("Webhook URLs must be absolute https URLs without credentials.")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") {
		return apperrors.InvalidRequest("Webhook URLs must be public endpoints.")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return apperrors.InvalidRequest("Webhook URLs must be public endpoints.")
	}
	if p := u.Port(); p != "" {
		if n, e := strconv.Atoi(p); e != nil || n != 443 && n < 1024 {
			return apperrors.InvalidRequest("Unsupported webhook port.")
		}
	}
	return nil
}

func hospitalityRules() {
	opsHooks["housekeeping_checklist"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage == "create" {
			if _, ok := stringList(attr(rec), "items", 1, 100, nil); !ok {
				return apperrors.InvalidRequest("1-100 distinct checklist items are required.")
			}
		}
		return nil
	}
	chainOpsHook("housekeeping_task", func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		checklist, ok := attrID(attr(rec), "checklist_id")
		if !ok {
			return nil
		}
		var raw json.RawMessage
		var kind string
		if e := tx.QueryRow(ctx, `SELECT attributes->'items',kind FROM housekeeping_checklists WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND state='ACTIVE'`, t.Organisation, rec.ScopeID, checklist).Scan(&raw, &kind); e != nil {
			return apperrors.Conflict("CHECKLIST_NOT_ACTIVE", "The checklist must be active in the same property.")
		}
		switch stage {
		case "create":
			if kind != rec.Kind {
				return apperrors.InvalidRequest("The checklist kind must match the task kind.")
			}
		case "before:SUBMIT_INSPECTION":
			var items []string
			_ = json.Unmarshal(raw, &items)
			results, _ := cmd.Attributes["checklist_results"].(map[string]any)
			for _, item := range items {
				if _, ok := results[item].(bool); !ok {
					return apperrors.InvalidRequest("Every checklist item must be recorded before inspection.")
				}
			}
		}
		return nil
	})
	opsHooks["lost_found_item"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			if !opsText(attrString(attr(rec), "found_location"), 2, 200) {
				return apperrors.InvalidRequest("found_location is required.")
			}
		case "before:DISPOSE":
			if rec.Kind == "DOCUMENT" || rec.Kind == "CASH" {
				return apperrors.Conflict("DISPOSAL_NOT_PERMITTED", "Documents and cash are handed to the authorities or owner, never disposed.")
			}
			if h.Auth.Service.Now().Before(rec.CreatedAt.Add(90 * 24 * time.Hour)) {
				return apperrors.Conflict("RETENTION_ACTIVE", "Items are retained for at least 90 days before disposal.")
			}
		}
		return nil
	}
	opsHooks["linen_stock"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := mergedAttrs(rec, cmd)
		par, ok1 := attrInt(m, "par_level")
		hand, ok2 := attrInt(m, "on_hand")
		if stage != "create" && stage != "before:RECORD_COUNT" {
			return nil
		}
		if !ok1 || !ok2 || par < 0 || hand < 0 || par > 1_000_000 || hand > 1_000_000 {
			return apperrors.InvalidRequest("Non-negative par_level and on_hand counts are required.")
		}
		if stage == "before:RECORD_COUNT" {
			if _, ok := attrInt(cmd.Attributes, "on_hand"); !ok {
				return apperrors.InvalidRequest("A counted on_hand quantity is required.")
			}
			setCmdAttr(cmd, "below_par", hand < par)
			setCmdAttr(cmd, "counted_at", h.Auth.Service.Now().UTC())
		} else {
			setAttr(rec, "below_par", hand < par)
		}
		return nil
	}
	opsHooks["shortlet_setting"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		switch stage {
		case "create":
			m := attr(rec)
			guests, ok1 := attrInt(m, "max_guests")
			nights, ok2 := attrInt(m, "min_nights")
			if !clockPattern.MatchString(attrString(m, "check_in_time")) || !clockPattern.MatchString(attrString(m, "check_out_time")) || !ok1 || guests < 1 || guests > 100 || !ok2 || nights < 1 || nights > 365 {
				return apperrors.InvalidRequest("check_in_time, check_out_time (HH:MM), max_guests and min_nights are required.")
			}
		case "before:ACTIVATE":
			return h.supersedeLive(ctx, tx, t, "shortlet_setting", `scope_id=$3 AND state='ACTIVE'`, "SUPERSEDED", "SETTINGS_SUPERSEDED", rec.ID, rec.ScopeID)
		}
		return nil
	}
	opsHooks["property_service"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage == "create" && !slices.Contains([]string{"PER_ITEM", "PER_PERSON", "PER_HOUR", "PER_NIGHT", "PER_STAY"}, attrString(attr(rec), "pricing_unit")) {
			return apperrors.InvalidRequest("pricing_unit must be PER_ITEM, PER_PERSON, PER_HOUR, PER_NIGHT or PER_STAY.")
		}
		return nil
	}
	opsHooks["content_suggestion"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		m := mergedAttrs(rec, cmd)
		switch stage {
		case "create":
			if !slices.Contains([]string{"MEDIA_EXTRACTION", "OPERATOR_NOTE", "SPACE_GRAPH", "DOCUMENT", "REVIEW_TOPIC"}, attrString(m, "source_kind")) || len(cmd.EvidenceRefs) == 0 || !opsText(attrString(m, "suggested_text"), 2, 4000) {
				return apperrors.InvalidRequest("Suggestions must be source-backed (source_kind, evidence references) with suggested_text.")
			}
		case "before:ACCEPT_EDITED":
			if !opsText(attrString(cmd.Attributes, "edited_text"), 2, 4000) {
				return apperrors.InvalidRequest("edited_text is required when accepting with edits.")
			}
		case "before:PUBLISH":
			if _, e := ids.ParseV7(cmd.ExternalReference); e != nil {
				return apperrors.InvalidRequest("Publication references the approved content version identifier.")
			}
		}
		return nil
	}
	opsHooks["experiment"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		m := attr(rec)
		if !opsText(attrString(m, "hypothesis"), 5, 1000) || !opsText(attrString(m, "primary_metric"), 2, 100) || !withinDays(h, rec.EndsAt, 90) {
			return apperrors.InvalidRequest("hypothesis, primary_metric and an end within 90 days are required.")
		}
		if _, ok := stringList(m, "variants", 2, 5, nil); !ok {
			return apperrors.InvalidRequest("Two to five distinct variants are required.")
		}
		return nil
	}
	opsHooks["market_benchmark"] = func(ctx context.Context, tx pgx.Tx, h *Handler, t tenant, d *opsDomain, rec *OpsRecord, stage string, cmd *OpsRecordTransition) error {
		if stage != "create" {
			return nil
		}
		m := attr(rec)
		_, hasValue := attrInt(m, "value")
		if !opsText(attrString(m, "source_reference"), 3, 300) || !opsText(attrString(m, "licence_reference"), 3, 300) || !hasValue || rec.EndsAt.Sub(*rec.StartsAt) > 90*24*time.Hour {
			return apperrors.InvalidRequest("Licensed, source-backed benchmarks need source_reference, licence_reference, an integer value and a validity window of at most 90 days.")
		}
		return nil
	}
}

// residentialArrears projects outstanding rent and charges per contract. The
// outstanding amount uses Payment-readback settlement on the linked financial
// obligation; Business never computes settlement itself.
func (h *Handler) residentialArrears(w http.ResponseWriter, r *http.Request) {
	h.opsList(w, r, "residential:read", `SELECT jsonb_build_object('subject_id',x.subject_id,'currency',x.currency,'overdue_obligations',count(*),
	 'overdue_minor',sum(x.amount_minor),'verified_settled_minor',sum(coalesce(o.settled_minor,0)),'outstanding_minor',sum(x.amount_minor-coalesce(o.settled_minor,0)),
	 'oldest_due_at',min(x.starts_at),'settlement_source','payment-service-readback')
	 FROM rent_obligations x LEFT JOIN financial_obligations o ON o.organisation_id=x.organisation_id AND o.id::text=x.attributes->>'financial_obligation_id'
	 WHERE x.organisation_id=$1 AND x.property_id=$2 AND x.state='OVERDUE'
	 GROUP BY x.subject_id,x.currency ORDER BY min(x.starts_at),x.subject_id LIMIT $3 OFFSET $4`)
}

// assetServiceHistory is the derived service history of one asset or space:
// work orders, warranties and preventive schedules with their transitions.
func (h *Handler) assetServiceHistory(w http.ResponseWriter, r *http.Request) {
	asset, e := ids.ParseV7(chi.URLParam(r, "id"))
	if e != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A canonical asset identifier is required."))
		return
	}
	h.opsList(w, r, "maintenance:read", `SELECT jsonb_build_object('entity_type',x.entity_type,'id',x.id,'kind',x.kind,'state',x.state,'title',x.title,'amount_minor',x.amount_minor,'currency',x.currency,'starts_at',x.starts_at,'ends_at',x.ends_at,'created_at',x.created_at,
	 'history',(SELECT coalesce(jsonb_agg(jsonb_build_object('action',hh.action,'from',hh.from_state,'to',hh.to_state,'at',hh.created_at) ORDER BY hh.sequence),'[]') FROM operations_history hh WHERE hh.organisation_id=x.organisation_id AND hh.entity_id=x.id))
	 FROM (SELECT 'work_order' entity_type,organisation_id,id,kind,state,title,amount_minor,currency,starts_at,ends_at,created_at FROM work_orders WHERE organisation_id=$1 AND property_id=$2 AND subject_id=$3
	  UNION ALL SELECT 'warranty',organisation_id,id,kind,state,title,amount_minor,currency,starts_at,ends_at,created_at FROM warranties WHERE organisation_id=$1 AND property_id=$2 AND subject_id=$3
	  UNION ALL SELECT 'preventive_schedule',organisation_id,id,kind,state,title,amount_minor,currency,starts_at,ends_at,created_at FROM preventive_schedules WHERE organisation_id=$1 AND property_id=$2 AND subject_id=$3) x
	 ORDER BY x.created_at DESC,x.id LIMIT $4 OFFSET $5`, asset)
}

// OpsDomainGuards exposes each governed table's generated transition edges
// so contract tests can prove the database guard enforces exactly them.
func OpsDomainGuards() map[string][]string {
	out := map[string][]string{}
	for _, d := range opsDomains {
		out[d.Table] = append([]string(nil), d.Edges...)
	}
	return out
}
