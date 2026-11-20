# Operations

Runbooks for production. Setup from scratch is in [deploy.md](deploy.md). Every command uses `AWS_PROFILE=permaily` and region `us-east-2`.

## Deploy a release

The running enclave hands the root secret to the next release, then the next release is started (ADR 0013). Nothing in AWS is edited to let a release in: each release has a KMS key of its own that only its image can use.

```sh
old=v0.8.0 new=v0.9.0 dir=/tmp/permaily-v0.9.0
# 0. The image. Releases publish its measurements, not the image (ADR 0023):
#    this builds it from the tag (Nix, or Docker) and refuses unless its
#    measurements are the release's pcrs.json, whose build provenance it checks,
#    as it does the host program's. It leaves the three files in $dir.
scripts/release-image.sh $new $dir

# 1. The key of the new image. deploy/tofu/production.auto.tfvars:
#      release_keys = { "v0.8.0" = "<its PCR0>", "v0.9.0" = "<jq -r .PCR0 $dir/pcrs.json>" }
tofu -chdir=deploy/tofu apply          # creates one KMS key and its alias; then `tofu plan` must show no changes

# 2. The running enclave gives the root secret to the new release. The script
#    first gives it the trust root Sigstore publishes today, which it checks.
scripts/handoff.sh $old $new

# 3. Start the new image and verify.
scripts/deploy.sh $dir $dir/permaily-host-linux-arm64
scripts/verify.sh $new                 # from a server with outbound port 25, add -require-smtp
```

Then delete the key of the previous release (below), and open the pull request that commits `production.auto.tfvars`. Step 1 and deleting a key need `tofu apply`, which only the owner can run; the other operators do the rest ("Operators").

- **The handoff is refused** (HTTP 422): the answer says why. Nothing was written and the running enclave keeps serving. The usual causes are a key that was not created yet, or files of the wrong release.
- **The new image was started first**: it logs `waiting for the running release to hand over the root secret` and serves nothing. Start the old image again, run the handoff, start the new one.
- **Step 2 can be repeated**; the second time it answers `"created": false`.
- **Step 2 verifies first.** `scripts/handoff.sh` runs `permaily-verify` against the `pcrs.json` of `$old` and sends the admin token only to the TLS key it attested ("The operator's scripts verify first", below). When what answers on `api.permaily.com` is not `$old`, it stops and nothing is sent.

## Operators

The account's owner can do everything here. Other operators sign in through the organization's IAM Identity Center, which asks for MFA at every sign-in, with the permission set `permaily-operator`: it references this account's customer-managed policies `permaily-operators-operate` and `permaily-operators-guard` (path `/`, `deploy/tofu/operators.tf`, ADR 0036) and carries nothing else. The organization's service control policy denies IAM groups and console passwords for IAM users in this account, so operators have no IAM user here, and nothing of theirs is in the repository or the state. The repository never names an operator: it is public.

| An operator can | An operator cannot |
|---|---|
| Deploy an image: `scripts/deploy.sh` (artifacts upload, SSM command to the instance), `--logs-only` too; read `tofu output` (the state, read-only) | `tofu apply` of anything: no write to the state, so no release key created (step 1 of "Deploy a release") and no old release's key deleted; the owner does those |
| Run the handoff, the verifier and the operator's scripts (`handoff.sh`, `verify.sh`, `invite.sh`, `accounts.sh`, `provider.sh`, `model-key.sh`): these need the admin token or a secret, which the owner gives or not, not AWS | Create, delete, enable, disable or change any KMS key, its policy, its grants or its aliases (denied explicitly) |
| Open an SSM session on the instance, end or resume their own, and read the logs (`permaily-host`, `/aws/lambda/permaily-drift`) | Change anything in IAM: no user, role, policy, key or provider (denied explicitly) |
| See the instance, the keys and their policies, the alarms, EventBridge's rules and metrics, SES's account and identity, CloudTrail's events, the versions in the anchors bucket; run the drift check now | Delete, reconfigure or unlock the anchors' two Object Lock buckets or their objects (denied explicitly) |
| Look up and remove an address from SES's suppression list | Read or write DynamoDB directly (denied explicitly), read the mail bucket, read secrets, change DNS (Cloudflare is not in AWS), change alarms, rules, subscriptions, the site, or any other resource |

**What SSM on the instance gives.** The instance is untrusted and its role is what the enclave uses for storage: a root shell there can read, write and delete the mail table's items (which include the readable account data: contact addresses, names, dates; never sealed content), read and delete the sealed objects of the mail bucket, write the anchors bucket, and send mail as the service through SES. Deploying needs that shell, so an operator has what the owner has there. What the enclave seals stays out of reach, and what it refuses to accept (an earlier copy of an anchored record) stays refused. Give the role to people the owner would give the instance to.

**What the guard holds.** It holds against anything else the permission set ever carries (an AWS managed policy, an inline policy): an explicit deny wins. It does not hold against whoever administers the organization's IAM Identity Center, who can change the permission set, and who can already do anything in this account through the organization's management account. So the owner checks the role it makes here (below), and again whenever the administrator says the permission set changed.

**Onboarding.** The owner applies `deploy/tofu` first, so that both policies exist as the repository has them; Identity Center refuses to provision a permission set whose customer-managed policy is not in the account. Then whoever administers the organization's IAM Identity Center, from the management account (a profile of theirs, here `org`; Identity Center's own region):

```sh
# 0. Identity Center > Settings > Authentication > Multi-factor authentication: "Every time they sign in",
#    and users without a device must register one at sign-in. It applies to the whole organization.
read -r instance store < <(aws sso-admin list-instances --profile org \
  --query 'Instances[0].[InstanceArn,IdentityStoreId]' --output text)

# 1. The permission set: one hour sessions, nothing but the two policies of this account.
ps=$(aws sso-admin create-permission-set --profile org --instance-arn "$instance" \
  --name permaily-operator --description 'Permaily operators (ADR 0036)' --session-duration PT1H \
  --query PermissionSet.PermissionSetArn --output text)
for name in permaily-operators-operate permaily-operators-guard; do
  aws sso-admin attach-customer-managed-policy-reference-to-permission-set --profile org \
    --instance-arn "$instance" --permission-set-arn "$ps" --customer-managed-policy-reference Name=$name,Path=/
done
#    No AWS managed policy, no inline policy, no permissions boundary.

# 2. The assignment: the operator's own Identity Center user, on the permaily account only.
user=$(aws identitystore get-user-id --profile org --identity-store-id "$store" \
  --alternate-identifier '{"UniqueAttribute":{"AttributePath":"userName","AttributeValue":"<their user name>"}}' \
  --query UserId --output text)
aws sso-admin create-account-assignment --profile org --instance-arn "$instance" --permission-set-arn "$ps" \
  --target-type AWS_ACCOUNT --target-id <permaily account id> --principal-type USER --principal-id "$user"
#    It provisions the role AWSReservedSSO_permaily-operator_<suffix> in the permaily account;
#    aws sso-admin describe-account-assignment-creation-status says when it is done, or why it failed.
```

**The owner checks it** from this account (`AWS_PROFILE=permaily`), after the assignment and after any change to the permission set:

```sh
aws iam list-roles --path-prefix /aws-reserved/sso.amazonaws.com/ --query 'Roles[].RoleName'   # every permission set that reaches this account
role=$(aws iam list-roles --path-prefix /aws-reserved/sso.amazonaws.com/ \
  --query "Roles[?starts_with(RoleName, 'AWSReservedSSO_permaily-operator_')].RoleName" --output text)
aws iam list-attached-role-policies --role-name "$role"   # exactly permaily-operators-operate and permaily-operators-guard, path /
aws iam list-role-policies --role-name "$role"            # nothing: no inline policy
aws iam get-role --role-name "$role" --query 'Role.[MaxSessionDuration,PermissionsBoundary]'   # 3600 and null
aws cloudtrail lookup-events --lookup-attributes AttributeKey=EventName,AttributeValue=AssumeRoleWithSAML \
  --query 'Events[].[EventTime,Username]'                  # who signed in to a role here, and when
```

Who is assigned is visible only in the management account; the owner sees here which roles exist, what each carries, and who signed in. The drift check does not watch the role: it is not in the state.

**The CLI** (the operator, once):

```sh
aws configure sso --profile permaily   # the organization's access portal URL and Identity Center's region,
                                       # the permaily account, the role permaily-operator, region us-east-2
aws sso login --profile permaily       # in the browser, with MFA; again when the session ends
export AWS_PROFILE=permaily            # what the scripts expect; the role's credentials last one hour
aws sts get-caller-identity            # assumed-role/AWSReservedSSO_permaily-operator_<suffix>/<their user name>
```

**Offboarding**: the Identity Center administrator deletes the operator's assignment (`aws sso-admin delete-account-assignment`, same arguments as creating it), and their Identity Center user if they no longer need it. Then the owner rotates what they held outside AWS: the admin token, if they had it, and any secret they were given.

**Cutting every operator off at once** (the owner, from this account alone, when the administrator cannot act): make the guard's default version one that denies everything; the role carries the guard, so every operator's session is refused at its next call.

```sh
arn=arn:aws:iam::<account id>:policy/permaily-operators-guard
aws iam create-policy-version --policy-arn "$arn" --set-as-default \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}'
```

The drift check reports the guard changed until the owner applies `deploy/tofu` again, which restores it (delete the extra version with `aws iam delete-policy-version` once it is not the default; a policy keeps five at most).

**The first apply (2026-11-17)** made an IAM user, `permaily-operator-2`, before the service control policy stopped it; it has no password, key, MFA device or group. The apply that removes it forgets it rather than destroying it (destroying it would delete its login profile, which that policy denies); then the owner deletes it:

```sh
aws iam delete-user --user-name permaily-operator-2
```

## Roll back

Build the previous release's image again (`scripts/release-image.sh <tag> <dir>`) and deploy it with `scripts/deploy.sh`. Its record and its key are still there, and everything stored since is sealed under the same root secret, so it reads it. A handoff back to an older release is refused on purpose: a rollback only works while the old release's key exists.

## Delete the key of an old release

An old image can open the root secret for as long as its key exists: a flaw found in an old release stays a way in, and whoever verifies the service has to trust that release too. So deleting the previous key is the last step of a deployment. While the service has no users, do it once the new release is verified, in the pull request that records the deployment. With users, wait one to three days of normal running that include a restart of the new release, because the old key is also the only way back. Remove the old release from `release_keys` and apply: its key is scheduled for deletion and gone after seven days. This cannot be undone, from the moment it is applied: cancelling the deletion leaves the key disabled, and its policy lets nobody enable it again. Never remove the release that is running.

## A new deployment, or losing everything

An image creates a root secret only when the table has neither a record `root-<its PCR0>` nor the record `deployment` (`pk=system`, `sk=record#deployment`). To start over on purpose, delete those items, the TLS identity (every `sk` that starts with `record#tls-<CA host>`) and the mailbox items; the next image creates a new root secret, a new TLS key and a new ACME account (see Certificates below), and everything stored before is unreadable. There is no way back. The commits in the anchors bucket cannot be deleted and need not be: they belong to the root secret that is gone, and the new deployment's versions go on after them.

The accounts are kept under the same root secret: delete them too, or they stay in the table as records nobody can read, and an address or a provider's account would still point at an account that cannot be opened. That is every item of `pk=system` whose `sk` starts with `record#account-` (the accounts, every version) or `record#provider-apps` (the providers' applications); every item whose `pk` starts with `account#` (sessions and devices), `login#` (the pointers from an address, `login#email#…`, and from a provider's account, `login#<provider>#…`), `invitation#` (the invitations), `pending#` (the sign-ins, address changes, confirmations and passkey ceremonies in progress, with their `used` marks) or `acme-challenge#` (an ACME order left open); and every item of `pk=accounts`, `pk=closing`, `pk=admins` (the admins), `pk=admin-log` (the admin log) and `pk=invitations` (the list of invitations). With those, the records above and the mailbox items gone, nothing is left in the table: emptying it does the same.

If the instance is lost, nothing is: create it again with OpenTofu and deploy the same release. The root secret is in DynamoDB, locked by the release's KMS key.

## Test with staging images

A staging image (`.#eif-staging`) is a deployment of its own on the same instance and names: Let's Encrypt staging, the table `permaily-mail-staging`, and a root secret that only staging images can receive. Production is down while a staging image runs.

```sh
git push origin HEAD:refs/heads/staging/<name>     # the staging-image workflow builds and attests its pcrs.json
scripts/release-image.sh --staging <run id> /tmp/stg-a   # builds the image of that run's commit, refuses other measurements
# release_keys: add "staging-a" = "<its PCR0>", without committing; acme_account_uris: see Certificates below
tofu -chdir=deploy/tofu apply
scripts/deploy.sh /tmp/stg-a /tmp/stg-a/permaily-host-linux-arm64
go run ./cmd/permaily-verify -pcrs /tmp/stg-a/pcrs.json -ca-file letsencrypt-stg-root-x1.pem -skip-certificates
PERMAILY_CA_FILE=letsencrypt-stg-root-x1.pem scripts/handoff.sh /tmp/stg-a /tmp/stg-b    # to a second image, built later
```

`letsencrypt-stg-root-x1.pem` is the root of Let's Encrypt staging, fetched with `curl -sO https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x1.pem` (as in [verification.md](verification.md)). Every operator script needs it against a staging image, as `PERMAILY_CA_FILE=letsencrypt-stg-root-x1.pem`: `scripts/invite.sh`, `scripts/accounts.sh`, `scripts/provider.sh` and `scripts/handoff.sh` (which passes it on to `scripts/sigstore-trust-root.sh --send`). The first three also need `PERMAILY_PCRS=/tmp/stg-a/pcrs.json`, the staging image that runs, to verify it before they send anything; `scripts/handoff.sh` takes it from its first argument.

The staging table keeps its root secret from one test to the next, and only the key of an image that holds it can open it. When the key of the last staging image that ran is gone (removed from `release_keys` and applied: it is disabled at once and deleted after seven days), nothing can open that secret again: start the staging table over as in "A new deployment, or losing everything", on `permaily-mail-staging`, before the next staging image.

The TLS-RPT record (`_smtp._tls.inbox`) is published only while `tls_rpt_address` is set; v0.12.0's deployment removed it, and it is published again since 2026-11-14, pointing at the "TLS reports" mailbox ("Mailboxes per account" below). Leave `tls_rpt_address` as committed during a test, so that `tofu plan` changes no record but the ones the test needs.

Give every test account a contact address that exists and takes mail. A notice sent to one that no longer does bounces, and SES then puts that address on the account's suppression list, which production shares: production stops writing to it too. See it with `AWS_PROFILE=permaily aws sesv2 get-suppressed-destination --region us-east-2 --email-address <address>` and remove it with `AWS_PROFILE=permaily aws sesv2 delete-suppressed-destination --region us-east-2 --email-address <address>`.

The test mailbox's token is gone, so a rehearsal reads mail only through an account that is signed in. Its first one is an account made under the old release and kept signed in through the handoff, or the super admin's, signed in and read by the person who owns its address.

Two staging images built at the same time may be logged in either order, and a handoff only goes forward in that order. Afterwards deploy the production release again, remove the staging keys from `release_keys`, restore the CAA records and delete the `staging/` branches.

## Records that cannot go back

The TLS identity is read by its newest commit in the bucket `permaily-anchors-<account>` (ADR 0016); `/status` and the verifier show its version and until when the commit is locked.

- **`the committed version of the record is gone`** in the enclave's log, for the TLS identity: its item was deleted or replaced. The enclave makes a new identity (see "Replace the TLS key" for the CAA records).
- **The verifier fails with `the store of commits went back`**: the enclave had seen a commit that the bucket no longer answers with. Nobody in the account can remove one, so treat it as an incident: find out what happened to the bucket (CloudTrail, `aws s3api list-object-versions`) before restarting the enclave, which forgets what it saw.
- **The verifier warns that a lock ends within 30 days**: the newest commit of a record is ten years old. Make the record change (for the TLS identity, a renewal does) so that it gets a new commit.
- The enclave's role can list, read and write in that bucket and cannot delete. Nothing needs to be cleaned there: a commit is a few hundred bytes.

## Two enclaves

Not deployed: one enclave runs today. The code is ready for two behind one address (ADR 0017, ADR 0024). What the operator would see:

- **The TLS identity.** One enclave logs `acme: certificate obtained`, the other `acme: certificate installed` within ten minutes. Both serve one key. While one orders, the identity holds its claim; the other waits up to the end of that claim (eleven minutes, more after failed tries) and then orders itself. Nothing needs doing.
- **`the stored identity holds another key`** in one enclave's log: the identity was lost and made again by the other enclave. Restart the enclave that logs it, so both serve the new key, and follow "Replace the TLS key" for the CAA records.
- **The challenges** of an open order are items `pk=acme-challenge#tls-<CA host>` in the table, sealed. They expire with the order and are deleted when it ends; one left behind ends by itself.
- **The test mailbox** is made once by whichever enclave starts first, until the release that retires it (ADR 0026). From that release, mailboxes are made once and changed at the version read whichever enclave a request lands on, every enclave sweeps disconnected mailboxes every ten minutes, and each counts the mail one mailbox takes in its own memory (600 messages an hour, 2 GiB a day each).
- **Sign-ins in progress** (by mail, address changes, confirmations, passkey ceremonies, trips to a provider) are sealed items `pk=pending#…` in the table, so one started through one enclave ends through the other. An enclave ends none started before it started: replacing or restarting one ends the sign-ins started before, and the person asks again.
- **Limits** are counted in each enclave's memory, on purpose, so with two each is at most twice what it says, and **`/status`** is the enclave's that answered (ADR 0017, "Two enclaves at once").

Still needed before two run:

1. A load balancer that passes TCP through on ports 443 and 25 (a Network Load Balancer with TCP listeners, never TLS ones), with health checks that do not end TLS.
2. The parent instances, or one instance with two enclaves, each with its host proxy, behind it.
3. A first release with ADR 0024 must run alone before a second enclave is added: a release from before writes the TLS identity without the claim.
4. A way to roll a release out to two: hand the root secret to the new release once (any enclave of the running release can), then replace the enclaves one at a time. While both releases serve, `permaily-verify -release` fails on connections that land on the older one.
5. A way for a verifier to reach each enclave behind one address (ADR 0024).

## A person's rules

A person's own rules (ADR 0040, composed by its pull request 8; on with the release that carries it) are one strict record per account and epoch (`rules-<account>-<epoch>`), sealed, read and written through an Anchored of their own, cached at most five seconds in each enclave. There is nothing for the operator to do with them, and no route of the operator reaches them.

- **`rules_unreadable`** in `/status`'s `connections.person_rules` counts, for the enclave that answered and since it started, the reads of an account's rules it could not use: a record lost, forked or put back, one that does not open, or a rule in it that does not compile. Each one answered that account's connections `503` with `Retry-After` and gave nothing, the default rule alone included; the enclave logs `accountrules: …` with its kind, never the account. A count that grows is an incident like a record that went back ("Records that cannot go back"): find which kind in the log, and do not restart the enclave before looking, since it forgets what it saw. It does not mean a rule was skipped or that mail left: nothing leaves for an account whose rules cannot be read. A lost or forked record shows besides as a regression named `rules` in `anchors`.
- **`words.calls`** counts the person's words sent to the Responses API by outcome (`proposed`, `unusable`, `too_many`, `unavailable`), for the enclave that answered. Each call is reserved through the model's spend guard in the account's share, as a question of the Decisions API is, and at most 30 an account a day an enclave. Many `unavailable` with the model's key set mean the provider refuses or fails: see the model's `calls` and the guard.
- **Removing by meaning** asks the Decisions API through the same guard; a rule whose sentences cannot be asked removes the whole subject and text, so a model that is off does not let details out.

## The queue of notices

The notices by mail about connections and receipts (ADR 0041: a connection approved, a device added, a connection renewed or ending in a week, a device ended for reuse, receipts found deleted or changed, every device signed out by the operator or an admin) are queued in the table and sent by whichever enclave tries them, through SES behind the watch of ADR 0021, like every message. On with the release that carries ADR 0041's pull request 7.

- **Where.** Entries at `pk=notices#queue` and marks at `pk=notices#sent`, signed; neither holds an address. An entry lasts until its notice is sent, skipped, refused or dropped (about 25 hours at most), a mark 30 days. Nothing to do with them by hand: deleting an entry loses a notice, putting one back sends it again within its day.
- **How.** The first attempt is made at once by the enclave that queued it; every enclave runs a pass a minute, which tries what is due (a minute after a failure, doubling up to an hour), at most two sends a second and eight at once each. A notice is tried for 24 hours from its time, then dropped.
- **The counts.** `/status`'s `mail.notices` (and `permaily-verify`'s output) has, for the enclave that answered and since it started: `queued`, `sent`, `failed` (attempts that will be tried again), `dropped`, `skipped` (an account gone, closing or blocked) and `refused` (an entry that does not verify, or a notice this image does not word). Each enclave counts its own: add both. The log says the same by kind and outcome (`"msg":"notice"` lines with `kind` and `outcome`; `notices: a step failed` with the step), never whom.
- **`failed` rising** is SES refusing or not answering, the watch refusing to send (`mail.failing`, or a configuration set in `mail.state`: see "Outgoing mail"), or an account whose record cannot be read. Notices wait; nothing is lost while they are under a day old. Find the cause in the log (`sesmail:` lines for SES and the watch, `notice` lines with the outcome `failed`) and fix it within the day.
- **`dropped` above zero** (`permaily-verify` warns) means someone was not told of a change to their connections or receipts within a day. Record it as an incident with its window (the log's `notice` lines with the outcome `dropped` say when and which kind, never whom), fix what made the attempts fail, and, if the window is known, consider telling the accounts affected by other means: the activity screen still shows every receipt and each connection's page its state. A dropped notice is not queued again by the same event for 30 days.
- **`refused` above zero** (`permaily-verify` warns) means entries of the queue that do not verify (written by hand, moved, edited or past 2 KiB) or that an older or newer image wrote with a kind this one does not word. Nothing sends them; they expire within a day. Outside a release that changes the notices, treat it as someone writing the table: record an incident and look at who wrote to it (CloudTrail data events, if on).
- **Bounces and complaints** are not read: SES puts an address that bounced for good on the account's suppression list and then counts sends to it as accepted. A staging rehearsal must give every test account an address that exists and takes mail.

## Look at enclave logs

The enclave sends its log lines to the host, which keeps them in the journal with the prefix `enclave:` (since v0.5.0). The CloudWatch agent on the instance ships the journal of `permaily-host` and `permaily-enclave` to the CloudWatch Logs group `permaily-host`, one stream per instance, kept 90 days (ADR 0034), so the lines outlive the instance:

```sh
aws logs tail permaily-host --region us-east-2 --since 1h --filter-pattern '"enclave:"'   # each event is the journal entry as JSON; the line is its MESSAGE
aws logs tail permaily-host --region us-east-2 --follow
```

On the instance itself, for the last minutes or when the agent is not running:

```sh
aws ssm start-session --target "$(tofu -chdir=deploy/tofu output -raw instance_id)"
sudo journalctl -u permaily-host --since "1 hour ago" | grep "enclave:"
```

A line `log lines dropped` means the host was not reading for a while. The lines hold kinds only, never content (docs/threat-model.md, "Logging"); in CloudWatch they are readable by whoever has CloudWatch Logs read permissions in the account, and the instance can add lines and cannot read or delete them.

- **The agent.** `scripts/deploy.sh` installs it on every deploy (`deploy/host/install-logs.sh`: the RPM pinned by version and SHA-256, configured by `deploy/host/cloudwatch-agent.json`), before it restarts anything, so a new instance ships from its first deploy. `scripts/deploy.sh --logs-only` installs it alone and restarts nothing else, for an instance that runs already. A failed install prints `WARNING: the CloudWatch agent was not installed` and does not stop the deploy: the journal keeps the lines, and the next deploy or `--logs-only` tries again.
- **Nothing arrives.** On the instance, `systemctl status amazon-cloudwatch-agent` and `/opt/aws/amazon-cloudwatch-agent/logs/amazon-cloudwatch-agent.log` say why: usually the role's policy (`host-logs` in `deploy/tofu/logs.tf`) or the group missing (the agent cannot create it). The agent does not send what the journal held before it started.
- **Updating the agent.** Change `version` and `sha256` in `deploy/host/install-logs.sh` together, from the RPM of `amazon_linux/arm64/<version>/` in AWS's bucket, then deploy or run `--logs-only`.

For an enclave that does not even start, deploy the same image with `--debug` (`scripts/deploy.sh eif host --debug`) and read its console:

```sh
sudo nitro-cli console --enclave-name permaily
```

Debug mode zeroes the PCRs, so verification fails on purpose and the enclave does not even ask KMS for keys: a debug enclave has no root secret, so no TLS key and no mailbox, and its console says so. Use it to read why an enclave does not start, not to test features. Redeploy without `--debug` right after.

## Let's Encrypt rate limits

At most 5 certificates per week for the same set of names. Since v0.4.0 the enclave keeps its key and certificate sealed, so a restart or an upgrade issues nothing; only a renewal (every 60 days) or a new TLS key does. With two enclaves, one asks and the other serves what it stores, so they make no more orders than one (ADR 0024). If issuance fails with `rateLimited`, wait for the window, and use `.#eif-staging` for tests.

To see that an upgrade issued nothing: v0.10.0 logs `acme: certificate installed` both when it reuses the stored certificate and right after ordering one, so for that release only the certificate's serial shows it (the same before and after: `openssl s_client -connect api.permaily.com:443 -servername api.permaily.com </dev/null | openssl x509 -noout -serial`). Later releases also log `acme: certificate obtained` when they order; an upgrade that logs only `installed` ordered nothing.

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

## The drift check

Once an hour, at minute 23, the Lambda function `permaily-drift` runs `tofu plan -detailed-exitcode` of `deploy/tofu` as it is at the newest commit of `develop` against production (`deploy/tofu/drift.tf`, ADR 0035). When the plan is not clean, or cannot be made, it mails the subscribers of the SNS topic `permaily-sigstore-deadline` (below, "Who is told about a Sigstore deadline"): "production drifted from deploy/tofu" with the resources the plan would change, or "the drift check failed" with the end of its output. A clean plan sends nothing. It only reads: its role can change nothing in the account, it plans without taking the state's lock, its Cloudflare token can only read, and nothing in it applies. Its whole output is in CloudWatch Logs, `/aws/lambda/permaily-drift`, 90 days.

Two CloudWatch alarms mail the same topic when the check cannot: `permaily-drift-errors` (it crashed, timed out, or SNS refused its mail) and `permaily-drift-stopped` (no run for three hours); each mails again when it clears.

- **Production drifted.** Someone or something changed the account or the zone outside OpenTofu, or a change was applied from a branch that is not on `develop` yet. Right after a release's `tofu apply`, the plan differs until the pull request that commits `production.auto.tfvars` is merged: merge it promptly, and expect one or two mails meanwhile. Otherwise read the plan in the log, find out who changed what (CloudTrail), and either apply to restore it or commit the change; never leave it. A CAA record that differs is urgent (deploy/AGENTS.md: a half-applied CAA change stops certificate issuance).
- **The check failed.** The mail ends with the error. `AccessDenied` on a refresh means a resource type was added to `deploy/tofu` without its read permission: add the action to `aws_iam_role_policy.drift_refresh` (read actions only, never one that reads data). `the Cloudflare token could not be read` or `has no value`: the secret below. Errors from GitHub or the registry are transient; the next hour tries again.
- **To silence it** for a planned change: `aws events disable-rule --name permaily-drift --region us-east-2`, and `enable-rule` afterwards.
- **Run it now:** `aws lambda invoke --function-name permaily-drift --region us-east-2 --invocation-type Event /dev/null`.

**Setting it up, once** (and the token again before it expires):

```sh
# 1. The ECR repository, the secret, the role and the log group (the function waits for an image).
tofu -chdir=deploy/tofu apply
# 2. A Cloudflare token that can only read: My Profile > API Tokens > Create, permissions
#    Zone > Zone > Read and Zone > DNS > Read, zone resources: permaily.com only, with an expiry.
#    Put it in the secret without showing it or putting it on a command line (never in the repository or the state):
read -rsp 'Cloudflare token: ' token; echo
printf %s "$token" | aws secretsmanager put-secret-value --region us-east-2 \
  --secret-id permaily/drift/cloudflare-token --secret-string file:///dev/stdin; unset token
# 3. The image: builds deploy/drift and pushes it, then prints its digest.
scripts/drift-image.sh
# 4. deploy/tofu/production.auto.tfvars: drift_image_digest = "sha256:…" (public), then
tofu -chdir=deploy/tofu apply          # creates the function, its schedule and its two alarms
aws lambda invoke --function-name permaily-drift --region us-east-2 /dev/stdout   # a first run: {"result":"done"}, and no mail when production matches
```

Commit `drift_image_digest` in the pull request that follows. Rebuild the image (`scripts/drift-image.sh`, then the digest and an apply) only when `deploy/drift` changes, to move OpenTofu or the base image: the configuration it plans is fetched on each run, with the providers its lock file names. ECR expires only untagged images (a push that failed half way); every pushed image keeps its commit's tag, so the one the function runs never expires. To remove old images by hand, never the one in `drift_image_digest` nor the one the function runs (`aws lambda get-function --function-name permaily-drift --region us-east-2 --query Code.ResolvedImageUri`): list them with `aws ecr describe-images --repository-name permaily-drift --region us-east-2 --query 'imageDetails[].[imageDigest,imageTags[0],imagePushedAt]' --output table`, then `aws ecr batch-delete-image --repository-name permaily-drift --region us-east-2 --image-ids imageDigest=sha256:…` for each old one. To rotate the Cloudflare token, create the new one, put it with step 2, run it once, then delete the old one.

## Port 25, from its instance

GitHub's runners and Lambda cannot connect out on port 25, so the scheduled checks above skip the certificate the enclave presents on SMTP (they warn). The hourly port 25 check does it instead (`deploy/tofu/port25.tf`, `deploy/port25/`, ADR 0038): a `t4g.nano` of its own in the account, `permaily-port25-check`, with nothing in, out only to 443 and 25, and a role that can only publish to the alarms' topic, read its GitHub token and be an SSM node (the managed policy `AmazonSSMManagedInstanceCore`, every SSM parameter of the account denied). It reaches no key, bucket, table or log of production. A systemd timer starts `deploy/port25/run.sh` at minute 41 of every hour, which reads the token and runs `verify-cron.sh`: `verify-production.sh -require-smtp` (copies in `deploy/port25/` of those in `scripts/`, since the drift check fetches only `deploy/`), the latest release's verifier and `pcrs.json` checked against their Sigstore provenance and never run when either does not verify (the previous release's while the latest is not deployed), which fails when port 25 cannot be reached. A passing run sends nothing. EC2 blocks outbound port 25 until AWS lifts the account's block on request, and until then every run would fail, so the instance exists only once `port25_check` is true in `deploy/tofu/production.auto.tfvars`; its security group, role and secret exist before.

A run has a deadline of 50 minutes (`PERMAILY_VERIFY_TIMEOUT`, in seconds): past it the run and everything it started are stopped (TERM, then KILL 30 s later, `PERMAILY_VERIFY_GRACE`) and it is a failure, so a download or a verifier that hangs cannot stop the hourly checks. One run at a time: the next run leaves a run within its deadline be, and treats a lock still held past the deadline, or a lock it cannot take, as a failure. The alert runs after the lock is released, and never holds it, with a deadline of its own of 2 minutes (`PERMAILY_ALERT_TIMEOUT`). systemd stops what is left of a run after 57 minutes. On the instance, the last report is in `/var/lib/permaily-port25/permaily-verify-cron/last.txt` and every run is in the journal of `permaily-port25.service`.

What its mail means (the subscribers of `permaily-sigstore-deadline`, "Who is told about a Sigstore deadline"):

- **"Permaily: the port 25 check failed"**, with the verifier's report: read it as a failed run of `verify-production`, an incident: which check failed and since when. A timeout or a refused connection on port 25 alone means AWS blocks port 25 again or the MX does not answer; check the second from a workstation (`scripts/verify.sh`, which warns when it cannot reach 25), and turn `port25_check` off if it is the block. `could not be read from Secrets Manager`, `does not hold a fine-grained GitHub token`, or GitHub answering 401 (`Bad credentials`) in the report: the token (below) is missing, of the wrong kind, expired or revoked. No verification ran in those.
- **"Permaily: the port 25 check's run failed"**: the run failed without mailing its report (it crashed, systemd stopped it past 57 minutes, or SNS refused its alert), with the unit's journal of the last hour. No result is known for that hour.
- **"Permaily: the port 25 check could not be set up"**: the first boot failed and nothing runs; a download of `gh` that did not end within 10 minutes is one such failure (GitHub slow or down: replace the instance once it answers). Read `/var/log/cloud-init-output.log` over SSM, fix the cause in `deploy/port25`, and replace the instance: `tofu -chdir=deploy/tofu apply -replace='aws_instance.port25[0]'`.
- **The alarm `permaily-port25-down`**: the instance failed its status checks or stopped reporting (stopped, terminated); no check runs. `aws ec2 describe-instance-status --region us-east-2 --include-all-instances --instance-ids "$(tofu -chdir=deploy/tofu output -raw port25_instance)"`; reboot it, or replace it as above. It mails OK when it clears. Right after the instance is created it can mail once before the first metrics arrive, then OK.

Nothing mails when the timer stops firing on a healthy instance, or when SNS refuses both a run's alert and the failure unit's; `systemctl list-timers permaily-port25.timer` on it shows the last and next run. The operators other than the owner cannot open a session on it (their policy names the enclave host only).

**Setting it up, once.** The GitHub token first, then the instance after AWS lifts the block:

```sh
# 1. The security group, the role and the secret (no instance while port25_check is false).
tofu -chdir=deploy/tofu apply
# 2. The token gh signs in with. GitHub > Settings > Developer settings > Personal access tokens >
#    Fine-grained tokens > Generate new token: an expiry, Repository access "Public repositories"
#    (read-only), no permission added. Put it without showing it or putting it on a command line
#    (never in the repository or the state):
read -rsp 'GitHub token: ' token; echo
printf %s "$token" | aws secretsmanager put-secret-value --region us-east-2 \
  --secret-id permaily/port25/github-token --secret-string file:///dev/stdin; unset token
# 3. Once AWS has lifted the account's block of outbound port 25 (us-east-2):
#    deploy/tofu/production.auto.tfvars: port25_check = true, in a pull request, then
tofu -chdir=deploy/tofu plan           # 2 to add: aws_instance.port25[0], aws_cloudwatch_metric_alarm.port25_down[0]
tofu -chdir=deploy/tofu apply          # its first boot runs the check once; a failure mails within minutes
# 4. A look at the first run (a session as ssm-user, which may sudo):
aws ssm start-session --region us-east-2 --target "$(tofu -chdir=deploy/tofu output -raw port25_instance)"
#   sudo systemctl status permaily-port25.service      # "Deactivated successfully", status 0
#   sudo journalctl -u permaily-port25.service         # the run, empty of output when it passed
#   systemctl list-timers permaily-port25.timer        # the next run at minute 41
```

- **The token again**: put a new one with step 2 before the old one expires (each run reads it), then revoke the old one on GitHub. Never a classic token: the run refuses one.
- **Run it now**: in a session, `sudo systemctl start permaily-port25.service` (it waits for the run, a minute or two); or `aws ssm send-command --region us-east-2 --instance-ids "$(tofu -chdir=deploy/tofu output -raw port25_instance)" --document-name AWS-RunShellScript --parameters 'commands=["systemctl start --no-block permaily-port25.service"]'`.
- **To silence it** for a planned change: `sudo systemctl stop permaily-port25.timer` in a session, and `start` afterwards (a reboot starts it again). For longer, or if AWS blocks port 25 again: `port25_check = false` and an apply, which removes the instance and its alarm.
- **Updating it**: the instance runs `deploy/port25/` as it was applied, written into its user data, with its copies of `scripts/verify-cron.sh` and `scripts/verify-production.sh`: change a script in `scripts/` and copy it there in the same commit (`cp scripts/verify-cron.sh scripts/verify-production.sh deploy/port25/`; `scripts/port25.test.ts` fails until they are identical, and `scripts/tofu-validate.sh` checks the configuration in the drift check's layout). An apply after any of them changes replaces the instance (and updates its alarm), and the drift check mails until that apply. `gh` is pinned in `port25.tf` by version and SHA-256: to update it, download the new `gh_<version>_linux_arm64.tar.gz`, check it against the release's checksums file and with `gh attestation verify <archive> --repo cli/cli`, and change both values together. A newer Amazon Linux image is taken only when the instance is replaced.

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

The same topic carries the drift check's mail and its alarms ("The drift check"), and the port 25 check's ("Port 25, from its instance"). The workflow publishes as the IAM user `permaily-sigstore-alarm`, which can publish to that topic and do nothing else. Its access key is in the repository secrets `SIGSTORE_ALARM_AWS_ACCESS_KEY_ID` and `SIGSTORE_ALARM_AWS_SECRET_ACCESS_KEY`, and the topic in the variable `SIGSTORE_ALARM_TOPIC_ARN`. To replace the key: `aws iam create-access-key --user-name permaily-sigstore-alarm`, set both secrets with `gh secret set`, then `aws iam delete-access-key` for the old one. GitHub's keyless sign-in is not used because the organization the AWS account belongs to forbids identity providers in it.

## Rotate the GitHub dispatch token

The token `permaily-verify-dispatch` is a fine-grained token of the repository's owner, limited to this repository, with Actions read and write and nothing else. It expires on 2027-11-10. EventBridge keeps it in Secrets Manager; a copy is in `~/.config/permaily/github-dispatch-token` (mode 0600).

Create a new one with the same permissions (GitHub > Settings > Developer settings > Fine-grained tokens), store it in that file, then:

```sh
TF_VAR_github_dispatch_token=$(cat ~/.config/permaily/github-dispatch-token) \
  tofu -chdir=deploy/tofu apply -replace=aws_cloudwatch_event_connection.github
```

Check that the next run starts, then delete the old token. Other applies run without the variable.

## Read the test mailbox

Retired by v0.12.0, the release that builds mailboxes per account (ADR 0026), in production since 2026-11-14: see the next section. That release has no `/v1/mailbox` and no `-mailbox-*` flags; what follows worked only against v0.11.0 and before, as after a rollback to it. Since 2026-11-17 there is none: F1's items are deleted and the keys of v0.10.0 and v0.11.0 are scheduled for deletion on 2026-11-24.

```sh
token=$(cat ~/.config/permaily/test-mailbox-token)
curl -s -H "Authorization: Bearer $token" https://api.permaily.com/v1/mailbox                  # address, retention, Gmail codes
curl -s -H "Authorization: Bearer $token" https://api.permaily.com/v1/mailbox/messages         # newest first
curl -s -H "Authorization: Bearer $token" https://api.permaily.com/v1/mailbox/messages/<id>/raw > message.eml
curl -s -X PATCH -H "Authorization: Bearer $token" -d '{"retention_days":30}' https://api.permaily.com/v1/mailbox
```

To send a test message, connect to `inbox.permaily.com:25` with STARTTLS from a server: home connections usually block outbound port 25.

To forward a Gmail account: in Gmail, Settings > Forwarding > Add a forwarding address (the mailbox address), read the confirmation code from `/v1/mailbox`, enter it in Gmail, then enable forwarding.

## Mailboxes per account: what changes for the operator

Takes effect with v0.12.0, the release that builds ADR 0026, in production since 2026-11-14.

- **The test mailbox is retired.** The release reads nothing of it: `/v1/mailbox` answers 404, the token reads nothing, and `u-9b393600758a2985dae3@inbox.permaily.com` refuses mail like any unknown address. Turn off any Gmail forwarding to it. Mail is read in the app, by the account that owns the mailbox; the operator's token reads nobody's.
- **TLS-RPT.** The release removes the `_smtp._tls.inbox` record (`tls_rpt_address` is empty by default in `deploy/tofu`). After it is verified: sign in as the super admin, make a mailbox labelled "TLS reports", then set `tls_rpt_address` to its address and apply. Reports arrive there as attachments, read with "Download". This is the one mailbox whose address is public, by design: anyone can send to it, and only its account reads it. Done on 2026-11-14: `tls_rpt_address` is `skr25i9u9s364ra7a6a9@inbox.permaily.com` in `deploy/tofu/production.auto.tfvars`, and the record is published again.
- **Deleting F1's items, and the keys of the releases before**, once the release is verified and a rollback to v0.11.0 is no longer wanted (after it, v0.11.0 would start with a new, empty test mailbox): in the table every item with `pk=mailbox#test` (`sk=mailbox` and every `sk=msg#…`), and in the mail bucket every object under `raw/test/`. Then remove v0.11.0 and older releases from `release_keys` ("Delete the key of an old release"): until their keys are gone they can still open the root secret, and the test mailbox's mail was readable by them and by the token. This step is required, not tidying: the guarantees of mailboxes assume that releases from before them no longer run against the data (ADR 0026, "The store"). Until it is done, a rollback is possible and those guarantees hold only while this release or a later one runs. Done on 2026-11-17: 4 items and 3 objects deleted, none left, and the keys of v0.10.0 and v0.11.0 scheduled for deletion on 2026-11-24; a rollback to v0.11.0 or before is no longer possible.
- **Staging rehearsals** read mail through a mailbox of a signed-in account: sign in to the staging app with an invitation, make one or two mailboxes, send to them from a server with STARTTLS, and read them in the app or with the session's cookie under `/api/v1/mailboxes`.
- **Pausing a mailbox** for abuse: `scripts/accounts.sh mailbox find <address>`, then `mailbox pause <id>` and later `mailbox resume <id>`; ten changes of one mailbox in thirty days at most. Its person is told by mail; nothing is deleted. The operator cannot disconnect a mailbox or read mail ("See and manage the accounts", "Mailboxes").
- **A regression marked `fork` in `/status`**, under the name `mailbox`, means two commits of different digests at one version of a mailbox's record or an account's list: someone wrote to the anchors bucket. That mailbox stops for good, for the lock period (ten years): it refuses mail, nobody opens it, its person sees "Can't be read" and cannot disconnect it, and it keeps its slot. Nothing makes it readable again; tell its person through support. One marked `lost` means the newest commit of a mailbox's record or an account's list names a value the table no longer holds: someone deleted or changed the item in DynamoDB. A lost mailbox refuses mail and nobody opens it; it shows as `unreadable` in `scripts/accounts.sh mailboxes` and in the admin panel, with its identifier and when it was added, and its person can disconnect it, which frees its slot. A lost account list stops delivery to every mailbox of that account. One marked `unlocked` means S3 holds a commit without the lock the enclave wrote it with: report it to AWS and look at the bucket's configuration. Accounts and the TLS identity are not strict records: a fork there shows as a regression without the mark, as before (ADR 0016), and the TLS identity's is handled as ADR 0024 says.

## The operator's scripts verify first

`scripts/invite.sh`, `scripts/accounts.sh`, `scripts/provider.sh`, `scripts/handoff.sh` and `scripts/sigstore-trust-root.sh --send` send the admin token, and `provider.sh set` a provider's secret. Before they send anything, each runs `permaily-verify` against the enclave, and then sends only on connections with the TLS key it attested (`curl --pinnedpubkey`, the CA and hostname checked too, no redirects; ADR 0029). A server with another valid certificate for `api.permaily.com` gets nothing. They need to know what the enclave must measure:

```sh
export PERMAILY_RELEASE=v0.14.0                        # the running release's tag: its verifier is built from the commit its provenance names
export PERMAILY_PCRS=/tmp/permaily-v0.14.0/pcrs.json   # and its pcrs.json from scripts/release-image.sh <tag> <dir>;
                                                       # without it, the release's, authenticated by its provenance (trusts CI)
```

The verifier must be the running release's: it checks the Sigstore trust root the enclave reports against the one compiled into itself, so one built from a later checkout refuses an older release, and above all during a trust-root update or a handoff. With `PERMAILY_RELEASE` (or `handoff.sh`'s `<running>` tag) the scripts never build from the tag's name: they authenticate the release's `pcrs.json` with `gh attestation verify` (the repository `permaily-org/permaily`, the release workflow, `main`), take the source commit from that attestation, check that the tag at `github.com/permaily-org/permaily` names the same commit, fetch that commit by its id from `github.com/permaily-org/permaily` into a private repository in the run's temporary directory (never from `origin` or the clone: its replacement refs, hooks, configuration and `GIT_*` variables are not used), and build it with go in a fixed environment (no `GOFLAGS`, `go.env` file or workspace, `go.mod`'s toolchain line, no cgo, `-trimpath`); the log names the commit. Without `PERMAILY_PCRS`, the `pcrs.json` whose provenance they verified is the very file the verifier checks, not a second download. A compromised go, git or computer is not defended against. That needs git 2.32 or later. That needs `gh` and GitHub: without them, or for a tag with no release, nothing is sent, and you set `PERMAILY_VERIFY=<permaily-verify binary>`, which is used as it is whenever it is set. With only `PERMAILY_PCRS`, as for a staging image, it is built from the checkout as it is, uncommitted changes included; the script says so. Nothing checks that the checkout is the running image's source: you trust it as such, as you would a `PERMAILY_VERIFY` you give. A trusted verifier of another release fails closed (it refuses, and nothing is sent); code you have not reviewed does not. When the enclave does not verify, or no measurements are given, the script says why and that nothing was sent. A failure unrelated to the token, such as the SES state, stops them too: read it as you would a failed `scripts/verify.sh`. What the enclave answers is printed field by field, never whole, and under `bash -x` neither the token nor a secret is traced.

`PERMAILY_API` is an origin and nothing else: `https://<host>` or `https://<host>:<port>`, with or without a `/` at the end, in lowercase, with no user, password, path, query, fragment or percent-encoding; anything else is refused before anything runs. `PERMAILY_DEV_UNVERIFIED=1` (only `1`; any other value is refused) is for an enclave in dev mode on your machine: it skips the verification, is refused for any name but `localhost`, `127.0.0.1`, `[::1]`, a `.localhost` or a `.test` name, and connects to `127.0.0.1` whatever DNS or a proxy variable says.

## Invite someone

Signing up needs an invitation: a code that works once, for thirty days, for one new account.

```sh
scripts/invite.sh        # one; scripts/invite.sh 5 for five, at most 100 at a time
```

It prints each code once, on standard output (the enclave keeps only its hash), and reads the admin token from `~/.config/permaily/admin-token`. Give the person the code, or the link `https://app.permaily.com/sign-up?invitation=<code>`. It also prints, on standard error and in the same order, the identifier of each invitation (`inv_...`): what it is listed and revoked by. An identifier is not the code, and the code cannot be had from it.

```sh
scripts/invite.sh list                # the ones not used yet: identifier, when made, when it ends
scripts/invite.sh revoke inv_...      # makes one of no use; asks for a "yes" unless --yes follows
```

An invitation that was used is deleted when it is used, so the list does not show it: the list is what is still waiting. One that is revoked is gone the same way, and a sign-up that was started with it fails. The invitations made by v0.10.0 and earlier have no identifier: they are not listed, cannot be revoked, and end by themselves thirty days after they were made.

## See and manage the accounts

```sh
scripts/accounts.sh                       # every account: identifier, address, name, state, passkeys, sessions, mailboxes
scripts/accounts.sh acc_...               # one
scripts/accounts.sh find ana@example.org  # the account of exactly that address
scripts/accounts.sh stats                 # how many, by state, and their mailboxes
```

It asks the enclave (`GET /v1/admin/accounts`, with the admin token from `~/.config/permaily/admin-token`), which answers from the signed records: the table in DynamoDB shows what is stored, this shows what the enclave accepts. `unreadable` counts accounts in the list whose record could not be read or does not verify: more than zero means the storage was changed by hand or is failing, and the enclave's log says which kind without naming the account. `stats` reads the whole list a page at a time and counts: active, closing (asked by the person or by the operator), blocked, and a few more.

```sh
scripts/accounts.sh block <account>            # nobody signs in to it, and every device is signed out
scripts/accounts.sh unblock <account>
scripts/accounts.sh delete <account>           # removed in seven days, unless that is cancelled
scripts/accounts.sh cancel <account>           # cancels a deletion the operator asked for
scripts/accounts.sh close-sessions <account>   # signs every device out, and nothing else
```

`<account>` is an identifier or the account's address. Each of these reads the account from the enclave first, says what it will do and to which account (address and identifier), and asks for a `yes`; `--yes` after the account skips the question.

- **Block** when an account is being misused or was taken over. Its person is told by mail, and when they sign in and prove who they are they are told the account is blocked and to write to support@permaily.com. Nothing says so before that proof, so nobody else learns of it. Blocking is apart from deleting: a blocked account stays as it is until it is unblocked or removed.
- **Delete** is what a person does from Settings, asked for them: the account is closing for seven days and then removed with everything in it, within ten minutes of the moment it is due or as soon as it is next used. The person is told by mail, sees who asked, and can cancel from the app unless the account is blocked. There is no way to bring a removed account back.
- **Cancel** only a deletion the operator asked for. One the person asked for is theirs to cancel: the enclave answers `not_cancellable` and changes nothing, and the script says so before asking. To keep the account of a blocked person who asked to delete it, unblock it: they sign in and cancel.
- **Close sessions** when a device was lost: the person signs in again.

What cannot be done, on purpose (ADR 0017): change an account's contact address, add or remove a passkey or a provider, turn "passkey only" off, or sign in as someone. Each would be a way into the account. So someone who lost their mail, their passkeys and their recovery codes cannot be given the account back: support can block it and delete it, and the person signs up again. A person changes their own address in Settings, with a code sent to the new one.

Everything done here is counted by the enclave, by its kind and never by account, on `/status` (`admin`), which `permaily-verify` prints twice an hour for anyone, and is logged by its kind. A restart starts the count again.

### Mailboxes

```sh
scripts/accounts.sh mailboxes <account>            # its mailboxes: identifier, state, dates, retention, whether Google confirmed it
scripts/accounts.sh mailbox find <address>         # the mailbox an address leads to, and whose it is
scripts/accounts.sh mailbox pause mbx_...          # it refuses new mail until resumed; asks for a "yes" unless --yes follows
scripts/accounts.sh mailbox resume mbx_...         # it accepts mail again
```

They ask the enclave (`GET /v1/admin/accounts/{id}/mailboxes`, `GET /v1/admin/mailboxes?address=`, `POST /v1/admin/mailboxes/{id}/pause` and `/resume`, with the admin token), which answers from each mailbox's signed record (ADR 0026, "What the operator and admins see and do"). What it shows of a mailbox is its identifier, its account, its state (waiting, receiving, paused or unreadable), when it was added, how long it keeps mail, since when it receives, when it was paused and whether Google confirmed the Gmail account behind it. Never its address, its label, that Gmail account or its mail: the enclave gives none of them to the operator or to an admin. A mailbox whose record is lost or forked ("An unreadable mailbox" in ADR 0026) is shown as unreadable with its identifier, its account (from the signed index) and when it was added, and nothing else: it accepts no mail, nobody can open it, and it cannot be paused or resumed (`not_found`).

- **Find** a mailbox when a report of abuse names its address: the enclave hashes the address as SMTP does, and answers the mailbox it leads to or nothing, the same nothing for an address never made, one disconnected and one that is not an address. It is one exact address a request and tells only whose an address you already know is: what bounds it as a way to test addresses is that an address is 100 random bits, which anyone can test against SMTP as well. The script never prints the address back.
- **Pause** a mailbox that is flooded, or whose mail harms the service: it refuses new mail at `RCPT TO` with the same answer as an unknown address, keeps everything it holds, and its person can still read, delete and disconnect. They are told by mail ("Permaily paused one of your mailboxes"), and again when it is resumed. A mailbox is paused or resumed ten times in thirty days at most (`too_many_requests` after that), counted in its record, so the limit holds with any number of enclaves.
- **What cannot be done, on purpose**: read a mailbox, see its address or label, disconnect it, delete its mail, change its label or retention, or move it to another account. Disconnecting destroys what is the person's; pausing stops the harm.

Pausing and resuming are counted on `/status` as `mailbox_paused` and `mailbox_resumed`, never which mailbox, and logged by kind alone. Through the admin panel they also need a fresh proof, a mailbox of an admin's account is the super admin's alone to pause, and each is written in the admin log with the account and the mailbox by identifier.

## The admin panel

Admins manage the same things from the web app, signed in to their own accounts, under `/api/v1/admin/` on the app's host (ADR 0022). The super admin is the account whose contact address is `super_admin` in `deploy/enclave.json` (`hello@permaily.com`); it makes and removes the other admins there. The token and the scripts above stay, for the first invitation and for automation, and cannot make admins.

1. Sign up `hello@permaily.com` with an invitation from `scripts/invite.sh`, from the mailbox of that address.
2. Add a passkey and turn "passkey only" on at once: until then whoever reads that mailbox is the super admin, and the panel refuses everything (`passkey_only_needed`).
3. Make the other admins from the panel. Each is told by mail, and needs "passkey only" too.

Every change in the panel asks the admin to confirm with a passkey when their last proof is older than ten minutes. An admin who is not the super admin acts on no admin's account and does not touch the providers' applications. Nobody blocks or deletes the super admin's account there. Every action is in the panel's log (400 days, read by admins) and counted on `/status` with the token's. Whoever writes to the table can delete entries of the log; the counts on `/status` are what is public.

## Sign-in with Google, GitHub, Meta and Microsoft

A provider is on once the enclave has been given the application registered there:

```sh
export PERMAILY_PCRS=<dir>/pcrs.json                       # the running release: verified before the secret is sent
scripts/provider.sh                                        # which are set, with their client ids
scripts/provider.sh set google <client id>                 # asks for the secret without showing it
pass show permaily/github | scripts/provider.sh set github <client id>
scripts/provider.sh remove meta                            # off from then; the links are kept
```

At each provider's console the application is a web application whose only redirect address is `https://app.permaily.com/api/v1/auth/provider/callback`. The secret is kept by the enclave, sealed, and is never answered back: keep your own copy where you keep secrets, never in the repository. It survives upgrades with everything else under the root secret; after starting over (a new deployment) every provider has to be set again.

- **A provider's sign-in fails for everyone.** The enclave's log says `provider` and a fixed reason: the secret or the redirect address is wrong at the provider (`invalid_provider_app`), or the provider cannot be reached (an old `permaily-host` without its hosts in the allowlist). Rotating a secret at the provider means setting it again here.
- **Meta and Microsoft work accounts** always ask the person to confirm an address by mail the first time: neither vouches for the address (ADR 0017).

## The model (OpenAI)

From the release that composes it (ADR 0028), the enclave asks OpenAI's Decisions API (`gpt-6-luna` at `https://api.openai.com/v1/decisions`, prompt cache off on every request) what exact conditions cannot settle, for the owner's previews of a formula (`ask_model`), and only for an account that agreed to the model's notice. It needs a key, which only the operator gives; without one the model is off and what needs it stays in `needs_model`.

**The OpenAI project.** The key is of a project of its own, `permaily-enclave`, in Permaily's OpenAI organisation, and of nothing else: give it to no other program, workstation or service. The eval of ADR 0028 (`go test -tags modeleval`) uses another key of another project, `permaily-eval`, on the owner's workstation. In OpenAI's dashboard, for `permaily-enclave`:

1. Restrict the key to the Decisions endpoint where OpenAI's permissions allow it.
2. Set the project's monthly budget to **50 USD** and turn its **hard enforcement** on, so OpenAI refuses calls past it (`project_spend_limit_exceeded`); set its notification at **25 USD**, which reaches the owner by OpenAI's mail. The enclave's own guard holds the same 50 USD for every enclave together, before any request leaves, and is what `/status` shows; the project's limit is the stop for what the enclave cannot count (the key used elsewhere, or a bug in its count). Neither the enclave nor a verifier can see whether this setting is on: record it, with a screenshot of the dashboard, in docs/acceptance.md (A35) whenever it changes.
3. Put the organisation's identifier (`org-…`) and the project's (`proj_…`) in `deploy/enclave.json`, `model.organization` and `model.project`, before the release: the enclave sends them as `OpenAI-Organization` and `OpenAI-Project`, so a key of another organisation or project is refused by OpenAI rather than used. While they are empty the requests pin neither, `/status` leaves them out, and `permaily-verify` warns on every run. They are in the image (PCR0): changing them is a release.

**Set, replace or remove the key**, over the connection pinned to the attested key ("The operator's scripts verify first"):

```sh
export PERMAILY_RELEASE=<running tag> PERMAILY_PCRS=<dir>/pcrs.json
scripts/model-key.sh                             # whether a key is set, since when, and its fingerprint
pass show permaily/openai | scripts/model-key.sh set   # or: scripts/model-key.sh set, which asks without showing it
printf %s "$key" | sha256sum | cut -c1-16        # your copy's fingerprint, to compare
scripts/model-key.sh remove                      # the model is off from then
```

The key is sealed in the record `model-key` and never answered back, logged or shown. An enclave started without a key logs that the canary did not run and holds the canary's lease for ten minutes, so after the key is set the day's first check completes at a later tick, up to twenty minutes after the start (v0.15.0's deployment: started 03:20:56, key set 03:21:32, `same` at 03:40:59); `/status` says `failed` meanwhile, and `permaily-verify` warns. A key set or removed through one enclave is used, or stops being used, by every other within a minute. To rotate it: make the new key in the project, `set` it, check the fingerprint, then revoke the old one in OpenAI's dashboard. After starting over (a new deployment) the key has to be set again.

**The limits** are compiled into the image (`deploy/enclave.json`, `model`): 50 USD a month for every enclave together (`month_limit_usd`), an alarm when a day's reservations pass 10 USD (`day_alarm_usd`), and 0.50 USD a day for one account in one enclave (`account_day_usd`). A staging image (`.#eif-staging`) lowers the month's limit to 1 USD and the day's alarm to 0.20 USD (`staging.model`), so A35 can reach both. `/status` shows the limits and the guard's state (`ok`, `month_limit`, `provider_limit`, `key_refused`, `unreadable`), never an amount spent: the bill is at OpenAI.

**The alarm** is mail through SES to the super admin's address (`super_admin`), at most once a day for each kind, sent by whichever enclave runs, and again every minute until SES takes it. Its words are fixed and hold no amount tied to an account and nothing of anyone's mail. What to do:

- **The model's spend passed the day's alarm**: nothing is held back yet. Look at the project's usage at OpenAI and at `/status`'s `calls`: a person's previews are bounded by their share, so a jump means many accounts or a bug.
- **The model's monthly limit was reached**: nothing new is asked until the month ends (UTC); kept answers are still used. Only a release changes the limit.
- **OpenAI's limit was reached**: the project's budget or the organisation's credit ran out. The enclave probes every ten minutes and asks again once one is answered. If the enclave's own guard is not at its limit, someone used the key elsewhere, or OpenAI counts what the enclave does not: rotate the key and find out which.
- **OpenAI refused the model's key**: it was revoked, belongs to another project than `model.project`, or the project was deleted. Set a key of `permaily-enclave`; the enclave tries a new key at once.
- **The model's canary saw an answer move**: see "When the canary changes".
- **No canary check completed for two days**: look at the enclave's logs for `canary` lines (`decisions: the canary's check did not complete`), at the guard on `/status` (a check is reserved like any call, and is not sent with no key or at a limit), and at whether the host still allows `api.openai.com:443`.

The enclave's log says, by kind alone, when an alarm could not be added or sent (`decisions: an alarm could not be added; it is kept to add again`): the mail system's error is never logged, as it carries addresses. A lost or forked record of the model (`/status` `anchors.regressions` with the name `model`, without versions) stops what needs it: the spend's for the rest of the month, the key's until it is set again.

**When the canary changes.** Once a day an enclave asks 156 fixed synthetic statements (`internal/decide/canary.json`) and compares the answers with what was measured for the prompt version in force. `changed` on `/status` (and a warning of `permaily-verify`) says an answer moved past 0.02, changed a classification, or another model answered; the service is not paused and kept answers do not change. Then:

1. Run the eval on the whole corpus with the eval project's key, never the enclave's: `OPENAI_API_KEY=… PERMAILY_EVAL_ORG=org-… PERMAILY_EVAL_PROJECT=proj_… go test -tags modeleval -run TestA10 -v -timeout 30m ./internal/decide/openai/` (ADR 0028, A10; `eval_test.go` says the rest).
2. If its thresholds still hold, re-measure the canary and release it as the new baseline (`canary.json`'s values, so its SHA-256 and the canary's state are new); if they do not, release a new prompt version, or an image without the model.
3. Write down in ADR 0028 what the eval found and what was released.

`permaily-verify` also warns when the canary's baseline is not the SHA-256 of the `canary.json` of the commit the enclave runs: the image compares answers with another release's.

## Connections (apps and agents)

From the release that composes them (ADR 0030 and 0031), apps and agents a person allows read some of their mail: the API at `https://api.permaily.com/v1`, MCP at `https://mcp.permaily.com/mcp`, the authorization server on `api.` (issuer `https://api.permaily.com`, its metadata at `/.well-known/oauth-authorization-server`) and the consent screen on `app.` (`/oauth/authorize`). There is no operator route for connections: the operator cannot see, make, renew or revoke one, and the admin panel shows none. The person sees, revokes and renews theirs in the app.

- **Vendors' documents.** The enclave fetches each vendor's public client document (and, for `private_key_jwt` clients such as ChatGPT, its key set) through the host's second proxy, `permaily-host -vendor-egress-port` (vsock 8004 by default, which the unit does not change): HTTPS to any public name on 443 and nothing else, HTTP/2 only. The host logs `vendor egress: tunnel`, `vendor egress: refused` (with its reason) and `vendor egress: failed`, naming a vendor only when it is on the verified list (`claude.ai`, `chatgpt.com`), else `other`. A vendor whose host has no HTTP/2 cannot connect (the owner's decision 11); the person sees that the app's details could not be read.
- **The api and mcp hosts speak HTTP/1.1.** They offer only `http/1.1` by ALPN, the app's host `h2` too: Go's HTTP/2 server gives no honest signal of when an answer's last byte left, so mail over HTTP/2 is refused (`503`, the log's `no_abort`). A client that names the app's host by SNI and another by `Host` over HTTP/2 gets that 503 for mail; nothing needs doing.
- **The hourly pass.** Every enclave ends connections past their end, fences the pending ones a crash left, removes ended ones 30 days after they ended with their families' records, finishes the fence of an account being deleted, and removes dead records of client assertions; once when it starts, then every hour. `connections: the hourly pass did not finish` (by kind alone) means a part of it failed; the next pass tries again, and nothing it does is what refuses a token (a connection past its end is refused whether or not the pass ran).
- **The model off, nothing leaves.** While the model cannot be asked for an account (no key, or a limit: "The model (OpenAI)"), every search, read and attachment of its connections answers `503 model_unavailable`, decisions already made included, and `GET /v1/connection` and MCP's `connection_info` say `delivery: model_unavailable`. Removing the model's key therefore stops every connection within the minute an enclave keeps the key it read; setting it again resumes them.
- **What `/status` counts.** `connections.refused` is what this enclave refused (and a few events, `allowed`, `denied`, `token_issued`, `revoked_person`, `revoked_vendor`) by fixed reason since it started. A rise of `invalid_token` or `wrong_resource` is clients holding old tokens (a revocation, a restart of their own), of `vendor_unreachable` a vendor's host or the vendor egress failing, of `assertion` or `assertion_replay` a `private_key_jwt` client misbehaving or a replay, of `model_unavailable` the model being off or at a limit, of `rate_limited` a client past its limits (20 searches and 120 reads a minute a connection, 600 unauthenticated token requests a minute in all, each enclave), and of `decisions_full` mailboxes whose day of rule decisions has no room (held, undecided: nothing leaves for them; see ADR 0031, "Bounds"). `cut` and `too_late` are answers stopped by a revocation, an end or the storage failing while they were prepared or sent.
- **Records that cannot go back.** A connection (`connection-<id>`), an account's list of them (`connections-<account>-<epoch>`), a device's token family (`oauth-family-<id>-<epoch>`) and a record of client assertions (`oauth-assertions-…`) are strict anchored records read and written through one `Anchored` of their own; `/status` names a regression of any of them `connection`, without its version. Treat it as an incident, as for mailboxes ("Records that cannot go back"): the connections it names refuse their tokens until it reads again.
- **Entries.** Requests, pushed requests, codes and tokens are sealed items `pk=oauth#request#…`, `oauth#pushed#…`, `oauth#code#…`, `oauth#access#…`, `oauth#refresh#…`, each named by a hash of its secret; receipts are sealed items `pk=receipts#<account>`, never updated, deleted with the account. Deleting any of them refuses its token or loses a receipt and decides nothing else; from the release that chains receipts, deleting a receipt shows ("The chain of receipts" below).
- **The staging check** of a release that changes connections runs on a staging image ("Test with staging images") before the release: Claude Code and Codex connect, search and read, the model's key is removed and set again, and a revocation through one enclave cuts an answer being sent by the other within five seconds (ADR 0030, "The owner's guided session").

## The chain of receipts

From the release that chains receipts (ADR 0037), no answer of a connection leaves until its receipt is stored and named by a committed link of its account's chain. A link is a strict record made once (`receipt-link-<account>-<n>`), its value in the account's own partition under `lnk_<n>`, its commit in the anchors bucket under `commits/g/<hash of the account's chain>/<n>/`, every link of an account under one prefix. Three more series of the same kind are the deployment's: the catalogue of chains (`receipt-chains-<k>`, values in `pk=receipts-catalogue`), the audit's passes (`receipt-audit-<p>`, `pk=receipts-audit`) and the record of incidents (`receipt-incidents-<i>`, `pk=receipts-incidents`). They are read and written through one strict `Anchored` of their own, and `/status` names a regression of any of them `receipt link`, never which. The audit's lease is the item `pk=receipts-audit-lease`, `sk=lease`, sealed.

- **The daily audit.** Every enclave asks every hour whether a pass is due (a day after the newest began) and, when it is, runs it under the lease, at most an hour; the other enclave runs nothing while the lease holds, and takes it over an hour and ten minutes later if the run that held it stopped. A pass reads every chain the catalogue names, links every receipt left without a link for ten minutes to three days, begins the chain of an account with receipts and none, records what it found broken as incidents, then writes itself as a strict record that both enclaves report. Erasing or forging the lease costs a pass twice and nothing else. The log says `receipts: a pass of the audit did not finish` (kind `audit`) and `receipts: the record of incidents was not read` (kind `incidents`), never an account, a number or a count; the next hour tries again.
- **`permaily-verify` fails because no pass finished in the last 48 hours.** Look at the log for `kind: audit` since the last pass. A storage that refuses (the table or the bucket), a chain whose junk keeps a run from finishing within its hour, an account record that `Removing` cannot read (below), or an enclave that keeps restarting all keep passes from finishing. A new deployment fails this check until its first pass, minutes after it starts.
- **`permaily-verify` fails because the record of incidents holds an entry**, or a pass or the enclave that answered found something broken, or a link is a regression: **an incident**, and it does not wait for `permaily-verify` to pass, which it will not do again until accepting a known incident is decided (ADR 0037, a later decision). 1. Say publicly that the verifier fails and why: its message names the kinds and counts, never an account. 2. Find how the storage was written: the table's and the bucket's access, CloudTrail (`DeleteItem`, `PutItem`, `BatchWriteItem` on the table; `PutObject`, `DeleteObject` on the anchors bucket), who held which role and when. 3. Keep the deployment running: the chain goes on past a break, and stopping it would let whoever deletes one item stop every connection. 4. Do not restart the enclaves to make it pass, and do not put anything back: a restart forgets nothing that was recorded, and a receipt put back changes no entry. The operator cannot learn which accounts or receipts were affected: the record is sealed, and each person sees it in their own walk (pull request 3); telling them is step 9's notice.
- **`/status` shows `receipts.broken` above zero** without `incidents.recorded` growing: this enclave found something and its record failed (the storage refusing writes). It is recorded when found again; the verifier fails meanwhile.
- **`receipts.audit.recovered` or `unlinked` above zero** (warnings): receipts were stored and their links failed (a write that failed with the storage, answered 503 with nothing released), or an earlier release is still writing. Expect `recovered` at the second pass after the release that chains receipts: adoption takes only receipts older than a minute (`AdoptAfter`), so those the earlier release wrote in its last minute before the first pass wait for the next pass (L22, ADR 0037's "Cost"). Recovered ones are linked now. Unlinked ones are older than three days and can be deleted unseen; they mean the audit did not run for three days, or an earlier release wrote them. Delete the keys of earlier releases ("Delete the key of an old release") as soon as the rollback window allows: an earlier release writes receipts with no link.
- **The person's receipts** (`GET /api/v1/receipts` and `/api/v1/receipts/export`, pull request 3). A walk or an export records what it finds broken before it shows it, so a person's page can raise `incidents.recorded`; the operator sees the count, never the account. `receipts_unreadable` (503) is an account whose commits under the anchors bucket hold more at one link than a page lists (junk under `commits/g/<hash>/`): whoever writes the bucket did it, and it is an incident as above, the account's connections answered 503 too. `busy` (503, `Retry-After`) is a page that begins at a link whose commit's lock ended and that took more than one request's reading to authenticate: asking again goes on from where the last stopped, and it does not happen before 2036 in production. A person who reports that `permaily-verify -receipts` fails on an export they made: `not a consistent export` names what is wrong with the file (edited, cut short, pages missing) or a noted head it does not hold; `not intact` means the export reports a break (`BREAK` lines), which either the storage made, and then the deployment recorded it as an incident and `/status` shows it, or someone edited the file to look that way, which the offline check cannot tell apart. The chain itself is read by their walk and the audit, so ask nothing of their receipts and check `/status`.
- **`receipts.audit.expired` above zero** (warning): links whose commits' ten-year lock ended. Not before 2036 in production; the decision on locks is due before then.
- **What the operator sees.** How many links an account has and when, from the bucket and the table's keys, as the table already showed; after an account is deleted, its links' commits and its catalogue entry stay until their lock ends (ADR 0037, the owner's choice 1). Nothing of what a receipt said.

### Before the release that chains receipts

Two checks, run by the orchestrator or the operator before release-pr ([releasing.md](releasing.md), "Gates a release waits for"). Neither changes anything.

**1. The accounts' records fit the strict reader's limits.** The audit asks `Removing` of an account before it writes for it, and `Removing` reads the account record's whole history strictly: more than 10,000 object versions and delete markers under its commits makes it fail for ever, and more than 500 not yet read by that enclave makes the read fail and go on at the next, so a pass fails each time until the enclave has read them all (and again after a restart). Every account with receipts and no chain is asked at the first pass. Measure every record's history in the anchors bucket, read-only, with the operator's profile:

```sh
AWS_PROFILE=permaily aws s3api list-object-versions --region us-east-2 --bucket permaily-anchors-<account id> --prefix commits/ \
  --query '{v: Versions[].Key, d: DeleteMarkers[].Key}' --output json \
  | jq -r '((.v // []) + (.d // []))[] | select(startswith("commits/g/") | not) | split("/")[1]' \
  | sort | uniq -c | sort -rn | awk '{ n++; if ($1 > 500) over500++; if ($1 > 10000) over10k++ } NR == 1 { max = $1 } END { printf "records %d, most objects %d, over 500: %d, over 10000: %d\n", n, max, over500, over10k }'
```

Each line of `uniq -c` is one record (its name hashed: an account's record is `account-<id>`, and the hash says nothing of whose). It passes when the most objects is at most 500. Between 500 and 10,000, the first passes fail until each enclave has read them (one more failed pass per 500), which is acceptable only when said in the release notes; over 10,000 is a blocker: that account's audit writes never happen. The CLI pages on its own; the bucket holds a few thousand objects today.

**2. L22, the staging check.** It measures what ADR 0037's "Cost" estimates, on a staging image ("Test with staging images") of the commit that carries pull requests 2 and 3, and writes the measurements into ADR 0037 as distributions. One enclave runs on staging ("Two enclaves": not deployed), so contention between two enclaves is measured in dev mode (L5) and again once two run.

1. Deploy a staging image of the release before the chain, make an account, connect Claude Code or Codex, and let it search and read a dozen times: receipts with no chain.
2. Deploy the staging image with the chain (handoff). The first request of that account enters it in the catalogue and adopts those receipts: time it. The audit's first pass runs when the image starts and usually does both first (L22 on 2026-11-18: within a second of the start); then the walk's adopted links' `at` times the adoption, and the first request is a cold one.
3. Latency: 200 searches and 200 reads, one at a time, timed by the client from the request to the last byte; the first after a restart of the enclave (cold: the head found by listing) and the rest (warm). Then the same with 20 at once (full and partial groups of links). Compare with the same runs against step 1's image: the difference is the chain's, by p50 and p99, cold and warm.
4. Requests per receipt: for the length of step 3, count the S3 requests on the anchors bucket (CloudTrail data events on it, or a CloudWatch request metrics filter on `commits/g/`, each turned on for the test and off after) and the table's consumed read and write units (CloudWatch, `permaily-mail-staging`); divide by the receipts written.
5. A restart: the head is found again, and the next request adds one link.
6. A head lost: delete the newest `lnk_` item of the account's partition by hand (`aws dynamodb delete-item` on `permaily-mail-staging`) and restart the enclave; the next request writes a link with `break: lost` and is answered. Without the restart the appender still holds the head it made, so the next link names the lost one by `prev`, and only the walk shows it `lost`.
7. A receipt deleted by hand (an `rcp_` item of the same partition): the person's walk (`GET /api/v1/receipts` signed in as that account) shows it `missing` and `/status`'s `incidents.recorded` rises at the enclave's next read of the record, within ten minutes (a walk records what it finds before it shows it; the read is every ten minutes and after a pass); the next pass, a day after the newest began (a restart does not hasten it), counts it in `receipts.audit.broken`; `permaily-verify` fails with the kind and count, never the account.
8. Write the measurements, the image's commit and the date into ADR 0037's "Cost", and start the staging table over afterwards (step 7 leaves it failing for good).


## Outgoing mail

The enclave sends its own mail (sign-in messages) through SES in `us-east-2`, from the address in `mail_from` of `deploy/enclave.json`, on a domain of its own (`deploy/tofu/ses.tf`: the identity, DKIM, the bounce domain with SPF, DMARC).

- **Nobody receives the sign-in message.** Look at the enclave's logs: `sesmail: SES refused a message` carries the kind of error SES gave (`MessageRejected` while the account is in the sandbox and the recipient is not verified; `AccountSuspendedException`, `SendingPausedException` when AWS stopped the account), and `sesmail: SES could not be reached` means the host does not let the enclave reach `email.us-east-2.amazonaws.com:443` (an old `permaily-host`). The address is never logged. `aws sesv2 get-account` says whether the account is out of the sandbox and how much it may send; `aws sesv2 get-email-identity --email-identity <domain>` says whether DKIM and the bounce domain verified.
- **The sandbox.** The account left it on 2026-11-11 (50,000 messages a day, 14 a second; `aws sesv2 get-account` says what it is now). A new SES account, as after moving to another AWS account or region, sends only to verified addresses, 200 messages a day. Ask AWS for production access once, in the console (SES > Account dashboard > Request production access) or with `aws sesv2 put-account-details`: transactional mail, sign-in messages only, to addresses whose owners asked for them. Until then, verify the address of each person who tests (`aws sesv2 create-email-identity --email-identity <address>`; they get a message from AWS to confirm) and delete it afterwards. In the sandbox SES also asks for the permission to send on the identity of the recipient, which is why the role's policy names every identity, with the sender's address as its condition.
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
