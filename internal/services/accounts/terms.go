package accounts

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Agreeing to the terms (ADR 0017). Making an account takes the person's
// agreement to the terms of service and the privacy policy in force, which
// the account's record keeps: which version, and when. A request names the
// version the person was shown, and one that is not the version in force is
// refused, so nobody agrees to a text they were not shown.
//
// When the version changes, an account whose person agreed to an earlier
// one, or to none, changes nothing (change, and the starts of what change
// does not cover) until they agree to the new one. What stays open is what a
// person needs to agree, to leave or to see where they are: reading their
// account and what it has, signing out and signing a device out, confirming
// it's them, agreeing, and deleting the account or cancelling that.
//
// The agreement given at the start of a sign-up travels with its invitation,
// sealed in the storage with the sign-in in progress (Agreed in attemptDoc,
// tripDoc and unconfirmedDoc, pending.go) and never in one enclave's memory,
// so whichever enclave ends the sign-up writes it into the record when the
// account is made.

// termsDoc is, in an account's record, the terms its person agreed to.
type termsDoc struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// agreed reports whether the account's person agreed to that version.
func (d *accountDoc) agreed(version string) bool {
	return d.Terms != nil && d.Terms.Version == version
}

// agreedVersion is the version the account's person agreed to, "" for none.
func (d *accountDoc) agreedVersion() string {
	if d.Terms == nil {
		return ""
	}
	return d.Terms.Version
}

// agreedTo is the agreement in the record as the operator and the admins see it.
func (d *accountDoc) agreedTo() *contracts.TermsAgreement {
	if d.Terms == nil {
		return nil
	}
	return &contracts.TermsAgreement{Version: d.Terms.Version, At: d.Terms.At}
}

// agreement is the agreement a sign-up gives with an invitation: needed, and
// to the version in force. Without an invitation nothing is made, and nothing
// is asked.
func (s *Service) agreement(invitation, version string) (termsDoc, error) {
	if invitation == "" {
		return termsDoc{}, nil
	}
	if version != s.terms() {
		return termsDoc{}, contracts.ErrTermsNeeded
	}
	return termsDoc{Version: version, At: s.now()}, nil
}

// agreeing refuses what an account changes while its person has not agreed
// to the terms in force: for the starts of a change, which change itself
// refuses too.
func (s *Service) agreeing(doc *accountDoc) error {
	if !doc.agreed(s.terms()) {
		return contracts.ErrTermsChanged
	}
	return nil
}

// AgreeTerms records that the person behind a session agreed to the terms in
// force, which in.Version names. Agreeing again changes nothing.
func (s *Service) AgreeTerms(ctx context.Context, who *Authenticated, in contracts.TermsAgree) (*contracts.Account, error) {
	current := s.terms()
	if in.Version != current {
		// The account exists: it is told to agree to go on, not to make one.
		return nil, contracts.ErrTermsChanged
	}
	at := s.now()
	var view contracts.Account
	_, err := s.changeAgreed(ctx, who.AccountID, false, func(doc *accountDoc) error {
		if doc.agreed(current) {
			view = s.view(doc)
			return errUnchanged
		}
		doc.Terms = &termsDoc{Version: current, At: at}
		view = s.view(doc)
		return nil
	})
	switch {
	case errors.Is(err, errUnchanged):
	case err != nil:
		return nil, err
	default:
		s.cfg.Log.Info("accounts: terms agreed", "version", current)
	}
	return &view, nil
}
