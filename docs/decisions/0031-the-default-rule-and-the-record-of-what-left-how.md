# 0031 The default rule and the record of what left: how they are built

Status: Proposed. Designed on 2026-11-16 with ADR 0030, for step 5 of F2 (docs/plan.md), on the owner's decision of that day to build both before connections open; not built. Builds on ADR 0019 ("A rule from the start", "Receipts") and ADR 0027 (a rule is one or more formulas, read for a rule); meets ADR 0028's prerequisite for rules that protect relying on kept answers ("Holds that cannot be undone"). Its pull requests are ADR 0030's 4 and 5, in that record's table; its tests are H1 to H18. The choices made without the owner are listed for the design review ("Chosen without an owner's decision").
Date: 2026-11-16

## Context

ADR 0019 promised two things that its order of work left to steps 7 and 8: every new account has a rule, turned on, that holds back sign-in and security mail; and every message that leaves writes a receipt, sealed, kept for as long as the account, later chained by hash with its head anchored. The owner decided on 2026-11-16 (ADR 0030, the owner's decision 1) that connections open to every invitee at once, so both must exist the day they do: the rule holding sign-in codes and password resets for every account, by exact conditions and by `about`, failing toward holding as rules do; and a sealed record of every message that leaves (which connection, which parts, by which query or search, what was held, whether a model decided), refusals too. Both must be shaped so that step 7 (the person's own rules) and step 8 (receipts chained and anchored, the activity screen) extend them without migration where possible.

ADR 0028 left a prerequisite for step 7 (docs/plan.md): before any rule that protects relies on a kept answer of the model, rule-mode answers must be rollback resistant, failing closed when a prior decision cannot be recovered; whoever writes the table can delete or put back a kept answer, which forces a new ask, and for a protecting rule a new ask after a silent change of the model could release a message that had been held. The default rule is such a rule, so the prerequisite is met here.

## Decision in short

- **One rule, compiled into the image, the same for every account**: `rules.Default()` (`default-sign-in-and-security`, version 1), three formulas in `internal/rules/default.json`: the subject contains, and the text contains, any of 21 phrases in English and Spanish (verification, security, sign-in, login and one-time codes, passcodes, two-factor, password resets); and one formula a model decides, `message about` a sign-in or verification code or a one-time password, or resetting a password or recovering an account. It applies when any formula is True or Unknown (`query.ForRule`): every message a connection would read that the exact phrases do not catch is held until the model answers below 0.30 for both topics. ("The default rule")
- **Failing toward holding, plainly**: with no model key, the model's spend limit reached, the provider down, or an answer in doubt, every message is held (the phrases can only hold, never clear), so a connection gets nothing until the model answers. ("When the model cannot answer")
- **Holds that cannot be undone**: a hold that rested on a model's kept answer is committed, before the page that decided it is answered, in the mailbox's strict anchored record of holds for the month the message arrived (`rule-holds-<mailbox>-<YYYYMM>`); a later evaluation that would let the message out on a model's answer reads that record first and holds it again; a record that cannot be read holds every message of its month whose value rests on a model. A kept answer deleted or put back can then only make a held message stay held, which is ADR 0028's prerequisite met. ("Holds that cannot be undone")
- **The record of what left**: one sealed entry per request of a connection that reads mail or is refused, and per consent, written once (at version 0) before the answer, in the account's partition `receipts#<account>`, keyed by a time-ordered identifier, kept for as long as the account, deleted with it. It names the connection, the vendor, every formula by role and canonical identifier (the vendor's search in full), the rules and their versions, each message that left with its parts and, of what left, its senders and subject, each message held and by what, and the model's answers a decision rested on. A request whose receipt cannot be written gives nothing. ("The record of what left")
- **Steps 7 and 8 extend them without migration**: the person's rules are a strict anchored record beside the compiled default, which says whether the default is off; receipts are chained in their keys' order by step 8 from the first one written, the head anchored, nothing rewritten. Until step 8, whoever writes the table can delete a receipt unseen: the stated limit. ("How steps 7 and 8 extend them", "Deferred")

## The default rule

### What it is

`internal/rules/default.json`, read and made canonical by `rules.Default()` and pinned by its canonical identifiers (`TestTheDefaultRuleIsWhatADR0031Says`):

| Formula | Canonical identifier | What it holds |
|---|---|---|
| 1, exact | `qry_ghi2xjhazzz25rgsy6arrif4ke` | The subject contains any of the phrases |
| 2, exact | `qry_dq33uknaplrglzyuws6gcb2uje` | The text (as a connection receives it) contains any of the phrases |
| 3, a model decides | `qry_lh52gob7oeycyjrkqg2lpn2lje` | `message about "a sign-in, login or verification code, or a one-time password"`, or `message about "resetting a password or recovering an account"` |

The phrases, matched as substrings after ADR 0027's normalisation (case, width and invisible characters ignored): "verification code", "security code", "sign-in code", "sign in code", "login code", "log-in code", "one-time code", "one-time password", "one time password", "passcode", "authentication code", "two-factor", "2-step verification", "password reset", "reset your password", "reset password", "código de verificación", "código de seguridad", "código de acceso", "restablecer tu contraseña", "restablecer la contraseña". English is the product's language; Spanish is the language of the first mailboxes' mail; the model's formula covers the rest (an English topic on Spanish mail scored 0.984 in ADR 0028's spike). The list can grow, as a new version.

A rule is one or more formulas and applies when one of them does (ADR 0027, "Rules: any of their criteria"), each read for a rule: True or Unknown applies. Formulas 1 and 2 are exact and settle most security mail without a model; formula 3 is asked only of what they leave. Its two topics are two atoms, asked in one call with the subject and the whole text (ADR 0028), and kept per message like every answer, so a message asked about once is not asked again for another connection, a preview or the next page.

### Where it applies

To every read of every connection, before the connection's formulas (ADR 0030, "What a read gives"): a message it applies to is held, not given, and the vendor is told nothing, a held message being `not_found` like one that does not exist. It applies to the consent screen's preview with the model (`MatchPage.Held`), and not to the owner's own previews of a formula or to the inbox: the rule is about what leaves.

It is on for every account and cannot be turned off in step 5: the switch ADR 0019 promises comes with step 7's rules screen.

### When the model cannot answer

Formula 3 is Unknown until the model answers, and Unknown applies a rule. So when the model has no key, the month's spend or an account's share is reached, the provider does not answer, an answer falls between 0.30 and 0.70, or a message's text cannot be read, every message formulas 1 and 2 do not catch is held. A connection then gets nothing beyond what the model cleared: a phrase that does not match leaves the value Unknown, not False, so with the model unable to answer nothing leaves at all. That is "failing toward holding" taken literally; the consent screen says the rule asks the model, `/status` shows the model's guard, and the connection's page says when mail is being held because the model could not be asked. Each message the model is asked about costs about $0.00013 (ADR 0028's measure), within the month's 50 USD and the account's 0.50 USD a day an enclave; a connection that reads more than that holds the rest until the next day.

## Holds that cannot be undone

### The problem

Answers are kept per message (ADR 0028), and the first kept for a key is the one every enclave uses. Whoever writes the table cannot write one, but can delete one or put back an earlier copy of the entry, which loses the answers added since. The next evaluation then asks again. For a query that is harmless; for the default rule it means that a message held on a kept 0.90 can be asked again, and if the model's behaviour changed under its name in between (which the canary samples and cannot exclude), come back 0.10 and leave. Not keeping rule answers at all would not help: every ask would then be a fresh one, and a held message would be released the same way by the first fresh ask after a change. What has to hold is that **a hold, once decided, is never undone by the storage**.

### The record of holds

Each mailbox has, for each month in which messages arrived (UTC, from the message identifier's time), a strict anchored record `rule-holds-<mailbox>-<YYYYMM>` (ADR 0016, ADR 0026), sealed under a key from the root secret: the list of `{message, rule, version, at, expires}` of the holds committed for messages of that month. It is made with `CreateNew` and changed with `PutRecordAt` at the version read (read again on `store.ErrChanged`, at most five times).

- **When a hold is committed.** When a rule's value on a message rests on a model's kept answer and applies (formula 3 True, or Unknown because its answer fell between 0.30 and 0.70). Not when it is Unknown because no answer was kept (no key, a limit, a failure): that is not a decision, and a later ask decides it. Not when an exact formula held it: exact conditions are read from the sealed meta and entry, which cannot be changed. The commit is made before the page or the read that decided it is answered: a page whose hold cannot be committed holds all its model-dependent messages and gives the rest.
- **When it is read.** Before a message leaves on a model's answer for a rule that protects (formula 3 False on a kept answer), the record of its month is read (from this enclave's read if younger than five seconds, as connections are): a hold of that rule committed for the message holds it, whatever the answer now says.
- **When it cannot be read.** Lost, forked, put back, or past its bound: every message of that month whose rule value rests on a model is held, until the record reads again. A month with more than 5,000 holds in one mailbox (`inbox.HoldsMax`) stops taking new ones, and holds every message of the month that would need one.
- **How long.** A hold expires with its message; later versions drop the expired ones. A month's record stops changing once its messages are all decided; the mailbox's removal (disconnecting it, deleting the account) removes its records with `RemoveRecord`.

**Why monthly.** A strict read lists every version of a record, and fails past 10,000 entries. One record a mailbox would grow by a version a hold, for ever; one a month grows by the holds of that month's messages (a few a day for a person's mail), far below the bound, and old months are read only for old messages.

**What it guarantees.** Whatever whoever writes the table does to kept answers, a message that a rule held on a model's answer stays held for as long as the message exists: deleting the answer leads to a new ask whose "release" is overruled by the committed hold; putting back an earlier record of holds is refused; hiding one (a fork) holds the whole month. What it does not cover: a message never asked before is decided by its first answer, which is the model's and nobody else's; and the first evaluation of a message racing the deletion of its kept answer through another enclave is two asks of one model, the same unless the model changed in that moment. ADR 0028's prerequisite asked for a commitment that tells a decision never made from one lost, failing closed: this is it, for holds, which are the only decisions whose loss could let mail out.

**Step 7** uses it for every rule that protects, the person's own included; nothing in it is the default rule's.

## The record of what left

### What a receipt is

An entry of the account's partition `receipts#<account>`, keyed by its identifier (`rcp_` and a time-ordered identifier, the time first, so the keys' order is the time's), its value sealed (`envelope.SealText`, `permaily receipts v1`, a key from the root secret) and bound to its partition and key, written with `PutEntry` at version 0 (never updated), with no expiry. Its document (`receipts.Receipt`, version 1):

| Kind | When | Holds |
|---|---|---|
| `search` | A page of a search answered | The connection, client, vendor, resource; every formula by role (`consented`, `mailboxes`, `since`, `search`) and canonical identifier, the search's canonical form in full; the rules and versions; each message that left (mailbox, identifier, when it arrived, the parts given, and of those its senders and subject); each message held (mailbox, identifier, rule, version, whether a model decided); the model's answers each decision rested on (atom, model, prompt version, the canary's last check and last change, ADR 0028) |
| `read` | One message read whole | The same for the one message; never its text |
| `attachment` | One attachment given | The message, the attachment's index |
| `refused` | A read or a search that gave none: a held message, one outside the scope (or not there), an attachment without the part, a formula refused | The reason; the message's identifier when one was asked |
| `consent` | The person allowed or refused a request, or renewed a connection | What the screen showed: the sentences, parts, mail since, end, mailboxes, the model's notice's version, what it replaced; the decision |
| `ended` | A connection ended | Why, and what replaced it |

What left is recorded; what did not leave is recorded by identifier only. A receipt keeps a message's senders and subject only when those parts left with it, so the person can still see, once the message is deleted, what a vendor got; never its text or an attachment's content, which would make the record a second copy of the mail. A request refused before a connection is known (no token, a token not valid) has no account to write to: it is counted on `/status` and logged by kind.

### When a receipt is written

Before the answer, and the answer is given only once it is stored: a request whose receipt cannot be written is answered 503 and gives nothing (`receipts.Writer`). One receipt per request, holding every message of it (at most 50 for a search): one write per request, never one per message. Two enclaves write receipts of one account at once without coordinating: each is a new entry, and the time-ordered identifier with 10 random bytes never collides.

### What it guarantees, and until when

Whoever writes the table cannot read a receipt, change one, or make one. Until step 8 they can delete one, or the whole partition, unseen; putting back a deleted one changes nothing (it is the same receipt). The receipts outlive the mail: deleting a message or disconnecting a mailbox leaves them; removing the account deletes the partition (`DeletePartition`), as its other entries are.

## How steps 7 and 8 extend them

- **Step 7, rules.** The person's rules are a strict anchored record of the account (`rules-<account>`, made at the first rule), each a `rules.Rule` with an identifier of its own and its version, beside the compiled default; that record also says whether the default is off, so an account without one has the default on, which is every account of step 5: nothing to migrate. Rules for some connections only are a field of each rule. Removing details is a new effect and a new view of the text (ADR 0028's `TextView`). `Holds` serves every rule that protects. A new version of `default.json` is a new `DefaultVersion`; receipts name the version that applied.
- **Step 8, receipts.** The chain is computed over the entries of `receipts#<account>` in their keys' order, from the first one ever written, each link the hash of the receipt's sealed bytes and the link before; the head is an anchored record of the account, updated as receipts are added. The receipts of step 5 are the chain's start: none is rewritten, which is why each is written once, never updated, and keyed by time. Ordering between two enclaves is the key's, which step 8 may tighten by appending through the head. The activity screen and the export read the same entries; the receipt's fields are what the sample's activity screen (`web/src/pages/activity`) shows.

## Deferred

Turning the default off, the person's own rules, rules for some connections, removing details (step 7); chaining, anchoring the head, the activity screen, the export with CSV guarded against formula injection (step 8). The default rule's phrases in more languages, as the product gains them.

## The contract and the seams

Committed with this design, listed in ADR 0030's "Committed with this design": `internal/rules` (`Rule`, `Effect`, `Hold`, `DefaultID`, `DefaultVersion`, `Default` and `default.json`, its tests), `inbox.Holds` and `inbox.HoldsMax` (`internal/inbox/disclose.go`), and `internal/services/receipts/receipts.go` (`Version`, `Partition`, `Purpose`, `Kind`, `Receipt` and its parts, `Writer`). Pull request 4 implements `Holds` in `internal/services/mailboxes` and uses it in `inbox.Discloser`; pull request 5 writes the receipts service and deletes the partition with the account.

## Tests

H1 to H12 are pull request 4's (with V37 to V40), H13 to H18 pull request 5's.

- **H1.** `Default` reads, is canonical and pinned by identifier; a message with each phrase in its subject, and in its text, is held without a model, in any case and width; a message with none and no answer is held; with both answers below 0.30 it is not; with either between 0.30 and 0.70, or at 0.70 and above, it is (`TestTheDefaultRuleHoldsWhenInDoubt` today, through `Eval` alone).
- **H2.** Through the `Discloser`, every connection read applies the rule before the formulas; a held message is `not_found` to the vendor and in the receipt as held; the rule's atoms and a query's own share one ask and one kept entry.
- **H3.** The model off, at its limit, failing, or a text that cannot be read: every message the phrases do not settle is held; a page says nothing of it to the vendor.
- **H4.** A hold on a kept answer is committed before the page is answered; a hold on no answer (off, limit, failure) is not; an exact hold is not.
- **H5.** The kept answer of a committed hold deleted from the table, the provider then answering 0.05: the message stays held, through either enclave and after a restart.
- **H6.** An earlier copy of the month's record of holds put back (without the hold): refused; every model-dependent message of that month held until it reads.
- **H7.** A forked record of holds (`MemoryCommits.Fork`): every model-dependent message of the month held, through every enclave; messages of other months unaffected.
- **H8.** A record lost (`store.ErrLost`): the same as H7.
- **H9.** A hold that cannot be committed (the write refused at every try): the page holds its model-dependent messages and gives the rest.
- **H10.** Two enclaves committing holds of one month at once: both kept, neither undoing the other.
- **H11.** At `HoldsMax` holds a month, a new hold fails and the month's model-dependent messages are held; expired holds are dropped from later versions.
- **H12.** Disconnecting the mailbox and removing the account remove its records of holds (`RemoveRecord`), and a mailbox made afterwards starts with none.
- **H13.** A receipt of each kind, golden as a document; sealed and bound (another account's partition, another key, a changed byte: none opens); written at version 0 and never updated; no expiry.
- **H14.** Its contents: what left with its parts, senders and subject only when given; what was held by identifier only; never a text or an attachment's content; the search's canonical form; the model's answers with the model, prompt and canary's dates.
- **H15.** A writer that fails: nothing of the request leaves (with V32).
- **H16.** Two enclaves writing receipts of one account at once: all kept, the keys in time order.
- **H17.** Removing the account deletes the partition; deleting a message or disconnecting a mailbox leaves its receipts.
- **H18.** Logs: no receipt's content, identifier or count in any line.

## Options considered

- **No model in the default rule** (phrases only): no spend and no holding when the model is off; but a code in a message that says "Here is your number: 482913" leaves. The owner chose exact conditions and `about`.
- **The phrases as a model's `mentions`**: one atom fewer, but every message would need the model even for the plainest "Your verification code".
- **Not keeping rule answers** (asking afresh every time): as argued in "The problem", a fresh ask after a model's change releases a held message just the same; the commitment of holds is what prevents it.
- **Rule answers in an anchored record per answer**: an S3 commit for every message asked; holds are the only decisions whose loss lets mail out, and are rare.
- **One record of holds per mailbox**: grows without end past the strict read's bound; monthly records do not.
- **A receipt per message**: simpler to show, but 51 writes for a page of 50; one per request holds the same.
- **Receipts without senders and subjects**: less kept of the mail, but after a deletion the person could no longer see what a vendor got, which is what receipts are for (ADR 0019: they outlive the mail).
- **The receipt written after the answer**: faster, but mail could leave without one when the write fails.
- **Chaining now**: needs the head's anchored record updated by every receipt from two enclaves, which is step 8's design; written once and keyed by time, the receipts of step 5 can be chained later as they are.

## Chosen without an owner's decision

1. The phrases (21, English and Spanish) and the two topics; three formulas; version 1.
2. The default rule not applied to the owner's own previews and the inbox; cannot be turned off until step 7.
3. With the model unable to answer, a connection gets nothing at all (the phrases can only hold, never clear): stated on the screen and the connection's page.
4. Holds committed in a strict anchored record per mailbox and month, only for holds that rested on a kept answer, before the answer; a record that cannot be read holding the month's model-dependent messages; 5,000 holds a month.
5. One receipt per request, written before the answer, failing closed; senders and subjects kept only of the parts that left; no text ever.
6. Receipts keyed `rcp_` and a time-ordered identifier, never updated, so step 8 chains them as written.
7. A refusal before an account is known only counted, not recorded.

## What it rests on

- **The model** for formula 3 (ADR 0028's measures: a rule at 0.30 missed 3 of 473 with the whole text), and the phrases for what the model is not asked.
- **Strict anchored records** for the holds (ADR 0016, 0026), with their stated limits.
- **The enclave's own sealed storage** for receipts, with deletion unseen until step 8.
