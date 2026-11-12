# 0018 Mailboxes: forwarding only, several per account

Status: Accepted. Not built beyond the single test mailbox of F1. How it is built: [ADR 0026](0026-mailboxes-per-account-how.md) (proposed 2026-11-12), which keeps the address, the label and the Gmail account sealed where ADR 0017 listed the forwarding address as readable.
Date: 2026-11-11 (decided on 2026-11-09)

## Context

F1 receives the mail of one test mailbox that a Gmail account forwards to. F2 gives each account its own mailboxes. The other way to reach a user's mail, the Gmail API, needs Google's security assessment (CASA) before real users can connect, so it is a later step.

## Decision

- **Forwarding only.** The user forwards all their mail to an address of their own at `inbox.permaily.com`, random, one per mailbox. Rules and queries are applied inside the enclave; nothing is filtered at the user's provider.
- **Several mailboxes per account.**
- **No mail from before.** Permaily sees mail from the day the forwarding starts, and the app says so.
- **Retention** as F1 has it: ninety days unless the user picks 7, 30, 180 or 365.
- **A full inbox in the app:** browse, search and delete what is stored.
- **Disconnecting a mailbox deletes its mail at once:** the address stops accepting, and the mailbox's key is destroyed. Receipts of what left are kept (ADR 0019).
- **Which account is behind a mailbox.** For Gmail it comes from Google's forwarding confirmation, signed by `google.com`, so it is verified. For any other provider the user types a label, shown as not verified.

## Consequences

- F1 as built already matches this, so the test mailbox becomes the first mailbox of the first account without a change of format.
- The mailbox's state is overwritten in place today. Before a mailbox holds anything whose going back matters, it is stored through `store.Anchored` (ADR 0016).
- A deleted message put back in storage, or one removed, is not something a commit per message can show: that is the audit log's job (ADR 0019).
