package oauth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// C1 (the second design review): an assertion's replay is decided by a
// strict record named by its client, jti and exp, so the same assertion
// meets the same record through either enclave and after a restart, and a
// deleted mark changes nothing; the record is bounded.
func TestReview2C1AnAssertionIsDecidedByItsRecord(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 59, 30, 0, time.UTC)
	exp := at.Add(5 * time.Minute)
	name, use := AssertionRecordName("https://chatgpt.com/oauth/client.json", "j1", exp)
	again, use2 := AssertionRecordName("https://chatgpt.com/oauth/client.json", "j1", exp)
	if name != again || use != use2 || !strings.HasPrefix(name, RecordAssertions) || !strings.Contains(name, "-2026111611-") {
		t.Fatalf("%s %s", name, again)
	}
	if other, _ := AssertionRecordName("https://chatgpt.com/oauth/chatgpt/client.json", "j1", exp); other == name {
		t.Fatal("two clients share a record")
	}
	if strings.Contains(name, "j1") || strings.Contains(use, "j1") || strings.Contains(name, "chatgpt") {
		t.Fatal("the name holds the client or the jti")
	}

	r := &AssertionRecord{V: 1}
	if got := r.Accept(use, exp, at, 0); got != AssertionAccepted || len(r.Uses) != 1 || !r.Uses[0].Until.Equal(AssertionAcceptedUntil(exp)) {
		t.Fatalf("%v %+v", got, r.Uses)
	}
	// The other enclave, with no memory of it, reads the same record.
	b, _ := json.Marshal(r)
	var read AssertionRecord
	_ = json.Unmarshal(b, &read)
	for _, now := range []time.Time{at, exp.Add(-time.Second), exp, exp.Add(time.Second)} {
		if got := read.Accept(use, exp, now, 1); got != AssertionReplayed {
			t.Fatalf("replayed at %v: %v", now, got)
		}
	}
	if got := read.Accept(use, exp, AssertionAcceptedUntil(exp), 1); got != AssertionExpired {
		t.Fatalf("past its acceptance: %v", got)
	}

	full := &AssertionRecord{V: 1}
	for i := range AssertionsPerRecord {
		full.Uses = append(full.Uses, AssertionUse{Hash: strings.Repeat("0", 63) + string(rune('a'+i%26))})
	}
	if got := full.Accept(use, exp, at, 1); got != AssertionFull || len(full.Uses) != AssertionsPerRecord {
		t.Fatalf("full: %v", got)
	}
	if got := (&AssertionRecord{}).Accept(use, exp, at, AssertionVersionsMax); got != AssertionFull {
		t.Fatalf("past its versions: %v", got)
	}
	b, _ = json.Marshal(full)
	if len(b) > 300<<10 {
		t.Fatalf("a full record is %d bytes", len(b))
	}
}
