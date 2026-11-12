package smtpd

import "context"

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
	// reading each record again first. It returns nil only when every one of
	// them holds the message, stored now or by an earlier delivery of the
	// same message (250); otherwise mailboxes.ErrNotAllStored or another
	// error (451 4.3.0), after storing what it could: the sender's retry
	// stores nothing twice, and a mailbox that stopped accepting meanwhile
	// is refused at its RCPT TO, so the sender learns which.
	Deliver(ctx context.Context, from string, mailboxes []string, raw []byte) error
}
