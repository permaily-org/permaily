package sesmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/permaily-org/permaily/pkg/attestation"
)

const (
	// maxAge is how old the reading a message is sent on may be.
	maxAge = 5 * time.Second
	// minGap is the least time from the start of one reading to the start of
	// the next, whoever asks: SES limits these calls.
	minGap = time.Second
	// readTimeout bounds one reading, both calls. It is shorter than maxAge,
	// so a reading is never too old to send on when it arrives.
	readTimeout = 4 * time.Second
	// watchInterval is how often Run reads on its own.
	watchInterval = time.Minute
	// maxHistory bounds what /status carries.
	maxHistory = 32
)

// Why nothing is sent. Neither says more: what SES answers names addresses.
var (
	errNotClean = errors.New("sesmail: SES is set to do more with a message than deliver it; nothing is sent")
	errUnread   = errors.New("sesmail: what SES would do with a message could not be read; nothing is sent")
)

// Watch reads from SES what it would do with a message besides delivering it.
// Whoever administers the account can give an identity a configuration set
// that applies even when the sender names none, and tie it to an archive:
// then SES keeps a copy of every message, a sign-in code among them. The
// enclave names no configuration set, so the rule is that none applies to
// the domain it sends from, nor to the address, whose own identity would
// replace the domain's settings.
//
// SES is reached with TLS from inside the enclave, so the host cannot alter
// the answer. Every different state read is kept for /status, so a
// configuration set that was put and taken away while nobody was verifying
// still shows.
//
// Set every field but Endpoint, Log and Now.
type Watch struct {
	// HTTP reaches SES; in the enclave, through the host's proxy.
	HTTP        *http.Client
	Credentials aws.CredentialsProvider
	Region      string
	// Domain is the identity mail is sent from, and Address the address,
	// which must be of that domain.
	Domain  string
	Address string
	// Endpoint replaces https://email.<region>.amazonaws.com (tests).
	Endpoint string
	Log      *slog.Logger
	Now      func() time.Time

	// pause waits until a reading may start, and interval replaces
	// watchInterval (tests).
	pause    func(ctx context.Context, d time.Duration) error
	interval time.Duration

	mu sync.Mutex
	// flight is the reading being made, if any, and last the newest one done.
	flight, last *reading
	// asked holds at most one request for a reading after a message.
	asked chan struct{}

	started    time.Time // first attempt
	checkedAt  time.Time // last reading that got an answer
	history    []attestation.MailObservation
	historyCut bool
}

// reading is one look at both identities.
type reading struct {
	// at is when it started: what it says was so at some moment after.
	at   time.Time
	done chan struct{}
	// ok is false when SES gave no answer to go by, and state what it said.
	// Both are set before done is closed.
	ok    bool
	state attestation.MailState
}

// Run reads at once, then every watchInterval and after each message sent,
// until ctx is done: the history does not depend on anybody sending mail or
// asking for /status.
func (w *Watch) Run(ctx context.Context) {
	every := w.interval
	if every == 0 {
		every = watchInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		w.next(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.asks():
		}
	}
}

// Clean returns nil only when a reading at most maxAge old says no
// configuration set applies. It makes that reading when there is none, and
// callers at the same time share one. A reading that could not be made is an
// error too: mail waits, it does not go out unwatched.
func (w *Watch) Clean(ctx context.Context) error {
	r := w.newest(ctx)
	switch {
	case r == nil || !r.ok:
		return errUnread
	case !r.state.Clean():
		return errNotClean
	}
	return nil
}

// Status is what /status says about the mail the enclave sends. Nil for no
// watch at all, so an image that sends no mail reports none.
func (w *Watch) Status() *attestation.MailStatus {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	st := &attestation.MailStatus{
		Domain: w.Domain, Address: w.Address,
		CheckedAt: w.checkedAt, Failing: w.last != nil && !w.last.ok,
		// A copy, so a caller cannot change what the watch remembers.
		WatchedSince: w.started, History: slices.Clone(w.history), HistoryTruncated: w.historyCut,
	}
	if n := len(w.history); n > 0 {
		state := w.history[n-1].MailState
		st.State = &state
	}
	return st
}

// again asks Run for a reading soon, and does not wait for it.
func (w *Watch) again() {
	select {
	case w.asks() <- struct{}{}:
	default: // one is asked for already
	}
}

func (w *Watch) asks() chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.asked == nil {
		w.asked = make(chan struct{}, 1)
	}
	return w.asked
}

// newest returns the reading to send on: the last one while it is new
// enough, or else the one being made, or one started now. Nil when ctx ends
// before it is there.
func (w *Watch) newest(ctx context.Context) *reading {
	w.mu.Lock()
	if r := w.last; r != nil {
		age := w.now().Sub(r.at)
		// One that failed is not tried again before minGap, or every message
		// that waits would be a call to SES.
		if (r.ok && age <= maxAge) || (!r.ok && age < minGap) {
			w.mu.Unlock()
			return r
		}
	}
	// From here the last reading started minGap ago or more: one may start.
	f := w.flight
	if f == nil {
		f = w.start()
	}
	w.mu.Unlock()
	select {
	case <-f.done:
		return f
	case <-ctx.Done():
		return nil
	}
}

// next makes a reading that starts from now on, for Run: one that was on its
// way already when a message left says nothing about what followed it.
func (w *Watch) next(ctx context.Context) {
	for ctx.Err() == nil {
		w.mu.Lock()
		f, gap := w.flight, time.Duration(0)
		if f == nil {
			if w.last != nil {
				gap = minGap - w.now().Sub(w.last.at)
			}
			if gap <= 0 {
				f = w.start()
				w.mu.Unlock()
				select {
				case <-f.done:
				case <-ctx.Done():
				}
				return
			}
		}
		w.mu.Unlock()
		if f != nil {
			select {
			case <-f.done:
			case <-ctx.Done():
			}
			continue
		}
		if w.wait(ctx, gap) != nil {
			return
		}
	}
}

// start begins a reading. The caller holds mu, and has checked that none is
// being made and that the last one started minGap ago or more.
func (w *Watch) start() *reading {
	f := &reading{at: w.now(), done: make(chan struct{})}
	w.flight = f
	// A message that left before now is covered by this reading.
	if w.asked != nil {
		select {
		case <-w.asked:
		default:
		}
	}
	go w.finish(f)
	return f
}

// finish makes the reading f and records it.
func (w *Watch) finish(f *reading) {
	// The reading belongs to everyone who waits for it, not to whoever
	// started it: it does not end with their request.
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	state, status, ok := w.read(ctx)
	f.ok, f.state = ok, state

	w.mu.Lock()
	before := w.last
	w.mu.Unlock()
	w.report(before, f, status)

	w.mu.Lock()
	at := f.at.UTC().Truncate(time.Second)
	if w.started.IsZero() {
		w.started = at
	}
	if ok {
		w.checkedAt = at
		if n := len(w.history); n > 0 && w.history[n-1].MailState == state {
			w.history[n-1].LastSeen = at
		} else {
			w.history = append(w.history, attestation.MailObservation{MailState: state, FirstSeen: at, LastSeen: at})
			if len(w.history) > maxHistory {
				w.history, w.historyCut = w.history[len(w.history)-maxHistory:], true
			}
		}
	}
	w.last, w.flight = f, nil
	w.mu.Unlock()
	close(f.done)
}

// report logs a reading that says something else than the one before it.
// Logs leave the enclave and are public: they carry which of the known
// states each identity is in and an HTTP status, never a name, an address or
// words of SES or of the network.
func (w *Watch) report(before, f *reading, status int) {
	if before != nil && before.ok == f.ok && before.state == f.state {
		return
	}
	switch {
	case !f.ok:
		w.log().Warn("sesmail: what SES would do with a message could not be read; nothing is sent until it can be", "status", status)
	case !f.state.Clean():
		w.log().Error("sesmail: SES is set to do more with a message than deliver it; nothing is sent until that is undone",
			"domain_identity", f.state.DomainIdentity, "address_identity", f.state.AddressIdentity)
	default:
		w.log().Info("sesmail: SES is set to deliver a message and nothing else; mail is sent",
			"domain_identity", f.state.DomainIdentity, "address_identity", f.state.AddressIdentity)
	}
}

// read asks SES about both identities. ok is false when there is no answer
// to go by; status is then the HTTP status of the one that could not be
// used, 0 when there was none.
func (w *Watch) read(ctx context.Context) (state attestation.MailState, status int, ok bool) {
	// An address of another domain would be sent under an identity that is
	// not read here.
	local, domain, found := strings.Cut(w.Address, "@")
	if w.HTTP == nil || w.Credentials == nil || w.Region == "" || w.Domain == "" || !found || local == "" || !strings.EqualFold(domain, w.Domain) {
		return state, 0, false
	}
	c := client{http: w.HTTP, credentials: w.Credentials, region: w.Region, endpoint: w.Endpoint, now: w.Now}
	if state.DomainIdentity, status, ok = identity(ctx, c, w.Domain, "DOMAIN"); !ok {
		return attestation.MailState{}, status, false
	}
	if state.AddressIdentity, status, ok = identity(ctx, c, w.Address, "EMAIL_ADDRESS"); !ok {
		return attestation.MailState{}, status, false
	}
	return state, 0, true
}

// identity asks SES for one identity (GetEmailIdentity) and says which of the
// states of attestation.MailState it is in. Anything it does not know how to
// read is no answer: only "no such identity" in SES's own words counts as
// absent, because an address that is absent is a clean one.
func identity(ctx context.Context, c client, name, kindOf string) (state string, status int, ok bool) {
	got, err := c.do(ctx, http.MethodGet, "/v2/email/identities/"+escape(name), nil)
	if err != nil {
		return "", 0, false
	}
	switch {
	case got.status == http.StatusNotFound && got.kind == "NotFoundException":
		return attestation.MailAbsent, got.status, true
	case got.status != http.StatusOK:
		return "", got.status, false
	}
	var said struct {
		IdentityType             string
		ConfigurationSetName     string
		VerifiedForSendingStatus bool
	}
	// The kind of identity is asked for so that an answer to something else,
	// which would have no configuration set in it, is not taken for one.
	if json.Unmarshal(got.body, &said) != nil || said.IdentityType != kindOf {
		return "", got.status, false
	}
	switch {
	case said.ConfigurationSetName != "":
		return attestation.MailConfigurationSet, got.status, true
	case !said.VerifiedForSendingStatus:
		return attestation.MailNotVerified, got.status, true
	}
	return attestation.MailNoConfigurationSet, got.status, true
}

// escape writes s as one segment of a path the way AWS expects it, and the
// way its signature is computed: every byte but letters, digits, "-", ".",
// "_" and "~" as %XX. url.PathEscape leaves "@" as it is.
func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (w *Watch) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Watch) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (w *Watch) wait(ctx context.Context, d time.Duration) error {
	if w.pause != nil {
		return w.pause(ctx, d)
	}
	return wait(ctx, d)
}
