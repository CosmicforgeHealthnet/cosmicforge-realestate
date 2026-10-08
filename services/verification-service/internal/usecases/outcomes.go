package usecases

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

// Attempt states.
const (
	attemptQueued     = "QUEUED"
	attemptSubmitting = "SUBMITTING"
	attemptSubmitted  = "SUBMITTED"
	attemptUnknown    = "UNKNOWN"
	attemptCompleted  = "COMPLETED"
	attemptFailed     = "FAILED"
)

// Result channels.
const (
	viaCallback = "CALLBACK"
	viaReadBack = "READ_BACK"
	viaSync     = "SYNC"
)

// outcomeInput is a provider outcome to apply.
type outcomeInput struct {
	outcome     providers.Outcome
	reasonCode  string
	eventID     string
	providerRef string
	via         string
	payloadHash string
}

// applyOutcome records a provider result and, when the case is still
// PROCESSING, performs the corresponding transition. Duplicate results
// (same provider event) are no-ops. Must be called with the case locked.
func (s *Service) applyOutcome(ctx context.Context, tx pgx.Tx, c db.VerificationCase, a db.VerificationAttempt, in outcomeInput) (applied bool, duplicate bool, err error) {
	if in.eventID == "" {
		in.eventID = a.ClientReference + ":" + string(in.outcome)
	}
	// Final outcomes and every callback are recorded once per provider event
	// (duplicates are no-ops). Non-final poll results are not results.
	var res db.ProviderResult
	if in.outcome.Final() || in.via == viaCallback {
		var err error
		res, err = s.q.InsertProviderResult(ctx, tx, db.InsertProviderResultParams{
			ProviderResultID: ids.New(), VerificationID: c.VerificationID, AttemptID: a.AttemptID, Provider: a.Provider,
			ProviderEventID: in.eventID, Outcome: string(in.outcome), ReasonCode: strPtr(in.reasonCode), ReceivedVia: in.via,
			PayloadSha256: strPtr(in.payloadHash), ReceivedAt: s.now(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, true, nil
		}
		if err != nil {
			return false, false, err
		}
	}
	now := s.now()
	providerRef := in.providerRef
	if providerRef == "" && a.ProviderReference != nil {
		providerRef = *a.ProviderReference
	}
	if !in.outcome.Final() {
		// PENDING / RESULT_AVAILABLE: keep processing; fetch soon.
		next := now.Add(s.opts.PollInterval)
		if in.outcome == providers.OutcomeResultAvailable {
			next = now
		}
		if domain.Status(c.Status) == domain.StatusProcessing && a.State != attemptCompleted && a.State != attemptFailed {
			if _, err := s.q.UpdateAttempt(ctx, tx, db.UpdateAttemptParams{State: attemptSubmitted, ProviderReference: strPtr(providerRef),
				NextAttemptAt: next, UpdatedAt: now, AttemptID: a.AttemptID}); err != nil {
				return false, false, err
			}
			if err := s.setSubstatus(ctx, tx, c, domain.SubstatusProviderSubmitted); err != nil {
				return false, false, err
			}
		}
		return false, false, nil
	}
	if domain.Status(c.Status) != domain.StatusProcessing {
		// Late/duplicate result for a decided case: recorded, not applied.
		s.log.InfoContext(ctx, "provider result recorded for non-processing case", "verification_id", c.VerificationID.String(),
			"status", c.Status, "outcome", string(in.outcome))
		return false, false, s.completeAttempt(ctx, tx, a, providerRef, now)
	}
	actorType := ""
	if in.via == viaCallback {
		actorType = audit.ActorProvider
	} else if requestcontext.From(ctx).CallingService == "" {
		actorType = audit.ActorSystem
	}
	switch in.outcome {
	case providers.OutcomeVerified, providers.OutcomeRejected:
		to, ev := domain.StatusVerified, EventVerified
		var expires *time.Time
		if in.outcome == providers.OutcomeRejected {
			to, ev = domain.StatusRejected, EventRejected
		} else {
			expires = timePtr(now.Add(s.opts.Validity[domain.Type(c.VerificationType)]))
		}
		updated, err := s.apply(ctx, tx, c, transition{to: to, decidedAt: &now, expiresAt: expires, reasonCode: in.reasonCode,
			decisionSource: domain.SourceProvider, provider: a.Provider, providerRef: providerRef, event: ev, actorType: actorType})
		if err != nil {
			return false, false, err
		}
		if _, err := s.q.InsertDecision(ctx, tx, db.InsertDecisionParams{DecisionID: ids.New(), VerificationID: c.VerificationID,
			Decision: string(to), DecisionSource: string(domain.SourceProvider), ReasonCode: strPtr(in.reasonCode),
			Provider: &a.Provider, ProviderReference: strPtr(providerRef), DecidedByService: string(platform.VerificationService),
			ExpiresAt: updated.ExpiresAt, CreatedAt: now}); err != nil {
			return false, false, err
		}
	case providers.OutcomeInconclusive:
		if err := s.toManualReview(ctx, tx, c, domain.ReviewProviderInconclusive, in.reasonCode, a.Provider, providerRef, actorType); err != nil {
			return false, false, err
		}
	}
	if err := s.q.MarkProviderResultApplied(ctx, tx, res.ProviderResultID); err != nil {
		return false, false, err
	}
	return true, false, s.completeAttempt(ctx, tx, a, providerRef, now)
}

func (s *Service) completeAttempt(ctx context.Context, tx pgx.Tx, a db.VerificationAttempt, providerRef string, now time.Time) error {
	if a.State == attemptCompleted || a.State == attemptFailed {
		return nil
	}
	_, err := s.q.UpdateAttempt(ctx, tx, db.UpdateAttemptParams{State: attemptCompleted, ProviderReference: strPtr(providerRef),
		NextAttemptAt: now, CompletedAt: &now, UpdatedAt: now, AttemptID: a.AttemptID})
	return err
}

func (s *Service) toManualReview(ctx context.Context, tx pgx.Tx, c db.VerificationCase, reason domain.ReviewReason, reasonCode, provider, providerRef, actorType string) error {
	if _, err := s.apply(ctx, tx, c, transition{to: domain.StatusManualReview, reasonCode: reasonCode, reviewReason: reason,
		decisionSource: domain.SourceProvider, provider: provider, providerRef: providerRef, event: EventManualReviewRequired, actorType: actorType}); err != nil {
		return err
	}
	_, err := s.q.CreateManualReview(ctx, tx, db.CreateManualReviewParams{ReviewID: ids.New(), VerificationID: c.VerificationID,
		Reason: string(reason), CreatedAt: s.now()})
	return err
}

func (s *Service) setSubstatus(ctx context.Context, tx pgx.Tx, c db.VerificationCase, sub domain.Substatus) error {
	if c.ProcessingSubstatus != nil && *c.ProcessingSubstatus == string(sub) {
		return nil
	}
	v := string(sub)
	_, err := s.q.UpdateCaseState(ctx, tx, db.UpdateCaseStateParams{Status: c.Status, ProcessingSubstatus: &v, UpdatedAt: s.now(),
		VerificationID: c.VerificationID, ExpectedStatus: c.Status})
	return err
}

func (s *Service) logProviderCall(ctx context.Context, a db.VerificationAttempt, op, outcome, code string, d time.Duration) {
	err := s.q.InsertProviderRequest(ctx, s.db, db.InsertProviderRequestParams{
		ProviderRequestID: ids.New(), AttemptID: a.AttemptID, VerificationID: a.VerificationID, Provider: a.Provider,
		Operation: op, Outcome: outcome, ErrorCode: strPtr(code), DurationMs: int32(d.Milliseconds()), CreatedAt: s.now(),
	})
	if err != nil {
		s.log.WarnContext(ctx, "provider request log failed", "error", err.Error())
	}
}

func (s *Service) backoff(tries int32) time.Duration {
	d := 5 * time.Second
	for i := int32(1); i < tries && d < s.opts.MaxBackoff; i++ {
		d *= 2
	}
	if d > s.opts.MaxBackoff {
		d = s.opts.MaxBackoff
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// DispatchDue claims due attempts and drives them with the provider.
func (s *Service) DispatchDue(ctx context.Context, owner string, batch int) (int, error) {
	now := s.now()
	leaseUntil := now.Add(s.opts.AttemptLease)
	attempts, err := s.q.ClaimDueAttempts(ctx, s.db, db.ClaimDueAttemptsParams{LeaseOwner: &owner, LeaseExpiresAt: &leaseUntil, Now: now, RowLimit: int32(batch)})
	if err != nil {
		return 0, err
	}
	for _, a := range attempts {
		actx := requestcontext.Job(ctx, "verification-dispatch")
		if err := s.drive(actx, owner, a); err != nil {
			s.log.ErrorContext(actx, "attempt processing failed", "attempt_id", a.AttemptID.String(), "error", err.Error())
		}
	}
	return len(attempts), nil
}

func (s *Service) drive(ctx context.Context, owner string, a db.VerificationAttempt) error {
	p, ok := s.providers.Get(a.Provider)
	if !ok {
		return s.escalate(ctx, a, "PROVIDER_DISABLED")
	}
	switch a.State {
	case attemptSubmitted:
		return s.poll(ctx, p, a)
	case attemptQueued, attemptSubmitting, attemptUnknown:
		// Any prior try may have reached the provider (crash mid-call or an
		// ambiguous response): read back before re-submitting so a side
		// effect is never repeated blindly.
		if a.State != attemptQueued || a.Tries > 1 {
			st, err := s.readBack(ctx, p, a)
			if err != nil {
				return s.ambiguous(ctx, a, "READ_BACK_FAILED")
			}
			if st.Found {
				return s.handleStatus(ctx, a, st, viaReadBack)
			}
		}
		return s.submit(ctx, owner, p, a)
	}
	return nil
}

func (s *Service) readBack(ctx context.Context, p providers.Provider, a db.VerificationAttempt) (providers.StatusResult, error) {
	cctx, cancel := context.WithTimeout(ctx, s.opts.ProviderTimeout)
	defer cancel()
	start := time.Now()
	st, err := p.ReadStatus(cctx, a.ClientReference)
	switch {
	case err != nil:
		s.logProviderCall(ctx, a, "READ_STATUS", "ERROR_RETRYABLE", errCode(err), time.Since(start))
	case !st.Found:
		s.logProviderCall(ctx, a, "READ_STATUS", "NOT_FOUND", "", time.Since(start))
	default:
		s.logProviderCall(ctx, a, "READ_STATUS", outcomeLabel(st.Outcome), "", time.Since(start))
	}
	return st, err
}

func (s *Service) submit(ctx context.Context, owner string, p providers.Provider, a db.VerificationAttempt) error {
	n, err := s.q.MarkAttemptSubmitting(ctx, s.db, db.MarkAttemptSubmittingParams{Now: s.now(), AttemptID: a.AttemptID, LeaseOwner: &owner})
	if err != nil || n == 0 {
		return err // lease lost or state moved on
	}
	c, err := s.q.GetCase(ctx, s.db, a.VerificationID)
	if err != nil {
		return err
	}
	evs, err := s.q.ListEvidence(ctx, s.db, a.VerificationID)
	if err != nil {
		return err
	}
	req := providers.SubmitRequest{
		ClientReference: a.ClientReference, VerificationID: c.VerificationID, Type: domain.Type(c.VerificationType),
		Subject:  domain.Subject{Type: domain.SubjectType(c.SubjectType), Domain: platform.Domain(c.SubjectDomain), ID: c.SubjectID},
		Metadata: metadataMap(c.ProviderMetadata), EvidenceSource: s.media,
	}
	for _, e := range evs {
		cs := ""
		if e.MediaChecksumSha256 != nil {
			cs = *e.MediaChecksumSha256
		}
		req.Evidence = append(req.Evidence, providers.Evidence{MediaID: e.MediaID, EvidenceType: e.EvidenceType, Checksum: cs})
	}
	cctx, cancel := context.WithTimeout(ctx, s.opts.ProviderTimeout)
	start := time.Now()
	res, err := p.Submit(cctx, req)
	cancel()
	if err != nil {
		switch providers.Classify(err) {
		case providers.KindRetryable:
			s.logProviderCall(ctx, a, "SUBMIT", "ERROR_RETRYABLE", errCode(err), time.Since(start))
			return s.retryLater(ctx, a, attemptQueued, errCode(err))
		case providers.KindPermanent:
			s.logProviderCall(ctx, a, "SUBMIT", "ERROR_PERMANENT", errCode(err), time.Since(start))
			return s.escalate(ctx, a, errCode(err))
		default:
			s.logProviderCall(ctx, a, "SUBMIT", "TIMEOUT", errCode(err), time.Since(start))
			return s.ambiguous(ctx, a, errCode(err))
		}
	}
	s.logProviderCall(ctx, a, "SUBMIT", acceptedLabel(res.Outcome), "", time.Since(start))
	return s.handleStatus(ctx, a, providers.StatusResult{Found: true, ProviderReference: res.ProviderReference,
		Outcome: res.Outcome, ReasonCode: res.ReasonCode, EventID: res.EventID}, viaSync)
}

func (s *Service) poll(ctx context.Context, p providers.Provider, a db.VerificationAttempt) error {
	if int(a.Tries) > s.opts.MaxPollTries {
		return s.escalate(ctx, a, "PROVIDER_RESULT_TIMEOUT")
	}
	cctx, cancel := context.WithTimeout(ctx, s.opts.ProviderTimeout)
	defer cancel()
	start := time.Now()
	var st providers.StatusResult
	var err error
	op := "READ_STATUS"
	if a.ProviderReference != nil {
		op = "FETCH_RESULT"
		st, err = p.FetchResult(cctx, *a.ProviderReference)
	} else {
		st, err = p.ReadStatus(cctx, a.ClientReference)
	}
	if err != nil {
		s.logProviderCall(ctx, a, op, "ERROR_RETRYABLE", errCode(err), time.Since(start))
		return s.retryLater(ctx, a, attemptSubmitted, errCode(err))
	}
	if !st.Found {
		s.logProviderCall(ctx, a, op, "NOT_FOUND", "", time.Since(start))
		// The provider lost the submission: safe to submit again.
		return s.retryLater(ctx, a, attemptQueued, "PROVIDER_NOT_FOUND")
	}
	s.logProviderCall(ctx, a, op, outcomeLabel(st.Outcome), "", time.Since(start))
	return s.handleStatus(ctx, a, st, viaReadBack)
}

// handleStatus applies a provider status inside a transaction.
func (s *Service) handleStatus(ctx context.Context, a db.VerificationAttempt, st providers.StatusResult, via string) error {
	return database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		c, err := s.lockCase(ctx, tx, a.VerificationID)
		if err != nil {
			return err
		}
		cur, err := s.q.GetAttemptForUpdate(ctx, tx, a.AttemptID)
		if err != nil {
			return err
		}
		_, _, err = s.applyOutcome(ctx, tx, c, cur, outcomeInput{outcome: st.Outcome, reasonCode: st.ReasonCode,
			eventID: st.EventID, providerRef: st.ProviderReference, via: via})
		return err
	})
}

// ambiguous records an unknown provider outcome: the case stays PROCESSING
// with substatus UNKNOWN_PROVIDER_RESULT and the next try starts with a
// read-back. It is never collapsed into a failure.
func (s *Service) ambiguous(ctx context.Context, a db.VerificationAttempt, code string) error {
	if int(a.Tries) >= s.opts.MaxSubmitTries {
		return s.escalate(ctx, a, code)
	}
	return database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		c, err := s.lockCase(ctx, tx, a.VerificationID)
		if err != nil {
			return err
		}
		if domain.Status(c.Status) == domain.StatusProcessing {
			if err := s.setSubstatus(ctx, tx, c, domain.SubstatusUnknownProviderResult); err != nil {
				return err
			}
		}
		now := s.now()
		_, err = s.q.UpdateAttempt(ctx, tx, db.UpdateAttemptParams{State: attemptUnknown, NextAttemptAt: now.Add(s.backoff(a.Tries)),
			LastErrorCode: &code, UpdatedAt: now, AttemptID: a.AttemptID})
		return err
	})
}

func (s *Service) retryLater(ctx context.Context, a db.VerificationAttempt, state, code string) error {
	limit := s.opts.MaxSubmitTries
	if state == attemptSubmitted {
		limit = s.opts.MaxPollTries
	}
	if int(a.Tries) >= limit {
		return s.escalate(ctx, a, code)
	}
	now := s.now()
	_, err := s.q.UpdateAttempt(ctx, s.db, db.UpdateAttemptParams{State: state, NextAttemptAt: now.Add(s.backoff(a.Tries)),
		LastErrorCode: &code, UpdatedAt: now, AttemptID: a.AttemptID})
	return err
}

// escalate hands the case to a human (PROCESSING -> MANUAL_REVIEW) when the
// provider cannot produce a decision. Provider failure is never turned into
// a REJECTED decision automatically.
func (s *Service) escalate(ctx context.Context, a db.VerificationAttempt, code string) error {
	return database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		c, err := s.lockCase(ctx, tx, a.VerificationID)
		if err != nil {
			return err
		}
		now := s.now()
		if _, err := s.q.UpdateAttempt(ctx, tx, db.UpdateAttemptParams{State: attemptFailed, NextAttemptAt: now,
			LastErrorCode: &code, CompletedAt: &now, UpdatedAt: now, AttemptID: a.AttemptID}); err != nil {
			return err
		}
		if domain.Status(c.Status) != domain.StatusProcessing {
			return nil
		}
		return s.toManualReview(ctx, tx, c, domain.ReviewProviderFailure, code, a.Provider, "", audit.ActorSystem)
	})
}

// CallbackResult reports how a callback was handled.
type CallbackResult struct {
	Status string `json:"status"` // APPLIED | RECORDED | DUPLICATE | IGNORED
}

// HandleCallback authenticates a provider callback and applies it.
// Duplicate callbacks are harmless no-ops.
func (s *Service) HandleCallback(ctx context.Context, providerName string, h http.Header, body []byte) (CallbackResult, error) {
	p, ok := s.providers.Get(providerName)
	if !ok {
		return CallbackResult{}, apperrors.NotFound(apperrors.CodeRouteNotFound, "Unknown provider.")
	}
	ev, err := p.VerifyCallback(ctx, h, body)
	if err != nil {
		s.log.WarnContext(ctx, "provider callback rejected", "provider", providerName)
		return CallbackResult{}, apperrors.New(http.StatusUnauthorized, domain.CodeInvalidProviderCallback, "Callback could not be authenticated.")
	}
	var a db.VerificationAttempt
	switch {
	case ev.ClientReference != "":
		a, err = s.q.GetAttemptByClientReference(ctx, s.db, ev.ClientReference)
	default:
		a, err = s.q.GetAttemptByProviderReference(ctx, s.db, db.GetAttemptByProviderReferenceParams{Provider: providerName, ProviderReference: &ev.ProviderReference})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		s.log.WarnContext(ctx, "provider callback for unknown reference ignored", "provider", providerName, "event_id", ev.EventID)
		return CallbackResult{Status: "IGNORED"}, nil
	}
	if err != nil {
		return CallbackResult{}, err
	}
	if a.Provider != providerName {
		return CallbackResult{Status: "IGNORED"}, nil
	}
	out := CallbackResult{Status: "RECORDED"}
	err = database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		c, err := s.lockCase(ctx, tx, a.VerificationID)
		if err != nil {
			return err
		}
		cur, err := s.q.GetAttemptForUpdate(ctx, tx, a.AttemptID)
		if err != nil {
			return err
		}
		applied, dup, err := s.applyOutcome(ctx, tx, c, cur, outcomeInput{outcome: ev.Outcome, reasonCode: ev.ReasonCode,
			eventID: ev.EventID, providerRef: ev.ProviderReference, via: viaCallback, payloadHash: security.SHA256Hex(body)})
		switch {
		case dup:
			out.Status = "DUPLICATE"
		case applied:
			out.Status = "APPLIED"
		}
		return err
	})
	return out, err
}

func errCode(err error) string {
	var pe *providers.Error
	if errors.As(err, &pe) && pe.Code != "" {
		return pe.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	return "PROVIDER_ERROR"
}

func outcomeLabel(o providers.Outcome) string {
	if o.Final() {
		return "RESULT"
	}
	return "PENDING"
}

func acceptedLabel(o providers.Outcome) string {
	if o.Final() {
		return "RESULT"
	}
	return "ACCEPTED"
}
