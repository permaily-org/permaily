package httpapi

import (
	"context"
	"log/slog"
	"net/http"
)

// A request of the API for apps or of the issuer's endpoints logs one line
// when it ends, a transfer cut or aborted included (ADR 0030, "Logging": an
// API request, its route and its outcome): the route by a fixed name, the
// outcome in fixed words, and the status. Never its path, query, headers or
// body, a token, a code, an identifier or an error's text.

// The outcomes a request's line says, besides the status's own (outcome).
const (
	outcomeCut         = "cut"          // the lease ended while the answer was sent
	outcomeNotReleased = "not_released" // the lease refused to start: nothing of the answer went
	outcomeClientLeft  = "client_left"  // the client went away before the answer
	outcomeAborted     = "aborted"      // the response was aborted for another reason
	outcomeNoAbort     = "no_abort"     // mail asked over HTTP/2, where the last byte cannot be confirmed: refused before the token is checked
	outcomeNotRecorded = "not_recorded" // a refusal of the API's own could not be recorded (Refuse): 503
)

// requestEvent is what a request's line says. Its handler names the route
// and, when the status alone does not say it, the outcome.
type requestEvent struct {
	route   string
	outcome string
	status  int
}

type requestEventKey struct{}

// eventOf is r's requestEvent; one that is logged nowhere for a request
// that is not logged.
func eventOf(r *http.Request) *requestEvent {
	if e, ok := r.Context().Value(requestEventKey{}).(*requestEvent); ok {
		return e
	}
	return &requestEvent{}
}

// setRoute names r's route in its line.
func setRoute(r *http.Request, route string) { eventOf(r).route = route }

// setOutcome says r's outcome in its line, the first said standing.
func setOutcome(r *http.Request, outcome string) {
	if e := eventOf(r); e.outcome == "" {
		e.outcome = outcome
	}
}

// result is the outcome the line says: the one the handler said, else the
// status's.
func (e *requestEvent) result() string {
	if e.outcome != "" {
		return e.outcome
	}
	switch s := e.status; {
	case s < 400:
		return "ok"
	case s == http.StatusUnauthorized:
		return "unauthorized"
	case s == http.StatusForbidden:
		return "forbidden"
	case s == http.StatusTooManyRequests:
		return "rate_limited"
	case s == http.StatusServiceUnavailable:
		return "unavailable"
	case s < 500:
		return "refused"
	}
	return "failed"
}

// logRequests writes msg once for every request of h, when it ends: an
// abort (http.ErrAbortHandler, as send aborts a transfer) is logged and
// then goes on as it was, never recovered into an answer. The writer h is
// given unwraps to w, so ResponseController still reaches the server's.
func logRequests(log *slog.Logger, msg string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := &requestEvent{route: "other"}
		defer func() {
			p := recover()
			switch {
			case p != nil && e.outcome == "":
				e.outcome = outcomeAborted
			case p == nil && e.status == 0:
				e.status = http.StatusOK
			}
			log.Info(msg, "route", e.route, "outcome", e.result(), "status", e.status)
			if p != nil {
				panic(p)
			}
		}()
		h.ServeHTTP(&statusWriter{w, e}, r.WithContext(context.WithValue(r.Context(), requestEventKey{}, e)))
	})
}

// statusWriter records the status a handler answered.
type statusWriter struct {
	http.ResponseWriter
	e *requestEvent
}

func (w *statusWriter) WriteHeader(code int) {
	if w.e.status == 0 {
		w.e.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.e.status == 0 {
		w.e.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
