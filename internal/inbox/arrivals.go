package inbox

import "time"

// Arrivals is told that a message was stored in a mailbox, once its mark is
// written (ADR 0026, "The sequence", 6), so a connection's webhook can tell
// its vendor soon (ADR 0039, "New mail"). It is a hint and decides nothing:
// it must return at once and never fail or slow the delivery; a hint lost
// (a crash, a full buffer) is found by the webhooks' hourly repair, which
// looks at every mailbox a webhook reads. It is given nothing of the message
// but its mailbox and when it was received. services/webhooks implements it
// (ADR 0039's pull request 3), and services/mailboxes calls it.
type Arrivals interface {
	Arrived(mailbox string, received time.Time)
}
