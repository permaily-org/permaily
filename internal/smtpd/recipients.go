package smtpd

import (
	"context"
	"time"
)

// Recipients is what the server asks of the mailboxes, from the release that
// builds ADR 0026 on; *mailboxes.Service implements it (pull request 1), and
// pull request 1 replaces Ingester with it. The answers to the sender, and
// the limits on memory and sessions that hold before any of this is asked,
// are ADR 0026's ("Delivery", "Admission").
type Recipients interface {
	// Accept is asked at RCPT TO: the mailbox the address leads to, after
	// reading its pointer and its record, or mailboxes.ErrNoSuchMailbox
	// (550 5.1.1, the same for every refusal), mailboxes.ErrSlowDown
	// (451 4.7.1), or any other error (451 4.3.0). Two recipients that lead
	// to one mailbox are one.
	Accept(ctx context.Context, rcpt string) (mailbox string, err error)
	// Deliver stores raw once in each mailbox accepted for the transaction,
	// reading each record again first, under a context that ends
	// mailboxes.IngestDeadline after the last byte of DATA was read. It
	// returns nil only when every one of them holds the message, stored now
	// or by an earlier delivery of the same message (250); otherwise
	// mailboxes.ErrNotAllStored or another error (451 4.3.0), after storing
	// what it could: the sender's retry stores nothing twice, and a mailbox
	// that stopped accepting meanwhile is refused at its RCPT TO, so the
	// sender learns which.
	Deliver(ctx context.Context, from string, mailboxes []string, raw []byte) error
}

// The admission and the deadlines of a session (ADR 0026, "Admission"),
// built by pull request 1. Every deadline is absolute: go-smtp v0.25.1 sets
// its ReadTimeout before each command line it reads and nothing else, so the
// server wraps each connection, and the deadline go-smtp asks for is cut to
// the earliest of these that applies.
const (
	// MaxSessions is how many SMTP connections an enclave holds at once;
	// one more is answered 421 4.3.2 and closed.
	MaxSessions = 64
	// MaxSessionsPerAddress is how many of them one remote address holds:
	// the enclave sees connections over vsock without the sender's address,
	// so the host's proxy (internal/hostproxy) counts them.
	MaxSessionsPerAddress = 8
	// MaxDataInFlight is how many bytes of DATA an enclave holds at once,
	// all sessions together. Bytes are taken from it as they arrive, in
	// steps of DataStep, before they are buffered: never at MAIL FROM, and
	// never from a declared SIZE, which is only a hint.
	MaxDataInFlight = 128 << 20
	DataStep        = 1 << 20
	// AdmissionWait is how long a step waits for room before the
	// transaction is refused with 452 4.3.1 and the connection closed.
	AdmissionWait = 10 * time.Second

	// TLSDeadline bounds a session from the connection to the end of the
	// TLS handshake (seen as the EHLO that follows STARTTLS).
	TLSDeadline = 30 * time.Second
	// IdleDeadline bounds the time from the end of the handshake to the
	// first MAIL FROM, and from the final answer of a transaction to the
	// next MAIL FROM or QUIT.
	IdleDeadline = 30 * time.Second
	// TransactionDeadline bounds a transaction from MAIL FROM to its final
	// answer, across every RCPT TO, the whole of DATA or every BDAT chunk,
	// and the delivery (mailboxes.IngestDeadline).
	TransactionDeadline = 12 * time.Minute
	// DataStall ends a transaction whose DATA or BDAT brings no byte for
	// this long.
	DataStall = 30 * time.Second
	// CommandTimeout is go-smtp's ReadTimeout and WriteTimeout: one
	// command line, one answer.
	CommandTimeout = time.Minute
	// SessionDeadline bounds a whole session.
	SessionDeadline = 30 * time.Minute
	// MaxTransactions, MaxCommands and MaxFailedCommands bound what one
	// session does: transactions (MAIL FROM), commands the session sees
	// (EHLO, MAIL, RCPT, DATA, BDAT, RSET), and answers 4xx or 5xx of its
	// own. go-smtp closes a session after three syntax errors or unknown
	// commands itself; NOOP, VRFY and HELP reach no hook, read nothing and
	// are bounded by the deadlines alone.
	MaxTransactions   = 20
	MaxCommands       = 200
	MaxFailedCommands = 10
	// CloseGrace is how long a session that is being closed may still
	// write its last answer: reading stops at once.
	CloseGrace = 10 * time.Second
)
