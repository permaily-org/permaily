package inbox

import (
	"bytes"
	"context"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/mailhtml"
)

// The code review of pull request 5 (#92), finding 4 (a): Get read the HTML
// part of a message, found it and transcoded it before it asked for a
// render slot. Every step of the HTML (the MIME walk for its part, the
// charset, the render) runs after the render is admitted, under the
// admitted context; the text needs no slot.
func TestReview4AMessageHTMLIsWorkedOnlyAfterItsAdmission(t *testing.T) {
	ctx := context.Background()
	b := Box{MailboxID: "mbx", Key: bytes.Repeat([]byte{7}, 32)}
	htmlRaw := []byte("Content-Type: text/html; charset=windows-1252\r\n\r\n<p>Caf\xe9 <a href=\"https://a.example/\">a</a></p>")
	type mark struct{}
	var calls []bool // for each call of the HTML's input: was it under the admitted context?
	old := htmlInputFn
	htmlInputFn = func(ctx context.Context, raw []byte, b Box, id string) (mailhtml.Input, string, bool, error) {
		calls = append(calls, ctx.Value(mark{}) != nil)
		return old(ctx, raw, b, id)
	}
	defer func() { htmlInputFn = old }()

	// Busy: nothing of the HTML is read, and the summary is unavailable.
	asked := 0
	busy := admitFunc(func(context.Context) (context.Context, func(), error) {
		asked++
		return nil, nil, contracts.ErrBusy
	})
	if h := messageHTML(WithRenderAdmission(ctx, busy), htmlRaw, b, "m1"); h == nil || h.Status != contracts.HTMLUnavailable || asked != 1 || len(calls) != 0 {
		t.Fatalf("busy: %+v, admission asked %d times, the HTML's input made %d times", h, asked, len(calls))
	}
	// Admitted: the input is made once, under the admitted context, and
	// the one release is called once.
	released := 0
	admitted := admitFunc(func(ctx context.Context) (context.Context, func(), error) {
		asked++
		c, cancel := context.WithCancel(context.WithValue(ctx, mark{}, true))
		return c, func() { released++; cancel() }, nil
	})
	asked = 0
	h := messageHTML(WithRenderAdmission(ctx, admitted), htmlRaw, b, "m1")
	if h == nil || h.Status != contracts.HTMLShown || h.Links != 1 || asked != 1 || released != 1 || len(calls) != 1 || !calls[0] {
		t.Fatalf("admitted: %+v, asked %d, released %d, calls %v", h, asked, released, calls)
	}
	// A message that holds no HTML asks for no slot at all: the text view
	// never waits behind renders.
	asked, calls = 0, nil
	plain := []byte("Content-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\nhello\r\n--m--\r\n")
	if h := messageHTML(WithRenderAdmission(ctx, admitted), plain, b, "m1"); h != nil || asked != 0 {
		t.Fatalf("a plain message: %+v, asked %d", h, asked)
	}
}

type admitFunc func(context.Context) (context.Context, func(), error)

func (f admitFunc) Admit(ctx context.Context) (context.Context, func(), error) { return f(ctx) }
