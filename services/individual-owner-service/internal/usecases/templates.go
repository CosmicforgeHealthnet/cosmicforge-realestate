package usecases

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Agreement and notice templates are controlled legal configuration (§13.13,
// §13.15): operators publish versioned templates per jurisdiction through
// admin-service; owners choose among ACTIVE versions and every agreement or
// notice records the exact template version used.

// TemplateClause is one controlled clause of an agreement template.
type TemplateClause struct {
	Code     string `json:"code" v:"required,pattern=[A-Z][A-Z0-9_]{1,63}"`
	Title    string `json:"title" v:"required,max=200"`
	Required bool   `json:"required"`
}

// AgreementTemplateView is an agreement template version.
type AgreementTemplateView struct {
	ID              ids.ID           `json:"id"`
	Code            string           `json:"code"`
	VersionNo       int32            `json:"version_no"`
	Country         string           `json:"country"`
	AgreementKind   string           `json:"agreement_kind"`
	Title           string           `json:"title"`
	DocumentMediaID ids.ID           `json:"document_media_id" doc:"Template document in media-document-service"`
	Clauses         []TemplateClause `json:"clauses"`
	Status          string           `json:"status"`
	PolicyVersion   string           `json:"policy_version"`
	CreatedAt       time.Time        `json:"created_at"`
	RetiredAt       *time.Time       `json:"retired_at"`
}

func agreementTemplateView(t db.AgreementTemplate) AgreementTemplateView {
	v := AgreementTemplateView{ID: t.ID, Code: t.Code, VersionNo: t.VersionNo, Country: t.Country, AgreementKind: t.AgreementKind, Title: t.Title,
		DocumentMediaID: t.DocumentMediaID, Status: t.Status, PolicyVersion: t.PolicyVersion, CreatedAt: t.CreatedAt, RetiredAt: t.RetiredAt,
		Clauses: []TemplateClause{}}
	_ = json.Unmarshal(t.Clauses, &v.Clauses)
	return v
}

// AgreementTemplateInput publishes a new template version (admin).
type AgreementTemplateInput struct {
	Code            string           `json:"code" v:"required,pattern=[A-Z][A-Z0-9_]{2,63}" doc:"Stable code; each publish creates the next version"`
	Country         string           `json:"country" v:"required,format=country"`
	AgreementKind   string           `json:"agreement_kind" v:"required,enum=RESIDENTIAL_LEASE|COMMERCIAL_LEASE|LAND_LEASE|RENEWAL_ADDENDUM"`
	Title           string           `json:"title" v:"required,min=3,max=200"`
	DocumentMediaID ids.ID           `json:"document_media_id" v:"required" doc:"Template document uploaded to media-document-service"`
	Clauses         []TemplateClause `json:"clauses,omitempty" v:"max=100"`
}

func (s *Service) adminActorOf(ctx context.Context) (string, ids.ID, error) {
	a := actorFromCtx(ctx)
	if a == nil {
		return "", ids.Nil, apperrors.PermissionDenied("An attributable admin actor is required.")
	}
	return a.Domain, a.ID, nil
}

// AdminPublishAgreementTemplate publishes the next version of a template.
func (s *Service) AdminPublishAgreementTemplate(ctx context.Context, subj authz.Subject, in AgreementTemplateInput) (AgreementTemplateView, error) {
	if err := schema.Validate(in); err != nil {
		return AgreementTemplateView{}, err
	}
	var out AgreementTemplateView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminTemplates, authz.Resource{}); err != nil {
			return err
		}
		dom, actor, err := s.adminActorOf(ctx)
		if err != nil {
			return err
		}
		clauses := in.Clauses
		if clauses == nil {
			clauses = []TemplateClause{}
		}
		t, err := s.q.InsertAgreementTemplate(ctx, mustTx(ctx), db.InsertAgreementTemplateParams{ID: ids.New(), Code: in.Code, Country: in.Country,
			AgreementKind: in.AgreementKind, Title: in.Title, DocumentMediaID: in.DocumentMediaID, Clauses: jsonb(clauses), PolicyVersion: s.Policy.Version,
			CreatedByActorDomain: dom, CreatedByActorID: actor, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = agreementTemplateView(t)
		return s.record(ctx, "legal_template.agreement.publish", "agreement_template", t.ID, map[string]any{"code": t.Code, "version_no": t.VersionNo}, nil)
	})
	return out, err
}

// AdminRetireAgreementTemplate retires a template version (existing
// agreements keep referencing it).
func (s *Service) AdminRetireAgreementTemplate(ctx context.Context, subj authz.Subject, id ids.ID) (AgreementTemplateView, error) {
	var out AgreementTemplateView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminTemplates, authz.Resource{}); err != nil {
			return err
		}
		t, err := s.q.RetireAgreementTemplate(ctx, mustTx(ctx), db.RetireAgreementTemplateParams{ID: id, RetiredAt: ptr(s.now())})
		if isNoRows(err) {
			return notFound("agreement_template")
		}
		if err != nil {
			return err
		}
		out = agreementTemplateView(t)
		return s.record(ctx, "legal_template.agreement.retire", "agreement_template", id, nil, nil)
	})
	return out, err
}

// ListAgreementTemplates lists templates (owners see ACTIVE versions only).
func (s *Service) ListAgreementTemplates(ctx context.Context, subj authz.Subject, country, kind string) ([]AgreementTemplateView, error) {
	admin := subj.Kind == authz.KindService
	action := authz.ActLegalTemplatesRead
	if admin {
		action = authz.ActInternalAdminTemplates
	}
	if _, err := s.Authz.Require(ctx, subj, action, authz.Resource{AccountID: accountOf(subj)}); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgreementTemplates(ctx, s.dbq(ctx), db.ListAgreementTemplatesParams{Country: strp(country), AgreementKind: strp(kind), IncludeRetired: admin})
	out := make([]AgreementTemplateView, 0, len(rows))
	for _, r := range rows {
		out = append(out, agreementTemplateView(r))
	}
	return out, err
}

// NoticeTemplateView is a notice template version.
type NoticeTemplateView struct {
	ID              ids.ID     `json:"id"`
	Code            string     `json:"code"`
	VersionNo       int32      `json:"version_no"`
	Country         string     `json:"country"`
	NoticeType      string     `json:"notice_type"`
	Title           string     `json:"title"`
	MinNoticeDays   int32      `json:"min_notice_days" doc:"Policy minimum between issue and legal effect"`
	DocumentMediaID *ids.ID    `json:"document_media_id"`
	Status          string     `json:"status"`
	PolicyVersion   string     `json:"policy_version"`
	CreatedAt       time.Time  `json:"created_at"`
	RetiredAt       *time.Time `json:"retired_at"`
}

func noticeTemplateView(t db.NoticeTemplate) NoticeTemplateView {
	return NoticeTemplateView{ID: t.ID, Code: t.Code, VersionNo: t.VersionNo, Country: t.Country, NoticeType: t.NoticeType, Title: t.Title,
		MinNoticeDays: t.MinNoticeDays, DocumentMediaID: t.DocumentMediaID, Status: t.Status, PolicyVersion: t.PolicyVersion, CreatedAt: t.CreatedAt,
		RetiredAt: t.RetiredAt}
}

// NoticeTemplateInput publishes a notice template version (admin).
type NoticeTemplateInput struct {
	Code            string  `json:"code" v:"required,pattern=[A-Z][A-Z0-9_]{2,63}"`
	Country         string  `json:"country" v:"required,format=country"`
	NoticeType      string  `json:"notice_type" v:"required,enum=RENT_REMINDER|ARREARS|RENT_REVIEW|RENEWAL|TERMINATION|ENTRY|BREACH|GENERAL"`
	Title           string  `json:"title" v:"required,min=3,max=200"`
	MinNoticeDays   int32   `json:"min_notice_days" v:"min=0,max=730"`
	DocumentMediaID *ids.ID `json:"document_media_id,omitempty"`
}

// AdminPublishNoticeTemplate publishes the next version of a notice template.
func (s *Service) AdminPublishNoticeTemplate(ctx context.Context, subj authz.Subject, in NoticeTemplateInput) (NoticeTemplateView, error) {
	if err := schema.Validate(in); err != nil {
		return NoticeTemplateView{}, err
	}
	var out NoticeTemplateView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminTemplates, authz.Resource{}); err != nil {
			return err
		}
		dom, actor, err := s.adminActorOf(ctx)
		if err != nil {
			return err
		}
		t, err := s.q.InsertNoticeTemplate(ctx, mustTx(ctx), db.InsertNoticeTemplateParams{ID: ids.New(), Code: in.Code, Country: in.Country,
			NoticeType: in.NoticeType, Title: in.Title, MinNoticeDays: in.MinNoticeDays, DocumentMediaID: in.DocumentMediaID, PolicyVersion: s.Policy.Version,
			CreatedByActorDomain: dom, CreatedByActorID: actor, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = noticeTemplateView(t)
		return s.record(ctx, "legal_template.notice.publish", "notice_template", t.ID, map[string]any{"code": t.Code, "version_no": t.VersionNo}, nil)
	})
	return out, err
}

// AdminRetireNoticeTemplate retires a notice template version.
func (s *Service) AdminRetireNoticeTemplate(ctx context.Context, subj authz.Subject, id ids.ID) (NoticeTemplateView, error) {
	var out NoticeTemplateView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminTemplates, authz.Resource{}); err != nil {
			return err
		}
		t, err := s.q.RetireNoticeTemplate(ctx, mustTx(ctx), db.RetireNoticeTemplateParams{ID: id, RetiredAt: ptr(s.now())})
		if isNoRows(err) {
			return notFound("notice_template")
		}
		if err != nil {
			return err
		}
		out = noticeTemplateView(t)
		return s.record(ctx, "legal_template.notice.retire", "notice_template", id, nil, nil)
	})
	return out, err
}

// ListNoticeTemplates lists notice templates.
func (s *Service) ListNoticeTemplates(ctx context.Context, subj authz.Subject, country, noticeType string) ([]NoticeTemplateView, error) {
	admin := subj.Kind == authz.KindService
	action := authz.ActLegalTemplatesRead
	if admin {
		action = authz.ActInternalAdminTemplates
	}
	if _, err := s.Authz.Require(ctx, subj, action, authz.Resource{AccountID: accountOf(subj)}); err != nil {
		return nil, err
	}
	rows, err := s.q.ListNoticeTemplates(ctx, s.dbq(ctx), db.ListNoticeTemplatesParams{Country: strp(country), NoticeType: strp(noticeType), IncludeRetired: admin})
	out := make([]NoticeTemplateView, 0, len(rows))
	for _, r := range rows {
		out = append(out, noticeTemplateView(r))
	}
	return out, err
}

// requireActiveTemplate loads an ACTIVE template valid for the property.
func (s *Service) requireAgreementTemplate(ctx context.Context, id ids.ID, country, kind string) (db.AgreementTemplate, error) {
	t, err := s.q.GetAgreementTemplate(ctx, s.dbq(ctx), id)
	if isNoRows(err) {
		return t, precondition("AGREEMENT_TEMPLATE_NOT_FOUND", "The agreement template does not exist.")
	}
	if err != nil {
		return t, err
	}
	if t.Status != "ACTIVE" {
		return t, precondition("AGREEMENT_TEMPLATE_RETIRED", "The agreement template version is retired; choose the current version.")
	}
	if t.Country != country || t.AgreementKind != kind {
		return t, precondition("AGREEMENT_TEMPLATE_MISMATCH", "The template does not apply to this property's jurisdiction or agreement kind.")
	}
	return t, nil
}

func requiredClauses(t db.AgreementTemplate) []string {
	var cl []TemplateClause
	_ = json.Unmarshal(t.Clauses, &cl)
	var out []string
	for _, c := range cl {
		if c.Required {
			out = append(out, c.Code)
		}
	}
	slices.Sort(out)
	return out
}
