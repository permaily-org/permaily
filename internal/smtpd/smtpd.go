// Package smtpd is the MX for inbox.permaily.com, inside the enclave. It
// offers STARTTLS with the enclave's own certificate and refuses mail on a
// connection without it, so no message reaches the mailbox in clear text
// (MTA-STS makes senders require that certificate). It accepts mail only for
// the mailbox and hands each message to the inbox. It never logs addresses or
// content.
package smtpd

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/permaily-org/permaily/internal/inbox"
)

// Ingester stores one message.
type Ingester interface {
	Accepts(rcpt string) bool
	Ingest(ctx context.Context, from string, rcpts []string, raw []byte) (string, error)
}

// Config configures the server.
type Config struct {
	Domain          string // greeting name, e.g. inbox.permaily.com
	TLS             *tls.Config
	MaxMessageBytes int64
	MaxRecipients   int
}

// New returns an SMTP server for in.
func New(in Ingester, cfg Config, log *slog.Logger) *smtp.Server {
	s := smtp.NewServer(&backend{in: in, log: log})
	s.Domain = cfg.Domain
	s.TLSConfig = cfg.TLS
	s.MaxMessageBytes = cfg.MaxMessageBytes
	if s.MaxMessageBytes == 0 {
		s.MaxMessageBytes = inbox.MaxMessageBytes
	}
	s.MaxRecipients = cfg.MaxRecipients
	if s.MaxRecipients == 0 {
		s.MaxRecipients = 10
	}
	s.ReadTimeout = 2 * time.Minute
	s.WriteTimeout = 2 * time.Minute
	s.MaxLineLength = 4000
	s.AllowInsecureAuth = false
	return s
}

type backend struct {
	in  Ingester
	log *slog.Logger
}

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &session{b: b, encrypted: func() bool { _, ok := c.TLSConnectionState(); return ok }}, nil
}

type session struct {
	b         *backend
	encrypted func() bool
	from      string
	rcpts     []string
}

func (s *session) Reset()        { s.from, s.rcpts = "", nil }
func (s *session) Logout() error { return nil }

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	if !s.encrypted() {
		// Whoever sits between the sender and the enclave (the parent instance
		// included) could read a message sent without TLS.
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Must issue a STARTTLS command first"}
	}
	s.from = from
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !s.b.in.Accepts(to) {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such mailbox"}
	}
	s.rcpts = append(s.rcpts, to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		if errors.Is(err, smtp.ErrDataTooLarge) {
			return err
		}
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Could not read message"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := s.b.in.Ingest(ctx, s.from, s.rcpts, buf.Bytes()); err != nil {
		if errors.Is(err, inbox.ErrUnknownRecipient) {
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such mailbox"}
		}
		s.b.log.Error("smtpd: store message", "err", err)
		// Temporary failure: the sender retries later; nothing is lost.
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Temporary failure, try again later"}
	}
	return nil
}
