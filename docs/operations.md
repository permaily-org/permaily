# Operations

Runbooks for production. Setup from scratch is in [deploy.md](deploy.md). Every command uses `AWS_PROFILE=permaily` and region `us-east-2`.

## Deploy a release

The running enclave hands the root secret to the next release, then the next release is started (ADR 0013). Nothing in AWS is edited to let a release in: each release has a KMS key of its own that only its image can use.

```sh
old=v0.7.0 new=v0.8.0
mkdir -p /tmp/permaily-$new && cd /tmp/permaily-$new
gh release download $new --repo permaily-org/permaily
gh attestation verify permaily.eif --repo permaily-org/permaily
sha256sum -c SHA256SUMS --ignore-missing
mkdir eif && mv permaily.eif pcrs.json eif/

# 1. The key of the new image. deploy/tofu/production.auto.tfvars:
#      release_keys = { "v0.7.0" = "<its PCR0>", "v0.8.0" = "<jq -r .PCR0 eif/pcrs.json>" }
tofu -chdir=deploy/tofu apply          # creates one KMS key and its alias; then `tofu plan` must show no changes

# 2. The running enclave gives the root secret to the new release.
scripts/handoff.sh $old $new

# 3. Start the new image and verify.
scripts/deploy.sh eif permaily-host-linux-arm64
scripts/verify.sh $new                 # from a server with outbound port 25, add -require-smtp
```

Then open the pull request that commits `production.auto.tfvars`.

- **The handoff is refused** (HTTP 422): the answer says why. Nothing was written and the running enclave keeps serving. The usual causes are a key that was not created yet, or files of the wrong release.
- **The new image was started first**: it logs `waiting for the running release to hand over the root secret` and serves nothing. Start the old image again, run the handoff, start the new one.
- **Step 2 can be repeated**; the second time it answers `"created": false`.
- **Verify before step 2** when in doubt about what is answering on `api.permaily.com`: the admin token only lets someone ask for a handoff, which the enclave decides alone, but there is no reason to give it to an impostor.

## Roll back

Deploy the previous release again with `scripts/deploy.sh`. Its record and its key are still there, and everything stored since is sealed under the same root secret, so it reads it. A handoff back to an older release is refused on purpose: a rollback only works while the old release's key exists.

## Delete the key of an old release

An old image can open the root secret for as long as its key exists. Once the new release has run long enough that no rollback is planned, remove the old release from `release_keys` and apply: its key is scheduled for deletion and gone after seven days. This cannot be undone, from the moment it is applied: cancelling the deletion leaves the key disabled, and its policy lets nobody enable it again. Never remove the release that is running.

## A new deployment, or losing everything

An image creates a root secret only when the table has neither a record `root-<its PCR0>` nor the record `deployment` (`pk=system`, `sk=record#deployment`). To start over on purpose, delete those items, the TLS identity (`sk=record#tls-<CA host>`) and the mailbox items; the next image creates a new root secret, a new TLS key and a new ACME account (see Certificates below), and everything stored before is unreadable. There is no way back.

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

The workflow `verify-production` runs `scripts/verify-production.sh` at minutes 7 and 37 of every hour: the latest release's verifier against production, or the previous release's while the latest is not deployed. Its output is in each run's summary, public. A failed run is an incident: read which check failed and since when.

GitHub does not keep that schedule: in the first day it started two runs instead of about forty, hours apart, and each ran as soon as it was started. Treat the runs as an occasional public record, not as a check every half hour; `gh workflow run verify-production` starts one by hand. GitHub also disables scheduled workflows after 60 days without activity in the repository; re-enable it under Actions if that happens.

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
- **Replace the TLS key** (suspected leak): delete the item `pk=system`, `sk=record#tls-acme-v02.api.letsencrypt.org` from the mail table, set `acme_account_uris = []`, restart the enclave, then pin the new account. The root secret is not affected. The verifier will show the new creation time, and certificates of the old key as older until they expire.
- **A partial `tofu apply`** can leave a host without its CAA record; then the apex record applies, which allows only Amazon, and Let's Encrypt refuses every certificate. Run `tofu plan` after each apply: it must show no changes.

## Planned (F1+)

- Rotate KMS keys (automatic yearly rotation is enabled).
- Restore from backup, with a dated drill.
- Delete a user's data (crypto-shredding).
