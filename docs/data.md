# Data

What Permaily stores, where, how it is protected, for how long, and who processes it. This is the basis for the privacy policy, the DPA and the subprocessor list.

## F0 (releases up to v0.1.1)

No user data. The enclave keeps only its TLS key and ACME account key in memory; both disappear when it stops. The parent instance logs connection metadata (timestamps, destination host of egress) but never content.

## F1 (deployed since v0.2.0, 2026-11-10)

One test mailbox, compiled into the image (`deploy/enclave.json`), read with a bearer token whose SHA-256 is in the image.

| Data | Where | Protection | Retention | Deletion |
|---|---|---|---|---|
| Raw message (with attachments) | S3 `permaily-mail-<account>`, key `raw/<mailbox>/<message id>`, tagged `retention-days=<n>` | AES-256-GCM with the mailbox data key and key commitment (AAD: purpose, mailbox, message id); SSE-S3 underneath | 7, 30, 90 (default), 180 or 365 days, set per mailbox; applies to mail received after the change | Lifecycle rule for its tag (period + 1 day), or `DELETE /v1/mailbox/messages/{id}` |
| Message metadata (envelope, From/To/Cc, subject, date, List-Id, snippet, attachment names and sizes, DKIM results) | DynamoDB `permaily-mail`, `pk=mailbox#<id>`, `sk=msg#<message id>` | Sealed like the raw message | Same as the raw message (DynamoDB TTL on `expires_at`, also filtered on read) | Same |
| Mailbox state (Gmail forwarding confirmation codes), with a count of its writes so that two enclaves do not undo each other | DynamoDB, `sk=mailbox` | Sealed | Mailbox lifetime (last 10 codes) | With the mailbox |
| TLS identity (certificate key, ACME account key, last certificate chain, and while an enclave asks the CA, which one and until when) | DynamoDB, `pk=system`, `sk=record#tls-<CA host>@<version>-<hash>`, one item per version; its commits (name, version, hash of the item) in S3 `permaily-anchors-<account>`, locked so that an earlier copy cannot be put back (ADR 0016) | Sealed with its own data key, wrapped under the root secret | Until replaced | Deleting it makes the next enclave create a new key |
| The challenge of an open ACME order (the certificate and its private key that answer the CA's TLS-ALPN-01 check), one per host | DynamoDB, `pk=acme-challenge#tls-<CA host>`, `sk=<host>` | Sealed under the TLS identity's data key, bound to the identity and the host, with its end sealed in it (ADR 0024) | The order, at most eleven minutes and more after failed tries | Deleted when the order ends; expires with it |
| Mailbox data key | DynamoDB, `sk=mailbox`, wrapped | Wrapped under the root secret (AES-256-GCM, bound to the mailbox) | Mailbox lifetime | Destroying it makes every message unreadable (crypto-shredding) |
| Root secret, one record per enclave image | DynamoDB, `pk=system`, `sk=record#root-<PCR0>` | Sealed with a data key from that image's KMS key, which only that image can use and whose policy nobody can change; carries the attestation document of the enclave that wrote it (ADR 0013) | Until the release's key is deleted | Deleting every record, or the key of the running image, makes everything unreadable |

Plaintext outside the enclave: ids, sizes, timestamps, retention period. Message ids are time-ordered.

## F2+ (planned)

| Data | Where | Protection | Retention | Deletion |
|---|---|---|---|---|
| Account: contact address, name, language, state, ways to sign in (ADR 0017). Built; on in production since v0.10.0 (2026-11-11) | DynamoDB, `pk=system`, `sk=record#account-<id>@<version>-<hash>`, as text; the pointer from the address at `pk=login#email#<address>`; its commits (the hash of the name, a version, the hash of the item) in the anchors bucket, which keeps them ten years | Readable by the operator and signed by the enclave, so it cannot be changed by hand; an earlier copy is refused (ADR 0016) | Account lifetime | Seven days after the account is deleted |
| Whether the operator blocked the account and when (`blocked_at`), who asked to delete a closing account (`closing_by`: the person or the operator), and, only while a change of address is under way, the address it is changing to (`moving_to`). Built, not released yet | In the account's record | Readable and signed, an earlier copy refused: a block cannot be undone by writing to the table | Until unblocked, kept or changed; account lifetime | With the account |
| The devices that signed in to an account, to tell its person of a sign-in from a new one: for each, a hash of the value of the browser's device cookie, made with the account's identifier, and when it last signed in. Not the cookie's value, and nothing of the browser. Built, not released yet | DynamoDB, `pk=account#<id>`, `sk=device#<hash>` | Signed. Whoever writes to the table can cause one notice more or one less, and nothing else | A year from the device's last sign-in; fifty an account at most (DynamoDB TTL) | When they end, when a newer one takes their place, or with the account |
| The device cookie (`__Host-permaily-device`): 32 random bytes | The person's browser | `HttpOnly`, `Secure`, `SameSite=Lax`. It stands for nothing and signs nobody in | A year, set again at each sign-in | By the browser |
| The list of accounts and the list of accounts that are closing: one entry each, with the account's identifier and a time | DynamoDB, `pk=accounts` and `pk=closing` | Signed; they prove nothing by themselves, the record does | With the account | With the account |
| Sessions (device as the browser describes itself, times of creation and last use, and since ADR 0022 when its person last proved who they are, `confirmed_at`). Built; on in production since v0.10.0 (2026-11-11) | DynamoDB, `pk=account#<id>`, `sk=session#<id>` | Signed; the secret only by its hash | Seven days without use, thirty at most (DynamoDB TTL) | When closed, or when they end |
| That an account is an admin: which super admin made it one, and when (`admin`). The super admin is not stored: it is the account with the address compiled into the image. Built, not released yet (ADR 0022) | In the account's record; a pointer to find it at `pk=admins`, `sk=<account id>` | Readable and signed, an earlier copy refused; the pointer proves nothing | Until the super admin removes it, or account lifetime | When removed, or with the account |
| The admin log: when, which admin, what kind of action, and the identifier of the account or invitation it was about. No address: it is read from the account's record when the log is read. Built, not released yet (ADR 0022) | DynamoDB, `pk=admin-log` | Signed; whoever writes to the table can delete entries and cannot write one | 400 days (DynamoDB TTL, and read by the time signed) | When they end. The identifiers of an account that was removed stay in it until then |
| Invitations. Built; on in production since v0.10.0 (2026-11-11) | DynamoDB, `pk=invitation#<hash of the code>` | Signed; the code only by its hash | Thirty days, or until used | When used |
| The list of invitations, for the operator: for each, a random identifier, where the invitation is kept (the hash of its code) and when it was made. Never the code. Built, not released yet | DynamoDB, `pk=invitations`, `sk=<identifier>` | Signed; it proves nothing by itself, the invitation does | Thirty days (DynamoDB TTL) | When the invitation is revoked, or found used or ended |
| A sign-in by email that was started: the address, the hashes of its code and link | The enclave's memory only | Never stored | Ten minutes | When used, when it ends, or when the enclave stops |
| A change of the contact address that was started: the new address, the account, the hash of its code | The enclave's memory only | Never stored | Ten minutes | When used, when it ends, or when the enclave stops |
| A confirmation that was started (ADR 0022): by mail, the contact address, the session and the hash of its code; with a passkey, the session and the challenge | The enclave's memory only | Never stored | Ten minutes by mail, five with a passkey | When used, when it ends, or when the enclave stops |
| The message that proves a new contact address (a six-digit code, no link), and the notices of security and access: a sign-in from a new device (when, how, and the browser named from fixed words, such as "Firefox on Mac"), a change in how the account is signed into, the address changed, deleting asked or cancelled, the account blocked or unblocked, made an admin or no longer one; and the message with the code that confirms it is the person (ADR 0022, a code and no link). A notice holds no code, link or token | In transit through Amazon SES, to the person's mail provider | As the sign-in message (ADR 0021) | Not kept by Permaily | As the sign-in message |
| How many of each kind of thing the operator and the admins did to accounts through the enclave, since it started. Kinds and counts, never an account | The enclave's memory only; public on `/status` | Never stored | Until the enclave stops | When the enclave stops |
| The sign-in message itself: the address, a six-digit code and a link | In transit through Amazon SES, to the person's mail provider | TLS from the enclave to SES. AWS reads it; the enclave sends only while SES says nobody else is given a copy (ADR 0021) | Not kept by Permaily. SES keeps the addresses that bounced or complained (its suppression list) | By hand, from the suppression list |
| Passkeys: the credential's identifier, its public key, a name, when it was made, whether it is synced. Never the private key, which stays with the person's authenticator | In the account's record | Readable and signed, an earlier copy refused: a passkey written by hand is not a way in, and a removed one does not come back | Until removed, or account lifetime | When removed, or with the account |
| Linked providers: which provider, the provider's identifier of the person's account there, and when it was linked. Not the address or the name the provider gave, and no token | In the account's record; a pointer from the provider's identifier at `pk=login#<provider>#<identifier>` | Readable and signed, an earlier copy refused | Until unlinked, or account lifetime | When unlinked, or with the account |
| The applications registered at the providers: client id and client secret | DynamoDB, the record `provider-apps` | Sealed under the root secret, an earlier copy refused | Until replaced or removed by the operator | By the operator |
| A sign-in with a provider that was started, or that waits for an address to be confirmed | The enclave's memory only | Never stored | Ten minutes; twenty while an address is confirmed | When used, when it ends, or when the enclave stops |
| Recovery codes, and the "passkey only" switch | In the account's record | The switch readable and signed; the codes only by their hash | Until used, replaced, or the switch is turned off | When used: a used code does not come back |
| A passkey ceremony that was started: its challenge, and for making a passkey the account | The enclave's memory only | Never stored | Five minutes | When used, when it ends, or when the enclave stops |
| Connections: vendor, state, dates and schedule readable and signed; the approved query, tokens and webhook secrets sealed (ADR 0019) | DynamoDB | As said; an earlier copy is refused, so a revoked connection does not come back | Until the connection ends or is revoked | With the connection |
| The person's rules, with their text (ADR 0019) | DynamoDB | Sealed | Account lifetime | With the account |
| What the model said of a message and a topic (ADR 0020) | DynamoDB | Sealed | As long as the message | With the message |
| Receipts of what left, and of what was refused (ADR 0019) | DynamoDB | Sealed, chained by hash, the head anchored | Account lifetime: they outlive the mail | With the account |

## Subprocessors (planned)

| Subprocessor | Purpose | Data |
|---|---|---|
| Amazon Web Services | Compute (Nitro Enclaves), storage (S3, DynamoDB), keys (KMS), outgoing mail (SES) | Encrypted content and metadata |
| Cloudflare | DNS for permaily.com; DNS-over-HTTPS resolver for DKIM checks (`cloudflare-dns.com`) | Hostnames; the domains and DKIM selectors of incoming mail senders |
| Let's Encrypt | TLS certificates | Hostnames |
| Google, GitHub, Meta, Microsoft (only for a person who chooses to sign in with one) | Saying who signed in | That the person signed in to Permaily, and when. Nothing of their mail |
| OpenAI (ADR 0020) | Deciding `about` conditions and removals by meaning; turning a rule written in plain words into a formula | The subject or the body text of the messages where exact conditions did not settle the matter; the text of a rule. Until zero data retention is approved for the organization, under OpenAI's ordinary retention |

## TODO

- [x] Default retention: 90 days, configurable to 7, 30, 180 or 365.
- [x] Receipts are kept for as long as the account (ADR 0019).
- [x] The model provider is OpenAI (ADR 0020).
- [ ] Zero data retention approved by OpenAI, before real users.
