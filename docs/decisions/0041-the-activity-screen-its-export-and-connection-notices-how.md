# 0041 The activity screen, its CSV export and the notices about connections: how

Status: Proposed. Not built. Designed on 2026-11-20 without the owner, who is away and asked for F2's steps 6 to 9 to be done on their own; every product choice not already the owner's is listed in "Choices made for the owner (to confirm)", with its alternative, so it can be reversed. Builds on ADR 0017 ("Notices by mail"), 0019 ("Receipts"), 0021 (the watch on copies of what the enclave sends), 0025 (mail written once as blocks), 0030 and 0031 (connections, receipts) and 0037 (the chain, `GET /api/v1/receipts`, its export, `permaily-verify -receipts`). Amends ADR 0017 ("Notices by mail": the list grows, and notices about connections are queued and tried again) and ADR 0030 (choice 25: the notices about connections are no longer deferred). Coordinates by name only with ADR 0039 (delivery: webhooks and schedules) and ADR 0040 (rules), designed at the same time by others: what they add here is named as a seam, not designed ("Seams with ADR 0039 and 0040"). Its tests are E1 to E41; its pull requests are in "Implementation pull requests". Words of screens and mail are drafted here and marked "to review with the designer"; that review is the owner's, at the end.
Date: 2026-11-20

## Choices made for the owner (to confirm)

Each fails closed and is the simplest to build and test of what was weighed. The alternative is what the owner would pick instead; none of them changes a seam committed here except where it says so.

1. **The activity screen reads the existing route, unchanged, and filters in the browser.** `GET /api/v1/receipts` gives pages of the account's whole chain; the screen keeps the rows of the connection or the kind picked. *Alternative*: a `connection=` parameter on the route, the filter in the enclave. It reads the same storage (every receipt must be opened to know its connection), adds trusted code and a second way to leave a break out of a page.
2. **The CSV is made in the browser from the walk's pages.** *Alternative*: a route `GET /api/v1/receipts/export.csv` in the enclave. One request cannot hold a whole chain within a request's deadline, so it would be paged too, and a CSV cut short by a deadline looks complete; the browser's export is whole or nothing.
3. **The CSV is always the whole record, whatever the screen filters.** *Alternative*: export what is shown. Whole means nothing found is left out of a file a person keeps.
4. **No date filter at launch**: rows grouped by day, the newest first, and "Show older". *Alternative*: a from and to date, which the chain's order allows only approximately (links are in append order, a recovered link names receipts up to three days older).
5. **Activity and both exports stay reachable while the account is closing and under terms it has not agreed to**, in the limited shell the mailbox and connections routes use. The API already allows it (ADR 0037), and the record is deleted with the account: the closing week is when a person most wants to keep a copy. *Alternative*: Activity follows the rest of the app and shows only "being deleted".
6. **Seven notices**: a connection approved (new or replacing the client's), a device added, a connection renewed, one about to end, a device ended because a token of it was used twice, receipts found deleted or changed in the storage, and every device signed out by the operator or an admin. The plan named three; renewing extends access, a reused token is the mark of a stolen one, ADR 0037 left the incident notice to step 9, and docs/legal.md (engineering item 5) asks for the sign-out. *Alternative*: the three of docs/plan.md alone.
7. **"About to end" is one notice, seven days before the end, and only for a connection approved or renewed for more than 14 days**; nothing when it ends. *Alternative*: two notices (14 days and one day), and one at the end.
8. **No link in any notice.** "Renew it in Connections at app.permaily.com" is text, as every notice names the app today (ADR 0017, ADR 0025: only the sign-in message links). *Alternative*: a button to the connection's page, the first link a notice would carry.
9. **A vendor is named only when the image's verified list names it** ("Claude, by Anthropic (claude.ai)"); any other is "an app Permaily hasn't verified", never its domain or its claimed name, which a vendor chooses. *Alternative*: every vendor's domain, as the consent screen shows it.
10. **Queued once, sent at least once, tried for 24 hours.** A crash between SES taking the message and the mark being written sends it twice. *Alternative*: in line and at most once, as today's notices (dropped after five seconds).
11. **The notices that exist today stay as they are** (in line, at most once); only the seven above use the queue. Moving the others to it, which closes docs/legal.md's engineering item 5 in full, is a later pull request. *Alternative*: move them now.
12. **None of them can be turned off**, like every notice (ADR 0017). *Alternative*: a switch for "about to end", the one that is a reminder rather than a warning.
13. **No limit per account**; each kind is bounded by what causes it, and the queue's sending by the enclave's own rate. *Alternative*: at most N notices per account an hour, which would drop or merge security notices.
14. **The enclave reads no bounce or complaint.** SES's notification topics would carry the messages' headers (ADR 0021 reads none). *Alternative*: an SNS topic read by the enclave.
15. **A cell that would start a formula gets a single quote before it.** *Alternative*: drop the character, or put a space or a tab before it. The quote is OWASP's advice, shows plainly in the cell, and keeps every character the person's mail held.

## Context

Step 8 kept the activity screen and the CSV export once ADR 0037 brought the chaining forward (docs/plan.md). The route, its pages and the JSON export exist (`internal/httpapi/receipts.go`, `receipts()` and `exportReceipts()` in `web/src/lib/api.ts`), and nothing renders them: `/activity` still runs on sample data (`web/src/pages/activity`), with a sample "Export CSV" that guards nothing. "F2: lessons to apply" asks for an audit log "exportable (CSV guarded against formula injection, JSON)". The JSON export is the verifiable one (`permaily-verify -receipts`); the CSV is for people.

Step 9 keeps "the notices about connections (one approved, a device added, one about to end), which need step 5" (docs/plan.md); until then a fresh proof stands in for them (ADR 0030, choice 25). Today's notices (`internal/services/accounts/notices.go`) are sent in line, after the change is stored, for five seconds at most, and dropped when they fail or 32 are in flight. That is enough for an event that happens in one request in one enclave. It is not for "about to end": the hourly sweep that sees it runs in both enclaves, every hour, and restarts; sent in line it would go twice an hour or never. docs/legal.md (engineering item 5) also asks that notices be queued and tried again, not dropped.

Three facts bound the design. Two enclaves may run at once (ADR 0017, "Two enclaves at once"). SES's suppression list is the account's, so production shares it with staging (docs/operations.md). A notice proves nothing by itself (docs/threat-model.md): it tells, and the app shows.

## Decision in short

- **The activity screen** is `/activity`, on the existing route, the newest first, a page of 100 receipts at a time, every receipt a row with what left or was held, and every break of the chain a row of its own in every filter. A connection's page links to its activity (`/activity?connection=<id>`). ("The activity screen")
- **Two downloads.** "For checking" is the JSON export as it is (ADR 0037). "As a spreadsheet" is a CSV the browser writes from the walk's pages: UTF-8 with a byte order mark, CRLF, every cell quoted, controls and the characters that reorder text replaced, and a single quote before any cell a spreadsheet would read as a formula. Whole or nothing. ("The CSV export")
- **Seven notices by mail**, from fixed words, naming a vendor only from the image's verified list, with no link, code, token or identifier. ("Which notices", "What a notice may say")
- **A queue in the storage** (`internal/services/notices`): each notice an entry created once under a key the event names, so two enclaves, a retry and the hourly sweep queue it once; tried at once, then by a runner in each enclave under a lease, for 24 hours; marked when sent, so it is not queued again for 30 days. At least once, rarely twice. ("The queue")
- **Seen on `/status`** under `mail.notices`: what the enclave that answered queued, sent, retried, dropped and skipped. `permaily-verify` prints it and warns on anything dropped. ("What /status and permaily-verify show")

## The activity screen

### What it reads

`GET /api/v1/receipts?before=&limit=100` (`receipts()`), from the head, then from each page's `next`, as ADR 0037 built it; nothing in the enclave changes for the screen. The connections' names come from `GET /api/v1/connections` (the list the connections pages already read); a receipt of a connection no longer listed shows its vendor's domain alone. The search of a receipt is put in words only when the person asks, by `POST /api/v1/queries/check` with the receipt's search formula ("What a receipt shows").

**Why not a new route.** A filter by connection must open every receipt to know its connection, in the enclave or in the browser: the storage read is the same. Filtering in the enclave adds a rule to an adapter (adapters hold none) and a second place where a broken link or receipt could be left out of a page. Filtering in the browser keeps every page whole as the chain gave it.

### What it lists

One row per receipt, in the walk's order (links in descending number, a link's receipts in descending index), grouped under the day of the receipt's own time in the person's time zone (`Intl`, as dates are everywhere in the app). A row shows the time, the vendor (verified owner's name, or the domain in monospace), what it was about, and its outcome:

| Kind | Row (to review with the designer) | Outcome chip |
|---|---|---|
| `search`, `read`, `attachment` | One row per message: its first sender and subject (as text, in `bdi`), the parts given; a message held is a row of its own | "Made available", or "Held by <rule's name>" |
| `refused` | What was asked and why | "Refused" with the reason in words |
| `consent` | "Approved a connection", "Added a device", "Renewed a connection", "Refused a request" | "Approved", "Refused" |
| `ended` | "A connection ended" or "A device ended", and why | "Ended" |
| `refusals` | "Refused 42 requests in a minute" | "Refused" |
| A receipt the walk did not find `ok` | Its position and what was found ("Verdicts on the screen") | the verdict |
| A receipt this app cannot read (another version or kind, `readReceipt` null) | Its identifier and position, "This app can't show this receipt" | none |
| A link that could not be read | Its number and what was found | the verdict |

A search page that delivered nothing and held nothing is one row, "Searched, nothing matched". "Made available", never "received": a receipt records what was prepared and released, not what the vendor's side got (ADR 0031).

### Filters and pages

- **Connection**: every connection, or one (`?connection=<id>`, the only state in the address; a connection's identifier is on its own page already, and no message, search or address ever goes there, web/AGENTS.md). The connection's page gains "Activity" (a link).
- **Kind**: everything, made available, held or refused, approvals and ends. In memory only.
- **Pages.** The first load reads one page; "Show older" reads the next. With a filter, one press reads up to five pages (500 receipts) or until 50 rows match, and then says how far it looked: "No receipt of this connection in the newest 500. Look further." The walk's own bounds (8 MiB sealed, 256 links a page) can make a page short or empty with `done` false: the screen says "Looked through 3 links" and offers "Look further", the inbox's state for a short page with more. `done` ends it ("That's the start of your record.").
- **Breaks pass every filter.** A row whose verdict is not `ok`, of a link or of a receipt, and a receipt this app cannot read, are shown whatever is picked: a deleted receipt has no connection to filter by, and a filter must never hide what was found.

### What a receipt shows

Opening a row shows its receipt beside the list from 80rem, under the row below it, as the sample does. Senders, subjects, topics and sentences are what mail and vendors wrote: React puts them as text, each in `bdi`; no address becomes a link (web/AGENTS.md).

- **What left**: for each message, its mailbox (by the label the person gave, from `GET /api/v1/mailboxes`, or "A mailbox that was disconnected"), when it was received, its senders and subject when those parts were given, the parts given (Sender and date, Subject, Body text, an attachment by its index), and for an attachment which. Never the text, which a receipt does not keep. The sample's "N characters" goes: no receipt records it.
- **What a rule held**: the rule by name and version, and how it decided: "by exact words", "by the model", "held because its decision was lost" (`recovered`), "held because it couldn't be decided" (`undecided`).
- **What a rule removed**: nothing yet; ADR 0040 adds what a rule removes from a message, as a field of the receipt this screen shows, and a column of the CSV ("Seams with ADR 0039 and 0040").
- **Whether a model decided**: when the receipt holds the model's answers (`model`, a rule's `cleared`, a held message decided by the model), "A model decided" with each answer: the condition in words ("subject is about invoices"), its probability (two decimals), the model and the prompt's version, and the canary's days behind an `InfoTip`.
- **Why it was allowed**: the connection (a link to its page while it is listed), the vendor, through the API or MCP, the device; for a search, "Show the search in words", which asks `POST /api/v1/queries/check` with the receipt's search formula and shows its sentence; a formula that route refuses (a later version of the language) is shown as its canonical JSON in monospace. The consented query is the connection's sentence, shown on the connection's page; a consent receipt shows the sentences it showed (`consent.sentences`), the parts, the mail since, the end and the model's notice's version.
- **Technical record** (folded): the receipt's identifier, its position (`<n>.<index>`), its digest, its link's kind, the link before by digest. The sample's made-up hash goes.

### Verdicts on the screen

To review with the designer. Each is words, never colour alone; a break is styled as a fact, not as an error to try again.

| Of | Verdict | Words |
|---|---|---|
| Receipt | `missing` | "Receipt 1,234.3 was deleted from the storage." |
| | `changed` | "Receipt 1,234.3 was changed in the storage: it isn't what was recorded." |
| | `unreadable` | "Receipt 1,234.3 can't be read: something else is stored in its place." |
| Link | `lost` | "Link 1,234 is missing from the storage: the receipts it named can't be listed." |
| | `forked` | "Link 1,234 was written twice: the storage holds two versions of it." |
| | `unreadable` | "Link 1,234 can't be read: something else is stored in its place." |
| | `prev` | "Link 1,234 doesn't match the link after it." |
| | `expired` | Shown as `ok`, with an `InfoTip`: "This link's lock ended. What it says is still checked by the link after it." |
| Link kind | `recovered` | A chip "Linked late", `InfoTip`: "This receipt was found without a link by Permaily's daily check, and linked then." |
| | `adopted` | A chip "From before", `InfoTip`: "This receipt was there when your record was first chained. Nothing can show it wasn't changed before that." |
| Break of a link (`break`) | `lost`, `forked`, `unreadable` | Under the link: "The link before this one couldn't be read when this one was written." |

### The head

Above the list: "Your record has 1,234 links." and, when the head is `ok`, "Newest: link 1,234, sha256 3f2a…9c1d" with "Copy" (the full number and digest as `1234:<sha256>`, what `permaily-verify -receipts-head` takes), and an `InfoTip`: "Note this before you download your record for checking. permaily-verify checks the file against it." A head that is not `ok` says so in the words of its verdict and offers nothing to note.

### States

To review with the designer.

- **Loading**: the list `aria-busy`, "Reading your record…".
- **Empty** (head `n` 0): "Nothing has left your mailboxes." and a link to Connections.
- **`receipts_unreadable`**: its fixed words (`src/pages/auth/messages.ts`), no retry offered.
- **`busy`**: "Your record is busy. Try again in a moment." and Try again.
- **No answer**: "Permaily can't be reached." and Try again; rows already shown stay.
- **`invalid_before`** (a position the chain does not have): start again from the head.
- **Closing or not agreed**: the screen in the limited shell, with both downloads (choice 5).

### Accessibility

The page's `h1` is "Activity" (focus moves to it as on every page). Days are `section`s named by their day; rows are buttons with `aria-expanded` (below 80rem) or `aria-pressed` (beside the list), as the sample has them. "Show older" keeps focus after it loads, and a polite live region says "Showed 37 more receipts" or "No more". The verdicts are text. The export's progress is a `role="status"` with a Cancel button; the end says what was saved. axe in every state (`expectAccessible`), and the manual pass of docs/accessibility.md before the release.

### What it never does

It stores nothing (no receipt, filter or position in storage: web/AGENTS.md's three things stay three), puts nothing of a receipt in the page's title, the address (but `?connection=`) or a log, and sends nothing to the server it did not get from it but a search formula to the check route, on the person's press.

## The CSV export

### Two files

| | For checking | As a spreadsheet |
|---|---|---|
| What | The export's pages as stored, one JSON object a line (ADR 0037) | One row per message made available or held, per refusal, per approval, per end, per count of refusals, per break |
| Made by | `exportReceipts()`, unchanged | `web/src/lib/csv.ts`, in the browser, from `receipts()`'s pages |
| Checked by | `permaily-verify -receipts`, against heads the person noted | Nobody: a person reads it |
| Name | `permaily-receipts-<YYYY-MM-DD>.jsonl` | `permaily-receipts-<YYYY-MM-DD>.csv` |

The date is the person's local day when the download began. Neither name holds the account, an address or anything of the head. The JSON download keeps the bytes `exportReceipts()` returns, untouched, so the file is the one `permaily-verify -receipts` reads.

### Why in the browser

The browser already pages through the walk for the screen. A route in the enclave would have to be paged too (a whole chain is not read within a request's minute), and a CSV cut short by a deadline reads as complete. In the browser the file exists only once every page was read: a page that fails, a cancel or a closed tab leave no file. Nothing new is trusted in the image. No `Content-Disposition` is needed: the file is a `Blob` saved by an `<a download>`, as every download of the app is a same-origin one; were it ever served, it would be `text/csv; charset=utf-8`, `Content-Disposition: attachment; filename="permaily-receipts-<date>.csv"`, `nosniff` and `no-store`.

**Bounds.** None on the record's size: the file is built as a list of chunks, one per page, joined into a `Blob` only at the end, so the memory it takes is the file's. Progress says how many receipts were read; Cancel stops at the next page. The JSON export works the same way (it holds its pages in memory today; it moves to chunks too).

### Columns

Committed in `CSV_COLUMNS` (`web/src/lib/csv.ts`). New columns only go at the end; none changes place or meaning. Every cell is text; empty when it does not apply.

| Column | What |
|---|---|
| `receipt` | The receipt's identifier (`rcp_…`) |
| `position` | `<link>.<index>`; a link's number alone for a link's row |
| `record` | What the walk found, in the screen's words: "kept", "deleted from the storage", "changed in the storage", "can't be read", "linked late", "from before", "link missing", "link written twice", "link can't be read", "link doesn't match", "this app can't read it" |
| `time` | The receipt's time, RFC 3339 in UTC |
| `kind` | The receipt's kind |
| `outcome` | `made available`, `held`, `refused`, `approved`, `refused request`, `renewed`, `ended`, `nothing matched` |
| `vendor` | The vendor's domain |
| `verified` | The owner the image's list names, or empty |
| `connection`, `device` | Identifiers |
| `through` | `api` or `mcp` |
| `mailbox`, `message` | Identifiers |
| `received` | The message's received time, RFC 3339 in UTC |
| `from` | The senders as `Name <address>`, separated by `; ` |
| `subject` | The subject, when it was given |
| `parts` | The parts given, separated by spaces |
| `attachment` | The attachment's index |
| `rule` | `<id> v<version>` of the rule that held, or that cleared on a model's answer |
| `reason` | Why held (`exact`, `decided`, `recovered`, `undecided`), refused (the reason), or ended (the end reason); for a count of refusals, `<operation>: <reason>` |
| `model` | Each answer the decision rested on, `<var> <op> "<topic>": <p> (<model>, <prompt>)`, separated by `; ` |
| `count` | For a count of refusals |
| `search` | The vendor's search, as its canonical JSON |
| `sha256` | The receipt's digest as its link names it |

### Encoding and the guard

- **UTF-8 with a byte order mark** (spreadsheets read UTF-8 without asking only with it), rows ending in CRLF, a header row of the column names (RFC 4180). Every cell is quoted and its quotes doubled, so no comma, quote or line break of a cell ends it.
- **Characters that never reach a cell as themselves**, each written as U+FFFD: C0 and C1 controls and DEL (a tab or a line break ends a cell or a row in a reader that ignores quotes), U+2028 and U+2029, and the characters that reorder text (U+061C, U+200E, U+200F, U+202A to U+202E, U+2066 to U+2069), which could make a subject read as another in a cell.
- **Formulas.** A cell whose first character, after any white space and characters that show nothing (U+200B to U+200D, U+2060, U+FEFF and the U+FFFD above), is `=`, `+`, `-`, `@`, `|` or `%`, or their full-width forms, gets a single quote before it (OWASP's CSV injection advice, with the pipe and percent of older spreadsheets' commands and the full-width forms some read as their ASCII). The rule is checked on the cell after controls are replaced, so a tab or a carriage return first is caught too. The quote shows in the cell; nothing of the subject is lost. Numbers the export writes (positions, counts, indexes) start with a digit and are never changed. `csvCell` and `csvRow` are committed with this design, with their vectors and a property test (E13, E14).

### Relation to the JSON export and the verifier

The CSV is made from the walk, not from the export: it shows what the enclave found, which the person can read, and proves nothing. A person who wants proof downloads the JSON and runs `permaily-verify -receipts <file> -receipts-head <n>:<sha256>` with the head they noted (docs/verification.md, "Your receipts, offline"). The screen says so behind the head's `InfoTip`; the CSV has no line saying it, since every cell is data.

## Which notices

Words to review with the designer; "When" is every notice's first fact, as today.

| Kind | Sent when | Found by | Key (the event) | Subject | Facts |
|---|---|---|---|---|---|
| `connection_approved` | The person approved a request that made a connection or replaced the client's (`ChangeNew`, `ChangeReplace`) | `connections.Decide`, after the commit | The consent receipt's identifier | "A connection to your mail was approved" | When, App, Reads (the parts), Through (API or MCP), Ends |
| `device_added` | The person approved a request that added a device (`ChangeAddDevice`) | `connections.Decide`, after the commit | The consent receipt's identifier | "A device was added to one of your connections" | When, App, Reads, Ends |
| `connection_renewed` | The person renewed a connection | `connections.Renew`, after its write | The consent receipt's identifier | "One of your connections was renewed" | When, App, Ends |
| `connection_ending` | A connection approved or renewed for more than 14 days ends within 7 | The hourly sweep (`sweepAccount`, which reads every active connection's record) | The connection and its end | "One of your connections ends in a week" | App, Ends |
| `device_ended_reuse` | A device was ended because a refresh token or a code of it was used again after its grace (`EndedByReuse`) | `connections` token endpoint, after the device's end is written | The connection and the device | "Permaily ended a device of one of your connections" | When, App |
| `receipts_incident` | A deletion or change of the account's receipts or links was recorded in the record of incidents (ADR 0037) | The receipts' chain, after the incident's entry commits | The UTC day | "Some of your receipts were deleted or changed" | When |
| `devices_signed_out` | The operator or an admin signed every device out of the account | `accounts.closeAccountSessions`, after the change | The account's count of sign-outs after it | "Every device was signed out of your Permaily account" | When |

Not sent: a request refused by the person (nothing changed), a connection the person revoked themselves or the vendor revoked (the person or the vendor did it; the connection's page says so), one that ended at its end (the week's notice said it would), and an account's connections ended by its deletion (the deletion's notice says it).

### The words

Drafted, to review with the designer. Each is blocks of `domain/mail` (a lead, facts, a paragraph, a note): no new kind of block, no new template, so `packages/mail` changes by its fixtures only. "App" is "Claude, by Anthropic (claude.ai)" for a verified vendor (the owner's name and domain from `vendors.List`, compiled in) and "An app Permaily hasn't verified" for any other. "Reads" is the parts as "Sender and date, subject, body text, attachments". Times are as every notice writes them ("20 November 2026, 10:00 UTC").

- **Approved.** Lead: "A connection to your mail was approved on your Permaily account. The app below can read the mail it allows until it ends." (with "It replaces the connection that app had before." when it did). Paragraph: "You can see what it reads, and revoke it, in Connections at app.permaily.com." Note: "If this wasn't you, sign in at app.permaily.com, revoke it in Connections, sign out the devices you don't know, and write to support@permaily.com."
- **Device added.** Lead: "A new device was added to one of your connections. It reads the same mail the connection reads." Paragraph and note as above.
- **Renewed.** Lead: "One of your connections was renewed. It keeps reading the mail it reads now until its new end." Paragraph and note as above.
- **Ending.** Lead: "One of your connections ends in a week. After that, the app below can no longer read your mail." Paragraph: "To keep it, sign in at app.permaily.com, open Connections and renew it. To let it end, do nothing." No note.
- **Device ended for reuse.** Lead: "Permaily ended a device of one of your connections, because a key that device reads with was used twice. That can mean someone copied it." Paragraph: "The app may ask you to connect again: approve it only if you asked for it." Note: "If you don't know why this happened, revoke the connection in Connections at app.permaily.com, and write to support@permaily.com."
- **Receipts incident.** Lead: "Permaily found that some of the receipts that record what left your mailboxes were deleted or changed in its storage." Paragraph: "Open Activity at app.permaily.com to see which. Your receipts after them are still recorded, and Permaily's public check now fails until this is looked into." Note: "To ask about it, write to support@permaily.com."
- **Devices signed out.** Lead: "Permaily's operator signed every device out of your Permaily account." (or "An admin of Permaily …"). Paragraph: "You can sign in again at app.permaily.com." Note: "To ask why, write to support@permaily.com."

Settings' Notifications says, with no switch (to review with the designer): "Permaily emails you only about security and access: a sign-in from a new device, a change in how you sign in or in your contact address, your account being deleted or blocked, a connection approved, renewed or given a new device, a connection that ends in a week or loses a device for safety, receipts found deleted or changed, and every device signed out by Permaily. These emails can't be turned off."

### What a notice may say

ADR 0017's rules, applied to connections: fixed words; no code, link, token or identifier (no `con_`, `fam_`, `acc_`, `rcp_`); nothing a person typed (no mailbox label, no name); and nothing a vendor chose: no client name, no unverified domain, no query, sentence or formula, no topic. The vendor is named only from the image's verified list (choice 9): a domain is a vendor's choice, and "permaily-security.example" in a message from Permaily reads as a warning from Permaily. Facts are times, parts and the resource, each from a fixed list. Mail clients may turn `app.permaily.com` and `claude.ai` into links on their own; the message carries no `href` (ADR 0025's tests). No mail content ever: a notice is about access, not about a message.

## The queue

`internal/services/notices`, committed as a seam (`notices.go`) with its fake and contract (`noticestest`); pull request 1 writes it.

### Its records

- **A queued notice** is an entry of `notices#queue` under its key, created with `PutEntry` at version 0 (`store.ErrExists`: it was queued, by this enclave or the other), signed (`envelope.Sign` under `permaily notices v1`) bound to its partition and key, holding the notice (`notices.Notice`: the account's identifier, the kind, the key, when, the facts), its attempts, when the next is due, and the lease of the enclave trying it (`lease_until`, `by`). It is changed only at the version read (`PutEntry` at its version), deleted with `DeleteEntryAt`.
- **A mark** is an entry of `notices#sent` under the same key, created at version 0, signed: sent, dropped or skipped, and when. It expires after `KeptFor` (30 days): the same event is not queued again while it lasts.
- **Neither holds the contact address**: the Deliverer reads it from the account's signed record when it sends, so a changed address gets the notice and the queue holds no more of the person than the connection's record already shows the operator (the vendor's domain, the parts, the dates; ADR 0030). docs/data.md gets both rows.
- **A key** is `notices.Key(account, kind, parts…)`: the SHA-256 of a fixed prefix, the account, the kind and the event's parts, each after a zero byte, as 32 hexadecimal digits. It names the event and carries nothing readable of it.

### Enqueue, then send

1. **Enqueue** (`Queue.Enqueue`), by the caller, after its change is stored: `Check` refuses a notice that is not one of its kind with its facts (`ErrInvalid`); a mark under its key means it was sent (nil); otherwise the entry is created at version 0, its lease held by this enclave for `LeaseFor` (two minutes, longer than `sesmail`'s 15-second attempt). Bounded by `EnqueueMax` (two seconds). An error fails nothing of what caused it: the caller logs the kind and goes on, as a notice that could not be sent today.
2. **The first attempt** starts at once in this enclave, in the background (the request is not held), within the in-flight bound: `Deliverer.Deliver`, which reads the account, writes the message from its kind's words and sends it through accounts' `Mailer`, the watch of ADR 0021 included.
3. **Sent**: the mark is created, then the entry is deleted at the version read. A crash between them leaves both; the runner finds the mark and deletes the entry without sending.
4. **Failed**: the entry is written at its version with the attempt counted and the next due after `RetryFirst` (a minute), doubling up to `RetryMax` (an hour).
5. **Skipped** (`notices.ErrSkip`: the account is gone, closing or blocked, or its record cannot say it is open or not agreed): marked skipped, deleted, counted.
6. **The runner**, every `RunEvery` (a minute) in each enclave, lists `notices#queue`, and for each entry that verifies, is due and whose lease ended: reads its mark (present: deletes the entry), takes the lease by writing the entry at the version read (`store.ErrChanged`: the other enclave took it; skip), sends, and goes on as in 3 to 5. An entry that does not verify is counted `refused` and left; nothing sends it. One older than `RetryFor` (24 hours) is marked dropped, deleted and counted; it is not tried again.

### Exactly once, at least once

Queued exactly once per key: `PutEntry` at version 0 lets one writer make it, whichever enclave, and a mark keeps it from being made again for 30 days. Sent at least once: a send that SES took and whose mark was not written (a crash, a storage that failed) is sent again after the lease. That is the one way a notice goes twice. The other way round, the queue loses a notice only when whoever writes the table deletes its entry, or when it was not sent in 24 hours of trying; both are visible, the first not to the enclave (one notice fewer, as ADR 0017 accepted of devices' entries) and the second on `/status` (`dropped`).

**Why not in line, as today** (choice 10). In line, at most once, is what accounts' notices do, and loses a notice to any SES failure, to the watch refusing to send for a minute, and to a restart. For "about to end", sent by a sweep in both enclaves every hour, in line would send it twice an hour or need the same create-once entry anyway. Strict anchored records were weighed and are more than this needs: a notice is not an authority, and an entry rolled back or deleted costs one notice more or fewer, never access.

**The cut before the queue.** A notice is queued after its change commits. A crash between the commit and `Enqueue` (milliseconds) loses that notice, as today; queuing it before the commit would tell of an approval that may then be refused, and checking at send time that it happened would make the Deliverer read connections. Accepted, and said in the threat model.

### When "about to end" is due

`notices.EndingDue(days, endsAt, now)`: the connection was approved or last renewed for more than `EndingMinDays` (14) days, has not ended, and ends within `EndingBefore` (7 days). The sweep, which reads every active connection's record each hour in both enclaves, calls `Enqueue` with `EndingKey(account, connection, endsAt)` for each that is due; an enclave remembers the keys it queued since it started, so an hour's pass reads nothing for them again. A renewal moves the end, so the new end has a new key and is told a week before it. A connection that ends, is revoked or replaced before the notice is sent: the Deliverer reads nothing of connections, so the notice goes; its words say "ends in a week" of a connection the app then shows ended. Accepted: the sweep queues only live connections, and the window is the hour between two passes.

### Bounds and throttling

- Each enclave's runner sends at most `SendsPerSecond` (2) and has at most `InFlightMax` (8) attempts at once; with two enclaves that is 4 a second of SES's 14 for the whole account, leaving the rest to sign-in messages.
- No limit per account (choice 13): approvals and renewals need the person's fresh proof; a device ends for reuse once; an ending is one per connection and end; an incident one a day; a sign-out one per operator's action. A connection's 16 devices and an account's 64 active connections (`ListActiveMax`) bound the rest.
- The queue's entries are small (under 2 KiB). A runner lists them all each minute: at a thousand queued, a few reads a minute.

### SES failures, the watch, bounces

- **SES refusing or not answering**: an attempt that failed, tried again for 24 hours. **The watch** (ADR 0021): while it refuses to send, attempts fail the same way; notices wait, and nothing goes another way.
- **Bounces and complaints are not read** (choice 14). SES puts an address that bounced for good on the account's suppression list and then accepts sends to it without delivering them: the queue counts those as sent. The list is shared with production, so a staging rehearsal must give every test account an address that exists and takes mail, as docs/operations.md already says; dev mode keeps every notice in `/dev/outbox` and sends nothing. Removing a deleted account's address from the list stays the operator's (docs/legal.md, engineering item 9).

### No switch

They cannot be turned off (choice 12): each is about access to the person's mail. No unsubscribe link or `List-Unsubscribe` header, as no notice has one (ADR 0025); they are transactional (docs/legal.md, "Promotional mail").

### Templates and previews

No new kind of block and no new template. Each message is added to `sampleMails` (`internal/services/accounts/mail_samples_test.go`) in every variant (verified and not, replaced and not, API and MCP, each set of parts, operator and admin); `-update` writes its golden text and its fixture in `packages/mail/fixtures`, so `bun run dev` there previews it with the others; `internal/domain/mail`'s checks of the HTML run on it. The preview's "Send" and "Spam" tabs stay unused (packages/mail/AGENTS.md).

## Seams with ADR 0039 and 0040

Named, not designed here.

- **ADR 0039 (deliveries)** may add a receipt kind for a webhook's call or a scheduled run: until this screen learns it, `readReceipt` answers null and the row says "This app can't show this receipt", with its identifier and position, never hidden; the CSV writes its row with `record` "this app can't read it". A notice of deliveries that keep failing, if it wants one, is a `notices.Kind` added in `Kinds()` with its words, its key and its caller; the queue needs nothing more.
- **ADR 0040 (rules)** adds what a rule removes from a message (a field of `Delivered`, its name ADR 0040's): the screen shows it under "What left" and the CSV gains a column at the end. A notice of a rule turned off (the default rule above all), if it wants one, is a kind added the same way.

## What /status and permaily-verify show

`attestation.MailStatus` gains `notices` (`attestation.NoticesStatus`, committed with this design): `since`, and what the enclave that answered `queued`, `sent`, `failed` (attempts), `dropped`, `skipped` and `refused`, every one also at zero. Each enclave's own, as `/status` always is: a notice is queued once for the deployment and either enclave may send it. Nothing of an account, a key or a kind of anyone's event. `permaily-verify` prints them and warns when `dropped` or `refused` is above zero; it fails on none of them: a notice tells, the app shows, and what the verifier asserts is what the image does, which the measurements say.

## The threat model

What docs/threat-model.md gains (pull request 7):

- **The queue in the storage.** Whoever writes the table can delete a queued notice (one fewer) or a mark (one more, for an ending the sweep sees again), and can hold one back by nothing but deleting it. They cannot make, move or edit one: entries are signed with their partition and key, and one that does not verify is counted and never sent. They cannot send a notice to another address: the Deliverer reads the address from the account's signed record. They cannot make an ending early: the end is read from the connection's strict record.
- **What a notice tells AWS and the person's mail provider**: that the account has a connection to a verified vendor or an unverified app, its parts, its end; never the query, the mailboxes or a message. Sealed it is not: SES reads every message the enclave sends (ADR 0021).
- **A vendor cannot put words in a notice**: no client name and no unverified domain is ever written, so a vendor cannot make Permaily's mail say what it wants.
- **The CSV**: mail is hostile input, and a spreadsheet runs formulas. Every cell is guarded (controls, reordering characters, formulas); the JSON export is unchanged and is the one to verify.
- **The screen**: senders, subjects and topics are text in `bdi`, never markup, as everywhere in the app.
- **Accepted**: a crash between a commit and its notice loses the notice; a crash after SES took a message and before its mark sends it twice; bounces are not read.

## Data and the privacy page

- docs/data.md gains rows for the queue's entries (signed; the account's identifier, the kind, the key, when, the facts: a verified vendor's domain, the parts, the resource, the end, who signed devices out; kept until sent, at most 25 hours) and the marks (signed; the key and the outcome; 30 days), and the notices in transit through SES (the contact address and fixed words).
- The privacy page's list of notices (`site/src/pages/privacy.astro`, "Mail we send you") gains the seven, and the activity screen and the CSV are said as built where it says the receipts' screen is planned. Not a material change: more notices of the same kind, and the person's own data shown to them. `LEGAL_UPDATED` is set when it is published.
- docs/legal.md: engineering item 5 in part (queued and tried again for these seven; the sign-out notice), and item 7's "Export" in part (the record of what left can be exported; the rest of the account's data not yet).

## The contract and the seams

Committed with this design; nothing of it is reached by the image yet (the web app's build is byte for byte the same, and no package of the enclave imports `notices`).

- `internal/services/notices/notices.go`: the kinds, `Notice`, `Facts`, `Check`, `Queue`, `Deliverer`, `ErrInvalid`, `ErrSkip`, the partitions and the signing purpose, the bounds, `EndingDue`, `Key`, `EndingKey`, `IncidentKey`; its tests (E20, E24 and E32 in the seam's part, 100% of statements, in `scripts/coverage-thresholds.txt`).
- `internal/services/notices/noticestest`: `Fake` (a `Queue` in memory with `Queued`, `Drain` and `Fail`), `Recorder` (a `Deliverer` that keeps what it is given and fails on demand), `Sample`, and `Contract`, which the fake keeps now and the real queue keeps in pull request 1, alone and as two enclaves over one store. depguard's `no-fakes-in-production` names it.
- `pkg/attestation`: `NoticesStatus` and `MailStatus.Notices` (nil until pull request 7 fills it), with its JSON pinned (E29's shape).
- `web/src/lib/api.ts`: `Receipt` and its parts (`ReceiptKind`, `RuleRef`, `ReceiptFormula`, `ReceiptModel`, `ReceiptDelivered`, `ReceiptHeld`, `ReceiptRefused`, `ReceiptConsent`, `ReceiptRefusals`) and `readReceipt`, tested against the receipts service's golden documents (`internal/services/receipts/testdata/golden`, H13), which `web/tests/lib/receipt-documents.test.ts` reads where they are: one source for both sides (E19).
- `web/src/lib/csv.ts`: `CSV_COLUMNS`, `CSV_BOM`, `CSV_NEWLINE`, `csvCell`, `csvRow`, with their vectors and property test (E12 to E14 in the seam's part).

### What each pull request adds to the seams

- Pull request 1: `notices.New(Config{Entries, Keys, Deliverer, Now, Random, Log})` implementing `Queue`, `Run(ctx)` (the runner's ticker), `Drain(ctx)` (one pass, for tests), `Status() attestation.NoticesStatus`.
- Pull request 2: `(*accounts.Service).Deliver`, implementing `notices.Deliverer`; `accounts.Config.Notices notices.Queue` for the sign-out notice.
- Pull request 3: `connections.Config.Notices notices.Queue`.
- Pull request 4: `receipts.WithNotices(notices.Queue)`, an option of `NewChained`.

## Tests

E-prefixed. Each pull request's are in its row of "Implementation pull requests".

The activity screen (web, against a played server and the golden receipts):

- **E1.** It reads the walk from the head, then from each page's `next` on "Show older", never a position the server did not give; `done` ends it; a page that does not move on is refused (`other`) rather than asked for ever.
- **E2.** Every kind of receipt (the golden documents) renders its row and its panel; a receipt `readReceipt` cannot read shows its identifier, position and "This app can't show this receipt", never dropped.
- **E3.** Every verdict of a link and of a receipt has its words; `recovered` says "Linked late", `adopted` "From before", `expired` reads as kept; a `break` is said under its link.
- **E4.** Filters: by connection from `?connection=`, by kind; a filtered press reads at most five pages or 50 rows and says how far it looked; a break and an unreadable receipt pass every filter; a short page with more says so.
- **E5.** The head: its number and digest when `ok`, and Copy writes `<n>:<sha256>`; a head not `ok` offers nothing to note.
- **E6.** Loading, empty, `receipts_unreadable`, `busy` with Try again, no answer with Try again and the rows kept, `invalid_before` starting from the head.
- **E7.** Senders, subjects and topics as text in `bdi`; nothing of a receipt in the title, the address (but `?connection=`), storage or history (`expectNotKept`); no `dangerouslySetInnerHTML`.
- **E8.** "Show the search in words" asks the check route with the receipt's formula only on the press; a refusal shows the canonical JSON.
- **E9.** `expectAccessible()` in every state; focus stays on "Show older"; the live region says what loaded; rows expand and are pressed as the sample's.
- **E10.** Reachable while closing and under unagreed terms, in the limited shell, both downloads working there.
- **E11.** A connection's page links to its activity, and the activity of a connection no longer listed names its vendor's domain.

The export (web):

- **E12.** The CSV's header is `CSV_COLUMNS` in order, after the byte order mark; rows end in CRLF; every cell is quoted, its quotes doubled.
- **E13.** The formula guard's vectors: each trigger, full-width, after white space and invisible characters, after a tab or a line break, gets a quote; text, addresses and the numbers the export writes do not.
- **E14.** Controls, separators and reordering characters are U+FFFD; generated cells never start a formula and never hold a control.
- **E15.** One row per message made available or held, per refusal, per approval, per end, per count of refusals, per break, per receipt this app cannot read, from the golden receipts and a walk with every verdict.
- **E16.** Whole or nothing: a page that fails, a cancel, or a page that does not move on leave no file and say so; progress is announced.
- **E17.** The names `permaily-receipts-<date>.csv` and `.jsonl`, from the person's local day; neither names the account.
- **E18.** The JSON download holds exactly the bytes `exportReceipts()` returned, and `permaily-verify -receipts` passes on the file of a played export (a Go test over the web test's file, as `web/tests/formula` does).
- **E19.** `readReceipt` reads every golden receipt, gives a version's missing lists as empty, and is null for another version, kind or a receipt without its kind's part.

The queue (Go, pull request 1, over `store.Memory` and two enclaves' queues over one store):

- **E20.** `noticestest.Contract` on the real queue: delivered once; one key is one notice, queued at once from eight goroutines and through two enclaves, and after it was sent; invalid refused; failed tried again; skipped never again.
- **E21.** The first attempt in the enclave that queued; a failure left for the runner, which tries again after `RetryFirst`, doubling to `RetryMax`; the other enclave's runner skips a held lease and takes an expired one.
- **E22.** A crash after `Deliver` returned and before the mark: sent again after the lease (at least once). A crash after the mark and before the entry's deletion: not sent again, the entry deleted.
- **E23.** After `RetryFor`, dropped, marked, counted, and not queued again by the same key.
- **E24.** An entry moved to another key or partition, edited, or written by hand is refused, counted `refused` and never sent; `Check` refuses every notice that is not its kind's.
- **E25.** The address read when sending (an address changed after queuing gets it); a gone, closing or blocked account is skipped (accounts' half in pull request 2).
- **E26.** A thousand queued drain at `SendsPerSecond` and `InFlightMax` at most, in each enclave.
- **E27.** While the watch of ADR 0021 refuses, attempts fail and are tried again, and nothing is sent through another path (with `sesmail`'s watch played in the composition's test).
- **E28.** Logs carry the kind and the outcome alone: never an account, a key, an address or a word of SES's error (`TestTheLogHoldsNothingOfAnyone`'s check over the queue's lines).
- **E29.** `/status` `mail.notices` counts, every one at zero too; `permaily-verify` prints them and warns on `dropped` and `refused`, failing on none.

The events (Go):

- **E30.** `Decide`: `ChangeNew` and `ChangeReplace` queue `connection_approved` (`Replaced` for the second), `ChangeAddDevice` queues `device_added`, each keyed by its consent receipt, through either enclave; a denial, a refused commit and `consent_changed` queue nothing.
- **E31.** `Renew` queues `connection_renewed` with the new end; a renewal whose write loses queues nothing.
- **E32.** `EndingDue`'s table; the sweep queues one notice per connection and end over two enclaves and many passes; a renewal queues one for the new end; a connection of 14 days or fewer, an ended or revoked one, and a closing account's queue nothing.
- **E33.** A device ended for reuse, by a refresh token and by a code, queues `device_ended_reuse` once per device.
- **E34.** An incident's entry naming the account, from a walk, an export, an append or the audit, queues `receipts_incident` once a day per account; an entry that fails to commit queues nothing.
- **E35.** The operator and an admin signing every device out queue `devices_signed_out` once per count of sign-outs, with who.
- **E36.** A queue that cannot be written fails none of these and holds none of them for more than `EnqueueMax`; it is logged by its kind.

The words (Go and the mail package, pull request 2):

- **E37.** Every message in `sampleMails` in every variant, its golden text and its fixture current; `internal/domain/mail`'s checks pass on each (no link, nothing fetched, every value escaped, small).
- **E38.** No notice holds a link, a code, a token, an identifier (`acc_`, `con_`, `fam_`, `rcp_`, `mbx_`), a client name, an unverified domain, a query or a sentence, whatever a hostile vendor's document and domain say (`TestNoticesHoldNoCodeLinkOrToken` extended).
- **E39.** Settings' Notifications lists the new notices, with no switch.

End to end:

- **E40.** In dev mode (`TestMailEndToEnd`'s connections half): approving puts the approved notice in `/dev/outbox` once; a device added and a renewal theirs; with the clock moved to a week before the end, one ending notice over several sweeps; a reused refresh token its notice.
- **E41.** On staging, with the owner's address and a real client: the approved, device added and renewed notices arrive, look right in Gmail on the web and on a phone, and are each received once; the activity screen shows the session's receipts, both downloads open (the CSV in a spreadsheet, a subject `=1+1` shown as text; the JSON passing `permaily-verify -receipts` with the noted head).

## Implementation pull requests

Eight: this one first and alone, then 1 to 6 at the same time, then 7.

| Pull request | Owns | May edit outside, and only that | Must not touch | Depends on | Tests |
|---|---|---|---|---|---|
| 0. Design and seams (this one) | This record; `internal/services/notices/notices.go`, its test and `noticestest`; `NoticesStatus` in `pkg/attestation`; the receipt types and `readReceipt` in `web/src/lib/api.ts`; `web/src/lib/csv.ts`; their tests; docs/plan.md, docs/decisions/index.md, internal/AGENTS.md's row | `.golangci.yml` (the fake), `scripts/coverage-thresholds.txt` | Everything else | Nothing | E19, the seam's parts of E12 to E14, E20, E24, E29, E32 |
| 1. The queue | `internal/services/notices` beyond the seam: the entries, the marks, the lease, the runner, the counts | `scripts/coverage-thresholds.txt` | Every other package | 0 | E20 to E24, E26, E28 |
| 2. The words | In `internal/services/accounts`: `Deliver` (a new `connection_notices.go`), the seven messages as blocks, `sampleMails`, their goldens and fixtures; the sign-out's `Enqueue` in `operator.go` and `admins.go` | `packages/mail/fixtures` (written by the test); `web/src/pages/settings` (the Notifications words) and its test | `internal/services/connections`, `internal/services/notices`, `packages/mail/src` | 0 | E25 (accounts' half), E35, E37 to E39 |
| 3. Connections | In `internal/services/connections`: `Config.Notices`, the calls in `consent.go`, `person.go`, `token.go` and the sweep in `lifecycle.go`, with `noticestest.Fake` | | `internal/services/notices`, `internal/services/accounts`, `internal/httpapi` | 0 | E30 to E33, E36 |
| 4. Receipts | In `internal/services/receipts`: `WithNotices` and the call where an incident's entry commits | | Every other package | 0 | E34 |
| 5. The activity screen | `web/src/pages/activity` on the real API (the sample's files replaced), but `Downloads.tsx`; the limited shell's paths in `src/pages/accounts/standing.ts`; the link on `ConnectionDetailPage.tsx`; their tests; docs/web.md ("Activity") | `web/vitest.config.ts` (`src/pages/activity` gets coverage thresholds) | `web/src/lib/csv.ts`, `Downloads.tsx`, the server | 0 | E1 to E11 |
| 6. The export | `web/src/lib/csv.ts` beyond the seam (the rows from the walk), `exportReceipts()` by chunks, the downloads' component `web/src/pages/activity/Downloads.tsx`, their tests; the Go test of E18 under `web/tests/receipts`; docs/web.md ("The exports") | `web/src/pages/activity/ActivityPage.tsx`: the one line that renders `Downloads` beside the head, once 5 merged | The rest of `web/src/pages/activity`, the server | 0; 5 to merge | E12 to E18 |
| 7. The image | `cmd/permaily-enclave` (`notices.go`: the queue over the entries and the root secret's key, accounts as Deliverer, the runner's ticker, the queue given to accounts, connections and receipts, `/status`'s source), `cmd/permaily-verify`'s printing and warnings, `TestMailEndToEnd`'s notices (E40); docs/threat-model.md, docs/data.md, docs/operations.md (the queue, its counts, what to do when `dropped` rises), docs/legal.md and the privacy page; ADR 0017's "Notices by mail" | `internal/httpapi/status.go` (the source), `web/src/pages/settings` if 2 did not | The packages of 1 to 6 but by calling them | 1 to 6 | E27, E29, E40, E41 |

**Definition of done of each.** Its tests pass; `make check` passes, coverage thresholds included; internal/AGENTS.md, web/AGENTS.md and the docs it owns say what it built; an adversarial review of its code, each finding a test named for it. For 7 also the staging check (E41) with its result in docs/acceptance.md, and the release. Step 8's rest and step 9's notices are done once 7 is released and the owner has reviewed the words of every message and screen with the designer (docs/plan.md).

**What truly runs in parallel.** 1 needs only the seam and `store.Memory`. 2 builds `Deliver` against `noticestest.Sample` and the Recorder. 3 and 4 build against `noticestest.Fake`. 5 builds against the played server, the golden receipts and `readReceipt`; 6 against `csvCell`, `csvRow` and the same pages, its component tested alone and rendered by the page once 5 merged. 7 waits for all.

**Estimate.** About eight days of work, five elapsed with 1 to 6 at once: 1 three days, 2 two, 3 two, 4 one, 5 three, 6 two, 7 two with the staging check. The plan had half a week for step 8's rest and a week for step 9.

## Options considered

- **Activity in the enclave, a filtered route** (`connection=`, `kind=`): rejected for choice 1's reasons; it reads what the browser reads and adds a way to leave out a break.
- **A CSV route in the enclave, streamed**: rejected for choice 2's; a truncated stream reads as complete, and a whole chain does not fit a request's deadline.
- **A CSV of the export's pages instead of the walk's**: it would need the browser to open nothing (the export carries documents as base64), and then decode every receipt in the browser anyway; the walk gives the verdicts and the receipts read, which the CSV shows.
- **Prefixing a tab or a space instead of a quote**: some readers trim white space and then run the formula; dropping the character changes what the mail said.
- **Notices in line, as today**: rejected (choice 10); an ending needs the create-once entry anyway.
- **Notices in strict anchored records**: a notice is not an authority; a rollback costs one notice more or fewer, which ADR 0017 already accepts of devices.
- **A notice queued before its commit and checked when sent**: tells the truth of every commit even across a crash, at the cost of the Deliverer reading connections and receipts; rejected for the milliseconds it covers.
- **The ending's notice from a scheduler of ADR 0039**: ADR 0039's scheduler runs a connection's queries; the connections' sweep already reads every end hourly, and needs nothing of it.
- **Naming every vendor by domain**: rejected (choice 9).
- **A link to the connection in a notice**: rejected (choice 8).

## Open questions

- Whether the incident notice should wait for the later decision of ADR 0037 to accept a known incident: today an incident keeps `permaily-verify` failing for good, and the notice says so.
- The terms promise 30 days' notice by mail of a material change (docs/legal.md). That is a message to every account at once, started by the operator: not a notice of this kind, and not designed here.
- Whether Excel and LibreOffice, opening the CSV by a double click, honour the byte order mark and the quote as tested; E41 checks it by hand, and the guard's alternative (choice 15) is ready if one does not.
- Whether the receipts' screen needs a search of senders and subjects across pages, as the sample had: left out at launch (it would read the whole record in the browser to find anything old).

## What changes in other documents

When each pull request builds it: docs/web.md (Activity, the exports, Notifications; pull requests 5, 6, 2), web/AGENTS.md (the limited shell's third exception, the downloads; 5, 6), internal/AGENTS.md (`services/notices`; 1, 7), docs/threat-model.md, docs/data.md, docs/operations.md, docs/legal.md, docs/acceptance.md, the privacy page and ADR 0017's "Notices by mail" (7), docs/verification.md (`mail.notices`; 7). docs/plan.md's sub-lists for step 8's rest and step 9's notices are in this pull request.

## What it rests on

- The chain's walk and export as ADR 0037 built them, and its verdicts.
- `store.Entries`' `PutEntry` at version 0 refusing a second writer, and at a version refusing a stale one, through both enclaves (ADR 0017).
- SES delivering what it accepts, except to addresses on the suppression list; the watch of ADR 0021 on every message.
- The image's verified list of vendors (ADR 0030).
- A browser's `Blob` and `<a download>` for files the size of a person's record.
