package lifecycle

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"time"
)

type CaseHistoryFact struct {
	ID        ids.ID         `json:"fact_id"`
	Kind      string         `json:"kind"`
	Claim     *FinancialCase `json:"claim,omitempty"`
	Fact      *CaseFact      `json:"fact,omitempty"`
	Override  *CaseOverride  `json:"override,omitempty"`
	Decision  *CaseDecision  `json:"decision,omitempty"`
	JournalID *ids.ID        `json:"journal_id,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func (s *Service) CaseFacts(ctx context.Context, id ids.ID) ([]CaseHistoryFact, error) {
	if _, e := s.Case(ctx, id); e != nil {
		return nil, e
	}
	rows, e := s.DB.Query(ctx, `SELECT fact_id,kind,snapshot,journal_id,created_at FROM financial_case_facts WHERE case_id=$1 ORDER BY created_at,fact_id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []CaseHistoryFact{}
	for rows.Next() {
		var f CaseHistoryFact
		var b []byte
		if e = rows.Scan(&f.ID, &f.Kind, &b, &f.JournalID, &f.CreatedAt); e != nil {
			return nil, e
		}
		switch f.Kind {
		case "CLAIM":
			var c FinancialCase
			if e = decode(b, &c); e != nil {
				return nil, e
			}
			f.Claim = &c
		case "OVERRIDE":
			var v CaseOverride
			if e = decode(b, &v); e != nil {
				return nil, e
			}
			f.Override = &v
		case "DECISION":
			var d CaseDecision
			if e = decode(b, &d); e != nil {
				return nil, e
			}
			f.Decision = &d
		default:
			var v CaseFact
			if e = decode(b, &v); e != nil {
				return nil, e
			}
			f.Fact = &v
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *Service) Deposit(ctx context.Context, id ids.ID) (Deposit, error) {
	var d Deposit
	e := load(ctx, s.DB, `SELECT snapshot FROM refundable_deposits WHERE deposit_id=$1`, id, &d)
	if e != nil {
		return d, e
	}
	a, e := s.lineAllocation(ctx, d.LineID)
	if e != nil {
		return Deposit{}, e
	}
	if e = s.caseAccess(ctx, a, d.LineID); e != nil {
		return Deposit{}, e
	}
	var state string
	e = s.DB.QueryRow(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM financial_cases c WHERE c.line_id=$1 AND c.state='LEGAL_ESCALATION') THEN 'LEGAL_ESCALATED' WHEN EXISTS(SELECT 1 FROM financial_cases c WHERE c.line_id=$1 AND c.state='RESERVE_EXTENDED') THEN 'RESERVE_EXTENDED' WHEN EXISTS(SELECT 1 FROM financial_cases c WHERE c.line_id=$1 AND c.state<>'CLOSED') THEN 'DISPUTED' WHEN EXISTS(SELECT 1 FROM refund_attempts r WHERE r.line_id=$1 AND r.state IN ('SUBMITTED','PROCESSING','UNKNOWN_OUTCOME','REVIEW_REQUIRED')) THEN 'REFUND_SUBMITTED' WHEN EXISTS(SELECT 1 FROM refund_attempts r WHERE r.line_id=$1 AND r.state='RESERVED') THEN 'REFUND_RESERVED' WHEN COALESCE((SELECT sum(amount_minor) FROM refund_attempts r WHERE r.line_id=$1 AND r.state='CONFIRMED_REFUNDED'),0)=$2 THEN 'REFUNDED' WHEN EXISTS(SELECT 1 FROM financial_case_facts f JOIN financial_cases c USING(case_id) WHERE c.line_id=$1 AND f.kind='DECISION' AND (f.snapshot->>'award_minor')::bigint>0) THEN CASE WHEN COALESCE((SELECT sum((f.snapshot->>'award_minor')::bigint) FROM financial_case_facts f JOIN financial_cases c USING(case_id) WHERE c.line_id=$1 AND f.kind='DECISION'),0)=$2 THEN 'AWARDED' ELSE 'PARTIAL_AWARD' END WHEN EXISTS(SELECT 1 FROM financial_cases c WHERE c.line_id=$1 AND c.state='CLOSED') THEN 'REFUNDABLE' WHEN NOT EXISTS(SELECT 1 FROM transaction_funding WHERE intent_id=$3) THEN 'VERIFIED_AWAITING_SETTLEMENT' ELSE 'PROTECTED' END`, d.LineID, d.AmountMinor, d.IntentID).Scan(&state)
	if e != nil {
		return Deposit{}, e
	}
	d.State = state
	return d, nil
}
