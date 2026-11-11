# Operations

Runbooks for production. Setup from scratch is in [deploy.md](deploy.md). Every command uses `AWS_PROFILE=permaily` and region `us-east-2`.

## Deploy a release

The running enclave hands the root secret to the next release, then the next release is started (ADR 0013). Nothing in AWS is edited to let a release in: each release has a KMS key of its own that only its image can use.

```sh
old=v0.8.0 new=v0.9.0
mkdir -p /tmp/permaily-$new && cd /tmp/permaily-$new
gh release download $new --repo permaily-org/permaily
gh attestation verify permaily.eif --repo permaily-org/permaily --signer-workflow permaily-org/permaily/.github/workflows/release.yml --source-ref refs/heads/main
sha256sum -c SHA256SUMS --ignore-missing
mkdir eif && mv permaily.eif pcrs.json eif/

# 1. The key of the new image. deploy/tofu/production.auto.tfvars:
#      release_keys = { "v0.8.0" = "<its PCR0>", "v0.9.0" = "<jq -r .PCR0 eif/pcrs.json>" }
tofu -chdir=deploy/tofu apply          # creates one KMS key and its alias; then `tofu plan` must show no changes

# 2. The running enclave gives the root secret to the new release. The script
#    first gives it the trust root Sigstore publishes today, which it checks.
scripts/handoff.sh $old $new

# 3. Start the new image and verify.
scripts/deploy.sh eif permaily-host-linux-arm64
scripts/verify.sh $new                 # from a server with outbound port 25, add -require-smtp
```

Then delete the key of the previous release (below), and open the pull request that commits `production.auto.tfvars`.

- **The handoff is refused** (HTTP 422): the answer says why. Nothing was written and the running enclave keeps serving. The usual causes are a key that was not created yet, or files of the wrong release.
- **The new image was started first**: it logs `waiting for the running release to hand over the root secret` and serves nothing. Start the old image again, run the handoff, start the new one.
- **Step 2 can be repeated**; the second time it answers `"created": false`.
- **Verify before step 2** when in doubt about what is answering on `api.permaily.com`: the admin token only lets someone ask for a handoff, which the enclave decides alone, but there is no reason to give it to an impostor.

## Roll back

Deploy the previous release again with `scripts/deploy.sh`. Its record and its key are still there, and everything stored since is sealed under the same root secret, so it reads it. A handoff back to an older release is refused on purpose: a rollback only works while the old release's key exists.

## Delete the key of an old release

An old image can open the root secret for as long as its key exists: a flaw found in an old release stays a way in, and whoever verifies the service has to trust that release too. So deleting the previous key is the last step of a deployment. While the service has no users, do it once the new release is verified, in the pull request that records the deployment. With users, wait one to three days of normal running that include a restart of the new release, because the old key is also the only way back. Remove the old release from `release_keys` and apply: its key is scheduled for deletion and gone after seven days. This cannot be undone, from the moment it is applied: cancelling the deletion leaves the key disabled, and its policy lets nobody enable it again. Never remove the release that is running.

## A new deployment, or losing everything

An image creates a root secret only when the table has neither a record `root-<its PCR0>` nor the record `deployment` (`pk=system`, `sk=record#deployment`). To start over on purpose, delete those items, the TLS identity (every `sk` that starts with `record#tls-<CA host>`) and the mailbox items; the next image creates a new root secret, a new TLS key and a new ACME account (see Certificates below), and everything stored before is unreadable. There is no way back. The commits in the anchors bucket cannot be deleted and need not be: they belong to the root secret that is gone, and the new deployment's versions go on after them.

If the instance is lost, nothing is: create it again with OpenTofu and deploy the same release. The root secret is in DynamoDB, locked by the release's KMS key.

## Test with staging images

A staging image (`.#eif-staging`) is a deployment of its own on the same instance and names: Let's Encrypt staging, the table `permaily-mail-staging`, and a root secret that only staging images can receive. Production is down while a staging image runs.

```sh
git push origin HEAD:refs/heads/staging/<name>     # the staging-image workflow builds and attests it
gh run download <run id> --name staging-image --dir /tmp/stg-a
# release_keys: add "staging-a" = "<its PCR0>", without committing; acme_account_uris: see Certificates below
tofu -chdir=deploy/tofu apply
scripts/deploy.sh /tmp/stg-a /tmp/stg-a/permaily-host-linux-arm64
go run ./cmd/permaily-verify -pcrs /tmp/stg-a/pcrs.json -ca-file letsencrypt-stg-root-x1.pem -skip-certificates
PERMAILY_CA_FILE=letsencrypt-stg-root-x1.pem scripts/handoff.sh /tmp/stg-a /tmp/stg-b    # to a second image, built later
```

Two staging images built at the same time may be logged in either order, and a handoff only goes forward in that order. Afterwards deploy the production release again, remove the staging keys from `release_keys`, restore the CAA records and delete the `staging/` branches.

## Records that cannot go back

The TLS identity is read by its newest commit in the bucket `permaily-anchors-<account>` (ADR 0016); `/status` and the verifier show its version and until when the commit is locked.

- **`the committed version of the record is gone`** in the enclave's log, for the TLS identity: its item was deleted or replaced. The enclave makes a new identity (see "Replace the TLS key" for the CAA records).
- **The verifier fails with `the store of commits went back`**: the enclave had seen a commit that the bucket no longer answers with. Nobody in the account can remove one, so treat it as an incident: find out what happened to the bucket (CloudTrail, `aws s3api list-object-versions`) before restarting the enclave, which forgets what it saw.
- **The verifier warns that a lock ends within 30 days**: the newest commit of a record is ten years old. Make the record change (for the TLS identity, a renewal does) so that it gets a new commit.
- The enclave's role can list, read and write in that bucket and cannot delete. Nothing needs to be cleaned there: a commit is a few hundred bytes.

## Look at enclave logs

The enclave sends its log lines to the host, which keeps them in the journal with the prefix `enclave:` (since v0.5.0):

```sh
aws ssm start-session --target "$(tofu -chdir=deploy/tofu output -raw instance_id)"
sudo journalctl -u permaily-host --since "1 hour ago" | grep "enclave:"
```

A line `log lines dropped` means the host was not reading for a while. The journal is lost if the instance is replaced.

For an enclave that does not even start, deploy the same image with `--debug` (`scripts/deploy.sh eif host --debug`) and read its console:

```sh
sudo nitro-cli console --enclave-name permaily
```

Debug mode zeroes the PCRs, so verification fails on purpose and the enclave does not even ask KMS for keys: a debug enclave has no root secret, so no TLS key and no mailbox, and its console says so. Use it to read why an enclave does not start, not to test features. Redeploy without `--debug` right after.

## Let's Encrypt rate limits

At most 5 certificates per week for the same set of names. Since v0.4.0 the enclave keeps its key and certificate sealed, so a restart or an upgrade issues nothing; only a renewal (every 60 days) or a new TLS key does. If issuance fails with `rateLimited`, wait for the window, and use `.#eif-staging` for tests.

## Rotate the Cloudflare token

The OpenTofu token `permaily-opentofu-dns` (DNS edit and zone read on `permaily.com`) expires on 2027-02-07. Create a new one with the same permissions, store it in `~/.config/permaily/cloudflare-api-token` (mode 0600), run `tofu plan` to check it, then delete the old token.

## Verify production

```sh
scripts/verify.sh "$(gh release view --repo permaily-org/permaily --json tagName -q .tagName)"
```

Run it after every deploy and record failures as incidents. Then check that a proxy in front of the enclave is detected:

```sh
PERMAILY_RELEASE=<tag> go test -tags production -run TestProductionMITM -v ./cmd/permaily-verify
```

## Scheduled verification

The workflow `verify-production` runs `scripts/verify-production.sh` every half hour: the latest release's verifier against production, or the previous release's while the latest is not deployed. Its output is in each run's summary, public. A failed run is an incident: read which check failed and since when.

Two things start it:

- **EventBridge**, at minutes 7 and 37 (`deploy/tofu/verify.tf`, which also starts `sigstore-watch` once a day): a rule in the AWS account calls GitHub's API to start the workflow. It only says "run now"; the check runs on GitHub with the repository's code. These runs show as started by hand (`workflow_dispatch`), by the owner of the token.
- **GitHub's own schedule**, with the same minutes. GitHub does not keep it: in the first day it started two runs instead of about forty, hours apart. It stays because nobody in the AWS account chooses when those runs happen.

If the runs stop: check the rule (`aws events describe-rule --name permaily-verify-production`) and its failed invocations in CloudWatch (`AWS/Events`, `FailedInvocations`), then the token below. `gh workflow run verify-production` starts one by hand. GitHub disables scheduled workflows after 60 days without activity in the repository; re-enable it under Actions if that happens.

## Sigstore changed

A running enclave hands the root secret to a release only if the release's Sigstore bundle verifies with the trust root the enclave uses (the one compiled in, or a later one it was given), in the format `pkg/provenance` reads. If that stops being true and nobody notices in time, no release can be deployed without losing everything stored. The workflow `sigstore-watch` checks both things every day, for the first Rekor and for Rekor v2, (started by EventBridge at 06:53 UTC, and by GitHub's schedule when it fires), and `release.yml` checks each release before it cuts it. A failed run is a deadline, not an incident in production: the enclave that is running keeps working. So that it is not one more red mark, a failed run opens an issue with the label `sigstore-deadline`, assigned to the logins in `.github/sigstore-maintainers`, and every daily run adds a comment that names them again and sends mail to the subscribers of the SNS topic `permaily-sigstore-deadline`, until everything passes, when the issue is closed (`scripts/sigstore-alarm.sh`). When the change is urgent (a new issuer of signing certificates) the issue also gets the label `sigstore-urgent`: pull requests into `develop` fail their `go` check meanwhile, except the one that compiles in the new trust root (`scripts/check-sigstore-deadline.sh`), and the issue stays open after the checks pass, until production runs a release with the new trust root compiled in. Removing that label from the issue lets pull requests through.

- **`trust-root` fails**: Sigstore published a trust root newer than the one compiled in (`pkg/provenance/sigstore_trusted_root.json`); the run prints what changed, and opens the pull request `build: update sigstore's trust root` that compiles it in (`scripts/sigstore-trust-root-pr.sh`; by hand, `scripts/sigstore-trust-root.sh --update`). CI checks that the pull request holds exactly what Sigstore publishes and that the code of `develop` accepts it (`scripts/check-trust-root-change.sh`); read it and merge it, so the next release carries it. How urgent it is depends on what changed. **A log or a timestamp authority** (the usual case: a new instance of Rekor v2, a renewed certificate): nothing to do in production, because `scripts/handoff.sh` gives the running enclave the newest trust root before each handoff, and the enclave takes it with the signatures of Sigstore's keyholders; `scripts/sigstore-trust-root.sh --send` does that alone. **A certificate authority** (`certificateAuthorities` in the difference): the running enclave will not take a new issuer from an update, so a release with the new file has to be published and deployed while the old issuer still signs. If the run says instead that `pkg/provenance` refuses what Sigstore publishes, the TUF metadata changed in a way the code does not read (a new kind of key, for example): fix that first, against go-tuf in `tests/sigstore-differential`, and release it.
- **`canary-rekor2` fails**: a signature recorded today in Sigstore's Rekor v2 log, the way GitHub's attestations will be when they move there, is refused by `pkg/provenance` (or could not be made: read which step failed; the log or the timestamp authority being down is not the alarm). GitHub's attestations are not there yet, so there is time: fix `pkg/provenance` against sigstore-go (`tests/sigstore-differential`) and release it before they move.
- **`canary` fails**: GitHub attested a file today and `pkg/provenance` refuses the bundle. Read the reason in the run. Do not merge into `main` until `pkg/provenance` accepts what GitHub signs and a release with that code is deployed: the fix has to reach production in a release the running enclave still accepts, so it must be signed the old way. If GitHub no longer signs the old way, sign the release from the workflow with a client that still does, while the old log takes entries.
- **`A running enclave would accept this release` fails in `release.yml`**: no release was cut. Either of the two above happened between the daily runs, or the previous release's verifier refuses what this code accepts; the step says which verifier refused.

`permaily-verify -provenance bundle.json -pcrs pcrs.json` is the same check by hand, offline, with the verifier of any release from the first one that has the flag; `scripts/provenance-bundle.sh pcrs.json bundle.json` fetches the bundle. `permaily-verify -trust-update DIR` says whether an enclave of that release would take the trust root in a directory (`scripts/sigstore-trust-root.sh --fetch DIR` fills it). A trust root given to an enclave lasts until it restarts; `/status` and the verifier say which one it uses.

## Who is told about a Sigstore deadline

- **The issue**: the GitHub logins in `.github/sigstore-maintainers`, one per line.
- **The mail**: the subscriptions of the SNS topic, kept in AWS so that no address is in the repository. To add one, and the person confirms from the message AWS sends:

```sh
aws sns subscribe --topic-arn "$(tofu -chdir=deploy/tofu output -raw sigstore_alarm_topic)" --protocol email --notification-endpoint <address>
aws sns list-subscriptions-by-topic --topic-arn "$(tofu -chdir=deploy/tofu output -raw sigstore_alarm_topic)"    # who is subscribed, and who has not confirmed
```

The workflow publishes as the IAM user `permaily-sigstore-alarm`, which can publish to that topic and do nothing else. Its access key is in the repository secrets `SIGSTORE_ALARM_AWS_ACCESS_KEY_ID` and `SIGSTORE_ALARM_AWS_SECRET_ACCESS_KEY`, and the topic in the variable `SIGSTORE_ALARM_TOPIC_ARN`. To replace the key: `aws iam create-access-key --user-name permaily-sigstore-alarm`, set both secrets with `gh secret set`, then `aws iam delete-access-key` for the old one. GitHub's keyless sign-in is not used because the organization the AWS account belongs to forbids identity providers in it.

## Rotate the GitHub dispatch token

The token `permaily-verify-dispatch` is a fine-grained token of the repository's owner, limited to this repository, with Actions read and write and nothing else. It expires on 2027-11-10. EventBridge keeps it in Secrets Manager; a copy is in `~/.config/permaily/github-dispatch-token` (mode 0600).

Create a new one with the same permissions (GitHub > Settings > Developer settings > Fine-grained tokens), store it in that file, then:

```sh
TF_VAR_github_dispatch_token=$(cat ~/.config/permaily/github-dispatch-token) \
  tofu -chdir=deploy/tofu apply -replace=aws_cloudwatch_event_connection.github
```

Check that the next run starts, then delete the old token. Other applies run without the variable.

## Read the test mailbox

```sh
token=$(cat ~/.config/permaily/test-mailbox-token)
curl -s -H "Authorization: Bearer $token" https://api.permaily.com/v1/mailbox                  # address, retention, Gmail codes
curl -s -H "Authorization: Bearer $token" https://api.permaily.com/v1/mailbox/messages         # newest first
curl -s -H "Authorization: Bearer $token" https://api.permaily.com/v1/mailbox/messages/<id>/raw > message.eml
curl -s -X PATCH -H "Authorization: Bearer $token" -d '{"retention_days":30}' https://api.permaily.com/v1/mailbox
```

To send a test message, connect to `inbox.permaily.com:25` with STARTTLS from a server: home connections usually block outbound port 25.

To forward a Gmail account: in Gmail, Settings > Forwarding > Add a forwarding address (the mailbox address), read the confirmation code from `/v1/mailbox`, enter it in Gmail, then enable forwarding.

## Invite someone

Signing up needs an invitation: a code that works once, for thirty days, for one new account.

```sh
scripts/invite.sh        # one; scripts/invite.sh 5 for five, at most 100 at a time
```

It prints each code once (the enclave keeps only its hash) and reads the admin token from `~/.config/permaily/admin-token`. Give the person the code, or the link `https://app.permaily.com/sign-up?invitation=<code>`. Whoever holds the admin token decides who may sign up, and nothing else of an account. An invitation that was not used cannot be taken back; it ends by itself.

## See the accounts

```sh
scripts/accounts.sh               # every account: identifier, address, name, state, passkeys, sessions
scripts/accounts.sh acc_...       # one
```

It asks the enclave (`GET /v1/admin/accounts`, with the admin token from `~/.config/permaily/admin-token`), which answers from the signed records: the table in DynamoDB shows what is stored, this shows what the enclave accepts. It changes nothing. `unreadable` counts accounts in the list whose record could not be read or does not verify: more than zero means the storage was changed by hand or is failing, and the enclave's log says which kind without naming the account.

A person deletes their own account, from Settings; it is removed seven days later, within ten minutes of the moment it is due, or as soon as it is next used. Until then they can sign in and cancel. There is no way to delete an account for someone, and none to bring one back.

## Sign-in with Google, GitHub, Meta and Microsoft

A provider is on once the enclave has been given the application registered there:

```sh
scripts/verify.sh                                          # first: the secret goes to whoever answers
scripts/provider.sh                                        # which are set, with their client ids
scripts/provider.sh set google <client id>                 # asks for the secret without showing it
pass show permaily/github | scripts/provider.sh set github <client id>
scripts/provider.sh remove meta                            # off from then; the links are kept
```

At each provider's console the application is a web application whose only redirect address is `https://app.permaily.com/api/v1/auth/provider/callback`. The secret is kept by the enclave, sealed, and is never answered back: keep your own copy where you keep secrets, never in the repository. It survives upgrades with everything else under the root secret; after starting over (a new deployment) every provider has to be set again.

- **A provider's sign-in fails for everyone.** The enclave's log says `provider` and a fixed reason: the secret or the redirect address is wrong at the provider (`invalid_provider_app`), or the provider cannot be reached (an old `permaily-host` without its hosts in the allowlist). Rotating a secret at the provider means setting it again here.
- **Meta and Microsoft work accounts** always ask the person to confirm an address by mail the first time: neither vouches for the address (ADR 0017).

## Outgoing mail

The enclave sends its own mail (sign-in messages) through SES in `us-east-2`, from the address in `mail_from` of `deploy/enclave.json`, on a domain of its own (`deploy/tofu/ses.tf`: the identity, DKIM, the bounce domain with SPF, DMARC).

- **Nobody receives the sign-in message.** Look at the enclave's logs: `sesmail: SES refused a message` carries the kind of error SES gave (`MessageRejected` while the account is in the sandbox and the recipient is not verified; `AccountSuspendedException`, `SendingPausedException` when AWS stopped the account), and `sesmail: SES could not be reached` means the host does not let the enclave reach `email.us-east-2.amazonaws.com:443` (an old `permaily-host`). The address is never logged. `aws sesv2 get-account` says whether the account is out of the sandbox and how much it may send; `aws sesv2 get-email-identity --email-identity <domain>` says whether DKIM and the bounce domain verified.
- **The sandbox.** A new SES account sends only to verified addresses, 200 messages a day. Ask AWS for production access once, in the console (SES > Account dashboard > Request production access) or with `aws sesv2 put-account-details`: transactional mail, sign-in messages only, to addresses whose owners asked for them. Until then, verify the address of each person who tests (`aws sesv2 create-email-identity --email-identity <address>`; they get a message from AWS to confirm) and delete it afterwards. In the sandbox SES also asks for the permission to send on the identity of the recipient, which is why the role's policy names every identity, with the sender's address as its condition.
- **The verifier fails on `mail`.** The enclave saw a configuration set on the domain or on the address it sends from, which could give whoever set it a copy of every message, sign-in codes included (ADR 0021); or it cannot read those settings. Nobody signs in by mail meanwhile. Find out who did it (CloudTrail: `PutEmailIdentityConfigurationSetAttributes`, `CreateEmailIdentity`, `CreateConfigurationSet`, `PutConfigurationSetArchivingOptions`), remove it (`aws sesv2 put-email-identity-configuration-set-attributes --email-identity <domain>` with no set, or delete the address identity), and look in the archive for what was copied: every account that signed in by mail in that time may have been entered. The enclave starts sending again on the next clean reading, within a minute. The verifier keeps failing until the enclave restarts, because the history is the point; restart it only after writing down what happened, and say so in public. Never add a configuration set to that identity, for metrics or for anything else.
- **Bounces and complaints.** SES stops writing to an address that bounced for good or whose owner marked the mail as spam (the account's suppression list). A person who cannot get the message may be on it: `aws sesv2 get-suppressed-destination --email-address <address>`, and `delete-suppressed-destination` once their mailbox works.

## Certificates and the ACME account

`permaily-verify` checks the CAA records and Certificate Transparency (docs/verification.md). A failure of "certificates outside the enclave" is an incident: find who asked for the certificate it lists.

The CAA records of the enclave hosts name the ACME account the enclave reports:

```sh
curl -s https://api.permaily.com/status | jq -r .tls.acme_account
# deploy/tofu/variables.tf: acme_account_uris = ["<that URL>"]
tofu -chdir=deploy/tofu apply
```

- **First deployment, or a new TLS key:** the account does not exist yet, and a CAA record that names another account makes Let's Encrypt refuse. Set `acme_account_uris = []` (any Let's Encrypt account), deploy, read the account from `/status`, set it and apply.
- **Staging images** (`.#eif-staging`) use another CA and another account, kept in the staging table. To test one on the production names, set `acme_account_uris = []` in `production.auto.tfvars` without committing for the first start, then its account next to production's for the rest of the test, and restore the file afterwards.
- **Replace the TLS key** (suspected leak): delete from the mail table every item of `pk=system` whose `sk` starts with `record#tls-acme-v02.api.letsencrypt.org` (the newest version and the earlier ones), set `acme_account_uris = []`, restart the enclave, then pin the new account. The enclave finds that the version it committed is gone and makes a new identity, as a later version; it never takes an earlier one, so putting the old item back afterwards does nothing (ADR 0016). The root secret is not affected. The verifier will show the new creation time and the new version, and certificates of the old key as older until they expire.
- **A partial `tofu apply`** can leave a host without its CAA record; then the apex record applies, which allows only Amazon, and Let's Encrypt refuses every certificate. Run `tofu plan` after each apply: it must show no changes.

## Planned (F1+)

- Rotate KMS keys (automatic yearly rotation is enabled).
- Restore from backup, with a dated drill.
- Delete a user's data (crypto-shredding).
