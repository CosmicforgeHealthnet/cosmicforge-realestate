package usecases

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/templates"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// TemplateInput creates a new immutable template version.
type TemplateInput struct {
	TemplateKey       string   `json:"template_key"`
	Locale            string   `json:"locale"`
	Category          string   `json:"category"`
	Channels          []string `json:"channels"`
	RequiredVariables []string `json:"required_variables"`
	PushTitle         *string  `json:"push_title"`
	PushBody          *string  `json:"push_body"`
	EmailSubject      *string  `json:"email_subject"`
	EmailText         *string  `json:"email_text"`
	EmailHTML         *string  `json:"email_html"`
	SMSText           *string  `json:"sms_text"`
	TTLSeconds        int32    `json:"ttl_seconds"`
}

// TemplateView is a stored template version.
type TemplateView struct {
	TemplateID        ids.ID    `json:"template_id"`
	TemplateKey       string    `json:"template_key"`
	Version           int32     `json:"version"`
	Locale            string    `json:"locale"`
	Category          string    `json:"category"`
	Channels          []string  `json:"channels"`
	RequiredVariables []string  `json:"required_variables"`
	PushTitle         *string   `json:"push_title"`
	PushBody          *string   `json:"push_body"`
	EmailSubject      *string   `json:"email_subject"`
	EmailText         *string   `json:"email_text"`
	EmailHTML         *string   `json:"email_html"`
	SMSText           *string   `json:"sms_text"`
	TTLSeconds        int32     `json:"ttl_seconds"`
	Status            string    `json:"status"`
	CreatedByService  string    `json:"created_by_service"`
	CreatedAt         time.Time `json:"created_at"`
}

func templateView(t db.NotificationTemplate) TemplateView {
	return TemplateView{TemplateID: t.TemplateID, TemplateKey: t.TemplateKey, Version: t.Version, Locale: t.Locale, Category: t.Category,
		Channels: t.Channels, RequiredVariables: t.RequiredVariables, PushTitle: t.PushTitle, PushBody: t.PushBody, EmailSubject: t.EmailSubject,
		EmailText: t.EmailText, EmailHTML: t.EmailHtml, SMSText: t.SmsText, TTLSeconds: t.TtlSeconds, Status: t.Status,
		CreatedByService: t.CreatedByService, CreatedAt: t.CreatedAt}
}

func tmplErr(msg string) error { return apperrors.Unprocessable(domain.CodeTemplateInvalid, msg) }

// ValidateTemplate checks a new template version: plain {{var}} syntax only,
// every placeholder declared, every declared variable used, and content
// present for each channel.
func ValidateTemplate(in *TemplateInput) error {
	if !templateRe.MatchString(in.TemplateKey) || !localeRe.MatchString(in.Locale) {
		return apperrors.InvalidRequest("template_key and locale are required.")
	}
	switch domain.Category(in.Category) {
	case domain.CategorySecurity, domain.CategoryAccount, domain.CategoryTransactional, domain.CategoryActivity:
	default:
		return apperrors.InvalidRequest("category must be SECURITY, ACCOUNT, TRANSACTIONAL or ACTIVITY.")
	}
	if len(in.Channels) == 0 {
		return apperrors.InvalidRequest("channels must not be empty.")
	}
	seen := map[string]bool{}
	for _, c := range in.Channels {
		if !domain.ValidChannel(domain.Channel(c)) || seen[c] {
			return apperrors.InvalidRequest("channels must be distinct values of PUSH, EMAIL, SMS.")
		}
		seen[c] = true
	}
	if in.TTLSeconds == 0 {
		in.TTLSeconds = 86400
	}
	if in.TTLSeconds < 60 || in.TTLSeconds > 604800 {
		return apperrors.InvalidRequest("ttl_seconds must be between 60 and 604800.")
	}
	if in.RequiredVariables == nil {
		in.RequiredVariables = []string{}
	}
	declared := map[string]bool{}
	for _, v := range in.RequiredVariables {
		if !dataKey.MatchString(v) || declared[v] {
			return apperrors.InvalidRequest("required_variables must be distinct lowercase identifiers.")
		}
		declared[v] = true
	}
	need := map[string][]*string{"PUSH": {in.PushTitle, in.PushBody}, "EMAIL": {in.EmailSubject, in.EmailText}, "SMS": {in.SMSText}}
	for ch, fields := range need {
		for _, f := range fields {
			if seen[ch] && (f == nil || *f == "") {
				return tmplErr("Content is required for channel " + ch + ".")
			}
		}
	}
	used := map[string]bool{}
	for _, f := range []*string{in.PushTitle, in.PushBody, in.EmailSubject, in.EmailText, in.EmailHTML, in.SMSText} {
		if f == nil {
			continue
		}
		if len(*f) > 20000 {
			return tmplErr("Template text is too long.")
		}
		if err := templates.ValidateTemplate(*f); err != nil {
			return tmplErr("Only {{variable}} placeholders are allowed.")
		}
		for _, p := range templates.Placeholders(*f) {
			if !declared[p] {
				return tmplErr("Placeholder {{" + p + "}} is not declared in required_variables.")
			}
			used[p] = true
		}
	}
	for v := range declared {
		if !used[v] {
			return tmplErr("Variable " + v + " is declared but never used.")
		}
	}
	slices.Sort(in.Channels)
	return nil
}

// CreateTemplateVersionTx stores the next immutable version (admin only).
// Existing versions are never edited; notifications record the version used.
func (s *Service) CreateTemplateVersionTx(ctx context.Context, tx pgx.Tx, p Principal, in TemplateInput) (TemplateView, error) {
	if p.Service != platform.AdminService {
		return TemplateView{}, apperrors.PermissionDenied("Only admin-service may manage templates.")
	}
	next, err := s.q.NextTemplateVersion(ctx, tx, db.NextTemplateVersionParams{TemplateKey: in.TemplateKey, Locale: in.Locale})
	if err != nil {
		return TemplateView{}, err
	}
	t, err := s.q.InsertTemplate(ctx, tx, db.InsertTemplateParams{TemplateID: ids.New(), TemplateKey: in.TemplateKey, Version: next,
		Locale: in.Locale, Category: in.Category, Channels: in.Channels, RequiredVariables: in.RequiredVariables, PushTitle: in.PushTitle,
		PushBody: in.PushBody, EmailSubject: in.EmailSubject, EmailText: in.EmailText, EmailHtml: in.EmailHTML, SmsText: in.SMSText,
		TtlSeconds: in.TTLSeconds, CreatedByService: string(p.Service), CreatedByActorID: actorPtr(p), CreatedAt: s.now()})
	if database.IsUniqueViolation(err, "notification_templates_version_uniq") {
		return TemplateView{}, apperrors.Conflict(apperrors.CodeConflict, "A concurrent version was created; retry.").WithOutcome(apperrors.OutcomeRetryable)
	}
	if err != nil {
		return TemplateView{}, err
	}
	_, err = s.audit.Record(ctx, tx, audit.Event{Action: "notification.template_version_created", TargetType: "notification_template",
		TargetID: t.TemplateID, Changes: map[string]any{"template_key": t.TemplateKey, "locale": t.Locale, "version": t.Version},
		Metadata: map[string]any{"category": t.Category, "channels": t.Channels}})
	return templateView(t), err
}

// ListTemplateVersions returns all versions of a template key.
func (s *Service) ListTemplateVersions(ctx context.Context, key string) ([]TemplateView, error) {
	if !templateRe.MatchString(key) {
		return nil, apperrors.NotFound(domain.CodeTemplateNotFound, "Template not found.")
	}
	rows, err := s.q.ListTemplates(ctx, s.db, key)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, apperrors.NotFound(domain.CodeTemplateNotFound, "Template not found.")
	}
	out := make([]TemplateView, 0, len(rows))
	for _, t := range rows {
		out = append(out, templateView(t))
	}
	return out, nil
}
