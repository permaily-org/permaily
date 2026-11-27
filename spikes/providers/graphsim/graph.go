package graphsim

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Well-known names the simulated mailbox has (well-known-names lists 17;
// these are the ones the ADR reads or excludes, plus the root).
var wellKnown = []string{"msgfolderroot", "inbox", "drafts", "sentitems", "deleteditems", "junkemail", "archive", "outbox", "conversationhistory"}

// localised-display-names (not documented): illustrative names only.
var displayNames = map[string]map[string]string{
	"en": {"msgfolderroot": "Top of Information Store", "inbox": "Inbox", "drafts": "Drafts", "sentitems": "Sent Items", "deleteditems": "Deleted Items", "junkemail": "Junk Email", "archive": "Archive", "outbox": "Outbox", "conversationhistory": "Conversation History"},
	"es": {"msgfolderroot": "Parte superior del almacén de información", "inbox": "Bandeja de entrada", "drafts": "Borradores", "sentitems": "Elementos enviados", "deleteditems": "Elementos eliminados", "junkemail": "Correo no deseado", "archive": "Archivo", "outbox": "Bandeja de salida", "conversationhistory": "Historial de conversaciones"},
}

type folder struct {
	id, wellKnown, name, parent string
	hidden                      bool
}

type message struct {
	n        int
	gen      int // regular id generation; changes on every move (regular-ids-change)
	folder   string
	received time.Time
	read     bool
	mime     []byte
	purged   bool
}

func (m *message) immutableID() string { return fmt.Sprintf("imm-%d", m.n) }
func (m *message) regularID() string   { return fmt.Sprintf("reg-%d-%d", m.n, m.gen) }

type event struct {
	seq    int
	folder string
	msg    int
}

type deltaState struct {
	folder  string
	seq     int
	selects []string
	expired bool
	reset   bool
}

type pageItem struct {
	msg     int
	removed bool
}

type skipState struct {
	folder  string
	items   []pageItem
	endSeq  int
	selects []string
}

type mailbox struct {
	folders   map[string]*folder
	byWK      map[string]string
	messages  map[int]*message
	events    []event
	seq       int
	nextID    int
	tokens    map[string]*deltaState
	skips     map[string]*skipState
	overQuota bool
}

func newMailbox(locale string) *mailbox {
	b := &mailbox{
		folders:  map[string]*folder{},
		byWK:     map[string]string{},
		messages: map[int]*message{},
		tokens:   map[string]*deltaState{},
		skips:    map[string]*skipState{},
	}
	names := displayNames[locale]
	if names == nil {
		names = displayNames["en"]
	}
	for _, wk := range wellKnown {
		id := "fold-" + wk
		parent := "fold-msgfolderroot"
		if wk == "msgfolderroot" {
			parent = ""
		}
		b.folders[id] = &folder{id: id, wellKnown: wk, name: names[wk], parent: parent}
		b.byWK[wk] = id
	}
	return b
}

func (b *mailbox) record(folder string, msg int) {
	b.seq++
	b.events = append(b.events, event{seq: b.seq, folder: folder, msg: msg})
}

func (b *mailbox) resolveFolder(idOrName string) (*folder, bool) {
	if id, ok := b.byWK[strings.ToLower(idOrName)]; ok {
		return b.folders[id], true
	}
	f, ok := b.folders[idOrName]
	return f, ok
}

// --- What the person does in Outlook (test helpers, never HTTP: Permaily is
// read-only and never sends these). ---

// Deliver puts a message into a folder (id or well-known name) and returns
// its immutable id.
func (s *Sim) Deliver(a *Account, into string, received time.Time, mime []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := a.box
	f, ok := b.resolveFolder(into)
	if !ok {
		panic("graphsim: no folder " + into)
	}
	b.nextID++
	m := &message{n: b.nextID, folder: f.id, received: received, mime: mime}
	b.messages[m.n] = m
	b.record(f.id, m.n)
	return m.immutableID()
}

func (s *Sim) msgByImm(a *Account, imm string) *message {
	n, err := strconv.Atoi(strings.TrimPrefix(imm, "imm-"))
	if err != nil {
		panic("graphsim: bad id " + imm)
	}
	return a.box.messages[n]
}

// MoveMessage moves a message to another folder; its regular id changes, its
// immutable id does not.
func (s *Sim) MoveMessage(a *Account, imm, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := a.box
	m := s.msgByImm(a, imm)
	f, ok := b.resolveFolder(to)
	if !ok {
		panic("graphsim: no folder " + to)
	}
	from := m.folder
	m.folder = f.id
	m.gen++
	b.record(from, m.n)
	b.record(f.id, m.n)
}

// DeleteMessage is Outlook's delete: the message moves to deleted items.
func (s *Sim) DeleteMessage(a *Account, imm string) { s.MoveMessage(a, imm, "deleteditems") }

// PurgeMessage deletes a message for good.
func (s *Sim) PurgeMessage(a *Account, imm string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.msgByImm(a, imm)
	m.purged = true
	a.box.record(m.folder, m.n)
}

// MarkRead changes a message's read state (a change delta reports).
func (s *Sim) MarkRead(a *Account, imm string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.msgByImm(a, imm)
	m.read = true
	a.box.record(m.folder, m.n)
}

// IsRead reports a message's read state.
func (s *Sim) IsRead(a *Account, imm string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.msgByImm(a, imm).read
}

// CreateFolder makes a folder under parent (id or well-known name).
func (s *Sim) CreateFolder(a *Account, parent, name string, hidden bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := a.box
	p, ok := b.resolveFolder(parent)
	if !ok {
		panic("graphsim: no folder " + parent)
	}
	b.nextID++
	id := fmt.Sprintf("fold-%d", b.nextID)
	b.folders[id] = &folder{id: id, name: name, parent: p.id, hidden: hidden}
	return id
}

// ErrWellKnown is returned when the person tries to move or delete a
// well-known folder. The documentation lists no such exception
// (well-known-immovable, not documented); the simulator follows the ADR's
// assumption.
var ErrWellKnown = errors.New("graphsim: well-known folders cannot be moved or deleted (assumed)")

// MoveFolder reparents a folder; folder ids stay (folder-ids-constant).
func (s *Sim) MoveFolder(a *Account, id, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := a.box
	f := b.folders[id]
	if f.wellKnown != "" {
		return ErrWellKnown
	}
	dest, ok := b.resolveFolder(to)
	if !ok {
		return fmt.Errorf("graphsim: no folder %s", to)
	}
	f.parent = dest.id
	return nil
}

// DeleteFolder is the person deleting a folder in Outlook. Assumed (not
// documented, folder-delete-destination): it moves under deleted items and
// its messages keep their parentFolderId.
func (s *Sim) DeleteFolder(a *Account, id string) error {
	return s.MoveFolder(a, id, "deleteditems")
}

// EmptyDeletedItems purges every message under deleted items and removes its
// subfolders.
func (s *Sim) EmptyDeletedItems(a *Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := a.box
	root := b.byWK["deleteditems"]
	under := map[string]bool{root: true}
	for changed := true; changed; {
		changed = false
		for id, f := range b.folders {
			if !under[id] && under[f.parent] {
				under[id] = true
				changed = true
			}
		}
	}
	for _, m := range b.messages {
		if !m.purged && under[m.folder] {
			m.purged = true
			b.record(m.folder, m.n)
		}
	}
	for id := range under {
		if id != root {
			delete(b.folders, id)
		}
	}
}

// ExpireDeltaTokens makes every delta token of the mailbox unknown
// (delta-token-expired).
func (s *Sim) ExpireDeltaTokens(a *Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range a.box.tokens {
		t.expired = true
	}
}

// ResetSync makes every delta token answer 410 Gone (delta-reset-410).
func (s *Sim) ResetSync(a *Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range a.box.tokens {
		t.reset = true
	}
}

// OverQuota makes folder delta answer 403 ErrorQuotaExceeded.
func (s *Sim) OverQuota(a *Account, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.box.overQuota = on
}

// Throttle makes the next n Graph requests answer status (429 or 503) with
// Retry-After in seconds.
func (s *Sim) Throttle(n, status int, retryAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.throttle = append(s.throttle, throttleRule{left: n, status: status, retryAfter: retryAfter})
}

type throttleRule struct {
	left       int
	status     int
	retryAfter time.Duration
}

// --- HTTP ---

func (s *Sim) graph(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	g := s.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if g == nil || !s.now.Before(g.expires) {
		s.mu.Unlock()
		graphError(w, http.StatusUnauthorized, "InvalidAuthenticationToken", "Access token is empty or invalid.")
		return
	}
	a := g.account
	if !has(g.scopes, "Mail.Read") && !has(g.scopes, "https://graph.microsoft.com/Mail.Read") {
		s.mu.Unlock()
		graphError(w, http.StatusForbidden, "ErrorAccessDenied", "Access is denied. Check credentials and try again.")
		return
	}
	if a.RequireClaims {
		s.mu.Unlock()
		// graph-403-ca.
		claims := base64.StdEncoding.EncodeToString([]byte(`{"access_token":{}}`))
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_claims", claims="`+claims+`"`)
		graphError(w, http.StatusForbidden, "Forbidden", "Conditional access challenge.")
		return
	}
	if len(s.throttle) > 0 {
		t := &s.throttle[0]
		t.left--
		status, after := t.status, t.retryAfter
		if t.left <= 0 {
			s.throttle = s.throttle[1:]
		}
		s.mu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(int(after.Seconds())))
		code := "TooManyRequests"
		if status == http.StatusServiceUnavailable {
			code = "serviceNotAvailable"
		}
		graphError(w, status, code, "Please retry again later.")
		return
	}
	// outlook-limits: 10,000 a mailbox in ten minutes, four at once.
	if s.now.Sub(s.windowStart[a.OID]) >= 10*time.Minute {
		s.windowStart[a.OID] = s.now
		s.windowCount[a.OID] = 0
	}
	s.windowCount[a.OID]++
	if s.windowCount[a.OID] > 10000 {
		left := 10*time.Minute - s.now.Sub(s.windowStart[a.OID])
		s.mu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(int(left.Seconds())+1))
		graphError(w, http.StatusTooManyRequests, "TooManyRequests", "Please retry again later.")
		return
	}
	if s.inFlight[a.OID] >= 4 {
		s.mu.Unlock()
		// concurrency-429-form (not documented).
		w.Header().Set("Retry-After", "1")
		graphError(w, http.StatusTooManyRequests, "ApplicationThrottled", "Application is over its MailboxConcurrency limit.")
		return
	}
	s.inFlight[a.OID]++
	hold := s.Hold
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight[a.OID]--
		s.mu.Unlock()
	}()
	if hold != nil {
		hold()
	}

	immutable := preferImmutable(r)
	path := strings.TrimPrefix(r.URL.Path, "/v1.0")
	parts := splitPath(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	b := a.box
	switch {
	case len(parts) == 2 && parts[0] == "me" && parts[1] == "mailFolders":
		s.listFolders(w, r, b, b.byWK["msgfolderroot"], "/me/mailFolders")
	case len(parts) == 3 && parts[0] == "me" && parts[1] == "mailFolders" && parts[2] == "delta":
		if b.overQuota {
			graphError(w, http.StatusForbidden, "ErrorQuotaExceeded", "The mailbox is over its quota.")
			return
		}
		s.listFolders(w, r, b, "", "/me/mailFolders/delta")
	case len(parts) == 3 && parts[0] == "me" && parts[1] == "mailFolders":
		f, ok := b.resolveFolder(parts[2])
		if !ok {
			graphError(w, http.StatusNotFound, "ErrorItemNotFound", "The specified object was not found in the store.")
			return
		}
		writeJSON(w, http.StatusOK, folderJSON(b, f))
	case len(parts) == 4 && parts[0] == "me" && parts[1] == "mailFolders" && parts[3] == "childFolders":
		f, ok := b.resolveFolder(parts[2])
		if !ok {
			graphError(w, http.StatusNotFound, "ErrorItemNotFound", "The specified object was not found in the store.")
			return
		}
		s.listFolders(w, r, b, f.id, "/me/mailFolders/"+f.id+"/childFolders")
	case len(parts) == 5 && parts[0] == "me" && parts[1] == "mailFolders" && parts[3] == "messages" && parts[4] == "delta":
		f, ok := b.resolveFolder(parts[2])
		if !ok {
			graphError(w, http.StatusNotFound, "ErrorItemNotFound", "The specified object was not found in the store.")
			return
		}
		s.delta(w, r, b, f, immutable)
	case len(parts) == 3 && parts[0] == "me" && parts[1] == "messages":
		m, ok := lookupMessage(w, b, parts[2])
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, messageJSON(m, selects(r.URL.Query().Get("$select")), immutable))
	case len(parts) == 4 && parts[0] == "me" && parts[1] == "messages" && parts[3] == "$value":
		m, ok := lookupMessage(w, b, parts[2])
		if !ok {
			return
		}
		// mime-does-not-mark-read (not documented): read state untouched.
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(m.mime)))
		_, _ = w.Write(m.mime)
	default:
		graphError(w, http.StatusBadRequest, "BadRequest", "Resource not found for the segment.")
	}
}

// splitPath splits a Graph path, unfolding the key form the documented links
// use (`mailfolders('{id}')`) and normalising segment case.
func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(strings.Trim(path, "/"), "/") {
		if i := strings.Index(p, "('"); i > 0 && strings.HasSuffix(p, "')") {
			parts = append(parts, p[:i], p[i+2:len(p)-2])
			continue
		}
		parts = append(parts, p)
	}
	for i, p := range parts {
		for _, name := range []string{"mailFolders", "childFolders", "messages", "delta"} {
			if strings.EqualFold(p, name) {
				parts[i] = name
			}
		}
	}
	return parts
}

func preferImmutable(r *http.Request) bool {
	for _, v := range r.Header.Values("Prefer") {
		for _, p := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), `IdType="ImmutableId"`) {
				return true
			}
		}
	}
	return false
}

func maxPageSize(r *http.Request, def int) int {
	for _, v := range r.Header.Values("Prefer") {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if rest, ok := strings.CutPrefix(p, "odata.maxpagesize="); ok {
				if n, err := strconv.Atoi(rest); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return def
}

func lookupMessage(w http.ResponseWriter, b *mailbox, id string) (*message, bool) {
	var n, gen int
	switch {
	case strings.HasPrefix(id, "imm-"):
		v, err := strconv.Atoi(strings.TrimPrefix(id, "imm-"))
		if err != nil {
			break
		}
		n, gen = v, -1
	case strings.HasPrefix(id, "reg-"):
		f := strings.Split(strings.TrimPrefix(id, "reg-"), "-")
		if len(f) != 2 {
			break
		}
		v, err1 := strconv.Atoi(f[0])
		g, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			break
		}
		n, gen = v, g
	}
	if n == 0 {
		// malformed-id (not documented).
		graphError(w, http.StatusBadRequest, "ErrorInvalidIdMalformed", "Id is malformed.")
		return nil, false
	}
	m := b.messages[n]
	if m == nil || m.purged || (gen >= 0 && gen != m.gen) {
		// item-not-found (not documented): deleted for good, or an old
		// regular id after a move.
		graphError(w, http.StatusNotFound, "ErrorItemNotFound", "The specified object was not found in the store.")
		return nil, false
	}
	return m, true
}

func selects(q string) []string {
	if q == "" {
		return nil
	}
	return strings.Split(q, ",")
}

func messageJSON(m *message, sel []string, immutable bool) map[string]any {
	id := m.regularID()
	if immutable {
		id = m.immutableID()
	}
	out := map[string]any{
		"@odata.type": "#microsoft.graph.message",
		"@odata.etag": fmt.Sprintf(`W/"sim-%d-%d"`, m.n, m.gen),
		"id":          id,
	}
	all := len(sel) == 0
	want := func(p string) bool {
		if all {
			return true
		}
		for _, s := range sel {
			if strings.EqualFold(strings.TrimSpace(s), p) {
				return true
			}
		}
		return false
	}
	if want("receivedDateTime") {
		out["receivedDateTime"] = m.received.UTC().Format(time.RFC3339)
	}
	if want("parentFolderId") {
		out["parentFolderId"] = m.folder
	}
	if want("isRead") {
		out["isRead"] = m.read
	}
	return out
}

func folderJSON(b *mailbox, f *folder) map[string]any {
	children, total, unread := 0, 0, 0
	for _, c := range b.folders {
		if c.parent == f.id {
			children++
		}
	}
	for _, m := range b.messages {
		if !m.purged && m.folder == f.id {
			total++
			if !m.read {
				unread++
			}
		}
	}
	return map[string]any{
		"id":               f.id,
		"displayName":      f.name,
		"parentFolderId":   f.parent,
		"childFolderCount": children,
		"totalItemCount":   total,
		"unreadItemCount":  unread,
		"isHidden":         f.hidden,
	}
}

// listFolders lists the children of parent (or, for "", every folder, as
// folder delta's first round does), paged with $top and $skip.
func (s *Sim) listFolders(w http.ResponseWriter, r *http.Request, b *mailbox, parent, path string) {
	q := r.URL.Query()
	hidden := strings.EqualFold(q.Get("includeHiddenFolders"), "true")
	var list []*folder
	for _, f := range b.folders {
		if parent == "" && f.wellKnown == "msgfolderroot" {
			continue
		}
		if (parent == "" || f.parent == parent) && (hidden || !f.hidden) {
			list = append(list, f)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	top := 10
	if v, err := strconv.Atoi(q.Get("$top")); err == nil && v > 0 {
		top = v
	}
	skip, _ := strconv.Atoi(q.Get("$skip"))
	end := min(skip+top, len(list))
	value := []map[string]any{}
	for _, f := range list[min(skip, len(list)):end] {
		value = append(value, folderJSON(b, f))
	}
	resp := map[string]any{"value": value}
	base := "https://" + GraphHost + "/v1.0" + path
	if end < len(list) {
		next := url.Values{"$top": {strconv.Itoa(top)}, "$skip": {strconv.Itoa(end)}}
		if hidden {
			next.Set("includeHiddenFolders", "true")
		}
		resp["@odata.nextLink"] = base + "?" + next.Encode()
	} else if parent == "" {
		resp["@odata.deltaLink"] = base + "?$deltatoken=" + fmt.Sprintf("folders-%d", b.seq)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Sim) delta(w http.ResponseWriter, r *http.Request, b *mailbox, f *folder, immutable bool) {
	q := r.URL.Query()
	page := maxPageSize(r, 10)
	base := "https://" + GraphHost + "/v1.0/me/mailFolders('" + f.id + "')/messages/delta"
	var st *skipState
	switch {
	case q.Has("$skiptoken"):
		st = b.skips[q.Get("$skiptoken")]
		if st == nil || st.folder != f.id {
			graphError(w, http.StatusBadRequest, "syncStateNotFound", "The sync state generation is not found.")
			return
		}
		delete(b.skips, q.Get("$skiptoken"))
	case q.Has("$deltatoken") && q.Get("$deltatoken") != "":
		t := b.tokens[q.Get("$deltatoken")]
		switch {
		case t == nil || t.folder != f.id || t.expired:
			// delta-token-expired: a 40X, exact status not documented.
			graphError(w, http.StatusBadRequest, "syncStateNotFound", "The sync state generation is not found.")
			return
		case t.reset:
			// delta-reset-410 (documented), its code (not documented).
			w.Header().Set("Location", "https://"+GraphHost+"/v1.0/me/mailFolders/"+f.id+"/messages/delta?$deltatoken=")
			graphError(w, http.StatusGone, "resyncRequired", "Resync required.")
			return
		}
		seen := map[int]bool{}
		st = &skipState{folder: f.id, endSeq: b.seq, selects: t.selects}
		for _, e := range b.events {
			if e.seq <= t.seq || e.folder != f.id || seen[e.msg] {
				continue
			}
			seen[e.msg] = true
			m := b.messages[e.msg]
			st.items = append(st.items, pageItem{msg: e.msg, removed: m.purged || m.folder != f.id})
		}
	default:
		// A first round: every message in the folder now.
		st = &skipState{folder: f.id, endSeq: b.seq, selects: selects(q.Get("$select"))}
		var since time.Time
		filtered := false
		if fl := q.Get("$filter"); fl != "" {
			v := strings.Fields(fl)
			if len(v) != 3 || v[0] != "receivedDateTime" || (v[1] != "ge" && v[1] != "gt") {
				graphError(w, http.StatusBadRequest, "ErrorInvalidUrlQueryFilter", "The filter is not supported for delta.")
				return
			}
			t, err := time.Parse(time.RFC3339, v[2])
			if err != nil {
				graphError(w, http.StatusBadRequest, "ErrorInvalidUrlQueryFilter", "The filter is not supported for delta.")
				return
			}
			since, filtered = t, true
		}
		var ms []*message
		for _, m := range b.messages {
			if !m.purged && m.folder == f.id && (!filtered || !m.received.Before(since)) {
				ms = append(ms, m)
			}
		}
		sort.Slice(ms, func(i, j int) bool { return ms[i].received.After(ms[j].received) })
		if filtered && len(ms) > 5000 {
			// delta-filter-5000.
			ms = ms[:5000]
		}
		for _, m := range ms {
			st.items = append(st.items, pageItem{msg: m.n})
		}
	}
	n := min(page, len(st.items))
	value := []map[string]any{}
	for _, it := range st.items[:n] {
		m := b.messages[it.msg]
		if it.removed {
			id := m.regularID()
			if immutable {
				id = m.immutableID()
			}
			value = append(value, map[string]any{"@odata.type": "#microsoft.graph.message", "id": id, "@removed": map[string]string{"reason": "deleted"}})
			continue
		}
		value = append(value, messageJSON(m, st.selects, immutable))
	}
	resp := map[string]any{"@odata.context": "https://" + GraphHost + "/v1.0/$metadata#Collection(message)", "value": value}
	s.seq++
	if n < len(st.items) {
		tok := fmt.Sprintf("skip%d", s.seq)
		b.skips[tok] = &skipState{folder: st.folder, items: st.items[n:], endSeq: st.endSeq, selects: st.selects}
		resp["@odata.nextLink"] = base + "?$skiptoken=" + tok
	} else {
		tok := fmt.Sprintf("delta%d", s.seq)
		b.tokens[tok] = &deltaState{folder: st.folder, seq: st.endSeq, selects: st.selects}
		resp["@odata.deltaLink"] = base + "?$deltatoken=" + tok
	}
	writeJSON(w, http.StatusOK, resp)
}

// graphError writes Graph's documented error body (error-shape).
func graphError(w http.ResponseWriter, status int, code, msg string) {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code":    code,
		"message": msg,
		"innerError": map[string]string{
			"date":       "2026-11-27T12:00:00",
			"request-id": "00000000-0000-0000-0000-000000000000",
		},
	}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
