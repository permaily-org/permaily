---
title: Concepts
description: The words Permaily uses everywhere, what an access answers, and the order every release follows.
---

These words are used everywhere: in the app, the API and the code.

| Term | Meaning |
| --- | --- |
| **Account** | A mailbox connected to Permaily |
| **Connection** | An app, agent or vendor that receives mail through Permaily |
| **Rule** | A named definition of which emails match, such as "Flights and hotels" |
| **Pack** | A community-maintained, versioned rule, such as `travel.bookings` |
| **Access** | What one connection may do: its rules, parts, actions and time limits |
| **Never share** | A list that overrides every rule and every access |
| **Delivery** | How a connection receives mail: MCP, API, webhook or forwarding |
| **Receipt** | A record of one release of mail to one connection |
| **Proof** | Evidence that the running service matches the public code |
| **Token** | The billing unit for developers: one processed unit of an email, such as a piece of extracted information. Not the same as an LLM token |

## What an access answers

A permission has to answer four questions, not one. Tidying an inbox shows why: it needs to act on mail, and it never needs to read a message body.

| Question | Options |
| --- | --- |
| **Which emails?** | One or more rules or packs |
| **Which parts?** | Headers only, body text, attachments, or extracted fields only |
| **What can it do?** | Read by default. Label, archive and draft on request. Send and delete need approval each time |
| **For how long, and how far back?** | New mail only or history from a date, with an expiry or auto-revoke when unused |

An access looks like this:

```yaml
connection: trip-app
account: me@gmail.com
messages:
  pack: travel.bookings@3
  add:
    from_domain: [norwegian.com]
parts: [headers, body_text]
actions: [read]
history_from: 2026-01-01
expires_in: 90d
```

## How it fits real use cases

<div class="tbl-scroll" tabindex="0">

| Use case | Which emails | Which parts | Actions | Mailbox connection needed |
| --- | --- | --- | --- | --- |
| Trip planner | Flights and hotels | Body, or booking fields | Read | Forwarding for new mail, OAuth for history |
| Finance tracker | Invoices and receipts | Body and attachments | Read | Forwarding for new mail, OAuth for history |
| Inbox tidy-up | Everything except Never share | Headers only | Label, archive | OAuth only |
| Claude through MCP | Whatever the person picks | Body | Read | Either |
| Company vendor | Set by company policy | Set by company policy | Read | Workspace or Microsoft 365 admin |

</div>

## The order every release follows

Every release passes through these steps, in this order, inside the verified boundary.

1. **Never share.** If the message is on the list, it's blocked.
2. **Rule.** If it doesn't match this connection's rule, it's blocked.
3. **Parts.** Only the approved parts stay.
4. **Marking.** The message is marked as [untrusted content](/docs/untrusted-content/).
5. **Release.** The connection receives it.
6. **Receipt.** The release is [written to the ledger](/docs/receipts/).

## Never share

This is on by default and sits above everything. It blocks:

- Password resets, login links and one-time codes
- Security alerts
- Anything the person adds: someone they know, a domain, a label or a search

The reason is account takeover. Any connection that can read a password reset can take over the account it belongs to. A sloppy rule or a compromised connection shouldn't be able to reach those messages.

## Connecting a mailbox

There are two ways in, and they decide what an access can ask for.

| Method | Needs | Sees | Can act |
| --- | --- | --- | --- |
| **Forwarding** | No account access. Permaily generates a Gmail filter for the mail its rules cover, so it only receives what matches | New mail only | No |
| **Google account** | Account connection | History and new mail | Yes, if the access allows it |

Companies connect Google Workspace and Microsoft 365 through an admin.
