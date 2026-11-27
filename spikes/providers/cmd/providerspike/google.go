package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	gmailBase      = "https://gmail.googleapis.com/gmail/v1/users/me"
	// The redirect URI ADR 0042 registers for local runs.
	loopbackRedirect = "http://localhost:8443/api/v1/mailboxes/connect/callback"
	gmailScopes      = "openid email https://www.googleapis.com/auth/gmail.readonly"
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

func random43() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// googleTrip runs item 1's consent trip in testing mode: it prints the
// authorisation URL for the owner to open, waits for the loopback callback,
// redeems the code and records the response's shape and its `scope`.
func googleTrip(ctx context.Context, res *results, label string) error {
	client, err := loadGoogleClient()
	if err != nil {
		return err
	}
	ps, err := loadPseudonym()
	if err != nil {
		return err
	}
	state, verifier, nonce := random43(), random43(), random43()
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":          {"code"},
		"client_id":              {client.ClientID},
		"redirect_uri":           {loopbackRedirect},
		"scope":                  {gmailScopes},
		"access_type":            {"offline"},
		"prompt":                 {"consent"},
		"include_granted_scopes": {"false"},
		"state":                  {state},
		"nonce":                  {nonce},
		"code_challenge":         {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method":  {"S256"},
	}

	type callback struct{ code, errParam string }
	got := make(chan callback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/mailboxes/connect/callback", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		if v.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "The spike has the answer; you can close this tab.\n")
		select {
		case got <- callback{code: v.Get("code"), errParam: v.Get("error")}:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	for _, addr := range []string{"127.0.0.1:8443", "[::1]:8443"} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			if addr == "127.0.0.1:8443" {
				return err
			}
			continue
		}
		go func() { _ = srv.Serve(ln) }()
	}
	defer srv.Close()

	fmt.Println("Open this address in a browser signed in to a synthetic test account (label: " + label + "):")
	fmt.Println(googleAuthURL + "?" + q.Encode())
	var cb callback
	select {
	case cb = <-got:
	case <-time.After(10 * time.Minute):
		return errors.New("no callback within ten minutes")
	case <-ctx.Done():
		return ctx.Err()
	}
	if cb.errParam != "" {
		return res.record("google-trip", map[string]any{"label": label, "callback_error": cb.errParam})
	}

	start := time.Now()
	body, status, err := postForm(ctx, googleTokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {cb.code},
		"redirect_uri":  {loopbackRedirect},
		"client_id":     {client.ClientID},
		"client_secret": {client.ClientSecret},
		"code_verifier": {verifier},
	})
	if err != nil {
		return err
	}
	var tok map[string]any
	if err := json.Unmarshal(body, &tok); err != nil {
		return fmt.Errorf("token response is not JSON (status %d)", status)
	}
	rec := map[string]any{
		"label":         label,
		"status":        status,
		"fields":        keysOf(tok),
		"latency_ms":    time.Since(start).Milliseconds(),
		"refresh_token": tok["refresh_token"] != nil,
		"id_token":      tok["id_token"] != nil,
		"expires_in":    tok["expires_in"],
		"token_type":    tok["token_type"],
		"error":         tok["error"],
	}
	if sc, ok := tok["scope"].(string); ok {
		rec["scope"] = sc
	} else {
		rec["scope_omitted"] = true
	}
	if idt, ok := tok["id_token"].(string); ok {
		c, err := jwtClaims(idt)
		if err == nil {
			rec["id_token_claims"] = keysOf(c)
			rec["id_token_nonce_matches"] = c["nonce"] == nonce
			if sub, ok := c["sub"].(string); ok {
				rec["sub"] = ps.of("sub", sub)
			}
			rec["email_verified"] = c["email_verified"]
		}
	}
	if err := res.record("google-trip", rec); err != nil {
		return err
	}
	rt, _ := tok["refresh_token"].(string)
	at, _ := tok["access_token"].(string)
	exp, _ := tok["expires_in"].(float64)
	if rt == "" && at == "" {
		return nil
	}
	return saveGoogleGrant(googleGrant{RefreshToken: rt, AccessToken: at, AccessExpiry: time.Now().Add(time.Duration(exp) * time.Second).Unix(), GrantedAt: time.Now().Unix()})
}

// googleRefresh records a refresh: whether `scope` comes back, whether a new
// refresh token does, and, after day 7 in testing mode, invalid_grant.
func googleRefresh(ctx context.Context, res *results) error {
	client, err := loadGoogleClient()
	if err != nil {
		return err
	}
	g, err := loadGoogleGrant()
	if err != nil {
		return err
	}
	body, status, err := postForm(ctx, googleTokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {g.RefreshToken},
		"client_id":     {client.ClientID},
		"client_secret": {client.ClientSecret},
	})
	if err != nil {
		return err
	}
	var tok map[string]any
	if err := json.Unmarshal(body, &tok); err != nil {
		return fmt.Errorf("token response is not JSON (status %d)", status)
	}
	rec := map[string]any{
		"status":            status,
		"fields":            keysOf(tok),
		"days_since_grant":  time.Since(time.Unix(g.GrantedAt, 0)).Hours() / 24,
		"new_refresh_token": tok["refresh_token"] != nil,
		"error":             tok["error"],
	}
	if sc, ok := tok["scope"].(string); ok {
		rec["scope"] = sc
	} else {
		rec["scope_omitted"] = true
	}
	if err := res.record("google-refresh", rec); err != nil {
		return err
	}
	if at, ok := tok["access_token"].(string); ok {
		exp, _ := tok["expires_in"].(float64)
		g.AccessToken, g.AccessExpiry = at, time.Now().Add(time.Duration(exp)*time.Second).Unix()
		if rt, ok := tok["refresh_token"].(string); ok {
			g.RefreshToken = rt
		}
		return saveGoogleGrant(g)
	}
	return nil
}

func accessToken(ctx context.Context, res *results) (string, error) {
	g, err := loadGoogleGrant()
	if err != nil {
		return "", err
	}
	if time.Now().Unix() < g.AccessExpiry-60 {
		return g.AccessToken, nil
	}
	if err := googleRefresh(ctx, res); err != nil {
		return "", err
	}
	g, err = loadGoogleGrant()
	return g.AccessToken, err
}

// gmailGet calls the Gmail API and returns the response for the caller to
// read and close.
func gmailGet(ctx context.Context, res *results, method, path string, q url.Values) (*http.Response, error) {
	at, err := accessToken(ctx, res)
	if err != nil {
		return nil, err
	}
	u := gmailBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+at)
	req.Header.Set("Accept-Encoding", "identity")
	res.spend(method)
	return httpClient.Do(req)
}

func gmailJSON(ctx context.Context, res *results, method, path string, q url.Values) (map[string]any, int, error) {
	resp, err := gmailGet(ctx, res, method, path, q)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&v); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%s: answer is not JSON (status %d)", method, resp.StatusCode)
	}
	return v, resp.StatusCode, nil
}

// gmailError keeps only an error's code and reason, never its message.
func gmailError(v map[string]any) map[string]any {
	e, ok := v["error"].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{"code": e["code"], "status": e["status"]}
	if errs, ok := e["errors"].([]any); ok && len(errs) > 0 {
		if first, ok := errs[0].(map[string]any); ok {
			out["reason"] = first["reason"]
		}
	}
	return out
}

// gmailBaseline reads getProfile's historyId and keeps it as the baseline
// for the later history readings (item 2).
func gmailBaseline(ctx context.Context, res *results) error {
	v, status, err := gmailJSON(ctx, res, "getProfile", "/profile", nil)
	if err != nil {
		return err
	}
	rec := map[string]any{"status": status, "fields": keysOf(v), "history_id": v["historyId"], "messages_total": v["messagesTotal"], "error": gmailError(v)}
	if err := res.record("gmail-baseline", rec); err != nil {
		return err
	}
	if h, ok := v["historyId"].(string); ok {
		st, err := res.loadState()
		if err != nil {
			return err
		}
		st.GmailBaseline, st.GmailBaselineAt = h, time.Now().Unix()
		return res.saveState(st)
	}
	return nil
}

// gmailHistory lists history from start (or the baseline) and records each
// record's shape; with twice it reads the same start again and compares.
func gmailHistory(ctx context.Context, res *results, start string, twice bool) error {
	if start == "" {
		st, err := res.loadState()
		if err != nil {
			return err
		}
		if st.GmailBaseline == "" {
			return errors.New("no baseline: run gmail-baseline first")
		}
		start = st.GmailBaseline
	}
	ps, err := loadPseudonym()
	if err != nil {
		return err
	}
	read := func() (map[string]bool, error) {
		seen := map[string]bool{}
		page := ""
		for pages := 0; pages < 100; pages++ {
			q := url.Values{"startHistoryId": {start}, "maxResults": {"500"}}
			if page != "" {
				q.Set("pageToken", page)
			}
			v, status, err := gmailJSON(ctx, res, "history.list", "/history", q)
			if err != nil {
				return nil, err
			}
			rec := map[string]any{"start": start, "page": pages, "status": status, "fields": keysOf(v), "history_id": v["historyId"], "error": gmailError(v)}
			var shapes []map[string]any
			hist, _ := v["history"].([]any)
			for _, h := range hist {
				hm, _ := h.(map[string]any)
				shape := map[string]any{"id": hm["id"]}
				for _, kind := range []string{"messages", "messagesAdded", "messagesDeleted", "labelsAdded", "labelsRemoved"} {
					entries, _ := hm[kind].([]any)
					if len(entries) == 0 {
						continue
					}
					var labels []string
					for _, e := range entries {
						em, _ := e.(map[string]any)
						msg, _ := em["message"].(map[string]any)
						if msg == nil {
							msg = em
						}
						if id, ok := msg["id"].(string); ok {
							seen[ps.of("msg", id)] = true
						}
						for _, key := range []string{"labelIds"} {
							if ls, ok := msg[key].([]any); ok {
								for _, l := range ls {
									labels = append(labels, fmt.Sprint(l))
								}
							}
						}
						if ls, ok := em["labelIds"].([]any); ok && kind != "messages" {
							for _, l := range ls {
								labels = append(labels, "changed:"+fmt.Sprint(l))
							}
						}
					}
					shape[kind] = map[string]any{"count": len(entries), "labels": uniq(labels)}
				}
				shapes = append(shapes, shape)
			}
			rec["records"] = shapes
			if err := res.record("gmail-history", rec); err != nil {
				return nil, err
			}
			page, _ = v["nextPageToken"].(string)
			if status != http.StatusOK || page == "" {
				return seen, nil
			}
		}
		return seen, nil
	}
	first, err := read()
	if err != nil || !twice {
		return err
	}
	second, err := read()
	if err != nil {
		return err
	}
	same := len(first) == len(second)
	for k := range first {
		if !second[k] {
			same = false
		}
	}
	return res.record("gmail-history-twice", map[string]any{"start": start, "first": len(first), "second": len(second), "same_messages": same})
}

// gmailHistoryAge reads history from the stored baseline and records the
// days since it was taken: the 1, 7 and 14-day readings of item 2.
func gmailHistoryAge(ctx context.Context, res *results) error {
	st, err := res.loadState()
	if err != nil {
		return err
	}
	if st.GmailBaseline == "" {
		return errors.New("no baseline: run gmail-baseline first")
	}
	v, status, err := gmailJSON(ctx, res, "history.list", "/history", url.Values{"startHistoryId": {st.GmailBaseline}, "maxResults": {"1"}})
	if err != nil {
		return err
	}
	return res.record("gmail-history-age", map[string]any{
		"days":   time.Since(time.Unix(st.GmailBaselineAt, 0)).Hours() / 24,
		"status": status,
		"valid":  status == http.StatusOK,
		"error":  gmailError(v),
	})
}

// gmailMessage runs item 3 on one message: metadata's fields, its historyId
// against the baseline, raw streamed through the decoder, and UNREAD before
// and after.
func gmailMessage(ctx context.Context, res *results, id string) error {
	ps, err := loadPseudonym()
	if err != nil {
		return err
	}
	st, err := res.loadState()
	if err != nil {
		return err
	}
	meta := func() (map[string]any, error) {
		v, _, err := gmailJSON(ctx, res, "messages.get", "/messages/"+url.PathEscape(id), url.Values{"format": {"metadata"}})
		return v, err
	}
	before, err := meta()
	if err != nil {
		return err
	}
	resp, err := gmailGet(ctx, res, "messages.get raw", "/messages/"+url.PathEscape(id), url.Values{"format": {"raw"}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	started := time.Now()
	others, decoded, rawErr := streamRaw(io.LimitReader(resp.Body, 34<<20+1<<20), io.Discard, 1<<20)
	after, err := meta()
	if err != nil {
		return err
	}
	rec := map[string]any{
		"message":           ps.of("msg", id),
		"metadata_fields":   keysOf(before),
		"raw_status":        resp.StatusCode,
		"raw_decoded_bytes": decoded,
		"raw_ms":            time.Since(started).Milliseconds(),
		"unread_before":     hasLabel(before, "UNREAD"),
		"unread_after":      hasLabel(after, "UNREAD"),
		"size_estimate":     before["sizeEstimate"],
		"history_id":        before["historyId"],
		"baseline":          st.GmailBaseline,
	}
	if rawErr != nil {
		rec["raw_error"] = rawErr.Error()
	}
	var rawFields []string
	for k := range others {
		rawFields = append(rawFields, k)
	}
	sort.Strings(rawFields)
	rec["raw_fields"] = rawFields
	if h, ok := before["historyId"].(string); ok && st.GmailBaseline != "" {
		hv, _ := strconv.ParseUint(h, 10, 64)
		bv, _ := strconv.ParseUint(st.GmailBaseline, 10, 64)
		rec["history_id_above_baseline"] = hv > bv
	}
	return res.record("gmail-message", rec)
}

// gmailFirst returns the id of the first message matching q.
func gmailFirst(ctx context.Context, res *results, q string) (string, error) {
	v, _, err := gmailJSON(ctx, res, "messages.list", "/messages", url.Values{"q": {q}, "maxResults": {"1"}})
	if err != nil {
		return "", err
	}
	msgs, _ := v["messages"].([]any)
	if len(msgs) == 0 {
		return "", errors.New("no message matches the query")
	}
	m, _ := msgs[0].(map[string]any)
	id, _ := m["id"].(string)
	return id, nil
}

// gmailList runs messages.list with q=after: for the backfill's query.
func gmailList(ctx context.Context, res *results, days int) error {
	after := time.Now().AddDate(0, 0, -days).Unix()
	v, status, err := gmailJSON(ctx, res, "messages.list", "/messages", url.Values{"q": {fmt.Sprintf("after:%d -in:spam -in:trash", after)}, "maxResults": {"500"}})
	if err != nil {
		return err
	}
	msgs, _ := v["messages"].([]any)
	return res.record("gmail-list", map[string]any{"days": days, "status": status, "fields": keysOf(v), "first_page": len(msgs), "next_page": v["nextPageToken"] != nil, "result_size_estimate": v["resultSizeEstimate"], "error": gmailError(v)})
}

func postForm(ctx context.Context, u string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	return b, resp.StatusCode, err
}

func jwtClaims(tok string) (map[string]any, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var c map[string]any
	err = json.Unmarshal(b, &c)
	return c, err
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasLabel(v map[string]any, label string) bool {
	ls, _ := v["labelIds"].([]any)
	for _, l := range ls {
		if l == label {
			return true
		}
	}
	return false
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
