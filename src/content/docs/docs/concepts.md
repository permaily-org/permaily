---
title: Concepts
description: The words Permaily uses everywhere, how rules and queries split the work, and the order every release follows.
---

These words are used everywhere: in the app, the API and the code.

| Term | Meaning |
| --- | --- |
| **Account** | A mailbox connected to Permaily |
| **Connection** | An app, agent or vendor that receives mail through Permaily |
| **Rule** | A filter the person writes, such as "Sign-in and security" or "Home address". It holds back whole emails or removes details from them, for every connection or only some. Rules run first, and connections are never told about them |
| **Query** | What a connection asks for, such as "flights and hotels". The person approves it on the consent screen, and it caps what that connection can fetch. An agent writes its own searches inside a scope the person sets |
| **Access** | What one connection may do: its query, parts, actions and time limits |
| **Delivery** | How a connection receives mail: MCP, API, webhook or forwarding |
| **Receipt** | A record of one release of mail to one connection |
| **Proof** | Evidence that the running service matches the public code |
| **Token** | The billing unit for developers: one processed unit of an email, such as a piece of extracted information. Not the same as an LLM token |

## Rules and queries

Two things decide what a connection gets, and they belong to different people.

<div class="tbl-scroll" tabindex="0">

| | Rules | Queries |
| --- | --- | --- |
| **Written by** | The person. Permaily suggests one | The connection |
| **They say** | What must not leave: sign-in mail, an address, children's names, a car | What the connection wants: flights and hotels, invoices, the headers of everything |
| **Apply to** | Every connection, or the ones the person picks | That one connection |
| **Run** | First, on the whole mailbox | Second, on what the rules left |
| **Seen by** | Only the person | The person, on the connection's page and its consent screen |

</div>

A connection never sees rules. It gets results, and nothing tells it that an email was held back or a detail removed. See [Rules](/docs/rules/) and [Queries](/docs/queries/).

## What an access answers

A permission has to answer four questions, not one. Tidying an inbox shows why: it needs to act on mail, and it never needs to read a message body.

| Question | Options |
| --- | --- |
| **Which emails?** | The connection's query, approved by the person. An agent's own searches stay inside a scope the person sets |
| **Which parts?** | Headers only, body text, attachments, or extracted fields only |
| **What can it do?** | Read by default. Label, archive and draft on request. Send and delete need approval each time |
| **For how long, and how far back?** | New mail only or history from a date, with an expiry or auto-revoke when unused |

An access looks like this:

```yaml
connection: trip-app
account: marta.ruiz@example.com
query: >
  from:(iberia.com OR booking.com OR renfe.es OR airbnb.com)
  subject:(booking OR reservation OR itinerary)
parts: [headers, body_text]
actions: [read]
history_from: 2026-01-01
expires_in: 90d
```

## How it fits real use cases

Every row runs after the person's rules.

<div class="tbl-scroll" tabindex="0">

| Use case | Query | Which parts | Actions | Mailbox connection needed |
| --- | --- | --- | --- | --- |
| Trip planner | Flights and hotels | Body, or booking fields | Read | Forwarding for new mail, OAuth for history |
| Finance tracker | Invoices and receipts | Body and attachments | Read | Forwarding for new mail, OAuth for history |
| Inbox tidy-up | All mail | Headers only | Label, archive | OAuth only |
| Claude through MCP | Its own searches, inside the kinds of mail the person ticks | Body | Read | Either |
| Company vendor | Set by company policy | Set by company policy | Read | Workspace or Microsoft 365 admin |

</div>

## The order every release follows

Every release passes through these steps, in this order, inside the verified boundary.

1. **Rules that hold back.** If a rule for this connection holds the message back, it isn't released.
2. **Rules that remove.** The details a rule removes are taken out.
3. **Query.** If the message is outside this connection's approved query, it isn't released.
4. **Parts.** Only the approved parts stay.
5. **Marking.** The message is marked as [untrusted content](/docs/untrusted-content/).
6. **Release.** The connection receives it.
7. **Receipt.** The release is [written to the ledger](/docs/receipts/).

A connection can't tell steps 1 and 3 apart. An email a rule held back looks the same as one its query didn't match.

## Connecting a mailbox

There are two ways in, and they decide what an access can ask for.

| Method | Needs | Sees | Can act |
| --- | --- | --- | --- |
| **Forwarding** | No account access. Permaily generates a Gmail filter for the mail that connections' approved queries ask for, so it only receives what matches. Rules apply after the mail arrives | New mail only | No |
| **Google account** | Account connection | History and new mail | Yes, if the access allows it |

Companies connect Google Workspace and Microsoft 365 through an admin.
