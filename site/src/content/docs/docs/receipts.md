---
title: Receipts
description: The record written each time mail is released to a connection.
---

A receipt is a record of one release of mail to one connection. A person can open any receipt to see exactly what left.

## What a receipt records

Who received which message, which parts, when, for which query, and which details a rule removed. Only the person sees that last part.

## Where you meet them

Every message you read carries its receipt id, as `message.receipt` in the SDK, and so does every webhook.

## The ledger

Together, receipts form the ledger a person sees as Activity. Receipts are hash chained, so a missing one shows. Companies can [export them](/pricing/) for an audit, to show what each vendor received, when, and for which query.

## How they're kept

Sender and subject are sensitive even without a body, so receipts are stored under the person's own key. The person decides how long they're kept, with a default limit.

## What a receipt can't do

A receipt shows what left. It can't recall it, and it can't show what a connection did next. That's covered by the connection's own policies.
