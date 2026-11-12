# deploy/AGENTS.md: infrastructure

OpenTofu for AWS (`us-east-2`, profile `permaily`; `us-east-1` only for the site's certificate) and Cloudflare DNS, plus the parent instance setup in `host/`. Procedure: [../docs/deploy.md](../docs/deploy.md).

## Rules

- Always pass `AWS_PROFILE=permaily`; there is no default profile.
- The state is in S3, encrypted by OpenTofu (ADR 0014); never keep or commit a state file of `tofu/`. `tofu/bootstrap` is the exception: it only creates the state bucket and its key, and its state is committed.
- `tofu/production.auto.tfvars` says what production runs and is committed; update it in the pull request that follows a release. Never put a secret in it.
- The CAA records of the enclave hosts name the enclave's ACME account (`acme_account_uris`); `permaily-verify` compares them with what the enclave reports. Changing the TLS key or the CA needs the steps in docs/operations.md, or Let's Encrypt refuses every certificate.
- After every `tofu apply`, `tofu plan` must show no changes: a half-applied CAA change stops certificate issuance.
- No certificate for `*.permaily.com`, ever: it would be valid for the enclave hosts with a key outside the enclave (ADR 0012). Keep Cloudflare Universal SSL disabled, keep `issuewild ";"` at the apex, and give the site an exact-name certificate.
- DNS records must stay `proxied = false`. `main.tf` owns the enclave subdomains; `site.tf` owns the public site (apex, `www`, `beta`) on Amplify. Do not touch other records (mail).
- The parent instance is untrusted: no user data, secrets or business logic there. Only nitro-cli, `permaily-host` and SSM.
- Do not open ports beyond what the enclave serves (443 and 25). Declare them inline in `aws_security_group.enclave_host`: a separate rule resource is removed on the next apply.
- Never deploy with `--debug` to a deployment meant to be verified.
- `tofu destroy` removes everything except the mail table (deletion protection), the mail bucket (not empty) and the anchors bucket (its objects are locked for ten years); never destroy mail data by accident.
- `anchors.tf`: buckets with Object Lock for the commits of the records that cannot go back (ADR 0016). They must be created with Object Lock; the enclave sets each commit's retention itself, so do not add a default one, and never give the enclave's role a delete permission there.
- `releases.tf`: one KMS key per enclave image (`release_keys`), with a policy that only that image can use and nobody can change (ADR 0013). The enclave and `permaily-verify` both refuse a key that `kmspolicy.Sealed` does not accept: before changing the policy of new keys, change that function with a test. A key cannot be edited after it is created, and deleting the key of the running image loses everything stored.
- The image to deploy is built from the release's tag with `scripts/release-image.sh`, which refuses other measurements than the attested `pcrs.json`; releases do not publish it (ADR 0023).
- Upgrades edit nothing in AWS except creating the new release's key: the running enclave hands the root secret over (`scripts/handoff.sh`, docs/operations.md). Never remove the running release from `release_keys`.
- Never create grants on any key, and never try to change a release key's policy: the enclave remembers every state it saw and the verifier fails on a bad one until the enclave restarts.
- `verify.tf`: EventBridge rules that start the `verify-production` workflow every half hour and `sigstore-watch` once a day, with a GitHub token that can only start workflows of this repository. Keep the token's permissions at Actions read and write; never give it contents or administration. The workflow keeps its own schedule on purpose.
- `alarms.tf`: the SNS topic the `sigstore-watch` workflow sends mail through, and the IAM user that can only publish to it. Subscriptions and the user's access key are made by hand (docs/operations.md): no address and no key in the repository or in the state.
- `ses.tf`: the SES identity the enclave sends its own mail from, and its DNS records. Never give that identity, or the address in `mail_from`, a configuration set, and never verify that address as an identity of its own: either could give a copy of sign-in messages to whoever did it, the enclave stops sending when it sees one, and `permaily-verify` fails from then on until the enclave restarts (ADR 0021). The role may send from that address only.
- `staging.tf`: storage of the staging deployment. Staging images (`.#eif-staging`) never see production data; their KMS keys go in `release_keys` without committing, for the duration of a test.
- `permaily-verify` checks the MX and `_mta-sts` records. When the MTA-STS policy changes (`mta_sts_mode` in `../enclave.json`), change the `id` of the `_mta-sts` record in `mail.tf` in the same change.
- `../enclave.json` is the enclave's runtime configuration, compiled into the image (PCR0). Changing it changes the image.
- `super_admin` in `../enclave.json` names the super admin of the admin panel: whichever account has that contact address (ADR 0022). Changing it changes who that is, and needs an ADR. Its account must be "passkey only" before anything else: until then whoever reads that mailbox is the super admin.
