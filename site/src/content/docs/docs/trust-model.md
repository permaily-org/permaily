---
title: Trust model
description: What Permaily claims, what verification proves and doesn't prove, the hard problems, and the threats it answers.
---

The service handling your mail is running the public code, and that code only shares what you approved. This page is what that claim rests on, and where it stops.

## What verification proves

- The enclave runs an image whose measurement matches a public build.
- Sealed data is decrypted by the approved service using protected keys. Some account and connection metadata is deliberately stored in readable, signed records; the privacy policy lists it.
- Every approved release is publicly listed.

## What verification doesn't prove

- That the public code is free of bugs or hidden flaws. Review is still needed.
- That AWS and its Nitro hardware behave as documented. AWS stays in the trusted base.
- That a release is good, only that it's visible.
- What a connection does with mail after receiving it.
- That Google or Microsoft can't see your mail. They host it.
- That traffic patterns are hidden. The operator can see timing, sizes and IP addresses.
- That the operator was right to block or delete an account, only how many times each kind of action happened since the service last started.

**A bad release by the operator becomes detectable. It doesn't become impossible.** That's still a large improvement over a service where nobody can detect anything at all.

## The chain

Verification only works if every link holds. One broken link voids the claim.

| Link | Requirement |
| --- | --- |
| **Reproducible build** | Two independent builders produce an identical image |
| **Measurement** | Production never runs in debug mode. AWS [zeroes the measurements in debug mode](https://docs.aws.amazon.com/enclaves/latest/user/set-up-attestation.html), so verifiers must reject those |
| **TLS inside the enclave** | Pass-through load balancing, with the certificate key held only in the enclave |
| **Attested TLS** | The attestation includes the hash of the live TLS public key |
| **Key release** | The KMS policy is conditioned on the measurement |
| **Release log** | Every approved measurement is public and append-only |
| **Independent checking** | A command-line check, a public page and outside monitors |

## The hard problems

### Upgrades

Every release changes the measurement, which is exactly the point where trust can break.

AWS [recommends PCR3 and PCR8 together for flexibility](https://docs.aws.amazon.com/enclaves/latest/user/set-up-attestation.html), because keys then survive image changes. PCR8 identifies the signing certificate, so that design would trust whatever the operator signs. It undermines the whole claim, so we don't follow it. Instead:

1. Each release has its own KMS key, pinned to its image measurement, known as PCR0. Only that image can use the key, and the key's policy gives nobody in the AWS account a way to change that.
2. The running enclave hands the keys to the next release itself, and only after checking that its measurement was published by the public repository's release workflow in a public, append-only transparency log.
3. Nothing in the AWS account is edited to let a release in, and no key policy can be edited at all.

What remains: an older release can still open the keys until its own KMS key is deleted; the release workflow runs on GitHub and is recorded by Sigstore, and we rely on both; and AWS operates KMS, whose support team can restore an account owner's access to a key. The enclave reads its key's policy every minute and reports every state it has seen, and the verifier checks them all, so such a change would be visible to anyone.

### Browsers can't verify attestation

A command-line check can confirm that the TLS key it's talking to is the one in the attestation. A browser can't do this for the page it just loaded. An operator could in theory obtain a certificate for the domain and serve a modified app from outside the enclave.

None of these mitigations is complete:

- Certificate Transparency monitoring makes a rogue certificate publicly visible.
- A browser extension or desktop verifier can perform the real check for those who want it.
- The Proof page describes this limit, and doesn't imply that a green tick settles it.

### Nobody will compile the code themselves

Almost no one will run a verifier. That's fine, because the value lies in anyone being able to check, as with Certificate Transparency. It only needs a few independent parties actually doing it: a public monitor, published results, and community members running the check.

Apple's [Private Cloud Compute](https://security.apple.com/blog/private-cloud-compute/) is the best public model. It publishes measurements of production builds to a tamper-evident log, and its clients only send data to nodes whose attested measurement appears in that log.

## Threats

| Threat | Response |
| --- | --- |
| Operator deploys altered code | Measurement-bound keys, public log, independent monitors |
| Operator intercepts traffic before the enclave | TLS ends in the enclave, attested key, Certificate Transparency monitoring |
| Database or backup stolen | Mail content and other sealed fields remain encrypted. Account contact details and other signed account, mailbox and connection metadata are readable to someone with database access. Decrypting the sealed fields requires the protected keys; backups can also contain wrapped mailbox keys from before deletion. The privacy policy describes those limits. |
| Connection asks for too much | The consent screen shows the query, parts and expiry. Reads have limits, widening needs fresh approval, and the person can revoke access. Automatic unused-access revocation is not built. |
| Connection is compromised | Short-lived tokens, per-connection limits and revocation, with in-progress delivery stopped within five seconds. Unusual-activity alerts are not built. |
| Operator takes over an account | The code has no way for the operator to sign in as someone, change a contact address, or add or remove a way to sign in. The operator, and admins the operator names, can block and unblock an account, ask to delete it, which takes seven days, cancel a deletion they asked for (not one the person asked for), and sign its devices out: that keeps people out or lets them back, and lets nobody in. The person is told by mail of a block, an unblock or a deletion (not of being signed out), and each kind of action is counted on `/status` |
| Someone else signs in to an account | A notice by mail of a sign-in from a device that hadn't signed in to the account before, of a change in how it's signed into, and of a change of its contact address, sent to the old address and the new one |
| Password reset or login code exposed | Sign-in and security, a rule on every account from the start that holds this mail back from every connection before any query runs. Exact checks hold matching messages, and a model decides the rest, holding a message unless it answers clearly no. It can't be turned off yet |
| A rule misses what it should catch | The built-in Sign-in and security rule uses exact checks and then the model. Uncertain results are held, but a wrong confident answer can miss a message. User-authored rules and alerts about their match volume are planned. |
| A connection learns what a rule protects | Held-back mail looks the same as mail its query didn't match, and no rule name or count leaves Permaily |
| The model that checks rules leaks mail | The model runs at OpenAI, outside the enclave, so OpenAI sees the field a rule about meaning examines. The provider and the address it is reached at are compiled into the measured image, so mail can only go where the public code sends it; what OpenAI keeps is its own commitment, not something the enclave can prove |
| Spoofed sender enters a slice | Match on authenticated sender, not the `From:` header |
| Mail sent straight to the inbound address | Each mailbox has an unguessable forwarding address. Anyone who learns it can send mail to it; the service does not prove that every message passed through your provider. It records verified DKIM domains for conditions that require them. |
| Malicious email attacks the parser | Memory-safe language, fuzz testing, resource limits |
| Prompt injection reaches an agent | Mail is marked as untrusted content. The current API and MCP tools only read mail; they cannot send or delete it. These controls do not guarantee that a connected agent will ignore instructions inside a message. |
| Malicious dependency in the build | Pinned dependencies, reproducible builds, a small trusted codebase, independent rebuilds |

## Try to break it

We publish this and invite people to attack it. If you find something, [tell us](mailto:security@permaily.com), and we'll credit you in the [release log](https://github.com/permaily-org/permaily/releases).
