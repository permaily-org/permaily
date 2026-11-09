---
title: Messages
description: Reading mail through the SDK, the REST API and webhooks, with the approved parts only.
---

What you read is one view of the inbox, limited to the emails, parts and time the person approved. It's the same view however you read it.

## Read messages

Search and read inside the slice with the SDK or the REST API.

```ts
for await (const message of permaily.messages.list({ access })) {
  console.log(message.subject, message.receipt);
}
```

```http
GET /v1/messages?access=acc_9f2c
```

## What comes back

You only ever get the parts in the access: headers only, body text, attachments, or extracted fields only. An access asks for headers and body text as `headers` and `body_text`.

Every message also carries:

- Its [receipt](/docs/receipts/) id.
- A marking as [untrusted content](/docs/untrusted-content/), with the sender's authentication results.

## New mail as it arrives

Webhooks send new matching mail as it arrives, signed, with its receipt id. The event is `message.released`.

## Acting on mail

Reading is the default. Label, archive and draft have to be granted in the access, and send and delete need the person's approval each time. A mailbox connected by [forwarding](/docs/concepts/#connecting-a-mailbox) can't take actions at all.
