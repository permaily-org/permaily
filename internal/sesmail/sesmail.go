// Package sesmail sends the service's own mail (sign-in messages, notices)
// through Amazon SES. It makes the two calls it needs of the API v2,
// SendEmail and GetEmailIdentity, signed by hand: the whole SES client would
// be compiled into the image for nothing.
//
// What leaves here is read by AWS, which is trusted already (ADR 0017), and
// by nobody on the way: TLS ends inside the enclave. Whoever administers the
// account is not trusted, and can tell SES to keep a copy of what is sent:
// Watch reads whether they did, and Sender sends nothing when they did.
package sesmail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/permaily-org/permaily/internal/services/accounts"
)

// Sender sends mail from one address. Set every field but Endpoint, Log,
// Now and Wait.
type Sender struct {
	// HTTP reaches SES; in the enclave, through the host's proxy.
	HTTP        *http.Client
	Credentials aws.CredentialsProvider
	Region      string
	// Name and Address are who the mail is from: "Name <Address>". The
	// address must belong to an identity verified in SES.
	Name    string
	Address string
	// Endpoint replaces https://email.<region>.amazonaws.com (tests).
	Endpoint string
	Log      *slog.Logger
	Now      func() time.Time
	// Wait waits before a retry (tests).
	Wait func(ctx context.Context, d time.Duration) error
	// Watch reads what SES would do with a message besides delivering it.
	// When set, nothing is sent unless it says nothing else gets a copy.
	Watch *Watch
}

// Limits of one message.
const (
	attempts       = 3
	attemptTimeout = 15 * time.Second
	maxAnswer      = 16 << 10
)

// Error is a refusal by SES. It holds nothing of the message: what SES says
// in words names the addresses, so only its kind of error is kept.
type Error struct {
	Status int
	// Kind is SES's name for the error (MessageRejected, for example); empty
	// when the answer gave none.
	Kind string
}

func (e *Error) Error() string {
	if e.Kind == "" {
		return fmt.Sprintf("sesmail: SES answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("sesmail: SES answered HTTP %d (%s)", e.Status, e.Kind)
}

// temporary says whether the same request may work later.
func (e *Error) temporary() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests
}

type content struct {
	Data    string `json:"Data"`
	Charset string `json:"Charset"`
}

type sendEmail struct {
	FromEmailAddress string `json:"FromEmailAddress"`
	Destination      struct {
		ToAddresses []string `json:"ToAddresses"`
	} `json:"Destination"`
	Content struct {
		Simple struct {
			Subject content `json:"Subject"`
			Body    struct {
				Text content `json:"Text"`
			} `json:"Body"`
		} `json:"Simple"`
	} `json:"Content"`
}

// kind is what a name of an error may look like. Anything else is dropped:
// the name is logged.
var kind = regexp.MustCompile(`^[A-Za-z]{1,64}$`)

// Send implements accounts.Mailer. An error it returns never holds an
// address, the subject or the text.
func (s *Sender) Send(ctx context.Context, m accounts.Mail) error {
	if s.HTTP == nil || s.Credentials == nil || s.Region == "" || s.Name == "" || s.Address == "" {
		return errors.New("sesmail: a client, credentials, a region and who the mail is from are needed")
	}
	var in sendEmail
	in.FromEmailAddress = (&mail.Address{Name: s.Name, Address: s.Address}).String()
	in.Destination.ToAddresses = []string{m.To}
	in.Content.Simple.Subject = content{Data: m.Subject, Charset: "UTF-8"}
	in.Content.Simple.Body.Text = content{Data: m.Text, Charset: "UTF-8"}
	body, err := json.Marshal(in)
	if err != nil {
		return errors.New("sesmail: the message cannot be written as JSON")
	}

	for n := 1; ; n++ {
		err = s.attempt(ctx, body)
		if err == nil {
			return nil
		}
		var refused *Error
		switch {
		case errors.Is(err, errNotClean):
			// Waiting changes nothing: someone has to undo it.
			return err
		case errors.Is(err, errUnread):
			// The watch logged it.
		case errors.As(err, &refused):
			s.log().Warn("sesmail: SES refused a message", "status", refused.Status, "kind", refused.Kind, "attempt", n)
			if !refused.temporary() {
				return err
			}
		default:
			// Not the error: one of the network may hold the URL, and never more.
			s.log().Warn("sesmail: SES could not be reached", "attempt", n)
		}
		if n == attempts || ctx.Err() != nil {
			return err
		}
		if err := s.wait(ctx, time.Duration(n*n)*500*time.Millisecond); err != nil {
			return errors.New("sesmail: gave up waiting to try again")
		}
	}
}

// attempt sends once, and only when the watch, if there is one, has just read
// that nothing else gets a copy.
func (s *Sender) attempt(ctx context.Context, body []byte) error {
	if s.Watch == nil {
		return s.send(ctx, body)
	}
	if err := s.Watch.Clean(ctx); err != nil {
		return err
	}
	// Whatever the answer, SES may have taken the message: the watch reads
	// again after it, so a configuration set put there in between shows.
	defer s.Watch.again()
	return s.send(ctx, body)
}

// send makes one request.
func (s *Sender) send(ctx context.Context, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	c := client{http: s.HTTP, credentials: s.Credentials, region: s.Region, endpoint: s.Endpoint, now: s.Now}
	// The answer is not kept.
	got, err := c.do(ctx, http.MethodPost, "/v2/email/outbound-emails", body)
	if err != nil {
		return err
	}
	if got.status == http.StatusOK {
		return nil
	}
	return &Error{Status: got.status, Kind: got.kind}
}

// client is how SES is reached and asked: what Sender and Watch share.
type client struct {
	http        *http.Client
	credentials aws.CredentialsProvider
	region      string
	endpoint    string
	now         func() time.Time
}

// answer is what SES said to one request.
type answer struct {
	status int
	// kind is SES's name for the error; empty when it gave none.
	kind string
	// body is at most maxAnswer long: a longer one is cut.
	body []byte
}

// do makes one signed request to the API v2 of SES. path is already escaped.
// An error it returns is a fixed sentence: one of the network may hold the
// URL, and with it an address.
func (c client) do(ctx context.Context, method, path string, body []byte) (*answer, error) {
	endpoint := c.endpoint
	if endpoint == "" {
		endpoint = "https://email." + c.region + ".amazonaws.com"
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("sesmail: the request cannot be made")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	creds, err := c.credentials.Retrieve(ctx)
	if err != nil {
		return nil, errors.New("sesmail: no credentials")
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	sum := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "ses", c.region, now); err != nil {
		return nil, errors.New("sesmail: the request cannot be signed")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("sesmail: SES could not be reached")
	}
	defer res.Body.Close()
	got := &answer{status: res.StatusCode}
	// Read to the end, or to the limit, so the connection is used again. An
	// answer that stops halfway is not an error here: a message it answers
	// was taken, and a reading of it does not parse.
	got.body, _ = io.ReadAll(io.LimitReader(res.Body, maxAnswer))
	// The name may come followed by ":" and an address of its description.
	if name, _, _ := strings.Cut(res.Header.Get("X-Amzn-Errortype"), ":"); kind.MatchString(name) {
		got.kind = name
	}
	return got, nil
}

func (s *Sender) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (s *Sender) wait(ctx context.Context, d time.Duration) error {
	if s.Wait != nil {
		return s.Wait(ctx, d)
	}
	return wait(ctx, d)
}

// wait returns when d has passed, or with an error when ctx ends first.
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
