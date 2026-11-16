package mcpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/mcpapi"
)

// The bodies of the third review of #131 and #144 (finding 1): the
// middleware must read a body as the SDK does, or refuse it. The SDK matches
// members in their case, takes the last of two, and reads one value without
// looking after it; encoding/json's Unmarshal matched them in any case and
// refused what follows, so a tool call it did not see as one reached the
// SDK unbuffered, over HTTP/2 too, and the service prepared mail.
var reviewBodies = []struct {
	name, body string
	status     int // what the middleware answers, before the SDK
}{
	{"normal", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`, http.StatusServiceUnavailable},
	{"mixed_case_method", `{"jsonrpc":"2.0","id":1,"method":"tools/call","Method":"ping","params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`, http.StatusBadRequest},
	{"duplicate_params_type", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":1,"params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`, http.StatusBadRequest},
	{"null_method_suffix", `{"jsonrpc":"2.0","id":1,"method":"tools/call","method":null,"params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`, http.StatusBadRequest},
	{"mixed_case_param", `{"jsonrpc":"2.0","id":1,"method":"tools/call","Params":1,"params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`, http.StatusBadRequest},
	{"trailing_json", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}} {}`, http.StatusBadRequest},
	{"notification", `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`, http.StatusServiceUnavailable},
	{"batch", `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}]`, http.StatusBadRequest},
	{"escaped_method", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`, http.StatusServiceUnavailable},
	{"initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}`, http.StatusOK},
}

// V50 (the third review of #131 and #144, finding 1): over HTTP/2, with the
// connection's hook, none of the review's ten bodies gets the service past
// the token's check: a tool call however it is spelled is 503 before the SDK,
// a body the SDK could read otherwise than the middleware is 400
// invalid_body, initialize still answers; nothing of the mail, and no token,
// in the answer or the log.
func TestV50CHTTP2RefusesEveryToolCallTheSDKWouldRun(t *testing.T) {
	for _, c := range reviewBodies {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld()
			var lines logs
			done := make(chan struct{})
			h := w.handler(mcpapi.WithLog(lines.logger()))
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				defer close(done)
				h.ServeHTTP(rw, r)
			}))
			srv.EnableHTTP2 = true
			srv.Config.ConnContext = mcpapi.ConnContext
			srv.StartTLS()
			t.Cleanup(srv.Close)
			resp, err := srv.Client().Do(on(srv, request{token: tokenMCP, protocol: protocolCodex, body: c.body}))
			if err != nil {
				t.Fatal(err)
			}
			data := readAll(t, resp.Body)
			_ = resp.Body.Close()
			<-done
			if resp.ProtoMajor != 2 {
				t.Fatalf("HTTP/%d", resp.ProtoMajor)
			}
			checkRefusal(t, w, &lines, c.status, resp.StatusCode, resp.Header, data)
		})
	}
}

// V50 (the same finding, over HTTP/1.1): a body the SDK could read otherwise
// than the middleware is 400 invalid_body before the SDK, so no tool runs on
// an answer the middleware did not hold for its lease; this includes a
// tool's name twice in params, and method or params of another type.
func TestV50CHTTP1ReadsTheBodyAsTheSDKDoes(t *testing.T) {
	bodies := []struct{ name, body string }{
		{"params_name_twice", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"connection_info","Name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`},
		{"method_not_a_string", `{"jsonrpc":"2.0","id":1,"method":1,"params":{"name":"read_message","arguments":{"id":"mbx_A.msg_1"}}}`},
		{"params_an_array", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":["read_message"]}`},
		{"name_not_a_string", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":1,"arguments":{"id":"mbx_A.msg_1"}}}`},
		{"not_utf8", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"read_message\",\"arguments\":{\"id\":\"\xff\"}}}"},
		{"empty", ``},
	}
	for _, c := range reviewBodies {
		if c.status == http.StatusBadRequest {
			bodies = append(bodies, struct{ name, body string }{c.name, c.body})
		}
	}
	for _, c := range bodies {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld()
			var lines logs
			srv := httptest.NewUnstartedServer(w.handler(mcpapi.WithLog(lines.logger())))
			srv.Config.ConnContext = mcpapi.ConnContext
			srv.Start()
			t.Cleanup(srv.Close)
			resp := post(t, srv.URL, request{token: tokenMCP, protocol: protocolCodex, body: c.body})
			data := readAll(t, resp.Body)
			srv.Close() // waits for the handler
			checkRefusal(t, w, &lines, http.StatusBadRequest, resp.StatusCode, resp.Header, data)
		})
	}
}

// V50 (the third review's ruling 2): a tool call is answered only on a
// connection the abort can close, ConnContext's hook set; without it, over
// HTTP/1.1 too, it is 503 no_abort once the token is checked and before the
// SDK runs. With the hook the same call answers.
func TestV50CWithoutTheConnectionsHookAToolCallIsRefused(t *testing.T) {
	for _, hook := range []bool{false, true} {
		t.Run(map[bool]string{false: "without ConnContext", true: "with ConnContext"}[hook], func(t *testing.T) {
			w := newWorld()
			var lines logs
			srv := httptest.NewUnstartedServer(w.handler(mcpapi.WithLog(lines.logger())))
			if hook {
				srv.Config.ConnContext = mcpapi.ConnContext
			}
			srv.Start()
			t.Cleanup(srv.Close)
			resp := post(t, srv.URL, toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`))
			data := readAll(t, resp.Body)
			srv.Close() // waits for the handler
			if hook {
				if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), subject) {
					t.Fatalf("%d %s", resp.StatusCode, data)
				}
				return
			}
			checkRefusal(t, w, &lines, http.StatusServiceUnavailable, resp.StatusCode, resp.Header, data)
		})
	}
}

// checkRefusal checks one answer of the middleware: its status, the service
// asked only Check, no lease made, nothing of the mail or the token in the
// answer or the log; a 503 has Retry-After and is logged no_abort, a 400 is
// invalid_body.
func checkRefusal(t *testing.T, w *world, lines *logs, want, got int, h http.Header, data []byte) {
	t.Helper()
	if got != want {
		t.Errorf("status %d, want %d: %s", got, want, data)
	}
	if calls := w.fake.Calls(); !slices.Equal(calls, []string{"Check"}) {
		t.Errorf("the service was asked %v", calls)
	}
	if n := len(w.Leases()); n != 0 {
		t.Errorf("%d leases made", n)
	}
	for _, marker := range []string{message.ID, subject, text, tokenMCP} {
		if strings.Contains(string(data), marker) || strings.Contains(lines.String(), marker) {
			t.Errorf("leaked %q", marker)
		}
	}
	switch want {
	case http.StatusServiceUnavailable:
		if h.Get("Retry-After") != "5" || !strings.Contains(lines.String(), `"outcome":"no_abort"`) {
			t.Errorf("not the refusal: %v %s", h, lines.String())
		}
	case http.StatusBadRequest:
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error.Code != "invalid_body" {
			t.Errorf("not invalid_body: %s", data)
		}
	}
}
