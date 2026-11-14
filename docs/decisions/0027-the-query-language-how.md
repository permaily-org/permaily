# 0027 The query language: how it is built

Status: Proposed. Designed on 2026-11-14 for step 3 of F2 (docs/plan.md); not built. To be reviewed before its implementation pull requests start. Amends ADR 0019 (a rule is one or more formulas, "Rules: any of their criteria"), ADR 0020 (how its thresholds read a negated `about`, "What a formula means") and ADR 0026 (what delivery records for a message, "What delivery adds"). Its open product choices are in "Questions for the owner"; the design below assumes the recommended answer of each and says what changes with the other.
Date: 2026-11-14

## Context

ADR 0019 decided what a query is: an AND of groups, each an OR of conditions; a condition is a variable, an operator and a value, and can be negated; `about` is the one operator a model decides (ADR 0020); the system writes the sentence a person consents to from fixed templates, with no model; it simplifies a formula by logic alone and never touches `about`; whether one formula fits inside another decides whether asking for more needs fresh consent; an agent's search AND its approved scope is again a formula; a rule is a formula with an effect; and when in doubt, nothing leaves. It left to design "the list of variables and operators, and the grammar that vendors send".

This record says how, exactly enough that the language, the sentence, the evaluation over stored mail and the routes can be built at the same time by different people against types that are already in the repository ("The contract and the seams"). Steps 4 (the model), 5 (connections, consent, the API and MCP), 7 (rules) and 8 (receipts) build on it; it builds none of them.

It builds on how mail is stored (ADR 0026): a message's item with its sealed meta (`inbox.StoredMeta`), its sealed search entry (`txt#`, `inbox.SearchEntry`: senders, subject, the first 16 KiB of the text, when it arrived), its raw object read under the read budget, message identifiers that start with the time they were received, list and search pages bounded by items read with cursors that skip nothing, the four search slots, thirty search pages a minute per account, and the one-minute deadline of a request. It keeps their rules: nothing that decides anything lives only in one enclave's memory; logs hold nothing of a person's mail, and now nothing of a formula either.

## Decision in short

- **Fifteen variables, nine operators**, each evaluated over what Permaily stores and nothing else, and each seeing exactly what a connection would be given: the subject as stored, the body as the text a connection receives (up to 256 KiB). Exact: addresses and domains of senders and recipients, DKIM signers, words in the subject, the body and attachment names, attachments and their types, the mailing list, when it was received, its size, its mailbox. A model decides `about` and `mentions`. ("Variables and operators")
- **Vendors send JSON**, `{"v":1,"all":[{"any":[{"var","op","value","not"}]}]}`, read strictly (no unknown or repeated member), with fixed limits (32 groups, 64 conditions, 8 a model decides), every refusal a fixed reason and a path into the formula, never an echo of a value. No text syntax in the first version. The inbox's search `q` stays as it is, apart. ("The grammar vendors send")
- **A formula means a value in three**: true, false, or unknown, in Kleene's logic. Unknown is data that could not be read, or a model not asked, not answering or unsure. A query delivers only on true; a rule, every one of which protects, applies on true or unknown. ("What a formula means")
- **Simplification is a fixed set of rewrites** sound in that logic: duplicates, absorption by implication between conditions, ranges merged, and contradictions and tautologies only where a variable is always known. It always ends, and gives one canonical form for every reordering and respelling of a formula, which is identified by a hash. Conditions a model decides are touched only when they are identical. ("Simplification")
- **Containment is proven or refused**: B fits inside A when every group of A is implied by one group of B, condition by condition. Sound in the three-valued logic, quadratic, and "no" whenever it cannot prove "yes", which means fresh consent. A connection widened without consent is still evaluated as B AND A. ("Containment")
- **The sentence is a function of the canonical formula** and of fixed context (the language, the account's mailbox names), written from a table of templates per variable, operator and sign, with every value quoted, isolated and checked. It is injective: a parser in the tests reads every generated sentence back to its formula. English now; another language is another table that passes the same tests. ("The sentence")
- **Evaluation over stored mail reads the least it can**: a message's item first, then its search entry or its meta, then its raw text only when a body condition still depends on it; the time in message identifiers bounds the scan for `received`; pages are bounded by items read, raw reads and a soft deadline, and consider each message whole or leave it for the next page. `about` is left to step 4 as "needs a model". ("Evaluation over stored mail")
- **One pure package, `internal/query`**, a layer of its own like `internal/mailhtml` because it normalises with `golang.org/x/text` and `golang.org/x/net/idna`; the contract in `internal/contracts/query.go` and `web/src/lib/api.ts`; the matcher in `internal/inbox`; two routes of the web app's API. ("The contract and the seams")
- **Five pull requests**: this one (design and seams), then the language core, the sentence and stored mail written at the same time, then the routes. ("Implementation pull requests")

## Variables and operators

### What a variable may be

A variable is in the first version only if it can be evaluated over what Permaily stores, at a cost bounded per message, and if what it looks at is no more than what a connection with every part would receive. So a condition never decides on something that cannot leave, and what "the body" means in a query is what "the body" means in what is delivered.

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

"Read from" names the source evaluation reads it from ("Evaluation over stored mail"): the item's plain fields and identifier (item), the sealed search entry (entry), the sealed meta (meta), the text of the raw message (text). "Always known" variables are read from the item and are never unknown; every other one may be ("What a formula means").

| Variable | Means | Operators | Value | Read from | Always known |
|---|---|---|---|---|---|
| `from_address` | An address of the `From` header | `is` | An address | entry or meta | No |
| `from_domain` | The domain of an address of the `From` header is the value or one of its subdomains | `is` | A domain | entry or meta | No |
| `recipient_address` | An address of the `To` or `Cc` header | `is` | An address | meta | No |
| `recipient_domain` | As `from_domain`, for `To` and `Cc` | `is` | A domain | meta | No |
| `signed_by` | A DKIM signature that passed at delivery has a `d=` that is the value or one of its subdomains | `is` | A domain | meta | No |
| `subject` | The subject as stored (at most 2 KiB, ADR 0026) | `contains` (exact), `about` (model) | Words; a topic | entry or meta | No |
| `body` | The message's text as a connection receives it: the text/plain part, or the text taken out of the HTML, at most 256 KiB (`inbox.MessageTextMax`) | `contains` | Words | entry, then text | No |
| `message` | The subject and the text together, as the model is given them | `about`, `mentions` (both model) | A topic | (step 4) | No |
| `attachments` | It has at least one attachment | `exists` | none | meta | No |
| `attachment_name` | The name of an attachment, as the sender wrote it | `contains` | Words | meta | No |
| `attachment_type` | The media type of an attachment, as the message declared it | `is` | `type/subtype` or `type/*` | meta | No |
| `list` | The identifier of its `List-Id` header (RFC 2919) | `is`, `exists` | A list identifier | meta, for mail stored from this step on | No |
| `received` | When Permaily received it: the time in its identifier, which is the claim's `received_at` (ADR 0026) | `since` (on or after), `before` (strictly before) | An RFC 3339 time | item | Yes |
| `size` | The size of the message as received, in bytes | `at_least`, `below` | Bytes | item | Yes |
| `mailbox` | The mailbox it arrived at | `is` | A mailbox identifier of the account | item | Yes |

A condition on a variable with several values (the addresses of a header, the attachments, the signatures) holds when **one** of them satisfies it, and its negation when **none** does: "not sent from x" means none of the senders is x. Two conditions are never bound to the same value: "an attachment named invoice" and "an attachment of type application/pdf" may hold by two different attachments. That is said in the sentence by its words ("with an attachment whose name…", "with an attachment of type…"), and is the price of keeping every condition one variable, one operator, one value.

`from_address` and `from_domain` say what the message says. Anyone can write a `From` header; `signed_by` is the one variable a sender cannot claim without the domain's key. The sentence of a `from_` condition says "sent from", never "from the company"; the consent screen of step 5 may suggest `signed_by` next to a `from_domain`, which is product work for then.

### Values, and how each is normalised

A value is normalised by `Parse` before anything else uses it, and the field of a message by `Eval` in the same way, so that comparing is comparing bytes. What is refused is refused with `invalid_value` unless said otherwise.

- **Text** (the values of `contains`, and topics), and the fields they are compared with. In order: Unicode NFKC (`golang.org/x/text/unicode/norm`); every default-ignorable code point removed (derived as Unicode does, from the standard library's tables: `Other_Default_Ignorable_Code_Point`, `Cf` and `Variation_Selector`, less `White_Space`, `Prepended_Concatenation_Mark` and U+FFF9 to U+FFFB), which takes out zero-width characters, soft hyphens and every bidirectional control; simple case folding to one member of each orbit of `unicode.SimpleFold`: the lower-case of the orbit's smallest member when that is in the orbit, otherwise the smallest (so `A` and `a` are `a`, `Σ`, `σ` and `ς` are `σ`, `K` and the Kelvin sign are `k`, `ß` and `ẞ` are `ß`, and `İ` and `ı` stay themselves, as the inbox's search keeps them); every run of white space, line and paragraph separators included, made one space, and the ends trimmed. The steps are applied until nothing changes (a test runs them over every code point and fuzzes them; one round is expected to suffice). A field's invalid UTF-8 becomes U+FFFD first; a value's is refused. A value, after normalisation, has 1 to 100 characters and at most 400 bytes (`value_too_long`), at least one letter or digit, no control, private-use, surrogate or unassigned code point and no format character left, and neither `“` nor `”`, which the sentence quotes with. A word of a value (a run without spaces) whose letters are of more than one of Latin, Greek and Cyrillic is refused (`confusable_value`): those are the scripts whose letters look alike, and a vendor's “invоice” with a Cyrillic `о` would read as “invoice” to the person and match other mail.
- **Words, and where they may match** (`contains`). The value matches the normalised field where its bytes appear and, at each end of the value whose character is a letter, a mark or a decimal digit of a script that separates words with spaces, the character beside it in the field is not such a character (or the field ends there). So `invoice` matches "your invoice," and "INVOICE-22" and not "invoices"; `car` does not match "scar" or "careful"; `.pdf` matches "bill.pdf"; `Calle Falsa 123` matches it across a line break. Han, Hiragana, Katakana, Thai, Lao, Khmer and Myanmar letters need no boundary, since those scripts do not separate words with spaces. That `contains` is whole words is a product choice ("Questions for the owner", 3).
- **Domains** (the values of `from_domain`, `recipient_domain`, `signed_by`, and the domain of an address). A trailing dot removed; then UTS 46 to ASCII with `golang.org/x/net/idna`'s lookup profile (non-transitional, STD3 rules, the hyphen, joiner and bidi checks); lower case; at least two labels, each of at most 63 bytes, at most 253 in all. A label whose Unicode form mixes Latin, Greek and Cyrillic is refused (`confusable_value`). The canonical value is the ASCII form (`xn--…` for a Unicode label). A message's domain that fails these steps makes the condition unknown for that address, never false.
- **Addresses** (`from_address`, `recipient_address`). Split at the last `@`; the domain as above; the local part with its quotes and quoting backslashes removed, NFC, then the case folding above, 1 to 64 bytes, printable, with no space, `"` or control. A `+tag` is part of the local part and is kept. A message's address is normalised the same way; one that cannot be makes the condition unknown for it.
- **Media types** (`attachment_type`). Lower case; the type one of `application`, `audio`, `font`, `image`, `message`, `model`, `multipart`, `text`, `video`; the subtype `*` or 1 to 127 of `a`–`z`, `0`–`9` and `!#$&^_.+-`. A message's type is cut at its first `;`, trimmed and lowered; an unlisted or malformed one matches only the conditions it equals, never `type/*` of another type.
- **List identifiers** (`list`). The value inside the angle brackets of `List-Id`, lower case, 1 to 255 bytes of the characters RFC 2919 allows. The value is given without brackets.
- **Times** (`received`). RFC 3339 with seconds and an offset (`Z` or `±hh:mm`), no fraction, between 2000-01-01 and 2100-01-01; canonical in UTC, `2006-01-02T15:04:05Z`. Compared with the identifier's time to the nanosecond.
- **Sizes** (`size`). A decimal integer from 1 to 33,554,432 (32 MiB) without sign, leading zero or separator.
- **Mailboxes** (`mailbox`). `mbx_` and 26 base32 characters, and one of the account's mailboxes when the formula is checked for a person or a connection (`unknown_mailbox` otherwise). A mailbox disconnected later stays a valid value: it matches nothing.
- **Topics** (`about`, `mentions`). Text as above, at most 100 characters: short, one line, shown as it is (ADR 0020). Two topics are the same condition only when their normalised bytes are equal; the model is given the normalised topic.

### `mentions`, and the second operator a model decides

ADR 0019 named `about` the only operator a model decides. The rule editor's screens use a second one, "mentions" ("Message mentions my home address"), and they are not the same question: a message about a delivery may mention a home address in passing. This design adds `mentions` on `message` as a second opaque operator, exactly like `about` for every part of the language: never rewritten, never related to another topic or to `about`, a separate statement to the model. Whether it is in the first version is the owner's ("Questions for the owner", 2); without it, the editor's "mentions" becomes `about`, and the language loses nothing else.

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

- `v` is required and is `1`. A later version that adds variables or operators gets a new number; this release refuses any other (`unsupported_version`), and a version-1 reader refuses a variable or an operator it does not know (`unknown_variable`, `unknown_operator`), never ignores one: a condition dropped would widen a query.
- `all` is a list of groups and may be empty: the formula then matches every message ("All mail"). A group is `{"any": [...]}` with at least one condition (`empty_group`).
- A condition is `{"var", "op", "value", "not"}`. `value` is always a string, a time and a size included, and is absent for `exists` (`unexpected_value`) and required for every other operator (`missing_value`). `not` is optional, `false` when absent.
- **Read strictly.** One JSON object of at most 64 KiB (`too_large`), UTF-8 without a byte order mark, nothing after it (`not_json`); every member of the right type, never `null` (`wrong_type`); no member this version does not have (`unknown_field`), and no member twice in one object (`duplicate_field`): `encoding/json` keeps the last of two, and a vendor's parser may keep the first, so a formula two readers read differently is refused. The reader is a token walk over `encoding/json`'s `Decoder`, so no other parser is added to the image. Nesting is fixed: a group inside a group is `wrong_type`.

### Limits

| Limit | Value | Why |
|---|---|---|
| The JSON as sent | 64 KiB | Bounds parsing before anything is counted |
| Groups | 32 | |
| Conditions in a group | 32 | |
| Conditions in a formula | 64 | Bounds simplification and containment ("Simplification", "Containment") and the sentence (about 8 KiB at most) |
| Conditions a model decides | 8 | Each is a statement put to the model for every message the exact conditions leave open (ADR 0020) |
| A word, a phrase or a topic | 100 characters, 400 bytes after normalisation | Short enough to read in a sentence and to send to the model |
| An address, a domain, a type, a list | Their own bounds above | |
| A formula the system makes | Twice the above | An agent's search AND its scope ("Containment") |

They are constants of `internal/contracts/query.go`, so the API, MCP's schemas (step 5) and the web app read the same numbers.

### Errors

A refused formula is `400` with `{"error": {"code": "invalid_formula", "message": <fixed>, "details": {"at": <path>, "reason": <reason>}}}`. `at` is a path into the formula as sent (`all[2].any[0].value`, or `""` for the whole); `reason` is one of `contracts.FormulaReason`: `not_json`, `too_large`, `unknown_field`, `duplicate_field`, `wrong_type`, `unsupported_version`, `too_many_groups`, `too_many_conditions`, `too_many_model`, `empty_group`, `unknown_variable`, `unknown_operator`, `operator_not_allowed`, `missing_value`, `unexpected_value`, `invalid_value`, `value_too_long`, `confusable_value`, `unknown_mailbox`, `never_matches`. The first problem in the order of the formula as sent is answered, so the answer does not depend on the order checks run in. No error holds a value of the formula, so an error page or a log of one shows nothing a vendor or a person wrote. `invalid_query` stays the inbox search's code.

`never_matches` refuses a formula that simplifies to no message at all (`received since 2026-03-01` and `received before 2026-02-01`): a connection that asks for nothing is a mistake the vendor should hear of. A formula the system makes (a search AND its scope) may be "never", and then finds nothing.

### The canonical form, and its identifier

The canonical form ("Simplification") is a `contracts.Formula` too, written by `encoding/json` from the struct in its field order, with no space, `value` absent for `exists` and `not` absent when false; the same bytes in every enclave and release that reads version 1. Its identifier is `qry_` and the first 16 bytes of its SHA-256 in lower-case base32 without padding (26 characters). The identifier names a formula in receipts (step 8) and in the model's kept decisions (step 4); it is never logged, since a hash of a short formula can be guessed, and would say what a connection asks for.

### The inbox's search `q`: kept apart

ADR 0026's search is a person's search box: words that must each appear, as substrings, anywhere in the senders, the subject or the first 16 KiB of the text. This language is what vendors are held to: whole words, one variable each, three values, a sentence. Unifying them would change what the inbox's search finds, and mapping `q` to a formula would need a variable "any of senders, subject and text" with substring matching that nothing else wants. So `q` stays as it is, with its code and limits, and the two share code below the line only: `inbox`'s bounded scan with its cursor, which `Match` reuses ("Evaluation over stored mail"). An agent's free-text search through MCP (step 5) is turned into a formula there (a word becomes a group of `subject contains` and `body contains`), in the open, where its sentence shows what it became.

### No text syntax in the first version

Vendors send JSON. Agents build it through MCP's tool schemas, which step 5 derives from these structs; apps build it in code. A text syntax (`from:holded.com subject:invoice`) would be a second reader to keep strict, with its own quoting and precedence for a vendor to get wrong; the sample's "File" view shows the canonical form instead ("Departures from the sample screens"). Whether vendors get one is the owner's ("Questions for the owner", 4).

## What a formula means

### Three values

On a message, each condition has a value in Kleene's strong logic: false < unknown < true. A group's value is the greatest of its conditions', a formula's the least of its groups'; an empty formula is true, and a formula that is "never" false. A negated condition's value is the reverse of the condition's (true and false swap, unknown stays).

- An **exact** condition is unknown when the data it needs could not be read: a source not read yet ("Evaluation over stored mail"), or that cannot be read (a meta that does not open, a raw message past its bound), an address or domain of the message that does not normalise, a list of addresses or attachments that a parse limit may have cut (`Partial`) and where the value is not found, a header that could not be read (`Unparsed`), the `list` of a message stored before this step. It is never unknown when its data was read: "the subject does not contain invoice" is false or true, not unknown.
- A condition a **model** decides is unknown until the model is asked (step 4), when it does not answer, and when its answer falls in the band of doubt of the mode.

### What a query and a rule do with unknown

The value is acted on by mode (`query.Resolve`):

- **For a query** (a connection's query, an agent's search AND its scope): a message is delivered only when the value is **true**. Unknown delivers nothing.
- **For a rule**: every rule protects (it holds a message back or removes details of it, ADR 0019), so it applies when the value is **true or unknown**.

That is ADR 0019's "if a condition cannot be decided, it does not match for a query, and it matches for a rule that protects", made exact: it is decided once for the whole formula, and it equals deciding each condition that way on its own, because AND and OR of three values commute with both ways of reading unknown (each is a homomorphism of the lattice false < unknown < true onto false < true). Rules run first, on every message; the query runs on what they left (ADR 0019).

### The bands of a model

ADR 0020 set the thresholds: a query matches `about` at 0.80 or more; a rule that protects holds at 0.30. In three values, with `p` the model's probability:

| Mode | True | Unknown | False |
|---|---|---|---|
| Query | `p` ≥ 0.80 | 0.20 < `p` < 0.80 | `p` ≤ 0.20 |
| Rule | `p` ≥ 0.70 | 0.30 ≤ `p` < 0.70 | `p` < 0.30 |

So a query's `about` delivers at 0.80 and its negation only at 0.20 or less; a rule's `about` applies from 0.30 and its negation below 0.70. Each reading is on the side of not delivering, which is what ADR 0020 decided for `about` and which this extends to `not about` and `mentions` (an amendment of 0020: it named no threshold for a negation). Step 4 builds it; this step defines it, since it is what the logic below must hold for.

### Rules: any of their criteria

The rule editor's screens (`web/src/pages/rules/`) match a rule when mail meets **any** of its criteria, and a criteria when mail meets **every** one of its conditions, a condition holding when the field has **any** of its values. That is one formula per criteria: each condition with values `v1…vn` is the group `{v1 or … or vn}`, a negative one is the groups `{not v1}`, …, `{not vn}`; the criteria is the AND of its groups. A rule is then **one or more formulas**, and applies when one of them does: ADR 0019's "a rule is a formula with an effect" becomes "a rule is one or more formulas with an effect" (an amendment, "Questions for the owner", 6). Containment is never asked of a rule; a rule's sentence is its formulas' sentences, introduced by "Any of these:".

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
| `X contains p` | `X contains q`, the same `X` (`subject`, `body`, `attachment_name`) | `q` matches `p` by the rule of words above, with `p`'s two ends taken as ends of a field |
| `attachment_name contains p` | `attachments exists` | always |
| `attachment_type is t1` | `attachment_type is t2` | `t1` = `t2`, or `t2` is `T/*` and `t1` is of type `T` |
| `attachment_type is t` | `attachments exists` | always |
| `list is x` | `list is y` | `x` = `y` |
| `list is x` | `list exists` | always |
| `received since t1` | `received since t2` | `t1` ≥ `t2` |
| `received before t1` | `received before t2` | `t1` ≤ `t2` |
| `size at_least n1`, `size below n1` | the same operator with `n2` | as for `received` |
| any condition | the same condition | always (for a model's conditions, only this) |

And for negated ones, `not ℓ1 ⇒ not ℓ2` exactly when `ℓ2 ⇒ ℓ1`: reversing the values reverses the order. Each pair holds in three values because both sides read the same source and are unknown for the same reasons: for the lists of addresses, each is true when one known entry satisfies it, otherwise unknown when the list may be incomplete or an entry did not normalise, otherwise false, so a test on each entry that implies another implies it on the list. The "is a word of" rule makes `X contains "invoice 2026"` imply `X contains "invoice"`: wherever the longer matches, the shorter matches inside it with the same boundaries.

**Only for the variables that are always known** (`received`, `size`, `mailbox`), two-valued facts are used too:

- `received` and `size` conditions are made positive: `not received since t` is `received before t`, `not received before t` is `received since t`, and the same for `size`'s `at_least` and `below`.
- **Disjoint**: `since s` and `before t` with `t` ≤ `s`; `at_least m` and `below n` with `n` ≤ `m`; `mailbox is m1` and `mailbox is m2` with `m1` ≠ `m2`; a condition and its negation.
- **Covering**: `before t` or `since s` with `s` ≤ `t`, and the same for `size`; a condition or its negation.

No other relation is used: no two topics, `about` and `mentions`, two variables, or a condition and its negation outside those three variables (in three values `X or not X` may be unknown, and `X and not X` may be unknown, which a rule acts on).

### The rewrites

`Canonicalise` first normalises every value and makes `received` and `size` conditions positive, then applies these until none applies:

1. **Duplicates in a group** are one.
2. **Absorption in a group**: of `ℓ1 or ℓ2` with `ℓ1 ⇒ ℓ2`, keep `ℓ2` (the greater of the two is their OR). This merges ranges in a group: `since t1 or since t2` keeps the earlier time.
3. **A group that always holds** goes: it has two covering conditions of an always-known variable (`before t or since s` with `s` ≤ `t`; `mailbox is m or not mailbox is m`). A formula left with no group matches every message.
4. **Duplicates of groups** are one.
5. **Absorption between groups**: when every condition of group `G1` implies a condition of group `G2`, `G1 ⇒ G2` and `G2` goes (`G1 and G2` is `G1`). This merges ranges across groups: `since t1` and `since t2` keep the later time.
6. **A lone condition rules out the conditions disjoint from it**: when a group is one condition `ℓ` of an always-known variable, every condition disjoint from `ℓ` is taken out of every other group (`ℓ and (ℓ' or R)` is `ℓ and R` when `ℓ` and `ℓ'` are disjoint; it holds in three values because `ℓ` is never unknown). A group left empty makes the formula "never": `since s` and `before t` with `t` ≤ `s`, or `mailbox is m1` and `mailbox is m2`.

Then the conditions of each group are sorted by variable (the order of the table), operator (the order of `contracts.Operator`), sign (positive first) and the bytes of the value, and the groups by their sorted conditions, compared one by one, a shorter group first when one is the start of the other.

A condition a model decides is touched by rules 1, 2, 4 and 5 only as "the same condition": duplicates go, and a group that holds another's identical topic is absorbed. Nothing proves two different topics mean the same (ADR 0019), and nothing relates a topic to an exact condition.

### What it guarantees

- **It ends.** Rules 1, 2, 4 and 5 take out a condition or a group, rule 3 a group, rule 6 a condition; making ranges positive happens once, before. So the number of rewrites is at most the number of conditions plus the number of groups (at most 64 + 32 for a vendor's formula). Each pass compares pairs of conditions, at most 64 × 64, and a pair of words costs at most 400 × 400 byte comparisons: the worst case is bounded and small (a test asserts it under 10 ms, "Tests", Q14).
- **It keeps the meaning**: every rewrite gives the same value in three values on every message, so a query delivers the same mail and a rule applies to the same mail.
- **One form for every spelling and order.** Values are normalised before anything compares them, and the rewrites run on the sorted set of groups of sorted conditions in a fixed order, so reordering conditions or groups, repeating them, or spelling a value another way that normalises the same gives the same bytes, and so the same identifier and the same sentence.
- **It is idempotent**: the canonical form of a canonical form is itself, since no rewrite applies to it and sorting a sorted form changes nothing.
- **What it does not guarantee**: that two formulas that mean the same give the same canonical form. Deciding that for formulas of this shape is co-NP-hard in general, and is not needed: what must hold is that the sentence says what the formula does ("The sentence"), and that containment is sound ("Containment"). `subject contains "invoice" or not subject contains "invoice"` stays as it is: in three values it may be unknown.

## Containment

### The question, and where it is asked

"Does B fit inside A?": is every message B matches one A matches? It is asked when a connection approved for A asks for B instead (step 5), and the answer decides whether the person is asked again: "yes" changes the connection without asking; anything else asks. It is not asked of an agent's search: what an agent gets is its search AND its scope (ADR 0019), computed by `And`, which is the union of the two canonical forms' groups, canonicalised; it may be "never", and then finds nothing. It is not asked of rules.

### The algorithm

`Contains(A, B)`, on canonical forms:

1. A is "every message" (no group): yes. B is "never": yes. A is "never", or B is "every message" and A is not: no.
2. For **every** group `GA` of A, look for **one** group `GB` of B such that every condition of `GB` implies some condition of `GA` (the relation of "Simplification"). If every `GA` has one, yes; otherwise no.

### Why it is sound

On any message, in three values: each condition of `GB` is at most a condition of `GA`, so `GB`'s value (their greatest) is at most `GA`'s; B's value (the least of its groups) is at most `GB`'s, so at most `GA`'s, for every `GA`, so at most A's (the least of A's groups). So wherever B is true, A is true (a query delivers nothing more), and wherever B is unknown, A is unknown or true (a rule would apply as much). It never says yes when B can match a message A does not, whatever is unknown.

### Where it says no although the answer is yes

It answers only what it can prove condition by condition, and says no, which means fresh consent, when:

- B's or A's group needs a **model's** condition other than an identical one: `message about "invoices"` does not fit inside `message about "bills"`, nor `about` inside `mentions`.
- The proof needs **several groups of B at once**: B = `{x or y}` and `{not y}` fits inside `{x}` only by resolution between B's groups, which in three values is not valid for a variable that can be unknown, and is not tried for the ones that cannot.
- The proof needs **what the account has**: "not mailbox m1" fits inside "mailbox m2" only while the account has exactly those two mailboxes, which changes.
- The proof needs **how two variables go together in real mail**: an attachment named "x.pdf" is usually of type `application/pdf`, and a message signed by a domain is usually sent from it, but nothing makes either so, and the table relates no two variables but an address and its domain.

Each is on the side of asking the person again: a wrong "no" costs a click, a wrong "yes" would let mail leave without consent.

### Cost

At most `|groups of A| × |groups of B| × |conditions of GA| × |conditions of GB|` implication tests: for two vendor formulas at their limits, at most 64 × 64 = 4,096 tests of conditions, each at most 400 × 400 byte comparisons for two words; well under a millisecond on the enclave. No search, no backtracking.

### What a connection widened without consent is evaluated as

When B fits inside A and the connection becomes B without asking, what runs is B AND A, never B alone. It means the same as B where the containment is right, and keeps every delivery inside what the person approved even if this algorithm had a bug. The receipt (step 8) names both identifiers.

## The sentence

### Its shape

`Render(f, Context)` writes a `contracts.Sentence` from a canonical formula: the plain text (`Text`), and the same words in parts (`Lead`, `Lines`), each part fixed words of a template or a value of the formula, marked when a model decides it, so the web app shows values set apart without reading `Text`. It takes the formula, the language and the account's mailboxes, and nothing else: no time, no clock, no state, no model. The web app never writes a sentence of its own; it shows the server's.

- Every message: `All mail.`
- One group: `Mail ` then the group's conditions joined by `, or `, then `.`: "Mail sent from an address at holded.com or a subdomain of it, or with the word “invoice” in the subject."
- Several groups: the lead `Mail that meets all of these:`, then one line per group in canonical order, each its conditions joined by `, or `; in plain text each line starts with `- ` on a line of its own and the last ends with `.`.
- A rule of several formulas (step 7): `Any of these:` and each formula's sentence.

The groups are lines rather than one sentence because "a or b and c or d" in English does not say where the brackets are; a list of lines, every one of which must hold, does.

### Templates (English)

`{v}` is a value, always quoted with `“` and `”` in plain text, a part of its own in the structure. `{d}` is a domain, `{t}` a media type, `{T}` a time, `{N}` a size and `{M}` a mailbox, written as below and never quoted, since their characters cannot hold a quote or a separator. "(a model decides)" follows every condition a model decides in plain text, and marks its parts in the structure.

| Condition | Positive | Negative |
|---|---|---|
| `from_address is` | sent from “{v}” | not sent from “{v}” |
| `from_domain is` | sent from an address at {d} or a subdomain of it | not sent from any address at {d} or a subdomain of it |
| `recipient_address is` | addressed to “{v}” | not addressed to “{v}” |
| `recipient_domain is` | addressed to an address at {d} or a subdomain of it | not addressed to any address at {d} or a subdomain of it |
| `signed_by is` | signed by {d} or a subdomain of it | not signed by {d} or any subdomain of it |
| `subject contains` | with the word “{v}” in the subject (the words, for a value with a space) | without the word “{v}” in the subject |
| `body contains` | with the word “{v}” in the body | without the word “{v}” in the body |
| `attachments exists` | with attachments | without attachments |
| `attachment_name contains` | with an attachment whose name has the word “{v}” | with no attachment whose name has the word “{v}” |
| `attachment_type is` | with an attachment of type {t}; for `T/*`, with an attachment of any {T} type | with no attachment of type {t}; with no attachment of any {T} type |
| `list exists` | sent to a mailing list | not sent to a mailing list |
| `list is` | sent to the mailing list “{v}” | not sent to the mailing list “{v}” |
| `received since` | received on or after {T} | (made positive: `before`) |
| `received before` | received before {T} | (made positive: `since`) |
| `size at_least` | of {N} or more | (made positive: `below`) |
| `size below` | smaller than {N} | (made positive: `at_least`) |
| `mailbox is` | that arrived at your mailbox {M} | that didn't arrive at your mailbox {M} |
| `subject about` | whose subject is about “{v}” (a model decides) | whose subject is not about “{v}” (a model decides) |
| `message about` | about “{v}” (a model decides) | not about “{v}” (a model decides) |
| `message mentions` | that mentions “{v}” (a model decides) | that doesn't mention “{v}” (a model decides) |

- **Domains** `{d}`: the Unicode form when it differs from the ASCII one, followed by the ASCII form in brackets: "bücher.example (xn--bcher-kva.example)"; otherwise the ASCII form. An address's domain the same way, outside its quotes: “ann@bücher.example” (xn--bcher-kva.example).
- **Times** `{T}`: in UTC, said once at the end ("Times are in UTC."): `1 January 2026` at midnight, `1 January 2026, 09:30` with minutes, `1 January 2026, 09:30:15` with seconds. UTC has no hour twice, so no two times are written alike. Showing them in the person's time zone waits for the account to have one (step 6, the schedule, needs it); the zone then becomes part of `Context`, and a time written in an hour that a change of clocks repeats carries its offset.
- **Sizes** `{N}`: `N MB` when divisible by 1,000,000, `N KB` when by 1,000, otherwise `N bytes`, with `,` between thousands: one way to write each number.
- **Mailboxes** `{M}`: “<label>” when the label is not empty and no other mailbox of the account has it; otherwise the mailbox's address, unquoted. A mailbox the account no longer has: "a mailbox you disconnected". Labels are the person's own, and a vendor never sets one.

### Faithful, exactly

**The claim.** For a fixed language and `Context`, two different canonical formulas give two different sentences, as plain text and as parts. Where it may fail, and why that is safe: a mailbox renamed changes the sentence of a formula that did not change (the person renamed it, and reads the new name); two mailboxes that the account disconnected both read "a mailbox you disconnected" (each matches nothing, so the two formulas deliver the same mail). Nothing else.

**How it is held.** Every template's fixed words differ, and none holds `“`, `”`, a line break or the separator `, or `; every value is inside quotes and holds no quote, line break or control; domains, types, times, sizes and addresses shown unquoted are written in one way each from characters that cannot hold a separator. So the plain text can be read back: the tests carry a parser of sentences (`parseSentence`, test only) and assert that it gives back, for every generated canonical formula, that formula (Q21). A function with a left inverse is injective. The parser is in the tests, never in the service: nothing reads a sentence to decide anything.

**Values cannot add words.** A vendor controls the values and nothing else. A value is normalised before it is rendered: no bidirectional control (they are default-ignorable and removed), no line break (made a space), no quote of the sentence's own (refused), no control or format character (refused), no confusable mix of scripts (refused), at most 100 characters. In the plain text it is inside quotes; in the app it is a part of its own, shown in its own style (monospace for exact values, as the screens set them), isolated in `bdi` so right-to-left text cannot reorder the fixed words around it, and never as markup (web/AGENTS.md). A value that is itself English ("not sent from", "or with attachments") stays inside its quotes and its part.

**Model conditions say so.** A condition a model decides is marked in every form of the sentence, as ADR 0019 asks of the consent screen and the receipt.

### A second language

A language is a table: the templates above, the lead, the separators, the months, the number formats, the words for a disconnected mailbox and for UTC, keyed by its tag (`en` today). Values are never translated. A table is accepted only when the same tests pass with it: every condition and sign has a template, no fixed text holds a quote, a line break or its separator, and the parser reads every generated sentence back (Q21 to Q23 run per language). The language of a sentence is the account's (`language` in the account's record, docs/data.md); `Context.Lang` carries it, and an account in a language without a table reads English. Whether a second language comes with this step is the owner's ("Questions for the owner", 5).

## Evaluation over stored mail

### What each source costs

| Source | What it gives | How it is read | Cost |
|---|---|---|---|
| The item and its identifier | `received` (the identifier's time), `size`, `mailbox` | `ScanMessagesDown` in batches of 100, or `Item` | One item read; no unsealing |
| The search entry (`txt#`) | Senders, subject, the first 16 KiB of the text, whether it was cut | `ScanEntriesDown` in batches of 100, or `GetEntry` | One item of up to about 18 KiB, unsealed |
| The meta | Senders, recipients, subject, attachments, DKIM results, the list, the flags | The item's sealed meta | Unsealed from the item read |
| The text | Up to 256 KiB of text from the raw message | `ReadRaw` under the read budget, then `inbox.MessageText` | Up to 25 MiB read and parsed |

### What is indexed, and what is scanned

Message identifiers begin with the nanosecond they were received (ADR 0026), and both scans read a partition from the greatest key down. So `received` is indexed: when the canonical formula has a group of one `received before t` condition, the scan starts below the smallest identifier of `t` (its 16 hex digits of time, then zeros), whatever `after` says if that is lower; when it has a group of one `received since s`, the scan stops at the first identifier below `s`, and the page then has no `next`. Every other variable is scanned: each message considered is read. A sealed index of words or senders was rejected for the inbox's search (ADR 0026, "Searching") for what it shows whoever reads the table, and the same holds here.

### Which scan

`Eval` is three-valued and tells which source would settle an unknown value (`Result.Needs`), so a message's sources are read in order, each only if the value still depends on it:

- **Items first** when the formula has no `body` condition: the scan reads message items (which carry the meta); the entry is never read.
- **Entries first** when it has one: the scan reads search entries (which carry the senders, the subject and the start of the text), and a message's item is read only when the formula still depends on the meta or the size.
- Then, for a `body` condition that the first 16 KiB did not settle (not found, and the text was cut there), the raw message is read under the read budget and its text taken out, up to `inbox.MessageTextMax`: the text a connection receives.

A message whose value is true or false once a source is read is settled there. One whose value depends only on conditions a model decides is "needs a model" (`Result.Model` names them, and step 4 asks only those); one whose value depends on an exact condition that could not be read is "unknown". Gmail's forwarding confirmations are considered and never listed: they are Permaily's, and never leave.

### Budgets of a page

A page runs under the request's deadline (one minute from its start, ADR 0026) and holds one of the four search slots of the enclave, counted in the account's thirty search pages a minute, which the inbox's search and this share. It ends at the first of:

| Bound | Value |
|---|---|
| Matches | `limit`, 1 to 100 (50 by default) |
| Messages that need a model or are unknown | 100 (`inbox.MatchMaxUndecided`) |
| Stored items read: items and entries, scanned or read one by one, expired ones included | 500 (`inbox.MatchMaxRead`) |
| Raw messages read for their text | 10 (`inbox.MatchMaxTextReads`), each under the read budget of 128 MiB of mail open at once, waiting at most 10 seconds as every reader does (then 429 for the request) |
| Time spent considering messages | 20 seconds (`inbox.MatchSoftDeadline`), then the page answers what it has |

**Pages skip nothing.** A message is considered whole on a page, or not at all: when a bound is reached in the middle of a message (its raw text not affordable on this page, the 500th item read before its entry), the page ends before it, and `next` is the last message considered, so the next page starts with it. `next` is absent only when the scan says nothing older is left, or the `received since` bound is passed. A message that cannot be read at all (its meta or entry does not open, its raw message is past its bound) is considered and listed as unknown, so it does not stop every page. `searched_to` is when the last message considered was received. With every limit at once a page reads at most 500 items (about 9 MiB) and 10 raw messages under the shared read budget: the inbox's search page reads 2,000 entries, so a match page costs less in reads and more in parsing, and both are bounded per enclave by the same four slots.

### What delivery adds for it

Three things ADR 0026's delivery does not record today (an amendment of its format, written by pull request 3):

- `StoredMeta.List`, the identifier of the first `List-Id` header (lower case, without brackets), and `StoredMeta.Format`, `2` from this step on. A meta without `format` was written before, and every `list` condition on it is unknown: whether it had a `List-Id` is not stored. Nothing is migrated: such messages expire with their retention.
- `SearchEntry.Cut`, set when the text was longer than the 16 KiB kept. An entry without it was written before, and is taken as cut when its text is within 4 bytes of 16 KiB, which is where a cut text ends.

Mail is still never refused for its content, and the sizes ADR 0026 bounds do not change.

### Logs

A formula, a value, a sentence, an identifier of a formula and the number of matches are never logged. What is counted and logged is a kind: a page answered, refused for a reason of `contracts.FormulaReason`, or stopped by which bound. `TestMailEndToEnd`'s log check gains a formula with hostile values.

## Departures from the sample screens

The screens are the designer's view of what a person sees. Where this design departs, and why:

1. **Rule criteria are OR of AND**, with values OR'ed inside a condition; ADR 0019 said AND of OR. Kept as the screens have it: each criteria is one formula and a rule is any of them ("Rules: any of their criteria"). The editor needs no change.
2. **"Message contains"** looks at the subject and the text: it becomes `subject contains v or body contains v` (negated: `not subject contains v` and `not body contains v`), and the sentence says both.
3. **"Message mentions"** is `message mentions`, if the owner keeps it; otherwise `message about`. **"Message is about"** is `message about`.
4. **"Kind is password reset"** is marked in `valueStyle` as exact (no model), and nothing exact decides what kind a message is. It becomes `message about "password reset"`, with the editor's suggestions kept as suggested topics, and the screen must mark it as decided by a model.
5. **"Label is Family"** (a Gmail label) cannot be evaluated: forwarding carries no labels. It goes from the editor, and the sample rule "Private" keeps its senders only.
6. **"Sender is mapfre.es"** (an address or a domain) becomes `from_address is` when the value has an `@` and `from_domain is` otherwise; its sentence says "or a subdomain of it".
7. **Connections' queries in Gmail's syntax** (`in:inbox category:primary newer_than:1d`, `in:anywhere -in:spam -in:trash`, `from:(…) OR subject:(…)`): the first two are not expressible (no folders, categories or spam in a forwarded mailbox; "new mail only" is a term of the connection); the third is one group. The connection's "Readable" view shows the sentence; its "File" view shows the canonical JSON, or YAML written from it, not Gmail's syntax.
8. **"Fields it asks for"** (`needs_reply`, `amount`, `billing_address`, … in `web/src/data/queries.ts`) are values a model would extract from each message. No ADR decided them, and they are not conditions: they are outside this language. They stay sample until a decision of their own.
9. **The consent screen's "Asks for"** shows a name of a kind of mail ("Flight, hotel, rail and car bookings"). It shows the sentence instead (ADR 0019: the sentence is what the person consents to); a vendor's name for its connection may still head it.

## The contract and the seams

### Committed with this design

- `internal/contracts/query.go`: `Formula`, `Group`, `Condition`, `Variable` and `Operator` with their constants in canonical order, `FormulaVersion`, the limits, `ErrInvalidFormula`, `FormulaProblem` and `FormulaReason` with every reason, `FormulaCheck`, `Sentence`, `SentenceLine`, `SentencePart`, `MatchRequest`, `MatchPage`; `query_test.go` pins their JSON (`TestQueryShapes`).
- `web/src/lib/api.ts`: the same shapes (`QueryFormula`, `QueryCondition`, `QueryVariable`, `QueryOperator`, `FormulaProblem`, `FormulaCheck`, `QuerySentence`, `SentencePart`, `MatchPage`). The calls come with the routes (pull request 4).
- `internal/query/query.go`: the package's doc and the types its pull requests share: `Value` (`False`, `Unknown`, `True`), `Mode` (`ForQuery`, `ForRule`), `Canonical`, `Sources` (`FromItem`, `FromEntry`, `FromMeta`, `FromText`), `Message` and `Attachment` (what evaluation knows of a message), `Result` and `Place`, `Context` and `MailboxName`.
- `internal/inbox/match.go`: `Matcher` and its bounds (`MatchMaxRead`, `MatchMaxTextReads`, `MatchMaxUndecided`, `MatchSoftDeadline`).
- `.golangci.yml`: the depguard rule `query-is-pure`: `internal/query` imports the standard library, `internal/contracts`, itself, `golang.org/x/text/unicode/norm` and `golang.org/x/net/idna`, and nothing else. Both modules are direct dependencies already (`go.mod`), in the image through `go-message` and `mailhtml`; this adds no module.

As ADR 0026 did, the seams are types and interfaces, not functions with empty bodies: nothing implements `Matcher` yet, and nothing calls into `internal/query`.

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

// And is the conjunction of two canonical formulas, canonical: an agent's
// search AND its scope. It may be "never".
func And(a, b *Canonical) *Canonical

// Contains reports whether every message inner matches is one outer matches;
// false whenever it cannot prove it ("Containment").
func Contains(outer, inner *Canonical) bool

// Eval is a canonical formula's value on what is known of a message, and
// what would change it.
func Eval(f *Canonical, m *Message) Result

// Resolve says whether a value delivers (ForQuery) or applies the rule
// (ForRule).
func Resolve(v Value, mode Mode) bool
```

By pull request 2:

```go
// Render writes the sentence of a canonical formula, in c.Lang (English when
// it has no table), naming mailboxes from c.Mailboxes.
func Render(f *Canonical, c Context) contracts.Sentence
```

In `internal/inbox`, by pull request 3: `Matcher` implemented by the store's reader (`match.go`), and `inbox.QueryMessage(item, entry, meta, text)` filling `query.Message` from what was read. In `internal/httpapi`, by pull request 4: the routes below, through `MailboxesService.Open` and the reader, as the inbox's search goes.

### The routes (the web app's API, pull request 4)

| Request | Needs | Answers | Errors besides ADR 0026's common ones |
|---|---|---|---|
| `POST /api/v1/queries/check` `{formula}` | a session; an account open, closing or not agreed | 200 `FormulaCheck`, its sentence in the account's language, its mailboxes named | 400 `invalid_formula` with `details`, `invalid_body` |
| `POST /api/v1/mailboxes/{id}/matches` `MatchRequest` | a session; the standing of `GET .../messages?q=` (ADR 0026's access table: refused with `terms_needed` to an account that has not agreed) | 200 `MatchPage` | 400 `invalid_formula`, `invalid_limit`, `invalid_cursor`, `invalid_body`; 409 `mailbox_unreadable`; 429 `too_many_requests` |

They are the web app's: the check is what the rule editor (step 7) and the consent screen (step 5) show sentences from, and the matches what their previews count. Vendors reach the same functions through the API and MCP of step 5, behind their tokens. No screen is built in this step. Both routes are under `/api/` on the app's host, behind the cross-site rule and the canonical path (ADR 0026, "The API"), and are POSTs because a formula does not belong in an address bar or a log of one.

### Who calls what, later

| Step | Uses |
|---|---|
| 4, the model | `Result.Model` to ask only what a message still needs; the bands of "What a formula means"; the formula's identifier and the normalised topic to key kept decisions |
| 5, connections | `Parse` on a vendor's request, `Render` on the consent screen, `Contains` for asking for more, `And` for an agent's search, `Matcher` for the preview's count, `Resolve(ForQuery)` |
| 7, rules | `Canonicalise` of the formula the model writes from the person's words, `Render` to confirm it, `Resolve(ForRule)` |
| 8, receipts | The canonical identifier and the sentence of what was asked |

## Tests

Numbered so a review can check each. "Generated" means by `querytest` (pull request 1, test only): random canonical and non-canonical formulas over a small universe of values chosen to collide (two domains and a subdomain of one, three addresses, words that are prefixes and parts of each other, four times, three sizes, two mailboxes, two topics), and random messages over the same universe with every source read or not, `Partial`, `Unparsed`, cut texts, unnormalisable addresses, and model answers in every band.

**Parsing** (pull request 1)

- **Q1.** A table of valid formulas, each with the canonical JSON it gives (golden).
- **Q2.** A table with one formula per refusal reason, and the `at` of each; the error's message is fixed and holds no value.
- **Q3.** Strictness: a member twice (`{"v":1,"v":2,…}`), an unknown member, `null` anywhere, a number for `value`, a group inside a group, a byte order mark, invalid UTF-8, trailing data, 64 KiB and one byte more; none panics.
- **Q4.** `FuzzParse` (in `make fuzz`): never panics; every accepted input's canonical JSON parses to itself, byte for byte; canonical JSON is deterministic.
- **Q5.** Every limit at its value and one past it.

**Normalisation** (pull request 1)

- **Q6.** Addresses and domains: case, a Unicode domain and its ASCII form giving one value, a trailing dot, a quoted local part, a `+tag` kept, an invalid label refused, a mixed-script label refused, the message's side unnormalisable giving unknown, not false.
- **Q7.** Text: NFKC (`ﬁ`, full-width letters), the folds of `ß`, `ẞ`, `Σ`, `ς`, the Kelvin sign and `İ`, zero-width and bidirectional characters removed, white space made one, `“` refused, controls refused, `invоice` with a Cyrillic `о` refused; the normalisation is a fixed point on every code point, and `FuzzNormalise` holds it on any string.
- **Q8.** Words: `invoice` in "invoices", "invoice,", "INVOICE-22" and "the invoice"; `car` in "scar" and "careful"; `.pdf` in "bill.pdf"; a phrase across a line break; `发票` inside a Chinese sentence without spaces; Thai; a match at the start and at the end of a field; a value ending in a mark.
- **Q9.** Times with offsets, a fraction, a leap second, a year before 2000 and after 2100; sizes with a sign, a leading zero, 0 and 32 MiB plus one.

**Simplification** (pull request 1)

- **Q10.** A table with one case per rewrite and per relation of the implication table, before and after.
- **Q11.** Idempotent: the canonical form of a generated formula's canonical form is itself, byte for byte.
- **Q12.** It keeps the meaning: for generated formulas and messages, `querytest`'s brute-force evaluator on the formula as sent and `Eval` on its canonical form give the same three-valued value, and so the same `Resolve` in both modes.
- **Q13.** One form for every order: a generated formula with its conditions and groups shuffled, repeated and its values respelled (case, Unicode and ASCII domains, white space) gives the same bytes and identifier.
- **Q14.** Bounded: the worst formula at the limits (64 conditions, every pair related) is canonicalised in fewer rewrites than its size and in under 10 ms; `And` of two such formulas likewise.
- **Q15.** Three values, not two: `subject contains "x" or not subject contains "x"` is kept; `message about "x" and not message about "x"` is kept, not "never"; `mailbox is m or not mailbox is m` goes; `received since s and received before t` with `t` ≤ `s` is "never" and is refused by `Parse` as `never_matches`.

**Containment** (pull request 1)

- **Q16.** A table of yes and no per kind of variable: addresses in domains, subdomains, words in phrases, types in `T/*`, lists, ranges, mailboxes, identical topics, different topics (no), `about` and `mentions` (no), several groups of B needed (no), every message and never.
- **Q17.** Sound: for generated A and B, built so that "yes" is common (B made by strengthening A: a condition replaced by one that implies it, a group added, a condition taken out of a group), whenever `Contains(A, B)`, on every generated message, `Eval(B) ≤ Eval(A)` in three values.
- **Q18.** Sound by brute force: over a universe small enough to list every message (two domains, three addresses, two words, two times, attachments or not), for every pair of formulas of up to two groups of two conditions, `Contains` yes implies containment over every message and every assignment of unknown; and for formulas of single conditions, the answer is exactly the brute force's (complete there).
- **Q19.** Bounded: two formulas at their limits are compared in at most 4,096 tests of conditions and under a millisecond.

**The sentence** (pull request 2)

- **Q20.** Golden sentences in English for a table covering every template and sign, one group and several, every message, a rule of several formulas, a domain with a Unicode form, times at midnight, with minutes and with seconds, each kind of size, mailboxes named by label and by address and disconnected; reviewed with the designer.
- **Q21.** Injective: `parseSentence(Render(f).Text)` is `f` for every generated canonical formula, and the parts read back give `f` too; for every language with a table.
- **Q22.** No injection: values that are every template's fixed words, `, or `, `.`, `- `, right-to-left text, the longest value, and values whose normalisation removed bidirectional controls and line breaks: each stays one value part, inside its quotes in plain text, and the parser still reads the formula back.
- **Q23.** The tables: every variable, operator and sign has a template in every language; no fixed text holds `“`, `”`, a line break or its separator; no template's fixed text is another's.
- **Q24.** Mailbox names: two mailboxes with one label are named by their addresses, an empty label likewise, a renamed one by its new name, and no two mailboxes of an account read alike.
- **Q25.** Model conditions are marked in plain text and in parts, and only they are.

**Evaluation** (pull requests 1 and 3)

- **Q26.** `Eval` per variable, with a table of messages for true, false and each reason for unknown (a source not read, `Partial`, `Unparsed`, a meta of format 1 for `list`, an unnormalisable address, a cut text not read), and `Result.Needs` naming the source that would settle it.
- **Q27.** Staged equals whole: for generated messages written with `inboxtest.Seed` (which writes the new fields), what `Match` lists as a match, needing a model, unknown or nothing equals `Eval` on everything read at once.
- **Q28.** Pages skip nothing: generated mailboxes of up to 2,000 messages, pages of random `limit`, bounds set low (`MatchMaxRead` 7, two raw reads) so pages end inside batches and before a message's raw text: the union of the pages' matches is exactly the brute force's, with no message twice, and `next` absent only at the end.
- **Q29.** The `received` bounds are used: a formula with `received since` and `before` reads no item outside them (the store's reads counted).
- **Q30.** Budgets: the read budget taken before each raw read and given back, 429 when it is not admitted within 10 seconds, the soft deadline ending a page with `next`, the search slot and the thirty pages a minute shared with the inbox's search, the request's deadline.
- **Q31.** A Gmail forwarding confirmation is never listed, whatever the formula.
- **Q32.** Delivery: a message with `List-Id` stores its identifier and format 2; a text longer than 16 KiB sets `Cut`; a meta without format reads `list` as unknown; an entry without `Cut` whose text is 16 KiB long reads as cut.
- **Q33.** The bands: `Resolve` of a model's probability at 0.19, 0.20, 0.21, 0.29, 0.30, 0.69, 0.70, 0.79, 0.80 in both modes, for a condition and its negation, and no answer.

**Routes and the contract** (pull request 4)

- **Q34.** `queries/check`: the shapes, every reason with its `details`, a fixed message, no value echoed; a mailbox of another account is `unknown_mailbox`; the sentence in English for an account of another language (until a table exists).
- **Q35.** `matches`: ADR 0026's order of failures (401, 404 for another account's mailbox before anything is read, the standing, `mailbox_unreadable`, the input, limits), JSON with `nosniff`, and the reader sees no call for another account's mailbox.
- **Q36.** `TestQueryShapes` (committed now), the web app's types type-checked against a fixture of each answer, and `TestMailEndToEnd` extended: a mailbox, mail with a list, an attachment and a long text, a formula through `matches`, and the log holding no value, sentence or identifier.

## Implementation pull requests

Five. This one first, alone. Then 1, 2 and 3 are written at the same time against the committed types; 1 merges first; 2 and 3 merge after it in either order; 4 last. A release takes all of them: step 3 is done when it is deployed and the staging check of pull request 4 has passed. Every file has one owner; another pull request edits it only where its "May edit outside" names it.

| Pull request | Owns | May edit outside, and only that | Must not touch | Depends on |
|---|---|---|---|---|
| 0. Design and seams (this one) | This ADR; `internal/contracts/query.go` and its test; `internal/query/query.go`; `internal/inbox/match.go`; the `query-is-pure` rule; the TS types in `web/src/lib/api.ts`; docs/plan.md, docs/decisions/index.md, internal/AGENTS.md's row | | Everything else | Nothing |
| 1. The language | `internal/query`: `parse.go`, `normalise.go`, `canon.go`, `implies.go`, `contain.go`, `eval.go`, their tests and fuzz targets; `internal/query/querytest` (generators and the brute force, test only) | `.golangci.yml`'s `no-fakes-in-production` (one line for `querytest`); the `fuzz` target of the makefile (its fuzz targets); `scripts/coverage-thresholds.txt` (a line for `internal/query`, 95 or more) | `internal/inbox`, `internal/httpapi`, the web app | 0 |
| 2. The sentence | `internal/query/sentence.go`, `sentence_en.go`, their tests and the test-only `parseSentence` | | What 1 owns, except calling it | 0; 1 to merge |
| 3. Stored mail | `internal/inbox/match.go` (beyond the interface), `queryview.go` (`QueryMessage`); in `parse.go`, `deliver.go` and `box.go` the three fields of "What delivery adds", and only those; `inboxtest` writing them; docs/data.md's rows | | `read.go`, `search.go` beyond sharing the scan's helpers by moving them unchanged; `internal/query` | 0; 1 to merge |
| 4. Routes | `internal/httpapi/queries.go` (new: the check route), the `matches` route in `mailboxes.go` and its registration, the error writer's `details`; in `web/src/lib/api.ts` the two calls and `invalid_formula` in `ApiErrorCode`; `cmd/permaily-enclave` mounting; `TestMailEndToEnd`; docs/web.md (the API), docs/threat-model.md (what a formula may hold and what is logged), docs/acceptance.md (the staging check), docs/testing.md | `ApiErrorCode` in `api.ts` | `internal/query`, `internal/inbox` | 1, 2, 3 |

**Definition of done of each.** Its tests in "Tests" pass (1: Q1 to Q19, Q26 and Q33 in `query`; 2: Q20 to Q25; 3: Q26 to Q32 in `inbox`; 4: Q34 to Q36); `make check` passes, coverage thresholds included; `make fuzz` runs its fuzz targets; internal/AGENTS.md and the docs it owns say what it built; an adversarial review of its code, every finding a test named for it. For 4, also: a staging image where a mailbox receives mail (with a `List-Id`, an attachment and a text past 16 KiB), and `queries/check` and `matches` answered through the app with a session, recorded in docs/acceptance.md.

**What truly runs in parallel.** 1, 2 and 3 need only the committed types: 2 renders a `Canonical` it can build in its tests by hand until 1's generators merge, then switches to them; 3 builds `Matcher` and fills `query.Message` against `Eval`'s contract with a stand-in in its tests, and merges after 1 to run Q27 against the real `Eval`. 4 waits for the three.

**Why the language goes in one pull request and the sentence in another.** Parsing, simplification, containment and evaluation share the implication table and its proofs, and one reviewer must hold all of them; the sentence is a separate question (is every template right, is it injective, can a value inject) with its own reviewer, the designer included.

## Options considered

- **A text syntax as the wire form** (Gmail's): rejected above ("No text syntax in the first version").
- **Arbitrary nesting** (any tree of AND, OR and NOT): ADR 0019 fixed AND of OR; a tree has no canonical form short of normal forms that grow exponentially, and its sentence needs brackets.
- **Two values, with unknown read as false**: a protecting rule would let a message go when its data could not be read, against ADR 0019. Three values, resolved by mode.
- **Substring matching** for `contains`: "car" would match "scar", and a vendor's query would take more than its sentence says to a person who reads words.
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

## Questions for the owner

Only the product choices; everything else above is decided. Each with the recommendation the design assumes, and what the other answer changes.

1. **Which variables are in the first version?** Recommended: all fifteen of the table. Trade-off: `list` works only for mail received from this release on (older mail reads it as unknown, so a query asking for newsletters misses older ones and a rule holding them back holds older mail too); `mailbox` lets a vendor target one mailbox, which the consent screen must then name; dropping either removes its rows and templates and nothing else.
2. **Is `mentions` a second operator a model decides?** ADR 0019 named `about` the only one; the rule editor uses "mentions". Recommended: yes, on `message` only. Trade-off: one more kind of statement for the model and the spike of step 4 to measure; without it, the editor's "mentions" becomes `about`, which holds back less ("a message about a delivery that mentions my address" is not about my address).
3. **Does `contains` match whole words?** Recommended: yes, whole words and phrases, case and width ignored. Trade-off: "invoice" does not find "invoices", so vendors list both and a rule written from plain words gets both from the model that writes it; the other way, "car" finds "scar" and "careful", and the sentence would have to say "contains the letters".
4. **Do vendors get a text syntax?** Recommended: no, JSON only in the first version, and the connection's "File" view shows the canonical JSON (or YAML of it) instead of the sample's Gmail syntax. Trade-off: a person or a developer cannot type a query by hand in one line; agents and apps lose nothing, since they build JSON.
5. **Which languages does the sentence have?** Recommended: English only in this step, with Spanish as the next table when the app itself has Spanish. Trade-off: a person whose account says Spanish reads the sentence they consent to in English until then; adding Spanish now costs its table, its golden sentences and a native reader's review, and nothing in the design.
6. **Does a rule match when any of its criteria does?** Recommended: yes, as the editor shows, which amends ADR 0019's "a rule is a formula" to "one or more formulas". Trade-off: the other answer forces one formula per rule, and the editor's several criteria would become several rules.

## What it rests on

- ADR 0019 (the formula, the sentence, simplification, containment, rules, when in doubt), 0020 (the thresholds, exact conditions first, kept decisions, topics shown as they are), 0026 (the stored format, the scans, the budgets, the access table, the API's rules), and the screens in `web/src/data/sample.ts`, `web/src/data/queries.ts`, `web/src/pages/connections/`, `web/src/pages/rules/` (read on 2026-11-14).
- The repository on 2026-11-14: `golang.org/x/text` v0.43.0 and `golang.org/x/net` v0.62.0 are direct dependencies (`go.mod`); depguard's `domain-is-pure` allows only the standard library, `contracts` and `domain` (`.golangci.yml`); message identifiers are the receipt's nanosecond in 16 hex digits and 16 random ones (`inbox.NewMessageID`), and the claim's `received_at` is the same instant (`inbox/deliver.go`); the search entry keeps the first 16 KiB of text and discards whether it was cut (`inbox/parse.go`); `StoredMeta` has no `List-Id`.
- Kleene's strong logic of three values, and that its AND and OR are the least and the greatest in false < unknown < true; that a map of that order onto false < true that keeps the order keeps both (so resolving unknown at the end equals resolving each condition).
- Unicode: NFKC (UAX #15), the derivation of `Default_Ignorable_Code_Point` (UAX #44, `DerivedCoreProperties.txt`), simple case folding (`CaseFolding.txt`, status C and S, as Go's `unicode.SimpleFold` follows it), the scripts that do not separate words with spaces (UAX #29 leaves them to dictionaries; this design needs no boundary there instead), UTS 46's lookup processing, and UTS 39's mixed-script concern, of which only the Latin, Greek and Cyrillic case is taken.
- RFC 5322 (address syntax and quoted local parts), RFC 2919 (`List-Id`), RFC 3339 (times), RFC 6376 (DKIM's `d=`), RFC 6838 (media types' top-level names).
- That containment of formulas of this shape is decidable but co-NP-hard in general (it is the validity of an implication between CNF formulas), which is why the test is sound and not complete.
