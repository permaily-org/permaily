package main

import (
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// What the operator and the admins are given of mailboxes (ADR 0026): both
// wrappers of accounts are what the routes take, and accounts tells people
// by mail of what happened to their mailboxes (the mailboxes service's
// Notifier, given to it where it is made).
var (
	_ httpapi.MailboxOperator = (*accounts.Service)(nil)
	_ httpapi.MailboxOperator = (*accounts.Admin)(nil)
	_ mailboxes.Notifier      = (*accounts.Service)(nil)
)
