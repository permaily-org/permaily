---
title: Receipts
description: The record of mail prepared for a connection's request, or of why the request was refused.
---

A receipt records mail prepared and made available for a connection's authenticated mail request, or why that request was refused. Receipts are stored now; a screen for viewing them and an export are not built yet.

## What a receipt records

Which messages were prepared for which connection, which parts, when, for which query, which messages a rule held back, and whether a model decided. Only the person will see what was held back, once the Activity screen is built.

## Where you meet them

Receipts are kept for the person, not handed to your app. Planned, not built yet: a receipt id on every message you read, as `message.receipt` in the SDK, and on every webhook.

## The ledger

Together, receipts form the ledger a person will see as Activity. Planned, not built yet: the Activity screen, chaining receipts by hash so that a missing one shows, and exporting them for an audit. Until receipts are chained, someone who can write to the database could delete one without it showing.

## How they're kept

Sender and subject are sensitive even without a body, so receipts are sealed so that only the service can read them. They are written before anything leaves, and a request whose receipt can't be written gets nothing. They are kept for as long as the account exists, even after the mail they describe is gone.

## What a receipt can't do

A receipt does not prove that the app received all or any of the prepared bytes. It cannot recall information or show what the app did with it.
