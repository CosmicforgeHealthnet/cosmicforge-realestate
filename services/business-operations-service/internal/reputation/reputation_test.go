package reputation

import "testing"

func TestClassifyIsSourceBackedAndExplainable(t *testing.T) {
	s := Classify("The room was dirty and the shower was broken. Staff were friendly!", 4000)
	topics := map[string]Signal{}
	for _, x := range s {
		topics[x.Topic] = x
	}
	if topics["CLEANLINESS"].Sentiment != "NEGATIVE" || topics["MAINTENANCE"].Sentiment != "NEGATIVE" || topics["STAFF_BEHAVIOUR"].Sentiment != "POSITIVE" {
		t.Fatalf("signals %+v", s)
	}
	for _, x := range s {
		if len(x.EvidenceTerms) == 0 || x.ConfidenceBPS < 5000 {
			t.Fatalf("unexplained signal %+v", x)
		}
	}
	if got := Classify("", 2000); len(got) != 0 {
		t.Fatal("fabricated sentiment for empty text")
	}
	again := Classify("The room was dirty and the shower was broken. Staff were friendly!", 4000)
	if len(again) != len(s) || again[0].Topic != s[0].Topic || again[0].Sentiment != s[0].Sentiment {
		t.Fatal("classification not deterministic")
	}
}

func TestNormaliseReplyPolicyAndRouting(t *testing.T) {
	if v, ok := Normalise(4, 5); !ok || v != 8000 {
		t.Fatal(v)
	}
	if _, ok := Normalise(6, 5); ok {
		t.Fatal("out of scale accepted")
	}
	if ReplyPolicy("DIRECT_FEEDBACK", "Thank you for staying with us") != "PROVIDER_REPLIES_UNSUPPORTED" || ReplyPolicy("UNKNOWN", "x") == "" {
		t.Fatal("unsupported reply source")
	}
	if ReplyPolicy("BOOKING_COM", "Call us on +234 801 234 5678 anytime") != "RESPONSE_CONTAINS_CONTACT_OR_LINK" || ReplyPolicy("MARKETPLACE_VERIFIED_STAY", "Thank you for the feedback, we fixed the shower.") != "" {
		t.Fatal("reply policy")
	}
	if Team("CLEANLINESS") != "HOUSEKEEPING" || Team("MAINTENANCE") != "MAINTENANCE" || Team("NOISE") != "OPERATIONS" {
		t.Fatal("routing")
	}
}
