# 0015 Send the enclave's logs to the parent instance

Status: Accepted.
Date: 2026-11-10

## Context

A production enclave has no console: `nitro-cli console` works only in debug mode, and since ADR 0012 a debug enclave gets no keys from KMS, so it behaves differently from production. When a certificate could not be issued on 2026-11-10 (a missing CAA record) the enclave served nothing and said nothing; finding the cause took guesswork.

## Options

1. **Nothing leaves the enclave.** Safest by construction, and failures are invisible.
2. **Log lines go to the parent instance over vsock.** The parent is untrusted, so the lines must be harmless.
3. **An attested log endpoint over HTTPS.** Useless exactly when it is needed: when there is no certificate.

## Decision

Option 2.

- The enclave writes its log (structured, one JSON line per event) to the console and to vsock port 8003 of the parent (`internal/enclave.LogSink`). Writing never blocks and never fails the caller: if the parent is not reading, lines are dropped, counted, and the count is reported when the connection returns. Lines are cut at 8 KiB.
- `permaily-host` prints each line with the prefix `enclave: ` on its own output, which systemd keeps in the journal. It never interprets them.
- The channel carries data one way. The parent could already see the same lines on the console in debug mode; the rule that logs are public is not new, it now applies in production.
- What may be logged stays as in the threat model: no email content, addresses, tokens, cookies, codes or keys. `TestMailEndToEnd` keeps every line logged while a message is received, listed and read, at debug level, and fails if any of the test's addresses, subject, body or token appears.

## Consequences

- The operator can read why an enclave fails, with `journalctl -u permaily-host`.
- A log line is now a way for data to leave the enclave, so each new log statement needs the same care as an API response. Errors from libraries are logged; today they name AWS resources and hosts, never mail.
- The journal lives on the instance and is lost with it. Shipping it elsewhere is not done yet.
