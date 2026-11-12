package smtpd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The findings of PR #91's code review on the SMTP server: each test is the
// sequence the reviewer described.

// heldUntil makes Deliver wait for its context, and says when it ended.
type heldUntil struct {
	recipients
	mu    sync.Mutex
	ended time.Time
}

func (h *heldUntil) Deliver(ctx context.Context, _ string, _ []string, _ []byte) error {
	<-ctx.Done()
	h.mu.Lock()
	h.ended = time.Now()
	h.mu.Unlock()
	return ctx.Err()
}

// Review 6a: active DATA does not end at the deadline of the command that
// started it: lines every 50 ms for well past Command go through.
func TestReview6aDataIsNotBoundByTheCommandDeadline(t *testing.T) {
	f := start(t, Limits{Command: 200 * time.Millisecond, DataStall: time.Second, Transaction: 10 * time.Second})
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	c.do("DATA", 354)
	for i := range 12 {
		if _, err := fmt.Fprintf(c.conn, "line %d\r\n", i); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, _ = io.WriteString(c.conn, ".\r\n")
	if code, msg := c.answer(); code != 250 {
		t.Fatalf("%d %s", code, msg)
	}
}

// Review 6b: bytes that arrive steadily but slowly restart the stall timer,
// though no 32 KiB read completes.
func TestReview6bSlowBytesRestartTheStallTimer(t *testing.T) {
	f := start(t, Limits{DataStall: 100 * time.Millisecond, Transaction: 10 * time.Second})
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	c.do("DATA", 354)
	for range 24 {
		if _, err := io.WriteString(c.conn, "x"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	_, _ = io.WriteString(c.conn, "\r\n.\r\n")
	if code, msg := c.answer(); code != 250 {
		t.Fatalf("%d %s", code, msg)
	}
}

// Review 6c: RSET (or EHLO, NOOP) outside a transaction does not restart
// the idle deadline: a session that sends only those ends at Idle.
func TestReview6cRSETDoesNotKeepAnIdleSessionOpen(t *testing.T) {
	f := start(t, Limits{Idle: 150 * time.Millisecond})
	c := f.open(t, true)
	began := time.Now()
	for time.Since(began) < 2*time.Second {
		code, _, err := c.send("RSET")
		if err != nil || code != 250 {
			break
		}
		time.Sleep(60 * time.Millisecond)
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("an idle session kept open by RSET for %v", took)
	}
}

// Review 6d: the delivery's context carries the session's absolute end.
func TestReview6dTheDeliveryEndsWithTheSession(t *testing.T) {
	f := start(t, Limits{Session: 400 * time.Millisecond, Idle: time.Minute, Transaction: time.Minute, Ingest: time.Minute})
	held := &heldUntil{}
	f.srv.rcpts = held
	began := time.Now()
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	c.do("DATA", 354)
	_, _ = io.WriteString(c.conn, message+".\r\n")
	_, _ = c.answer()
	held.mu.Lock()
	ended := held.ended
	held.mu.Unlock()
	if ended.IsZero() || ended.Sub(began) > time.Second {
		t.Fatalf("the delivery ended %v after the session began", ended.Sub(began))
	}
}

// Review 7: BDAT chunks after the first are commands too: with Commands 20
// a session cannot send thirty.
func TestReview7aEveryBDATIsACommand(t *testing.T) {
	f := start(t, Limits{Commands: 20})
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	accepted := 0
	for range 30 {
		_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := c.tp.PrintfLine("BDAT 1"); err != nil {
			break
		}
		if _, err := io.WriteString(c.conn, "x"); err != nil {
			break
		}
		code, _ := c.answer()
		if code != 250 {
			break
		}
		accepted++
	}
	if accepted == 30 || !c.closed() {
		t.Fatalf("%d chunks accepted with Commands 20", accepted)
	}
	f.zero(t)
}

// Review 7: a chunk that makes the message larger than MaxMessage is refused
// and the connection closed, as a refused DATA is, not drained; nothing is
// stored. go-smtp answers a chunk that is not the last once it has copied it
// to the session, so a chunk that ends exactly one byte past the limit is
// answered 250 before the session sees that byte: the session ends at the
// next command (421) all the same.
func TestReview7bAnOversizedChunkCloses(t *testing.T) {
	f := start(t, Limits{MaxMessage: 32})
	for _, chunk := range []string{"BDAT 40", "BDAT 33 LAST"} {
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
		_ = c.tp.PrintfLine("%s", chunk)
		n := 40
		if strings.HasPrefix(chunk, "BDAT 33") {
			n = 33
		}
		_, _ = io.WriteString(c.conn, strings.Repeat("x", n))
		if code, msg := c.answer(); code != 552 || !c.closed() {
			t.Fatalf("%s: %d %s", chunk, code, msg)
		}
	}
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	_ = c.tp.PrintfLine("BDAT 33")
	_, _ = io.WriteString(c.conn, strings.Repeat("x", 33))
	if code, _ := c.answer(); code != 552 && code != 250 {
		t.Fatalf("BDAT 33: %d", code)
	}
	if code, _, _ := c.send("BDAT 1 LAST"); code == 250 || !c.closed() {
		t.Fatalf("the session goes on after an oversized chunk: %d", code)
	}
	if f.in.count() != 0 {
		t.Fatal("an oversized message was stored")
	}
	f.zero(t)
}

// Review 7: a delivery that fails is a refusal: the eleventh closes the
// session with the cap at ten.
func TestReview7cFailedDeliveriesAreRefusals(t *testing.T) {
	f := start(t, Limits{Failed: 10})
	f.in.fail = errors.New("the store failed")
	c := f.open(t, true)
	for i := range 10 {
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		if code, msg := c.data(message); code != 451 {
			t.Fatalf("delivery %d: %d %s", i+1, code, msg)
		}
	}
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	if code, msg := c.data(message); code != 421 || !strings.HasPrefix(msg, "4.7.0") || !c.closed() {
		t.Fatalf("the eleventh: %d %s", code, msg)
	}
}

// Review 12: a message of exactly MaxMessage bytes is accepted, by DATA and
// by BDAT; one byte more is refused; a declared SIZE over it is refused at
// MAIL FROM.
func TestReview12AMessageAtTheLimitIsAccepted(t *testing.T) {
	const limit = 64
	body := strings.Repeat("y", limit-2) + "\r\n"
	f := start(t, Limits{MaxMessage: limit})
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	if code, msg := c.data(body); code != 250 || len(f.in.delivered[0]) != limit {
		t.Fatalf("DATA of exactly the limit: %d %s", code, msg)
	}
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	if code, msg := bdat(t, c, body, true); code != 250 {
		t.Fatalf("BDAT of exactly the limit: %d %s", code, msg)
	}
	if code, _, _ := c.send(fmt.Sprintf("MAIL FROM:<a@example.org> SIZE=%d", limit+1)); code != 552 {
		t.Fatalf("SIZE over the limit: %d", code)
	}
	c.do(fmt.Sprintf("MAIL FROM:<a@example.org> SIZE=%d", limit), 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	if code, _ := c.data("x" + body); code != 552 || !c.closed() {
		t.Fatalf("DATA one byte over: %d", code)
	}
	d := f.open(t, true)
	d.do("MAIL FROM:<a@example.org>", 250)
	d.do("RCPT TO:<u@inbox.test>", 250)
	if code, _ := bdat(t, d, "x"+body, true); code != 552 || !d.closed() {
		t.Fatalf("BDAT one byte over: %d", code)
	}
	f.zero(t)
}

// Review 12: the size is the session's to check. EHLO advertises SIZE
// without a number (go-smtp's reader of DATA would refuse a message of
// exactly a number it was given) and CHUNKING, which go-smtp always offers.
func TestReview12EHLOAdvertisesSize(t *testing.T) {
	f := start(t, Limits{})
	c := f.open(t, true)
	lines := strings.Split(c.do("EHLO client.test", 250), "\n")
	if !slices.Contains(lines, "SIZE") || !slices.Contains(lines, "CHUNKING") || !slices.Contains(lines, "PIPELINING") {
		t.Fatalf("%q", lines)
	}
}
