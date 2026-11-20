# 0040 Rules in plain words, holding back and removing details: how

Status: Proposed. Designed on 2026-11-20 for step 7 of F2 (docs/plan.md), while the owner was away and asked for steps 6 to 9 to go on without them; not built, and nothing in it is in the image yet. Builds on ADR 0019 ("Rules"), 0020 (OpenAI for meaning), 0027 (the query language; "Rules: any of their criteria"), 0028 (the model, its spend guard and canary; "Who calls what, later": step 7 uses the same `Asker` and the Responses API for words), 0030 (connections, the consent screen) and 0031 (the default rule, `inbox.Discloser`, `inbox.Decisions` and `inbox.BlockDecisions`, `SettleAfterAsk`, receipts), whose "How steps 7 and 8 extend them" it follows for step 7, and 0016 (`store.Anchored`). Coordinated by name with ADR 0039 (delivery on new mail and on a schedule) and ADR 0041 (the activity screen, the CSV export, notices about connections), designed at the same time: this record states the seams they call, not their designs. Its pull requests are in "Implementation pull requests"; its tests are U1 to U36 (R is taken by the review findings of ADR 0031 and 0037). Every product choice the owner had not made is listed in "Choices made for the owner (to confirm)", with its alternative, so the owner can reverse it at the design review.
Date: 2026-11-20

## Context

ADR 0019 promised that a person's own rules hold a message back, or remove details from it, before any query, for every connection, every one but some, or only those picked; that the person writes a rule in plain words, a model turns it into a formula, and the person confirms it by the sentence the system writes; that removing details covers whole parts, data with a fixed shape that needs no model (addresses, phone numbers, account and card numbers, numeric codes, links), and meaning, sentence by sentence; and that when in doubt nothing leaves. ADR 0020 chose OpenAI's Responses API, with the answer bound to a schema and `store: false`, for words, and the Decisions API, asked sentence by sentence, for removing by meaning, so that what leaves is always the original text minus pieces.

Step 5 built the default rule and met step 7's prerequisite from ADR 0028's design review for every rule that protects: a rule's value that rests on the model is a decision begun in a strict anchored record of the mailbox's day before the model is asked, settled after, and a decision whose settlement was lost, or a record that cannot be read, holds the message (ADR 0031, "Holds that cannot be undone"). ADR 0031 left step 7 the person's rules as a strict anchored record beside the compiled default, which also says whether the default is off; rules for some connections as a field of each rule; removing details as a new effect and a new view of the text (`inbox.TextView`); and `Decisions` serving every rule that protects.

What this record must decide, each with the alternatives weighed: the rules' record and how a change applies to mail already decided and to answers in flight; scope, and connections made later; words to a formula through the model, what the person confirms, and the model unavailable; holding back against removing, and their order with the default rule and the queries; fixed shapes exactly; meaning sentence by sentence, with its cost bounds; rollback resistance for every model-dependent decision of a rule; and how rules show in previews, the consent screen, receipts, `/status`, `permaily-verify` and the web app, with the threat model, the privacy page and the costs.

## Decision in short

- **One record of rules per account**: `rules-<account>-<epoch>`, a strict anchored record (ADR 0016, 0026), sealed under a key from the root secret, made with `CreateNew` at the account's first change and written at the version read; at most 16 rules of at most 16 KiB each, 300 KiB of JSON, rolling to its next epoch at 9,000 versions as the account's list of connections does (ADR 0030, C2). It says whether the default rule is off and which rules' notice the account agreed to. An account without one has the default on and nothing else: every account today, so nothing migrates. ("The rules' record")
- **Every change needs a fresh proof** (ADR 0022), names the record's version the screen read, and is recorded in a receipt of a new kind, `rules`, before it is answered. A rule's version changes with its criteria, effect or removal, whose decisions are then made afresh; its name, scope and switch change no version. A change applies to every request that begins after it: at once through the enclave that saved it, within five seconds through the other, never undone by an earlier copy; an answer already being prepared finishes under the rules it began with, which its receipt names. ("Changes, and what they apply to")
- **Scope by client**: all connections, all but those of the clients listed, or only theirs; a client is the app's `client_id`, so a replacement, a device added or a later connection of the same app keeps the rule's scope; a connection of a new app is covered by "all" and "all but", never by "only". ("Scope")
- **Words to a formula**: the person's words (at most 1,000 characters) and nothing of their mail or account go to OpenAI's Responses API with a prompt pinned in the image (`internal/rulewords`, its version in `/status`), `store: false`, the answer bound to a strict schema of criteria, effect and removal; the enclave checks it as it checks a vendor's formula, the system writes the sentence, and nothing applies until the person saves it, confirming that sentence with a fresh proof. With the model unavailable, words are refused (`model_unavailable`) and the editor builds the rule from conditions with no model. The words are kept nowhere. ("Plain words")
- **A rules' notice of its own**, per account, agreed before words are sent and before a rule a model decides any of is saved; a rule saved under another notice asks nothing and applies wherever it would need the model, until the person agrees and saves it again. The model's notice of connections is unchanged, so no connection stops. ("The rules' notice")
- **The order**: on the message as stored, every rule that holds (the default first), read for a rule; then every rule that removes and applies, its removals in a fixed order: whole parts, then fixed shapes in the subject and the text, then meaning sentence by sentence on what the shapes left; then the connection's formulas on what is left, never on the original; then the parts the connection reads. A message held is never also partly given. ("The order")
- **Fixed shapes**, no model: six kinds, exactly specified over Unicode text (any script's digits, invisible characters transparent, NFKC for the at sign), over-removing on purpose (dates, years and amounts are numbers; a domain under an ICANN suffix is a link), on the subject and the text only (the HTML never leaves); a rule that removes shapes or topics removes the attachments too, whose content it cannot read. Each piece becomes `[removed]`. ("Fixed shapes")
- **Meaning, sentence by sentence**: the text left by the shapes, split by a fixed rule into at most 128 sentences (the subject first when given) within 32 KiB, is sent once with numbered sentences and one statement per sentence and topic to the Decisions API (a new prompt version); a sentence is kept only when every topic answered below 0.30; what is past the bounds, refused, in doubt or unanswered is removed. The outcome is a decision of `inbox.Decisions`, begun before anything is asked and settled with the sentences kept, so it can never be undone; a decision that cannot be made or used removes the whole text. ("Removing by meaning")
- **Rollback resistance for every rule**: the default rule's mechanism, unchanged, for every rule's model-dependent applicability, and the same records, with one new field (the sentences kept), for meaning. ("Decisions that cannot be undone")
- **Seen everywhere it acts**: the consent screen lists the rules that will apply to that app and its preview marks what they hold and remove; receipts record each rule's removal (never the content) and every change of rules; `/status` publishes the rules' design constants and prompt versions and `permaily-verify` checks them against the source; delivery (ADR 0039) learns rules only through `accountrules.Source` and decides whether to notify only through the discloser. ("Previews and consent", "Receipts", "/status and permaily-verify", "The seam with delivery")

## The rules' record

**Where.** One strict anchored record per account and epoch, `rules.RecordName(account, epoch)` (`rules-<account>-<epoch>`), in the composition's strict `store.Anchored` (as the connections' records are), sealed with `envelope.SealText` under a key derived from the root secret (`permaily rules v1`) and bound to its name, so whoever writes the table can neither read nor change it. Its document is `rules.Record` (committed with this design): the format, the account, the epoch, `next` once the next epoch holds the rules, whether the default is off and when it last changed, the rules' notice agreed (`version`, `at`), and the rules (`rules.Stored`: an identifier `rul_` and 26 base32 characters, random; the rule's version; the draft as saved, canonical; the draft's identifier; the notice it was saved under when it asks a model; when made and last changed).

**Made and written.** The first change of an account's rules makes epoch 0 with `CreateNew`; every later change reads the newest epoch strictly and now (`GetRecordAt`), applies the change, and writes it at the version read (`PutRecordAt`); `store.ErrChanged` is answered `rules_changed`, never retried with the person's change on another state, since the screen named the version it read (`RuleSave.Version`). An account without a record has the default on and no rule: every account of step 5, so nothing migrates.

**Bounds.** At most `contracts.MaxRules` (16) rules, each at most `MaxRuleBytes` (16 KiB of canonical JSON), at most `MaxRuleCriteria` (8) criteria, each a formula within the query language's limits, at most `MaxRuleModelConditions` (8) conditions a model decides in a rule and `MaxAccountModelConditions` (32) over the account's rules that are on (so every rule's statements about one message fit one call with the default's and a query's, `decide.MaxStatementsPerCall` being 64), at most `MaxRuleTopics` (3) topics in a removal and `MaxMeaningRules` (4) rules that remove by meaning. The record is at most `rules.RecordBytesMax` (300 KiB of JSON before sealing, within the 400 KiB item: 16 rules at their largest are 256 KiB). It rolls over at `rules.RecordVersionsMax` (9,000 versions, below the strict read's 10,000): the epoch is written `next`, then the next is made with `CreateNew` holding everything; a crash between the two leaves a `next` whose epoch a strict read finds never written, which the next writer makes; the newest epoch is found from the last this enclave saw, following `next`, as `connections.readList` does.

**Read.** `accountrules.Source.Applying(ctx, account, client)` answers the rules a connection of `client` applies, in order (`rules.Record.For`, committed): the default first unless off, then the person's rules that are on and whose scope covers the client, those that hold before those that remove, each in the order made. A cache per account in each enclave, at most `accountrules.Fresh` (five seconds) old, read again strictly once older and at once after a change made through this enclave. A record lost (`store.ErrLost`), forked (`store.ErrForked`), that does not open, or a rule in it that does not compile, is `accountrules.ErrUnavailable`: the account's connections are answered 503 with `Retry-After` and give nothing, the default alone included, counted on `/status` (`rules_unreadable`) and logged by kind. Nothing is ever answered from a record that could not be read.

**Removed with the account.** Removing an account removes every epoch (`RemoveRecord`, walking from 0 by `next`) with its other records, in the removal's sweep (ADR 0030's `EndAll`); a later account starts with none (U30).

## Scope

A rule is for every connection (`all`), every one but those of the clients listed (`all_except`), or only theirs (`only`), at most 16 clients (`contracts.RuleScope`). **A client is the app's `client_id`** (its document's URL, ADR 0030), shown by its domain, not a connection's identifier: a client has at most one active connection, which a new request replaces on fresh consent, and devices are added to it; naming the client keeps the rule's scope across a replacement and a device without rewriting the rules' record in the same commit as the connection's, which the store cannot do. **A connection made later** is covered by `all` and `all_except` (unless its client is listed) and never by `only`; its consent screen shows which rules will apply to it, by their sentences, before Allow. A scope this release cannot read covers every client (`rules.Scope.Covers`): a rule protects, so it errs toward applying.

The default rule is on or off for every connection; it has no scope and its criteria cannot be edited (`default_not_editable`).

## Plain words

**What is sent.** `POST /api/v1/rules/words` `{words, notice}`: 1 to 1,000 characters of the person's own words, and the rules' notice the screen showed (`rules_notice_needed` when the account has not agreed to the one in force). The enclave sends OpenAI's Responses API the pinned developer message, the JSON schema of the answer, and the words as the user's message: nothing of the person's mail, mailboxes, connections, account or address (a rule that names "my home address" is turned into a topic, "a home address", as the default rule's topics are asked with nothing of the account, ADR 0028's owner's decision 2). `store: false`, no tools, no previous response, `max_output_tokens` 2,000, structured output in strict mode. The words are kept nowhere: not in the record, a receipt, a log line or a cache.

**The prompt, pinned.** `internal/rulewords` (pure, depguard's `rulewords-is-pure`, as `internal/decide` is): the developer message, the answer's JSON schema generated from the contract's types (criteria as formulas of the query language with its variables and operators as enums, the effect, the removal's parts, the six shape kinds as an enum, topics as strings of at most 100 characters), the model's name, and `rulewords.PromptVersion` (`w1-<date>`), which names every byte of them: a change of a word is a new version, never an edit. `/status` shows the version and `permaily-verify` compares it with the source. The adapter (`internal/rulewords/openai`) makes one attempt a call through the model's egress, as `internal/decide/openai` does.

**What the person confirms.** The answer is checked as a vendor's formula is (`query.Parse` with the account's mailboxes, so a mailbox the model invents is refused), with the rule's bounds; a criteria that never matches, an unknown variable or operator, a shape kind that is not one of the six, a topic the language refuses, an effect with the wrong removal, or anything outside the schema, is `rule_words_unusable`, and nothing is proposed. A usable answer is answered as a `RuleProposal`: the draft made canonical, with `scope: all` and `on: true` (the model knows nothing of the account's connections), and the system's own sentence of its criteria (`query.RenderRule`), its effect and its scope in fixed words (`RuleCheck.Does`, `For`), with the parts a model decides marked. The model's own text is never shown. The person reads that, may change any of it in the editor, and saves: `POST /api/v1/rules` with the draft and its `draft_id`, which the server works out again and refuses (`rule_changed`) when it differs, so what is saved is exactly what the screen showed. Nothing applies before the save.

**The model unavailable.** No key, the provider refusing it, the month's or the provider's limit, the account's day share, a failure or a timeout: `model_unavailable` for words, nothing proposed, nothing saved. The editor then builds the rule from fields, operators and values with no model, and saving needs no model either: a rule with `about` or `mentions` saved while the model is off is held wherever it needs the model until the model answers (it protects). Each words call is reserved through the same guard before it is sent (`decisions.Service.Write`, at its upper bound: the developer message, the schema and the words at the provider's input price, plus 2,000 output tokens at its output price, compiled with the prompt), in the account's day share, and settled at what the answer reports; at most 30 words calls an account a day in each enclave besides the share (the rate a person writes; in memory, like ADR 0017's limits).

## The rules' notice

ADR 0028 requires each disclosure of mail to OpenAI to run on a consent the person gave, recorded with what was shown, its version and its time, and docs/legal.md's engineering item 2 says a rule of the person's records its own in step 7. The model's notice of connections (version 2026-11-16) says what is sent to decide a condition and that the Sign-in and security rule's decisions are kept; it does not say that a person's words are sent, nor that their own rules keep decisions, nor that sentences are sent for removing by meaning. Changing it would stop every connection until renewed (ADR 0030, finding 18). So the rules have **their own notice**, `domain.RulesNoticeVersion` and `web/src/lib/rules-notice.ts` (a test compares them, as the model's notice's does), whose words are in docs/web.md: what is sent for words, for a rule a model decides and for removing by meaning; that the words are not kept; what OpenAI keeps while zero data retention is not approved; how long a rule's decisions are kept. The account agrees once (`POST /api/v1/account/rules-notice {version}`), recorded in the rules' record with its time; each rule a model decides any of records the notice it was saved under. While the account has not agreed to the notice in force, or a rule was saved under another, that rule asks nothing (`rules.Rule.ModelOff`): its model atoms stay Unknown, so a rule that holds holds wherever its value needs the model, and a removal by meaning removes the whole text; the rules page says so and asks the person to agree and save it again. Nothing is ever sent for a rule without the consent in force.

## The order

For each message a connection's request considers, `inbox.Discloser` evaluates, in this order:

1. **Every rule that holds**, on the message as stored: the default first (unless off), then the person's, each read for a rule (True or Unknown applies), a model-dependent value decided through `Decisions` (ADR 0031). A message one of them applies to is held: not given, and the vendor told nothing (`not_found`), as today.
2. **Every rule that removes and applies**, each read for a rule on the message as stored, a model-dependent value decided through `Decisions` as a hold is: a rule whose applicability is Unknown applies.
3. **The removals**, the union of every applying rule's, in a fixed order whatever order the rules were made in: whole parts (sender, subject, text, attachments); then the fixed shapes on the subject and the text; then meaning, rule by rule, on the text the shapes left, so the model never sees a piece the shapes removed. A rule that removes shapes or topics removes the attachments too.
4. **The connection's formulas** (consented scope, mailboxes, mail since, device, the vendor's search) on the message as it would leave: a `subject` or `body` condition reads the reduced subject and text, a condition on a part removed whole is Unknown (so a query does not match it), and a model's atom of a query is asked about the reduced text, under a view of its own (`inbox.TextView` named `r-` and the first 16 hexadecimal digits of the SHA-256 of what was removed: the rules' references, `redact.Version` and each meaning decision's sentences kept), so an answer about the original is never used for the reduced text and the reverse (ADR 0028's views). Evaluating a vendor's search on the original would let it probe what a rule removed ("does the text contain 555-1234?").
5. **The parts** the connection reads, as today.

Holding wins: a held message is never partly given. Rules apply to what leaves through connections and to the consent screen's preview; not to the person's own inbox and previews of a formula, as the default rule does not (ADR 0031).

## Fixed shapes

`internal/redact` (pure, `redact-is-pure`), at `redact.Version` 1, on the subject and the text a connection receives (`inbox.MessageText`, at most 256 KiB). The HTML part never leaves a connection, so it is never redacted; should an HTML view ever leave, it must be taken from the redacted text, not redacted on its own. Attachments are not read: a rule that removes shapes or topics removes them whole, their names with them (choice 6). The `From` field is not scanned: a rule removes it whole (`sender`) or not at all (choice 9).

**Reading the text.** Rune by rune, over valid UTF-8 (an invalid byte is a separator of its own, never part of a piece). A rune is a **digit** when Unicode says it is a decimal digit (`unicode.IsDigit`, any script: ASCII, fullwidth, Arabic-Indic, Devanagari…). Default-ignorable and format runes (`Cf`, U+200B to U+200D, U+2060, U+FEFF, U+00AD, variation selectors) are **transparent**: inside a piece they belong to it, and they never end one. The at sign is any rune whose NFKC form is `@` (`@`, U+FF20, U+FE6B). Removal works on the original text's spans, never on a normalised copy.

| Kind | What is a piece |
|---|---|
| `email_address` | A run of runes that are not white space, controls or ``<>()[]"',;:`` that holds an at sign with at least one rune before it and, after it, at least two labels joined by `.`; the run's trailing `.`, `,`, `!`, `?`, `)` and `:` are not part of it |
| `phone_number` | A **digit group** of 7 to 15 digits |
| `card_number` | A digit group of 12 to 19 digits (no Luhn check: a number that fails it is still removed) |
| `account_number` | A digit group of 8 to 34 digits; or an IBAN: two uppercase letters, two digits, then 11 to 30 uppercase letters and digits, in groups of up to four joined by single spaces |
| `numeric_code` | A digit group of 4 to 10 digits |
| `link` | A run of non-white-space runes that starts with a scheme (a letter, then letters, digits, `+`, `.`, `-`) and `://`, or with `mailto:` or `tel:`, or with `www.`; or two or more labels joined by `.` whose last is under an ICANN suffix of the public suffix list (`golang.org/x/net/publicsuffix`, compiled into the image), with or without a path after a `/`; the same trailing punctuation left out |

A **digit group** is a maximal sequence of digits in which any two consecutive digits are separated by at most two separator runes, a separator being a space of any width (`Zs`), a dash (`Pd`, `-`), `.`, `,`, `/`, `(` or `)`; with a `+` or `(` just before its first digit and a `)` just after its last included. Its count is its digits.

**Every piece becomes `[removed]`** (`contracts.RemovedMarker`), one marker for each run of pieces that touch or overlap, whatever their kinds; a piece of several kinds is counted once, under the first in `contracts.ShapeKinds`. The marker holds no digit, at sign or dot, so redacting twice changes nothing (U9).

**False positives, on purpose.** Fail closed means over-removing: dates (`2026-11-20` is eight digits), years (`2026`), amounts (`1.299,00`), room and order numbers, version numbers, and words joined by a dot under a real suffix (`hello.world`) are removed by the kinds that match them, and the rule editor says so beside each kind ("every number of 4 to 10 digits, years and amounts included"). **False negatives, stated**: an address or a number written out in words ("five five five…", "ana at example dot com"), a domain with no dot or under a suffix not in the list, a number split by more than two separators, a lowercase IBAN's letters (its digits are still an account number), anything inside an attachment (removed whole instead) or an image. A removal by meaning ("a phone number", "a home address") covers what a shape cannot, at the model's cost. The golden cases are `internal/redact/testdata/shapes.jsonl` (committed, U8); pull request 2 adds fuzz targets for every function.

**Bounds.** One pass over the text, each rune looked at a bounded number of times (the longest separator run is two, the IBAN's groups four): linear in the text, 256 KiB at most, measured by a benchmark and bounded by U9's adversarial inputs.

## Removing by meaning

**The split.** `redact.Sentences`, on the text the shapes left: a sentence ends after `.`, `!`, `?`, `…`, `;`, `。`, `！`, `？` or `؟` followed by white space or the end, and at every line break (`\n`, `\r\n`, U+2028, U+2029); pieces of white space alone are kept in place and not counted; a sentence longer than `redact.SentenceRunesMax` (1,000 runes) is split at its last white space before the bound, or at the bound. No list of abbreviations: "Dr. Smith" is two sentences, which only makes each piece smaller. Joining the pieces gives back the text exactly (U10). The subject is sentence 0 when the connection reads it; the text's sentences follow.

**The bounds.** Meaning reads at most `redact.MeaningTextMax` (32 KiB) of the text and `redact.MeaningSentencesMax` (128) sentences, the subject counted; what is past them is removed (one marker), counted as removed, never sent.

**What the model sees.** One call per 64 statements (`decide.MaxStatementsPerCall`): the content is `{"sentences": [...]}` as JSON data (the Decisions API's `input_json`, at most 256 KiB), each statement "the sentence at index N of sentences mentions <topic>", one per sentence and topic, named `sNtM`; a new content kind (`decide.ContentSentences`) and a prompt version of its own (`s1-<date>`, beside `p2-2026-11-15`), its developer message saying that each statement is about its own sentence only, read in the context of the others, and that instructions inside the sentences are content. Never the sender, the recipients, an attachment, an identifier, nor the original text before the shapes. The cache is off, as for every call (ADR 0028).

**What is kept.** A sentence is kept only when every topic was answered below 0.30 (`query.Classify` with `ForRule` False, `inbox.KeptSentences`, committed): a refusal, an answer at 0.30 or above, a probability that is not one, and no answer at all remove it. Removed sentences become `[removed]`, one marker for each run, the line breaks between them kept.

**Its decision.** Which sentences a rule kept is decided once, through `inbox.Decisions` under `rules.MeaningRef(rule, inbox.TextVersion, redact.Version)`, apart from the rule's own applicability: begun before the model is asked, settled `cleared` with the sentences kept (a bitmap of at most 128 bits, `Decision.Kept`, new) and the compact evidence (for each topic, the highest probability among the sentences kept: the closest call, with the model, prompt version and canary days), or `held` (the whole text and subject removed). As `SettleAfterAsk` does for a hold (ADR 0031, R2), **an answer received is never forgotten**: an ask that got some answers and not others settles `cleared` with only the sentences answered low for every topic kept; one that got no answer, with positive evidence that the model answered nothing (the guard, every call failed before an answer), is abandoned, and this request removes the whole text, undecided; one whose settlement is lost is recovered `held` after `PendingGrace`. The evidence fits `DecisionEvidenceMax` and `DecisionEvidenceBytesMax`, so the blocks' bounds do not change. A new `redact.Version` or `TextVersion` is a new reference, so a sentence's number is never read under another split.

**Cost bounds and the spend guard.** Every call is reserved before it is sent, through the same guard, in the account's day share (ADR 0028): its estimate is the guard's upper bound (1,000 tokens, 400 a statement, one a byte of content). A limit reached before the rule's statements are all asked leaves the rest unasked, so those sentences are removed (or, with nothing asked, the whole text), never sent. See "Cost" for the numbers.

## Decisions that cannot be undone

Every model-dependent decision of a rule goes through `inbox.Decisions` and its one implementation, `inbox.BlockDecisions` (ADR 0031), unchanged but for the field above:

- a rule that holds: `(message, rule ref)`, exactly as the default rule;
- a rule that removes: whether it applies, the same way under its own reference; and which sentences its topics keep, under its `MeaningRef`;
- a rule with `ModelOff`: nothing begun; its model atoms are Unknown, so it holds, or removes the whole text.

So whatever whoever writes the table does to kept answers, a message a rule held stays held, and a sentence a removal removed stays removed, for as long as the message exists; a decision lost fails closed. A mailbox with `k` model-dependent rules uses up to `k` (plus one per meaning rule) decisions a message: a day's 28,800 decisions (`decisions_per_day`) then decide 28,800 / `k` messages before the rest is held, undecided, which with the bounds above is still at least 2,400 a mailbox a day. `/status` keeps saying `decisions: strict`.

## Changes, and what they apply to

- **A new rule, a rule turned on, a scope widened, the default turned on**: applies to every request that begins after the save, through the enclave that saved it at once and through the other within `accountrules.Fresh` (five seconds), to every message, old mail included.
- **A rule's criteria, effect or removal changed**: a new version; its decisions are made afresh (ADR 0031, H20), the old version's never used and dropped with their messages.
- **A rule turned off and on again, renamed, or its scope changed**: the same version, and its final decisions still stand: a decision depends on the message and the rule, not on who reads.
- **A rule deleted, or the default turned off**: from the next request, as above; mail that a rule held may then leave. Every such change needs a fresh proof, like every other change (choice 4), and is recorded in a `rules` receipt.
- **Mail already given** cannot be recalled; receipts say which rules and versions applied to each answer.
- **An answer in flight** (a page or a read already being prepared, at most a request's minute) finishes under the rules it began with; it is not cut as a revocation cuts one (choice 12).
- **Delivery's notices** (ADR 0039) carry no mail: the vendor fetches through the same checks, which apply the rules in force when it fetches.

## The seam with delivery

ADR 0039 decides whether to send a notice for a new message or a schedule; this record states what it must call so rules apply before its matching, without designing delivery:

1. The rules of the connection: `accountrules.Source.Applying(ctx, account, clientID)` (committed); an error sends no notice and is tried again later.
2. Whether the message would be given: `inbox.Discloser.Message` with the `inbox.Disclosure` the connections service builds for that connection (`connections.Service.Disclosure`, which pull request 5 adds, so delivery and a read build it in one place: the rules, the connection's formulas and parts, the account, the model's gate), notifying only on an `Outcome` with `Disclosed`. A message held, not matched, or an error sends nothing, so a notice never reveals a held message.
3. Decisions it begins are the same records a later read meets (ADR 0031, H22).

`accountrulestest.Fake` and its `Contract` are what delivery's tests use for the first; `disclosetest` and the discloser's fakes for the second.

## Previews and consent

- **The consent screen** (ADR 0030) shows, under "what never leaves", the rules that will apply to that app (`Source.Applying` with the request's client): each by its sentence, what it does and who for, in the server's words; the snapshot it approves records them (`consentShown.Rules`, with versions). Its count of what passes the exact conditions applies the rules' exact parts; its preview with the model applies them all, listing what they hold (`held`) and what would leave with details taken out (`MatchPage.Removed`, committed).
- **The rule editor's preview** (`POST /api/v1/rules/preview`): a page of a mailbox's mail the draft's criteria apply to (True and Unknown, read for a rule), as the owner's previews are; with `ask_model`, the model asked as the owner's previews ask it (kept answers shared, the account's share), under the rules' notice; a preview begins and settles no decision. With `message`, one message as the draft's removal would leave it (`RuleRemovalPreview`), for the person only.
- **The connection's page** lists the rules that apply to it, as the sample's design does.

## Receipts

- A message given with details removed: `receipts.Delivered.Removed`, one entry per rule that removed something: the rule's reference and version, `contracts.RemovedSummary` (parts removed whole, pieces by shape kind, sentences removed of how many, whether the whole text was removed and why, attachments removed) and the evidence its decisions rest on. **Never the removed content**, and the senders and subject kept in the receipt are those that left (the reduced subject, never the original): a receipt keeps what left, not a copy of what did not.
- A message held by a person's rule: `Held` with that rule's reference, as the default's.
- A change of rules: a new kind, `rules` (receipts' version 3): which rule, its version before and after, what changed (made, criteria, effect or removal, scope, on, off, deleted, default on or off, notice agreed), the draft's identifier and its criteria's canonical identifiers, the notice; never the words. Written before the change is answered, as a consent is; a change whose receipt cannot be written is not made.
- `Receipt.Rules` keeps naming each rule and version a request applied.

The receipt's 256 KiB bound holds: a page of 50 messages, each with 16 rules' summaries and evidence, is within it (U24). The activity screen and the CSV (ADR 0041) show `Removed` and `rules` receipts; their words are that record's.

## /status and permaily-verify

`/status`'s `connections` keeps `rules` (the compiled default, `on` meaning on by default) and adds `person_rules`: `record: "strict"`, `effects: ["hold","remove"]`, `shapes: {version, kinds}`, `meaning: {prompt, threshold: 0.30, sentences_max: 128, text_max: 32768, topics_max: 3}`, `words: {model, prompt}`, `notice` (the rules' notice in force), `fresh_seconds: 5`, and the counts `rules_unreadable`. `permaily-verify` fails unless `record` is `strict` and every constant and version is the source's (`redact.Version`, the kinds, the prompts' versions, the threshold, the bounds). Nothing per account is ever on `/status`.

## The screens

In docs/web.md's terms (the sidebar's Rules, which the brief for this record called Settings → Rules; Settings keeps no rules of its own), replacing the sample screens `web/src/pages/rules/` with the server's:

- **`/rules`**: the default rule first, with its sentence and an on/off switch (turning it off asks for a fresh proof and says what it lets out); then the person's rules, each with its name, what it does, who for, its switch, and "Asks the model under an older notice" when `model_off`; "New rule"; at 16, "New rule" is off with why. The rules' notice, when not agreed, as a line with "Read and agree".
- **`/rules/new` and `/rules/:id`**: "Describe it in your words" (a text box, the rules' notice above it, "Turn into a rule"; while the model is unavailable, the box says so and the editor below is used); the criteria in the existing editor's terms (any of the criteria; each a set of conditions, each a field, an operator and values), mapped to the language's variables (the sample's "Label" and "Kind" go, ADR 0027); the effect (hold back, or take out: parts, kinds of data with the false positives said beside each, topics); who for (every connection, every one but, only; picking apps from the account's connections by domain); on; then **the sentence the server wrote** (`RuleCheck`), what it does and who for, which is what Save confirms, through `useConfirmed()`; a live preview of the mailbox's mail it applies to, and of one message as the removal leaves it, with the model on request. `rule_changed` and `rules_changed` read again and show what changed; `too_many_rules`, `invalid_rule` and `rule_words_unusable` say why in fixed words (`src/pages/auth/messages.ts`).
- **Delete** asks first and needs a fresh proof.
- **The consent screen and a connection's page** list the rules that apply (above).

Everything of a rule shown is the server's words or the person's own values, in `bdi`; nothing of a proposal is shown but its checked draft.

## Threat model

- **Whoever writes the table** cannot read, change or make a rule (sealed, bound to its name), cannot put back an earlier record (strict reads, refused) and cannot hide a change without every read of the account failing (a fork, a loss: nothing leaves). They can still delete a kept answer, which changes no decision once begun.
- **The host** sees ciphertext and the timing and size of writes; never the rules, the words or a sentence.
- **OpenAI** sees the person's words (under the rules' notice, `store: false`), and the sentences of the text the shapes left, for rules that remove by meaning; never the sender, recipients or attachments. Under ordinary retention until zero data retention is approved (ADR 0020).
- **The model proposing a rule** cannot make anything apply: its answer is checked as a vendor's formula, written back by the system as a sentence, and saved only by the person with a fresh proof. A proposal that holds or removes less than meant is what the person confirms by its sentence; the stated risk is that the person does not read it.
- **The sender of a message** can write text to push the model toward keeping a sentence (ADR 0020's accepted risk, bounded to that message); the fixed shapes, which need no model, cannot be pushed, and are applied first.
- **A vendor** cannot probe what was removed (its search reads the reduced message), is told nothing of holds, and sees `[removed]` where pieces were taken out. It cannot learn which rules exist beyond what the markers and absent parts show.
- **A stolen session** cannot weaken rules without a fresh proof; every change is in a chained receipt the person can check.

docs/threat-model.md gains these when pull request 8 composes them.

## Privacy page and data

docs/data.md gains a row for the rules' record (what: the rules, the default's switch, the notice agreed; sealed; for as long as the account; removed with it), the meaning decisions in the existing decisions' row, and the `rules` receipts in the receipts' row. The privacy page (pull request 8) says: the person's rules and their switch are kept sealed for as long as the account; what they write to make a rule is sent to OpenAI and kept nowhere by Permaily; a rule that removes by meaning sends OpenAI the sentences of a message's text after fixed shapes are removed; a rule's decisions are kept as the default rule's are; receipts say what each rule removed, never what. docs/legal.md's engineering item 2 is met for rules by the rules' notice; whether the terms' section 5 needs a new version for the person's words is an open question for the owner.

## Cost

- **Words**: one Responses API call a proposal, about 3,000 input tokens (the developer message and the schema) plus the words, and at most 2,000 output; priced at the provider's page read by pull request 3 and compiled with the prompt. Bounded by 30 calls an account a day per enclave and the account's 0.50 USD share.
- **Holds**: a person's rule's atoms join the default's and the query's in the same call for a message (at most 32 of the rules' and 8 of a query's in 64), so a message costs about what it costs now: about $0.00013 (ADR 0028's measure), a little more per statement.
- **Meaning**: a typical message (3 KiB of text, 25 sentences, one topic) is one call of 25 statements: about 2,500 real tokens, $0.00025, reserved at its upper bound of 14,000 ($0.0014). The worst (32 KiB, 128 sentences, 3 topics: 384 statements) is 6 calls, reserved at about 356,000 tokens ($0.036) and costing perhaps half. A page of 50 typical messages with one meaning rule: about $0.0125.
- **Storage**: one strict record per account and a commit per change (a handful a person); decisions within ADR 0031's blocks.
- **Spend guard**: unchanged: 50 USD a month, the alarm at 10 a day, 0.50 an account a day an enclave. A connection that reads past an account's share gets its meaning removals as whole texts removed, and its holds held, until the next day.

## What it guarantees, and what it does not

It guarantees: a rule saved applies to every request that begins five seconds later, through either enclave, and an earlier copy cannot undo it; a model-dependent decision of any rule, once begun, is never undone by the storage, and a decision that cannot be made or used holds, or removes the whole text; a vendor's search never reads what a rule removed; nothing of the account goes to OpenAI with the words; a rule never asks the model without the consent in force; every change of rules and every removal is in a chained receipt. It does not guarantee: that a model's first answer is right (a sentence that mentions a topic in words the model misses leaves); that fixed shapes catch data written in words or inside attachments (which are removed whole instead); that the person reads the sentence before saving; that mail already given comes back.

## The contract and the seams

Committed with this design, so pull requests 1 to 7 can be written at once:

- `internal/contracts/rules.go`: the rules routes' shapes (`RuleDraft`, `RuleScope`, `RuleRemoval`, `ShapeKind` and `ShapeKinds`, `RemovedMarker`, `RuleCheck`, `RuleSentence`, `RuleWords`, `RuleProposal`, `Rule`, `RuleDefault`, `RuleNoticeAgreement`, `RuleList`, `RuleSave`, `RuleDelete`, `RuleDefaultSet`, `RulePreview`, `RulePreviewPage`, `RuleRemovalPreview`, `RemovedSummary`), the bounds, the errors and `RuleProblem` with `RuleError`; `ErrorDetails`' third shape, `{"rule"}`; `MatchPage.Removed`. Tested by U1.
- `web/src/lib/api.ts`: the same types, `RULE_PROBLEMS`, `SHAPE_KINDS`, `REMOVED_MARKER`, `ErrorDetails`' third shape and `MatchPage.removed`; the calls and error codes come with pull request 7.
- `internal/rules/person.go`: `Remove`, `Removal`, `Scope` with `Covers`, `Record`, `Stored`, `RecordName`, the record's bounds, `Record.For`, `Compile`, `Rule.Model`, `MeaningRef`; `Rule.Removal` and `Rule.ModelOff` in `rules.go`. Tested by U2, U3, U5.
- `internal/inbox/meaning.go`: `SentenceAsker`, `SentenceQuestion`, `SentenceTopic`, `SentencesAsked`, `KeptSentences`, `RemovedBy`; `Disclosed.Removed`. Tested by U17.
- `internal/redact/redact.go`: `Version` and the meaning bounds, with the golden cases `testdata/shapes.jsonl`, whose form U8 checks now and pull request 2 runs.
- `internal/services/accountrules`: `Source`, `Applying`, `Fresh`, `ErrUnavailable`; `accountrulestest.Fake` and `Contract`, which every `Source` passes.
- `.golangci.yml`: `rules-are-pure` allows what `person.go` needs; `redact-is-pure` is new.

## Tests

U1 to U3, U5 (their seams' halves), U8 (the corpus' form) and U17 are committed with this design.

- **U1.** The contract's shapes, field by field, as the web app reads them; an invalid rule's details `{"rule"}` written and read back, never mixed with a formula's or a client's, those two written exactly as before; the six shape kinds and nothing else (`TestU1…`).
- **U2.** The record: named per account and epoch; made with `CreateNew`, written at the version read; sealed and bound (another account's name, another key, a changed byte: none opens); an earlier copy refused; lost, forked, unopenable: `ErrUnavailable`, nothing leaves; at its versions' bound it rolls to the next epoch with everything, a crash between `next` and the next epoch made again by the next writer; within `RecordBytesMax` with 16 rules at their largest (`TestU2…` for the names and the meaning reference; the rest pull request 1's).
- **U3.** `For` and `Source`: the default first unless off, then the rules on whose scope covers the client, holds before removes, each in the order made; a client connected later covered by `all` and `all_except`, never by `only`; an unknown scope covers everyone; a rule that does not compile is an error for the account, never a rule skipped (`TestU3…`, `accountrulestest.Contract`).
- **U4.** Freshness through two enclaves on strict records: a rule saved through A applied by A's next request; by B's within `Fresh`; a request that began before the save finishes under the old rules, and its receipt names them; never undone by an earlier copy.
- **U5.** The rules' notice: words and a model rule refused without it (`rules_notice_needed`); a rule saved under another notice, or an account that has not agreed to the one in force, is `ModelOff`, asks nothing (a played provider that fails the test when called), holds where it would need the model and removes the whole text for meaning (`TestU5…` for `For`; the rest pull requests 1 and 5's).
- **U6.** Every bound refused with its `RuleProblem` or `too_many_rules`: rules, bytes, criteria, a rule's and the account's model conditions, topics, meaning rules, scope clients (none, twice, too many, with `all`), the name, an unknown part, shape, effect or scope, a removal on a hold, an empty removal, the default's criteria.
- **U7.** Saving: the draft's identifier worked out again (`rule_changed`), the record's version (`rules_changed`), a fresh proof for every change; the version changes with criteria, effect or removal and only then; a `rules` receipt for each change, written before the answer, and a change whose receipt cannot be written not made.
- **U8.** The shapes' golden cases, each kind exercised, false positives and negatives included (the corpus's form now, `TestU8…`; pull request 2 runs every case).
- **U9.** Shapes' properties: redacting twice changes nothing; nothing a kind matches is left in the output; invalid UTF-8; 256 KiB of the worst inputs (digits and separators alternating, at signs, dots) within a linear bound; fuzz targets (no panic, idempotence, markers counted).
- **U10.** The split: line breaks, each ending punctuation, CJK and Arabic ones, a 1,000-rune sentence cut at its last space, white space alone not counted; joining gives back the text exactly; fuzz.
- **U11.** The words' request, against a played provider that records it: the pinned developer message and schema (golden, any byte a new `PromptVersion`), `store: false`, no tools, `max_output_tokens`, strict schema; the words and nothing else of the account (a test account whose mailboxes, connections and address are searched for in every byte sent).
- **U12.** The words' answer: outside the schema, past a bound, a criteria that never matches, an invented mailbox, an unknown shape, an effect with the wrong removal: `rule_words_unusable`, nothing proposed; a usable one: canonical, `scope: all`, `on`, the system's sentence, the model's text nowhere in the answer; nothing saved before `POST /api/v1/rules`.
- **U13.** The model unavailable for words: no key, a refused key, each limit, the account's share, a 5xx, a timeout: `model_unavailable`, nothing proposed; each call reserved before it is sent and settled once; the 31st call of a day in an enclave refused.
- **U14.** The order through the `Discloser`: a hold wins over a removal; the default before the person's; removals in their fixed order whatever the rules' order; the query reads the reduced message (a search for a removed number matches nothing, a condition on a removed sender is Unknown and does not match); the parts after.
- **U15.** Parts removed whole: each part; the receipt keeps the subject as it left; attachments removed by a rule removing shapes or topics, their names with them, counted.
- **U16.** A removal's applicability decided through `Decisions` as a hold is: begun before the ask, settled, recovered `held` after the grace and then applying; an Unknown value applies.
- **U17.** What meaning keeps: only sentences answered below 0.30 for every topic (`TestU17…`).
- **U18.** Meaning's decision through two enclaves on strict records: begun under `MeaningRef` before the ask; settled `cleared` with the sentences kept; partial answers settle with only the answered-low kept; nothing answered, abandoned and the whole text removed for the request; a settlement lost, recovered `held` and the whole text removed for good; the kept answers erased and the played model now answering low: the same sentences removed; a new `redact.Version` or `TextVersion` decides again.
- **U19.** Meaning's bounds: past 32 KiB or 128 sentences removed and never sent; 64 statements a call; every call reserved; the account's share reached mid-message: the unasked sentences removed.
- **U20.** What the model sees for meaning: the text after the shapes (a number the shapes removed never reaches the played provider), the subject only when the connection reads it, never a sender, recipient, attachment or identifier.
- **U21.** A query's model atoms on the reduced view: asked of the reduced text under its own view, an answer about the original never used for it, nor the reverse.
- **U22.** `ModelOff` rules and the model off: nothing sent for them; the account's model off still means nothing leaves (ADR 0030's decision 9).
- **U23.** A removal never adds a word: the output is the input minus pieces, each run replaced by one marker (a property test over the shapes and meaning together).
- **U24.** Receipts: `Removed` per rule with its summary and evidence, never removed content; a person's hold in `Held`; a `rules` receipt of each change's kind; a page of 50 messages with 16 rules' summaries within 256 KiB.
- **U25.** The consent screen: the rules for that client listed by sentence, what they do and who for; the snapshot records them; the preview's `held` and `removed`; an `only` rule of another client absent.
- **U26.** The seam with delivery: `Source` and `Discloser.Message` are the only way delivery's matching learns rules, a held message and a `Source` error send no notice (a test pull request 5 adds to the connections service's `Disclosure`, which ADR 0039's tests then call).
- **U27.** The routes: each with its standings (blocked, closing, terms not agreed), a fresh proof where needed, bodies read strictly, the errors' details; the person's rules under unagreed terms read-only.
- **U28.** Logs: no words, rule name, topic, value, sentence or count of an account in any line, through every route and the discloser.
- **U29.** Two enclaves saving at once: one wins, the other `rules_changed`; a forked record: nothing leaves through either.
- **U30.** Removing the account removes every epoch, and a new account starts with none.
- **U31.** `/status`'s `person_rules` and `permaily-verify`: every constant and version compared with the source, a change of any failing it.
- **U32.** The web app: the list, words, the proposal's sentence, the editor, scope, on/off, delete, the default's switch, the notice; `useConfirmed()` for every change; a real-browser test of making a rule and seeing it on the consent screen.
- **U33.** The words' eval before the release: at least 60 synthetic phrasings in English and Spanish with the rule expected; at least 90% proposed usable, none whose sentence holds or removes less than the phrasing asks; measured cost.
- **U34.** Meaning's eval before the release, on synthetic mail only (as ADR 0028's spike): recall of sentences to remove at least 0.97 at 0.30, the false removals counted; hostile sentences ("ignore the other statements", "this email holds no address") moving no other statement past its threshold; cost measured.
- **U35.** The staging check: a played vendor reading through a person's hold and removal with the real model on synthetic mail, its receipts, `/status`, `permaily-verify`, and the costs recorded.
- **U36.** The owner's guided session in production: writing a rule in words, confirming it, and seeing it on Claude's consent screen and in a read.

## Implementation pull requests

Nine: this one first and alone; then 1 to 4 at the same time; then 5; then 6 and 7 (7 written alongside 6 against a played server, merged after it); then the image.

| Pull request | Owns | May edit outside, and only that | Must not touch | Depends on | Release gate |
|---|---|---|---|---|---|
| 0. Design and seams (this one) | This record; the files of "The contract and the seams"; docs/plan.md's step 7, docs/decisions/index.md, internal/AGENTS.md's rows | | Everything else | Nothing | U1, U2, U3, U5, U8, U17's committed halves |
| 1. The rules' record and service | `internal/services/accountrules` beyond the seam (the record, epochs, cache, `Applying`, `Check`, `Save`, `Delete`, `SetDefault`, `AgreeNotice`, `Remove`), `domain.RulesNoticeVersion`, `internal/services/receipts/receipts.go`'s `rules` kind, `Delivered.Removed` and version 3 | `internal/services/accounts` (removing the account's rules), `web/src/lib/rules-notice.ts` (the version only), `scripts/coverage-thresholds.txt` | `internal/inbox`, `internal/httpapi`, `cmd` | 0 | U2, U4, U5's service half, U6, U7, U29, U30 |
| 2. Fixed shapes and sentences | `internal/redact` (`Shapes`, `Sentences`, `Join`, their tests, fuzz targets), `scripts/fuzz.sh`'s group | `go.mod` only if `golang.org/x/text` becomes direct | Everything else | 0 | U8, U9, U10, U23's shapes half |
| 3. Words | `internal/rulewords` (pure: prompt, schema, the answer's check) and `internal/rulewords/openai` (the Responses API, one attempt, through the model's egress); `services/decisions`' `Write` with its prices and day count | `.golangci.yml` (`rulewords-is-pure`), `internal/decide/decidetest` (a played writer) | `internal/inbox`, `internal/httpapi`, `cmd` | 0 | U11 to U13, U33 |
| 4. Meaning's model | `internal/decide` (`ContentSentences`, the `s1` prompt, its plan), `internal/decide/openai`'s sentences request, `services/decisions`' `AskSentences`; `internal/inbox/rule_decisions.go`'s `Decision.Kept` and `Settlement.Kept` and their bounds, `disclosetest`'s cases | | The discloser, `internal/services/connections` | 0 | U18's records half, U19's calls, U20's provider half, U34 |
| 5. Disclosure | `internal/inbox`'s discloser (the order, the views, meaning through `Decisions`, `Disclosed.Removed`), `internal/services/connections` (`Source` instead of `Config.Rules`, `Disclosure`, the consent screen's rules and preview, receipts' `Removed`) | `internal/services/receipts` only to fill what 1 added | `internal/httpapi`, `cmd` | 1, 2, 4 | U14 to U16, U18, U20 to U22, U24 to U26 |
| 6. Routes | `internal/httpapi/rules*.go`, the router's lines, the rules notice's route | | The services but by calling them | 1, 3; the removal preview after 5 | U27, U28's routes |
| 7. Web | `web/src/pages/rules/` on the server, the calls and error codes in `api.ts`, the words in `messages.ts`, `rules-notice.ts`, the consent screen's and connection page's rules, docs/web.md | | The server | 0; merged after 6 | U32 |
| 8. The image | `cmd/permaily-enclave`'s composition, `pkg/attestation`'s `PersonRulesStatus` and its check, `cmd/permaily-verify`, the staging check | docs/threat-model.md, docs/data.md, docs/legal.md, the privacy page, docs/operations.md, docs/verification.md, docs/acceptance.md, internal/AGENTS.md | | 1 to 7 | U31, U35, U36; the release |

Estimate: about four weeks of work in parallel (the plan's one week, less step 5's half, did not count words, meaning's records, the reduced view or the evals): 1 is six days, 2 four, 3 five with its eval, 4 five with its eval, 5 six, 6 three, 7 five, 8 three.

## Definition of done

Built, released and verified: every test above passing in CI; both evals (U33, U34) run and their numbers in this record; the staging check (U35) with its costs; a release tag whose image both builders rebuild byte for byte; deployed to production replacing the release before it, its KMS key made and the root secret handed over, as docs/deploy.md says; `/status`'s `person_rules` matching the source and `permaily-verify` passing locally and with `-require-smtp`; the privacy page published with the rules said as built; docs/acceptance.md's rows for rules filled; and the owner's guided session (U36). The default rule's switch and the person's own rules are then live for every account.

## Options considered

- **One record per rule**: changes of two rules never conflict, but reading an account's rules is one strict read per rule for every request; one record per account is one read, cached five seconds.
- **Rules inside the account's record**: one record fewer, but every sign-in and setting would write under the rules' version and the record's bound.
- **Scope by connection identifier**: exact, but a replacement or a re-connection makes a new identifier, and an `only` rule would silently stop covering the app; rewriting the rules' record in the same commit as the connection's is not something the store can do.
- **Applying only rules made before a connection**: simpler to explain for `only`, but `all` must cover connections made later, or every new app would leave every rule behind.
- **Words to a formula with the Decisions API**: it answers probabilities, not formulas.
- **Words without a model** (a keyword parser): no OpenAI, but "my home address except for Amazon" cannot be parsed reliably, and ADR 0019 promised words.
- **Showing the model's own explanation**: friendlier, but its words could claim what the formula does not do; the system's sentence is the only description.
- **Changing the model's notice instead of a rules' notice**: one notice, but every connection would get no mail until renewed (ADR 0030, finding 18), for consent that concerns rules only.
- **Evaluating the query on the original**: one view fewer, but a vendor's search would probe what a rule removed.
- **Removing before holding**: a hold's criteria would read a reduced message, and a rule meant to hold a message mentioning a number would miss it after another rule removed the number.
- **Regular expressions for shapes**: Go's are linear, but the digit group's separators, the transparent runes and Unicode digits are clearer and testable as one pass; the cases pin the behaviour either way.
- **Luhn for cards, a calendar for dates**: fewer false positives, at the cost of letting through a number that only looks invalid; fail closed is to remove.
- **A typed marker** (`[phone number removed]`): more useful to an agent, and it tells which kinds a person removes; one marker says only that something was removed.
- **Leaving attachments with a warning**: more useful, but a phone number inside a PDF would leave under a rule that says it removes phone numbers.
- **One call per sentence**: each sentence judged alone, which loses context ("my address is below:" then the address), and about forty times the cost of one call with the sentences numbered.
- **A model that rewrites** (ADR 0020 rejected it): it could add or change words; removing whole sentences cannot.
- **Kept answers for each sentence statement** (the `Asker`'s): the decision's record already keeps the outcome rollback-resistant; kept answers per sentence would add an entry per statement with nothing gained.
- **Cutting answers in flight on a change**, as a revocation does: a lease read again while an answer is sent, for a change that takes effect within seconds anyway; left out (choice 12).
- **A fresh proof only for changes that weaken**: less friction, but a classifier of what weakens (a scope narrowed, criteria changed, a rule replaced) is a place to be wrong; every change is simpler and fails closed.

## Choices made for the owner (to confirm)

Each is the option that fails closed and is the simplest to build and test; the alternative is what reversing it would mean.

1. **One strict record per account, in epochs, 16 rules at most.** Alternative: a record per rule, or more rules.
2. **Scope names the app (`client_id`), not the connection**, so a replacement or reconnection of the same app keeps "all but" and "only". Alternative: name connections, and let a reconnection start without the person's exceptions.
3. **A connection made later is covered by "all" and "all but", not by "only"**, and its consent screen says which rules apply. Alternative: ask the person, on the consent screen, whether each `only` rule should cover the new app.
4. **Every change of rules needs a fresh proof**, adding a rule included. Alternative: only changes that weaken.
5. **The words are kept nowhere**, so a rule is edited by its conditions afterwards. Alternative: keep them, sealed, in the rule, to edit in words later.
6. **A rule that removes shapes or topics removes the attachments too.** Alternative: leave them, saying beside the rule that attachments are not read.
7. **One fixed marker, `[removed]`, for every piece and sentence removed; the vendor is told nothing else.** Alternative: remove silently, or name the kind.
8. **Shapes over-remove**: dates, years and amounts are numbers; any domain under an ICANN suffix is a link; no Luhn. Alternative: exceptions for dates, amounts and invalid card numbers.
9. **Shapes read the subject and the text, not the `From` field**, which a rule removes whole or not at all. Alternative: redact display names too.
10. **Meaning**: the subject is a sentence; 0.30 is the threshold (the rules' band); 32 KiB, 128 sentences, 3 topics a rule, 4 meaning rules an account; past them, removed. Alternative: larger bounds at a larger cost, or the query's 0.80.
11. **A rules' notice of its own**, so no connection stops; a rule under an older notice asks nothing and holds. Alternative: a new version of the model's notice, which stops every connection until renewed.
12. **A change applies to requests that begin after it (five seconds through the other enclave); an answer being prepared is not cut.** Alternative: read the rules' version again while an answer is sent, as a revocation's lease is.
13. **The default rule is on or off, for every connection, and not editable.** Alternative: scope it, or let the person edit a copy.
14. **Holds before removals, the query on the reduced message.** Alternative: none simpler that keeps a vendor from probing.
15. **The words' model proposes criteria, effect and removal, never scope; a proposal is for every connection and on**, which the person changes before saving. Alternative: send the names of the account's connected apps to the model.
16. **A `rules` receipt for every change**, which makes receipts' version 3. Alternative: rely on the record's history alone.
17. **Rules do not apply to the person's own inbox and previews of a formula**, as the default does not. Alternative: show the inbox as a connection would see it.
18. **Turning a rule off and on keeps its version and its decisions.** Alternative: a new version, deciding again.
19. **30 words calls an account a day in an enclave**, besides the spend share. Alternative: the share alone.

## Open questions

1. **The Responses API's price and limits for the words' model** (`gpt-6-luna`, or a smaller one if it proposes as well): pull request 3 reads the provider's page and measures U33; the cost above is an estimate until then.
2. **Whether one call with numbered sentences is as accurate as ADR 0028's subject and body calls**: pull request 4's eval (U34) decides; if recall at 0.30 is below 0.97, one call per sentence is the fallback, at about forty times the cost, and this record is revised before pull request 5 merges.
3. **The terms**: whether section 5 must change for a person's words sent to OpenAI (a new terms version makes every account agree again, not every connection); for the owner, with docs/legal.md.
4. **ADR 0039** must adopt "The seam with delivery"; **ADR 0041** shows `Removed` and `rules` receipts and the rules' changes on the activity screen and in the CSV.
5. **The sample editor's "Kind" and "Label" fields** are not variables of the language (ADR 0027 dropped labels); the designer reviews the editor without them.
6. **Zero data retention** is still not approved (docs/legal.md): rules go out under the same ordinary retention as `about`, which the rules' notice says.

## What changes in other documents

- **docs/plan.md** (with this design): step 7's sub-list, its pull requests and estimate.
- **docs/decisions/index.md** (with this design): this record.
- **internal/AGENTS.md** (with this design): rows for `redact` and `services/accountrules`.
- **ADR 0031**: none to its text; its "How steps 7 and 8 extend them" is followed for step 7 by this record.
- **When built**: docs/threat-model.md, docs/data.md, docs/legal.md (engineering items 2 and 12: the Responses API's first call, `store: false`) and the privacy page, docs/web.md (the rules' notice's words, the screens), docs/verification.md (`person_rules`), docs/operations.md (what `rules_unreadable` means and does not), docs/acceptance.md.

## What it rests on

- ADR 0031's decisions, blocks and `SettleAfterAsk`, unchanged but for `Kept`; strict anchored records (ADR 0016, 0026).
- The query language's canonical forms, sentences and three values (ADR 0027).
- The Decisions API answering statements about numbered sentences in one content, which U34 measures before anything depends on it.
- The Responses API honouring `store: false` and a strict schema, which U11 and U33 check.
- The public suffix list as `golang.org/x/net` compiles it, fixed for a release.
