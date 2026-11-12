---
title: Verifying the build
description: One command compares the public build with what's running now, and here's what it checks.
---

You can check that we run the public code. One command compares the public build with what's running now.

```bash
permaily verify app.permaily.com
```

```text
attestation    signature valid, AWS Nitro root
release log    found, v1.4.2, logged 2 Mar

public build   7f3a…c91e
running now    7f3a…c91e
result         matches public code
```

## What the command checks

- **The attestation.** The running service signs what it's running, through AWS Nitro, and the chain has to be valid.
- **Debug mode.** It has to be off. AWS zeroes the measurements in debug mode, so the check rejects those.
- **The connection.** The TLS key you're talking to has to be the one in the attestation.
- **The release log.** Every approved build is listed publicly, before it goes live, and the running one has to be there.
- **The fingerprint.** Rebuild the public code and you get the same fingerprint. The running service signs its own, and the two have to be equal.

The command also prints what the operator did to accounts through the service since it last started: how many times each kind of action, such as blocking an account or asking to delete one, never which account. It doesn't fail on these counts. It shows that something happened, not whether it was right.

## Why a web page isn't enough

A browser can't confirm the page it just loaded came from the verified service. The command checks the connection itself, and rogue certificates would show up in public logs. The [Security page](/security/) can show you the two fingerprints, but only the command can check them.

## What a match doesn't prove

A match means the service runs the public code. It doesn't mean that code has no bugs, or that an app treats your mail well after receiving it. The [trust model](/docs/trust-model/) lists every limit.
