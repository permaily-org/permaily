// Package smtpd is the MX for inbox.permaily.com, inside the enclave. It
// offers STARTTLS with the enclave's own certificate and refuses mail on a
// connection without it, so no message reaches a mailbox in clear text
// (MTA-STS makes senders require that certificate). It asks the mailboxes
// at RCPT TO which mailbox an address leads to, and hands each message to
// them at the end of DATA (Recipients).
//
// What a sender can make it hold is bounded before anything is buffered
// (ADR 0026, "Admission" and "Deadlines of a session"): sessions at once,
// bytes of DATA at once (taken as they arrive, never from a declared SIZE),
// and an absolute deadline for every phase of a session, cut into the
// deadlines go-smtp sets on the connection. A refused DATA closes the
// connection instead of reading the rest of the message. It never logs
// addresses or content.
package smtpd

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// MaxRecipients is how many recipients one message has at most.
const MaxRecipients = 10

// Limits are the deadlines and caps of a server; a zero field is the
// package's constant of the same meaning. Tests make them short.
type Limits struct {
	TLS, Idle, Transaction, DataStall, Command, Session, CloseGrace, AdmissionWait, Ingest time.Duration
	Sessions, Transactions, Commands, Failed                                               int
	DataInFlight, DataStep, MaxMessage                                                     int64
}

func (l Limits) withDefaults() Limits {
	d := func(v *time.Duration, def time.Duration) {
		if *v == 0 {
			*v = def
		}
	}
	n := func(v *int, def int) {
		if *v == 0 {
			*v = def
		}
	}
	b := func(v *int64, def int64) {
		if *v == 0 {
			*v = def
		}
	}
	d(&l.TLS, TLSDeadline)
	d(&l.Idle, IdleDeadline)
	d(&l.Transaction, TransactionDeadline)
	d(&l.DataStall, DataStall)
	d(&l.Command, CommandTimeout)
	d(&l.Session, SessionDeadline)
	d(&l.CloseGrace, CloseGrace)
	d(&l.AdmissionWait, AdmissionWait)
	d(&l.Ingest, mailboxes.IngestDeadline)
	n(&l.Sessions, MaxSessions)
	n(&l.Transactions, MaxTransactions)
	n(&l.Commands, MaxCommands)
	n(&l.Failed, MaxFailedCommands)
	b(&l.DataInFlight, MaxDataInFlight)
	b(&l.DataStep, DataStep)
	b(&l.MaxMessage, inbox.MaxMessageBytes)
	return l
}

// Config configures the server.
type Config struct {
	Domain string // greeting name, e.g. inbox.permaily.com
	TLS    *tls.Config
	Limits Limits
}

// Server is the SMTP server.
type Server struct {
	smtp     *smtp.Server
	rcpts    Recipients
	limits   Limits
	log      *slog.Logger
	sessions chan struct{}
	data     chan struct{}
}

// New returns an SMTP server that delivers to r.
func New(r Recipients, cfg Config, log *slog.Logger) *Server {
	l := cfg.Limits.withDefaults()
	s := &Server{
		rcpts: r, limits: l, log: log,
		sessions: make(chan struct{}, l.Sessions),
		data:     make(chan struct{}, l.DataInFlight/l.DataStep),
	}
	srv := smtp.NewServer(s)
	srv.Domain = cfg.Domain
	srv.TLSConfig = cfg.TLS
	srv.MaxMessageBytes = l.MaxMessage
	srv.MaxRecipients = MaxRecipients
	srv.ReadTimeout = l.Command
	srv.WriteTimeout = l.Command
	srv.MaxLineLength = 4000
	srv.AllowInsecureAuth = false
	srv.ErrorLog = discard
	s.smtp = srv
	return s
}

// discard keeps go-smtp's own log lines, which name remote addresses, out of
// the enclave's log.
var discard = log.New(io.Discard, "", 0)

// Serve accepts connections on ln until Close.
func (s *Server) Serve(ln net.Listener) error { return s.smtp.Serve(&listener{Listener: ln, s: s}) }

// Close stops the server and closes every connection.
func (s *Server) Close() error { return s.smtp.Close() }

// Held is how many bytes of DATA the server holds now, all sessions together.
func (s *Server) Held() int64 { return int64(len(s.data)) * s.limits.DataStep }

// Sessions is how many sessions the server holds now.
func (s *Server) Sessions() int { return len(s.sessions) }

// take takes one step of the DATA budget, waiting at most AdmissionWait.
func (s *Server) take(ctx context.Context) bool {
	select {
	case s.data <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(s.limits.AdmissionWait)
	defer t.Stop()
	select {
	case s.data <- struct{}{}:
		return true
	case <-t.C:
	case <-ctx.Done():
	}
	return false
}

func (s *Server) give(steps int) {
	for range steps {
		<-s.data
	}
}

// listener refuses a session past MaxSessions with 421 4.3.2, and wraps each
// other connection so its deadlines are cut to the session's.
type listener struct {
	net.Listener
	s *Server
}

func (l *listener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.s.sessions <- struct{}{}:
			return newConn(c, l.s), nil
		default:
			_ = c.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(c, "421 4.3.2 Too busy, try again later\r\n")
			_ = c.Close()
		}
	}
}

// conn is one session's connection. Every deadline go-smtp asks for is cut
// to the earliest limit of the session's phase (readCap, writeCap); tls.Conn
// sets its deadlines on it too, so the cut holds after STARTTLS.
type conn struct {
	net.Conn
	s     *Server
	start time.Time
	end   time.Time

	mu                sync.Mutex
	readCap, writeCap time.Time
	reqRead, reqWrite time.Time
	// inData is set while a message's bytes are being read: a timeout then
	// answers 451 4.4.2 rather than go-smtp's own words.
	inData   bool
	released bool

	// What the session did, across the sessions go-smtp makes for it.
	transactions, commands, failed int
	txEnd                          time.Time
	afterData                      bool
}

func newConn(c net.Conn, s *Server) *conn {
	now := time.Now()
	k := &conn{Conn: c, s: s, start: now, end: now.Add(s.limits.Session)}
	k.capRead(now.Add(s.limits.TLS))
	k.capWrite(k.end)
	return k
}

func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero() || a.Before(b):
		return a
	}
	return b
}

func (k *conn) capRead(t time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.readCap = earliest(t, k.end)
	_ = k.Conn.SetReadDeadline(earliest(k.reqRead, k.readCap))
}

func (k *conn) capWrite(t time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.writeCap = earliest(t, k.end)
	_ = k.Conn.SetWriteDeadline(earliest(k.reqWrite, k.writeCap))
}

func (k *conn) SetReadDeadline(t time.Time) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.reqRead = t
	return k.Conn.SetReadDeadline(earliest(t, k.readCap))
}

func (k *conn) SetWriteDeadline(t time.Time) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.reqWrite = t
	return k.Conn.SetWriteDeadline(earliest(t, k.writeCap))
}

func (k *conn) SetDeadline(t time.Time) error {
	if err := k.SetReadDeadline(t); err != nil {
		return err
	}
	return k.SetWriteDeadline(t)
}

// errStalled is a timeout while a message's bytes are read: the transaction
// past its deadline, or DATA silent for DataStall.
var errStalled = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 4, 2}, Message: "Timeout, closing connection"}

func (k *conn) Read(p []byte) (int, error) {
	n, err := k.Conn.Read(p)
	if err != nil {
		var ne net.Error
		k.mu.Lock()
		inData := k.inData
		k.mu.Unlock()
		if inData && errors.As(err, &ne) && ne.Timeout() {
			return n, errStalled
		}
	}
	return n, err
}

func (k *conn) Close() error {
	k.mu.Lock()
	release := !k.released
	k.released = true
	k.mu.Unlock()
	if release {
		<-k.s.sessions
	}
	return k.Conn.Close()
}

// closeSoon closes the session after its next answer: reading stops at once
// and writing has CloseGrace. go-smtp then fails its next read and closes.
func (k *conn) closeSoon() {
	now := time.Now()
	k.capRead(now)
	k.capWrite(now.Add(k.s.limits.CloseGrace))
}

// The answers of the session's own (ADR 0026, "The answers, all of them").
var (
	errNoTLS     = &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Must issue a STARTTLS command first"}
	errNoMailbox = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such mailbox"}
	errSlowDown  = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "Too much mail for this mailbox now, try again later"}
	errTemporary = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Temporary failure, try again later"}
	errNoRoom    = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 3, 1}, Message: "Insufficient system storage, try again later"}
	errTooLarge  = &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "Maximum message size exceeded"}
	errTooMuch   = &smtp.SMTPError{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: "Too many commands, closing connection"}
	errLongLine  = &smtp.SMTPError{Code: 500, EnhancedCode: smtp.EnhancedCode{5, 4, 0}, Message: "Too long line, closing connection"}
)

// NewSession implements smtp.Backend: at each EHLO that starts a session,
// before STARTTLS and after it.
func (s *Server) NewSession(c *smtp.Conn) (smtp.Session, error) {
	k := connOf(c)
	if k == nil {
		return nil, errors.New("smtpd: a connection that is not the server's")
	}
	ss := &session{s: s, k: k, c: c}
	if ss.encrypted() {
		// The EHLO that follows STARTTLS ends the TLS deadline: from here to
		// the first MAIL FROM is idle.
		k.capRead(time.Now().Add(s.limits.Idle))
	}
	if err := ss.command(); err != nil {
		return nil, err
	}
	return ss, nil
}

func connOf(c *smtp.Conn) *conn {
	nc := c.Conn()
	if t, ok := nc.(*tls.Conn); ok {
		nc = t.NetConn()
	}
	k, _ := nc.(*conn)
	return k
}

type session struct {
	s         *Server
	k         *conn
	c         *smtp.Conn
	from      string
	mailboxes []string
}

func (ss *session) encrypted() bool { _, ok := ss.c.TLSConnectionState(); return ok }

// command counts a command; past MaxCommands the session closes.
func (ss *session) command() error {
	k := ss.k
	k.mu.Lock()
	k.commands++
	over := k.commands > ss.s.limits.Commands
	k.mu.Unlock()
	if over {
		k.closeSoon()
		return errTooMuch
	}
	return nil
}

// refuse counts a refusal of the session's own; past MaxFailedCommands the
// session closes instead.
func (ss *session) refuse(err *smtp.SMTPError) error {
	k := ss.k
	k.mu.Lock()
	k.failed++
	over := k.failed > ss.s.limits.Failed
	k.mu.Unlock()
	if over {
		k.closeSoon()
		return errTooMuch
	}
	return err
}

// Reset implements smtp.Session: RSET, a second EHLO, and the end of every
// transaction. The session is idle until its next MAIL FROM.
func (ss *session) Reset() {
	ss.from, ss.mailboxes = "", nil
	k := ss.k
	k.mu.Lock()
	afterData := k.afterData
	k.afterData, k.txEnd = false, time.Time{}
	k.mu.Unlock()
	if !afterData {
		_ = ss.command()
	}
	k.mu.Lock()
	closing := !k.readCap.After(time.Now())
	k.mu.Unlock()
	if !closing && ss.encrypted() {
		k.capRead(time.Now().Add(ss.s.limits.Idle))
	}
}

// Logout implements smtp.Session.
func (ss *session) Logout() error { return nil }

// Mail implements smtp.Session.
func (ss *session) Mail(from string, _ *smtp.MailOptions) error {
	if err := ss.command(); err != nil {
		return err
	}
	if !ss.encrypted() {
		// Whoever sits between the sender and the enclave (the parent instance
		// included) could read a message sent without TLS.
		return ss.refuse(errNoTLS)
	}
	k := ss.k
	k.mu.Lock()
	k.transactions++
	over := k.transactions > ss.s.limits.Transactions
	k.txEnd = time.Now().Add(ss.s.limits.Transaction)
	txEnd := k.txEnd
	k.mu.Unlock()
	if over {
		k.closeSoon()
		return errTooMuch
	}
	k.capRead(txEnd)
	ss.from = from
	return nil
}

// Rcpt implements smtp.Session.
func (ss *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if err := ss.command(); err != nil {
		return err
	}
	ctx, cancel := ss.deadline(ss.s.limits.Command)
	defer cancel()
	id, err := ss.s.rcpts.Accept(ctx, to)
	switch {
	case errors.Is(err, mailboxes.ErrNoSuchMailbox):
		return ss.refuse(errNoMailbox)
	case errors.Is(err, mailboxes.ErrSlowDown):
		return ss.refuse(errSlowDown)
	case err != nil:
		ss.s.log.Warn("smtpd: a recipient could not be looked up")
		return ss.refuse(errTemporary)
	}
	for _, m := range ss.mailboxes {
		if m == id {
			return nil // two addresses of one mailbox are one
		}
	}
	ss.mailboxes = append(ss.mailboxes, id)
	return nil
}

// deadline is a context that ends after d, and no later than the
// transaction's deadline.
func (ss *session) deadline(d time.Duration) (context.Context, context.CancelFunc) {
	end := time.Now().Add(d)
	ss.k.mu.Lock()
	if !ss.k.txEnd.IsZero() {
		end = earliest(end, ss.k.txEnd)
	}
	ss.k.mu.Unlock()
	return context.WithDeadline(context.Background(), end)
}

// Data implements smtp.Session: for DATA, and for the first BDAT chunk of a
// transaction, whose chunks go-smtp hands over through a pipe. Bytes are
// taken from the budget as they arrive, and every byte taken is given back
// when it returns, whatever ends it.
func (ss *session) Data(r io.Reader) (err error) {
	k := ss.k
	if err := ss.command(); err != nil {
		return err
	}
	k.mu.Lock()
	k.afterData = true
	k.inData = true
	txEnd := k.txEnd
	k.mu.Unlock()
	steps, whole := 0, false
	defer func() {
		k.mu.Lock()
		k.inData = false
		k.mu.Unlock()
		ss.s.give(steps)
		if !whole && err != nil && !errors.Is(err, smtp.ErrDataReset) {
			// Closing, never draining: go-smtp's read of the rest of the
			// message fails at once.
			k.closeSoon()
		}
	}()
	stall := func() { k.capRead(earliest(txEnd, time.Now().Add(ss.s.limits.DataStall))) }
	stall()
	admit, cancel := ss.deadline(ss.s.limits.Transaction)
	defer cancel()
	var buf bytes.Buffer
	chunk := make([]byte, 32<<10)
	for {
		room := int64(steps)*ss.s.limits.DataStep - int64(buf.Len())
		if room == 0 {
			if int64(buf.Len()) >= ss.s.limits.MaxMessage {
				// One byte past the largest message is enough to refuse it.
				room = 1
			} else {
				if !ss.s.take(admit) {
					return errNoRoom
				}
				steps++
				continue
			}
		}
		n, rerr := r.Read(chunk[:min(int64(len(chunk)), room)])
		if n > 0 {
			buf.Write(chunk[:n])
			if int64(buf.Len()) > ss.s.limits.MaxMessage {
				return errTooLarge
			}
			stall()
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			var smtpErr *smtp.SMTPError
			switch {
			case errors.Is(rerr, smtp.ErrDataReset):
				return rerr // the transaction was abandoned: RSET, QUIT, a lost connection
			case errors.As(rerr, &smtpErr) && smtpErr.Code == 552:
				return errTooLarge
			case errors.As(rerr, &smtpErr):
				return rerr
			case errors.Is(rerr, smtp.ErrTooLongLine):
				return errLongLine
			}
			return errStalled
		}
	}
	k.mu.Lock()
	k.inData = false
	k.mu.Unlock()
	if !time.Now().Before(txEnd) {
		return errStalled
	}
	whole = true
	// The delivery: one minute from the last byte, within the transaction.
	k.capRead(time.Now().Add(ss.s.limits.Ingest + ss.s.limits.Command))
	ctx, cancelIngest := ss.deadline(ss.s.limits.Ingest)
	defer cancelIngest()
	if err := ss.s.rcpts.Deliver(ctx, ss.from, ss.mailboxes, buf.Bytes()); err != nil {
		if !errors.Is(err, mailboxes.ErrNotAllStored) {
			ss.s.log.Warn("smtpd: a message could not be stored")
		}
		return errTemporary
	}
	return nil
}
