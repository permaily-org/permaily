package accounts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
)

// Notices by mail (ADR 0017): a person is told of security and access, and of
// nothing else. A sign-in from a device that had not signed in before, a
// change in how the account is signed into, a change of the contact address,
// a deletion asked or cancelled, and the operator blocking or unblocking the
// account. They cannot be turned off.
//
// A notice is plain text, from the same sender and through the same Mailer
// as a sign-in message, so the watch of ADR 0021 applies to it. Its subject
// is fixed words. It never holds a code, a link, a token or an identifier,
// and nothing a person typed: not a name, not an address. Not even what the
// browser of a sign-in said of itself, which whoever signs in chooses: a
// notice names that browser from fixed words (domain.DeviceName).
//
// A notice never fails what caused it, and holds its answer back for
// noticeTimeout at most: it is sent after the change is stored, and one that
// cannot be sent is logged, by its kind alone.

// The kinds of notice, as they are logged when one cannot be sent.
const (
	noticeNewDevice         = "new_device"
	noticePasskeyAdded      = "passkey_added"
	noticePasskeyRemoved    = "passkey_removed"
	noticeProviderLinked    = "provider_linked"
	noticeProviderUnlinked  = "provider_unlinked"
	noticePasskeyOnlyOn     = "passkey_only_on"
	noticePasskeyOnlyOff    = "passkey_only_off"
	noticeRecoveryCodesNew  = "recovery_codes_new"
	noticeRecoveryCodeUsed  = "recovery_code_used"
	noticeAddressChanged    = "address_changed"
	noticeDeletionAsked     = "deletion_asked"
	noticeDeletionCancelled = "deletion_cancelled"
	noticeBlocked           = "blocked"
	noticeUnblocked         = "unblocked"
)

// supportAddress is where a person writes when they cannot help themselves.
const supportAddress = "support@permaily.com"

// notify sends the messages of one notice, in order, and returns when they
// are sent or when noticeWait has passed, whichever is first. It returns
// nothing: what caused the notice is done already.
func (s *Service) notify(ctx context.Context, kind string, mails ...Mail) {
	select {
	case s.noticing <- struct{}{}:
	default:
		s.cfg.Log.Warn("accounts: too many notices being sent, one was dropped", "kind", kind)
		return
	}
	// The request may end before the notice is sent, and the wait ends the
	// sending too: a mail system that does not answer holds nothing.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.noticeWait)
	done := make(chan sending, 1)
	s.work.Add(1)
	go func() {
		defer func() { cancel(); <-s.noticing; s.work.Done() }()
		how := noticeSent
		for _, m := range mails {
			// Not the error: a mail system's errors carry the address.
			if s.cfg.Mailer.Send(ctx, m) != nil {
				how = noticeFailed
			}
		}
		// Only the wait ends ctx before this goroutine does: a send that
		// failed once it was over was not given the time.
		if how == noticeFailed && ctx.Err() != nil {
			how = noticeLate
		}
		done <- how
	}()
	switch settle(done, ctx.Done()) {
	case noticeFailed:
		s.cfg.Log.Warn("accounts: a notice could not be sent", "kind", kind)
	case noticeLate:
		s.cfg.Log.Warn("accounts: a notice was not sent in time", "kind", kind)
	}
}

// sending is how the sending of a notice ended.
type sending int

const (
	noticeSent sending = iota
	noticeFailed
	noticeLate
)

// settle returns how a sending ended, or noticeLate when the wait is over
// first. The sending ends the wait too, after it says how it ended: when the
// end of the wait is seen with the answer there, the answer is what counts.
func settle(done <-chan sending, over <-chan struct{}) sending {
	select {
	case how := <-done:
		return how
	case <-over:
		select {
		case how := <-done:
			return how
		default:
			return noticeLate
		}
	}
}

// when writes a time the way a notice shows one.
func when(t time.Time) string { return t.UTC().Format("2 January 2006, 15:04 UTC") }

// notWho is what a notice says to do when the person did not do what it
// tells of, and can still sign in.
func notWho(app string) string {
	return "If it was not you, sign in at " + app + ", open Settings, sign out the devices you do not know and remove every way of signing in that is not yours. If you cannot sign in, write to " + supportAddress + "."
}

// noticeMail writes a notice: what happened, the facts of it one a line, and
// what to do.
func noticeMail(to, subject, what string, facts [][2]string, after ...string) Mail {
	var b strings.Builder
	b.WriteString(what + "\n\n")
	for _, f := range facts {
		fmt.Fprintf(&b, "    %s: %s\n", f[0], f[1])
	}
	for _, p := range after {
		b.WriteString("\n" + p + "\n")
	}
	return Mail{To: to, Subject: subject, Text: b.String()}
}

const itWasYou = "If it was you, there is nothing to do."

// browserNotRecognised is what a notice says of a browser it cannot name.
const browserNotRecognised = "not recognised"

// newDeviceMail tells of a sign-in from a device that had not signed in to
// the account before: when, how, and which browser, named as the list of
// sessions names it. Nothing else of what the browser said is repeated.
func newDeviceMail(to, app string, at time.Time, how way, device string) Mail {
	browser := domain.DeviceName(device)
	if browser == "" {
		browser = browserNotRecognised
	}
	return noticeMail(to, "A new device signed in to your Permaily account",
		"A device that had not signed in to your Permaily account before has just signed in to it.",
		[][2]string{{"When", when(at)}, {"How", string(how)}, {"Browser", browser}},
		itWasYou, notWho(app))
}

// The changes in how an account is signed into, each with what its notice says.
var signInChanges = map[string]string{ //nolint:gosec // G101: the words of notices about passkeys, not a credential
	noticePasskeyAdded:     "A passkey was added to your Permaily account. It signs in to the account from now on.",
	noticePasskeyRemoved:   "A passkey was removed from your Permaily account. It no longer signs in to the account.",
	noticePasskeyOnlyOn:    "\"Passkey only\" was turned on for your Permaily account. Email and the providers no longer sign in to it: only its passkeys and its recovery codes do.",
	noticePasskeyOnlyOff:   "\"Passkey only\" was turned off for your Permaily account. Email and the providers linked to it sign in to it again, and its recovery codes no longer work.",
	noticeRecoveryCodesNew: "New recovery codes were made for your Permaily account. The ones it had before no longer work.",
	noticeRecoveryCodeUsed: "A recovery code was used to sign in to your Permaily account. That code no longer works.",
}

// signInChangeMail tells of a change in how the account is signed into.
func signInChangeMail(to, app string, at time.Time, what string) Mail {
	return noticeMail(to, "A change to how you sign in to Permaily", what,
		[][2]string{{"When", when(at)}}, itWasYou, notWho(app))
}

// providerMail tells of a provider linked to the account, or unlinked.
func providerMail(to, app string, at time.Time, p idp.Provider, linked bool) Mail {
	what := "Signing in with " + providerNames[p] + " was removed from your Permaily account. It no longer signs in to the account."
	if linked {
		what = "Signing in with " + providerNames[p] + " was added to your Permaily account. It signs in to the account from now on."
	}
	return signInChangeMail(to, app, at, what)
}

// noticeChange sends the notice of a change in how an account is signed into.
func (s *Service) noticeChange(ctx context.Context, doc *accountDoc, kind string) {
	s.notify(ctx, kind, signInChangeMail(doc.Address, s.rp.ID, s.now(), signInChanges[kind]))
}

// addressMails tell of a change of the contact address: the address it was,
// and the one it is. Neither names the other: whoever reads the old one's
// mail now is not told where the person went.
func addressMails(from, to string, at time.Time) []Mail {
	const subject = "The contact address of your Permaily account changed"
	facts := [][2]string{{"When", when(at)}}
	return []Mail{
		noticeMail(from, subject,
			"The contact address of your Permaily account was changed to another one. This address is no longer the account's: messages about the account, and signing in by email, go to the new address from now on.",
			facts, itWasYou, "If it was not you, write to "+supportAddress+" from this address as soon as you can."),
		noticeMail(to, subject,
			"This address is now the contact address of your Permaily account. Messages about the account, and signing in by email, come here from now on.",
			facts, "If you did not ask for this, write to "+supportAddress+"."),
	}
}

// who names whoever asked for a deletion, or cancelled one, at the start of a
// sentence.
func who(by string) string {
	if by == contracts.ClosingByOperator {
		return "Permaily's operator"
	}
	return "You"
}

// deletionAskedMail tells that the account is closing: who asked, and the day
// it is removed.
func deletionAskedMail(doc *accountDoc, app string, at time.Time) Mail {
	by := doc.closingBy()
	last := "If it was not you, sign in and cancel it, and then sign out the devices you do not know and remove every way of signing in that is not yours. If you cannot sign in, write to " + supportAddress + "."
	if by == contracts.ClosingByOperator {
		last = "To ask why, write to " + supportAddress + "."
	}
	after := []string{"Until then you can sign in at " + app + " and cancel the deletion.", last}
	if doc.blocked() {
		// Only the operator deletes a blocked account: its person is not signed in.
		after = []string{"The account is blocked, so nobody can sign in to cancel the deletion. To ask why, or to ask for the account to be kept, write to " + supportAddress + "."}
	}
	return noticeMail(doc.Address, "Your Permaily account is being deleted",
		who(by)+" asked to delete your Permaily account. It will be removed, with everything in it, on "+when(doc.ClosesAt)+".",
		[][2]string{{"Asked", when(at)}, {"Removed", when(doc.ClosesAt)}}, after...)
}

// deletionCancelledMail tells that the account is kept: who cancelled, and when.
func deletionCancelledMail(to, app string, at time.Time, by string) Mail {
	last := notWho(app)
	if by == contracts.ClosingByOperator {
		last = "To ask why, write to " + supportAddress + "."
	}
	return noticeMail(to, "Your Permaily account is no longer being deleted",
		who(by)+" cancelled the deletion of your Permaily account. The account stays, as it was.",
		[][2]string{{"When", when(at)}}, last)
}

// blockedMail tells that the operator blocked the account, or unblocked it.
func blockedMail(to, app string, at time.Time, blocked bool) Mail {
	facts := [][2]string{{"When", when(at)}}
	if blocked {
		return noticeMail(to, "Your Permaily account was blocked",
			"Permaily's operator blocked your Permaily account. Every device was signed out of it, and nobody can sign in to it while it is blocked.",
			facts, "To ask why, or to ask for it to be unblocked, write to "+supportAddress+".")
	}
	return noticeMail(to, "Your Permaily account was unblocked",
		"Permaily's operator unblocked your Permaily account. You can sign in to it again at "+app+".",
		facts, "To ask why it was blocked, write to "+supportAddress+".")
}
