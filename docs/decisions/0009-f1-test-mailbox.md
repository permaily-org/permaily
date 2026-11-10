# 0009 One test mailbox in F1

Status: Accepted.
Date: 2026-11-09

## Context

F1 receives and stores mail, but user accounts are F2. The decisions taken with the owners:

- Mailboxes: a single test mailbox compiled into the image, instead of an invitation API or bringing accounts forward.
- Retention: 90 days by default, configurable.
- Senders: any sender may deliver to the mailbox.
- Gmail forwarding confirmation: the code is shown through the mailbox API; the enclave never follows links from mail.

## Decision

- `deploy/enclave.json` holds the mailbox id, its address (`u-<random>@inbox.permaily.com`) and the SHA-256 of a bearer token. The token itself is kept by the owners (`~/.config/permaily/test-mailbox-token` on the operator machine). The image is public, so it holds only the hash.
- The API on `api.permaily.com/v1/mailbox` (read, list, raw, delete, retention) requires that token.
- Retention periods are 7, 30, 90, 180 and 365 days, one S3 lifecycle rule each (objects are tagged on upload); a change applies to mail received afterwards.
- DKIM results are recorded per message; a Gmail confirmation is marked verified only with a valid `google.com` signature.

## Consequences

- Rotating the token or adding a mailbox needs a new image (and a new PCR0). Acceptable for a test mailbox; F2 replaces it with accounts.
- The address is public in the repository, so it will receive spam. The mailbox accepts it and stores it with its DKIM results.
