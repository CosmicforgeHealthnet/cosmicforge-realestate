// Package reputation holds explainable review intelligence: rating
// normalisation, an approved topic taxonomy with source-term evidence,
// provider reply policy and quality routing. Topic signals are quality
// inputs, never confirmed incidents and never a rewrite of the review.
package reputation

import (
	"regexp"
	"sort"
	"strings"
)

const (
	Engine       = "cosmicforge-review-topics"
	ModelVersion = "taxonomy-keyword-v1"
)

// Sources supported for intake. Anything else fails closed.
var Sources = map[string]struct {
	Replies  bool
	Retract  bool
	MaxReply int
}{
	"MARKETPLACE_VERIFIED_STAY": {Replies: true, Retract: true, MaxReply: 2000},
	"BOOKING_COM":               {Replies: true, Retract: false, MaxReply: 1000},
	"AIRBNB":                    {Replies: true, Retract: false, MaxReply: 1000},
	"DIRECT_FEEDBACK":           {Replies: false},
	"LEGACY_IMPORT":             {Replies: false},
}

// Normalise converts a provider rating to basis points of its scale.
func Normalise(value, scale int) (int, bool) {
	if scale < 1 || scale > 100 || value < 0 || value > scale {
		return 0, false
	}
	return value * 10000 / scale, true
}

// Taxonomy maps approved topics to evidence terms.
var Taxonomy = map[string][]string{
	"CLEANLINESS":         {"dirty", "clean", "dust", "stain", "smell", "hair in", "unclean", "filthy", "towel"},
	"MAINTENANCE":         {"broken", "leak", "not working", "repair", "faulty", "hot water", "air conditioning", "ac ", "mould", "mold"},
	"CHECK_IN_DELAY":      {"check-in", "check in", "waited", "wait for", "late check", "queue"},
	"SERVICE_QUALITY":     {"service", "helpful", "responsive", "unresponsive"},
	"STAFF_BEHAVIOUR":     {"rude", "staff", "receptionist", "friendly", "unprofessional"},
	"PRICING":             {"overpriced", "expensive", "value for money", "price", "hidden fee"},
	"ROOM_CONDITION":      {"old furniture", "worn", "tiny room", "bed was", "uncomfortable bed"},
	"ACCESS":              {"key card", "door code", "locked out", "could not get in", "couldn't get in", "access"},
	"NOISE":               {"noise", "noisy", "loud", "thin walls"},
	"SAFETY":              {"unsafe", "safety", "security", "theft", "stolen"},
	"CANCELLATION_REFUND": {"refund", "cancellation", "cancelled", "charged twice", "chargeback"},
}

var negativeCues = []string{"not ", "no ", "never", "dirty", "broken", "rude", "bad", "terrible", "awful", "poor", "worst", "late", "waited", "noisy", "unsafe", "overpriced", "uncomfortable", "smell", "stain", "leak", "filthy", "unclean", "locked out", "couldn't", "could not", "charged twice", "unresponsive", "unprofessional"}
var positiveCues = []string{"great", "excellent", "clean", "friendly", "helpful", "spotless", "quiet", "lovely", "perfect", "comfortable", "responsive"}

// Signal is one explainable topic observation.
type Signal struct {
	Topic         string   `json:"topic"`
	Sentiment     string   `json:"sentiment"`
	ConfidenceBPS int      `json:"confidence_bps"`
	EvidenceTerms []string `json:"evidence_terms"`
}

var sentenceSplit = regexp.MustCompile(`[.!?\n;]+`)

// Classify returns deterministic, source-term backed signals. Empty or
// unsupported text yields no signals (never a fabricated sentiment).
func Classify(text string, ratingBPS int) []Signal {
	text = strings.ToLower(text)
	if strings.TrimSpace(text) == "" {
		return []Signal{}
	}
	found := map[string]*Signal{}
	for _, sentence := range sentenceSplit.Split(text, -1) {
		s := " " + strings.Join(strings.Fields(sentence), " ") + " "
		for topic, terms := range Taxonomy {
			for _, term := range terms {
				if !strings.Contains(s, term) {
					continue
				}
				neg, pos := 0, 0
				for _, c := range negativeCues {
					if strings.Contains(s, c) {
						neg++
					}
				}
				for _, c := range positiveCues {
					if strings.Contains(s, c) && !strings.Contains(s, "not "+c) {
						pos++
					}
				}
				sentiment := "NEUTRAL"
				if neg > pos {
					sentiment = "NEGATIVE"
				} else if pos > neg {
					sentiment = "POSITIVE"
				} else if ratingBPS > 0 && ratingBPS <= 4000 {
					sentiment = "NEGATIVE"
				}
				sig, ok := found[topic]
				if !ok {
					sig = &Signal{Topic: topic, Sentiment: sentiment, EvidenceTerms: []string{}}
					found[topic] = sig
				}
				if sentiment == "NEGATIVE" {
					sig.Sentiment = "NEGATIVE"
				}
				if len(sig.EvidenceTerms) < 5 && !contains(sig.EvidenceTerms, term) {
					sig.EvidenceTerms = append(sig.EvidenceTerms, term)
				}
				break
			}
		}
	}
	out := []Signal{}
	for _, s := range found {
		s.ConfidenceBPS = min(5000+1500*len(s.EvidenceTerms), 9500)
		sort.Strings(s.EvidenceTerms)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Team routes a repeated negative topic to the accountable operation.
func Team(topic string) string {
	switch topic {
	case "CLEANLINESS":
		return "HOUSEKEEPING"
	case "MAINTENANCE", "ROOM_CONDITION":
		return "MAINTENANCE"
	case "CHECK_IN_DELAY", "STAFF_BEHAVIOUR", "SERVICE_QUALITY", "ACCESS":
		return "FRONT_DESK"
	case "PRICING", "CANCELLATION_REFUND":
		return "GUEST_RECOVERY"
	}
	return "OPERATIONS"
}

var contactPattern = regexp.MustCompile(`(?i)([a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}|\+?\d[\d\s().-]{7,}\d|https?://)`)

// ReplyPolicy validates a business response against provider restrictions.
func ReplyPolicy(source, body string) string {
	cfg, ok := Sources[source]
	switch {
	case !ok || !cfg.Replies:
		return "PROVIDER_REPLIES_UNSUPPORTED"
	case len(strings.TrimSpace(body)) < 10:
		return "RESPONSE_TOO_SHORT"
	case len(body) > cfg.MaxReply:
		return "RESPONSE_EXCEEDS_PROVIDER_LIMIT"
	case contactPattern.MatchString(body):
		return "RESPONSE_CONTAINS_CONTACT_OR_LINK"
	}
	return ""
}
