package accounts

// SetMailboxes gives accounts the mailboxes service, once, before the
// service serves anything: the two refer to each other, so neither can be
// made with the other (ADR 0026, "The composition").
//
// Pull request 1 of ADR 0026 adds this method next to Config.Mailboxes; this
// file stands in for it until that pull request is merged, and is deleted
// then.
func (s *Service) SetMailboxes(m Mailboxes) { s.cfg.Mailboxes = m }
