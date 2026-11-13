# 0024 Two enclaves share one TLS identity and answer each other's ACME challenge

Status: Accepted, built, in production since v0.11.0 (2026-11-12). No deployment runs two enclaves yet.
Date: 2026-11-12

## Context

The maintainer decided on 2026-11-12 that the service must be ready to run two enclaves at once behind one address: a load balancer that passes TCP through, so TLS still ends inside an enclave, and any connection may land on either (ADR 0017, "Two enclaves at once"). One enclave runs today. Accounts are another piece of work. Three things outside accounts did not hold:

1. **The TLS identity** (the certificate key, the ACME account key and the certificate, ADR 0016) was written with a plain write. Two enclaves hold the same key, and each asked the CA for a certificate of its own. The last one stored won, and nothing coordinated them. Two enclaves starting together made two orders, and each renewal made two more. Let's Encrypt allows 5 certificates a week for one set of names (docs/operations.md).
2. **The CA's check can land on the other enclave.** The enclave proves the names with TLS-ALPN-01: the CA connects to the address with ALPN `acme-tls/1` and expects the challenge's certificate. Only the enclave that ordered had that certificate, so a check that reached the other one failed.
3. **The test mailbox** (ADR 0009) was made, and its state written, with a plain write. Two enclaves that start together could each make it with a data key of its own. The later write stayed, and mail sealed under the other key could not be opened. One enclave's change of the state (a retention period, a forwarding code) could undo the other's.

Whoever runs the service can read and write the whole storage and restart an enclave. This work must give them nothing: no way to make an enclave serve a key or a certificate it should not, to put back an earlier identity (ADR 0016 already refuses that), or to read a mailbox.

## Options

**Who asks the CA.**

1. **Each enclave has its own key.** Attestation binds one key per connection, so a verifier would pass. But the certificate history would show certificates with a key the enclave being verified does not hold, which is the failure `permaily-verify` looks for (ADR 0012). Two keys would also mean two identities to keep and to replace.
2. **A lock in a plain entry of the table.** An entry is versioned, so two enclaves cannot both take it. But it is not sealed: whoever writes to the table can delete it, and both enclaves would order.
3. **A claim inside the sealed, anchored identity.** The identity says which enclave is ordering and until when. It is written only if nothing was written since it was read (`PutRecordAt`, ADR 0016). Only an enclave can make one, and putting back an earlier copy is refused.
4. **One enclave chosen by configuration.** The parent instance would have to say which one it is, and the parent is untrusted. If that enclave were down, nobody would renew.

**How either enclave answers the CA's check.**

1. **Send the CA's connection to the enclave that ordered.** A load balancer that passes TCP through does not know which one that is. Only the host could read the ALPN, and the host is untrusted.
2. **DNS-01.** It needs a token for Cloudflare inside the enclave, which ADR 0002 refused.
3. **Keep the challenge in the storage while the order is open.** Both enclaves answer with it.

## Decision

Options 3 and 3.

**The claim.** The identity has a field `order`, with a random name of the enclave that orders, made new at each start, and the time the claim ends. It is a field added to version 1 of the record, so a release from before ignores it. An enclave that finds no good certificate, or one due for renewal, does this:

- It reads the identity with `GetRecordAt`.
- If another enclave's claim has not ended, it orders nothing. It reads again a minute later, or when the claim ends if that comes first.
- Otherwise it writes its own claim at the version it read. If another enclave wrote first (`ErrChanged`), it reads again and decides again, at most five times.
- It orders only after it reads its own claim back. When it has the certificate, it writes it at the version it read, and so the claim ends. If that write is refused, it reads again. It serves the stored certificate when that one lasts longer, and otherwise writes its own, keeping any claim of another enclave.
- The claim lasts for one order (10 minutes) plus the wait before the next try should the order fail (one minute at first, doubling up to an hour). Each try claims again. While one enclave keeps failing, the other keeps waiting, so two enclaves never order more often than one would.
- An enclave that stops in the middle of an order leaves a claim that ends by itself. The other enclave takes over then.

**Adopting.** Every enclave reads the stored identity every 10 minutes, and every minute while another enclave holds a claim. It takes from it the account and any certificate for its own key that is valid, covers every host and ends later than the one it serves. Then it serves that certificate, without a restart. If the stored identity holds another key, it takes nothing, orders nothing and writes nothing. It goes on serving its own key until it restarts. That happens only when the identity was lost and made again (ADR 0016).

**The challenge.** Before the enclave tells the CA to check, it stores the challenge's certificate and its private key as an entry, `pk=acme-challenge#tls-<CA host>`, `sk=<host>`. The entry is sealed under the identity's data key and bound to the identity's name and the host. Inside the seal it says when it ends, which is when the claim would end. The entry also expires then, and is deleted when the order ends. An enclave asked for `acme-tls/1` with a name of its own that it has no challenge for in memory reads the entry, at most once a second for each name. It answers with the entry only until the time sealed inside it.

**What someone who reads the table could do with a challenge** while the order is open. The challenge's certificate is self-signed. It carries the SHA-256 of the key authorization: the token, a dot, and the thumbprint of the ACME account's public key. Anyone who connects to the enclave with `acme-tls/1` during the order gets that certificate anyway. Its private key is used only to complete one TLS handshake with that certificate. With both, someone could answer the CA's check instead of an enclave, which the host can already arrange, since it carries the CA's connection. That check validates the authorization of the enclave's own ACME account. Only the holder of the account's key can use it to finalize an order, with a CSR signed by the key it wants certified. The account's key is sealed. The key authorization holds the account's thumbprint, so it cannot validate an order of another account either. No browser trusts the challenge's certificate. Deleting the entry makes a check that lands on the other enclave fail, which is no more than dropping the CA's connection. An earlier entry put back is refused once the time sealed in it has passed. Before that it answers a token the CA no longer asks for. So the challenge would be safe in the clear. It is sealed all the same, for the cost of one seal under a key the enclave already holds: no private key leaves the enclave unsealed, as `internal/AGENTS.md` asks.

**The test mailbox.** It is made with a write that only creates. The enclave that loses reads what the other made and uses its data key. Its state is written at the version read (`store.Mailbox.Version`), and on `ErrConflict` it is read and changed again, up to three times. A mailbox stored before versions existed reads as version 1, and is written at version 1 once. This mailbox is scaffolding of F1 that mailboxes per account replace (ADR 0018); the release that builds them retires it (ADR 0026). It is not anchored, as ADR 0016 says: whoever writes to the table can still put back its state from before. That is a retention period and Gmail's forwarding codes.

## What a verifier sees

`permaily-verify` checks each connection on its own: the attestation binds the key of the certificate served on that connection, and `/status` is fetched only over a connection with that key. Two enclaves serve one key, so a run whose requests land on both passes. What each `/status` reports is one enclave's: the version at which it read the TLS identity, the commits it saw, the mail it watched, the operator's counts. Each is checked on its own (`TestTwoEnclavesAnswerOneRun`).

The certificate history (`pkg/certwatch`) sorts certificates by key, not by how many there are. During a renewal, two valid certificates with the attested key both count as the enclave's own. The other enclave serves the earlier one for up to 10 minutes after the new one is stored. Nothing in the verifier assumes one enclave, so nothing needed fixing. Tests now pin this down: the case "two enclaves renewing" in `TestCheckCertificates`, and `TestClassify`.

What a verifier cannot do is reach each enclave on purpose. Behind one address, it checks the ones its connections happen to land on. Each attestation document names the enclave that made it (`module_id`), and the verifier prints it for each host. Counting the distinct ones over many connections, or giving each enclave its own address, needs a decision, and is not built. If one enclave serves a key the other no longer stores, the verifier fails on the certificates with the other key, as it should.

## The root secret with two enclaves

`internal/keys` already writes the records of the root secret with writes that only create, and needed no change. Two enclaves of one image that start together both read that there is no record, and both make one. One write is refused, and that enclave opens the record the other wrote (`TestTwoEnclavesOfOneImageStartingTogetherHaveOneRootSecret`). The marker `deployment` is written the same way. Two enclaves of the running release asked for a handoff at once write the next release's record once: the other is told it exists. The next release's enclaves all open that one record and get one secret (`TestTwoEnclavesHandingOverAtOnceGiveTheNextReleaseOneRecord`). An enclave of the next release started before the handoff waits for it, one or two of them alike.

## What it rests on

- **The store of commits keeps one commit per version** (ADR 0016). Whoever writes to the bucket can, in the moment between two writes, put a commit where one is. Two claims would then both count, and two enclaves would order. That is noticed (`/status`, `permaily-verify`), not prevented, and costs one certificate.
- **The enclaves' clocks agree to within a minute.** A claim lasts at least 11 minutes. Clocks that differ by more could let both enclaves order: one certificate more.
- **Records that keep commits.** The identity uses `GetRecordAt` and `PutRecordAt` only when its records are `store.Anchored`, which every image with an anchor bucket has: production and staging. Without one the last write wins, which is for one enclave only.
- **The limit of the CA.** At most 5 certificates a week for one set of names. One enclave alone makes one order every 60 days. Two enclaves make the same, plus one in the worst case each time an order outlives its claim (more than 11 minutes) or the clocks or the bucket go wrong as above.
- **A lost identity** is made again with a new key, as it was with one enclave. An enclave that was running keeps its old key until it restarts.

## What is left

Running two enclaves still needs:

- A pass-through load balancer for ports 443 and 25, and its health checks.
- The parent instances or enclaves behind it, and a host proxy for each.
- A way to roll a release out to two (docs/operations.md).
- A first release with this code must run alone before a second enclave is added. A release from before writes the identity with a plain write and does not know the claim.
- How a verifier reaches each enclave behind one address (above).

None of it was run against a real CA or real enclaves. The ordering, the claim and the challenges were run against Pebble: one that accepts every challenge, and one that validates, where every one of the CA's connections was sent to the enclave that did not order (`TestTheCAsCheckReachesTheOtherEnclaveAgainstACME`, run by hand, docs/testing.md).
