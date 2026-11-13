// Package automation contains the provider-neutral AI operations policy:
// grounded factual answering, prompt-injection isolation and action-card
// intent detection. It never executes actions. Retrieved or guest supplied text
// is treated as data only; no instruction found in it changes behaviour.
package automation

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Engine and model identity recorded on every suggestion for provenance.
const (
	Engine       = "cosmicforge-grounded-factual"
	ModelVersion = "deterministic-grounded-v1"
	// MinimumConfidenceBPS is the floor below which answers escalate to a human.
	MinimumConfidenceBPS = 6000
)

// KnowledgeEntry is an approved, versioned grounding source.
type KnowledgeEntry struct {
	ID              string    `json:"id"`
	Topic           string    `json:"topic"`
	Answer          string    `json:"answer"`
	SourceKind      string    `json:"source_kind"`
	SourceReference string    `json:"source_reference"`
	SourceVersion   int64     `json:"source_version"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// ReservationFacts are the facts of the single reservation bound to the
// conversation context. Facts of any other reservation are never supplied.
type ReservationFacts struct {
	ReservationID string `json:"reservation_id"`
	State         string `json:"state"`
	DateFrom      string `json:"date_from"`
	DateTo        string `json:"date_to"`
}

// Citation identifies the exact source version used.
type Citation struct {
	SourceID        string `json:"source_id"`
	SourceKind      string `json:"source_kind"`
	SourceReference string `json:"source_reference"`
	SourceVersion   int64  `json:"source_version"`
}

// Answer is the engine decision.
type Answer struct {
	Kind              string     `json:"kind"`
	Topic             string     `json:"topic,omitempty"`
	Text              string     `json:"text,omitempty"`
	Citations         []Citation `json:"citations"`
	ConfidenceBPS     int        `json:"confidence_bps"`
	RefusalReason     string     `json:"refusal_reason,omitempty"`
	InjectionDetected bool       `json:"injection_detected"`
	Intents           []string   `json:"intents"`
}

var topicTerms = map[string][]string{
	"CHECK_IN":            {"check-in", "check in", "checkin", "arrival time", "arrive"},
	"CHECK_OUT":           {"check-out", "check out", "checkout", "departure time"},
	"PARKING":             {"parking", "park my car", "garage"},
	"WIFI":                {"wifi", "wi-fi", "internet", "password for the network"},
	"BREAKFAST":           {"breakfast"},
	"PETS":                {"pet", "dog", "cat"},
	"SMOKING":             {"smoke", "smoking"},
	"ACCESSIBILITY":       {"wheelchair", "accessible", "accessibility", "step-free"},
	"HOUSE_RULES":         {"house rules", "quiet hours", "party", "parties"},
	"CANCELLATION_POLICY": {"cancellation policy", "free cancellation"},
	"LOCATION":            {"address", "directions", "located", "location"},
	"FACILITIES":          {"pool", "gym", "spa", "laundry", "facilities"},
	"CONTACT":             {"phone number", "contact", "reception"},
}

// Price and availability are never answered from static content.
var authoritativeOnly = []string{"price", "cost", "how much", "rate", "available", "availability", "discount", "vacancy"}

var intentTerms = map[string][]string{
	"RESERVATION_AMENDMENT":   {"change my dates", "extend my stay", "extend", "add a night", "modify my booking", "change my booking"},
	"CANCELLATION_REVIEW":     {"cancel"},
	"REFUND_REVIEW":           {"refund", "money back", "chargeback", "compensation"},
	"MAINTENANCE_REQUEST":     {"broken", "leak", "not working", "repair", "no hot water", "air conditioning"},
	"ACCESS_ISSUE":            {"locked out", "door code", "key card", "can't get in", "cannot get in"},
	"HOUSEKEEPING_ESCALATION": {"dirty", "towels", "cleaning", "housekeeping", "linen"},
	"RATE_REVIEW":             {"cheaper", "price match", "lower rate"},
}

var injection = regexp.MustCompile(`(?i)(ignore (all |any )?(previous|prior|above) (instructions|messages)|disregard (the )?(system|instructions)|you are now|system prompt|act as (an? )?(admin|administrator|manager|developer)|execute|run the following|override (your|the) (rules|policy)|<\s*/?\s*(system|instruction)s?\s*>|reveal (the )?(prompt|password|secret|token)|approve (the |my )?(refund|request)|other guests?'?s? (booking|reservation|details))`)

func normalise(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// DetectInjection flags instruction-like content. Detection only escalates;
// flagged content still never acquires authority.
func DetectInjection(text string) bool { return injection.MatchString(text) }

// Intents returns sorted suggested-action intents found in the text.
func Intents(text string) []string {
	t := normalise(text)
	out := []string{}
	for intent, terms := range intentTerms {
		for _, term := range terms {
			if strings.Contains(t, term) {
				out = append(out, intent)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func topics(text string) []string {
	t := normalise(text)
	out := []string{}
	for topic, terms := range topicTerms {
		for _, term := range terms {
			if strings.Contains(t, term) {
				out = append(out, topic)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// Ground answers only from approved, unexpired entries for exactly one
// detected topic. Reservation date questions use only the bound reservation.
func Ground(question string, now time.Time, entries []KnowledgeEntry, reservation *ReservationFacts) Answer {
	a := Answer{Citations: []Citation{}, Intents: Intents(question)}
	a.InjectionDetected = DetectInjection(question)
	q := normalise(question)
	if len(q) == 0 || len(q) > 2000 {
		return refuse(a, "QUESTION_EMPTY_OR_TOO_LONG")
	}
	if a.InjectionDetected {
		return refuse(a, "INSTRUCTION_CONTENT_ESCALATED")
	}
	for _, term := range authoritativeOnly {
		if strings.Contains(q, term) {
			return refuse(a, "PRICE_OR_AVAILABILITY_REQUIRES_AUTHORITATIVE_QUOTE")
		}
	}
	if len(a.Intents) > 0 {
		return refuse(a, "ACTION_REQUEST_REQUIRES_OPERATOR")
	}
	if reservation != nil && (strings.Contains(q, "my reservation") || strings.Contains(q, "my booking")) && (strings.Contains(q, "date") || strings.Contains(q, "when")) {
		a.Kind, a.Topic, a.ConfidenceBPS = "FACTUAL_REPLY", "RESERVATION_DATES", 9000
		a.Text = "Your reservation is " + strings.ToLower(reservation.State) + " from " + reservation.DateFrom + " to " + reservation.DateTo + "."
		a.Citations = append(a.Citations, Citation{SourceID: reservation.ReservationID, SourceKind: "RESERVATION", SourceReference: "reservation:" + reservation.ReservationID, SourceVersion: 1})
		return a
	}
	found := topics(question)
	if len(found) != 1 {
		if len(found) == 0 {
			return refuse(a, "NO_APPROVED_TOPIC_MATCH")
		}
		return refuse(a, "AMBIGUOUS_MULTIPLE_TOPICS")
	}
	for _, e := range entries {
		if e.Topic != found[0] {
			continue
		}
		if !now.Before(e.ExpiresAt) {
			return refuse(a, "GROUNDING_SOURCE_STALE")
		}
		a.Kind, a.Topic, a.Text, a.ConfidenceBPS = "FACTUAL_REPLY", e.Topic, e.Answer, 8500
		a.Citations = append(a.Citations, Citation{SourceID: e.ID, SourceKind: e.SourceKind, SourceReference: e.SourceReference, SourceVersion: e.SourceVersion})
		return a
	}
	return refuse(a, "NO_APPROVED_SOURCE_FOR_TOPIC")
}

func refuse(a Answer, reason string) Answer {
	a.Kind, a.RefusalReason, a.ConfidenceBPS, a.Text, a.Topic = "ESCALATION", reason, 0, "", ""
	a.Citations = []Citation{}
	return a
}

// ActionPolicy maps an intent to the owning use case controls. Cards carry
// these requirements; they never grant them.
type ActionPolicy struct {
	TargetType     string
	Capability     string
	ApprovalAction string
	Risk           string
	OwningRoute    string
}

var actionPolicies = map[string]ActionPolicy{
	"RESERVATION_AMENDMENT":   {"RESERVATION", "reservations:modify", "COMMERCIAL_RESERVATION_CHANGE", "HIGH", "POST /api/v1/business/reservations/{id}/amend"},
	"CANCELLATION_REVIEW":     {"RESERVATION", "reservations:cancel", "", "HIGH", "POST /api/v1/business/reservations/{id}/cancel"},
	"REFUND_REVIEW":           {"RESERVATION", "reservations:write", "HIGH_VALUE_REFUND", "HIGH", "POST /api/v1/business/reservations/{id}/payment-requests"},
	"MAINTENANCE_REQUEST":     {"PROPERTY", "maintenance:write", "", "MEDIUM", "POST /api/v1/business/work-orders"},
	"ACCESS_ISSUE":            {"PROPERTY", "pms:write", "", "HIGH", "POST /api/v1/business/stays/{id}/access"},
	"RATE_REVIEW":             {"RATE_PLAN", "rms:write", "COMMERCIAL_RMS_RATE_CHANGE", "HIGH", "POST /api/v1/business/revenue/recommendations"},
	"HOUSEKEEPING_ESCALATION": {"PROPERTY", "housekeeping:write", "", "LOW", "POST /api/v1/business/housekeeping/tasks"},
	"SUPPORT_CASE":            {"PROPERTY", "support:write", "", "LOW", "POST /api/v1/business/support-cases"},
	"STAFF_FOLLOW_UP":         {"THREAD", "inbox:write", "", "LOW", "POST /api/v1/business/inbox/assignments"},
}

// Policy returns the owning controls for an action type.
func Policy(action string) (ActionPolicy, bool) {
	p, ok := actionPolicies[action]
	return p, ok
}
