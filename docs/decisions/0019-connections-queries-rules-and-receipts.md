# 0019 Connections, queries, rules and receipts

Status: Accepted. Not built.
Date: 2026-11-11

## Context

The product is this: an app or an agent asks for some of a person's mail, the person sees exactly what is asked and allows it or not, and from then on only that leaves. This record says what "what is asked" is, how it is shown, how it is delivered, how the person's own rules cut it down, and what is written down of what left. The words are the ones of the web app's design: a vendor (Claude, an app) has connections, and a connection is one approved query on the person's mail.

## Decision

### A query is a formula

- A query is an AND of groups, and each group is an OR of conditions.
- A condition is a variable, an operator and a value: who sent it, the subject, the body, when it arrived, whether it has attachments, and so on, each kind of variable with its own operators. A condition can be negated.
- One operator is `about`: "the body is about invoices". It is the only one a model decides (ADR 0020); every other one is exact. Both kinds mix in one formula.
- **The system writes the sentence.** From a formula, fixed templates give a sentence a person understands, and that sentence is what the person consents to. No model writes it: it must say exactly what the formula does, and it must be testable.
- **The system simplifies.** A formula that comes in is reduced to an equivalent, clearer one by logic alone: duplicates, conditions that contain others, ranges that merge. An `about` condition is a piece that is not touched, because nothing proves that two topics worded differently mean the same.

The same form gives three things for free. What an agent searches is another formula, and what it gets is its search AND the approved scope, which is again such a formula. A rule of the user's is a formula with an effect. And "asking for more needs fresh consent" is the question of whether one formula fits inside another.

### Connections

- **Who may ask.** Anyone: a vendor is identified by its domain, shown on the consent screen. Known ones carry a name and a "verified" mark, from a list kept in the image.
- **Consent.** The screen shows the vendor, the sentence, the parts asked for, until when, and how many messages pass the exact conditions. The full preview, which uses the model when the query has `about`, is computed only when the person asks for it, with a notice that their mail will be analysed. The person accepts or refuses the request as a whole; the one thing they can change is to leave the connection at new mail only.
- **Agents.** The person approves a scope. The agent makes its own searches, and the enclave answers only with what falls inside the scope. Each search is recorded.
- **Read only, by parts.** A connection reads; it changes nothing. The request says which parts it gets: sender and date, subject, body text, attachments. Attachments are off unless asked for and approved.
- **Mail from before.** The vendor asks for new mail only, or mail since a date; new mail only unless the person accepts otherwise.
- **Every connection ends.** Ninety days by default, a year at most, renewed with one action.
- **Delivery, three ways.** On demand, through the API and MCP. On new mail: when a message that matches arrives, Permaily calls the vendor's webhook. On a schedule the vendor asked for and the person approved, in the person's time zone: Permaily runs the query at that time and calls the webhook. A webhook call carries a signed notice that there is something to fetch, never mail: the vendor fetches with its token, through the same checks, leaving the same receipts.

### Rules

- A rule is the person's own filter, applied before any query: it holds a message back, or removes details from it, for every connection, every one but some, or only those picked.
- **Written in plain words.** The person writes what they want; a model turns it into a formula (ADR 0020); the person confirms it by the sentence the system writes from that formula.
- **Removing details.** Whole parts (body, attachments, sender); data with a fixed shape that needs no model (addresses, phone numbers, account and card numbers, numeric codes, links); and by meaning, sentence by sentence (ADR 0020).
- **A rule from the start.** Every new account has one that holds back sign-in and security mail (codes, password resets), turned on; the person can turn it off.
- **When in doubt, nothing leaves.** If a condition cannot be decided, it does not match for a query, and it matches for a rule that protects.

### Receipts

- Every message that leaves writes a receipt: which connection got it, which parts, by which query or search, what a rule removed, and whether a model decided. Refusals are written too.
- Receipts are sealed, and kept for as long as the account exists: they outlive the mail they are about, so that deleting a message or disconnecting a mailbox does not erase the record of what left.
- They are chained by hash, and the head of the chain goes through `store.Anchored` (ADR 0016), so a receipt cannot be dropped or the log put back to an earlier copy without it showing.

### No charge, no limits

The first version is by invitation, free, and without limits per person. A spending alarm on the whole service takes the place of limits.

## What is left to design while building

- The list of variables and operators, and the grammar that vendors send.
- The authorization server (ADR 0007, still waiting for its spike): tokens checked on every request, an audience per resource, how a vendor identifies itself.
- The scheduler and the webhook queue inside the enclave: retries, signatures, what happens when a vendor does not answer.
- The MCP tools and what each one needs.
- How a vendor gets on the verified list.

## Accepted risks

- A model decides `about`, and a model can be wrong: a message can leave that the person would have said was outside, or be held that was inside. The consent screen and the receipt say when a model decided.
- The person cannot narrow a request, only refuse it. A vendor that asks for too much gets a yes or loses the person.
- Without limits per person, one account can spend the service's money until the alarm is seen.
