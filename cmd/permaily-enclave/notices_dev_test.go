//go:build dev

package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/notices"
)

// connectionsSteps is what noticesEndToEnd takes from connectionsEndToEnd:
// its token endpoint and the agent's connection, still active.
type connectionsSteps struct {
	token func(enclave int, form url.Values) (*connectionsTokens, vendorAnswer)
	agent *connectionsTokens
}

// The subjects of the notices about connections (accounts' words, ADR 0041).
const (
	subjectApproved    = "A connection to your mail was approved"
	subjectDeviceAdded = "A device was added to one of your connections"
	subjectRenewed     = "One of your connections was renewed"
	subjectEnding      = "One of your connections ends in a week"
	subjectEndedReuse  = "Permaily ended a device of one of your connections"
)

// outboxNotices reads the notices to Bea in the outboxes of the enclaves
// of TestMailEndToEnd (E40).
type outboxNotices struct {
	t *testing.T
	w e2eWorld
}

// outboxMail is a message of the outbox.
type outboxMail struct{ To, Subject, Text, HTML string }

func (o outboxNotices) decode(a mailAnswer, status int, v any) {
	o.t.Helper()
	if a.code != status || (v != nil && json.Unmarshal([]byte(a.body), v) != nil) {
		o.t.Fatalf("%d %s, want %d", a.code, a.body, status)
	}
}

// told is every notice to Bea with the subject in either enclave's outbox
// (whichever enclave's attempt sent it).
func (o outboxNotices) told(subject string) []outboxMail {
	o.t.Helper()
	var all []outboxMail
	for enclave := range o.w.enclaves {
		var out []outboxMail
		o.decode(o.w.call(o.w.newBrowser(), enclave, "GET", "/dev/outbox", ""), 200, &out)
		for _, m := range out {
			if m.To == "bea@example.org" && m.Subject == subject {
				all = append(all, m)
			}
		}
	}
	return all
}

// once waits until want notices with the subject were sent, lets every
// queue make a pass that tries what is left, due or not, and checks that
// no more were, and that none names the vendor's domain, a client's name,
// a link or an identifier.
func (o outboxNotices) once(subject string, want int) []outboxMail {
	o.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(o.told(subject)) < want {
		if time.Now().After(deadline) {
			o.t.Fatalf("%d notices %q, want %d", len(o.told(subject)), subject, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, p := range o.w.platforms {
		if err := p.notices.svc.Drain(context.Background()); err != nil {
			o.t.Fatal(err)
		}
	}
	got := o.told(subject)
	if len(got) != want {
		o.t.Fatalf("%d notices %q after the queues' passes, want %d", len(got), subject, want)
	}
	for _, m := range got {
		for _, never := range []string{"vendor-e2e", "E2E", "http://", "https://", "href", "con_", "fam_", "acc_", "rcp_", "mbx_", "127.0.0.1"} {
			if strings.Contains(m.Text, never) || strings.Contains(m.HTML, never) {
				o.t.Fatalf("the notice %q holds %q:\n%s", subject, never, m.Text)
			}
		}
		if !strings.Contains(m.Text, "An app Permaily hasn't verified") {
			o.t.Fatalf("the notice %q does not say the app is not verified:\n%s", subject, m.Text)
		}
	}
	return got
}

// noticesSoFar is E40's first half, before connectionsEndToEnd moves the
// clock a day on (a notice is sendable for a day from its time; one still
// waiting then is dropped, as it would be in the image): each approval put
// its notice in the outbox once (the app's through the API and the agent's
// through MCP), each device added its own, and the code exchanged twice
// its device's end.
func noticesSoFar(t *testing.T, w e2eWorld) {
	t.Helper()
	o := outboxNotices{t: t, w: w}
	approved := o.once(subjectApproved, 2)
	if through := approved[0].Text + approved[1].Text; !strings.Contains(through, "API") || !strings.Contains(through, "MCP") {
		t.Fatalf("the approvals:\n%s", through)
	}
	o.once(subjectDeviceAdded, 2)
	o.once(subjectEndedReuse, 1)
}

// noticesEndToEnd is E40's second half (ADR 0041), at the end of
// connectionsEndToEnd: a renewal of the agent's connection puts its notice
// in the outbox once, a refresh token used again after its grace its
// device's end; and with the clock moved to a week before the renewed end,
// the sweeps of both enclaves, run several times, put one notice of the
// ending. A pass of every queue after each sends nothing again; the
// enclaves counted every notice queued and sent, none dropped, skipped or
// refused.
func noticesEndToEnd(t *testing.T, w e2eWorld, c connectionsSteps) {
	t.Helper()
	o := outboxNotices{t: t, w: w}
	decode := o.decode
	o.once(subjectApproved, 2)
	o.once(subjectDeviceAdded, 2)
	o.once(subjectEndedReuse, 1)

	// The agent's connection, renewed with a fresh proof.
	var conns contracts.ConnectionList
	decode(w.call(w.bea, 0, "GET", "/api/v1/connections", ""), 200, &conns)
	var agent contracts.Connection
	for _, conn := range conns.Connections {
		if conn.Vendor.Domain == "agents.vendor-e2e.org" && conn.State == contracts.ConnectionActive {
			agent = conn
		}
	}
	if agent.ID == "" {
		t.Fatalf("no active agent's connection: %+v", conns)
	}
	var confirm struct{ Attempt string }
	decode(w.call(w.bea, 1, "POST", "/api/v1/auth/confirm/email/start", ""), 200, &confirm)
	decode(w.call(w.bea, 0, "POST", "/api/v1/auth/confirm/email/finish", `{"attempt":"`+confirm.Attempt+`","code":"`+w.outboxCode(1, "bea@example.org", "Confirm it is you on Permaily", "")+`"}`), 200, nil)
	renewal, _ := json.Marshal(contracts.ConnectionRenewal{ModelNotice: domain.ModelNoticeVersion, Version: agent.Version})
	var renewed contracts.Connection
	decode(w.call(w.bea, 1, "POST", "/api/v1/connections/"+agent.ID+"/renew", string(renewal)), 200, &renewed)
	if !renewed.EndsAt.After(agent.EndsAt) {
		t.Fatalf("renewed: %+v, before %+v", renewed, agent)
	}
	o.once(subjectRenewed, 1)

	// The agent's refresh token, used again past its grace: the device it
	// belongs to ends, and Bea is told once.
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.agent.RefreshToken}, "client_id": {e2eAgentClient}, "resource": {oauthConfig(router.HostsForDomain(w.enclaves[0].domain), w.enclaves[0].oauthPort()).MCPResource}}
	if next, a := c.token(0, refresh); next == nil {
		t.Fatalf("the agent's refresh: %+v", a)
	}
	devAhead.Add(int64(oauth.RefreshRetryGrace + time.Second))
	if _, a := c.token(1, refresh); a.code != 400 || !strings.Contains(a.body, `"invalid_grant"`) {
		t.Fatalf("the agent's refresh token again: %+v", a)
	}
	o.once(subjectEndedReuse, 2)

	// Six days before the renewed end: every sweep of both
	// enclaves finds the connection due, and one notice is sent.
	devAhead.Add(int64(renewed.EndsAt.Add(-notices.EndingBefore + 24*time.Hour).Sub(devClock())))
	if !notices.EndingDue(90, renewed.EndsAt, devClock()) {
		t.Fatalf("not due at %s for an end at %s", devClock(), renewed.EndsAt)
	}
	for range 3 {
		for _, tick := range w.sweepTicks {
			tick <- time.Now()
		}
	}
	// A tick is taken once the pass before it finished: one more each and
	// the third pass is over.
	for _, tick := range w.sweepTicks {
		tick <- time.Now()
	}
	ending := o.once(subjectEnding, 1)
	if !strings.Contains(ending[0].Text, renewed.EndsAt.UTC().Format("2 January 2006")) {
		t.Fatalf("the ending does not say the renewed end %s:\n%s", renewed.EndsAt, ending[0].Text)
	}

	// Each enclave counted what it did (/status's mail.notices in the
	// image; dev mode has no SES, so no mail section): every notice queued
	// once and sent once, none dropped, skipped or refused.
	var queued, sentCount int64
	for _, p := range w.platforms {
		n := p.notices.status()
		if n.Dropped != 0 || n.Refused != 0 || n.Skipped != 0 {
			t.Fatalf("an enclave's notices: %+v", n)
		}
		queued, sentCount = queued+n.Queued, sentCount+n.Sent
	}
	if queued != 8 || sentCount != 8 {
		t.Fatalf("the enclaves queued %d and sent %d notices, want 8 and 8", queued, sentCount)
	}
}
