---
title: Receipts
description: The record written each time mail is released to a connection.
---

A receipt is a record of one release of mail to one connection. A person can open any receipt to see exactly what left.

## What a receipt records

Who received which message, which parts, when, for which query, which messages a rule held back, and whether a model decided. Only the person sees what was held back.

## Where you meet them

Receipts are kept for the person, not handed to your app. Planned, not built yet: a receipt id on every message you read, as `message.receipt` in the SDK, and on every webhook.

## The ledger

Together, receipts form the ledger a person will see as Activity. Planned, not built yet: the Activity screen, chaining receipts by hash so that a missing one shows, and exporting them for an audit. Until receipts are chained, someone who can write to the database could delete one without it showing.

## How they're kept

Sender and subject are sensitive even without a body, so receipts are sealed so that only the service can read them. They are written before anything leaves, and a request whose receipt can't be written gets nothing. They are kept for as long as the account exists, even after the mail they describe is gone.

## What a receipt can't do

A receipt shows what left. It can't recall it, and it can't show what a connection did next. That's covered by the connection's own policies.
