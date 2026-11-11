# 0021 Watch who else gets a copy of the mail the enclave sends

Status: Accepted.
Date: 2026-11-11

## Context

Accounts sign in by mail: one message with a six-digit code and a link, sent through Amazon SES (ADR 0017). The message leaves the enclave over TLS, so the parent instance does not see it, and AWS carries it, which is trusted already.

Found while building it: whoever administers the AWS account can make SES keep a copy of everything sent. A configuration set can be tied to an archive (`PutConfigurationSetArchivingOptions`), and an identity can be given a default configuration set (`PutEmailIdentityConfigurationSetAttributes`) that applies to every message whose sender names none. Both are changes in the account, away from the enclave. With the copy of a sign-in message the operator enters the account, which is what the rest of the design keeps them from doing through the storage.

Nothing in AWS forbids an administrator from doing it, unlike the KMS keys, whose policy nobody can change (ADR 0013).

## Options

1. **Write it down as a risk.** Sign-in by mail then rests on the operator not doing it, with nothing to show whether they did; "passkey only" is the way out.
2. **The enclave reads the settings and tells.** It asks SES which configuration set applies to what it sends, sends only while none does, and puts every state it saw on `/status` for the verifier, as it already does with the policy of its KMS key.
3. **Send without SES.** Deliver straight to each recipient's mail server from the enclave. Nobody in the account could copy it. EC2 blocks outgoing port 25 until AWS agrees to open it, and mail from a new address at a cloud provider is refused or filed as spam by the large providers: sign-in messages that do not arrive are worse than the risk.

## Decision

2, chosen by the maintainer on 2026-11-11 over leaving it as a written risk.

**What is read.** `GetEmailIdentity` for the domain the mail is from, and for the address itself: an address that was verified on its own has settings of its own, which win over the domain's. Clean means the domain is there, verified, with no configuration set (unverified, SES would send under a parent domain's identity, which is not read), and the address is not there, or is there with none. The name of a configuration set is not published or logged, only that there is one. The enclave names no configuration set when it sends, so with those two clean none applies.

**When.** Once a minute; before a message, unless a reading was made in the last five seconds; and after each message. Never more than about one reading a second, whatever the number of messages.

**What it does with it.** While the newest reading is not clean, or cannot be had, no message is sent: a sign-in by mail fails and the person asks again. Every state seen since the enclave started is kept, with when it was first and last seen, and served on `/status` under `mail`; `permaily-verify` fails when any of them is not clean or when the newest reading is not recent.

**The call is made by hand** (`internal/sesmail`): two operations of the SES API, signed with the SDK's signer, which the image already has. The SES client of the SDK would add every other operation to the image for nothing.

## What it rests on, and what is left

- It is noticed, not prevented. A configuration set put in place and removed between two readings, exactly while a message leaves, is not seen. The readings around each message leave about a second, and up to six when many messages leave at once (the reading a message is sent on may be five seconds old, and readings start a second apart); a watch once a minute alone would leave a whole minute.
- The history is in memory: a restart, which the operator can cause, starts it again. The start time is printed by the verifier.
- Other settings of the identity are not read: notification topics, which can carry a message's headers (so the subject never holds the code or the link), and the forwarding of bounces to the sender's address.
- Only this way of copying is read. If AWS adds another (as it added archives to configuration sets), it is not covered until someone reads about it and the watch learns it.
- When SES does not answer the readings, nobody signs in by mail. That is on purpose.
- The role the enclave uses needs `ses:GetEmailIdentity` on both identities. Taking it away stops sign-in by mail and shows in the verifier as readings that fail.
- AWS reads the message whatever is done here, and so does the person's mail provider.
