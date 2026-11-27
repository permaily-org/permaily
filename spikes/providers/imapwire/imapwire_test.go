package imapwire

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

var update = flag.Bool("update", false, "rewrite the transcripts in testdata/")

const (
	user = "synthetic-user"
	pass = "synthetic-app-password"
)

var fixedTime = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func synthetic(subject string, body []byte) []byte {
	head := fmt.Sprintf("From: sender@example.test\r\nTo: receiver@example.test\r\nSubject: %s\r\nDate: Fri, 27 Nov 2026 12:00:00 +0000\r\nMessage-ID: <%s@example.test>\r\n\r\n", subject, subject)
	return append([]byte(head), body...)
}

// memServer starts go-imap's in-memory server on a loopback port.
func memServer(t *testing.T, caps imap.CapSet) (*imapmemserver.User, string) {
	t.Helper()
	mem := imapmemserver.New()
	u := imapmemserver.NewUser(user, pass)
	if err := u.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         caps,
		InsecureAuth: true, // loopback only; real hosts are 993 with implicit TLS
		Logger:       log.New(io.Discard, "", 0),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return u, ln.Addr().String()
}

func appendMsg(t *testing.T, u *imapmemserver.User, mailbox string, msg []byte) {
	t.Helper()
	if _, err := u.Append(mailbox, bytes.NewReader(msg), &imap.AppendOptions{Time: fixedTime}); err != nil {
		t.Fatal(err)
	}
}

func dial(t *testing.T, addr string) (*imapclient.Client, *Recorder) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	rec := Record(conn)
	c := imapclient.New(rec, nil)
	t.Cleanup(func() { _ = c.Close() })
	if err := c.WaitGreeting(); err != nil {
		t.Fatal(err)
	}
	if err := c.Login(user, pass).Wait(); err != nil {
		t.Fatal(err)
	}
	return c, rec
}

func logout(t *testing.T, c *imapclient.Client) {
	t.Helper()
	if err := c.Logout().Wait(); err != nil {
		t.Fatal(err)
	}
}

// canonicalFetch sorts the plain items of a client FETCH list after UID.
// beta.9's writeFetchItems ranges over a Go map for FLAGS, MODSEQ,
// INTERNALDATE and the like, so their order on the wire changes from run to
// run (a finding: the adapter's own command writer must fix the order).
func canonicalFetch(line string) string {
	i := strings.Index(line, " FETCH ")
	if !strings.HasPrefix(line, "C: ") || i < 0 {
		return line
	}
	open := strings.Index(line[i:], "(")
	if open < 0 {
		return line
	}
	open += i
	end := strings.Index(line[open:], ")")
	if end < 0 {
		return line
	}
	end += open
	inner := line[open+1 : end]
	if strings.ContainsAny(inner, "(") {
		return line
	}
	items := strings.Fields(inner)
	rest := items
	var head []string
	if len(items) > 0 && items[0] == "UID" {
		head, rest = items[:1], items[1:]
	}
	sort.Strings(rest)
	return line[:open+1] + strings.Join(append(head, rest...), " ") + line[end:]
}

func canonical(transcript string) string {
	lines := strings.Split(transcript, "\n")
	for i, l := range lines {
		lines[i] = canonicalFetch(l)
	}
	return strings.Join(lines, "\n")
}

// golden compares a transcript with testdata/<name>.txt (-update rewrites).
func golden(t *testing.T, name, got string) {
	t.Helper()
	got = canonical(got)
	path := filepath.Join("testdata", name+".txt")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("transcript %s changed:\n%s", name, got)
	}
}

func hasSeen(flags []imap.Flag) bool {
	for _, f := range flags {
		if f == imap.FlagSeen {
			return true
		}
	}
	return false
}

func flagsOf(t *testing.T, c *imapclient.Client) []imap.Flag {
	t.Helper()
	msgs, err := c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	return msgs[0].Flags
}

// Read-only select is EXAMINE, and BODY.PEEK[] leaves \Seen unset.
func TestExamineAndPeek(t *testing.T) {
	u, addr := memServer(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	appendMsg(t, u, "INBOX", synthetic("first", []byte("Synthetic body one.\r\n")))
	appendMsg(t, u, "INBOX", synthetic("second", []byte("Synthetic body two.\r\n")))
	c, rec := dial(t, addr)

	data, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if data.NumMessages != 2 || data.UIDValidity == 0 {
		t.Errorf("select data %+v", data)
	}
	if hasSeen(flagsOf(t, c)) {
		t.Fatal("\\Seen before reading")
	}
	msgs, err := c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || len(msgs[0].BodySection) != 1 {
		t.Fatalf("fetch %+v", msgs)
	}
	if hasSeen(flagsOf(t, c)) {
		t.Error("BODY.PEEK[] under EXAMINE set \\Seen")
	}
	logout(t, c)
	golden(t, "examine-peek", rec.Transcript())
}

// The in-memory server sets \Seen for a non-peek BODY[] even under EXAMINE
// (its code says "TODO: forbid write commands in read-only mode"): one more
// reason the adapter only ever sends BODY.PEEK[], and that a played server
// is no evidence of a real host's behaviour.
func TestMemServerSetsSeenUnderExamine(t *testing.T) {
	u, addr := memServer(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	appendMsg(t, u, "INBOX", synthetic("first", []byte("Synthetic body one.\r\n")))
	c, rec := dial(t, addr)
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{}}}).Collect(); err != nil {
		t.Fatal(err)
	}
	seen := hasSeen(flagsOf(t, c))
	logout(t, c)
	t.Logf("in-memory server, BODY[] under EXAMINE: \\Seen set = %v", seen)
	golden(t, "memserver-body-under-examine", rec.Transcript())
}

func TestUIDSearch(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps imap.CapSet
		opts *imap.SearchOptions
	}{
		{"uid-search-rev1", imap.CapSet{imap.CapIMAP4rev1: {}}, nil},
		{"uid-search-esearch", imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapESearch: {}}, &imap.SearchOptions{ReturnAll: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, addr := memServer(t, tc.caps)
			for i := range 3 {
				appendMsg(t, u, "INBOX", synthetic(fmt.Sprintf("m%d", i), []byte("Synthetic.\r\n")))
			}
			c, rec := dial(t, addr)
			if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
				t.Fatal(err)
			}
			all, err := c.UIDSearch(&imap.SearchCriteria{}, tc.opts).Wait()
			if err != nil {
				t.Fatal(err)
			}
			if got := all.AllUIDs(); len(got) != 3 {
				t.Errorf("UID SEARCH ALL: %v", got)
			}
			// The presence search the ADR's deletions use: is UID 2 still here?
			var set imap.UIDSet
			set.AddNum(2, 9)
			some, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, tc.opts).Wait()
			if err != nil {
				t.Fatal(err)
			}
			if got := some.AllUIDs(); len(got) != 1 || got[0] != 2 {
				t.Errorf("UID SEARCH UID 2,9: %v", got)
			}
			since, err := c.UIDSearch(&imap.SearchCriteria{Since: fixedTime.AddDate(0, 0, -1)}, tc.opts).Wait()
			if err != nil {
				t.Fatal(err)
			}
			if len(since.AllUIDs()) != 3 {
				t.Errorf("UID SEARCH SINCE: %v", since.AllUIDs())
			}
			logout(t, c)
			golden(t, tc.name, rec.Transcript())
		})
	}
}

// The in-memory server does not implement CONDSTORE at all: SELECT with the
// parameter and FETCH with CHANGEDSINCE are refused.
func TestCondStoreAgainstMemServer(t *testing.T) {
	u, addr := memServer(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	appendMsg(t, u, "INBOX", synthetic("first", []byte("Synthetic.\r\n")))
	c, rec := dial(t, addr)
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true, CondStore: true}).Wait(); err == nil {
		t.Error("EXAMINE (CONDSTORE) accepted by the in-memory server")
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	var all imap.UIDSet
	all.AddRange(1, 0)
	if _, err := c.Fetch(all, &imap.FetchOptions{UID: true, Flags: true, ChangedSince: 1}).Collect(); err == nil {
		t.Error("FETCH (CHANGEDSINCE) accepted by the in-memory server")
	}
	logout(t, c)
	golden(t, "condstore-memserver", rec.Transcript())
}

// scripted answers go-imap's client with RFC 7162's own example responses
// (sections 3.1.2.1 and 3.1.4.1), to show the client's side of CONDSTORE.
func scripted(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		w := bufio.NewWriter(conn)
		send := func(lines ...string) {
			for _, l := range lines {
				_, _ = w.WriteString(l + "\r\n")
			}
			_ = w.Flush()
		}
		send(`* OK [CAPABILITY IMAP4rev1 ENABLE CONDSTORE] scripted server (RFC 7162 examples)`)
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				return
			}
			tag, cmd := f[0], strings.ToUpper(f[1])
			if cmd == "UID" && len(f) > 2 {
				cmd += " " + strings.ToUpper(f[2])
			}
			switch cmd {
			case "CAPABILITY":
				send(`* CAPABILITY IMAP4rev1 ENABLE CONDSTORE`, tag+" OK CAPABILITY completed")
			case "LOGIN":
				send(tag + " OK [CAPABILITY IMAP4rev1 ENABLE CONDSTORE] LOGIN completed")
			case "ENABLE":
				send(`* ENABLED CONDSTORE`, tag+" OK ENABLE completed")
			case "EXAMINE":
				send(`* 172 EXISTS`, `* OK [UIDVALIDITY 3857529045] UIDs valid`, `* OK [UIDNEXT 4392] Predicted next UID`,
					`* FLAGS (\Answered \Flagged \Deleted \Seen \Draft)`, `* OK [PERMANENTFLAGS ()] No permanent flags permitted`,
					`* OK [HIGHESTMODSEQ 715194045007] Highest`, tag+" OK [READ-ONLY] EXAMINE completed")
			case "UID FETCH":
				send(`* 1 FETCH (UID 4 MODSEQ (65402) FLAGS (\Seen))`, `* 2 FETCH (UID 6 MODSEQ (75403) FLAGS (\Deleted))`,
					`* 4 FETCH (UID 8 MODSEQ (29738) FLAGS ($NoJunk $AutoJunk $MDNSent))`, tag+" OK FETCH completed")
			case "LOGOUT":
				send(`* BYE logging out`, tag+" OK LOGOUT completed")
				return
			default:
				send(tag + " BAD unexpected in this script")
			}
		}
	}()
	return ln.Addr().String()
}

func TestCondStoreClientSide(t *testing.T) {
	c, rec := dial(t, scripted(t))
	// beta.9's client refuses ENABLE CONDSTORE before sending anything (it
	// allows only IMAP4rev2, UTF8=ACCEPT and METADATA); RFC 7162 section 3.1
	// lets the CONDSTORE parameter of SELECT or EXAMINE enable it instead.
	if _, err := c.Enable(imap.CapCondStore).Wait(); err == nil {
		t.Error("ENABLE CONDSTORE accepted by the client")
	}
	data, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true, CondStore: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if data.HighestModSeq != 715194045007 || data.UIDValidity != 3857529045 {
		t.Errorf("select data %+v", data)
	}
	var all imap.UIDSet
	all.AddRange(1, 0)
	msgs, err := c.Fetch(all, &imap.FetchOptions{UID: true, Flags: true, ModSeq: true, ChangedSince: 12345}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	want := map[imap.UID]uint64{4: 65402, 6: 75403, 8: 29738}
	if len(msgs) != len(want) {
		t.Fatalf("fetched %d", len(msgs))
	}
	for _, m := range msgs {
		if want[m.UID] != m.ModSeq {
			t.Errorf("UID %d: MODSEQ %d, want %d", m.UID, m.ModSeq, want[m.UID])
		}
	}
	logout(t, c)
	golden(t, "condstore-client", rec.Transcript())
}

// A 24 MiB BODY.PEEK[] literal reaches the caller as a stream: the first
// bytes are handed over while most of the literal is still unread.
func TestStreamedLiteral(t *testing.T) {
	const (
		consumed     = 64 << 10 // what the caller reads before the check
		maxReadAhead = 1 << 20  // generous for bufio and earlier responses, 1/24 of the literal
	)
	u, addr := memServer(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	body := bytes.Repeat([]byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ+/\r\n"), (24<<20)/66)
	msg := synthetic("large", body)
	want := sha256.Sum256(msg)
	appendMsg(t, u, "INBOX", msg)
	c, rec := dial(t, addr)
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	cmd := c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}}})
	m := cmd.Next()
	if m == nil {
		t.Fatal("no message")
	}
	var streamed bool
	for {
		item := m.Next()
		if item == nil {
			break
		}
		sec, ok := item.(imapclient.FetchItemDataBodySection)
		if !ok {
			continue
		}
		if sec.Literal.Size() != int64(len(msg)) {
			t.Errorf("literal size %d, want %d", sec.Literal.Size(), len(msg))
		}
		h := sha256.New()
		if _, err := io.CopyN(h, sec.Literal, consumed); err != nil {
			t.Fatal(err)
		}
		readSoFar := rec.BytesRead()
		// Streaming means a bounded read-ahead: what the client pulled from
		// the socket beyond what the caller consumed is a few buffers (the
		// responses before the literal and bufio's buffer), not the literal.
		// The exact count depends on socket and bufio buffering.
		ahead := readSoFar - consumed
		streamed = ahead <= maxReadAhead
		t.Logf("after %d bytes of a %d-byte literal the client had read %d bytes from the socket (%d ahead, limit %d)", consumed, len(msg), readSoFar, ahead, maxReadAhead)
		if _, err := io.Copy(h, sec.Literal); err != nil {
			t.Fatal(err)
		}
		if got := h.Sum(nil); !bytes.Equal(got, want[:]) {
			t.Error("literal content differs")
		}
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	if !streamed {
		t.Errorf("the client read more than %d bytes ahead of the caller: the literal was buffered before it was handed over", maxReadAhead)
	}
	logout(t, c)
	golden(t, "streamed-literal", rec.Transcript())
}

// EXAMINE of a folder that never existed, or was deleted, is NO [NONEXISTENT];
// a folder made again under the same name has a greater UIDVALIDITY.
func TestNonexistentAndRecreated(t *testing.T) {
	u, addr := memServer(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	c, rec := dial(t, addr)
	nonexistent := func(name string) {
		t.Helper()
		_, err := c.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait()
		var ierr *imap.Error
		if !errors.As(err, &ierr) || ierr.Type != imap.StatusResponseTypeNo || ierr.Code != imap.ResponseCodeNonExistent {
			t.Errorf("EXAMINE %s: %v, want NO [NONEXISTENT]", name, err)
		}
	}
	nonexistent("Never-Existed")

	// The person makes, deletes and remakes a folder (out of band: Permaily
	// never sends CREATE or DELETE).
	if err := u.Create("Scratch", nil); err != nil {
		t.Fatal(err)
	}
	first, err := c.Select("Scratch", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := u.Delete("Scratch"); err != nil {
		t.Fatal(err)
	}
	nonexistent("Scratch")
	if err := u.Create("Scratch", nil); err != nil {
		t.Fatal(err)
	}
	again, err := c.Select("Scratch", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if again.UIDValidity <= first.UIDValidity {
		t.Errorf("UIDVALIDITY %d after remaking, was %d", again.UIDValidity, first.UIDValidity)
	}
	logout(t, c)
	golden(t, "nonexistent-recreated", rec.Transcript())
}
