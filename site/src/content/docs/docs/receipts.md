---
title: Receipts
description: The record of mail prepared for a connection's request, or of why the request was refused.
---

A receipt records mail prepared and made available for a connection's authenticated mail request, or why that request was refused. Receipts are stored and chained, and a person can export theirs and check the export themselves; the screen for viewing them is not built yet.

## What a receipt records

Which messages were prepared for which connection, which parts, when, for which query, which messages a rule held back, and whether a model decided. Only the person will see what was held back, once the Activity screen is built.

A refused request has its own receipt up to 30 a connection in a minute on each of the service's servers; past them, that minute's refusals there are counted together in one receipt, by kind of request and reason, naming no message. Until that receipt is stored, the count is only in that server's memory, normally for a few seconds after the minute ends, and longer while the database refuses it: if the server stops unexpectedly, every count it has not yet stored is lost. The refused requests themselves got no mail. A server that stops normally stores them first. When the account is removed, the counts not yet stored are dropped, never written after it.

## Where you meet them

Receipts are kept for the person, not handed to your app. Planned, not built yet: a receipt id on every message you read, as `message.receipt` in the SDK, and on every webhook.

## The ledger

Together, receipts form the ledger a person will see as Activity. Each account's receipts are chained: every receipt is named, by its hash, in a numbered link that also names the link before it, and each link is recorded once in storage that nobody, Permaily included, can rewrite or delete for ten years. So a receipt or a link deleted or changed in the database shows wherever the chain is read, and the service reads every chain once a day; what it found stays on record, and [the verifier](/docs/verifying-the-build/) fails while it holds anything. The Activity screen, which shows them and downloads the export, is not built yet.

## Checking your own

The chain's head is its newest link: its number and its hash. The export is every link and receipt as stored, one page a line, from the head down to the first link (`GET /api/v1/receipts/export` on the app, page by page, until the Activity screen downloads it for you). Note the head when you export, apart from the file, then check the file on your own computer:

```sh
permaily-verify -receipts receipts.jsonl -receipts-head 1234:<sha256>
```

It recomputes every hash: each link against the one above it, every receipt against its link, the first link against your account. A file alone proves only that it is consistent with itself, since whoever edits it can recompute every hash; the head you noted from the app is what proves it is the chain the service keeps. Links above the newest head you noted, and below a break, are reported as not checked against it.

It passes only a file that holds together and reports no break. It fails a file that was edited without recomputing, cut short or has pages missing, and a file that reports a receipt or a link missing, unreadable or not what its link names, or a link that names no link before it: it still lists every one, by its number, and which links your head checks. The file's word for what is broken is not covered by any hash, so the check cannot tell whether the storage broke it or someone edited the file to look that way; the service records every break it finds, and [the verifier](/docs/verifying-the-build/) fails against it while any is recorded. With `-json` it says apart whether the file is consistent, whether it is intact, and how much of it your head checks.

## How they're kept

Sender and subject are sensitive even without a body, so receipts are sealed so that only the service can read them. They are written before anything leaves, and a request whose receipt can't be written gets nothing. They are kept for as long as the account exists, even after the mail they describe is gone, and none is written after the account is removed.

## What a receipt can't do

A receipt does not prove that the app received all or any of the prepared bytes. It cannot recall information or show what the app did with it.
