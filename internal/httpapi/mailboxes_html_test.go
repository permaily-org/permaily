package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/store"
)

// ADR 0026, "Showing a message's HTML", tests 35 and 36: the frame's route
// and the link route, with mailboxestest.Fake and the real reader.

// frameHeaders are the frame's headers, exactly, for the tests' app.
func wantFrameHeaders(t *testing.T, name string, h http.Header) {
	t.Helper()
	want := map[string]string{
		"Content-Type":                 "text/html; charset=utf-8",
		"Content-Security-Policy":      "sandbox allow-popups allow-popups-to-escape-sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'; frame-ancestors https://app.permaily.test",
		"X-Content-Type-Options":       "nosniff",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Referrer-Policy":              "no-referrer",
		"Cache-Control":                "no-store",
		"X-Dns-Prefetch-Control":       "off",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s: %s is %q, want %q", name, k, got, v)
		}
	}
	if h.Get("Content-Disposition") != "" || h.Get("Set-Cookie") != "" {
		t.Errorf("%s: %v", name, h)
	}
}

// frameDocumentCheck is frameCheck for the frame's route: the frame's
// headers on every answer; a body that is a fixed one, or a document with
// no script, no event attribute, links only to the app's link page and
// images only as data: URIs.
func frameDocumentCheck(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	wantFrameHeaders(t, name, rec.Header())
	body := rec.Body.String()
	if rec.Code != 200 {
		if !strings.Contains(body, "This message can&#39;t be shown") && !strings.Contains(body, "This message can't be shown") {
			t.Errorf("%s: %d with body %q", name, rec.Code, body)
		}
		if strings.Contains(body, "7f3a") {
			t.Errorf("%s: the message in an error", name)
		}
		return
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "script" || n.Namespace != "" || n.Data == "iframe" || n.Data == "form" || n.Data == "base" || n.Data == "link" {
				t.Errorf("%s: <%s> in the frame", name, n.Data)
			}
			for _, a := range n.Attr {
				switch {
				case strings.HasPrefix(a.Key, "on"):
					t.Errorf("%s: %s on <%s>", name, a.Key, n.Data)
				case a.Key == "href" && !strings.HasPrefix(a.Val, "/inbox/"):
					t.Errorf("%s: href %q", name, a.Val)
				case a.Key == "src" && !strings.HasPrefix(a.Val, "data:image/"):
					t.Errorf("%s: src %q", name, a.Val)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

// frameCall asks the frame's route as a browser's frame would, with the
// Fetch Metadata given (none when dest is "").
func frameCall(h http.Handler, cookie, path, dest, site string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://app.permaily.test"+path, nil)
	if dest != "" {
		r.Header.Set("Sec-Fetch-Dest", dest)
		r.Header.Set("Sec-Fetch-Mode", "navigate")
	}
	if site != "" {
		r.Header.Set("Sec-Fetch-Site", site)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func animatedGIF(t *testing.T) []byte {
	t.Helper()
	p := color.Palette{color.Black, color.White}
	frames := []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 2, 2), p), image.NewPaletted(image.Rect(0, 0, 2, 2), p)}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: frames, Delay: []int{1, 1}}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// htmlMessage is a message whose HTML has three links, two remote images
// (one a tracking pixel), an inline PNG by cid:, an animated GIF and a
// script.
func htmlMessage(t *testing.T) string {
	t.Helper()
	logo := base64.StdEncoding.EncodeToString(pngBytes(t, 4, 3))
	anim := base64.StdEncoding.EncodeToString(animatedGIF(t))
	return "From: Ann <ann@example.org>\r\nSubject: News-7f3a\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; boundary=r\r\n\r\n" +
		"--r\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" +
		"--a\r\nContent-Type: text/plain\r\n\r\nPlain words\r\n" +
		"--a\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>Hi <a href=\"https://one.example/a\">https://bank.example</a> <a href=\"http://two.example:8080/b?c=d\">two</a>" +
		" <a href=\"javascript:alert(1)\">no</a> <a href=\"https://three.example/\">three</a></p>" +
		"<img src=\"cid:logo@x\" alt=\"Logo\"><img src=\"cid:anim@x\" alt=\"Anim\">" +
		"<img src=\"https://track.example/p.gif\" width=1 height=1><img src=\"https://cdn.example/photo.jpg\" alt=\"Photo\">" +
		"<script>alert(1)</script>\r\n--a--\r\n" +
		"--r\r\nContent-Type: image/png\r\nContent-ID: <logo@x>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + logo + "\r\n" +
		"--r\r\nContent-Type: image/gif\r\nContent-ID: <anim@x>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + anim + "\r\n--r--\r\n"
}

func plainMeta() inbox.StoredMeta {
	return inbox.StoredMeta{From: []contracts.MailAddress{{Name: "Ann", Address: "ann@example.org"}}, Subject: "News-7f3a"}
}

// 35. The frame route's headers on 200 and on every error; 404 for each
// Fetch Metadata combination but iframe with same-origin, 200 with none;
// the owner checked before the raw message is read; error bodies fixed.
func TestHTML35TheFrameRoute(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.AppOrigin = appOrigin })
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	plain := a.seed(box.ID, "From: a@example.org\r\nSubject: x\r\n\r\nHello\r\n", plainMeta())
	path := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/html"

	ok := frameCall(a.h, "alice", path, "iframe", "same-origin")
	if ok.Code != 200 {
		t.Fatalf("%d %s", ok.Code, ok.Body)
	}
	frameDocumentCheck(t, "200", ok)
	if n := strings.Count(ok.Body.String(), `href="/inbox/`); n != 3 {
		t.Errorf("%d links", n)
	}
	if none := frameCall(a.h, "alice", path, "", ""); none.Code != 200 {
		t.Errorf("no Fetch Metadata: %d", none.Code)
	}
	for _, dest := range []string{"iframe", "document", "frame", "empty", "image", "embed", "object"} {
		for _, site := range []string{"same-origin", "same-site", "cross-site", "none", ""} {
			if dest == "iframe" && site == "same-origin" {
				continue
			}
			rec := frameCall(a.h, "alice", path, dest, site)
			want := 404 // not a frame's request
			if site == "same-site" || site == "cross-site" {
				want = 403 // another site's, refused first (finding 9 of the review)
			}
			if rec.Code != want {
				t.Errorf("%s %s: %d", dest, site, rec.Code)
			}
			frameDocumentCheck(t, dest+" "+site, rec)
		}
	}
	// A request with only Sec-Fetch-Site is one with Fetch Metadata.
	if rec := frameCall(a.h, "alice", path, "", "same-origin"); rec.Code != 404 {
		t.Errorf("site alone: %d", rec.Code)
	}

	a.reader.mu.Lock()
	a.reader.read = nil
	a.reader.mu.Unlock()
	cases := []struct {
		name, cookie, path string
		status             int
	}{
		{"no session", "", path, 401},
		{"a session that stands for nothing", "nobody", path, 401},
		{"another account's message", "bob", path, 404},
		{"no such message", "alice", "/api/v1/mailboxes/" + box.ID + "/messages/ffffffffffffffffffffffffffffffff/html", 404},
		{"no HTML", "alice", "/api/v1/mailboxes/" + box.ID + "/messages/" + plain + "/html", 404},
		{"not a message", "alice", "/api/v1/mailboxes/" + box.ID + "/messages/x/html", 404},
	}
	for _, c := range cases {
		rec := frameCall(a.h, c.cookie, c.path, "iframe", "same-origin")
		if rec.Code != c.status {
			t.Errorf("%s: %d", c.name, rec.Code)
		}
		frameDocumentCheck(t, c.name, rec)
		if c.status == 401 && !strings.Contains(rec.Body.String(), "formatted") {
			t.Errorf("%s: %q", c.name, rec.Body)
		}
		if c.name == "another account's message" {
			a.reader.mu.Lock()
			read := len(a.reader.read)
			a.reader.mu.Unlock()
			if read != 0 {
				t.Errorf("another account's message was read: %v", a.reader.read)
			}
		}
	}
	if rec := a.call("alice", "POST", path, ""); rec.Code != 405 && rec.Code != 404 {
		t.Errorf("POST: %d", rec.Code)
	}
	if strings.Contains(a.logs.String(), "7f3a") || strings.Contains(a.logs.String(), box.ID) {
		t.Errorf("the log: %s", a.logs)
	}
}

// 36. Message.html's counts equal what the document holds; unavailable when
// the deadline passes or no slot comes; the link route's answers.
func TestHTML36CountsDeadlinesAndLinks(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.AppOrigin = appOrigin })
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	base := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg

	var m contracts.Message
	read(t, a.call("alice", "GET", base, ""), 200, &m)
	wantHTML := contracts.MessageHTML{Status: contracts.HTMLShown, RemoteImages: 2, InlineImages: 1, InlineImagesOmitted: 1,
		InlineImagesOmittedBy: contracts.MessageImagesOmitted{Animated: 1}, Links: 3}
	if m.HTML == nil || *m.HTML != wantHTML {
		t.Fatalf("html %+v", m.HTML)
	}
	doc := frameCall(a.h, "alice", base+"/html", "iframe", "same-origin").Body.String()
	if strings.Count(doc, `href="/inbox/`) != m.HTML.Links || strings.Count(doc, `src="data:image/`) != m.HTML.InlineImages ||
		strings.Count(doc, "Remote image not loaded") != m.HTML.RemoteImages-1 || strings.Count(doc, "Animated image not shown") != 1 {
		t.Errorf("the document does not hold what Message.html counts: %s", doc)
	}

	// The link route: each link of the document, by its token.
	tokens := linkTokens(t, doc)
	wantLinks := []contracts.MessageLink{
		{URL: "https://one.example/a", Host: "one.example", Shown: "bank.example"},
		{URL: "http://two.example:8080/b?c=d", Host: "two.example:8080"},
		{URL: "https://three.example/", Host: "three.example"},
	}
	for i, tok := range tokens {
		var l contracts.MessageLink
		read(t, a.call("alice", "GET", base+"/links/"+tok, ""), 200, &l)
		wantLinks[i].Link = tok
		if l != wantLinks[i] {
			t.Errorf("link %d: %+v", i, l)
		}
	}
	other := tokens[1][:2] + strings.Repeat("A", 16)
	want(t, a.call("alice", "GET", base+"/links/"+other, ""), 409, "link_changed")
	want(t, a.call("alice", "GET", base+"/links/7."+tokens[0][2:], ""), 409, "link_changed")
	for _, bad := range []string{"01." + tokens[0][2:], "1000." + tokens[0][2:], "x", "1.short"} {
		want(t, a.call("alice", "GET", base+"/links/"+bad, ""), 404, "not_found")
	}
	want(t, a.call("bob", "GET", base+"/links/"+tokens[0], ""), 404, "not_found")

	// The deadline is the render's: past it, the view is unavailable.
	old := inbox.RenderDeadline
	inbox.RenderDeadline = time.Nanosecond
	read(t, a.call("alice", "GET", base, ""), 200, &m)
	if m.HTML == nil || m.HTML.Status != contracts.HTMLUnavailable || m.Text == "" {
		t.Errorf("past the deadline: %+v", m.HTML)
	}
	if rec := frameCall(a.h, "alice", base+"/html", "iframe", "same-origin"); rec.Code != 503 {
		t.Errorf("the frame past the deadline: %d", rec.Code)
	}
	want(t, a.call("alice", "GET", base+"/links/"+tokens[0], ""), 503, "busy")
	inbox.RenderDeadline = old
}

// linkTokens are the tokens of the links of a frame's document, in order.
func linkTokens(t *testing.T, doc string) []string {
	t.Helper()
	var out []string
	for _, part := range strings.Split(doc, `href="/inbox/`)[1:] {
		end := strings.IndexByte(part, '"')
		p := part[:end]
		out = append(out, p[strings.LastIndexByte(p, '/')+1:])
	}
	if len(out) == 0 {
		t.Fatal("no links")
	}
	return out
}

// 36. The render slots: two at once, a queue of sixteen that waits five
// seconds (smaller here), one running and two waiting per account, 429 and
// 503 past them, and Message.html unavailable when no slot comes.
func TestHTML36TheRenderSlots(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) {
		c.AppOrigin = appOrigin
		c.Renders, c.RenderQueue, c.RenderWait = 2, 2, 150*time.Millisecond
		s := c.Sessions.(sessions)
		s["carol"] = &accounts.Authenticated{AccountID: "acc_carolcccccccccccccccccccc", Session: contracts.Session{ID: "ses_d"}}
		s["dave"] = &accounts.Authenticated{AccountID: "acc_daveddddddddddddddddddddd", Session: contracts.Session{ID: "ses_e"}}
	})
	boxes := map[string]string{}
	msgs := map[string]string{}
	for cookie, account := range map[string]string{"alice": alice, "bob": bob, "carol": "acc_carolcccccccccccccccccccc", "dave": "acc_daveddddddddddddddddddddd"} {
		box := a.fake.Add(account, "Mine")
		boxes[cookie] = box.ID
		msgs[cookie] = a.seed(box.ID, htmlMessage(t), plainMeta())
	}
	// Each raw read of a message is held until released: a render that
	// took a slot holds it.
	var mu sync.Mutex
	holding := map[string]int{}
	release := make(chan struct{})
	held := make(chan string, 16)
	a.mem.Interrupt = func(ctx context.Context, step store.Step, partition, key string) error {
		if step != store.StepRead || !strings.HasPrefix(key, "msg#") ||
			partition != "mailbox#"+boxes["alice"] && partition != "mailbox#"+boxes["bob"] {
			return nil
		}
		mu.Lock()
		holding[key]++
		n := holding[key]
		mu.Unlock()
		if n%2 == 0 { // the item, then the raw object
			held <- partition
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return nil
	}
	path := func(who string) string {
		return "/api/v1/mailboxes/" + boxes[who] + "/messages/" + msgs[who] + "/html"
	}
	results := make(chan int, 16)
	ask := func(who string) {
		go func() { results <- frameCall(a.h, who, path(who), "iframe", "same-origin").Code }()
	}
	ask("alice")
	<-held
	ask("bob")
	<-held // two running
	ask("alice")
	ask("alice")
	time.Sleep(30 * time.Millisecond)
	// Alice has one running and two waiting: the next is 429 at once.
	if code := frameCall(a.h, "alice", path("alice"), "iframe", "same-origin").Code; code != 429 {
		t.Errorf("past the account's share: %d", code)
	}
	// The queue (two here) is full: carol is 503 at once.
	if code := frameCall(a.h, "carol", path("carol"), "iframe", "same-origin").Code; code != 503 {
		t.Errorf("past the queue: %d", code)
	}
	// Message.html, which needs a slot too, is unavailable.
	var m contracts.Message
	read(t, a.call("dave", "GET", "/api/v1/mailboxes/"+boxes["dave"]+"/messages/"+msgs["dave"], ""), 200, &m)
	if m.HTML == nil || m.HTML.Status != contracts.HTMLUnavailable {
		t.Errorf("no slot: %+v", m.HTML)
	}
	// The two waiting gave up after the wait: 503.
	for range 2 {
		if code := <-results; code != 503 {
			t.Errorf("a wait that ended: %d", code)
		}
	}
	close(release)
	for range 2 {
		if code := <-results; code != 200 {
			t.Errorf("a render that held a slot: %d", code)
		}
	}
	a.mem.Interrupt = nil
	// Everything given back: renders run again.
	for _, who := range []string{"alice", "bob", "carol"} {
		if code := frameCall(a.h, who, path(who), "iframe", "same-origin").Code; code != 200 {
			t.Errorf("%s after: %d", who, code)
		}
	}
}

// The link route answers JSON, never a document, and nothing of the
// message's address in an error.
func TestHTMLTheLinkRouteAnswersJSON(t *testing.T) {
	a := newInboxApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	rec := a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg+"/links/0.AAAAAAAAAAAAAAAA", "")
	if rec.Code != 409 || rec.Header().Get("Content-Type") != "application/json" || strings.Contains(rec.Body.String(), "example") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	var e httpapi.APIError
	if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Message != contracts.ErrLinkChanged.Message {
		t.Fatal(rec.Body.String())
	}
	// Without an app origin configured, frame-ancestors names the request's
	// own host.
	frame := frameCall(a.h, "alice", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg+"/html", "iframe", "same-origin")
	if !strings.HasSuffix(frame.Header().Get("Content-Security-Policy"), "frame-ancestors https://app.permaily.test") {
		t.Fatal(frame.Header())
	}
}

// The link route's other answers: a message that is not there, one without
// HTML, one whose HTML is not shown, the account's share of the slots.
func TestHTMLTheLinkRoutesOtherAnswers(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.Renders, c.RenderQueue, c.RenderWait = 1, 3, 50*time.Millisecond })
	box := a.fake.Add(alice, "Mine")
	plain := a.seed(box.ID, "Content-Type: text/plain\r\n\r\nx", plainMeta())
	large := a.seed(box.ID, "Content-Type: text/html\r\n\r\n"+strings.Repeat("<br>", 50_001), plainMeta())
	unreadable := a.seed(box.ID, "Content-Type: text/html; charset=utf-7\r\n\r\n<p>x</p>", plainMeta())
	base := "/api/v1/mailboxes/" + box.ID + "/messages/"
	tok := "/links/0.AAAAAAAAAAAAAAAA"
	want(t, a.call("alice", "GET", base+"ffffffffffffffffffffffffffffffff"+tok, ""), 404, "not_found")
	for _, m := range []string{plain, large, unreadable} {
		want(t, a.call("alice", "GET", base+m+tok, ""), 404, "not_found")
		if rec := frameCall(a.h, "alice", base+m+"/html", "iframe", "same-origin"); rec.Code != 404 {
			t.Errorf("the frame of %s: %d", m, rec.Code)
		}
	}
	var m contracts.Message
	read(t, a.call("alice", "GET", base+large, ""), 200, &m)
	if m.HTML == nil || m.HTML.Status != contracts.HTMLTooLarge {
		t.Errorf("%+v", m.HTML)
	}
	read(t, a.call("alice", "GET", base+unreadable, ""), 200, &m)
	if m.HTML == nil || m.HTML.Status != contracts.HTMLUnreadable {
		t.Errorf("%+v", m.HTML)
	}
	// A slot held by alice's own render: her link waits its turn, as one
	// running and two waiting; one more is too_many_requests.
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	release := make(chan struct{})
	held := make(chan struct{}, 4)
	var once sync.Once
	a.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && key == "msg#"+msg {
			once.Do(func() {
				held <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
		return nil
	}
	done := make(chan int, 4)
	go func() { done <- a.call("alice", "GET", base+msg+tok, "").Code }()
	<-held
	go func() { done <- a.call("alice", "GET", base+msg+tok, "").Code }()
	time.Sleep(20 * time.Millisecond)
	go func() { done <- a.call("alice", "GET", base+msg+tok, "").Code }()
	time.Sleep(20 * time.Millisecond)
	want(t, a.call("alice", "GET", base+msg+tok, ""), 429, "too_many_requests")
	// Bob waits in the queue, and is busy when his wait ends.
	bobBox := a.fake.Add(bob, "His")
	bobMsg := a.seed(bobBox.ID, htmlMessage(t), plainMeta())
	want(t, a.call("bob", "GET", "/api/v1/mailboxes/"+bobBox.ID+"/messages/"+bobMsg+tok, ""), 503, "busy")
	close(release)
	codes := []int{<-done, <-done, <-done}
	slices.Sort(codes)
	if codes[0] != 409 || codes[2] != 503 {
		t.Errorf("%v", codes)
	}
}

type failingSessions struct{}

func (failingSessions) Authenticate(context.Context, string) (*accounts.Authenticated, error) {
	return nil, errors.New("storage down for acc_aliceaaaaaaaaaaaaaaaaaaaaa")
}

// An error of the service's own is 500 with the frame's fixed body, logged
// without an identifier.
func TestHTMLTheFrameOnAnInternalError(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.Sessions = failingSessions{} })
	rec := frameCall(a.h, "alice", "/api/v1/mailboxes/mbx/messages/"+strings.Repeat("a", 32)+"/html", "iframe", "same-origin")
	if rec.Code != 500 {
		t.Fatal(rec.Code)
	}
	frameDocumentCheck(t, "500", rec)
	if !strings.Contains(a.logs.String(), "internal error") || strings.Contains(a.logs.String(), "acc_alice") {
		t.Fatal(a.logs.String())
	}
}

// A render waiting in the queue takes the slot given back; one whose
// request ends while it waits leaves the queue.
func TestHTMLAWaitingRenderTakesTheSlotGivenBack(t *testing.T) {
	a := newInboxApp(t, func(c *httpapi.MailboxesConfig) { c.Renders, c.RenderQueue, c.RenderWait = 1, 4, 5*time.Second })
	box := a.fake.Add(alice, "Mine")
	bobBox := a.fake.Add(bob, "His")
	msg := a.seed(box.ID, htmlMessage(t), plainMeta())
	bobMsg := a.seed(bobBox.ID, htmlMessage(t), plainMeta())
	release := make(chan struct{})
	held := make(chan struct{}, 1)
	var once sync.Once
	a.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && key == "msg#"+msg {
			once.Do(func() {
				held <- struct{}{}
				<-release
			})
		}
		return nil
	}
	alicePath := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/html"
	bobPath := "/api/v1/mailboxes/" + bobBox.ID + "/messages/" + bobMsg + "/html"
	first := make(chan int, 1)
	go func() { first <- frameCall(a.h, "alice", alicePath, "iframe", "same-origin").Code }()
	<-held
	// Bob's request ends while it waits: it leaves the queue.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("GET", "https://app.permaily.test"+bobPath, nil).WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "bob"})
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Errorf("a request that ended while waiting: %d", w.Code)
	}
	second := make(chan int, 1)
	go func() { second <- frameCall(a.h, "bob", bobPath, "iframe", "same-origin").Code }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if c1, c2 := <-first, <-second; c1 != 200 || c2 != 200 {
		t.Fatalf("%d %d", c1, c2)
	}
}
