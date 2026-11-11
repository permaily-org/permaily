package sesmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/permaily-org/permaily/internal/services/accounts"
)

const (
	recipient = "ana@example.com"
	subject   = "Sign in to Permaily"
	text      = "Your code to sign in to Permaily:\n\n    123456\n"
)

type staticCredentials struct{ err error }

func (c staticCredentials) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret", SessionToken: "session"}, c.err
}

// ses is a stand-in for SES: it answers each request with the next of its
// answers, and the last one from then on.
type ses struct {
	mu       sync.Mutex
	answers  []int
	kind     string
	requests []*http.Request
	bodies   [][]byte
}

func (f *ses) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, body)
	status := http.StatusOK
	if len(f.answers) > 0 {
		status = f.answers[min(len(f.requests), len(f.answers))-1]
	}
	f.mu.Unlock()
	if status != http.StatusOK {
		if f.kind != "" {
			w.Header().Set("X-Amzn-Errortype", f.kind)
		}
		w.WriteHeader(status)
		// What SES says in words names the address.
		_, _ = w.Write([]byte(`{"message":"Email address is not verified: ` + recipient + `"}`))
		return
	}
	_, _ = w.Write([]byte(`{"MessageId":"0100018f-example"}`))
}

func (f *ses) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func sender(t *testing.T, f *ses) (*Sender, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	logs := &bytes.Buffer{}
	return &Sender{
		HTTP: srv.Client(), Credentials: staticCredentials{}, Region: "us-east-2",
		Name: "Permaily", Address: "no-reply@mail.example.org", Endpoint: srv.URL,
		Log:  slog.New(slog.NewJSONHandler(logs, nil)),
		Now:  func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) },
		Wait: func(context.Context, time.Duration) error { return nil },
	}, logs
}

func message() accounts.Mail {
	return accounts.Mail{To: recipient, Subject: subject, Text: text}
}

// says nothing fails when what is logged or returned names the recipient or
// holds the message.
func saysNothing(t *testing.T, what string) {
	t.Helper()
	for _, secret := range []string{recipient, "123456", subject} {
		if strings.Contains(what, secret) {
			t.Errorf("%q is in %q", secret, what)
		}
	}
}

func TestSend(t *testing.T) {
	f := &ses{}
	s, logs := sender(t, f)
	if err := s.Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatalf("%d requests, want 1", f.count())
	}
	r := f.requests[0]
	if r.Method != http.MethodPost || r.URL.Path != "/v2/email/outbound-emails" {
		t.Errorf("%s %s", r.Method, r.URL.Path)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content type %q", got)
	}
	// Signed for SES in the region, with the session's token.
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261111/us-east-2/ses/aws4_request") || !strings.Contains(auth, "Signature=") {
		t.Errorf("authorization %q", auth)
	}
	if r.Header.Get("X-Amz-Security-Token") != "session" || r.Header.Get("X-Amz-Date") != "20261111T120000Z" {
		t.Errorf("token %q, date %q", r.Header.Get("X-Amz-Security-Token"), r.Header.Get("X-Amz-Date"))
	}
	var got map[string]any
	if err := json.Unmarshal(f.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"FromEmailAddress": `"Permaily" <no-reply@mail.example.org>`,
		"Destination":      map[string]any{"ToAddresses": []any{recipient}},
		"Content": map[string]any{"Simple": map[string]any{
			"Subject": map[string]any{"Data": subject, "Charset": "UTF-8"},
			"Body":    map[string]any{"Text": map[string]any{"Data": text, "Charset": "UTF-8"}},
		}},
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if !bytes.Equal(a, b) {
		t.Errorf("sent\n%s\nwant\n%s", a, b)
	}
	if logs.Len() != 0 {
		t.Errorf("logged %s", logs)
	}
}

func TestRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		kind, want string
	}{
		"named":              {"MessageRejected", "MessageRejected"},
		"with a description": {"AccountSuspendedException:http://internal.amazon.com/coral/", "AccountSuspendedException"},
		"not a name":         {recipient, ""},
		"no name":            {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := &ses{answers: []int{http.StatusBadRequest}, kind: tc.kind}
			s, logs := sender(t, f)
			err := s.Send(context.Background(), message())
			var refused *Error
			if !errors.As(err, &refused) || refused.Status != http.StatusBadRequest || refused.Kind != tc.want {
				t.Fatalf("got %v", err)
			}
			// SES said no for good: asking again changes nothing.
			if f.count() != 1 {
				t.Errorf("%d requests, want 1", f.count())
			}
			if tc.want != "" && !strings.Contains(logs.String(), tc.want) {
				t.Errorf("the kind of error is not logged: %s", logs)
			}
			saysNothing(t, err.Error())
			saysNothing(t, logs.String())
		})
	}
}

func TestTriesAgain(t *testing.T) {
	t.Run("until it works", func(t *testing.T) {
		f := &ses{answers: []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusOK}}
		s, _ := sender(t, f)
		var waited []time.Duration
		s.Wait = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
		if err := s.Send(context.Background(), message()); err != nil {
			t.Fatal(err)
		}
		if f.count() != 3 {
			t.Errorf("%d requests, want 3", f.count())
		}
		if len(waited) != 2 || waited[0] >= waited[1] {
			t.Errorf("waited %v, want two waits, the second longer", waited)
		}
	})
	t.Run("and gives up", func(t *testing.T) {
		f := &ses{answers: []int{http.StatusServiceUnavailable}, kind: "ServiceUnavailable"}
		s, logs := sender(t, f)
		err := s.Send(context.Background(), message())
		var refused *Error
		if !errors.As(err, &refused) || refused.Status != http.StatusServiceUnavailable {
			t.Fatalf("got %v", err)
		}
		if f.count() != attempts {
			t.Errorf("%d requests, want %d", f.count(), attempts)
		}
		saysNothing(t, logs.String())
	})
	t.Run("unless nobody waits for it", func(t *testing.T) {
		f := &ses{answers: []int{http.StatusServiceUnavailable}}
		s, _ := sender(t, f)
		ctx, cancel := context.WithCancel(context.Background())
		real := &Sender{}
		s.Wait = func(ctx context.Context, _ time.Duration) error {
			cancel()
			return real.wait(ctx, time.Hour)
		}
		if err := s.Send(ctx, message()); err == nil {
			t.Fatal("sent")
		}
		if f.count() != 1 {
			t.Errorf("%d requests, want 1", f.count())
		}
	})
	t.Run("waiting for real", func(t *testing.T) {
		s := &Sender{}
		start := time.Now()
		if err := s.wait(context.Background(), 20*time.Millisecond); err != nil || time.Since(start) < 20*time.Millisecond {
			t.Errorf("waited %s: %v", time.Since(start), err)
		}
	})
	t.Run("stops when the request is over", func(t *testing.T) {
		f := &ses{answers: []int{http.StatusServiceUnavailable}}
		s, _ := sender(t, f)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.Send(ctx, message()); err == nil {
			t.Fatal("sent")
		}
		if f.count() > 1 {
			t.Errorf("%d requests, want one at most", f.count())
		}
	})
}

func TestUnreachable(t *testing.T) {
	f := &ses{}
	s, logs := sender(t, f)
	srv := httptest.NewServer(f)
	s.Endpoint = srv.URL
	srv.Close()
	err := s.Send(context.Background(), message())
	if err == nil {
		t.Fatal("sent to nobody")
	}
	// An error of the network names the URL: it is not passed on.
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(logs.String(), "127.0.0.1") {
		t.Errorf("the address of SES is in %q or in %s", err, logs)
	}
	if got := strings.Count(logs.String(), "could not be reached"); got != attempts {
		t.Errorf("%d tries logged, want %d", got, attempts)
	}
	saysNothing(t, err.Error())
	saysNothing(t, logs.String())
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWhereSESIs(t *testing.T) {
	var asked string
	s := &Sender{
		HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			asked = r.URL.String()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})},
		Credentials: staticCredentials{}, Region: "us-east-2", Name: "Permaily", Address: "no-reply@mail.example.org",
	}
	if err := s.Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	if asked != "https://email.us-east-2.amazonaws.com/v2/email/outbound-emails" {
		t.Errorf("asked %s", asked)
	}
}

func TestCannotSend(t *testing.T) {
	f := &ses{}
	full, _ := sender(t, f)
	for name, change := range map[string]func(*Sender){
		"no client":       func(s *Sender) { s.HTTP = nil },
		"no credentials":  func(s *Sender) { s.Credentials = nil },
		"no region":       func(s *Sender) { s.Region = "" },
		"no name":         func(s *Sender) { s.Name = "" },
		"no address":      func(s *Sender) { s.Address = "" },
		"credentials off": func(s *Sender) { s.Credentials = staticCredentials{err: errors.New("the host is down")} },
		"a bad endpoint":  func(s *Sender) { s.Endpoint = "http://[::1" },
	} {
		t.Run(name, func(t *testing.T) {
			s := *full
			change(&s)
			err := s.Send(context.Background(), message())
			if err == nil {
				t.Fatal("sent")
			}
			saysNothing(t, err.Error())
		})
	}
	if f.count() != 0 {
		t.Errorf("%d requests, want none", f.count())
	}
}
