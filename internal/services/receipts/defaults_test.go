package receipts

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A Service given no random source draws its identifiers' random bytes
// from crypto/rand, and one given no clock reads time.Now: neither panics
// on its first receipt (PR #147, ruling 1). Two receipts written at once
// still get different identifiers.
func TestNewWithNoRandomSourceOrClock(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	for name, s := range map[string]*Service{
		"no random source": New(w.mem, testKeys{secret: "root"}, func() time.Time { return start }, nil),
		"no clock":         New(w.mem, testKeys{secret: "root"}, nil, (&counter{}).read),
	} {
		t.Run(name, func(t *testing.T) {
			first, second := samples(t)[KindRead], samples(t)[KindRead]
			if err := s.Write(ctx, account, first); err != nil {
				t.Fatal(err)
			}
			if err := s.Write(ctx, account, second); err != nil {
				t.Fatal(err)
			}
			if first.ID == second.ID || !strings.HasPrefix(first.ID, IDPrefix) || len(first.ID) != IDLength {
				t.Fatalf("identifiers %q and %q", first.ID, second.ID)
			}
		})
	}
}
