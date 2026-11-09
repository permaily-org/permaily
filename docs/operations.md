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

Deploy the previous tag with the same steps. The enclave keeps no state in F0, so a rollback is only a redeploy. Each restart issues a new certificate (see Let's Encrypt below).

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

## Planned (F1+)

- Rotate KMS keys and update the key policy with the PCR0 of a new release.
- Restore from backup, with a dated drill.
- Delete a user's data (crypto-shredding).
