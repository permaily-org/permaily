# 0027 The query language: how it is built

Status: Proposed. Designed on 2026-11-14 for step 3 of F2 (docs/plan.md); not built. Revised the same day after its first design review and the owner's answers ("Changed after the first design review (2026-11-14)"). Amends ADR 0019 (a second operator a model decides, "`mentions`"; a rule is one or more formulas, "Rules: any of their criteria"), ADR 0020 (how its thresholds read a negated condition, "The bands of a model") and ADR 0026 (what delivery records for a message, "What delivery adds"). The owner decided its six product choices on 2026-11-14 ("The owner's decisions").
Date: 2026-11-14

## Context

ADR 0019 decided what a query is: an AND of groups, each an OR of conditions; a condition is a variable, an operator and a value, and can be negated; `about` is the one operator a model decides (ADR 0020); the system writes the sentence a person consents to from fixed templates, with no model; it simplifies a formula by logic alone and never touches `about`; whether one formula fits inside another decides whether asking for more needs fresh consent; an agent's search AND its approved scope is again a formula; a rule is a formula with an effect; and when in doubt, nothing leaves. It left to design "the list of variables and operators, and the grammar that vendors send".

This record says how, exactly enough that the language, the sentence, the evaluation over stored mail and the routes can be built at the same time by different people against types that are already in the repository ("The contract and the seams"). Steps 4 (the model), 5 (connections, consent, the API and MCP), 7 (rules) and 8 (receipts) build on it; it builds none of them.

It builds on how mail is stored (ADR 0026): a message's item with its sealed meta (`inbox.StoredMeta`), its sealed search entry (`txt#`, `inbox.SearchEntry`: senders, subject, the first 16 KiB of the text, when it arrived), its raw object read under the read budget, message identifiers that start with the time they were received, list and search pages bounded by items read with cursors that skip nothing, the four search slots, thirty search pages a minute per account, and the one-minute deadline of a request. It keeps their rules: nothing that decides anything lives only in one enclave's memory; logs hold nothing of a person's mail, and now nothing of a formula either.

## Decision in short

- **Fifteen variables, nine operators**, each evaluated over what Permaily stores and has authenticated, and each seeing exactly what a connection would be given: the subject as stored, the body as the text a connection receives (up to 256 KiB). Exact: addresses and domains of senders and recipients, DKIM signers, text in the subject, the body and attachment names, attachments and their types, the mailing list, when it was received, its size as delivery sealed it, its mailbox. A model decides `about` and `mentions`. ("Variables and operators")
- **`contains` is a substring**: "invoice" finds "invoices", and "car" finds "scar" (the owner's decision); case, width and invisible characters are ignored, and the sentence says "contains the text". ("Texts, and where they match")
- **Vendors send JSON**, `{"v":1,"all":[{"any":[{"var","op","value","not"}]}]}`, read strictly (no unknown or repeated member), with fixed limits (32 groups, 64 conditions, 8 a model decides), every refusal a fixed reason and a path into the formula, never an echo of a value. No text syntax. The inbox's search `q` stays as it is, apart. ("The grammar vendors send")
- **A formula means a value in three**: true, false, or unknown, in Kleene's logic. Unknown is data that could not be read or was not authenticated, or a model not asked, not answering or unsure. A query delivers only on true; a rule, every one of which protects, applies on true or unknown. A model's answer is kept per message and per condition, never per formula, and one pure function maps it to a value. ("What a formula means")
- **Simplification is a fixed set of rewrites** sound in that logic: duplicates, absorption by implication between conditions, ranges merged, and contradictions and tautologies only where a variable is always known (`received`, `mailbox`). It always ends, and gives one canonical form for every reordering and respelling of a formula, which is identified by a hash. Conditions a model decides are touched only when they are identical. ("Simplification")
- **Containment is proven or refused**: B fits inside A when every group of A is implied by one group of B, condition by condition. Sound in the three-valued logic, quadratic, and "no" whenever it cannot prove "yes", which means fresh consent. A connection that changes to B without consent keeps A, the scope the person consented to, as a formula of its own: delivery evaluates both and takes the lesser value, and nothing simplifies them together. ("Containment")
- **The sentence is a function of the canonical formula** and of fixed context (the language, the account's mailboxes), written from a table of templates per variable, operator and sign, every value shown through one presentation per type that quotes and escapes it. The structured sentence is injective by construction; the plain text, given the escaping, is checked so by a parser in the tests. English now; Spanish is the next table. ("The sentence")
- **Evaluation over stored mail reads the least it can, and classifies a message only once its sealed meta is read**: the item and its meta first, the search entry for the text, the raw message only when a body condition depends on a text the entry cut; the time in message identifiers bounds the scan for `received`; pages are bounded by items read, raw reads (each released before the next) and a soft deadline, and consider each message whole or leave it for the next page. `about` and `mentions` are left to step 4 as "needs a model". ("Evaluation over stored mail")
- **One pure package, `internal/query`**, a layer of its own like `internal/mailhtml` because it normalises with `golang.org/x/text` and `golang.org/x/net/idna`; the contract in `internal/contracts/query.go` and `web/src/lib/api.ts`; the matcher in `internal/inbox`, with the read budget taken one raw read at a time (`inbox.ReadGrants`); two routes of the web app's API, for the owner's own previews. ("The contract and the seams")
- **Five pull requests**: this one (design and seams), then the language core, the sentence and stored mail written at the same time, then the routes. ("Implementation pull requests")

## Variables and operators

### What a variable may be

A variable is in the first version only if it can be evaluated over what Permaily stores and has authenticated, at a cost bounded per message, and if what it looks at is no more than what a connection with every part would receive. So a condition never decides on something that cannot leave, or on something whoever writes the table could have set, and what "the body" means in a query is what "the body" means in what is delivered.

Not in the first version, and why:

- **Gmail's labels, folders, categories, read, starred, spam, trash** (the sample's `in:inbox`, `category:primary`, `-in:spam`, the rule editor's "Label is Family"): forwarding carries none of them (ADR 0018); a mailbox has no state of that kind.
- **The `Date` header**: the sender writes it. `received` is Permaily's own time.
- **`Reply-To`, `Message-ID`, `In-Reply-To`, `References`, threads, any other header**: not stored outside the raw message, or stored but of no use for selecting mail; a condition on any header would need the raw message of every message considered.
- **`Bcc`**: not in a received message.
- **The envelope sender**: after Gmail's forwarding it is Gmail's rewritten address, not the original sender's.
- **The display name of a sender**: anyone writes any name; offering it invites conditions that look like a sender check and are not.
- **Text inside attachments**: not extracted; a document's text would be a second extraction with its own budgets and parsers. A rule that looks for a home address does not see one inside a PDF; docs/web.md and the rule editor say so (step 7).
- **Relative times** (`newer_than:1d`): a connection's "new mail only" or "mail since a date" is a term of the connection (ADR 0019), not of its query; a relative condition would make what a formula means change while nobody changed it, and containment depend on when it is asked. Agents compute dates.
- **What a model extracts** (the sample's "Fields it asks for": `needs_reply`, `amount`, `billing_address`): not a condition, and not decided by ADR 0019 or 0020. It is a separate product decision, outside this language ("Departures from the sample screens").

### The table

"Read from" names the source evaluation reads it from ("Evaluation over stored mail"): the item's identifier and partition (item), the sealed meta (meta), the sealed search entry (entry), the raw message read and opened (raw). The item's identifier and its mailbox are authenticated by the meta, whose seal names both (ADR 0026), and no message is classified before its meta is open ("Which reads"); so "always known" variables are never unknown on a message that is classified. Every other one may be ("What a formula means").

| Variable | Means | Operators | Value | Read from | Always known |
|---|---|---|---|---|---|
| `from_address` | An address of the `From` header | `is` | An address | meta | No |
| `from_domain` | The domain of an address of the `From` header is the value or one of its subdomains | `is` | A domain | meta | No |
| `recipient_address` | An address of the `To` or `Cc` header | `is` | An address | meta | No |
| `recipient_domain` | As `from_domain`, for `To` and `Cc` | `is` | A domain | meta | No |
| `signed_by` | A DKIM signature that passed at delivery has a `d=` that is the value or one of its subdomains | `is` | A domain | meta | No |
| `subject` | The subject as stored (at most 2 KiB, ADR 0026) | `contains` (exact), `about` (model) | A text; a topic | meta | No |
| `body` | The message's text as a connection receives it: the text/plain part, or the text taken out of the HTML, at most 256 KiB (`inbox.MessageTextMax`) | `contains` | A text | entry, then raw when the entry's text was cut | No |
| `message` | The subject and the text together, as the model is given them | `about`, `mentions` (both model) | A topic | (step 4) | No |
| `attachments` | It has at least one attachment | `exists` | none | meta | No |
| `attachment_name` | The name of an attachment, as the sender wrote it | `contains` | A text | meta | No |
| `attachment_type` | The media type of an attachment, as the message declared it | `is` | `type/subtype` or `type/*` | meta | No |
| `list` | The identifier of its `List-Id` header (RFC 2919) | `is`, `exists` | A list identifier | meta, for mail stored from this step on | No |
| `received` | When Permaily received it: the time in its identifier, which is the claim's `received_at` (ADR 0026) | `since` (on or after), `before` (strictly before) | An RFC 3339 time | item, authenticated by the meta | Yes |
| `size` | The size of the message as delivered, in bytes, as the sealed meta records it | `at_least`, `below` | Bytes | meta, for mail stored from this step on; raw when it is read anyway | No |
| `mailbox` | The mailbox it arrived at | `is` | A mailbox identifier of the account | item, authenticated by the meta | Yes |

`size` is never read from the item's plain `size`: whoever writes the table writes it (`inbox.RawReadMax` says so), and a condition on it would let them turn a 1 KiB message into one "of 1 MB or more". Delivery records the size in the sealed meta from this step on ("What delivery adds"); for a message stored before, `size` is unknown, unless its raw message is read and opened for its body anyway, and then its length is the size. The raw message is never read for its size alone.

A condition on a variable with several values (the addresses of a header, the attachments, the signatures) holds when **one** of them satisfies it, and its negation when **none** does: "not sent from x" means none of the senders is x. Two conditions are never bound to the same value: "an attachment named invoice" and "an attachment of type application/pdf" may hold by two different attachments. That is said in the sentence by its words ("with an attachment whose name…", "with an attachment of type…"), and is the price of keeping every condition one variable, one operator, one value.

`from_address` and `from_domain` say what the message says. Anyone can write a `From` header; `signed_by` is the one variable a sender cannot claim without the domain's key. The sentence of a `from_` condition says "sent from", never "from the company"; the consent screen of step 5 may suggest `signed_by` next to a `from_domain`, which is product work for then.

### Values, and how each is normalised

A value is normalised by `Parse` before anything else uses it, and the field of a message by `Eval` in the same way, so that comparing is comparing bytes. What is refused is refused with `invalid_value` unless said otherwise.

- **Text** (the values of `contains`, and topics), and the fields they are compared with. In order: Unicode NFKC (`golang.org/x/text/unicode/norm`); every default-ignorable code point removed (derived as Unicode does, from the standard library's tables: `Other_Default_Ignorable_Code_Point`, `Cf` and `Variation_Selector`, less `White_Space`, `Prepended_Concatenation_Mark` and U+FFF9 to U+FFFB), which takes out zero-width characters, soft hyphens and every bidirectional control; simple case folding to one member of each orbit of `unicode.SimpleFold`: the lower-case of the orbit's smallest member when that is in the orbit, otherwise the smallest (so `A` and `a` are `a`, `Σ`, `σ` and `ς` are `σ`, `K` and the Kelvin sign are `k`, `ß` and `ẞ` are `ß`, and `İ` and `ı` stay themselves, as the inbox's search keeps them); every run of white space, line and paragraph separators included, made one space, and the ends trimmed. A field's invalid UTF-8 becomes U+FFFD first; a value's is refused. That one round of these steps is a fixed point is checked, not assumed: Q7 runs them over every code point and `FuzzNormalise` over random strings; the normaliser applies them again while they change something, at most three rounds, and a value that would need more is refused (a field that would need more makes its conditions unknown), so its work stays bounded even if a case escaped the checks. A value, after normalisation, has 1 to 100 characters and at most 400 bytes (`value_too_long`), at least one letter or digit, and no control, private-use, surrogate or unassigned code point and no format character left. A word of a value (a run without spaces) whose letters are of more than one of Latin, Greek and Cyrillic is refused (`confusable_value`): those are the scripts whose letters look alike, and a vendor's “invоice” with a Cyrillic `о` would read as “invoice” to the person and match other mail. A value may hold quotation marks and backslashes: the sentence escapes them ("Presenting values").
- **Texts, and where they match** (`contains`). The value matches when its normalised bytes appear anywhere in the normalised field: a substring, by the owner's decision ("The owner's decisions", 3). So `invoice` matches "invoices", "your INVOICE," and "re-invoiced"; `car` matches "scar" and "careful"; `.pdf` matches "bill.pdf"; `Calle Falsa 123` matches it across a line break (white space is one space on both sides); an `in­voice` with a soft hyphen matches `invoice`. Both sides are valid UTF-8 after normalisation, so a match of bytes starts and ends on whole code points. What normalisation keeps apart stays apart: NFKC composes `e` and a combining acute into `é`, so `cafe` does not match "café", and a letter followed by a combining mark that has no composed form (`q` and U+0301) still holds the bare letter, which `q` matches. The field is normalised whole, never piece by piece, which is what the proofs of "Simplification" need: every comparison is between one value's normalised bytes and one field's.
- **Domains** (the values of `from_domain`, `recipient_domain`, `signed_by`, and the domain of an address). A trailing dot removed; then UTS 46 to ASCII with `golang.org/x/net/idna`'s lookup profile (non-transitional, STD3 rules, the hyphen, joiner and bidi checks); lower case; at least two labels, each of at most 63 bytes, at most 253 in all. A label whose Unicode form mixes Latin, Greek and Cyrillic is refused (`confusable_value`). The canonical value is the ASCII form (`xn--…` for a Unicode label). A `signed_by` domain of the message that fails these steps makes every `signed_by` condition unknown for that signature, never false.
- **Addresses** (`from_address`, `recipient_address`). Split at the last `@`; the domain as above; the local part with its quotes and quoting backslashes removed, NFC, then the case folding above, 1 to 64 bytes, printable, with no space or control. A local part whose letters mix Latin, Greek and Cyrillic is refused (`confusable_value`), as a word of a text is. A `+tag` is part of the local part and is kept. **A message's address is decided once, whole**: it is valid when both its local part and its domain pass these steps, and an address that is not valid makes **every** condition over it unknown, on its address and on its domain alike, even when its domain alone would normalise (`"bad local"@other.example` is unknown for `from_domain is example.com`, not false). That one decision is what the implication between an address and its domain rests on ("Simplification").
- **Media types** (`attachment_type`). Lower case; the type one of `application`, `audio`, `font`, `image`, `message`, `model`, `multipart`, `text`, `video`; the subtype `*` or 1 to 127 of `a`–`z`, `0`–`9` and `!#$&^_.+-`. A message's type is cut at its first `;`, trimmed and lowered; an unlisted or malformed one matches only the conditions it equals, never `type/*` of another type.
- **List identifiers** (`list`). The value inside the angle brackets of `List-Id`, lower case, 1 to 255 bytes of the characters RFC 2919 allows. The value is given without brackets.
- **Times** (`received`). RFC 3339 with seconds and an offset (`Z` or `±hh:mm`), no fraction, between 2000-01-01 and 2100-01-01; canonical in UTC, `2006-01-02T15:04:05Z`. Compared with the identifier's time to the nanosecond.
- **Sizes** (`size`). A decimal integer from 1 to 33,554,432 (32 MiB) without sign, leading zero or separator.
- **Mailboxes** (`mailbox`). `mbx_` and 26 base32 characters, and one of the account's mailboxes when the formula is checked for a person or a connection (`unknown_mailbox` otherwise). A mailbox disconnected later stays a valid value: it matches nothing.
- **Topics** (`about`, `mentions`). Text as above, at most 100 characters: short, one line, shown as it is (ADR 0020). Two topics are the same condition only when their normalised bytes are equal; the model is given the normalised topic.

### `mentions`, the second operator a model decides

ADR 0019 named `about` the only operator a model decides. The rule editor's screens use a second one, "mentions" ("Message mentions my home address"), and they are not the same question: a message about a delivery may mention a home address in passing. `mentions` on `message` is a second opaque operator, exactly like `about` for every part of the language: never rewritten, never related to another topic or to `about`, a separate statement to the model. The owner decided it on 2026-11-14 ("The owner's decisions", 2), which amends ADR 0019.

## The grammar vendors send

### The wire form

A formula is one JSON object (`contracts.Formula`):

```json
{"v": 1, "all": [
  {"any": [{"var": "from_domain", "op": "is", "value": "holded.com"},
           {"var": "subject", "op": "contains", "value": "invoice"}]},
  {"any": [{"var": "received", "op": "since", "value": "2026-01-01T00:00:00Z"}]},
  {"any": [{"var": "attachments", "op": "exists", "not": true}]}
]}
```

- `v` is required and is `1`. A later version that adds variables or operators, or changes what one means, gets a new number; this release refuses any other (`unsupported_version`), and a version-1 reader refuses a variable or an operator it does not know (`unknown_variable`, `unknown_operator`), never ignores one: a condition dropped would widen a query.
- `all` is a list of groups and may be empty: the formula then matches every message ("All mail"). A group is `{"any": [...]}` with at least one condition (`empty_group`).
- A condition is `{"var", "op", "value", "not"}`. `value` is always a string, a time and a size included, and is absent for `exists` (`unexpected_value`) and required for every other operator (`missing_value`). `not` is optional, `false` when absent.
- **Read strictly.** One JSON object of at most 64 KiB (`too_large`), UTF-8 without a byte order mark, nothing after it (`not_json`); every member of the right type, never `null` (`wrong_type`); no member this version does not have (`unknown_field`), and no member twice in one object (`duplicate_field`): `encoding/json` keeps the last of two, and a vendor's parser may keep the first, so a formula two readers read differently is refused. The reader is a token walk over `encoding/json`'s `Decoder`, so no other parser is added to the image. Nesting is fixed: a group inside a group is `wrong_type`.

### Limits

| Limit | Value | Why |
|---|---|---|
| The JSON as sent | 64 KiB | Bounds parsing before anything is counted |
| Groups | 32 | |
| Conditions in a group | 32 | |
| Conditions in a formula | 64 | Bounds simplification and containment ("Simplification", "Containment") and the sentence (under 100 KiB, "Presenting values") |
| Conditions a model decides | 8 | Each is a statement put to the model for every message the exact conditions leave open (ADR 0020) |
| A text or a topic | 100 characters, 400 bytes after normalisation | Short enough to read in a sentence and to send to the model |
| An address, a domain, a type, a list | Their own bounds above | |

They are constants of `internal/contracts/query.go`, so the API, MCP's schemas (step 5) and the web app read the same numbers. Formulas evaluated together (a consented scope, a request that fits in it, an agent's search: "What a connection widened without consent is evaluated as") are each within them and are never merged, so a message is put at most 3 × 8 distinct statements of a model.

### Errors

A refused formula is `400` with `{"error": {"code": "invalid_formula", "message": <fixed>, "details": {"at": <path>, "reason": <reason>}}}`. `at` is a path into the formula as sent (`all[2].any[0].value`, or `""` for the whole); `reason` is one of `contracts.FormulaReason`: `not_json`, `too_large`, `unknown_field`, `duplicate_field`, `wrong_type`, `unsupported_version`, `too_many_groups`, `too_many_conditions`, `too_many_model`, `empty_group`, `unknown_variable`, `unknown_operator`, `operator_not_allowed`, `missing_value`, `unexpected_value`, `invalid_value`, `value_too_long`, `confusable_value`, `unknown_mailbox`, `never_matches`. The first problem in the order of the formula as sent is answered, so the answer does not depend on the order checks run in. No error holds a value of the formula, so an error page or a log of one shows nothing a vendor or a person wrote. `invalid_query` stays the inbox search's code.

`never_matches` refuses a formula that simplifies to no message at all (`received since 2026-03-01` and `received before 2026-02-01`): a connection that asks for nothing is a mistake the vendor should hear of. Only `received` and `mailbox` can make a formula "never" ("Simplification"); `size at_least 10` and `size below 5` is accepted, and delivers nothing.

### The canonical form, and its identifier

The canonical form ("Simplification") is a `contracts.Formula` too, written by `encoding/json` from the struct in its field order, with no space, `value` absent for `exists` and `not` absent when false; the same bytes in every enclave and release that reads version 1. Those bytes, and what they mean on a message, are stable across releases: a stored formula, its identifier and its sentence are what a person consented to, so normalisation, the matching of `contains` and the rewrites of version 1 do not change once a formula is stored; changing any of them is a new `v`. Its identifier is `qry_` and the first 16 bytes of its SHA-256 in lower-case base32 without padding (26 characters). The identifier names a formula in receipts (step 8); a model's kept answers are never keyed by it ("Model answers, kept per message and per atom"). It is never logged, since a hash of a short formula can be guessed, and would say what a connection asks for.

### The inbox's search `q`: kept apart

ADR 0026's search is a person's search box: words that must each appear, as substrings folded for case, anywhere in the senders, the subject or the first 16 KiB of the text, in two values. This language matches substrings too, but is what vendors are held to: one variable per condition, its own normalisation (NFKC, invisible characters removed, white space made one), the whole text a connection receives, three values, and a sentence. Unifying them would change what the inbox's search finds, and mapping `q` to a formula would need a variable "any of senders, subject and text" that nothing else wants. So `q` stays as it is, with its code and limits, and the two share code below the line only: `inbox`'s bounded scan with its cursor, which `Match` reuses ("Evaluation over stored mail"). An agent's free-text search through MCP (step 5) is turned into a formula there (a word becomes a group of `subject contains` and `body contains`), in the open, where its sentence shows what it became.

### No text syntax

Vendors send JSON, by the owner's decision ("The owner's decisions", 4). Agents build it through MCP's tool schemas, which step 5 derives from these structs; apps build it in code. A text syntax (`from:holded.com subject:invoice`) would be a second reader to keep strict, with its own quoting and precedence for a vendor to get wrong; the sample's "File" view shows the canonical form instead ("Departures from the sample screens").

## What a formula means

### Three values

On a message, each condition has a value in Kleene's strong logic: false < unknown < true. A group's value is the greatest of its conditions', a formula's the least of its groups'; an empty formula is true, and a formula that is "never" false. A negated condition's value is the reverse of the condition's (true and false swap, unknown stays). Formulas evaluated together take the least of their values.

- An **exact** condition is unknown when the data it needs could not be read or authenticated: a source not read yet ("Evaluation over stored mail"), or that cannot be read (a raw message past its bound), an address of the message that is not valid (its address and its domain conditions alike), a `signed_by` domain that does not normalise, a list of addresses or attachments that a parse limit may have cut (`Partial`) and where the value is not found, a header that could not be read (`Unparsed`), the `list` or the `size` of a message stored before this step, a body condition on a text the entry cut and the raw message not read. It is never unknown when its data was read: "the subject does not contain invoice" is false or true, not unknown. A message whose meta does not open is not evaluated at all: it is listed as unknown, whatever the formula.
- A condition a **model** decides is unknown until the model is asked (step 4), when it does not answer, and when its answer falls in the band of doubt of the mode (`query.Classify`).

### What a query and a rule do with unknown

The value is acted on by mode (`query.Resolve`):

- **For a query** (a connection's query, an agent's search with its scope): a message is delivered only when the value is **true**. Unknown delivers nothing.
- **For a rule**: every rule protects (it holds a message back or removes details of it, ADR 0019), so it applies when the value is **true or unknown**.

That is ADR 0019's "if a condition cannot be decided, it does not match for a query, and it matches for a rule that protects", made exact: it is decided once for the whole formula, and it equals deciding each condition that way on its own, because AND and OR of three values commute with both ways of reading unknown (each is a homomorphism of the lattice false < unknown < true onto false < true). Rules run first, on every message; the query runs on what they left (ADR 0019).

### The bands of a model

ADR 0020 set the thresholds: a query matches `about` at 0.80 or more; a rule that protects holds at 0.30. In three values, with `p` the model's probability that the condition (without its sign) holds, `query.Classify(p, mode)`:

| Mode | True | Unknown | False |
|---|---|---|---|
| Query | `p` ≥ 0.80 | 0.20 < `p` < 0.80 | `p` ≤ 0.20 |
| Rule | `p` ≥ 0.70 | 0.30 ≤ `p` < 0.70 | `p` < 0.30 |

A probability that is not a number, or is outside 0 to 1, is unknown in both modes. The sign is applied after: a query's `about` delivers at 0.80 and its negation only at 0.20 or less; a rule's `about` applies from 0.30 and its negation below 0.70. Each reading is on the side of not delivering, which is what ADR 0020 decided for `about` and which this extends to `not about` and `mentions` (an amendment of 0020: it named no threshold for a negation). `Classify` is committed with this design (`internal/query/query.go`, with Q33), since it is what the logic below must hold for; step 4 asks the model.

### Model answers, kept per message and per atom

An **atom** (`query.Atom`) is a condition a model decides without its sign: its variable, its operator and its normalised topic. Evaluation takes the model's answers for one message as an input (`query.Answers`, a probability by atom): an atom without an answer is unknown and listed in `Result.Model`, each atom once; every condition with that atom, in any group, in any formula evaluated with it, negated or not, reads the same answer. `subject about "x"`, `message about "x"` and `message mentions "x"` are three atoms.

A kept answer (step 4) is keyed by the message, a hash of the exact content the model saw (rules can change what it is given), the atom, the model and the prompt's version: never by a formula. That is what makes "identical conditions are equal" true in containment: if `message about "invoices"` could be answered 0.10 for one formula and 0.90 for another that holds it and more, the larger formula could deliver where the smaller does not, and every containment proof would fail. Formula identifiers go into receipts only.

### Rules: any of their criteria

The rule editor's screens (`web/src/pages/rules/`) match a rule when mail meets **any** of its criteria, and a criteria when mail meets **every** one of its conditions, a condition holding when the field has **any** of its values. That is one formula per criteria: each condition with values `v1…vn` is the group `{v1 or … or vn}`, a negative one is the groups `{not v1}`, …, `{not vn}`; the criteria is the AND of its groups. A rule is then **one or more formulas**, and applies when one of them does: ADR 0019's "a rule is a formula with an effect" becomes "a rule is one or more formulas with an effect" (an amendment, decided by the owner on 2026-11-14, "The owner's decisions", 6). Containment is never asked of a rule; a rule's sentence is its formulas' sentences, introduced by "Any of these:".

## Simplification

### Which relations between conditions it may use

Every rewrite must give the same value on every message in three values, not only in two: a rewrite that holds when every condition is known but not when one is unknown would change what is delivered. So the classical laws are used only where a variable is always known, and between other conditions only what holds in three values.

**A condition implies another** (`ℓ1 ⇒ ℓ2`) when, on every message, the value of `ℓ1` is at most the value of `ℓ2` (false < unknown < true). The pairs it knows, for positive conditions:

| `ℓ1` | `ℓ2` | When |
|---|---|---|
| `from_address is a` | `from_address is b` | `a` = `b` |
| `from_address is a` | `from_domain is d` | the domain of `a` is `d` or under it |
| `from_domain is d1` | `from_domain is d2` | `d1` is `d2` or under it |
| The same three for `recipient_` | | |
| `signed_by is d1` | `signed_by is d2` | `d1` is `d2` or under it |
| `X contains p` | `X contains q`, the same `X` (`subject`, `body`, `attachment_name`) | `q`'s normalised bytes are a substring of `p`'s |
| `attachment_name contains p` | `attachments exists` | always |
| `attachment_type is t1` | `attachment_type is t2` | `t1` = `t2`, or `t2` is `T/*` and `t1` is of type `T` |
| `attachment_type is t` | `attachments exists` | always |
| `list is x` | `list is y` | `x` = `y` |
| `list is x` | `list exists` | always |
| `received since t1` | `received since t2` | `t1` ≥ `t2` |
| `received before t1` | `received before t2` | `t1` ≤ `t2` |
| `size at_least n1`, `size below n1` | the same operator with `n2` | as for `received` |
| any condition | the same condition | always (for a model's conditions, only this) |

And for negated ones, `not ℓ1 ⇒ not ℓ2` exactly when `ℓ2 ⇒ ℓ1`: reversing the values reverses the order. Each pair holds in three values because both sides read the same source and are unknown for the same reasons. For the lists of addresses, each condition is true when one valid entry satisfies it, otherwise unknown when the list may be incomplete or an entry is not valid, otherwise false; an entry is valid or not once, as a whole address ("Addresses"), so on each entry the address test and the domain test are both unknown or both decided, and a test on each entry that implies another implies it on the list. Without that one decision the pair would fail: an address whose local part does not normalise but whose domain does would leave `not from_domain is d` true while `not from_address is a@d` is unknown (Q37). For `size`, both sides read one number, or are both unknown.

**Texts, as substrings.** `X contains p ⇒ X contains q` when `q` is a substring of `p`, both normalised: wherever `p`'s bytes occur in the normalised field, `q`'s occur inside them. Both read the same field and are unknown for the same reasons (a source not read, `Partial` for attachments' names, a cut text). So `contains "invoices"` implies `contains "invoice"`, `contains "scar"` implies `contains "car"`, `contains "invoice 2026"` implies `contains "e 2"`, and `contains "ﬁle"` is `contains "file"` once normalised. The substring is taken between normalised forms only; nothing is proven about texts as the sender wrote them, and nothing needs to be, since evaluation compares normalised forms too.

**Only for the variables that are always known** (`received`, `mailbox`), two-valued facts are used too:

- **Disjoint**: `since s` and `before t` with `t` ≤ `s`; `mailbox is m1` and `mailbox is m2` with `m1` ≠ `m2`; a condition and its negation.
- **Covering**: `before t` or `since s` with `s` ≤ `t`; a condition or its negation.

**Made positive.** `not received since t` is `received before t`, `not received before t` is `received since t`, and the same for `size`'s `at_least` and `below`. For `received` it is two-valued; for `size` it holds in three values too, because both read one number: known, they are exact opposites; unknown, both are unknown. `size` is not always known, so it has no disjoint or covering facts: `size at_least 10 and size below 5` is unknown, not false, on a message whose size was not recorded.

No other relation is used: no two topics, `about` and `mentions`, two variables but an address and its domain, or a condition and its negation outside `received` and `mailbox` (in three values `X or not X` may be unknown, and `X and not X` may be unknown, which a rule acts on).

### The rewrites

`Canonicalise` first normalises every value and makes `received` and `size` conditions positive, then applies these until none applies:

1. **Duplicates in a group** are one.
2. **Absorption in a group**: of `ℓ1 or ℓ2` with `ℓ1 ⇒ ℓ2`, keep `ℓ2` (the greater of the two is their OR). This merges ranges in a group: `since t1 or since t2` keeps the earlier time; `contains "invoice" or contains "invoices"` keeps `contains "invoice"`.
3. **A group that always holds** goes: it has two covering conditions of an always-known variable (`before t or since s` with `s` ≤ `t`; `mailbox is m or not mailbox is m`). A formula left with no group matches every message.
4. **Duplicates of groups** are one.
5. **Absorption between groups**: when every condition of group `G1` implies a condition of group `G2`, `G1 ⇒ G2` and `G2` goes (`G1 and G2` is `G1`). This merges ranges across groups: `since t1` and `since t2` keep the later time.
6. **A lone condition rules out the conditions disjoint from it**: when a group is one condition `ℓ` of an always-known variable, every condition disjoint from `ℓ` is taken out of every other group (`ℓ and (ℓ' or R)` is `ℓ and R` when `ℓ` and `ℓ'` are disjoint; it holds in three values because `ℓ` is never unknown). A group left empty makes the formula "never": `since s` and `before t` with `t` ≤ `s`, or `mailbox is m1` and `mailbox is m2`.

Then the conditions of each group are sorted by variable (the order of the table), operator (the order of `contracts.Operator`), sign (positive first) and the bytes of the value, and the groups by their sorted conditions, compared one by one, a shorter group first when one is the start of the other.

A condition a model decides is touched by rules 1, 2, 4 and 5 only as "the same condition": duplicates go, and a group that holds another's identical topic is absorbed. Nothing proves two different topics mean the same (ADR 0019), and nothing relates a topic to an exact condition.

These rewrites run inside one formula only. A consented scope, a request that fits in it and an agent's search are each canonical on their own and are never simplified together ("What a connection widened without consent is evaluated as").

### What it guarantees

- **It ends.** Rules 1, 2, 4 and 5 take out a condition or a group, rule 3 a group, rule 6 a condition; making ranges positive happens once, before. So the number of rewrites is at most the number of conditions plus the number of groups (at most 64 + 32). Each pass compares pairs of conditions, at most 64 × 64, and a pair of texts is one substring search of at most 400 bytes in 400. Q14 asserts these counts on the worst formula; how long it takes is reported by a benchmark (`BenchmarkCanonicaliseWorst`) and asserted by nothing.
- **It keeps the meaning**: every rewrite gives the same value in three values on every message, so a query delivers the same mail and a rule applies to the same mail.
- **One form for every spelling and order.** Values are normalised before anything compares them, and the rewrites run on the sorted set of groups of sorted conditions in a fixed order, so reordering conditions or groups, repeating them, or spelling a value another way that normalises the same gives the same bytes, and so the same identifier and the same sentence.
- **It is idempotent**: the canonical form of a canonical form is itself, since no rewrite applies to it and sorting a sorted form changes nothing.
- **What it does not guarantee**: that two formulas that mean the same give the same canonical form. Deciding that for formulas of this shape is co-NP-hard in general, and is not needed: what must hold is that the sentence says what the formula does ("The sentence"), and that containment is sound ("Containment"). `subject contains "invoice" or not subject contains "invoice"` stays as it is: in three values it may be unknown. Even where two values would allow it, the form is not always the smallest: `(mailbox is m or subject contains "x") and (not mailbox is m or subject contains "x")` means `subject contains "x"`, since `mailbox` is always known, but no rewrite above makes it so, and it stays two groups (Q46). Such a gap can only make containment answer "no", which asks for consent again; it never skips it.

## Containment

### The question, and where it is asked

"Does B fit inside A?": is every message B matches one A matches? It is asked when a connection asks for B instead of what it has (step 5), and the answer decides whether the person is asked again: "yes" changes the connection's request without asking; anything else asks. A is always **the scope the person consented to**, never the request a connection has now: a request accepted because it fit is compared with nothing later, so a series of requests each a little wider than the last cannot drift outside what was consented. It is not asked of an agent's search, which is evaluated with the connection's request and scope ("What a connection widened without consent is evaluated as"). It is not asked of rules.

### The algorithm

`Contains(A, B)`, on canonical forms:

1. A is "every message" (no group): yes. B is "never": yes. A is "never", or B is "every message" and A is not: no.
2. For **every** group `GA` of A, look for **one** group `GB` of B such that every condition of `GB` implies some condition of `GA` (the relation of "Simplification"). If every `GA` has one, yes; otherwise no.

### Why it is sound

On any message, in three values: each condition of `GB` is at most a condition of `GA`, so `GB`'s value (their greatest) is at most `GA`'s; B's value (the least of its groups) is at most `GB`'s, so at most `GA`'s, for every `GA`, so at most A's (the least of A's groups). So wherever B is true, A is true (a query delivers nothing more), and wherever B is unknown, A is unknown or true (a rule would apply as much). It never says yes when B can match a message A does not, whatever is unknown. It needs three things of evaluation, each stated where it is made: an address decided once, whole ("Addresses"); one answer per message and atom ("Model answers, kept per message and per atom"); and a field normalised whole ("Texts, and where they match").

**Texts, re-proven for substrings.** For `contains`, the cases the tests hold (Q16, Q18): `subject contains "invoices"` fits inside `subject contains "invoice"` (yes: "invoice" is in "invoices"); `subject contains "invoice"` inside `subject contains "invoices"` (no: "invoice" matches a subject without the s); `subject contains "scar"` inside `subject contains "car"` (yes) and the reverse (no); `not subject contains "car"` inside `not subject contains "scar"` (yes: reversing the order); `body contains "invoice 2026"` inside `body contains "invoice"` and inside `body contains "e 2"` (yes); `subject contains "invoice"` inside `body contains "invoice"` (no: two variables); `attachment_name contains "x.pdf"` inside `attachments exists` (yes). Each is the implication table's substring row, and its soundness is the paragraph "Texts, as substrings" above.

### Where it says no although the answer is yes

It answers only what it can prove condition by condition, and says no, which means fresh consent, when:

- B's or A's group needs a **model's** condition other than an identical one: `message about "invoices"` does not fit inside `message about "bills"`, nor `about` inside `mentions`.
- The proof needs **several groups of B at once**: B = `{x or y}` and `{not y}` fits inside `{x}` only by resolution between B's groups, which in three values is not valid for a variable that can be unknown, and is not tried for the ones that cannot.
- The proof needs **what the account has**: "not mailbox m1" fits inside "mailbox m2" only while the account has exactly those two mailboxes, which changes.
- The proof needs **how two variables go together in real mail**: an attachment named "x.pdf" is usually of type `application/pdf`, and a message signed by a domain is usually sent from it, but nothing makes either so, and the table relates no two variables but an address and its domain.

Each is on the side of asking the person again: a wrong "no" costs a click, a wrong "yes" would let mail leave without consent.

### Cost

At most `|groups of A| × |groups of B| × |conditions of GA| × |conditions of GB|` implication tests: for two formulas at their limits, at most 64 × 64 = 4,096 tests of conditions, each at most a substring search of 400 bytes in 400. No search over formulas, no backtracking. Q19 asserts the count; `BenchmarkContainsWorst` reports the time.

### What a connection widened without consent is evaluated as

A connection keeps the scope the person consented to, A, as a canonical formula of its own, for as long as the connection lasts. When a request B is accepted without fresh consent because it fits inside A, B is kept as the request, canonical on its own; A does not change. Delivery evaluates A and B separately on each message, each in three values, and takes the lesser of the two (their AND in Kleene's logic); an agent's search S is a third formula evaluated the same way (`Eval` takes the formulas evaluated together, `Matcher` likewise). No implication-based rewrite crosses from one formula to another: simplifying `B AND A` as one formula would let rule 5 delete exactly A's groups whenever `Contains(A, B)` holds, so a containment proof that was wrong would also remove the check that is meant to catch it.

What this guards against, exactly: a mistake in `Contains` (or in the implication table it uses) can let a request change without asking, but it cannot make delivery give a message that A does not make true, because A is evaluated as the person approved it. It does not guard against a mistake in `Eval` itself, which A and B share; Q12, Q17, Q18 and Q39 test that. ADR 0019's "an agent's search AND its approved scope is again a formula" keeps its meaning: the value is the AND of the formulas; what changes is that they are not merged into one canonical form. The receipt (step 8) names every identifier evaluated.

## The sentence

### Its shape

`Render(f, Context)` writes a `contracts.Sentence` from a canonical formula: the plain text (`Text`), and the same words in parts (`Lead`, `Lines`), each part fixed words of a template or a value of the formula, marked when a model decides it, so the web app shows values set apart without reading `Text`. It takes the formula, the language and the account's mailboxes, and nothing else: no time, no clock, no state, no model. The web app never writes a sentence of its own; it shows the server's.

- Every message: `All mail.`
- One group: `Mail ` then the group's conditions joined by `, or `, then `.`: "Mail sent from an address at holded.com or a subdomain of it, or whose subject contains the text “invoice”."
- Several groups: the lead `Mail that meets all of these:`, then one line per group in canonical order, each its conditions joined by `, or `; in plain text each line starts with `- ` on a line of its own and the last ends with `.`.
- A rule of several formulas (step 7): `Any of these:` and each formula's sentence. A connection's scope and its request are each their own sentence.

The groups are lines rather than one sentence because "a or b and c or d" in English does not say where the brackets are; a list of lines, every one of which must hold, does.

### Templates (English)

`{v}` is a text, a topic or a list, `{a}` an address, `{d}` a domain, `{t}` a media type, `{T}` a time, `{N}` a size and `{M}` a mailbox, each written by the presentation of its type ("Presenting values"). "(a model decides)" follows every condition a model decides in plain text, and marks its parts in the structure.

| Condition | Positive | Negative |
|---|---|---|
| `from_address is` | sent from {a} | not sent from {a} |
| `from_domain is` | sent from an address at {d} or a subdomain of it | not sent from any address at {d} or a subdomain of it |
| `recipient_address is` | addressed to {a} | not addressed to {a} |
| `recipient_domain is` | addressed to an address at {d} or a subdomain of it | not addressed to any address at {d} or a subdomain of it |
| `signed_by is` | signed by {d} or a subdomain of it | not signed by {d} or any subdomain of it |
| `subject contains` | whose subject contains the text {v} | whose subject doesn't contain the text {v} |
| `body contains` | whose body contains the text {v} | whose body doesn't contain the text {v} |
| `attachments exists` | with attachments | without attachments |
| `attachment_name contains` | with an attachment whose name contains the text {v} | with no attachment whose name contains the text {v} |
| `attachment_type is` | with an attachment of type {t}; for `T/*`, with an attachment of any {T} type | with no attachment of type {t}; with no attachment of any {T} type |
| `list exists` | sent to a mailing list | not sent to a mailing list |
| `list is` | sent to the mailing list {v} | not sent to the mailing list {v} |
| `received since` | received on or after {T} | (made positive: `before`) |
| `received before` | received before {T} | (made positive: `since`) |
| `size at_least` | of {N} or more | (made positive: `below`) |
| `size below` | smaller than {N} | (made positive: `at_least`) |
| `mailbox is` | that arrived at your mailbox {M} | that didn't arrive at your mailbox {M} |
| `subject about` | whose subject is about {v} (a model decides) | whose subject is not about {v} (a model decides) |
| `message about` | about {v} (a model decides) | not about {v} (a model decides) |
| `message mentions` | that mentions {v} (a model decides) | that doesn't mention {v} (a model decides) |

"Contains the text" says what the owner decided `contains` does: any piece of text, so “car” is in "scar". Case and width are not said in every condition; the consent screen and docs/web.md say once that they are ignored.

### Presenting values

Every value of every template goes through one presentation function of its type, and through nothing else:

- **Text, topics and lists** `{v}`: inside `“` and `”`; inside them, every backslash and every double quotation mark of Unicode's `Quotation_Mark` property (`"`, `«`, `»`, `“`, `”`, `„`, `‟`, `⹂`, `〝`, `〞`, `〟`) is written with a backslash before it, so a quote inside a value is never read as its end: a value `A”, or B` reads “A\”, or B”. A value holds no line break, control or format character (normalisation removed or refused them).
- **Addresses** `{a}`: the local part as normalised, presented as a text value is (quotes and backslashes escaped, inside `“` and `”`, with the domain), its domain as below outside the quotes: “ann@bücher.example” (xn--bcher-kva.example). The local part passed the same checks as a text value (no control, no space, no mix of Latin, Greek and Cyrillic), so `a”b@example.com` reads “a\”b@example.com” and a local part with a Cyrillic `а` among Latin letters was refused.
- **Domains** `{d}`: the Unicode form when it differs from the ASCII one, followed by the ASCII form in brackets: "bücher.example (xn--bcher-kva.example)"; otherwise the ASCII form. Never quoted: its characters cannot hold a quote, a space or a separator.
- **Media types** `{t}`, `{T}` for a type: as normalised, unquoted, of the characters above.
- **Times** `{T}`: in UTC, said once at the end ("Times are in UTC."): `1 January 2026` at midnight, `1 January 2026, 09:30` with minutes, `1 January 2026, 09:30:15` with seconds. UTC has no hour twice, so no two times are written alike. Showing them in the person's time zone waits for the account to have one (step 6, the schedule, needs it); the zone then becomes part of `Context`, and a time written in an hour that a change of clocks repeats carries its offset.
- **Sizes** `{N}`: `N MB` when divisible by 1,000,000, `N KB` when by 1,000, otherwise `N bytes`, with `,` between thousands: one way to write each number.
- **Mailboxes** `{M}`: the label, presented as a text value is (quoted and escaped), when it is not empty and no other mailbox of the account has it; otherwise the mailbox's address, unquoted (Permaily makes it, of characters that hold no quote, space or separator). A mailbox the account no longer has: "a mailbox you disconnected". Labels are the owner's own (a vendor never sets one), and may hold anything printable (`domain.MailboxLabel`), so they are escaped like any value: labels `A”, or that arrived at your mailbox “B`, `A` and `B` give three different sentences for `mailbox is m1` and `mailbox is m2 or mailbox is m3` (Q43).

**How long a sentence gets.** A text value is at most 400 bytes, and at most 800 once escaped; an address at most about 1,400 bytes (a local part of 64, escaped to 128, and a domain of 253 bytes in ASCII and at most 1,012 in Unicode); each template's fixed words are under 120 bytes. So a condition is at most about 1,600 bytes, and a sentence of 64 conditions with its lead and its last line under 100 KiB; a typical one is under 1 KiB. Q43 asserts the bound on the worst generated formula.

### Faithful, exactly

**The claim.** For a fixed language and `Context`: the structured sentence (`Lead`, `Lines`, each part fixed words or a value) is injective by construction: each condition is one template, chosen by its variable, operator and sign, whose fixed words differ from every other template's, followed by its value parts, and groups are lines in canonical order. The plain text is injective given the escaping above: every value is delimited by quotes it cannot hold unescaped, or written by a presentation whose characters cannot hold a quote, a line break or the separator `, or `. That is checked, not proven: by a parser of sentences in the tests (`parseSentence`, test only), which must give back every generated canonical formula from its plain text and from its parts (Q21), and by property tests whose generators are adversarial (values that are template words, separators, escaped and unescaped quotes, backslashes, right-to-left text, the longest values; labels that are sentences: Q22, Q43). The design claims no more than that.

Where the sentence may change while the formula does not, and why that is safe: a mailbox renamed changes the sentence of a formula that did not change (the person renamed it, and reads the new name); two mailboxes that the account disconnected both read "a mailbox you disconnected" (each matches nothing, so the two formulas deliver the same mail).

**Values cannot add words.** A vendor controls the values and nothing else. A value is normalised before it is rendered: no bidirectional control (they are default-ignorable and removed), no line break (made a space), no control or format character (refused), no confusable mix of scripts (refused), at most 100 characters, and every quote and backslash in it escaped. In the plain text it is inside quotes; in the app it is a part of its own, shown in its own style (monospace for exact values, as the screens set them), isolated in `bdi` so right-to-left text cannot reorder the fixed words around it, and never as markup (web/AGENTS.md). A value that is itself English ("not sent from", "or with attachments") stays inside its quotes and its part. The parser is in the tests, never in the service: nothing reads a sentence to decide anything.

**Model conditions say so.** A condition a model decides is marked in every form of the sentence, as ADR 0019 asks of the consent screen and the receipt.

### A second language

A language is a table: the templates above, the lead, the separators, the months, the number formats, the words for a disconnected mailbox and for UTC, keyed by its tag (`en` today). Values are never translated, and the presentation of values is the same in every language. A table is accepted only when the same tests pass with it: every condition and sign has a template, no fixed text holds a quote, a line break or its separator, and the parser reads every generated sentence back (Q21 to Q23 and Q43 run per language). The language of a sentence is the account's (`language` in the account's record, docs/data.md); `Context.Lang` carries it, and an account in a language without a table reads English. This step has English only; Spanish is the next table, its own pull request, when the app itself has Spanish (the owner's decision, "The owner's decisions", 5).

## Evaluation over stored mail

### What each source gives and costs

| Source | What it gives | How it is read | Cost |
|---|---|---|---|
| The item and its identifier | `received` (the identifier's time), `mailbox` (its partition); its sealed meta | `ScanMessagesDown` in batches of 100, or `Item` | One item read; the meta unsealed |
| The meta | Senders, recipients, subject, attachments, DKIM results, the list, the size (format 2), and the flags `Partial`, `Unparsed`, `Confirmation` | Inside the item | Unsealed from the item read |
| The search entry (`txt#`) | The first 16 KiB of the text, and whether it was cut there | `GetEntry` by the message's identifier | One item of up to about 18 KiB, unsealed |
| The raw message | Up to 256 KiB of text, and its length | `ReadRaw` under a grant of the read budget, opened, then `inbox.MessageText` | Up to 25 MiB read and parsed |

### What is indexed, and what is scanned

Message identifiers begin with the nanosecond they were received (ADR 0026), and the scan reads a partition from the greatest key down. So `received` is indexed: when every formula evaluated has a group of one `received before t` condition, the scan starts below the smallest identifier of `t` (its 16 hex digits of time, then zeros), whatever `before` says if that is lower; when one has a group of one `received since s`, the scan stops at the first identifier below `s`, and the page then has no `next`. Every other variable is scanned: each message considered is read. A sealed index of words or senders was rejected for the inbox's search (ADR 0026, "Searching") for what it shows whoever reads the table, and the same holds here. Bounding the scan by the identifiers before the meta is opened trusts the table only to withhold, which whoever writes it can always do (by deleting); it never adds a message, since a message outside the bound is not considered, and one inside is classified only after its meta, which authenticates its identifier, is open.

### Which reads

`Eval` is three-valued and tells which source would settle an unknown value (`Result.Needs`), so a message's sources are read in order, each only if the value still depends on it:

1. **The item and its meta, always, before anything is decided.** The scan reads message items (which carry the sealed meta) newest first; no message is classified, as true or as false, before its meta is open: the meta holds `Partial`, `Unparsed` and `Confirmation`, which change what every other source means. `Eval` answers Unknown with `FromItem` and `FromMeta` in `Needs` for a message without both, whatever else it has. Search entries are never scanned: one is read only by the identifier of an item, so an entry whose item is not there (ADR 0026 writes entries before items) is never read or listed. A message whose meta does not open is listed as unknown and the scan goes on.
2. **A Gmail forwarding confirmation** (the meta's `Confirmation`) is passed over: never listed, whatever the formula. It is Permaily's, and never leaves.
3. **The entry, for the text**, when the value still depends on a `body` condition. An item whose entry is missing or does not open has its body conditions read from the raw message, as a cut text does.
4. **The raw message, when the entry was cut** (`SearchEntry.Cut`, or a text that ends within 4 bytes of 16 KiB in an entry written before this step) and the value still depends on any `body` condition. No condition on a body is decided from a cut text, for true or for false: a text that seems to hold a value in its first 16 KiB may not hold it once normalised whole (a prefix that ends in `cafe` holds `cafe`, and the full text `café` does not), and one that seems not to hold it may hold it further on. When it is read, its length is also the size of a message whose meta has none.

A message whose value is true or false once a source is read is settled there. One whose value depends only on conditions a model decides is "needs a model" (`Result.Model` names their atoms, and step 4 asks only those); one whose value depends on an exact condition that could not be read is "unknown".

### Budgets of a page

A page runs under the request's deadline (one minute from its start, ADR 0026) and holds one of the four search slots of the enclave, counted in the account's thirty search pages a minute, which the inbox's search and this share. It ends at the first of:

| Bound | Value |
|---|---|
| Matches | `limit`, 1 to 100 (50 by default) |
| Messages that need a model or are unknown | 100 (`inbox.MatchMaxUndecided`) |
| Stored items read: items and entries, scanned or read one by one, expired ones included | 500 (`inbox.MatchMaxRead`) |
| Raw messages read for their text | 10 (`inbox.MatchMaxTextReads`), one at a time: each takes its bound from the read budget of 128 MiB (`inbox.ReadGrants`, waiting at most 10 seconds as every reader does, then 429 for the request) and gives it back once its text is taken out, before the next |
| Time spent considering messages | 20 seconds (`inbox.MatchSoftDeadline`), then the page answers what it has |

**Pages skip nothing.** A message is considered whole on a page, or not at all: when a bound is reached in the middle of a message (its raw text not affordable on this page, the 500th item read before its entry), the page ends before it, and `next` is the last message considered, so the next page starts with it. `next` is absent only when the scan says nothing older is left, or the `received since` bound is passed. A batch whose items have all expired advances `next` past them as ADR 0026's `Scan.Last` does, so such a page is not stuck (Q28 exercises it). A message that cannot be read at all (its meta does not open, its raw message is past its bound) is considered and listed as unknown, so it does not stop every page. `searched_to` is when the last message considered was received.

**What a page costs, at most.** Every message considered costs one item read, and one entry read when the value depends on the body: a page considers between 250 messages (every one needs its entry) and 500. An item is at most 400 KB (DynamoDB's bound; its meta at most 256 KiB, ADR 0026) and usually a few KiB; an entry about 18 KiB at most. So a page reads about 6 MiB in the usual case (250 items of a few KiB and 250 entries), and at most about 200 MB in the worst (500 items at DynamoDB's bound), in DynamoDB queries of at most 1 MB each. Raw messages: at most 10, read one after the other, so a page holds at most one reservation of the read budget (at most 32 MiB, `inbox.RawReadMax`) at a time, and the four search slots at most 128 MiB together; six messages of 25 MiB on one page are read in turn, not refused (Q44). A body formula over long mail (newsletters past 16 KiB) needs the raw message of each message it cannot settle otherwise, so its pages advance ten such messages each; thirty pages a minute make three hundred. The inbox's search page reads 2,000 entries; a match page reads fewer stored items and parses more.

### What delivery adds for it

Four things ADR 0026's delivery does not record today (an amendment of its format, written by pull request 3):

- `StoredMeta.List`, the identifier of the first `List-Id` header (lower case, without brackets), `StoredMeta.Size`, the size of the message as delivered, and `StoredMeta.Format`, `2` from this step on. A meta without `format` was written before: every `list` condition on it is unknown (whether it had a `List-Id` is not stored), and so is every `size` condition unless its raw message is read for its body. Nothing is migrated: such messages expire with their retention.
- `SearchEntry.Cut`, set when the text was longer than the 16 KiB kept. An entry without it was written before, and is taken as cut when its text is within 4 bytes of 16 KiB, which is where a cut text ends.

Mail is still never refused for its content, and the sizes ADR 0026 bounds do not change.

### Logs

A formula, a value, a sentence, an identifier of a formula and the number of matches are never logged. What is counted and logged is a kind: a page answered, refused for a reason of `contracts.FormulaReason`, or stopped by which bound. `TestMailEndToEnd`'s log check gains a formula with hostile values.

## Departures from the sample screens

The screens are the designer's view of what a person sees. Where this design departs, and why:

1. **Rule criteria are OR of AND**, with values OR'ed inside a condition; ADR 0019 said AND of OR. Kept as the screens have it: each criteria is one formula and a rule is any of them ("Rules: any of their criteria"). The editor needs no change.
2. **"Message contains"** looks at the subject and the text: it becomes `subject contains v or body contains v` (negated: `not subject contains v` and `not body contains v`), and the sentence says both.
3. **"Message mentions"** is `message mentions`. **"Message is about"** is `message about`.
4. **"Kind is password reset"** is marked in `valueStyle` as exact (no model), and nothing exact decides what kind a message is. It becomes `message about "password reset"`, with the editor's suggestions kept as suggested topics, and the screen must mark it as decided by a model.
5. **"Label is Family"** (a Gmail label) cannot be evaluated: forwarding carries no labels. It goes from the editor, and the sample rule "Private" keeps its senders only.
6. **"Sender is mapfre.es"** (an address or a domain) becomes `from_address is` when the value has an `@` and `from_domain is` otherwise; its sentence says "or a subdomain of it".
7. **Connections' queries in Gmail's syntax** (`in:inbox category:primary newer_than:1d`, `in:anywhere -in:spam -in:trash`, `from:(…) OR subject:(…)`): the first two are not expressible (no folders, categories or spam in a forwarded mailbox; "new mail only" is a term of the connection); the third is one group. The connection's "Readable" view shows the sentence; its "File" view shows the canonical JSON, or YAML written from it, not Gmail's syntax.
8. **"Fields it asks for"** (`needs_reply`, `amount`, `billing_address`, … in `web/src/data/queries.ts`) are values a model would extract from each message. No ADR decided them, and they are not conditions: they are outside this language. They stay sample until a decision of their own.
9. **The consent screen's "Asks for"** shows a name of a kind of mail ("Flight, hotel, rail and car bookings"). It shows the sentence instead (ADR 0019: the sentence is what the person consents to); a vendor's name for its connection may still head it.

## The contract and the seams

### Committed with this design

- `internal/contracts/query.go`: `Formula`, `Group`, `Condition`, `Variable` and `Operator` with their constants in canonical order, `FormulaVersion`, the limits, `ErrInvalidFormula`, `FormulaProblem` and `FormulaReason` with every reason, `FormulaCheck`, `Sentence`, `SentenceLine`, `SentencePart`, `MatchRequest`, `MatchPage` (the owner's previews only, its doc comment says so); `query_test.go` pins their JSON (`TestQueryShapes`).
- `web/src/lib/api.ts`: the same shapes (`QueryFormula`, `QueryCondition`, `QueryVariable`, `QueryOperator`, `FormulaProblem`, `FormulaCheck`, `QuerySentence`, `SentencePart`, `MatchPage`). The calls come with the routes (pull request 4).
- `internal/query/query.go`: the package's doc and the types its pull requests share: `Value` (`False`, `Unknown`, `True`), `Mode` (`ForQuery`, `ForRule`), `Canonical`, `Sources` (`FromItem`, `FromEntry`, `FromMeta`, `FromText`), `Message` (with `SizeKnown`) and `Attachment` (what evaluation knows of a message), `Atom` and `Answers` (a model's answers, by atom), `Result`, `Context` and `MailboxName`; and `Classify`, the one function committed now, with its test (`query_test.go`, Q33).
- `internal/inbox/match.go`: `Matcher` (over formulas evaluated together) and its bounds (`MatchMaxRead`, `MatchMaxTextReads`, `MatchMaxUndecided`, `MatchSoftDeadline`).
- `internal/inbox/read.go`: `ReadGrants`, the read budget taken one raw read at a time: `Grant(ctx, n)` returns its own `release`, which a page of matches calls once the message's text is taken out, before its next raw read. `ReadBudget` and its `Take` stay for every other reader.
- `.golangci.yml`: the depguard rule `query-is-pure`: `internal/query`'s production code imports `bytes`, `cmp`, `crypto/sha256`, `encoding/base32`, `encoding/json`, `errors`, `fmt`, `maps`, `slices`, `sort`, `strconv`, `strings`, `time`, `unicode` (and so `unicode/utf8`), `internal/contracts`, itself, `golang.org/x/text/unicode/norm` and `golang.org/x/net/idna`, and nothing else: not `$gostd`, which holds `os`, `net` and `io`. Its tests and `querytest` (test only, kept out of production by `no-fakes-in-production`, pull request 1) are outside the rule. Both modules are direct dependencies already (`go.mod`), in the image through `go-message` and `mailhtml`; this adds no module.

As ADR 0026 did, the seams are types and interfaces, not functions with empty bodies: nothing implements `Matcher` or `ReadGrants` yet, and nothing calls into `internal/query`. `Classify` is the exception, because it is part of what a formula means, and is ten lines.

### Why its own package, not `internal/domain/query`

`internal/domain` is "rules that need only their arguments", and depguard holds it to the standard library and `contracts`. The language is pure in that sense, but normalises with NFKC and UTS 46, which are in `golang.org/x/text` and `golang.org/x/net/idna`; writing them again would be a second Unicode and IDNA implementation to keep right, and widening `domain-is-pure` would let every rule of `domain` import them. `internal/mailhtml` is the precedent: pure, on `x/net/html`, a package of its own. Layers: `contracts` ← `query` ← `inbox`, `services` and adapters; `query` imports no service, store or key.

### The functions each pull request writes

In `internal/query`, by pull request 1:

```go
// Parse reads a formula as a vendor or the web app sends it, strictly; checks
// it against the limits and the account's mailboxes (nil: any well-formed
// identifier); and returns its canonical form. A refusal is a *Problem, which
// is contracts.ErrInvalidFormula (errors.Is) and carries a
// contracts.FormulaProblem. A formula that is "never" is refused
// (FormulaNeverMatches).
func Parse(data []byte, mailboxes []string) (*Canonical, error)
type Problem struct{ contracts.FormulaProblem }

// Canonicalise is Parse's second half, for a formula already decoded (a
// rule's, read back from storage): values normalised, the rewrites applied,
// sorted. It does not refuse "never"; it returns it.
func Canonicalise(f contracts.Formula) (*Canonical, error)

// Contains reports whether every message inner matches is one outer matches;
// false whenever it cannot prove it ("Containment"). outer is a consented
// scope, never a request accepted because it fit.
func Contains(outer, inner *Canonical) bool

// Eval is the value of formulas evaluated together (the least of their
// values; each canonical on its own, never merged) on what is known of a
// message, with the model's answers for that message read by Classify in
// mode, and what would change it. Unknown, needing FromItem and FromMeta,
// for a message without both.
func Eval(fs []*Canonical, m *Message, answers Answers, mode Mode) Result

// Resolve says whether a value delivers (ForQuery) or applies the rule
// (ForRule).
func Resolve(v Value, mode Mode) bool
```

Committed now, in `query.go`:

```go
// Classify is the value of a condition a model decides, from the model's
// probability p that it holds, in a mode ("The bands of a model").
func Classify(p float64, mode Mode) Value
```

By pull request 2:

```go
// Render writes the sentence of a canonical formula, in c.Lang (English when
// it has no table), naming mailboxes from c.Mailboxes.
func Render(f *Canonical, c Context) contracts.Sentence
```

In `internal/inbox`, by pull request 3: `Matcher` implemented by the store's reader (`match.go`), its raw reads through `ReadGrants`, and `inbox.QueryMessage(item, meta, entry, text)` filling `query.Message` from what was read. In `internal/httpapi`, by pull request 4: the routes below, through `MailboxesService.Open` and the reader, as the inbox's search goes, and `ReadGrants` implemented on the adapter's read budget (`heldBudget`), so a grant released goes back at once while what `Take` took is still held to the end of the response.

### The routes (the web app's API, pull request 4)

| Request | Needs | Answers | Errors besides ADR 0026's common ones |
|---|---|---|---|
| `POST /api/v1/queries/check` `{formula}` | a session; an account open, closing or not agreed | 200 `FormulaCheck`, its sentence in the account's language, its mailboxes named | 400 `invalid_formula` with `details`, `invalid_body` |
| `POST /api/v1/mailboxes/{id}/matches` `MatchRequest` | a session; the standing of `GET .../messages?q=` (ADR 0026's access table: refused with `terms_needed` to an account that has not agreed) | 200 `MatchPage` | 400 `invalid_formula`, `invalid_limit`, `invalid_cursor`, `invalid_body`; 409 `mailbox_unreadable`; 429 `too_many_requests` |

They are the web app's, in the owner's own session: the check is what the rule editor (step 7) and the consent screen (step 5) show sentences from, and the matches what their previews count. No screen is built in this step. Both routes are under `/api/` on the app's host, behind the cross-site rule and the canonical path (ADR 0026, "The API"), and are POSTs because a formula does not belong in an address bar or a log of one.

**`MatchPage` is the owner's preview, not a vendor's result.** Its `next`, `searched_to`, `needs_model` and `unknown` name messages outside any approved scope: their identifiers, when they arrived, that they exist. Only the owner's session reaches it today, so it reveals nothing to anyone else; no vendor route may ever answer it. A vendor's result and its continuation are a separate contract, designed in step 5, that names only messages delivered to it (true on every formula evaluated, after the rules). What a vendor can still observe through it, and step 5 must keep to that: how long a page took, and that a page ended (a continuation or none), which say something of how much mail it did not get.

### Who calls what, later

| Step | Uses |
|---|---|
| 4, the model | `Result.Model` (atoms) to ask only what a message still needs; `Answers` and `Classify`; a kept answer keyed by the message, a hash of the content the model saw, the atom, the model and the prompt's version, never by a formula |
| 5, connections | `Parse` on a vendor's request, `Render` on the consent screen, `Contains(consented scope, request)` for asking for more, `Eval` of scope, request and an agent's search together, `Matcher` for the preview's count, `Resolve(ForQuery)`; its own result contract for vendors, never `MatchPage` |
| 7, rules | `Canonicalise` of the formula the model writes from the person's words, `Render` to confirm it, `Resolve(ForRule)` |
| 8, receipts | The canonical identifiers and the sentences of every formula evaluated |

## Tests

Numbered so a review can check each. "Generated" means by `querytest` (pull request 1, test only): random canonical and non-canonical formulas over a small universe of values chosen to collide (two domains and a subdomain of one, three addresses and one that does not normalise, texts that are substrings of each other, four times, three sizes, two mailboxes, two topics), and random messages over the same universe with every source read or not, `Partial`, `Unparsed`, cut texts, sizes recorded or not, unnormalisable addresses, and model answers in every band or missing.

**Parsing** (pull request 1)

- **Q1.** A table of valid formulas, each with the canonical JSON it gives (golden).
- **Q2.** A table with one formula per refusal reason, and the `at` of each; the error's message is fixed and holds no value.
- **Q3.** Strictness: a member twice (`{"v":1,"v":2,…}`), an unknown member, `null` anywhere, a number for `value`, a group inside a group, a byte order mark, invalid UTF-8, trailing data, 64 KiB and one byte more; none panics.
- **Q4.** `FuzzParse` (in `make fuzz`): never panics; every accepted input's canonical JSON parses to itself, byte for byte; canonical JSON is deterministic.
- **Q5.** Every limit at its value and one past it.

**Normalisation** (pull request 1)

- **Q6.** Addresses and domains: case, a Unicode domain and its ASCII form giving one value, a trailing dot, a quoted local part, a `+tag` kept, an invalid label refused, a mixed-script label refused, a mixed-script local part refused, a local part with `”` accepted, the message's side not valid giving unknown for its address and its domain conditions, never false.
- **Q7.** Text: NFKC (`ﬁ`, full-width letters), the folds of `ß`, `ẞ`, `Σ`, `ς`, the Kelvin sign and `İ`, zero-width and bidirectional characters removed, white space made one, controls refused, `invоice` with a Cyrillic `о` refused, `“` and `\` accepted; one round of the normalisation is a fixed point on every code point, and `FuzzNormalise` holds it on any string; a value that would need more than three rounds is refused (a constructed one, if none exists, by a test double of a step).
- **Q8.** Substrings: `invoice` in "invoices", "your INVOICE," and "re-invoiced"; `car` in "scar" and "careful"; `.pdf` in "bill.pdf"; a text across a line break; `in­voice` with a soft hyphen; `发票` inside a Chinese sentence; Thai; a match at the start and at the end of a field; `cafe` not in "café", in NFC and in NFD; `q` in `q` and U+0301; a value ending in a mark.
- **Q9.** Times with offsets, a fraction, a leap second, a year before 2000 and after 2100; sizes with a sign, a leading zero, 0 and 32 MiB plus one.

**Simplification** (pull request 1)

- **Q10.** A table with one case per rewrite and per relation of the implication table, before and after, the substring row included (`contains "invoice" or contains "invoices"` keeps `invoice`; `contains "invoices" and contains "invoice"` keeps `invoices`).
- **Q11.** Idempotent: the canonical form of a generated formula's canonical form is itself, byte for byte.
- **Q12.** It keeps the meaning: for generated formulas and messages, `querytest`'s brute-force evaluator on the formula as sent and `Eval` on its canonical form give the same three-valued value, and so the same `Resolve` in both modes.
- **Q13.** One form for every order: a generated formula with its conditions and groups shuffled, repeated and its values respelled (case, Unicode and ASCII domains, white space) gives the same bytes and identifier.
- **Q14.** Bounded: the worst formula at the limits (64 conditions, every pair related) is canonicalised in at most as many rewrites as it has conditions and groups, and in at most 64 × 64 comparisons of conditions a pass (counted); `BenchmarkCanonicaliseWorst` reports the time and asserts nothing.
- **Q15.** Three values, not two: `subject contains "x" or not subject contains "x"` is kept; `message about "x" and not message about "x"` is kept, not "never"; `mailbox is m or not mailbox is m` goes; `received since s and received before t` with `t` ≤ `s` is "never" and is refused by `Parse` as `never_matches`; `size at_least 10 and size below 5` is kept and accepted, and is unknown on a message without a recorded size; `not size at_least n` becomes `size below n`, with the same value on every generated message.

**Containment** (pull request 1)

- **Q16.** A table of yes and no per kind of variable: addresses in domains, subdomains, texts in texts as substrings (the cases of "Texts, re-proven for substrings", each way), types in `T/*`, lists, ranges, mailboxes, identical topics, different topics (no), `about` and `mentions` (no), several groups of B needed (no), every message and never.
- **Q17.** Sound: for generated A and B, built so that "yes" is common (B made by strengthening A: a condition replaced by one that implies it, a text lengthened on either side, a group added, a condition taken out of a group), whenever `Contains(A, B)`, on every generated message, `Eval([B]) ≤ Eval([A])` in three values, with one `Answers` per message.
- **Q18.** Sound by brute force: over a universe small enough to list every message (two domains, three addresses and one not valid, two texts of which one is a substring of the other, two times, attachments or not), for every pair of formulas of up to two groups of two conditions, `Contains` yes implies containment over every message and every assignment of unknown; and for formulas of single conditions, the answer is exactly the brute force's (complete there).
- **Q19.** Bounded: two formulas at their limits are compared in at most 4,096 tests of conditions (counted); `BenchmarkContainsWorst` reports the time and asserts nothing.

**The sentence** (pull request 2)

- **Q20.** Golden sentences in English for a table covering every template and sign, one group and several, every message, a rule of several formulas, a domain with a Unicode form, times at midnight, with minutes and with seconds, each kind of size, mailboxes named by label and by address and disconnected; reviewed with the designer.
- **Q21.** Injective: `parseSentence(Render(f).Text)` is `f` for every generated canonical formula, and the parts read back give `f` too; for every language with a table.
- **Q22.** No injection: values that are every template's fixed words, `, or `, `.`, `- `, `“`, `”`, `\“`, `\`, right-to-left text, the longest value, and values whose normalisation removed bidirectional controls and line breaks: each stays one value part, inside its quotes in plain text with its quotes and backslashes escaped, and the parser still reads the formula back.
- **Q23.** The tables: every variable, operator and sign has a template in every language; no fixed text holds `“`, `”`, a backslash, a line break or its separator; no template's fixed text is another's.
- **Q24.** Mailbox names: two mailboxes with one label are named by their addresses, an empty label likewise, a renamed one by its new name, labels holding quotes, backslashes and template words are escaped, and no two mailboxes of an account read alike.
- **Q25.** Model conditions are marked in plain text and in parts, and only they are.

**Evaluation** (pull requests 1 and 3)

- **Q26.** `Eval` per variable, with a table of messages for true, false and each reason for unknown (a source not read, no meta read, `Partial`, `Unparsed`, a meta of format 1 for `list` and `size`, an address not valid, a cut text not read, a missing model answer), `Result.Needs` naming the source that would settle it, and `Result.Model` its atoms.
- **Q27.** Staged equals whole: for generated messages written with `inboxtest.Seed` (which writes the new fields), what `Match` lists as a match, needing a model, unknown or nothing equals `Eval` on everything read at once.
- **Q28.** Pages skip nothing: generated mailboxes of up to 2,000 messages, pages of random `limit`, bounds set low (`MatchMaxRead` 7, two raw reads) so pages end inside batches and before a message's raw text, and batches whose items have all expired: the union of the pages' matches is exactly the brute force's, with no message twice, `next` absent only at the end, and a page of expired items only advancing `next` (`Scan.Last`).
- **Q29.** The `received` bounds are used: a formula with `received since` and `before` reads no item outside them (the store's reads counted).
- **Q30.** Budgets: a grant taken before each raw read and released before the next (never two held by one page), 429 when one is not admitted within 10 seconds, the soft deadline ending a page with `next`, the search slot and the thirty pages a minute shared with the inbox's search, the request's deadline.
- **Q31.** A Gmail forwarding confirmation is never listed, whatever the formula.
- **Q32.** Delivery: a message with `List-Id` stores its identifier, its size and format 2; a text longer than 16 KiB sets `Cut`; a meta without format reads `list` and `size` as unknown; an entry without `Cut` whose text is 16 KiB long reads as cut.
- **Q33.** The bands (committed now, `TestClassifyBands`, `TestClassifyNegated`): `Classify` at 0, 0.19, 0.20, 0.21, 0.29, 0.30, 0.69, 0.70, 0.79, 0.80 and 1 in both modes, NaN, infinities and values outside 0 to 1 unknown, an unknown mode unknown, and a negated condition delivering only at 0.20 or less and applying below 0.70.

**Routes and the contract** (pull request 4)

- **Q34.** `queries/check`: the shapes, every reason with its `details`, a fixed message, no value echoed; a mailbox of another account is `unknown_mailbox`; the sentence in English for an account of another language (until a table exists).
- **Q35.** `matches`: ADR 0026's order of failures (401, 404 for another account's mailbox before anything is read, the standing, `mailbox_unreadable`, the input, limits), JSON with `nosniff`, and the reader sees no call for another account's mailbox.
- **Q36.** `TestQueryShapes` (committed now), the web app's types type-checked against a fixture of each answer, and `TestMailEndToEnd` extended: a mailbox, mail with a list, an attachment and a long text, a formula through `matches`, and the log holding no value, sentence or identifier.

**From the first design review** (each the reviewer's counterexample)

- **Q37.** (pull request 1) One decision per address: A = `not from_address is alice@example.com`, B = `not from_domain is example.com`; `Contains(A, B)` is yes, and on a message from `"bad local"@other.example` both A and B are unknown (never B true and A unknown); from `bob@other.example` both true; from `alice@example.com` both false; and on generated messages, address and domain conditions over one address are unknown together.
- **Q38.** (pull request 1) One answer per message and atom: A = `message about "invoices"`, B = A and `received since 2026-01-01T00:00:00Z`; with one `Answers` for the message, `Eval([B]) ≤ Eval([A])` for every probability of the atom; `subject about "x"` and `message mentions "x"` are two atoms with two answers; an atom in two groups and negated reads one answer; a missing answer is listed once in `Result.Model`. Step 4 adds the same test on its kept answers: one key per message, content hash, atom, model and prompt version, the formula in none.
- **Q39.** (pull request 1) The consented scope is evaluated on its own: on generated messages `Eval([A, B])` is the lesser of `Eval([A])` and `Eval([B])`; with Q37's A and B and the `"bad local"` message, `Eval([A, B])` is unknown and delivers nothing; with a test double of `Contains` that says yes to everything, nothing evaluated with A is ever true where A is not. Step 5 adds: a request B1 accepted because it fit A, then B2 that fits B1 but not A, asks for consent.
- **Q40.** (pull requests 1 and 3) No decision from a cut text: an entry of 16,377 spaces then `invoice`, cut, whose full text goes on `s`; and an entry ending in `cafe`, cut, whose full text goes on with U+0301: `body contains "invoices"`, `body contains "cafe"` and their negations are all unknown with `FromText` in `Needs` before the raw message is read; after it, `body contains "invoices"` is true and `body contains "cafe"` false, and their negations the reverse.
- **Q41.** (pull request 3) No classification from an entry alone: a message with 101 `From` addresses (`Partial`) and `not from_address is alice@example.com and body contains "hello"` with a text holding "hello" is unknown, not true; a Gmail confirmation whose text matches a body condition is never listed, even when its entry is read first by a test that forces it; an entry whose item is not there is never read or listed; `Eval` without `FromMeta` is unknown with `FromItem` and `FromMeta` in `Needs`.
- **Q42.** (pull request 3) Size from the sealed meta: a 1 KiB message whose item's plain size is set to 1,048,576 by the test (as an operator could) is false for `size at_least 1048576` and true for `size below 2048`, and the item's size is never read for it; a meta of format 1 makes both unknown; a meta of format 1 whose raw message is read for a cut body uses the raw message's length.
- **Q43.** (pull request 2) Presentation: mailboxes m1, m2, m3 labelled `A”, or that arrived at your mailbox “B`, `A` and `B` give different sentences for `mailbox is m1` and for `mailbox is m2 or mailbox is m3`, each read back by the parser; `a”b@example.com` is escaped in its quotes; a local part with a Cyrillic `а` among Latin letters is refused at `Parse` (`confusable_value`); values that are template words with quotes and backslashes stay one part; the worst generated sentence is under 100 KiB.
- **Q44.** (pull requests 3 and 4) Raw reads released one by one: in `inbox`, six messages of 25 MiB with cut texts on one page, under a fake `ReadGrants` of 32 MiB, are all read, never with two grants held at once, and the page answers; a read budget that is not a `ReadGrants` is refused with `ErrTooManyRequests` before the first raw read; in `httpapi`, two match pages at once over such mailboxes under the default 128 MiB both answer 200, and a grant released goes back to the budget before the response ends.
- **Q45.** (pull request 4, and step 5) `MatchPage` is answered only by `POST /api/v1/mailboxes/{id}/matches` in a session; step 5's tests assert that no vendor route answers it.
- **Q46.** (pull request 1) Canonical forms are not minimal: `(mailbox is m or subject contains "x") and (not mailbox is m or subject contains "x")` keeps its two groups; `Contains` of it inside `subject contains "x"` is yes, and of `subject contains "x"` inside it is no (consent asked again, never skipped).

## Implementation pull requests

Five. This one first, alone. Then 1, 2 and 3 are written at the same time against the committed types; 1 merges first; 2 and 3 merge after it in either order; 4 last. A release takes all of them: step 3 is done when it is deployed and the staging check of pull request 4 has passed. Every file has one owner; another pull request edits it only where its "May edit outside" names it.

| Pull request | Owns | May edit outside, and only that | Must not touch | Depends on |
|---|---|---|---|---|
| 0. Design and seams (this one) | This ADR; `internal/contracts/query.go` and its test; `internal/query/query.go` and `query_test.go` (`Classify`); `internal/inbox/match.go`; `ReadGrants` in `internal/inbox/read.go`; the `query-is-pure` rule; the TS types in `web/src/lib/api.ts`; docs/plan.md, docs/decisions/index.md, internal/AGENTS.md's row | | Everything else | Nothing |
| 1. The language | `internal/query`: `parse.go`, `normalise.go`, `canon.go`, `implies.go`, `contain.go`, `eval.go`, their tests, benchmarks and fuzz targets; `internal/query/querytest` (generators and the brute force, test only) | `.golangci.yml`'s `no-fakes-in-production` (one line for `querytest`); the `fuzz` target of the makefile (its fuzz targets); `scripts/coverage-thresholds.txt` (a line for `internal/query`, 95 or more) | `internal/inbox`, `internal/httpapi`, the web app; `Classify` and the types of `query.go` except by a change to this ADR | 0 |
| 2. The sentence | `internal/query/sentence.go`, `sentence_en.go`, `present.go` (the presentation of values), their tests and the test-only `parseSentence` | | What 1 owns, except calling it | 0; 1 to merge |
| 3. Stored mail | `internal/inbox/match.go` (beyond the interface), `queryview.go` (`QueryMessage`); in `parse.go`, `deliver.go` and `box.go` the four fields of "What delivery adds", and only those; `inboxtest` writing them; docs/data.md's rows | `internal/inbox/read.go`: the raw read's taking of the budget, so the reads `Matcher` makes go through `ReadGrants` and give their grant back after the text is taken out, and only that | `search.go` beyond sharing the scan's helpers by moving them unchanged; `internal/query` | 0; 1 to merge |
| 4. Routes | `internal/httpapi/queries.go` (new: the check route), the `matches` route in `mailboxes.go` and its registration, the error writer's `details`; in `web/src/lib/api.ts` the two calls and `invalid_formula` in `ApiErrorCode`; `cmd/permaily-enclave` mounting; `TestMailEndToEnd`; docs/web.md (the API), docs/threat-model.md (what a formula may hold and what is logged), docs/acceptance.md (the staging check), docs/testing.md | `ApiErrorCode` in `api.ts`; in `internal/httpapi/mailboxes.go` the read budget (`heldBudget`, `byteBudget`), to implement `inbox.ReadGrants` | `internal/query`, `internal/inbox` | 1, 2, 3 |

**Definition of done of each.** Its tests in "Tests" pass (1: Q1 to Q19, Q26, Q37 to Q40 and Q46 in `query`; 2: Q20 to Q25 and Q43; 3: Q26 to Q32, Q40 to Q42 and Q44 in `inbox`; 4: Q34 to Q36, Q44 and Q45; Q33 passes now); `make check` passes, coverage thresholds included; `make fuzz` runs its fuzz targets; internal/AGENTS.md and the docs it owns say what it built; an adversarial review of its code, every finding a test named for it. For 4, also: a staging image where a mailbox receives mail (with a `List-Id`, an attachment and a text past 16 KiB), and `queries/check` and `matches` answered through the app with a session, recorded in docs/acceptance.md.

**What truly runs in parallel.** 1, 2 and 3 need only the committed types: 2 renders a `Canonical` it can build in its tests by hand until 1's generators merge, then switches to them; 3 builds `Matcher` and fills `query.Message` against `Eval`'s contract with a stand-in in its tests, and merges after 1 to run Q27 against the real `Eval`. 4 waits for the three.

**Why the language goes in one pull request and the sentence in another.** Parsing, simplification, containment and evaluation share the implication table and its proofs, and one reviewer must hold all of them; the sentence is a separate question (is every template right, is it injective, can a value inject) with its own reviewer, the designer included.

## Options considered

- **A text syntax as the wire form** (Gmail's): rejected above ("No text syntax"), by the owner's decision.
- **Arbitrary nesting** (any tree of AND, OR and NOT): ADR 0019 fixed AND of OR; a tree has no canonical form short of normal forms that grow exponentially, and its sentence needs brackets.
- **Two values, with unknown read as false**: a protecting rule would let a message go when its data could not be read, against ADR 0019. Three values, resolved by mode.
- **Whole words for `contains`** (the first version of this design's recommendation): "car" would not match "scar", and the sentence could say "the word". The owner chose substrings ("The owner's decisions", 3): a person who types "invoice" means "invoices" too, and the sentence says "contains the text".
- **A connection widened without consent evaluated as one formula, `B AND A` canonicalised** (this design's first version, with an `And` function): simplification would delete A's groups whenever B fits inside A, which is the very case it guards; formulas evaluated together instead.
- **Kept model answers keyed by the formula** (this design's first version): two formulas holding one atom could get two answers, and containment would be unsound; keyed by the message and the atom instead.
- **The item's plain size for `size`**: whoever writes the table sets it.
- **The `Date` header for `received`**: the sender writes it.
- **Relative times**: rejected above.
- **A condition on any header**: needs the raw message of every message considered.
- **Simplification to a minimal or unique form for every equivalent formula** (prime implicates): exponential in general, and not needed for faithfulness or soundness.
- **Containment by a solver** (SAT on the formulas' atoms): complete in two values, but three values and the relations between conditions need encoding, it is a dependency in the image, and its worst case is not bounded as a request needs. The proven-or-no test is quadratic.
- **The language inside `internal/domain`**: rejected above.
- **The sentence written by the web app**: a second implementation of the templates, in another language of programming, that must agree with the server's to the byte; receipts and MCP need the server's anyway.
- **A sentence shown in the person's time zone now**: accounts store no zone yet; UTC is exact and said.
- **Mapping the inbox's `q` to a formula**: rejected above.
- **An index of senders or words for evaluation**: rejected for the inbox's search (ADR 0026) for what it shows whoever reads the table; the scan is bounded instead.

## The owner's decisions

Asked when this design's pull request opened, and answered by the owner on 2026-11-14, before any formula is stored. Since the canonical bytes and what they mean are stable across releases ("The canonical form, and its identifier"), changing the matching semantics later needs a new formula version.

1. **Which variables are in the first version?** Decided: all fifteen of the table, as recommended. `list` and `size` work only for mail received from this release on (older mail reads them as unknown, so a query asking for newsletters misses older ones and a rule holding them back holds older mail too); `mailbox` lets a vendor target one mailbox, which the consent screen must then name.
2. **Is `mentions` a second operator a model decides?** Decided: yes, on `message` only, as recommended; ADR 0019 changes ("`mentions`, the second operator a model decides"). One more kind of statement for the model and the spike of step 4 to measure.
3. **Does `contains` match whole words?** Decided: **no, any piece of text** (a substring), against the recommendation: "invoice" finds "invoices", and "car" also finds "scar". What it changed here: "Texts, and where they match"; the substring row of the implication table and "Texts, as substrings"; the rewrites' examples; "Texts, re-proven for substrings" in "Containment"; the templates ("contains the text"); "Which reads" (no decision from a cut text); the inbox's `q` paragraph; Q8, Q10, Q16 to Q18 and Q40.
4. **Do vendors get a text syntax?** Decided: no, JSON only, as recommended; the connection's "File" view shows the canonical JSON (or YAML of it) instead of the sample's Gmail syntax.
5. **Which languages does the sentence have?** Decided: English only in this step, Spanish next as another template table, as recommended. A person whose account says Spanish reads the sentence they consent to in English until then.
6. **Does a rule match when any of its criteria does?** Decided: yes, as the editor shows and as recommended, which amends ADR 0019's "a rule is a formula" to "one or more formulas".

## Changed after the first design review (2026-11-14)

A design review of this pull request at `5986137` found the three-valued structure and the proof of containment sound (no structural counterexample in 5,535 assignments over 615 accepted containments), and ten defects in the assumptions the proof needs; each is changed in place above, with the reviewer's counterexample as a numbered test. With the owner's answers, in short:

- An address and its domain share one validity decision, made once per address of the message; an address that fails it makes every condition over it unknown (Q37).
- A model's answer is kept per message, content hash and atom (variable, operator, topic, model, prompt version), never per formula; `query.Atom` and `query.Answers` committed (Q38).
- A connection keeps its consented scope A as a formula of its own; a request B accepted because it fits is evaluated beside it, never simplified with it; later requests are compared with A; `And` removed, `Eval` and `Matcher` take the formulas evaluated together (Q39).
- No condition on a body is decided from a cut entry, for true or false: the raw message is read first (Q40).
- No message is classified before its item and sealed meta are read; the entry is read by an item's identifier, for the text only; the read costs restated (250 to 500 messages a page) (Q41).
- `size` comes from the sealed meta (delivery records it, format 2), or is unknown; never from the item's plain size; no longer always known, so out of the disjoint and covering facts (Q42).
- One presentation per value type, escaping quotes and backslashes, the confusable check on local parts too, labels escaped; the faithfulness claim restated (structured sentence injective by construction, plain text checked by parser and adversarial generators); the sentence's bound corrected to under 100 KiB (Q43).
- Raw reads release their reservation one by one: `inbox.ReadGrants` committed; pull request 3 uses it in the reader, pull request 4 implements it in `internal/httpapi` (Q44).
- `query.Classify` committed with its test (Q33); evaluation takes the answers as input, a missing one "needs a model", duplicate atoms one answer.
- `MatchPage` is the owner's preview only, said in the ADR and its doc comment; a vendor's result is step 5's own contract (Q45).
- Notes: canonical forms are not minimal, which only ever asks for consent again (Q46); no timing is asserted, benchmarks report it, and the normaliser's fixed point is checked and its rounds bounded; `query-is-pure` lists the standard packages it needs instead of `$gostd`.
- The owner's six answers recorded as decided ("The owner's decisions"); `contains` is a substring, which changed normalisation, the implication table, simplification's examples, containment's cases, the templates, evaluation of a cut text and the tests.

## What it rests on

- ADR 0019 (the formula, the sentence, simplification, containment, rules, when in doubt), 0020 (the thresholds, exact conditions first, kept decisions, topics shown as they are), 0026 (the stored format, the scans, the budgets, the access table, the API's rules), and the screens in `web/src/data/sample.ts`, `web/src/data/queries.ts`, `web/src/pages/connections/`, `web/src/pages/rules/` (read on 2026-11-14).
- The repository on 2026-11-14: `golang.org/x/text` v0.43.0 and `golang.org/x/net` v0.62.0 are direct dependencies (`go.mod`); depguard's `domain-is-pure` allows only the standard library, `contracts` and `domain` (`.golangci.yml`); message identifiers are the receipt's nanosecond in 16 hex digits and 16 random ones (`inbox.NewMessageID`), and the claim's `received_at` is the same instant (`inbox/deliver.go`); the meta's seal names the mailbox and the message's identifier (`envelope.AAD(PurposeMeta, mailbox, id)`, `inbox/read.go`); the item's plain size is written beside the sealed meta, by whoever writes the table (`inbox.RawReadMax`); the search entry keeps the first 16 KiB of text and discards whether it was cut (`inbox/parse.go`), and holds neither `Partial`, `Unparsed` nor `Confirmation` (`inbox.SearchEntry`); `StoredMeta` has no `List-Id` and no size; `inbox.ReadBudget` has only `Take`, and the adapter holds what it took to the end of the response (`httpapi.heldBudget`); a mailbox's label is any printable line of up to 64 characters (`domain.MailboxLabel`).
- Kleene's strong logic of three values, and that its AND and OR are the least and the greatest in false < unknown < true; that a map of that order onto false < true that keeps the order keeps both (so resolving unknown at the end equals resolving each condition).
- Unicode: NFKC (UAX #15), the derivation of `Default_Ignorable_Code_Point` (UAX #44, `DerivedCoreProperties.txt`), simple case folding (`CaseFolding.txt`, status C and S, as Go's `unicode.SimpleFold` follows it), the `Quotation_Mark` property (`PropList.txt`, Go's `unicode.Quotation_Mark`), UTS 46's lookup processing, and UTS 39's mixed-script concern, of which only the Latin, Greek and Cyrillic case is taken; that UTF-8 is self-synchronising, so a match of valid UTF-8 bytes is a match of whole code points.
- RFC 5322 (address syntax and quoted local parts), RFC 2919 (`List-Id`), RFC 3339 (times), RFC 6376 (DKIM's `d=`), RFC 6838 (media types' top-level names).
- That containment of formulas of this shape is decidable but co-NP-hard in general (it is the validity of an implication between CNF formulas), which is why the test is sound and not complete.
