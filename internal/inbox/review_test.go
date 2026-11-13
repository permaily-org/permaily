package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// The findings of PR #91's code review on the inbox: each test is the
// sequence the reviewer described.

func htmlMessage(body string) []byte {
	return []byte("From: a@example.org\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + body)
}

// allocated is what f allocates, in bytes.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// Review 3: text inside a link, repeated, made the extraction copy the link's
// text each time: 262 KiB of HTML allocated 568 MiB, and twice the input four
// times as much. The work is linear: a few times the input, at any size.
func TestReview3TextFromHTMLIsLinear(t *testing.T) {
	small := htmlMessage("<a href=x>" + strings.Repeat("x<b></b>", 32768))
	big := htmlMessage("<a href=x>" + strings.Repeat("x<b></b>", 65536))
	a := allocated(func() { _, _, _ = inbox.MessageText(context.Background(), small, inbox.MessageTextMax) })
	b := allocated(func() { _, _, _ = inbox.MessageText(context.Background(), big, inbox.MessageTextMax) })
	if a > 64*uint64(len(small)) || b > 64*uint64(len(big)) {
		t.Fatalf("%d bytes allocated for %d of HTML, %d for %d", a, len(small), b, len(big))
	}
	// Nested links that never close, each with a long target: the state kept
	// while scanning is bounded too.
	nested := htmlMessage(strings.Repeat("<a href="+strings.Repeat("h", 4096)+">t", 400))
	if n := allocated(func() { _, _, _ = inbox.MessageText(context.Background(), nested, 1<<10) }); n > 64*uint64(len(nested)) {
		t.Fatalf("%d bytes for nested links", n)
	}
}

// Review 3: the delivery's deadline stops the extraction between tokens.
func TestReview3TheDeadlineStopsTheExtraction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw := htmlMessage(strings.Repeat("<p>word</p>", 100000))
	began := time.Now()
	text, _, truncated := inbox.MessageText(ctx, raw, inbox.MessageTextMax)
	if !truncated || len(text) > 64<<10 || time.Since(began) > time.Second {
		t.Fatalf("%d bytes, truncated %v, %v", len(text), truncated, time.Since(began))
	}
	if p := inbox.Parse(ctx, raw); !p.Meta.Partial {
		t.Fatal("a parse stopped by its deadline is not marked partial")
	}
}

// Review 5: characters whose lower case has another length, before an
// attribute or inside a skipped element, made the extraction slice out of
// bounds.
func TestReview5LowerCaseOfAnotherLengthDoesNotPanic(t *testing.T) {
	for _, body := range []string{
		"<a " + strings.Repeat("Ⱥ", 20) + " href=x>y</a>",
		"<script>" + strings.Repeat("Ⱥ", 20) + "</script>after",
		"<STYLE>" + strings.Repeat("ȺȾ", 20) + "</style>after",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%q: %v", body, r)
				}
			}()
			_, _, _ = inbox.MessageText(context.Background(), htmlMessage(body), 1<<10)
			_ = inbox.Parse(context.Background(), htmlMessage(body))
		}()
	}
}

// Review 13: "</scriptx>" does not end a script, so what follows it is still
// the script's; HTML cut at the input cap says so.
func TestReview13ScriptEndsAtItsOwnEndTag(t *testing.T) {
	text, _, _ := inbox.MessageText(context.Background(), htmlMessage("<script>secret</scriptx>hidden</script>after"), 1<<10)
	if text != "after" {
		t.Fatalf("%q", text)
	}
	// More HTML than is read for text: the text says it was cut, though what
	// was read gives less text than the output's bound.
	long := htmlMessage("<p>start</p>" + strings.Repeat("<b></b>", (2<<20)/7+1) + "<p>end</p>")
	text, _, truncated := inbox.MessageText(context.Background(), long, inbox.MessageTextMax)
	if !truncated || strings.Contains(text, "end") || !strings.HasPrefix(text, "start") {
		t.Fatalf("%q %v", text, truncated)
	}
}

// Review 8: the header block of every MIME entity obeys MaxHeaderBytes, and a
// field kept from a part (its content type) obeys its own bound, before it is
// kept: past either the parse is partial.
func TestReview8HeadersOfPartsObeyTheLimits(t *testing.T) {
	ctx := context.Background()
	bigHeader := "Content-Type: multipart/mixed; boundary=p\r\n\r\n--p\r\nContent-Type: text/plain\r\n" +
		strings.Repeat("X-Pad: "+strings.Repeat("p", 990)+"\r\n", 280) + "\r\nbody\r\n--p--\r\n"
	if p := inbox.Parse(ctx, []byte(bigHeader)); !p.Meta.Partial {
		t.Fatal("a part with 280 KiB of headers is not partial")
	}
	longType := "Content-Type: multipart/mixed; boundary=p\r\n\r\n--p\r\nContent-Type: application/" + strings.Repeat("x", 600000) +
		"\r\nContent-Disposition: attachment; filename=a.bin\r\n\r\nbody\r\n--p--\r\n"
	p := inbox.Parse(ctx, []byte(longType))
	if !p.Meta.Partial {
		t.Fatal("a content type of 600,012 bytes is not partial")
	}
	// A type within the header's limit and past its own: not kept.
	midType := strings.Replace(longType, strings.Repeat("x", 600000), strings.Repeat("x", 10000), 1)
	p = inbox.Parse(ctx, []byte(midType))
	if !p.Meta.Partial || len(p.Meta.Attachments) != 1 || p.Meta.Attachments[0].Type != "application/octet-stream" {
		t.Fatalf("a content type of 10,012 bytes: partial %v, %d attachments", p.Meta.Partial, len(p.Meta.Attachments))
	}
}

// Review 8: the metadata of a message, serialised, has a total bound checked
// before it is written: every field may be within its own limit and the sum
// still larger than a DynamoDB item.
func TestReview8TheMetadataHasATotalBound(t *testing.T) {
	field := strings.Repeat("<", inbox.MaxHeaderFieldBytes-100)
	raw := "From: " + field + "\r\nTo: " + field + "\r\nCc: " + field + "\r\nReply-To: " + field + "\r\nMessage-ID: " + field +
		"\r\nDate: " + field + "\r\nSubject: x\r\nContent-Type: text/plain\r\n\r\nbody\r\n"
	p := inbox.Parse(context.Background(), []byte(raw))
	meta, err := json.Marshal(p.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) > inbox.MaxMetaBytes || !p.Meta.Partial {
		t.Fatalf("%d bytes of metadata, partial %v", len(meta), p.Meta.Partial)
	}
	// What is written is within it too.
	mem := store.NewMemory()
	c := &clock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	w := writer(mem, c)
	claim, err := w.Claim(context.Background(), box, "seen-meta", 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Store(context.Background(), box, claim, &p, []byte(raw), nil); err != nil {
		t.Fatal(err)
	}
	msgs, _ := mem.ListMessages(context.Background(), box.MailboxID, 10, "")
	if len(msgs) != 1 || len(msgs[0].Meta) > inbox.MaxMetaBytes+1024 {
		t.Fatalf("%+v", msgs)
	}
}

// Review 9: A stores everything; B takes the stale claim over and stops; A's
// mark conflicts. A answers success only when the entry then says the message
// is stored: here it writes the mark at the version B left, the claim being
// the same message.
func TestReview9AMarkThatConflictsIsWrittenOrFails(t *testing.T) {
	ctx := context.Background()
	p := inbox.Parse(ctx, []byte(plainMessage))
	mem := store.NewMemory()
	c := &clock{t: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	mem.Now = c.now
	w := writer(mem, c)
	a, err := w.Claim(ctx, box, "seen9", 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Store(ctx, box, a, &p, []byte(plainMessage), nil); err != nil {
		t.Fatal(err)
	}
	c.add(3 * time.Minute)
	b, err := w.Claim(ctx, box, "seen9", 7) // B takes it over, and stops
	if err != nil || b.Seen.Message != a.Seen.Message {
		t.Fatalf("%v", err)
	}
	if err := w.Mark(ctx, box, a); err != nil {
		t.Fatal(err)
	}
	again, err := w.Claim(ctx, box, "seen9", 7)
	if err != nil || !again.Stored() {
		t.Fatalf("after A's success the entry is not a stored mark: %v", err)
	}
	// The entry is a claim of another message (a value nobody of this
	// deployment writes at that key, as whoever runs the table could): A's
	// mark is an error the sender retries, never a success.
	a2, err := w.Claim(ctx, box, "seen9b", 7)
	if err != nil {
		t.Fatal(err)
	}
	c.add(3 * time.Minute)
	other, err := w.Claim(ctx, box, "seen9b", 7)
	if err != nil {
		t.Fatal(err)
	}
	other.Seen.Message = inbox.NewMessageID(c.now())
	if err := w.Mark(ctx, box, other); err != nil { // a stored mark of another message
		t.Fatal(err)
	}
	if err := w.Mark(ctx, box, a2); err == nil {
		t.Fatal("a mark that conflicts with another message's answers success")
	}
	// An entry that does not open, or that is gone: an error too.
	a3, err := w.Claim(ctx, box, "seen9c", 7)
	if err != nil {
		t.Fatal(err)
	}
	mem.SetEntry(store.Entry{Partition: inbox.Partition(box.MailboxID), Key: inbox.KeySeen + "seen9c", Value: []byte("junk"), Version: 9})
	if err := w.Mark(ctx, box, a3); err == nil {
		t.Fatal("a mark over an entry that does not open answers success")
	}
	a4, err := w.Claim(ctx, box, "seen9d", 7)
	if err != nil {
		t.Fatal(err)
	}
	_ = mem.DeleteEntry(ctx, inbox.Partition(box.MailboxID), inbox.KeySeen+"seen9d")
	mem.SetEntry(store.Entry{Partition: inbox.Partition(box.MailboxID), Key: inbox.KeySeen + "seen9d", Value: []byte("junk"), Version: 5})
	_ = mem.DeleteEntry(ctx, inbox.Partition(box.MailboxID), inbox.KeySeen+"seen9d")
	if err := w.Mark(ctx, box, a4); err == nil {
		t.Fatal("a mark over an entry that is gone answers success")
	}
	mem.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepRead {
			return errors.New("down")
		}
		return nil
	}
	a5 := *a2
	if err := w.Mark(ctx, box, &a5); err == nil {
		t.Fatal("a mark whose read failed answers success")
	}
	mem.Interrupt = nil
}

// Review 11: with the mark on, a signed message from Gmail's forwarding
// address whose Subject is not the template is not verified, though its body
// is: the requester and the code come from the signed Subject.
func TestReview11VerificationNeedsTheSubject(t *testing.T) {
	withVerify(t)
	raw := "From: Gmail Team <forwarding-noreply@google.com>\r\nTo: " + address + "\r\nSubject: Something else\r\n\r\n" + template(address)
	recognised, verified, other := verdict(t, sign(t, raw, "From", "To", "Subject"))
	if !recognised || verified || other {
		t.Fatalf("recognised %v, verified %v, for another %v", recognised, verified, other)
	}
	// The template's own Subject still verifies.
	good := confirmation("forwarding-noreply@google.com", address, template(address))
	if _, verified, _ := verdict(t, sign(t, good, "From", "To", "Subject")); !verified {
		t.Fatal("the template no longer verifies")
	}
	// A Subject naming a code the body does not: the Subject's is kept.
	p := inbox.Parse(context.Background(), []byte(strings.Replace(good, "(#123456789)", "(#555555555)", 1)))
	if p.Confirmation == nil || p.Confirmation.Code != "555555555" {
		t.Fatalf("%+v", p.Confirmation)
	}
	_ = fmt.Sprint
}
