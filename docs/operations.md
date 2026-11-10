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

A production enclave has no console. To debug, deploy the same image with `--debug` (`scripts/deploy.sh eif host --debug`), then:

```sh
aws ssm start-session --target "$(tofu -chdir=deploy/tofu output -raw instance_id)"
sudo nitro-cli console --enclave-name permaily
```

Debug mode zeroes the PCRs, so verification fails on purpose. Redeploy without `--debug` right after.

## Let's Encrypt rate limits

At most 5 certificates per week for the same set of names. If issuance fails with `rateLimited`, stop restarting the enclave, use `.#eif-staging` for tests, and wait for the window. F1 removes this by sealing the certificate key with KMS.

## Rotate the Cloudflare token

The OpenTofu token `permaily-opentofu-dns` (DNS edit and zone read on `permaily.com`) expires on 2027-02-07. Create a new one with the same permissions, store it in `~/.config/permaily/cloudflare-api-token` (mode 0600), run `tofu plan` to check it, then delete the old token.

## Verify production

```sh
scripts/verify.sh "$(gh release view --repo permaily-org/permaily --json tagName -q .tagName)"
```

Run it after every deploy and record failures as incidents.

## Deploy a release with mail (F1)

Every release has a new PCR0, and the mail KMS key only serves allowlisted images. Before deploying a release:

```sh
pcr0=$(jq -r .PCR0 eif/pcrs.json)
# deploy/tofu/terraform.tfvars (not committed):
#   enclave_pcr0_allowlist = ["<current PCR0>", "<new PCR0>"]
tofu -chdir=deploy/tofu apply
```

After the new enclave serves mail, remove the old PCR0 and apply again. The first time, also set `mail_dns_enabled = true` to publish the MX, MTA-STS and TLS-RPT records.

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

## Planned (F1+)

- Rotate KMS keys (automatic yearly rotation is enabled).
- Restore from backup, with a dated drill.
- Delete a user's data (crypto-shredding).
