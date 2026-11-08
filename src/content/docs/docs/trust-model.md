---
title: Trust model
description: What Permaily claims, what verification proves and doesn't prove, the hard problems, and the threats it answers.
---

The service handling your mail is running the public code, and that code only shares what you approved. This page is what that claim rests on, and where it stops.

## What verification proves

- The enclave runs an image whose measurement matches a public build.
- Stored data can only be decrypted by approved measurements.
- Every approved release is publicly listed.

## What verification doesn't prove

- That the public code is free of bugs or hidden flaws. Review is still needed.
- That AWS and its Nitro hardware behave as documented. AWS stays in the trusted base.
- That a release is good, only that it's visible.
- What a connection does with mail after receiving it.
- That Google or Microsoft can't see your mail. They host it.
- That traffic patterns are hidden. The operator can see timing, sizes and IP addresses.

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

1. KMS is pinned to the image measurement, known as PCR0.
2. Each new measurement is published to the log before it can receive keys.
3. A waiting period lets outside monitors react, with a defined and publicly logged route for urgent security fixes.
4. Routine administrative access to the key policy is removed, so changes are rare and visible.

That still leaves the weakest link: what stops the operator from changing the key policy? AWS account control ultimately allows it. CloudTrail records it, but those records are private unless published. Two ways to close this are publishing key-policy events, and having the old enclave verify a new release and hand keys over, which would take KMS policy changes out of routine upgrades.

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
| Database or backup stolen | Ciphertext only. Keys need an approved measurement |
| Connection asks for too much | Preview at consent, plain-language scope, expiry, unused-access revocation |
| Connection is compromised | Short-lived tokens, per-connection limits, instant revocation, unusual-activity alerts |
| Password reset or login code exposed | Never share, on by default, above every rule |
| Over-broad rule | Preview before saving, alerts on sudden match volume |
| Pack silently widened | Version pinning, fresh consent for widening, review and automated checks |
| Spoofed sender enters a slice | Match on authenticated sender, not the `From:` header |
| Mail sent straight to the inbound address | Secret per-account addresses, and checks that mail came through the provider |
| Malicious email attacks the parser | Memory-safe language, fuzz testing, resource limits |
| Prompt injection reaches an agent | Untrusted-content markers, read-only by default, approval for send and delete |
| Malicious dependency in the build | Pinned dependencies, reproducible builds, a small trusted codebase, independent rebuilds |

## Try to break it

We publish this and invite people to attack it. If you find something, [tell us](mailto:security@permaily.com), and we'll credit you in the [release log](https://github.com/permaily-org/permaily/releases).
