package accounts

import (
	"context"
	"errors"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Agreeing to the model's notice (ADR 0028, "Consent"). Asking the model
// about a person's mail sends its subject, and for conditions on the whole
// message its text, to OpenAI: that runs only on the person's agreement to
// the model's notice in force, recorded in the account's record as the terms
// are (terms.go), with which version and when. A request names the version
// the person was shown, and one that is not the version in force is refused,
// so nobody agrees to a text they were not shown. The record changes through
// change, so the agreement counts through either enclave from the moment it
// is stored, and an account that is blocked, closing or has not agreed to the
// terms in force cannot agree (ADR 0026's standings).

// modelNotice is the version of the model's notice in force.
func (s *Service) modelNotice() string { return s.cfg.ModelNotice() }

// noticeAgreed reports whether the account's person agreed to that version
// of the model's notice.
func (d *accountDoc) noticeAgreed(version string) bool {
	return d.ModelNotice != nil && d.ModelNotice.Version == version
}

// noticeVersion is the version of the model's notice the account's person
// agreed to, "" for none.
func (d *accountDoc) noticeVersion() string {
	if d.ModelNotice == nil {
		return ""
	}
	return d.ModelNotice.Version
}

// AgreeModelNotice records that the person behind a session agreed to the
// model's notice in force, which in.Version names: another version is
// contracts.ErrModelNoticeNeeded. Agreeing again changes nothing.
func (s *Service) AgreeModelNotice(ctx context.Context, who *Authenticated, in contracts.TermsAgree) (*contracts.Account, error) {
	current := s.modelNotice()
	if in.Version != current {
		return nil, contracts.ErrModelNoticeNeeded
	}
	at := s.now()
	var view contracts.Account
	_, err := s.change(ctx, who.AccountID, func(doc *accountDoc) error {
		if doc.noticeAgreed(current) {
			view = s.view(doc)
			return errUnchanged
		}
		doc.ModelNotice = &termsDoc{Version: current, At: at}
		view = s.view(doc)
		return nil
	})
	switch {
	case errors.Is(err, errUnchanged):
	case err != nil:
		return nil, err
	default:
		s.cfg.Log.Info("accounts: model notice agreed", "version", current)
	}
	return &view, nil
}

// ModelNoticeAgreed reports whether the account behind a session agreed to
// the model's notice in force, read from its record as it is now: what the
// matches route asks before it lets the model be asked about the account's
// mail (contracts.MatchRequest.AskModel). An account blocked or gone is
// contracts.ErrUnauthorized.
func (s *Service) ModelNoticeAgreed(ctx context.Context, who *Authenticated) (bool, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return false, err
	}
	return doc.noticeAgreed(s.modelNotice()), nil
}
