# 0016 Refuse earlier copies of stored records

Status: Accepted.
Date: 2026-11-11

## Context

Everything the enclave stores is sealed, so whoever runs the storage (the operator, or anyone with the account) cannot read or forge it. They can still put back an earlier copy: a sealed value from last month is as authentic as today's, and an enclave keeps nothing across a restart to tell them apart. Found while reviewing what "the operator cannot alter content" really covered (docs/threat-model.md).

Where it hurts today is the TLS identity: a key replaced because it may have leaked comes back if its old record is restored. Where it will hurt most is the data model that comes next: a permission the user revoked must not reappear.

## Options

1. **Remember in memory.** The enclave keeps the newest version of each record it has seen and refuses older ones. Costs nothing and protects nothing across a restart, which the operator can cause.
2. **Anchor in a store nobody can remove from.** Each change leaves a small commit (name, version, hash) in an S3 bucket with Object Lock in compliance mode; a record is read by its newest commit. Rests on AWS, like the KMS keys already do.
3. **Anchor in the public transparency log.** Anyone could audit the commits. But Rekor v2 has no way to ask for "the newest entry of this kind", so after a restart the operator would be the one to say which is the newest, and each change would wait for a third party.

## Decision

1 and 2 together. 3 is left for the audit log, where being public is the point.

**Commits.** `store.Anchored` wraps the records that must not go back. On a change it stores the new value under a name of its own (`<name>@<version>-<hash>`), then appends a commit `{name, version, hash of the stored value}` to the store of commits. A read takes the newest commit and the value it names. If that value is gone, or is not what was committed, the record is lost (`store.ErrLost`): never the version before. A record that may be made again, like the TLS identity, is made again as a later version; one whose loss must not be forgiven checks for `ErrLost` and fails closed. The value goes in before the commit, so a failure in between leaves a value nobody refers to and the record as it was.

**The store of commits** (`store.S3Commits`) is a bucket created with Object Lock. The enclave writes each commit with the lock in the same request (compliance mode, until a date `anchor_retention_days` from then), so S3 refuses the write on a bucket without Object Lock and the enclave cannot be handed another kind of bucket. Tried on 2026-11-11 as an administrator of the account: deleting a locked version, shortening its retention, switching it to governance mode, suspending versioning, removing the lock configuration and deleting the bucket are all refused.

**What can still be added.** An administrator, or the parent instance with the enclave's credentials, can add: another version of an object, an object at a version not used yet, and a delete marker, which hides an object from a plain listing and a plain GET without removing it. So the enclave lists every version of every object, reads each by its version id, and counts only those with an authentication code under a key derived from the root secret (`keys.Root.CommitKey`). Nothing that is added can move the newest commit back; enough junk can stop a record from being read, which is the operator's to do anyway.

**In memory.** While it runs, an enclave remembers the newest commit of each record it read or wrote. If the store ever answers with less, it refuses the record (`store.ErrRolledBack`) and reports it in `/status`, where `permaily-verify` fails on it. It should never happen; if it does, the guarantee above failed or the bucket was replaced, and this is how anyone would know.

**A record from before.** The first time, a record written by an earlier release is under its plain name, without a commit. It is taken as it is, once, and committed as version 1. It stays where it was, so that release still finds it if it has to run again.

**Removing a record** (added on 2026-11-11, for accounts). A removal is a commit like any other whose hash is all zeros: the stored value is deleted and the newest commit says the record is gone. A read then answers "not there", never an earlier copy, and the name can be stored again as a later version. The commit carries the same authentication code and lock as the rest, so a removal cannot be turned into a record or back, and of two commits at one version the removal wins. An image from before this takes such a commit for a value that cannot exist and answers that the record is lost, which for an account is an error and never "no account" (argued from its code; the upgrade itself, from the release before to this code, was run on staging on 2026-11-11, docs/acceptance.md).

**Only the newest version, where asked** (`OnlyNewest`). Each version is stored under a name of its own. For accounts the one before is deleted once the new one is committed, so what a person took out of their account does not stay in the table; the delete never fails a write that was already committed. The TLS identity does not use it: an earlier release still has to find the copy it wrote. A reader that finds the value of the newest commit gone reads the newest commit again before saying the record is lost, since another writer may have moved on in between.

**A write only if nothing changed** (added on 2026-11-12, so that two enclaves can run at once). A change of a record is a read, an edit and a write. Two enclaves that did that to one record at once both wrote, the later one after the other, and the first change was gone. So a record is read together with where it was read (`GetRecordAt`) and stored only if nothing was committed for it since (`PutRecordAt`, `store.ErrChanged` otherwise). No lock is needed: the commit goes at the version the read saw as the next one, and the store of commits takes one commit per version (S3 writes it only if nothing is at that key). A removal is a commit too, so a write that raced with a removal is refused and the record does not come back. The value stored for a write that was refused is deleted, unless the commit that was made names that very value. The caller reads again and decides again; how many times is the caller's to bound. For this, the next version is the lowest one above the newest commit that nothing is at, and no longer one above everything in the bucket: something left higher up would send one enclave past the version another is about to write, and both would be told they wrote. `PutRecord` and `RemoveRecord` write whatever the record is, as before: the TLS identity uses the first, and a removal is not to be held back.

**What that condition does not hold against** is whoever can write to the bucket. A delete marker makes a version look free to S3, so a second commit can be added where one is, and both enclaves are told they wrote. One of the two commits counts, the same for every reader, and the change of the other is lost. The enclave whose commit does not count refuses the record from its next read (`store.ErrRolledBack`) and says so in `/status`, where `permaily-verify` fails on it: noticed, not prevented, and only while that enclave runs. It takes someone who writes to the bucket in the moment between two changes of one record through two enclaves. To close it, the first commit stored at a version would have to be the one that counts, and a writer would check that after writing; that changes which of two commits counts, so it has to be tried against a real bucket and thought through for an upgrade first. `TestAnchoredOverS3CommitsWithADeleteMarkerInBetween` shows the limit.

## What it covers, and what it does not

- **Now: the TLS identity** (certificate key, ACME account key, certificate chain), compiled into the image as `anchor_bucket` and `anchor_retention_days` in `deploy/enclave.json`.
- **With the data model:** the records it adds whose going back matters (permissions and their revocation first). They go through `store.Anchored` from the start.
- **Not the mailbox yet.** Its state is overwritten in place, and anchoring needs each version stored apart; that format is about to be redesigned with the data model, and what could go back today is a retention period and the confirmation codes of a test mailbox.
- **Not messages.** A deleted message put back, or a message removed, needs an audit log rather than a commit per message.
- **Not the root secret's records.** They carry an attestation document and are locked by KMS; putting one back gives the same secret.

## What it rests on

- **AWS.** S3 enforces the lock, and answers the enclave over a connection the enclave authenticates. Like the key policies, the owners of the account cannot undo it on their own.
- **The lock has an end.** Production keeps each commit ten years. A record that does not change for that long loses its protection the day the lock of its newest commit ends; the verifier warns from thirty days before. The TLS identity changes with every certificate. Extending locks before they end is not built.
- **A new deployment starts over.** A new root secret gives a new key, so the commits of the deployment before are no longer commits; versions go on from where they were.
- **Two enclaves of one deployment** that write the same record at once each store their value, and one commit per version counts, the same one for every reader. A caller whose change must not undo the other's reads with `GetRecordAt` and writes with `PutRecordAt`: accounts do (ADR 0017). The TLS identity does not: the later certificate stays in the storage.
- **The bucket cannot be deleted** while it holds a locked commit, so `tofu destroy` leaves it. Staging images lock theirs for a day.
