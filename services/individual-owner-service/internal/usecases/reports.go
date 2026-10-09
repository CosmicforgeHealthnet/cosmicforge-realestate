package usecases

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/reportpdf"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

// Owner finance, reports and market intelligence (§13.25) and the owner
// dashboard (§13.1). Reports combine owner-domain business state with the
// payment projections this service holds (payment-service read-back and
// events) — never another service's database. Exported reports are PDFs
// generated asynchronously and stored in media-document-service. Market
// insights are advisory statistics with model, version, input provenance
// and confidence; they never decide anything for the owner.

const (
	reportMaxAttempts = 5
	insightTTL        = 24 * time.Hour
	insightModel      = "cf-comparable-median"
	insightVersion    = "1.0.0"
)

// Money is an amount per currency.
type Money map[string]int64

func (m Money) add(cur string, v int64) {
	if v != 0 {
		m[cur] += v
	}
}

// ShortletFigures are short-let performance figures.
type ShortletFigures struct {
	NightsBooked    int64 `json:"nights_booked"`
	AvailableNights int64 `json:"available_nights"`
	OccupancyBps    int64 `json:"occupancy_bps"`
	Revenue         Money `json:"revenue" doc:"Collected less refunds, by check-in date"`
	ADR             Money `json:"adr" doc:"Average daily rate"`
}

// PortfolioReport is the owner's portfolio performance for a period.
type PortfolioReport struct {
	From               string           `json:"from"`
	To                 string           `json:"to"`
	PropertyID         *ids.ID          `json:"property_id"`
	Properties         int64            `json:"properties"`
	RentableUnits      int64            `json:"rentable_units" doc:"Lettable units, or the property itself when it has none"`
	OccupiedUnits      int64            `json:"occupied_units"`
	OccupancyBps       int64            `json:"occupancy_bps"`
	ActiveTenancies    int64            `json:"active_tenancies"`
	ListingsByStatus   map[string]int64 `json:"listings_by_status"`
	RentDue            Money            `json:"rent_due"`
	RentCollected      Money            `json:"rent_collected"`
	Arrears            Money            `json:"arrears"`
	OverdueObligations int64            `json:"overdue_obligations"`
	DepositsHeld       Money            `json:"deposits_held"`
	Shortlet           ShortletFigures  `json:"shortlet"`
	MaintenanceSpend   Money            `json:"maintenance_spend"`
	ProfessionalFees   Money            `json:"professional_fees"`
	NetIncome          Money            `json:"net_income" doc:"Rent collected + short-let revenue − maintenance − professional fees"`
	OpenMaintenance    int64            `json:"open_maintenance"`
	OpenDisputes       int64            `json:"open_disputes"`
	YieldBps           *int64           `json:"yield_bps" doc:"Gross yield when the valued properties use one currency; null without valuations"`
	Yield              *YieldFigures    `json:"yield" doc:"Gross rental yield over properties with a recorded valuation (basis disclosed); null when none is valued"`
	Sources            string           `json:"sources"`
}

const propsCTE = `WITH props AS (SELECT DISTINCT property_id FROM property_access WHERE account_id = $1
	AND (valid_until IS NULL OR valid_until > $2) AND ($3::uuid IS NULL OR property_id = $3))`

// portfolioFigures computes the report for an account's properties.
func (s *Service) portfolioFigures(ctx context.Context, account ids.ID, propertyID *ids.ID, from, to time.Time) (PortfolioReport, error) {
	q, now := s.dbq(ctx), s.now()
	r := PortfolioReport{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), PropertyID: propertyID, ListingsByStatus: map[string]int64{},
		RentDue: Money{}, RentCollected: Money{}, Arrears: Money{}, DepositsHeld: Money{}, MaintenanceSpend: Money{}, ProfessionalFees: Money{},
		NetIncome: Money{}, Shortlet: ShortletFigures{Revenue: Money{}, ADR: Money{}},
		Sources: "Owner-domain records; money figures are payment-service projections (read-back and events) or recorded off-platform evidence."}
	leaf := `('UNIT','ROOM','SHOP','OFFICE','WAREHOUSE')`
	active := `('ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING','MOVE_OUT_PENDING')`
	err := q.QueryRow(ctx, propsCTE+`
		SELECT (SELECT count(*) FROM props),
		       (SELECT count(*) FROM property_units u WHERE u.property_id IN (SELECT property_id FROM props) AND u.status = 'ACTIVE' AND u.unit_type IN `+leaf+`)
		     + (SELECT count(*) FROM props p WHERE NOT EXISTS (SELECT 1 FROM property_units u WHERE u.property_id = p.property_id AND u.status = 'ACTIVE' AND u.unit_type IN `+leaf+`)),
		       (SELECT count(DISTINCT coalesce(t.unit_id, t.property_id)) FROM tenancies t WHERE t.property_id IN (SELECT property_id FROM props) AND t.status IN `+active+`),
		       (SELECT count(*) FROM tenancies t WHERE t.property_id IN (SELECT property_id FROM props) AND t.status IN `+active+`),
		       (SELECT count(*) FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id WHERE t.property_id IN (SELECT property_id FROM props) AND o.status = 'OVERDUE'),
		       (SELECT count(*) FROM maintenance_requests m WHERE m.property_id IN (SELECT property_id FROM props) AND m.status NOT IN ('COMPLETED','CANCELLED')),
		       (SELECT count(*) FROM domain_disputes d WHERE d.account_id = $1 AND d.status IN ('OPEN','UNDER_REVIEW','AWAITING_INFORMATION'))`,
		account, now, propertyID).Scan(&r.Properties, &r.RentableUnits, &r.OccupiedUnits, &r.ActiveTenancies, &r.OverdueObligations, &r.OpenMaintenance, &r.OpenDisputes)
	if err != nil {
		return r, err
	}
	if r.RentableUnits > 0 {
		r.OccupancyBps = r.OccupiedUnits * 10000 / r.RentableUnits
	}
	rows, err := q.Query(ctx, propsCTE+` SELECT status, count(*) FROM listings WHERE property_id IN (SELECT property_id FROM props) GROUP BY status`, account, now, propertyID)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return r, err
		}
		r.ListingsByStatus[st] = n
	}
	rows.Close()
	money := func(sql string, args []any, scan func(cur string, vals []int64)) error {
		rows, err := q.Query(ctx, propsCTE+sql, append([]any{account, now, propertyID}, args...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cur string
			vals := make([]int64, len(rows.FieldDescriptions())-1)
			dest := []any{&cur}
			for i := range vals {
				dest = append(dest, &vals[i])
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			scan(cur, vals)
		}
		return rows.Err()
	}
	end := to.AddDate(0, 0, 1)
	if err := money(` SELECT o.currency, coalesce(sum(o.amount_minor),0)::bigint, coalesce(sum(o.paid_minor),0)::bigint,
			coalesce(sum(CASE WHEN o.status IN ('OVERDUE','PARTIAL') THEN o.amount_minor - o.paid_minor ELSE 0 END),0)::bigint
		  FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id
		 WHERE t.property_id IN (SELECT property_id FROM props) AND o.due_date >= $4 AND o.due_date <= $5 AND o.status <> 'WAIVED' GROUP BY o.currency`,
		[]any{from, to}, func(c string, v []int64) {
			r.RentDue.add(c, v[0])
			r.RentCollected.add(c, v[1])
			r.Arrears.add(c, v[2])
		}); err != nil {
		return r, err
	}
	if err := money(` SELECT currency, coalesce(sum(funded_minor - released_minor),0)::bigint FROM deposit_obligations
		 WHERE property_id IN (SELECT property_id FROM props) AND status IN ('FUNDED','HELD','RELEASE_PENDING','DEDUCTION_PROPOSED','DISPUTED') GROUP BY currency`,
		nil, func(c string, v []int64) { r.DepositsHeld.add(c, v[0]) }); err != nil {
		return r, err
	}
	nights := map[string]int64{}
	if err := money(` SELECT currency, coalesce(sum(paid_minor - refunded_minor),0)::bigint, coalesce(sum(check_out - check_in),0)::bigint FROM shortlet_bookings
		 WHERE property_id IN (SELECT property_id FROM props) AND status IN ('CONFIRMED','CHECKED_IN','CHECKED_OUT','COMPLETED','DISPUTED')
		   AND check_in >= $4 AND check_in <= $5 GROUP BY currency`,
		[]any{from, to}, func(c string, v []int64) {
			r.Shortlet.Revenue.add(c, v[0])
			nights[c] += v[1]
			r.Shortlet.NightsBooked += v[1]
		}); err != nil {
		return r, err
	}
	for c, n := range nights {
		if n > 0 && r.Shortlet.Revenue[c] > 0 {
			r.Shortlet.ADR[c] = r.Shortlet.Revenue[c] / n
		}
	}
	var lets int64
	if err := q.QueryRow(ctx, propsCTE+` SELECT count(*) FROM listings WHERE property_id IN (SELECT property_id FROM props) AND transaction_type = 'SHORT_LET'
		AND status IN ('PUBLISHED','PAUSED')`, account, now, propertyID).Scan(&lets); err != nil {
		return r, err
	}
	r.Shortlet.AvailableNights = lets * int64(end.Sub(from)/(24*time.Hour))
	if r.Shortlet.AvailableNights > 0 {
		r.Shortlet.OccupancyBps = min(10000, r.Shortlet.NightsBooked*10000/r.Shortlet.AvailableNights)
	}
	if err := money(` SELECT j.currency, coalesce(sum(j.paid_minor),0)::bigint FROM maintenance_jobs j JOIN maintenance_requests m ON m.id = j.request_id
		 WHERE m.property_id IN (SELECT property_id FROM props) AND j.accepted_at >= $4 AND j.accepted_at < $5 GROUP BY j.currency`,
		[]any{from, end}, func(c string, v []int64) { r.MaintenanceSpend.add(c, v[0]) }); err != nil {
		return r, err
	}
	if err := money(` SELECT a.currency, coalesce(sum(a.paid_minor),0)::bigint FROM professional_appointments a LEFT JOIN sale_transactions st ON st.id = a.sale_id
		 WHERE coalesce(a.property_id, st.property_id) IN (SELECT property_id FROM props) AND a.created_at >= $4 AND a.created_at < $5 GROUP BY a.currency`,
		[]any{from, end}, func(c string, v []int64) { r.ProfessionalFees.add(c, v[0]) }); err != nil {
		return r, err
	}
	for c, v := range r.RentCollected {
		r.NetIncome[c] += v
	}
	for c, v := range r.Shortlet.Revenue {
		r.NetIncome[c] += v
	}
	for c, v := range r.MaintenanceSpend {
		r.NetIncome[c] -= v
	}
	for c, v := range r.ProfessionalFees {
		r.NetIncome[c] -= v
	}
	y, err := s.yieldFigures(ctx, account, propertyID, from, to)
	if err != nil {
		return r, err
	}
	if r.Yield = y; y != nil && len(y.GrossYieldBps) == 1 {
		for _, v := range y.GrossYieldBps {
			r.YieldBps = &v
		}
	}
	return r, nil
}

// reportPeriod parses an optional period (default: the last 90 days).
func (s *Service) reportPeriod(from, to string) (time.Time, time.Time, error) {
	end := dayOf(s.now())
	start := end.AddDate(0, 0, -90)
	var err error
	if to != "" {
		if end, err = time.Parse(time.DateOnly, to); err != nil {
			return start, end, apperrors.InvalidRequest("to must be a date (YYYY-MM-DD).")
		}
	}
	if from != "" {
		if start, err = time.Parse(time.DateOnly, from); err != nil {
			return start, end, apperrors.InvalidRequest("from must be a date (YYYY-MM-DD).")
		}
	} else if to != "" {
		start = end.AddDate(0, 0, -90)
	}
	if end.Before(start) || end.Sub(start) > 3*366*24*time.Hour {
		return start, end, apperrors.InvalidRequest("The period must be from ≤ to and at most three years.")
	}
	return start, end, nil
}

// PortfolioReportFor returns the live portfolio report.
func (s *Service) PortfolioReportFor(ctx context.Context, subj authz.Subject, propertyID ids.ID, from, to string) (PortfolioReport, error) {
	if err := s.reportAccess(ctx, subj, propertyID); err != nil {
		return PortfolioReport{}, err
	}
	a, b, err := s.reportPeriod(from, to)
	if err != nil {
		return PortfolioReport{}, err
	}
	return s.portfolioFigures(ctx, accountOf(subj), idp(propertyID), a, b)
}

func (s *Service) reportAccess(ctx context.Context, subj authz.Subject, propertyID ids.ID) error {
	if propertyID.IsZero() {
		_, err := s.authorize(ctx, subj, authz.ActReportsRead, ids.Nil)
		return err
	}
	_, err := s.authorize(ctx, subj, authz.Read(authz.CapReportsView), propertyID)
	return err
}

// ReportExportInput queues a report document.
type ReportExportInput struct {
	ReportType string  `json:"report_type" v:"required,enum=PORTFOLIO|FINANCIAL|OCCUPANCY|SHORTLET|MAINTENANCE"`
	From       string  `json:"from" v:"required,format=date"`
	To         string  `json:"to" v:"required,format=date"`
	PropertyID *ids.ID `json:"property_id,omitempty"`
}

// OwnerReportView is a generated (or queued) report.
type OwnerReportView struct {
	ID          ids.ID          `json:"id"`
	ReportType  string          `json:"report_type"`
	From        *string         `json:"from"`
	To          *string         `json:"to"`
	PropertyID  *ids.ID         `json:"property_id"`
	Status      string          `json:"status"`
	Summary     json.RawMessage `json:"summary" doc:"Key figures captured when generated"`
	MediaID     *ids.ID         `json:"media_id" doc:"PDF in media-document-service"`
	DownloadURL *string         `json:"download_url,omitempty" doc:"Short-lived; on GET of a ready report"`
	ExpiresAt   *time.Time      `json:"download_expires_at,omitempty"`
	Failure     *string         `json:"failure_reason"`
	CreatedAt   time.Time       `json:"created_at"`
	CompletedAt *time.Time      `json:"completed_at"`
}

func ownerReportView(r db.ReportSnapshot) OwnerReportView {
	return OwnerReportView{ID: r.ID, ReportType: r.ReportType, From: dateStr(r.PeriodStart), To: dateStr(r.PeriodEnd), PropertyID: r.PropertyID, Status: r.Status,
		Summary: r.Summary, MediaID: r.MediaID, Failure: r.FailureReason, CreatedAt: r.CreatedAt, CompletedAt: r.CompletedAt}
}

// ExportReport queues a PDF report.
func (s *Service) ExportReport(ctx context.Context, subj authz.Subject, in ReportExportInput) (OwnerReportView, error) {
	if err := schema.Validate(in); err != nil {
		return OwnerReportView{}, err
	}
	if s.Media == nil {
		return OwnerReportView{}, unavailable("Report export", "media-document-service")
	}
	a, b, err := s.reportPeriod(in.From, in.To)
	if err != nil {
		return OwnerReportView{}, err
	}
	var out OwnerReportView
	err = s.InTx(ctx, func(ctx context.Context) error {
		if err := s.reportAccess(ctx, subj, deref(in.PropertyID)); err != nil {
			return err
		}
		r, err := s.q.InsertReport(ctx, mustTx(ctx), db.InsertReportParams{ID: ids.New(), AccountID: accountOf(subj), ReportType: in.ReportType, PeriodStart: &a,
			PeriodEnd: &b, PropertyID: in.PropertyID, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = ownerReportView(r)
		return s.record(ctx, "report.export", "report_snapshot", r.ID, map[string]any{"report_type": in.ReportType, "from": in.From, "to": in.To}, nil)
	})
	return out, err
}

// ListReports lists the caller's reports.
func (s *Service) ListReports(ctx context.Context, subj authz.Subject) ([]OwnerReportView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActReportsRead, ids.Nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListReports(ctx, s.dbq(ctx), accountOf(subj))
	out := make([]OwnerReportView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ownerReportView(r))
	}
	return out, err
}

// GetReport returns a report with a short-lived download link when ready.
func (s *Service) GetReport(ctx context.Context, subj authz.Subject, id ids.ID) (OwnerReportView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActReportsRead, ids.Nil); err != nil {
		return OwnerReportView{}, err
	}
	r, err := s.q.GetReport(ctx, s.dbq(ctx), id)
	if isNoRows(err) || (err == nil && r.AccountID != accountOf(subj)) {
		return OwnerReportView{}, notFound("report")
	}
	if err != nil {
		return OwnerReportView{}, err
	}
	v := ownerReportView(r)
	if r.Status == "READY" && s.Media != nil {
		d, err := s.Media.DownloadURL(ctx, *r.MediaID, "owner_report")
		if err != nil {
			return v, downstream("media-document-service", err)
		}
		v.DownloadURL, v.ExpiresAt = &d.URL, &d.ExpiresAt
	}
	return v, nil
}

// RunReportJobs renders queued reports and stores them in media-document-service.
func (s *Service) RunReportJobs(ctx context.Context, batch int32) error {
	if s.Media == nil {
		return nil
	}
	queued, err := s.q.QueuedReports(ctx, s.Pool, batch)
	if err != nil {
		return err
	}
	for _, r := range queued {
		summary, pdf, err := s.renderReport(ctx, r)
		var m media.Media
		if err == nil {
			m, err = s.Media.Upload(ctx, media.UploadRequest{OwnerAccountID: r.AccountID, Filename: lower(r.ReportType) + "-" + r.ID.String() + ".pdf",
				MimeType: "application/pdf", DocumentType: "OWNER_REPORT", Title: strings.ReplaceAll(lower(r.ReportType), "_", " ") + " report", Content: pdf},
				"report:"+r.ID.String())
		}
		if err != nil {
			s.Logger.WarnContext(ctx, "report generation deferred", "report_id", r.ID, "error", err)
			if err := s.InTx(ctx, func(ctx context.Context) error {
				return s.q.FailReportAttempt(ctx, mustTx(ctx), db.FailReportAttemptParams{ID: r.ID, FailureReason: ptr(truncate(err.Error(), 500)),
					MaxAttempts: reportMaxAttempts, Now: s.now()})
			}); err != nil {
				return err
			}
			continue
		}
		raw, _ := json.Marshal(summary)
		if err := s.InTx(ctx, func(ctx context.Context) error {
			if err := s.q.CompleteReport(ctx, mustTx(ctx), db.CompleteReportParams{ID: r.ID, Summary: raw, MediaID: &m.MediaID, CompletedAt: ptr(s.now())}); err != nil {
				return err
			}
			return s.record(ctx, "report.generated", "report_snapshot", r.ID, map[string]any{"media_id": m.MediaID}, nil)
		}); err != nil {
			return err
		}
	}
	return nil
}

func money(m Money) string {
	if len(m) == 0 {
		return "0"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %.2f", k, float64(m[k])/100))
	}
	return strings.Join(parts, ", ")
}

// renderReport computes the figures and renders the PDF.
func (s *Service) renderReport(ctx context.Context, r db.ReportSnapshot) (any, []byte, error) {
	if r.ReportType == "PRIVACY_EXPORT" {
		return s.renderPrivacyExport(ctx, r)
	}
	f, err := s.portfolioFigures(ctx, r.AccountID, r.PropertyID, *r.PeriodStart, *r.PeriodEnd)
	if err != nil {
		return nil, nil, err
	}
	sections := map[string]reportpdf.Section{
		"portfolio": {Heading: "Portfolio", Lines: []string{fmt.Sprintf("Properties: %d", f.Properties), fmt.Sprintf("Rentable units: %d", f.RentableUnits),
			fmt.Sprintf("Listings by status: %v", f.ListingsByStatus), fmt.Sprintf("Open maintenance requests: %d", f.OpenMaintenance),
			fmt.Sprintf("Open disputes: %d", f.OpenDisputes)}},
		"occupancy": {Heading: "Occupancy", Lines: []string{fmt.Sprintf("Occupied units: %d of %d (%.1f%%)", f.OccupiedUnits, f.RentableUnits, float64(f.OccupancyBps)/100),
			fmt.Sprintf("Active tenancies: %d", f.ActiveTenancies)}},
		"financial": {Heading: "Income and expenses", Lines: []string{"Rent due: " + money(f.RentDue), "Rent collected: " + money(f.RentCollected),
			"Arrears: " + money(f.Arrears), fmt.Sprintf("Overdue obligations: %d", f.OverdueObligations), "Deposits held: " + money(f.DepositsHeld),
			"Short-let revenue: " + money(f.Shortlet.Revenue), "Maintenance spend: " + money(f.MaintenanceSpend),
			"Professional fees: " + money(f.ProfessionalFees), "Net income: " + money(f.NetIncome), "Yield: not computed (no valuations recorded)."}},
		"shortlet": {Heading: "Short-let", Lines: []string{fmt.Sprintf("Nights booked: %d of %d (%.1f%%)", f.Shortlet.NightsBooked, f.Shortlet.AvailableNights,
			float64(f.Shortlet.OccupancyBps)/100), "Revenue: " + money(f.Shortlet.Revenue), "Average daily rate: " + money(f.Shortlet.ADR)}},
		"maintenance": {Heading: "Maintenance", Lines: []string{fmt.Sprintf("Open requests: %d", f.OpenMaintenance), "Spend: " + money(f.MaintenanceSpend)}},
	}
	order := map[string][]string{"PORTFOLIO": {"portfolio", "occupancy", "financial", "shortlet", "maintenance"}, "FINANCIAL": {"financial"},
		"OCCUPANCY": {"occupancy", "portfolio"}, "SHORTLET": {"shortlet"}, "MAINTENANCE": {"maintenance", "financial"}}
	var secs []reportpdf.Section
	for _, k := range order[r.ReportType] {
		secs = append(secs, sections[k])
	}
	secs = append(secs, reportpdf.Section{Heading: "Sources", Lines: []string{f.Sources, "Generated " + s.now().Format(time.RFC3339) + "."}})
	title := strings.ToUpper(r.ReportType[:1]) + lower(r.ReportType[1:]) + " report"
	return f, reportpdf.Render(title, "CosmicForge Real Estate - "+f.From+" to "+f.To, secs), nil
}

// renderPrivacyExport compiles the account's personal data held by the
// owner domain (never other services' data, never secrets or hashes).
func (s *Service) renderPrivacyExport(ctx context.Context, r db.ReportSnapshot) (any, []byte, error) {
	q := s.dbq(ctx)
	var email, phone, status string
	var created time.Time
	if err := q.QueryRow(ctx, `SELECT email, coalesce(phone_e164,''), status, created_at FROM owner_accounts WHERE account_id = $1`, r.AccountID).
		Scan(&email, &phone, &status, &created); err != nil {
		return nil, nil, err
	}
	lines := func(sql string, args ...any) ([]string, error) {
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				return nil, err
			}
			out = append(out, l)
		}
		return out, rows.Err()
	}
	profile, err := lines(`SELECT concat_ws(' | ', 'Name: ' || coalesce(preferred_name, ''), 'Languages: ' || array_to_string(languages, ','),
		'Timezone: ' || timezone, 'Preferred channel: ' || preferred_channel, 'Public profile: ' || public_profile::text, 'Bio: ' || coalesce(bio, ''))
		FROM owner_profiles WHERE account_id = $1`, r.AccountID)
	if err != nil {
		return nil, nil, err
	}
	contacts, err := lines(`SELECT concat_ws(' | ', contact_type, coalesce(name, label, ''), coalesce(email, phone_e164),
		CASE WHEN verified_at IS NOT NULL THEN 'verified' ELSE 'unverified' END)
		FROM owner_contacts WHERE account_id = $1 AND removed_at IS NULL ORDER BY created_at`, r.AccountID)
	if err != nil {
		return nil, nil, err
	}
	props, err := lines(`SELECT concat_ws(' | ', p.title, a.line1, a.city, a.country_code, p.status) FROM properties p
		LEFT JOIN property_addresses a ON a.property_id = p.id WHERE p.owner_account_id = $1 ORDER BY p.created_at`, r.AccountID)
	if err != nil {
		return nil, nil, err
	}
	consents, err := lines(`SELECT concat_ws(' | ', purpose, decision, policy_version, to_char(created_at, 'YYYY-MM-DD')) FROM consents WHERE account_id = $1 ORDER BY created_at`, r.AccountID)
	if err != nil {
		return nil, nil, err
	}
	sessions, err := lines(`SELECT concat_ws(' | ', to_char(created_at, 'YYYY-MM-DD HH24:MI'), coalesce(user_agent, 'unknown device'),
		CASE WHEN revoked_at IS NULL THEN 'active' ELSE 'ended' END) FROM auth_sessions WHERE account_id = $1 ORDER BY created_at DESC LIMIT 50`, r.AccountID)
	if err != nil {
		return nil, nil, err
	}
	summary := map[string]any{"properties": len(props), "contacts": len(contacts), "consent_decisions": len(consents), "sessions": len(sessions)}
	secs := []reportpdf.Section{
		{Heading: "Account", Lines: []string{"Email: " + email, "Phone: " + phone, "Status: " + status, "Created: " + created.Format(time.DateOnly)}},
		{Heading: "Profile", Lines: profile}, {Heading: "Contacts", Lines: contacts}, {Heading: "Properties", Lines: props},
		{Heading: "Privacy consents", Lines: consents}, {Heading: "Recent sessions", Lines: sessions},
		{Heading: "Not included", Lines: []string{"Identity documents and bank details are held by verification-service and payment-service; request them there.",
			"Passwords, MFA secrets and recovery codes are stored only as one-way hashes and are never exported."}},
	}
	return summary, reportpdf.Render("Personal data export", "Generated "+s.now().Format(time.DateOnly), secs), nil
}

// ---- market insights ----

// InsightView is an advisory market insight.
type InsightView struct {
	InsightType  string          `json:"insight_type"`
	ModelName    string          `json:"model_name"`
	ModelVersion string          `json:"model_version"`
	Inputs       json.RawMessage `json:"inputs" doc:"Criteria and sample size (other owners' listings are never identified)"`
	Result       json.RawMessage `json:"result"`
	Confidence   float64         `json:"confidence"`
	Advisory     bool            `json:"advisory" doc:"Always true: insights inform, the owner decides"`
	GeneratedAt  time.Time       `json:"generated_at"`
	ExpiresAt    time.Time       `json:"expires_at"`
}

func insightView(m db.MarketInsight) InsightView {
	var in map[string]any
	_ = json.Unmarshal(m.Inputs, &in)
	delete(in, "comparable_listing_ids") // provenance kept internally
	pub, _ := json.Marshal(in)
	return InsightView{InsightType: m.InsightType, ModelName: m.ModelName, ModelVersion: m.ModelVersion, Inputs: pub, Result: m.Result,
		Confidence: m.Confidence, Advisory: m.Advisory, GeneratedAt: m.GeneratedAt, ExpiresAt: m.ExpiresAt}
}

type comparable struct {
	id        ids.ID
	amount    int64
	published time.Time
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := p * float64(len(sorted)-1)
	lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
	return sorted[lo] + int64(float64(sorted[hi]-sorted[lo])*(pos-float64(lo)))
}

// MarketInsights returns (or computes) advisory price and trend insights
// for a listing from comparable listings in the same area.
func (s *Service) MarketInsights(ctx context.Context, subj authz.Subject, listingID ids.ID) ([]InsightView, error) {
	if listingID.IsZero() {
		return nil, apperrors.InvalidRequest("listing_id is required.")
	}
	var out []InsightView
	err := s.InTx(ctx, func(ctx context.Context) error {
		tx, now := mustTx(ctx), s.now()
		l, err := s.q.GetListing(ctx, tx, listingID)
		if isNoRows(err) {
			return notFound(AggListing)
		}
		if err != nil {
			return err
		}
		if _, err := s.authorize(ctx, subj, authz.ActMarketInsightsRead, ids.Nil); err != nil {
			return err
		}
		if _, err := s.authorize(ctx, subj, authz.Read(authz.CapReportsView), l.PropertyID); err != nil {
			if apperrors.CodeOf(err) == "PROPERTY_NOT_FOUND" {
				return notFound(AggListing)
			}
			return err
		}
		for _, t := range []string{"PRICE_RECOMMENDATION", "MARKET_TREND"} {
			m, err := s.q.LatestInsight(ctx, tx, db.LatestInsightParams{PropertyID: l.PropertyID, InsightType: t, ListingID: &l.ID, Now: now})
			if isNoRows(err) {
				break
			}
			if err != nil {
				return err
			}
			out = append(out, insightView(m))
		}
		if len(out) == 2 {
			return nil
		}
		out = nil
		var cur, period, ptype, city, state, cc string
		var own int64
		err = tx.QueryRow(ctx, `SELECT pv.currency, pv.base_period, pv.base_amount_minor, p.property_type, a.city, a.state_region, a.country_code
			FROM listings l JOIN price_schedules ps ON ps.listing_id = l.id JOIN pricing_versions pv ON pv.id = ps.active_pricing_version_id
			JOIN properties p ON p.id = l.property_id JOIN property_addresses a ON a.property_id = p.id WHERE l.id = $1`, l.ID).
			Scan(&cur, &period, &own, &ptype, &city, &state, &cc)
		if isNoRows(err) {
			return precondition("PRICING_NOT_SET", "Price the listing first; insights compare like-for-like prices.")
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT l.id, pv.base_amount_minor, coalesce(l.published_at, l.first_published_at)
			FROM listings l JOIN price_schedules ps ON ps.listing_id = l.id JOIN pricing_versions pv ON pv.id = ps.active_pricing_version_id
			JOIN properties p ON p.id = l.property_id JOIN property_addresses a ON a.property_id = p.id
			WHERE l.property_id <> $1 AND l.transaction_type = $2 AND l.first_published_at IS NOT NULL
			  AND coalesce(l.published_at, l.first_published_at) >= $3 AND p.property_type = $4
			  AND lower(a.city) = lower($5) AND lower(a.state_region) = lower($6) AND a.country_code = $7
			  AND pv.currency = $8 AND pv.base_period = $9
			ORDER BY coalesce(l.published_at, l.first_published_at) DESC LIMIT 500`,
			l.PropertyID, l.TransactionType, now.AddDate(-1, 0, 0), ptype, city, state, cc, cur, period)
		if err != nil {
			return err
		}
		var comps []comparable
		for rows.Next() {
			var c comparable
			if err := rows.Scan(&c.id, &c.amount, &c.published); err != nil {
				rows.Close()
				return err
			}
			comps = append(comps, c)
		}
		rows.Close()
		var amounts, recent, older []int64
		var idsUsed []ids.ID
		for _, c := range comps {
			amounts, idsUsed = append(amounts, c.amount), append(idsUsed, c.id)
			if now.Sub(c.published) <= 90*24*time.Hour {
				recent = append(recent, c.amount)
			} else {
				older = append(older, c.amount)
			}
		}
		slices.Sort(amounts)
		slices.Sort(recent)
		slices.Sort(older)
		inputs := map[string]any{"criteria": map[string]any{"transaction_type": l.TransactionType, "property_type": ptype, "city": city, "state_region": state,
			"country_code": cc, "currency": cur, "base_period": period, "window_days": 365}, "comparable_count": len(amounts),
			"own_price_minor": own, "comparable_listing_ids": idsUsed}
		price := map[string]any{"status": "INSUFFICIENT_DATA", "minimum_comparables": 3}
		conf := 0.0
		if len(amounts) >= 3 {
			med, p25, p75 := percentile(amounts, .5), percentile(amounts, .25), percentile(amounts, .75)
			below := sort.Search(len(amounts), func(i int) bool { return amounts[i] >= own })
			spread := 0.0
			if med > 0 {
				spread = math.Min(1, float64(p75-p25)/float64(med))
			}
			conf = math.Round(math.Min(1, float64(len(amounts))/20)*(1-spread/2)*1000) / 1000
			position := "WITHIN_RANGE"
			switch {
			case own < p25:
				position = "BELOW_RANGE"
			case own > p75:
				position = "ABOVE_RANGE"
			}
			price = map[string]any{"status": "OK", "currency": cur, "base_period": period, "median_minor": med, "range_low_minor": p25, "range_high_minor": p75,
				"own_percentile": below * 100 / len(amounts), "position": position}
		}
		trend := map[string]any{"status": "INSUFFICIENT_DATA", "minimum_comparables_each_window": 3}
		trendConf := 0.0
		if len(recent) >= 3 && len(older) >= 3 {
			mr, mo := percentile(recent, .5), percentile(older, .5)
			trend = map[string]any{"status": "OK", "recent_median_minor": mr, "earlier_median_minor": mo, "change_bps": (mr - mo) * 10000 / max(mo, 1),
				"recent_window_days": 90}
			trendConf = math.Round(math.Min(1, float64(min(len(recent), len(older)))/10)*1000) / 1000
		}
		rawIn, _ := json.Marshal(inputs)
		for _, x := range []struct {
			t    string
			res  map[string]any
			conf float64
		}{{"PRICE_RECOMMENDATION", price, conf}, {"MARKET_TREND", trend, trendConf}} {
			res, _ := json.Marshal(x.res)
			m, err := s.q.InsertInsight(ctx, tx, db.InsertInsightParams{ID: ids.New(), AccountID: accountOf(subj), PropertyID: l.PropertyID, ListingID: &l.ID,
				InsightType: x.t, ModelName: insightModel, ModelVersion: insightVersion, Inputs: rawIn, Result: res, Confidence: x.conf, GeneratedAt: now,
				ExpiresAt: now.Add(insightTTL)})
			if err != nil {
				return err
			}
			out = append(out, insightView(m))
		}
		return nil
	})
	return out, err
}

// ---- dashboard, calendar, audit activity ----

// DashboardView is the owner's home summary.
type DashboardView struct {
	Properties         int64            `json:"properties"`
	ListingsByStatus   map[string]int64 `json:"listings_by_status"`
	ActiveTenancies    int64            `json:"active_tenancies"`
	OccupancyBps       int64            `json:"occupancy_bps"`
	ActionItems        map[string]int64 `json:"action_items" doc:"Counts of items awaiting the owner"`
	Portfolio          PortfolioSummary `json:"portfolio"`
	RentCollected30d   Money            `json:"rent_collected_30d" doc:"Payment truth: collected as reported by payment-service (or recorded off-platform evidence)"`
	ShortletRevenue30d Money            `json:"shortlet_revenue_30d" doc:"Payment truth: collected less refunds, by check-in date"`
	Arrears            Money            `json:"arrears" doc:"Business obligations overdue or partly paid (last 30 days)"`
	ObligationsDue30d  Money            `json:"obligations_due_next_30d" doc:"Business obligations: unpaid rent due in the next 30 days (not payment truth)"`
	DepositsHeld       Money            `json:"deposits_held"`
	MoneyBasis         string           `json:"money_basis"`
	QuickActions       []QuickAction    `json:"quick_actions" doc:"Shortcuts to normal domain commands/queries; each is authorized and idempotent as usual when called"`
	Upcoming           []CalendarEntry  `json:"upcoming" doc:"Next 14 days"`
	Preferences        DashboardPrefs   `json:"preferences"`
}

// PortfolioSummary counts the portfolio across every owner workflow.
type PortfolioSummary struct {
	PropertiesByStatus   map[string]int64 `json:"properties_by_status"`
	PropertyVerification map[string]int64 `json:"property_verification" doc:"Latest PROPERTY_AUTHORITY status per property (NOT_STARTED when none)"`
	IdentityVerification string           `json:"identity_verification" doc:"Latest IDENTITY verification status (NOT_STARTED when none)"`
	Pipeline             map[string]int64 `json:"pipeline" doc:"Open inquiries, upcoming viewings, open applications, sent rental offers, active sales, open sale offers, upcoming bookings, stays in progress"`
	Operations           map[string]int64 `json:"operations" doc:"Active manager assignments, open maintenance, open professional appointments, open disputes, ready reports (30 days), statutory dates due in 30 days, open stay extensions"`
}

// QuickAction is a dashboard shortcut.
type QuickAction struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Count  int64  `json:"count,omitempty"`
}

// DashboardPrefs are the owner's dashboard preferences.
type DashboardPrefs struct {
	Widgets           []string `json:"widgets" v:"max=20,enum=PORTFOLIO|ACTION_ITEMS|CALENDAR|RENT|SHORTLET|MAINTENANCE|LEADS|INSIGHTS"`
	DefaultPropertyID *ids.ID  `json:"default_property_id,omitempty"`
}

// Dashboard returns counts, action items, money highlights and the next
// two weeks of the calendar.
func (s *Service) Dashboard(ctx context.Context, subj authz.Subject) (DashboardView, error) {
	if _, err := s.authorize(ctx, subj, authz.ActReportsRead, ids.Nil); err != nil {
		return DashboardView{}, err
	}
	acc, now := accountOf(subj), s.now()
	f, err := s.portfolioFigures(ctx, acc, nil, dayOf(now).AddDate(0, 0, -30), dayOf(now))
	if err != nil {
		return DashboardView{}, err
	}
	v := DashboardView{Properties: f.Properties, ListingsByStatus: f.ListingsByStatus, ActiveTenancies: f.ActiveTenancies, OccupancyBps: f.OccupancyBps,
		RentCollected30d: f.RentCollected, ShortletRevenue30d: f.Shortlet.Revenue, Arrears: f.Arrears, DepositsHeld: f.DepositsHeld,
		ObligationsDue30d: Money{}, ActionItems: map[string]int64{}, Preferences: DashboardPrefs{Widgets: []string{}},
		MoneyBasis: "Collected and held amounts are payment-service projections (or recorded off-platform evidence); due and arrears are business obligations."}
	counts := []struct{ key, sql string }{
		{"new_inquiries", `SELECT count(*) FROM inquiries WHERE property_id IN (SELECT property_id FROM props) AND status = 'NEW'`},
		{"viewing_requests", `SELECT count(*) FROM viewing_requests WHERE property_id IN (SELECT property_id FROM props) AND status = 'REQUESTED'`},
		{"applications_to_review", `SELECT count(*) FROM rental_applications WHERE property_id IN (SELECT property_id FROM props) AND status IN ('SUBMITTED','UNDER_REVIEW')`},
		{"maintenance_awaiting_decision", `SELECT count(*) FROM maintenance_requests WHERE property_id IN (SELECT property_id FROM props) AND status IN ('OPEN','APPROVAL_PENDING','COMPLETION_REVIEW')`},
		{"bookings_awaiting_approval", `SELECT count(*) FROM shortlet_bookings WHERE property_id IN (SELECT property_id FROM props) AND status = 'PENDING_APPROVAL'`},
		{"overdue_rent", `SELECT count(*) FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id WHERE t.property_id IN (SELECT property_id FROM props) AND o.status = 'OVERDUE'`},
		{"availability_confirmations_due", `SELECT count(*) FROM freshness_checks f JOIN listings l ON l.id = f.listing_id WHERE l.property_id IN (SELECT property_id FROM props) AND f.status IN ('PENDING','REMINDED') AND f.due_at <= $2 + interval '3 days'`},
		{"co_owner_consents", `SELECT count(*) FROM ownership_consents c JOIN ownership_parties p ON p.id = c.party_id WHERE p.account_id = $1 AND c.decision = 'PENDING'`},
		{"open_disputes", `SELECT count(*) FROM domain_disputes WHERE account_id = $1 AND status = 'AWAITING_INFORMATION'`},
	}
	for _, c := range counts {
		var n int64
		if err := s.dbq(ctx).QueryRow(ctx, propsCTE+" "+c.sql, acc, now, nil).Scan(&n); err != nil {
			return v, fmt.Errorf("dashboard %s: %w", c.key, err)
		}
		v.ActionItems[c.key] = n
	}
	if v.Portfolio, err = s.portfolioSummary(ctx, acc, now); err != nil {
		return v, err
	}
	rows, err := s.dbq(ctx).Query(ctx, propsCTE+` SELECT o.currency, coalesce(sum(o.amount_minor - o.paid_minor),0)::bigint
		FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id WHERE t.property_id IN (SELECT property_id FROM props)
		 AND o.status IN ('SCHEDULED','DUE','PARTIAL') AND o.due_date BETWEEN $4 AND $5 GROUP BY o.currency`, acc, now, nil, dayOf(now), dayOf(now).AddDate(0, 0, 30))
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var cur string
		var amt int64
		if err := rows.Scan(&cur, &amt); err != nil {
			rows.Close()
			return v, err
		}
		v.ObligationsDue30d.add(cur, amt)
	}
	rows.Close()
	if v.QuickActions, err = s.quickActions(ctx, subj, v); err != nil {
		return v, err
	}
	if v.Upcoming, err = s.calendarEntries(ctx, acc, dayOf(now), dayOf(now).AddDate(0, 0, 14)); err != nil {
		return v, err
	}
	if p, err := s.q.GetDashboardPreferences(ctx, s.dbq(ctx), acc); err == nil {
		v.Preferences = DashboardPrefs{Widgets: nonNil(p.Widgets), DefaultPropertyID: p.DefaultPropertyID}
	} else if !isNoRows(err) {
		return v, err
	}
	return v, nil
}

// PutDashboardPreferences stores the widget layout.
func (s *Service) PutDashboardPreferences(ctx context.Context, subj authz.Subject, in DashboardPrefs) (DashboardPrefs, error) {
	if err := schema.Validate(in); err != nil {
		return DashboardPrefs{}, err
	}
	var out DashboardPrefs
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.authorize(ctx, subj, authz.ActProfileWrite, ids.Nil); err != nil {
			return err
		}
		if in.DefaultPropertyID != nil {
			if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), *in.DefaultPropertyID); err != nil {
				return err
			}
		}
		p, err := s.q.UpsertDashboardPreferences(ctx, mustTx(ctx), db.UpsertDashboardPreferencesParams{AccountID: accountOf(subj), Widgets: nonNil(in.Widgets),
			DefaultPropertyID: in.DefaultPropertyID, UpdatedAt: s.now()})
		out = DashboardPrefs{Widgets: nonNil(p.Widgets), DefaultPropertyID: p.DefaultPropertyID}
		return err
	})
	return out, err
}

// CalendarEntry is one dated item across the portfolio, derived from the
// authoritative domain records (there is no separate calendar store).
type CalendarEntry struct {
	Date       string  `json:"date"`
	Kind       string  `json:"kind" doc:"VIEWING, RENT_DUE, TENANCY_END, CHECK_IN, CHECK_OUT, PREVENTIVE_TASK, MAINTENANCE_DUE, AVAILABILITY_CONFIRMATION, RENEWAL_RESPONSE_DUE, NOTICE_EFFECTIVE, MOVE_OUT, INSPECTION, TURNOVER, OFFER_EXPIRY, CLOSING_MILESTONE, PROFESSIONAL_DUE, INSURANCE_RENEWAL, MANDATE_END, STATUTORY_DUE"`
	Title      string  `json:"title"`
	PropertyID *ids.ID `json:"property_id" doc:"Null for account-level items (e.g. an income tax filing)"`
	RefType    string  `json:"ref_type"`
	RefID      ids.ID  `json:"ref_id"`
}

func (s *Service) calendarEntries(ctx context.Context, account ids.ID, from, to time.Time) ([]CalendarEntry, error) {
	rows, err := s.dbq(ctx).Query(ctx, propsCTE+`
		SELECT d, kind, title, property_id, ref_type, ref_id FROM (
		  SELECT v.starts_at::date AS d, 'VIEWING' AS kind, 'Viewing' AS title, v.property_id, 'viewing_request' AS ref_type, v.id AS ref_id
		    FROM viewing_requests v WHERE v.property_id IN (SELECT property_id FROM props) AND v.status IN ('REQUESTED','CONFIRMED')
		  UNION ALL
		  SELECT o.due_date, 'RENT_DUE', t.tenant_display_name, t.property_id, 'rent_obligation', o.id
		    FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id
		   WHERE t.property_id IN (SELECT property_id FROM props) AND o.status IN ('SCHEDULED','DUE','PARTIAL','OVERDUE')
		  UNION ALL
		  SELECT t.end_date, 'TENANCY_END', t.tenant_display_name, t.property_id, 'tenancy', t.id
		    FROM tenancies t WHERE t.property_id IN (SELECT property_id FROM props) AND t.end_date IS NOT NULL AND t.status <> 'ENDED'
		  UNION ALL
		  SELECT b.check_in, 'CHECK_IN', 'Guest arrival', b.property_id, 'shortlet_booking', b.id
		    FROM shortlet_bookings b WHERE b.property_id IN (SELECT property_id FROM props) AND b.status IN ('PENDING_APPROVAL','PENDING_PAYMENT','CONFIRMED')
		  UNION ALL
		  SELECT b.check_out, 'CHECK_OUT', 'Guest departure', b.property_id, 'shortlet_booking', b.id
		    FROM shortlet_bookings b WHERE b.property_id IN (SELECT property_id FROM props) AND b.status IN ('CONFIRMED','CHECKED_IN')
		  UNION ALL
		  SELECT p.next_due_on, 'PREVENTIVE_TASK', p.title, p.property_id, 'preventive_schedule', p.id
		    FROM preventive_schedules p WHERE p.property_id IN (SELECT property_id FROM props) AND p.status = 'ACTIVE'
		  UNION ALL
		  SELECT f.due_at::date, 'AVAILABILITY_CONFIRMATION', l.title, l.property_id, 'listing', l.id
		    FROM freshness_checks f JOIN listings l ON l.id = f.listing_id WHERE l.property_id IN (SELECT property_id FROM props) AND f.status IN ('PENDING','REMINDED')
		  UNION ALL
		  SELECT m.due_on, 'MAINTENANCE_DUE', m.title, m.property_id, 'maintenance_request', m.id
		    FROM maintenance_requests m WHERE m.property_id IN (SELECT property_id FROM props) AND m.due_on IS NOT NULL AND m.status NOT IN ('COMPLETED','CANCELLED')
		  UNION ALL
		  SELECT r.respond_by::date, 'RENEWAL_RESPONSE_DUE', 'Renewal response due', r.property_id, 'renewal_case', r.id
		    FROM renewal_cases r WHERE r.property_id IN (SELECT property_id FROM props) AND r.status IN ('PROPOSED','COUNTERED')
		  UNION ALL
		  SELECT n.effective_date, 'NOTICE_EFFECTIVE', n.notice_type, n.property_id, 'notice', n.id
		    FROM notices n WHERE n.property_id IN (SELECT property_id FROM props) AND n.effective_date IS NOT NULL AND n.status IN ('ISSUED','DELIVERED','ACKNOWLEDGED')
		  UNION ALL
		  SELECT mo.move_out_date, 'MOVE_OUT', 'Move-out', mo.property_id, 'move_out_case', mo.id
		    FROM move_out_cases mo WHERE mo.property_id IN (SELECT property_id FROM props) AND mo.status <> 'COMPLETED'
		  UNION ALL
		  SELECT mo.inspection_at::date, 'INSPECTION', 'Move-out inspection', mo.property_id, 'move_out_case', mo.id
		    FROM move_out_cases mo WHERE mo.property_id IN (SELECT property_id FROM props) AND mo.status = 'INSPECTION_SCHEDULED' AND mo.inspection_at IS NOT NULL
		  UNION ALL
		  SELECT j.scheduled_for::date, CASE WHEN j.job_type = 'INSPECTION' THEN 'INSPECTION' ELSE 'TURNOVER' END, initcap(lower(j.job_type)), j.property_id, 'turnover_job', j.id
		    FROM turnover_jobs j WHERE j.property_id IN (SELECT property_id FROM props) AND j.status IN ('SCHEDULED','IN_PROGRESS')
		  UNION ALL
		  SELECT ro.expires_at::date, 'OFFER_EXPIRY', 'Rental offer expires', ro.property_id, 'rental_offer', ro.id
		    FROM rental_offers ro WHERE ro.property_id IN (SELECT property_id FROM props) AND ro.status = 'SENT'
		  UNION ALL
		  SELECT so.expires_at::date, 'OFFER_EXPIRY', 'Sale offer expires', st.property_id, 'sale_offer', so.id
		    FROM sale_offers so JOIN sale_transactions st ON st.id = so.sale_id WHERE st.property_id IN (SELECT property_id FROM props) AND so.status IN ('SUBMITTED','COUNTERED')
		  UNION ALL
		  SELECT cm.due_date, 'CLOSING_MILESTONE', cm.title, st.property_id, 'closing_milestone', cm.id
		    FROM closing_milestones cm JOIN sale_transactions st ON st.id = cm.sale_id WHERE st.property_id IN (SELECT property_id FROM props)
		     AND cm.due_date IS NOT NULL AND cm.status IN ('PENDING','AWAITING_CONFIRMATION')
		  UNION ALL
		  SELECT pa.due_by, 'PROFESSIONAL_DUE', pa.purpose, coalesce(pa.property_id, st.property_id), 'professional_appointment', pa.id
		    FROM professional_appointments pa LEFT JOIN sale_transactions st ON st.id = pa.sale_id
		   WHERE coalesce(pa.property_id, st.property_id) IN (SELECT property_id FROM props) AND pa.due_by IS NOT NULL AND pa.status IN ('APPOINTED','IN_PROGRESS')
		  UNION ALL
		  SELECT ip.ends_on, 'INSURANCE_RENEWAL', ip.insurer_name, ip.property_id, 'insurance_policy', ip.id
		    FROM insurance_policies ip WHERE ip.property_id IN (SELECT property_id FROM props) AND ip.status IN ('ACTIVE','EXPIRING')
		  UNION ALL
		  SELECT ma.ends_at::date, 'MANDATE_END', ma.manager_display_name, ma.property_id, 'manager_assignment', ma.id
		    FROM manager_assignments ma WHERE ma.property_id IN (SELECT property_id FROM props) AND ma.status IN ('ACTIVE','EXPIRING','SUSPENDED')
		  UNION ALL
		  SELECT so.due_on, 'STATUTORY_DUE', so.title, so.property_id, 'statutory_obligation', so.id
		    FROM statutory_obligations so WHERE so.account_id = $1 AND so.status = 'UPCOMING'
		) e WHERE d BETWEEN $4 AND $5 ORDER BY d, kind LIMIT 500`, account, s.now(), nil, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CalendarEntry{}
	for rows.Next() {
		var e CalendarEntry
		var d time.Time
		if err := rows.Scan(&d, &e.Kind, &e.Title, &e.PropertyID, &e.RefType, &e.RefID); err != nil {
			return nil, err
		}
		e.Date = d.Format(time.DateOnly)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PortfolioCalendar lists dated items between two dates (max 366 days).
func (s *Service) PortfolioCalendar(ctx context.Context, subj authz.Subject, from, to time.Time) ([]CalendarEntry, error) {
	if _, err := s.authorize(ctx, subj, authz.ActReportsRead, ids.Nil); err != nil {
		return nil, err
	}
	if to.Before(from) || to.Sub(from) > 366*24*time.Hour {
		return nil, apperrors.InvalidRequest("Query at most 366 days, from ≤ to.")
	}
	return s.calendarEntries(ctx, accountOf(subj), from, to)
}

// AuditActivityView is one audit record visible to the owner.
type AuditActivityView struct {
	ID          ids.ID    `json:"id"`
	OccurredAt  time.Time `json:"occurred_at"`
	Action      string    `json:"action"`
	ActorType   string    `json:"actor_type"`
	ActorDomain *string   `json:"actor_domain"`
	ByMe        bool      `json:"by_me"`
	TargetType  string    `json:"target_type"`
	TargetID    ids.ID    `json:"target_id"`
	Result      string    `json:"result"`
}

// AuditActivity pages the owner's audit trail: their own actions, actions
// on their account (including privileged operator reads) and on their
// properties.
func (s *Service) AuditActivity(ctx context.Context, subj authz.Subject, cursor string, limit int) (pagination.Response[AuditActivityView], error) {
	if _, err := s.authorize(ctx, subj, authz.ActReportsRead, ids.Nil); err != nil {
		return pagination.Response[AuditActivityView]{}, err
	}
	at, cid, err := decodeTimeCursor(cursor)
	if err != nil {
		return pagination.Response[AuditActivityView]{}, err
	}
	rows, err := s.dbq(ctx).Query(ctx, propsCTE+`
		SELECT audit_id, occurred_at, action, actor_type, actor_domain, coalesce(actor_id = $1, false), target_type, target_id, result FROM audit_events
		 WHERE (actor_id = $1 OR target_id = $1 OR target_id IN (SELECT property_id FROM props))
		   AND ($4::timestamptz IS NULL OR (occurred_at, audit_id) < ($4::timestamptz, $5::uuid))
		 ORDER BY occurred_at DESC, audit_id DESC LIMIT $6`, accountOf(subj), s.now(), nil, at, cid, limit+1)
	if err != nil {
		return pagination.Response[AuditActivityView]{}, err
	}
	defer rows.Close()
	out := []AuditActivityView{}
	for rows.Next() {
		var r AuditActivityView
		if err := rows.Scan(&r.ID, &r.OccurredAt, &r.Action, &r.ActorType, &r.ActorDomain, &r.ByMe, &r.TargetType, &r.TargetID, &r.Result); err != nil {
			return pagination.Response[AuditActivityView]{}, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return pagination.Response[AuditActivityView]{}, err
	}
	return pagination.Build(out, limit, func(v AuditActivityView) any { return timeCursor{At: v.OccurredAt, ID: v.ID} })
}
