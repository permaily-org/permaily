package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// imapOptions are the opt-in parts of item 6 that write to, or load, the
// test account.
type imapOptions struct {
	folderWrites bool // CREATE and DELETE a scratch folder (UIDVALIDITY on remaking, NONEXISTENT on a deleted one)
	maxSessions  int  // open up to this many sessions at once to find the limit
}

func imapDial(h imapHost) (*imapclient.Client, error) {
	c, err := imapclient.DialTLS(h.Host+":993", &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: h.Host, MinVersion: tls.VersionTLS12},
	})
	if err != nil {
		return nil, err
	}
	if err := c.WaitGreeting(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// imapStatus keeps a server answer's type and code, never its text (which
// may echo the username).
func imapStatus(err error) map[string]any {
	if err == nil {
		return map[string]any{"ok": true}
	}
	var ierr *imap.Error
	if errors.As(err, &ierr) {
		return map[string]any{"type": string(ierr.Type), "code": string(ierr.Code)}
	}
	return map[string]any{"error": "transport"}
}

// imapCheck runs item 6 against one listed host with a test account.
func imapCheck(res *results, hostID string, opts imapOptions) error {
	h, ok := lookupHost(hostID)
	if !ok {
		return fmt.Errorf("%s is not on the closed list", hostID)
	}
	cred, err := loadIMAPCredentials(h.ID)
	if err != nil {
		return err
	}
	ps, err := loadPseudonym()
	if err != nil {
		return err
	}
	st, err := res.loadState()
	if err != nil {
		return err
	}
	if st.UIDValidity == nil {
		st.UIDValidity = map[string]uint32{}
	}

	rec := map[string]any{"host": h.ID, "failure_domain": h.FailureDomain}
	start := time.Now()
	c, err := imapDial(h)
	if err != nil {
		rec["dial"] = "failed"
		return res.record("imap-host", rec)
	}
	defer c.Close()
	rec["connect_ms"] = time.Since(start).Milliseconds()
	rec["caps_before_login"] = capList(c.Caps())

	loginErr := c.Login(cred.Username, cred.AppPassword).Wait()
	rec["login"] = imapStatus(loginErr)
	if loginErr != nil {
		return res.record("imap-host", rec)
	}
	caps := c.Caps()
	rec["caps"] = capList(caps)
	rec["special_use"] = caps.Has(imap.CapSpecialUse)
	rec["condstore"] = caps.Has(imap.CapCondStore)
	rec["qresync"] = caps.Has(imap.CapQResync)

	// Folders: special-use attributes, names only for INBOX and special-use
	// folders, others as pseudonyms.
	listOpts := &imap.ListOptions{}
	if caps.Has(imap.CapSpecialUse) {
		listOpts.ReturnSpecialUse = true
	}
	boxes, err := c.List("", "*", listOpts).Collect()
	rec["list"] = imapStatus(err)
	var folders []map[string]any
	for _, b := range boxes {
		var attrs []string
		special := false
		for _, a := range b.Attrs {
			attrs = append(attrs, string(a))
			switch a {
			case imap.MailboxAttrAll, imap.MailboxAttrArchive, imap.MailboxAttrDrafts, imap.MailboxAttrFlagged, imap.MailboxAttrJunk, imap.MailboxAttrSent, imap.MailboxAttrTrash:
				special = true
			}
		}
		name := ps.of("folder", b.Mailbox)
		if special || strings.EqualFold(b.Mailbox, "INBOX") {
			name = b.Mailbox
		}
		folders = append(folders, map[string]any{"name": name, "attrs": attrs})
	}
	rec["folders"] = folders

	// EXAMINE INBOX, with CONDSTORE when offered.
	sel, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true, CondStore: caps.Has(imap.CapCondStore)}).Wait()
	rec["examine"] = imapStatus(err)
	if err == nil {
		rec["uidvalidity"] = sel.UIDValidity
		rec["uidnext"] = sel.UIDNext
		rec["messages"] = sel.NumMessages
		rec["highestmodseq"] = sel.HighestModSeq
		key := h.ID + "/INBOX"
		if prev, ok := st.UIDValidity[key]; ok {
			rec["uidvalidity_stable"] = prev == sel.UIDValidity
		}
		st.UIDValidity[key] = sel.UIDValidity

		if sel.NumMessages > 0 {
			last := imap.SeqSetNum(sel.NumMessages)
			before, _ := c.Fetch(last, &imap.FetchOptions{UID: true, Flags: true}).Collect()
			n, peekErr := peekBody(c, last)
			after, _ := c.Fetch(last, &imap.FetchOptions{UID: true, Flags: true}).Collect()
			rec["peek"] = imapStatus(peekErr)
			rec["peek_bytes"] = n
			if len(before) == 1 && len(after) == 1 {
				rec["seen_before"] = flagged(before[0].Flags, imap.FlagSeen)
				rec["seen_after"] = flagged(after[0].Flags, imap.FlagSeen)
			}
		}
		if caps.Has(imap.CapCondStore) && sel.HighestModSeq > 1 {
			var all imap.UIDSet
			all.AddRange(1, 0)
			changed, err := c.Fetch(all, &imap.FetchOptions{UID: true, Flags: true, ModSeq: true, ChangedSince: sel.HighestModSeq - 1}).Collect()
			rec["changedsince"] = imapStatus(err)
			rec["changedsince_count"] = len(changed)
		}
	}

	// A folder that does not exist.
	_, err = c.Select("permaily-spike-never-"+random43()[:8], &imap.SelectOptions{ReadOnly: true}).Wait()
	rec["examine_nonexistent"] = imapStatus(err)

	if opts.folderWrites {
		rec["scratch"] = scratchFolder(c)
	}
	if err := res.saveState(st); err != nil {
		return err
	}
	_ = c.Logout().Wait()
	if opts.maxSessions > 1 {
		rec["sessions"] = sessionLimit(h, cred, opts.maxSessions)
	}
	return res.record("imap-host", rec)
}

func peekBody(c *imapclient.Client, set imap.SeqSet) (int64, error) {
	cmd := c.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}}})
	var n int64
	for m := cmd.Next(); m != nil; m = cmd.Next() {
		for item := m.Next(); item != nil; item = m.Next() {
			if sec, ok := item.(imapclient.FetchItemDataBodySection); ok && sec.Literal != nil {
				k, err := io.Copy(io.Discard, sec.Literal)
				n += k
				if err != nil {
					return n, err
				}
			}
		}
	}
	return n, cmd.Close()
}

// scratchFolder makes, deletes and remakes a folder on the test account to
// read UIDVALIDITY on remaking and EXAMINE of a deleted folder (opt-in:
// Permaily itself never sends CREATE or DELETE).
func scratchFolder(c *imapclient.Client) map[string]any {
	const name = "permaily-spike-scratch"
	out := map[string]any{}
	examine := func() (uint32, error) {
		d, err := c.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return 0, err
		}
		_, _ = c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
		return d.UIDValidity, nil
	}
	if err := c.Create(name, nil).Wait(); err != nil {
		out["create"] = imapStatus(err)
		return out
	}
	v1, err := examine()
	out["first_examine"] = imapStatus(err)
	out["delete"] = imapStatus(c.Delete(name).Wait())
	_, err = examine()
	out["examine_deleted"] = imapStatus(err)
	out["recreate"] = imapStatus(c.Create(name, nil).Wait())
	v2, err := examine()
	out["second_examine"] = imapStatus(err)
	out["uidvalidity_greater"] = v2 > v1
	out["cleanup"] = imapStatus(c.Delete(name).Wait())
	return out
}

// sessionLimit opens sessions at once until one fails or max is reached.
func sessionLimit(h imapHost, cred imapCredentials, max int) map[string]any {
	var mu sync.Mutex
	var clients []*imapclient.Client
	defer func() {
		for _, c := range clients {
			_ = c.Logout().Wait()
			_ = c.Close()
		}
	}()
	for i := 1; i <= max; i++ {
		c, err := imapDial(h)
		if err != nil {
			return map[string]any{"opened": i - 1, "failed_at": i, "failure": "dial"}
		}
		if err := c.Login(cred.Username, cred.AppPassword).Wait(); err != nil {
			_ = c.Close()
			return map[string]any{"opened": i - 1, "failed_at": i, "failure": imapStatus(err)}
		}
		mu.Lock()
		clients = append(clients, c)
		mu.Unlock()
	}
	return map[string]any{"opened": max, "failed_at": nil}
}

func capList(caps imap.CapSet) []string {
	var out []string
	for c := range caps {
		out = append(out, string(c))
	}
	return uniq(out)
}

func flagged(flags []imap.Flag, f imap.Flag) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}
