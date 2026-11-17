---
title: Rules
description: The filters that constrain what a connection may receive, and what that means for your app.
---

Rules constrain what a connection may receive; they do not hide mail from its owner. Today the built-in rule is evaluated when a connection or its consent preview considers candidate mail. Your app never sees the rules, nor the mail they hold back from its [query](/docs/queries/).

Today every account has one rule, [Sign-in and security](#on-every-account), on from the start. Rules a person writes, everything this page says about choosing, removing details and rules written in words, are planned, not built yet.

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

Some rules can't become conditions at all, such as "my children's names" or "anything about my car". For those, a model decides whether a message, or each of its sentences, is about the topic, and the service drops what is. That model runs at OpenAI, outside the enclave: only the field the rule names (the subject or the body text) leaves, only when exact conditions don't settle it, and only for people who turned such a rule on ([ADR 0020](https://github.com/permaily-org/permaily/blob/main/docs/decisions/0020-openai-models-for-meaning.md)). Planned, not built yet.

A model can miss, and a missed match is a leak. So the person previews every rule on their own mail before saving it.

## On every account

Every account has one rule from the start, called **Sign-in and security**. It holds back, from every connection:

- Sign-in, login and verification codes, and one-time passwords
- Password resets and account recovery

The reason is account takeover. Any connection that can read a password reset can take over the account it belongs to. A broad query or a compromised connection shouldn't be able to reach those messages.

Exact phrases, in English and Spanish, in the subject or the text hold matching messages. For every other message a connection would read, a model at OpenAI decides whether it's about a sign-in code or a password reset, and the message is held unless the answer is clearly no. If the model is disabled, its key is missing or refused, or a provider, spending or account limit closes the model gate, the affected connections receive no mail, including mail previously cleared. If a particular message needs a new model answer and no reliable answer is available, that message is held. Previously cleared messages can still be delivered during a temporary request failure while the gate remains open. Turning the rule off, and changing it, come with the rules a person writes.

## What your app sees

Nothing about rules. An email a rule held back looks the same as one your query didn't match, and nothing tells your app that a detail was removed. No rule name or count leaves Permaily.

So treat results as what the person chose to share. A missing email isn't proof that it doesn't exist.
