package webhookstest

import (
	"context"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

var start = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

// The fake keeps the contract services/connections must keep (ADR 0039, K10
// to K14), so pull request 3 builds against what pull request 2 delivers.
func TestTheFakeConnectionsKeepTheContract(t *testing.T) {
	Contract(t, func(*testing.T) World { return NewFakeWorld(start) })
}

// A read older than oauth.ConnectionFresh no longer lets a call start.
func TestACallStartsOnlyOnAFreshRead(t *testing.T) {
	w := NewFakeWorld(start)
	c, _ := w.Make(t, contracts.WebhookRequest{URL: "https://trip-app.com/h", NewMail: true})
	s, err := w.Connections().Subscription(context.Background(), c)
	if err != nil || !Fresh(s, w.Now()) {
		t.Fatal(err)
	}
	w.Advance(5*time.Second + time.Nanosecond)
	if Fresh(s, w.Now()) {
		t.Fatal("a read older than five seconds is not fresh")
	}
}
