# 0034 Chaining the receipts: how

Status: Proposed. Designed on 2026-11-17, on the owner's decision of that day to bring step 8's chaining of receipts forward (docs/plan.md): "chain an account's receipts with integrity records, as the other strict records are, so that deleting one shows". Not built. Amends ADR 0031 ("How steps 7 and 8 extend them", for step 8's chaining; "What it guarantees, and until when"); builds on ADR 0016 and 0026 (strict anchored records, `CreateNew`, commits locked in S3 Object Lock). Step 8 keeps the activity screen and the CSV export. Its pull requests are in "Implementation pull requests"; its tests are L1 to L25.
Date: 2026-11-17

## The owner's choices

Three, each with the design's recommendation; the rest is chosen in "Chosen without an owner's decision".

1. **What stays of a deleted account's chain.** Every link is a commit in the anchors bucket, locked until its lock ends: ten years in production, as every commit is. After the account is deleted, what stays there is one small object per link: the account's identifier, the link's number, a digest of a value no longer stored, and when. Nothing of what the receipts said. Recommended: accept it, as ADR 0030 accepted the same for connections. The other way is a shorter lock for links alone (13 months, say), after which an account that wrote no receipt for that long could have its newest receipts deleted unseen.
2. **Should the enclave look for deleted receipts on its own?** Recommended: yes, one pass a day over every account's chain under a lease, reported on `/status` as counts by kind, so `permaily-verify` fails the day after a deletion. It costs about $0.09 a year for every 1,000 stored receipts at today's prices ("Cost"). Without it, a deletion shows only when someone reads that part of the chain.
3. **Can the person check their chain now, or only with step 8's screen?** Recommended: now, through the API. A route returns the chain's head and its pages with what was found of each receipt, an export holds every link and receipt as stored, and `permaily-verify -receipts` checks an export offline. The screen and the CSV stay in step 8.

## Context

Receipts are sealed entries of `receipts#<account>`, written once before the answer (ADR 0031, `internal/services/receipts`). Whoever writes the table cannot read, change or make one. They can delete one, or the whole partition, and nothing shows. `permaily-verify` warns about it ("receipts of what left are sealed but not chained yet (step 8)"), and the threat model and docs/data.md state it as the limit.

ADR 0031 left step 8 a constraint: keys' order across two enclaves is not append order, so chaining needs a fenced cutover and a manifest of what existed before, never a hash of sorted pages while writes continue. Four things are being built alongside this design and bound it. Refusals of a connection are grouped by time window, so beyond N a minute one summary receipt counts the rest (the owner's decision 1 of 2026-11-17, a new kind). #152 removes an account's data with the account. Two enclaves may write at once. Receipts in production are few.

## Decision in short

- **A chain of numbered links, each a strict anchored record made once.** An account's receipts are named by links numbered from 1 with no gap. Link `n` is the strict anchored record `receipt-link-<account>-<n>`, made with `CreateNew` and never changed. Its one commit, locked in the anchors bucket, means link `n` can be neither removed nor made twice. Each link names its receipts by identifier and SHA-256 digest of the document as stored, and the link before it by its digest (`prev`). Link 1 names a genesis derived from the account. ("The chain")
- **Written after the receipt, before the answer.** A receipt is stored as today, then linked. `Write` returns only once a committed link names it, so nothing leaves unless its receipt is both stored and chained. Receipts of one account written at once in one enclave share a link (up to 64). Two enclaves compete for a number through `CreateNew`: the one refused reads the other's link and takes the next number. ("Appending")
- **Kind-agnostic, size-agnostic.** A link names documents and never reads what they record, so the refusal window's summary receipt and any later kind are chained the same way. The 256 KiB cap and a search's page cut are unchanged. ("Receipts of a refusal window", "Reading and checking")
- **Checked from the head down by digests.** One strict read finds the head. Every link below is checked against the next one's `prev`, and every receipt against its link. A deleted, replaced, moved or rolled-back receipt or link is therefore a verdict at its number, found by any reader. S3 is read again only at the head and below a break. ("Reading and checking")
- **What existed before is adopted.** An account's first links adopt, in key order, the receipts it had without a link from more than a minute before. A receipt without a link written later (an earlier release, or a request that failed after its entry was written) is shown as unlinked, never assumed away. ("Receipts written before the chain")
- **Deleted with the account.** Links' values are entries of the account's own partition, so #152's `DeletePartition` (run twice) removes them with the receipts. What stays is the commits, the owner's choice 1. ("Retention and deleting the account")
- **Seen on `/status` and by the person.** `receipts.chained` becomes true and `receipts.chain` is `strict`. `receipts.broken` counts by kind what the enclave found, and `permaily-verify` fails on any of it. The person gets the head, the pages and an export they can check offline (the owner's choices 2 and 3). ("What /status and permaily-verify show", "What a person can verify")
- **Cost**: about one S3 listing, one locked S3 write and one small DynamoDB write per link, about $0.011 per 1,000 links, and about 60 to 150 ms more per request before the answer (estimated; measured on staging, L22). ("Cost")

## The chain

**Links.** Link `n` of an account is the strict anchored record `receipts.LinkName(account, n)` = `receipt-link-<account>-<n>`, read and written through a strict `store.Anchored` of its own that reads and writes no other name. It is made only with `CreateNew` and never written again. Its document (`receipts.Link`, version 1) holds:

| Field | What |
|---|---|
| `n` | Its number, from 1 |
| `kind` | `receipts` (receipts written through the chain), or `adopted` (receipts written before it, "Receipts written before the chain") |
| `at` | When the enclave that committed it began its write, by that enclave's clock |
| `prev` | The digest of link `n-1`'s document, or for link 1 the genesis, `SHA-256("permaily receipts chain v1" ‖ 0x00 ‖ account)` |
| `break` | Instead of `prev`, when link `n-1` could not be read: `lost`, `forked` or `unreadable` |
| `before`, `more` | On adopted links only: the instant before which receipts are adopted, and whether another adopted link follows |
| `receipts` | 1 to 64 refs, each the receipt's identifier and the digest of its document |

A **digest** is the SHA-256 of a document's bytes as stored, before sealing, in lowercase hexadecimal. It is never taken of a document marshalled again, so a person can recompute it from an export. The link's document is checked whenever it is written and read (`Link.Check`). It is at most 16 KiB of JSON (`LinkBytesMax`; the largest is about 8.1 KiB) and is sealed under a key from the root secret (`permaily receipts chain v1`), bound to its record name.

**Where a link is kept.** `store.Anchored` keeps each version of a record under `<name>@<version>-<hash>`. A link's record table (`receipts.LinkTable`, a `store.RecordTable` over `store.Entries`) keeps that version in the account's own partition, `receipts#<account>`, under `lnk_<n in 16 hexadecimal digits>@<version>-<hash>` (`receipts.LinkKey`). This is apart from the receipts' `rcp_` keys, so `List` never meets a link, and `DeletePartition` removes links with the receipts. A link's plain name has no key. The table answers "not found" for it without reading, and refuses to store under it, so junk written there can neither stop a link from being made nor be read as one.

**Why it holds.** The commit of link `n` is locked: nobody can remove it, and a second commit at its one version is a fork that every reader refuses (ADR 0016, 0026). Numbers have no gaps, because an enclave makes link `n+1` only after it has read link `n` committed. Every link names the one before it by digest. So:

- A **deleted receipt** is a ref whose entry is missing.
- A **changed receipt** is a ref whose entry does not open for its key (moved, edited or made up, `ErrUnreadable`), or whose digest is not the ref's.
- A **receipt put back** is the same receipt, which changes nothing.
- A **deleted or replaced link** is a number below the head whose document is missing or whose digest is not what the next link's `prev` names. Its commit is still there.
- A **reordering** is a link whose document does not open for its number, or a `prev` that does not match.
- A **rollback**, an earlier copy of the partition put back, leaves every link and receipt written since missing. Their commits stay, so the head does not go back.
- A **truncation** of the tail would need commits removed from the bucket, which nobody can do while they are locked.

## Appending

`receipts.Service.Write` keeps its signature (`receipts.Writer`), so the connections service does not change:

1. **The receipt**, as today: built, checked against `MaxDocumentBytes`, sealed under `Purpose`, bound to its partition and key, stored with `PutEntry` at version 0 under its time-ordered `rcp_` key. Its ref is its identifier and the digest of the JSON it sealed.
2. **The link.** The ref goes to the account's appender in this enclave (`receipts.Chain.Link`). `Write` returns once a committed link names it, or with an error when nothing is known to name it. Its request then gives nothing (answered 503), as a receipt that could not be written does today. A receipt that cannot be linked is a receipt that was not written.

**The appender.** One per account in each enclave, with at most one link in flight. Refs that arrive while a link is being committed wait for the next, which takes all of them up to `LinkReceiptsMax` (64); more than four links' worth waiting is refused, failing those writes closed. For each group it:

1. Knows the head (`n` and the digest of link `n`'s document) from its own last link, or finds it ("Finding the head").
2. Builds link `n+1` naming the group, with `prev` the digest of link `n` and `at` now, and makes it with `CreateNew` under `store.WriteWithin(ReceiptWriteMax)`. No value of a link is then stored more than five seconds after its write began, which account removal's second pass relies on.
3. On success, answers every waiting `Write` with its position (`n+1`, its index) and keeps link `n+1` as the head.
4. On `store.ErrExists`, the appender reads link `n+1` strictly. If its document is the one this group built (the same digest), the commit is this group's own: a retried write whose first try landed is told `ErrExists`, and `CreateNew` keeps a value its commit names. That is success, as in 3. Otherwise another enclave made link `n+1` first. The appender takes it as the head and tries `n+2` with the same group, up to `LinkTries` (8) numbers and within the waiters' deadlines. The group's receipts are unchanged: only the link is built again. A receipt never has to be rewritten, and a lost race leaves nothing behind (`CreateNew` discards the staged value it lost with).
5. On any other error, fails the group's writes, forgets the head and finds it again next time. A link whose commit's outcome is unknown may name the group and still fail its writes. A receipt may then record more than left, never less, as ADR 0031 says.

**Two enclaves at once** never coordinate. They meet only at a number, where S3 takes one commit (ADR 0016) and the other enclave reads it and goes after it. Each link names receipts of one enclave's group. A receipt is in exactly one link, because a group is rebuilt on the next number only once the link at its last try is read and is not its own.

**Finding the head.** After a restart, or once it is forgotten, the head is the highest number with a commit. The appender reads a hint, the greatest `lnk_` key of the partition (`store.Scanner.ScanEntriesDown`, one item), and confirms it with a strict read. It then probes forward, `h+1, h+2, h+4, …`, until a number has no commit, and bisects between the last number with a commit and the first without. Every probe is a strict read, so junk objects where commits go are not commits (ADR 0016). The hint is never trusted: lowering it costs probes, raising it ends in the bisection. With a true hint it takes two strict reads; with none, at most about `2·log2(n)` (28 for 10,000 links, each one S3 listing). `Head` reads the head this way, strictly and now: the number above the head is always read and found empty.

**When the link before cannot be read.** When link `n` (another enclave's) is lost, forked or does not open, link `n+1` says `break` with the reason instead of `prev`, and appending goes on. The gap stays where link `n`'s commit is, and the walk shows it. Stopping would let whoever deletes one small entry stop every connection of the account. The strict `Anchored` reports the lost or forked record on `/status` as a regression of the fixed name `receipt link`, and `permaily-verify` fails on it as it does today.

**Memory.** The appenders and their heads are bounded (an account dropped finds its head again). `store.Anchored` remembers every name it reads or writes, so pull request 1 adds `Anchored.Forget(name)`, and the appender forgets link `n` once link `n+1` is known. One enclave's memory then holds one link a recent account.

## Receipts written before the chain

This is ADR 0031's fenced cutover and manifest, in a form that does not wait for every earlier writer to stop. That cannot be shown: the key policy may let an earlier release run.

- **Adoption.** When an appender finds an account's chain empty, it first adopts. `before` is now less `AdoptAfter` (one minute, longer than a receipt's write and than two enclaves' clocks differ). It lists the account's `rcp_` entries whose identifiers' time is earlier than `before`, in key order, opens each and takes its digest. It makes link 1 of kind `adopted` naming the first 64, with `before` and `more` set when more remain, and further adopted links until none remain. Only then does it make the link of its own group.
- **Continuing it.** Whoever finds the head adopted with `more` set continues it from that link's last identifier, before the same `before`. Two enclaves adopting at once are two makers of one number: one is made, and the other reads it and continues from it.
- **What adoption says.** An adopted link says the receipt existed when the chain began, with this digest, not that nothing was deleted before. That stays unknowable, and the person is told so. An entry that does not open is never adopted (it is counted `receipt_unreadable` by the audit).
- **Unlinked receipts.** A receipt without a link after `before` was written by an earlier release still running (as during a release's hand-off) or by a `Write` that failed after its entry. Its answer may have left. It is never adopted later: the audit and the export show it as `unlinked`, with its time.
- **Production.** There are few receipts. Each account adopts on its first receipt after the release, within that write's five seconds. An account whose adoption does not finish in them has that request answered 503, and the next request continues it.

## Reading and checking

`receipts.Chain.Walk(ctx, account, before, limit)` reads the receipts before a position, the newest first: links in descending number, and in each its receipts in descending index. It reads at most `limit` receipts and `WalkBytesMax` (8 MiB) of sealed entries a page. A page may end inside a link, and `Next` continues it, so nothing is skipped. It says what it found of each:

| Of | Verdict | When |
|---|---|---|
| A link | `ok` | Its document opens for its number, passes `Check`, and its digest is what the next link's `prev` names (the head: what its commit names) |
| | `prev` | It opens and passes, and the next link's `prev` names another digest |
| | `lost` | Its commit is there and its document is not, or is not what was committed |
| | `forked` | Two commits at its version |
| | `unreadable` | It does not open for its name, or is not a link |
| A receipt it names | `ok` | Its entry opens for its key and its document's digest is the ref's |
| | `missing` | No entry under its key |
| | `unreadable` | The entry does not open for its partition and key |
| | `changed` | It opens, and its digest is not the ref's |

The head is read strictly (one S3 listing, two with its probe). Every link below it is authenticated by the digest the link above names: the digest is SHA-256, and the link above was authenticated already. So S3 is read again only below a break or a link that is not `ok`, where a strict read of the next number picks the chain up again. A page therefore costs DynamoDB reads, apart from one or two S3 listings. Several stored versions of one number (a value another enclave staged and did not discard after a crash) are read together under `LinkKeyPrefix(n)`, and the one the chain names is the one whose digest matches. A walk fails only when the head cannot be read. A link or receipt it cannot read is a verdict, never an error and never left out.

**The 256 KiB cap and pagination.** Nothing changes for writing: a receipt is one document of at most 256 KiB, and a search's page still ends before the message that would pass it (ADR 0031, finding 10). A ref is the same size whatever the receipt's, so a link's size does not depend on its receipts. Reading is bounded by bytes, since 64 receipts at their largest are about 22 MB sealed.

## The audit

If the owner chooses it (choice 2): once a day, under a lease entry so that one enclave of two runs it, the receipts service walks every account that has receipts from its head to link 1. Accounts come from the accounts service's own list (`receipts.Audit` takes them as a function). For each account it lists the `rcp_` entries and the `lnk_` values (eventually consistent reads, read again strongly before anything is counted) and checks them as a walk does. It also counts every receipt no link names that is newer than the chain's `before` as `unlinked`. It holds one account's refs at a time (about 80 bytes a receipt), bounds what it reads in one pass, and goes on in the next. Logs and `/status` get kinds and counts only. What it finds is counted on `/status` (`receipts.broken`, `receipts.audited`), and the person sees it in the walk. No mail is sent: a notice to the person is step 9's. Without the audit, the same verdicts are reached by every walk and export, and counted the same way.

## What a person can verify

If the owner chooses it now (choice 3), with pull request 3; otherwise with step 8's screen.

- **In the app, through the API** (`GET /api/v1/receipts`, the person's session, their own account only, allowed while their terms are not agreed and while the account is closing, as the connections' pages are). The chain's head (`n`, its digest, when) and pages of the walk, each receipt with its position and verdict. Step 8's screen shows "receipt 1,234 was deleted from the storage" where the verdict says so, and "made available", never "received" (ADR 0031).
- **Offline.** `GET /api/v1/receipts/export` gives every link and every receipt as stored, in JSON with each document's exact bytes in base64, and the head. `permaily-verify -receipts <file>` recomputes every digest from the account's genesis to the head: `prev` link by link, every ref against its receipt, and the head against its link. With `-receipts-head <n>:<digest>`, a head the person noted earlier, it also checks that the export's link `n` has that digest, so the chain was only ever extended. It fails on any changed byte, missing receipt, reordered link or head that does not extend the noted one. It prints the breaks and unlinked receipts the export holds.
- **What each rests on.** The offline check needs no trust in the enclave or the storage for consistency: a chain that passes was not edited after the head the person noted. Completeness rests on the attested code: no answer left without its receipt linked (`Write`) is a property of the image `permaily-verify` checks by its measurements, not of the export. A new deployment, with a new root secret, starts a new chain that does not extend the noted head, and the person sees that.

## What /status and permaily-verify show

`attestation.ReceiptsStatus`, committed with this design:

- `chained: true` and `chain: "strict"` (`attestation.ReceiptsChainStrict`).
- `broken`: by every kind of `attestation.ReceiptsBrokenKinds`, also at zero (`receipt_missing`, `receipt_unreadable`, `receipt_changed`, `link_lost`, `link_forked`, `link_unreadable`, `link_prev`, `unlinked`), what this enclave found since it started by its walks, exports and audits. One enclave's observation, never an account, a number or a digest.
- `audited`: when it last finished a pass, with the audit.

`permaily-verify`, built from the release's commit, fails when `chained` is false or `chain` is not `strict`. That replaces today's warning. It also fails when any `broken` count is above zero ("the storage deleted or changed N receipts or links of the record of what left"), and it prints the counts and `audited`. A lost or forked link already fails it, as a regression of the record kind `receipt link`. Since `/status` names no account, someone verifying learns that something was deleted somewhere, and the person learns which receipts in their own pages.

## Retention and deleting the account

- **While the account lives**, receipts and links are kept for as long as the account (ADR 0019). No route deletes one. Should a person ever choose to keep receipts for less time (the sample's "Your data" shows such a choice; docs/legal.md, item 7), that would be a link of a new kind naming what was removed by the person's choice, never a deletion the chain cannot tell from an attack: a later decision.
- **When the account is removed** (#152, ADR 0031's choice 32), accounts deletes the partition `receipts#<account>`, receipts and links' values together, and again `RemoveAgainAfter` (ten minutes) later. Every link is written under `WriteWithin(ReceiptWriteMax)`, so a link racing the removal is stored within five seconds of its write's start or not at all, and the second pass removes it. No `RemoveRecord` is needed for links: an account's identifier is never used again, nothing reads a removed account's chain, and a value put back opens for no route.
- **What stays.** The links' commits in the anchors bucket stay until their lock ends: ten years in production, a day on staging images (the owner's choice 1). The table's point-in-time recovery keeps the deleted items for one day, as it does every deleted item (`deploy/tofu/mail.tf`).
- **Where docs/data.md and the privacy page say it.** docs/data.md gets a row for links. The privacy page's sentence on what stays of an account after deletion is checked against it in pull request 2, with docs/legal.md.

## Receipts of a refusal window

The summary receipt (the owner's decision 1 of 2026-11-17, designed in parallel) is a new `Kind` written through the same `Writer`, and the chain names it as it names any receipt. What this design asks of it, and of every later kind: **a receipt is a document written once.** A summary whose count changes is a new receipt each time it is written, or one receipt written when its window closes, never an entry updated, because a link names one digest. The summary also bounds the links its refusals cost, as it bounds the writes.

## Cost

At us-east-2's prices as of this writing: S3 PUT and LIST $0.005 per 1,000, GET $0.0004 per 1,000; DynamoDB on demand $0.625 per million write units and $0.125 per million read units.

| What | Requests | About |
|---|---|---|
| A link, made | `CreateNew`: one S3 listing (its number's history), one locked S3 write, one DynamoDB write of about 400 bytes; the plain name is not read | $0.011 per 1,000 links |
| A race lost to the other enclave | One strict read of its link: one listing, one S3 read, one DynamoDB read | $0.005 per 1,000 |
| Finding the head after a restart | Two strict reads with a true hint; at most `2·log2(n)` without | Once per account and enclave |
| A page of the walk | One or two S3 listings and the DynamoDB reads of its receipts and links | As reading the receipts costs |
| The audit, one pass (choice 2) | About two read units per receipt and one per link, eventually consistent, and finding each head | $0.09 a year per 1,000 receipts kept |
| Storage | One locked commit object of about 250 bytes per link for ten years, and one item of about 400 bytes per link while the account lives | S3: under $1 per million links over ten years. DynamoDB: about $0.13 a month per million links (at $0.25 a GB-month), a small part of what their receipts take |

With one receipt per link, the usual case for a person's mail, a receipt costs about $0.000011 more than today, about 8% of one ask of the model ($0.00013, ADR 0028). Grouping makes a burst cheaper. Latency: the link adds one S3 listing and one locked S3 write in series with a small DynamoDB write, from the enclave through the host's proxy, before the answer. That is estimated at 60 to 150 ms, plus a wait for the link in flight when several requests of one account arrive at once, all within `ReceiptWriteMax` (five seconds). The staging check measures p50 and p99 and the requests per receipt, and puts the numbers here (L22).

## What it guarantees, and what it does not

**It guarantees** that once the release runs, no answer of a connection leaves unless its receipt is stored and named by a committed link (`Write`). After that, whoever writes the table or the bucket cannot delete, replace, move, reorder, put back an earlier copy of, or hide a receipt or a link without a walk showing it at its number, the audit counting it and `permaily-verify` failing. Nor can they make the head go back: commits are locked, numbers have no gap, and digests tie each link to the one below. Two enclaves keep this however they interleave.

**It does not protect against:**

- **Deletion before chaining.** A receipt deleted before its account's chain began is not known. Adoption says what existed then, not what existed before.
- **An earlier release running.** A release from before this design, which the key policy still lets run, writes unchained receipts. They are shown `unlinked` if they survive, and can be deleted unseen. Retiring those releases' keys closes it (docs/operations.md).
- **Denial of service.** Whoever writes the table or the bucket can stop an account's receipts from being linked: junk or a fork where its next number goes, or the storage refusing writes. Every request of its connections is then answered 503 and nothing leaves. That fails closed, and is visible on `/status`.
- **The end of the lock.** An account that writes no receipt for the lock period (ten years) has a head whose commit can then be deleted, as every record's (ADR 0016, "The lock has an end").
- **A new deployment.** A new root secret makes old commits no longer this deployment's, so the chain starts over. The person's noted head shows it.
- **What a receipt proves.** It still records what was made available, not what the vendor received (ADR 0031).
- **Timing.** `at` is the writing enclave's clock. Receipts' order on the chain is the order their links were committed, which can differ from their identifiers' times by a few seconds across enclaves.
- **What the operator sees.** The operator sees how many links an account has and when, in the anchors bucket. While the account lives that adds nothing: the table's time-ordered keys show the same. After deletion it is what stays (the owner's choice 1).
- **Detection when nobody looks.** Without the audit, a deletion is detectable whenever the chain is read, not detected until then.
- **Trust in the code.** That every answer is chained rests on the attested code, as every guarantee of the enclave does.

## The contract and the seams

Committed with this design:

- `internal/services/receipts/chain.go` holds the chain's names and keys (`LinkName`, `ParseLinkName`, `LinkKey`, `LinkKeyPrefix`, `LinkPrefix`), `ChainPurpose` and `Genesis`, `Digest` and `DigestOf`, the link's document and its check (`Link`, `LinkVersion`, `LinkKind`, `Ref`, `BreakReason`, `Link.Check`, `LinkReceiptsMax`, `LinkBytesMax`), the bounds (`LinkTries`, `AdoptAfter`, `WalkBytesMax`), the walk's types (`Position`, `Head`, `LinkVerdict`, `ReceiptVerdict`, `WalkedLink`, `WalkedReceipt`, `Walked`), and the interface `Chain` (`Link`, `Head`, `Walk`), with its tests in `chain_test.go` (L1, L2).
- `internal/services/receipts/receipts.go`'s comments now say what chains a receipt. The `Receipt` document is unchanged (version 1): a receipt holds nothing of its chain.
- `pkg/attestation/status.go` gains `ReceiptsStatus.Chain`, `Broken` and `Audited`, `ReceiptsChainStrict` and `ReceiptsBrokenKinds`. They are empty until pull request 2 sets them, so `/status` does not change before then.

Pull request 1 writes, in `internal/services/receipts`: `LinkTable` (the `store.RecordTable` over `store.Entries` in the account's partition, the plain name never read); the appender behind `Chain.Link` (groups, `CreateNew`, the race and the break, finding the head); adoption; `Walk` and `Head`; `Service.Write` linking each receipt before it returns; the export's reader (`Service.Export`, links and receipts as stored); and `Audit`. In `internal/store` it adds only `Anchored.Forget`. `receipts.New` takes the links' strict `Anchored` beside the entries, the keys and the clock.

## Tests

L1 and L2 are committed with this design (`TestALinksNameIsItsAccountAndNumber`, `TestTheGenesisIsTheAccounts`, `TestALinkIsChecked`, `TestTheLargestLinkFits`). The store-level ones run on `store.Memory` with a strict `Anchored` over `store.MemoryCommits` (its `Fork` and `Take`, and the item-size-enforcing fake), and the real-bucket cases on the staging anchors bucket, as ADR 0026's store did.

- **L1.** Names, keys and the genesis: a link's name round-trips its account and number and nothing else parses as one; its versions are kept in the account's partition under keys ordered by number and apart from receipts'; its plain name has no key; the genesis is the design's and the account's own; a digest is lowercase hexadecimal and nothing else reads as one. Committed.
- **L2.** A link's document: `Check` refuses every malformed one (version, number, kind, time, both or neither of `prev` and `break`, link 1 with a break, adoption's fields where they do not belong, none or 65 receipts, a ref that is not a receipt, one named twice), and the largest fits `LinkBytesMax` and one item sealed. Committed.
- **L3.** `Write` stores the entry, then the link, and returns only once the link is committed, with its position. A played chain that fails: `Write` fails, the request gives nothing (with ADR 0030's V32), and the entry stays unlinked. The receipt's document is the same as before this design (ADR 0031's goldens unchanged).
- **L4.** Grouping: 200 concurrent writes of one account in one enclave make at most `⌈200/64⌉` plus one link per try lost. Every receipt is in exactly one link, numbers have no gap, every `prev` matches. A group past four links' worth fails closed.
- **L5.** Two enclaves (two services over one `Entries` and one `MemoryCommits`, two `Anchored`): 2,000 interleaved writes of one account, with a store that pauses each before its commit while the other acts. Every receipt in exactly one link, numbers without gap, no fork, each lost race rebuilt on the next number; with `-race -count=20`.
- **L6.** A crash between the entry and the link (`Memory.Interrupt`): `Write` fails, the entry is `unlinked` in the next audit, and the next write links normally. A crash after the commit and before the answer: `Write` fails, the receipt is linked once, and the head is found again with no second link naming it.
- **L7.** A commit whose outcome is unknown (S3's write lands, its answer is lost): the group's writes fail, the head is found again, and the group is in that one link, never in two. A commit whose retry is refused after its first try landed (`ErrExists` over this group's own link): the writes succeed at that number, and nothing is linked at the next.
- **L8.** Finding the head: chains of 0, 1, 2, 63, 64, 65 and 10,000 links, after a restart, with a true hint in two strict reads, with none in at most `2·⌈log2(n+1)⌉+1`. A hint lowered (newer values deleted) or raised (a junk `lnk_` key far above) still finds the true head. Junk objects at numbers above it, not commits, are ignored. A value under a link's plain name is never read and does not stop `CreateNew`.
- **L9.** A receipt deleted: `missing` at its position, through either enclave and after a restart. Another receipt's sealed value under its key: `unreadable`. A changed byte: `unreadable`. The same receipt put back: `ok`.
- **L10.** A link's value deleted: the walk says `lost` at its number, and the next link down is picked up by a strict read. The next append through the other enclave writes `break: lost` and goes on. The audit counts `link_lost`, and its receipts as `unlinked`.
- **L11.** A link forked (`MemoryCommits.Fork` at its version): `forked`, the regression `receipt link` with `fork` on `/status`, the next link `break: forked`, nothing written at the forked number.
- **L12.** A rollback: an earlier copy of the whole partition put back after 30 more links. The head is still the newest number, the 30 links and their receipts are `lost` and `missing`, and the chain is never shown whole and shorter.
- **L13.** A reordering: two receipts' entries swapped between their keys, `unreadable` both. Two links' values swapped between numbers, `unreadable` both (bound to their names). A link's value from another account's partition, `unreadable`.
- **L14.** Adoption: accounts with 0, 1, 64, 65 and 200 receipts from before the chain adopt them in key order, `before` and `more` as designed, then link their own. Two enclaves adopt at once, giving one adoption with no gap, the other continuing it. A receipt without a link written after `before` is `unlinked`. An entry that does not open is not adopted and is counted `receipt_unreadable`. An adoption cut by its write's deadline is continued by the next write.
- **L15.** Kind-agnostic: a receipt of every `Kind`, one of a kind this build does not know (the refusal window's summary stands in), and one of 256 KiB are linked and walked the same; the links' sizes do not depend on the receipts'.
- **L16.** Removing the account, with ADR 0030's `RemoveAll` and ADR 0031's H17: `DeletePartition` removes receipts and links' values. A link racing the removal is stored within `ReceiptWriteMax` of its write's start or not at all, and the second pass leaves nothing. Nothing of the chain reads after the removal. The commits stay, counted.
- **L17.** The walk's pages: bounded by `limit` and by `WalkBytesMax`, ending inside a link and continuing at `Next` with nothing skipped or repeated, through either enclave. Every link below the head authenticated by `prev` without S3 (S3 requests counted: one or two a page), and by a strict read only below a break.
- **L18.** The audit (choice 2): one pass under its lease across accounts finds each kind of L9 to L14 and counts it by kind. Two enclaves make one pass a day. A pass past its bound goes on in the next. Logs and `/status` get kinds and counts only.
- **L19.** `/status` and `permaily-verify`: `chained` true, `chain` `strict`, every kind of `broken` present at zero. `permaily-verify` fails when `chained` is false, `chain` is not `strict`, or any count is above zero, and prints the counts and `audited`. The warning of step 5 is gone.
- **L20.** Logs: no account, receipt identifier, link number, digest or count in any line (ADR 0031's H18 extended to the appender, the walk and the audit).
- **L21.** The composition: the links' strict `Anchored` reads and writes only `receipt-link-` names and no other `Anchored` reads them (`checkRecordsApart`). `TestMailEndToEnd` with a played vendor's search, read, attachment, refusal, consent and end: each receipt linked before its answer, the walk whole, the head extended by each.
- **L22.** The staging check: a connection's requests through both enclaves, p50 and p99 added per request, S3 and DynamoDB requests per receipt against "Cost", the head after a restart; a receipt and a link deleted by hand in the staging table, shown by the walk and the audit, and `permaily-verify` failing. The numbers go into this record.
- **L23.** The person's route (choice 3): the head and the walk's pages with their verdicts, for the session's own account only; another account's never; allowed under unagreed terms and while closing; a page bounded as L17.
- **L24.** The export and the offline check: every link and receipt as stored, streamed within a bound. `permaily-verify -receipts` passes an untouched export from genesis to head, and fails on one changed byte of any document, a receipt removed, two links swapped, a `prev` changed, or a noted head (`-receipts-head`) that the export does not extend.
- **L25.** Between two exports one receipt is deleted from the table: the second export's walk names it `missing`, the offline check of the second prints it, and the second extends the first's head, since deleting adds no link.

## Implementation pull requests

Four, this one first and alone; then 1; then 2 and 3 at the same time, both after 1; the release after 2 (and 3, if the owner chooses it now).

| Pull request | Owns | May edit outside, and only that | Must not touch | Depends on |
|---|---|---|---|---|
| 0. Design and seams (this one) | This record; `internal/services/receipts/chain.go` and `chain_test.go`; `ReceiptsStatus`'s new fields in `pkg/attestation/status.go`; the comments of `receipts.go`; docs/plan.md, docs/decisions/index.md, ADR 0031's status line, internal/AGENTS.md's row | | Everything else | Nothing |
| 1. The chain | `internal/services/receipts` beyond the seams: `LinkTable`, the appender, finding the head, adoption, `Walk`, `Head`, `Export`, `Audit`, `Write` linking, their tests (L3 to L18, L20, L25's store half) | `internal/store`: `Anchored.Forget` and its test; `scripts/coverage-thresholds.txt` | `internal/services/connections`, `internal/httpapi`, `cmd`, `pkg` | 0 |
| 2. Composition and verification | `cmd/permaily-enclave` (the links' strict `Anchored` over the anchors bucket, `receipts.New` with it, the audit's ticker and lease, `/status`'s fields, the regression name `receipt link`, `checkRecordsApart`), `cmd/permaily-verify`'s checks and printing, the staging check (L19, L21, L22) | docs/threat-model.md, docs/verification.md (the example of `/status`), docs/data.md (a row for links), docs/operations.md, docs/acceptance.md, docs/legal.md and the privacy page if what stays after deletion needs saying; internal/AGENTS.md's rules | `internal/services/receipts` except by calling it | 1 |
| 3. The person's view (choice 3) | `GET /api/v1/receipts` and `/api/v1/receipts/export` in `internal/httpapi`, their contract in `internal/contracts` and `web/src/lib/api.ts`, `permaily-verify -receipts` and `-receipts-head`, docs/web.md (L23 to L25) | `cmd/permaily-enclave` to mount the routes | `internal/services/receipts` except by calling it; the web app's screens (step 8) | 1 |

Estimate: about a week and a half: 1 is four days, 2 and 3 two each, at the same time. Step 8 keeps the activity screen and the CSV export, about half a week.

## Options considered

- **Periodic commitments per account** (a strict record anchoring, every minute or so, a digest over the receipts written since): fewer commits. But receipts written since the last commitment can be deleted unseen, and with a restart nothing remembers them. Closing that means waiting for the commitment before the answer, which costs what a link costs.
- **One strict record per account, appended at every receipt** (the head as a record changed at each version): the same commits, but a strict record's history is bounded (10,000 versions, 500 objects read at a time), so it needs epochs and a pointer to the current one. A link made once per number needs neither, and finding the head is a search over numbers.
- **Each receipt its own strict record**: no contention, but a receipt is up to 342 KiB sealed in `pk=system`, every read a strict read, and nothing would say how many there should be. Numbers and `prev` do.
- **`prev` inside the receipt's document** (a chain of receipts, with no links): a receipt's document would depend on the one before, so a race between two enclaves would rewrite a receipt already stored or leave orphans, and a receipt written before the chain could not be adopted as it is. Links keep receipts written once and unchanged.
- **Links' values in `pk=system`**, with the other records: deleting the account would then need a `RemoveRecord`, and another commit, for every link. In the account's partition, `DeletePartition` removes them.
- **Stopping at a lost link**: whoever deletes one small entry would stop every connection of the account. A break recorded and appending going on keeps the evidence and the service.
- **Waiting for every earlier writer to stop before adopting** (ADR 0031's words): it cannot be shown while the key policy lets an earlier release run, so receipts after the cutover without a link are shown `unlinked` instead.
- **Signing the head** with the attested TLS key, so a person could show it to a third party: not needed for the person's own check (the attested connection and the export suffice) and a new signing use of that key. Left for a later decision.
- **Rekor or another public log for heads**: Rekor cannot answer "the newest entry of this kind" (ADR 0016), and it would publish an account's activity.

## Chosen without an owner's decision

1. One strict anchored record per link, made with `CreateNew`, numbered without gap, with `prev` as a SHA-256 of the document as stored and a genesis per account.
2. Links in the account's partition through a `RecordTable` of their own, the plain name never read.
3. Grouping in each enclave, 64 receipts a link, four links' worth waiting at most, eight numbers tried a group.
4. The head found by a hint, probing and bisection, never trusting the hint; `Anchored.Forget` for memory.
5. A break recorded and appending going on when the link before cannot be read.
6. Adoption at the chain's start with a one-minute margin; receipts without a link after it shown `unlinked`, never adopted later.
7. `permaily-verify` fails, not warns, on any broken count and when the chain is not `strict`.
8. No mail to the person about a break until step 9's notices.
9. The receipt's document unchanged; a receipt is a document written once, whatever its kind, the refusal window's summary included.

## What changes in other documents

- **ADR 0031.** Its status says it is amended by this record. "How steps 7 and 8 extend them" is replaced, for step 8's chaining, by this record. "What it guarantees, and until when" holds until this release, after which deletion shows. Its other sections stand.
- **docs/plan.md.** Step 8's chaining is brought forward to this record, with its pull requests (this one).
- **When built** (pull request 2): docs/threat-model.md ("The record of what left", "Whoever writes the table"), docs/verification.md (the warning becomes failures, the example of `/status`), docs/data.md (a row for links, the receipts' row), docs/operations.md (the entries; retiring earlier releases' keys), docs/acceptance.md, and docs/legal.md with the privacy page for what stays after deletion.

## What it rests on

- **Strict anchored records** (ADR 0016, 0026) and their stated limits: S3 Object Lock in compliance mode, the lock's end, a deployment starting over.
- **SHA-256** for digests, and AES-GCM under keys from the root secret for sealing, as everything sealed.
- **Numbers without gap**: an enclave makes link `n+1` only after reading link `n` committed, a property of the attested code.
- **The measurements of the staging check** (L22) for the latency and cost above, which are estimates until then.
