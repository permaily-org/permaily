package connections

import (
	"context"
	"slices"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/accountrules"
)

// Disclosure is the inbox.Disclosure a read of connection would apply now,
// built in one place for reads and for delivery (ADR 0040, "The seam with
// delivery"; ADR 0039): the connection's consented query, mailboxes and mail
// since, the rules its client applies (accountrules.Source, the default
// first), its parts, the account and the model allowed. Delivery notifies
// only on an inbox.Discloser outcome that is Disclosed with it, so a notice
// never tells a vendor of a message a rule holds. The Applying answered is
// the rules read and the version of the account's record they were read at
// (webhooks.Basis). It reads the connection's record strictly and now:
// ErrInvalidToken when it is not account's or not live,
// contracts.ErrModelUnavailable when the model cannot be asked for it (it
// was approved under another notice, or the guard holds the account), and
// ErrUnavailable (with accountrules.ErrUnavailable) when its record or the
// account's rules cannot be read: nothing is then notified.
func (s *Service) Disclosure(ctx context.Context, account, connection string) (inbox.Disclosure, accountrules.Applying, error) {
	d, _, _, err := s.readConnAt(ctx, connection)
	if err != nil && !refusedRecord(err) {
		return inbox.Disclosure{}, accountrules.Applying{}, ErrUnavailable
	}
	if err != nil || !d.live(s.now()) || d.Account != account {
		return inbox.Disclosure{}, accountrules.Applying{}, ErrInvalidToken
	}
	sc, err := d.scope()
	if err != nil {
		return inbox.Disclosure{}, accountrules.Applying{}, ErrInvalidToken
	}
	var fs []*query.Canonical
	for _, f := range []*query.Canonical{sc.Query, sc.Mailboxes, sc.Since} {
		if f != nil {
			fs = append(fs, f)
		}
	}
	ap, err := s.rulesFor(ctx, account, d.ClientID)
	if err != nil {
		return inbox.Disclosure{}, accountrules.Applying{}, err
	}
	if d.ModelNotice != s.cfg.ModelNotice || s.cfg.Model == nil || s.cfg.Model.Gate(ctx, account) != inbox.AskAnswered {
		return inbox.Disclosure{}, ap, contracts.ErrModelUnavailable
	}
	return inbox.Disclosure{Formulas: fs, Rules: ap.Rules, Parts: slices.Clone(d.Parts), Account: account, Model: true}, ap, nil
}
