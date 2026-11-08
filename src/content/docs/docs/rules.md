---
title: Rules
description: How a rule decides which emails match, and why rules read mail while AI only writes them.
---

A rule is a named definition of which emails match, such as "Flights and hotels". Rules are deterministic, so they can be previewed and audited.

## What a rule can match

- Authenticated sender domain, verified by DKIM. A spoofed `From:` header doesn't qualify.
- Recipient, subject terms, labels and categories, and date.
- Attachment presence and file type.
- List headers, such as `List-Id`.
- Structured markup. Some booking emails embed schema.org types such as `FlightReservation`.

Where the provider supports it, a rule compiles to a provider query, so non-matching mail is never fetched. The result is then checked again inside the enclave.

## AI writes rules, and rules read mail

A person types "flights and hotel bookings". A model turns that sentence into a rule, seeing only the sentence and the public pack catalogue. It never sees the mailbox. The preview then runs inside the enclave, and the person judges the result on their own mail.

This gives people natural-language setup without sending their email to an LLM. The cost is real: fuzzy categories are harder. "Anything about my kids' school" can't be expressed well as a rule.

## Adding to a pack

An access can start from a [pack](/docs/packs/) and add to it. This one takes `travel.bookings` and also matches one more sender.

```yaml
messages:
  pack: travel.bookings@3
  add:
    from_domain: [norwegian.com]
```

## When a rule goes wrong

[Never share](/docs/concepts/#never-share) overrides every rule, so a sloppy one can't reach password resets. Beyond that:

| Problem | What catches it |
| --- | --- |
| A rule matches too much | A preview before saving, and alerts on sudden match volume |
| A spoofed sender tries to enter a slice | Rules match the authenticated sender, not the `From:` header |
