---
title: Rules
description: The filters a person writes, which run on the whole mailbox before any query, and what that means for your app.
---

A rule is a filter the person writes, such as "Sign-in and security" or "Home address". Rules run first, on the whole mailbox, before any connection's [query](/docs/queries/). Your app never sees them.

## What a rule does

Each rule does one of two things, chosen per rule:

- **Hold back the email.** No connection the rule applies to gets it.
- **Remove details.** Only the matching words are taken out, such as an address or a child's name, and the rest of the email goes through. A finance app still gets the order confirmation, without the delivery address.

A rule applies to every connection, including ones approved later, or only to the connections the person picks. "Car, held back from Claude" is a rule for one connection.

## What a rule can match

- Authenticated sender domain, verified by DKIM. A spoofed `From:` header doesn't qualify.
- Recipient, subject terms, labels and categories, and date.
- Attachment presence and file type.
- List headers, such as `List-Id`.
- The kind of mail, such as a password reset or a one-time code.
- A topic in the person's words, such as "my car" or "my children".
- Words or phrases, such as a street address or a name.

Field conditions are exact, so they can be previewed and audited.

## Rules written in words

A person can describe a rule in a sentence, and a model turns it into conditions. That model sees only the sentence, never the mailbox.

Some rules can't become conditions at all, such as "my children's names" or "anything about my car". For those, a small model inside the enclave checks each email against the topic and finds the words a rule removes. It runs inside the measured image, so no mail leaves the verified boundary and no third party reads it.

A model can miss, and a missed match is a leak. So the person previews every rule on their own mail before saving it.

## Suggested on every account

Permaily suggests one rule on every new account, called **Sign-in and security**. It holds back, from every connection:

- Password resets, login links and one-time codes
- Security alerts

The reason is account takeover. Any connection that can read a password reset can take over the account it belongs to. A broad query or a compromised connection shouldn't be able to reach those messages. This rule uses exact conditions, not a topic.

The person can add it or skip it when they connect a mailbox, and later change it or delete it like any rule.

## What your app sees

Nothing about rules. An email a rule held back looks the same as one your query didn't match, and nothing tells your app that a detail was removed. No rule name or count leaves Permaily.

So treat results as what the person chose to share. A missing email isn't proof that it doesn't exist.
