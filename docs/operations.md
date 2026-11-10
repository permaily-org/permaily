# Operations

Runbooks for production. Setup from scratch is in [deploy.md](deploy.md). Every command uses `AWS_PROFILE=permaily` and region `us-east-2`.

## Deploy a release

```sh
tag=v0.1.0
mkdir -p /tmp/permaily-$tag && cd /tmp/permaily-$tag
gh release download $tag --repo permaily-org/permaily
gh attestation verify permaily.eif --repo permaily-org/permaily
sha256sum -c SHA256SUMS --ignore-missing
mkdir eif && mv permaily.eif pcrs.json eif/
scripts/deploy.sh eif permaily-host-linux-arm64
scripts/verify.sh $tag
```

## Roll back

Deploy the previous tag with the same steps, with its PCR0 back in `enclave_pcr0_allowlist` (see below); otherwise KMS denies it the mailbox keys. Mail lives in DynamoDB and S3, not in the enclave, so a rollback keeps it. Each restart issues a new certificate (see Let's Encrypt below).

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

Debug mode zeroes the PCRs, so verification fails on purpose and KMS refuses the keys: a debug enclave has no TLS key and no mailbox, and its console shows the refusals. Use it to read why an enclave does not start, not to test features. Redeploy without `--debug` right after.

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

## Deploy a release with mail (F1)

Every release has a new PCR0, and the mail KMS key serves only the image in `enclave_pcr0_allowlist`. The policy changes from the old image to the new one in a single step, so two images are never allowed at once:

```sh
jq -r .PCR0 eif/pcrs.json
# deploy/tofu/production.auto.tfvars: enclave_pcr0_allowlist = ["<new PCR0>"]
tofu -chdir=deploy/tofu apply
scripts/deploy.sh eif permaily-host-linux-arm64
scripts/verify.sh <tag>          # from a server with outbound port 25, add -require-smtp
```

Between the `apply` and the restart the old enclave keeps serving with the keys it already has; it only cannot open a mailbox it had not opened yet. Then open the pull request that commits `production.auto.tfvars`.

If the new enclave does not come up, put the old PCR0 back, apply and deploy the old release. `-kms-also-allow` exists for a setup that must run two images for a while; a single instance never needs it.

The enclave reports every state of the policy it has seen since it started, and the verifier checks them all (docs/verification.md). So do not test by editing the policy of the production key: the change stays in the history until the enclave restarts, and `verify-production` will fail for as long.

## Scheduled verification

The workflow `verify-production` runs `scripts/verify-production.sh` at minutes 7 and 37 of every hour: the latest release's verifier against production, or the previous release's while the latest is not deployed. Its output is in each run's summary, public. A failed run is an incident: read which check failed and since when.

GitHub disables scheduled workflows after 60 days without activity in the repository; re-enable it under Actions if that happens.

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
- **Staging images** (`.#eif-staging`) use another CA and so another sealed record and another account. To test one on the production names, add its account with `-var 'acme_account_uris=[...]'`, or in `production.auto.tfvars` without committing, for the duration of the test.
- **Replace the TLS key** (suspected leak): delete the item `pk=system`, `sk=record#tls-acme-v02.api.letsencrypt.org` from the mail table, set `acme_account_uris = []`, restart the enclave, then pin the new account. The verifier will show the new creation time, and certificates of the old key as older until they expire.
- **A partial `tofu apply`** can leave a host without its CAA record; then the apex record applies, which allows only Amazon, and Let's Encrypt refuses every certificate. Run `tofu plan` after each apply: it must show no changes.

## Planned (F1+)

- Rotate KMS keys (automatic yearly rotation is enabled).
- Restore from backup, with a dated drill.
- Delete a user's data (crypto-shredding).
