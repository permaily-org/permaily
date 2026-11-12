# Threat model

## What verification proves

That the service answering on the Permaily hosts runs an enclave image built from a specific public commit, and that your TLS connection terminates inside that enclave. It does not prove that the code is correct or free of bugs; it proves that the code you can read is the code that runs.

## Trusted

- **AWS Nitro hardware and hypervisor**, the Nitro Security Module and the AWS Nitro Enclaves root certificate (pinned by fingerprint in `pkg/attestation`).
- **AWS prebuilt enclave kernel, init and nsm.ko** from aws-nitro-enclaves-cli (pinned by hash in `nix/eif.nix`).
- **The source code and its pinned dependencies**: Go modules (`go.sum`, `vendorHash`), npm packages (`bun.lock` and the `node_modules` hash), nixpkgs (`flake.lock`).
- **Let's Encrypt** for issuing certificates to the enclave's key, and the public WebPKI for the verifier's TLS checks and for the enclave's connections to KMS.
- **Sigstore's public instance** (Fulcio, the Rekor transparency logs and, for entries of Rekor v2, the timestamp authority that says when a signature existed; all pinned in `pkg/provenance`; three of the five keyholders of Sigstore's TUF root can give a running enclave other logs and timestamp authorities, never another issuer of certificates, ADR 0013) and **GitHub Actions** (its token issuer, its hosted runners, and the rule that `main` only changes through reviewed merges): together they say which images are public releases, and a running enclave hands the root secret only to those.
- In later phases: the LLM provider used for classification (model and prompts are in the image).

## Not trusted

- **The parent EC2 instance and its operator.** It sees only ciphertext and destination hostnames. It can drop, delay or replay traffic, and stop the service; it cannot read or alter content without the verifier detecting it.
- **DNS (Cloudflare) and the network.** A hijacked record points clients elsewhere; the verifier detects it because no other server can present a TLS key bound into a valid attestation. The zone is signed with DNSSEC, so a resolver that validates refuses records forged on the way; whoever controls the Cloudflare account controls the zone all the same.
- **Anything the parent supplies at runtime.** The enclave takes no configuration from the parent; hosts and the ACME directory are compiled into the image.

## Mechanisms

| Threat | Mitigation |
|---|---|
| Service runs different code | PCR0-2 in a fresh, AWS-signed attestation must equal the PCRs reproduced from source |
| Debug mode (console access, PCRs zero) | the verifier rejects all-zero PCRs |
| Replay of an old attestation | random 32-byte nonce per verification, 5-minute freshness window |
| TLS terminated outside the enclave (MITM, parent, CDN) | attestation `user_data = sha256(SPKI)` of the certificate on the same connection |
| Certificate for the hosts issued to someone else | detected per connection by the binding above, so only for people who verify: a browser accepts any valid certificate. CAA restricts the hosts to Let's Encrypt and forbids wildcards for the domain; Cloudflare Universal SSL is off and the site has an exact-name certificate (ADR 0012). CAA names the enclave's ACME account, whose key is sealed, and `permaily-verify` reads Certificate Transparency and fails on a certificate for the hosts with another key issued since the enclave's key exists (`pkg/certwatch`). Four wildcards issued before 2026-11-10 with keys at Cloudflare and AWS stay valid until 2027-05-26 at the latest. DNS is the operator's, so CAA can change between checks; Certificate Transparency shows it afterwards |
| Build tampering in CI | anyone can rebuild; CI builds on x86_64 and arm64 and compares; releases carry Sigstore provenance |
| Supply chain of dependencies | all inputs pinned by hash; review dependency changes |

## Known limits (F0)

- The TLS key and the ACME account key are created once inside an enclave and stored sealed under the root secret (ADR 0012, ADR 0013), so restarts and upgrades keep the same key, certificate and account. Only what can get the root secret can open them, like stored mail.
- The enclave keeps nothing itself: what it stores is sealed and lives in DynamoDB and S3, which the operator can delete, not read. Putting back an earlier copy is refused for the records that go through `store.Anchored` (ADR 0016): today the TLS identity. The mailbox's state and the messages can still be put back to an earlier copy, or removed; they get the same protection, and an audit log, with the data model.
- Availability is not protected: the operator can always take the service down.
- Metadata visible to the operator: connection timing, sizes, destination hostnames of egress.

## Egress allowlist

The host proxy only tunnels to:

| Destination | Why |
|---|---|
| `acme-v02.api.letsencrypt.org:443` | certificate issuance |
| `acme-staging-v02.api.letsencrypt.org:443` | test deployments |
| `kms.us-east-2.amazonaws.com:443` | mailbox data keys (Recipient with attestation) |
| `dynamodb.us-east-2.amazonaws.com:443` | sealed mailbox and message records |
| `s3.us-east-2.amazonaws.com:443` | sealed raw messages (path-style requests) |
| `cloudflare-dns.com:443` | DNS-over-HTTPS for DKIM lookups, so the host cannot forge DNS answers |
| `email.us-east-2.amazonaws.com:443` | SES: the service's own mail (sign-in messages), and reading who else could get a copy of it |
| `oauth2.googleapis.com:443`, `www.googleapis.com:443` | Signing in with Google: the code for an ID token, and the keys it is signed with |
| `github.com:443`, `api.github.com:443` | Signing in with GitHub: the code for an access token, who holds it and their verified address |
| `graph.facebook.com:443` | Signing in with Meta: the code for an access token, and who holds it |
| `login.microsoftonline.com:443` | Signing in with Microsoft: the code for an ID token, and the keys it is signed with |

## Mail (F1)

- **Root secret.** Everything stored is sealed under data keys, and every data key is wrapped under one root secret created inside an enclave (ADR 0013). Outside enclaves the root secret exists only in records, one per image, each locked by a KMS key that belongs to that image alone.
- **Keys nobody in the account can open or change.** The policy of an image's KMS key lets only an enclave that proves that image (PCR0) decrypt, and gives nobody `kms:PutKeyPolicy` or `kms:CreateGrant`, so KMS refuses to change it: account administrators were denied `PutKeyPolicy`, `CreateGrant`, `Decrypt`, `Encrypt`, `DisableKey`, `ReplicateKey` and more on such a key (checked 2026-11-10). Anyone in the account can create a data key under it, which gives them nothing: see the next point.
- **Only public releases receive the root secret.** An image accepts a record only with an AWS-signed attestation document from the enclave that wrote it, which must have run the same image or a public release, proved by that release's Sigstore bundle. The running enclave writes a record only for a release that was published, in the public transparency log, after itself, and whose key passes the check above. So neither a private image nor a record made by the operator gets the root secret, whoever asks.
- **Pinned account.** The AWS account and region are compiled into the image and every key must be in them; every KMS answer must name the key that was asked (`keys.ErrKeyMismatch`). The host supplies the AWS credentials (its own instance role); credentials for another account cannot make the enclave use a key elsewhere.
- **Encryption.** Message metadata, raw message and mailbox state are sealed with AES-256-GCM plus a key commitment, bound to mailbox, message id and purpose (`internal/envelope`).
- **What the operator sees.** Message sizes and arrival times, retention period, the number of messages, and (until accounts exist) the single test mailbox. Not senders, subjects, bodies or attachments.
- **Transport.** SMTP STARTTLS terminates in the enclave with the attested certificate, and the MX refuses `MAIL` on a connection without TLS. MTA-STS (`mode: enforce`) tells senders to deliver only to `inbox.permaily.com` with a valid certificate; TLS-RPT reports go to the test mailbox. `permaily-verify` checks the policy (served with the attested key), the `_mta-sts` and MX records, and that port 25 answers with the attested key (ADR 0011).
- **Gap: senders without MTA-STS, and DNS.** A sender that does not implement MTA-STS can be made to deliver in clear to whoever answers on port 25. DNS is the operator's: removing the `_mta-sts` record stops new senders from applying the policy (cached policies last a week); the verifier fails when it is missing.
- **Port 25 between manual checks.** The scheduled verification cannot reach port 25 (GitHub's runners block it), so the certificate served there is only checked by hand, from a server, with `-require-smtp`. Decided on 2026-11-11 to leave it so: a certificate for the host with a key outside the enclave shows in the certificate check, which runs every half hour, and what stays unwatched is the gap above, for senders without MTA-STS.
- **Senders.** Any sender may deliver to the mailbox (decision for F1); DKIM results are recorded with each message. A Gmail forwarding confirmation is only marked verified when it carries a valid `google.com` DKIM signature.
- **Who can use the key is public.** The enclave reads the policy, the grants and the properties of its image's key from KMS and serves them on `/status`; `permaily-verify` accepts that only over the attested TLS key and applies the same rule the enclave does (`kmspolicy.Sealed`): this image only, no way to change the policy from the account, no grants, key material that never left KMS.
- **The policy between checks.** AWS documents that its Support can restore the account owner's access to a key that nobody can manage. So the enclave reads the policy and the grants of its key once a minute and reports every state it has seen since it started; the verifier requires a reading made just now and checks every state, and a public job runs the verifier every half hour (ADR 0013). That job is started from the operator's AWS account, with a GitHub token that can start, cancel and delete workflow runs of this repository and nothing else; so the operator chooses when it runs, and GitHub's own schedule, which the operator does not time, starts it a few times a day as well. What remains: a change made while the enclave is cut off from KMS shows only as a gap, and restarting the enclave starts the history again (the start time is printed).
- **What remains.** An older release can still open its own record until its key is deleted, so a flaw in an old image stays a way in for that long (docs/operations.md). Whoever controls Sigstore's signing or GitHub's runners for this repository can publish an image that is accepted; it would be public in the transparency log and in the repository. The account's owner, with AWS Support, could regain control of a key and then open the root secret; it takes a support case and shows in the enclave's report from then on. The operator can still delete keys or records, which destroys data and reads nothing.
- **The old mail key.** Releases up to v0.6.0 wrapped data keys with `alias/permaily-mail`, whose policy administrators could replace. v0.7.0 moved those data keys under the root secret, the key was retired on 2026-11-10 with a policy that lets nobody use it, and it is scheduled for deletion.

## Accounts (F2)

- **The storage is not a way into an account.** An account's record is readable and signed with a key that comes from the root secret, and read through `store.Anchored`; sessions and invitations are signed with where they are stored and kept by the hash of their secret; a sign-in that was started, with its count of wrong codes, lives only in the enclave's memory (ADR 0017). So whoever runs DynamoDB and S3 can read who has an account, and can delete accounts, and cannot add a way to sign in, bring a removed one back, or make a session.
- **What the operator sees.** Contact addresses, names, dates, which ways each account signs in with, how many sessions it has and when each was used. That is the promise: account data is seen, mail is not.
- **Sign-in by mail trusts three parties.** The person's own mail provider, which can read the message; AWS, which carries it (SES); and, in part, whoever administers the AWS account, as the next point says. An account in "passkey only" mode (not built yet) trusts none of them.
- **A copy of the mail the enclave sends.** The message with the code and the link goes from the enclave to SES over TLS, so the host does not see it. An administrator of the AWS account can make SES keep a copy of everything sent: a configuration set tied to an archive, set as the default of the identity. With the copy of a sign-in message they would enter the account. The enclave never names a configuration set, so it reads from SES whether one applies to the domain it sends from, or to the address (whose settings would win over the domain's): before a message unless it read within the last five seconds, after each message, and once a minute. It sends only while the answer is that none applies and the answer can be had. Every state it saw since it started is on `/status`, and `permaily-verify`, which runs every half hour, fails on one that was ever not clean (ADR 0021).
- **What that leaves.** It is noticed, not prevented: nothing in AWS stops an administrator from changing those settings. A configuration set put in place and taken away between two readings, in the moment a message leaves, is not seen; the readings around each message leave about a second for it, up to six when many messages leave at once, where a watch once a minute would leave a minute. Restarting the enclave starts the history again (the start time is printed). A way to copy outgoing mail that AWS adds later, or one this does not read, is not covered. AWS itself reads the message in any case.
- **Other settings of SES are not read.** A notification topic on the identity can carry the headers of a message, so the subject of a sign-in message is fixed text and never holds the code or the link. A message that bounces is sent back to the sender's address, whose mail the operator could receive by adding an MX record: that copy exists only for a message that did not reach the person. Sending authorization lets others send, not read.
- **A link sent by someone else.** Someone can start a sign-in for their own account and send its link to another person, to have them act inside an account that is not theirs (connect their mailbox to it, for one). So opening the link signs nobody in: the page asks the enclave whose sign-in it is (`POST /api/v1/auth/email/look`, which ends nothing), shows the address, says not to go on with a link someone else sent, and signs in only when the person says so. What is left is a person who reads another's address and goes on all the same.
- **Mail in Permaily's name proves nothing.** The parent instance holds the role the enclave sends with, so the operator can send mail from the same address without the enclave; DNS is the operator's too. A message from Permaily is not proof that the enclave wrote it. What a sign-in message leads to is: the link opens `app.permaily.com`, where the verifier's check applies, and the code is only good in the browser that asked for it.
- **Passkeys.** What the browser answers is checked by this repository's own code (`internal/webauthn`): the challenge the enclave gave, the app's origin and host, the person present and verified, and the signature of the key kept in the account's record. No attestation is trusted, so which device holds a key is not known, and the signature counter is not kept, so a copied authenticator is not noticed by it. A ceremony lives in memory, works once and for five minutes. Signing in with a passkey asks for no address and its failures are all the same, so it tells nobody who has an account. Trusted here, besides the code: the browser and the authenticator of the person, and for synced passkeys whoever syncs them.
- **Two enclaves at once.** One runs today. A change to an account's record is written only if nothing was committed for the record since it was read, so two enclaves do not undo each other's change (a passkey removed stays removed, a recovery code works once) and neither brings back an account the other removed. That holds between enclaves. Whoever writes to the bucket of commits can still, in the moment between two changes, add a commit where one is and lose one of the two; the enclave whose commit does not count says so in `/status` (ADR 0016). What lives in an enclave's memory is not shared: the limits on tries count in each enclave, so they are as many times higher as there are enclaves (ADR 0017).
- **"Passkey only" and recovery codes.** With the switch on, mail no longer signs in, so neither the person's mail provider, nor SES, nor whoever administers the AWS account is a way into the account. The ten recovery codes are kept by their hash in the anchored record: one that was used does not come back, and whoever writes to the storage can neither add one nor turn the switch off. Five wrong codes an hour for an address, counted in memory, which a restart sets back.
- **Signing in with a provider** (Google, GitHub, Meta, Microsoft). The provider is trusted for the accounts linked to it: one that lies about who signed in takes them, and so does one that says an address is verified when it is not, for an account with that address that never linked it. What the provider says reaches the enclave over TLS that ends inside it, and Google's and Microsoft's is signed besides, checked by this repository's own code (`internal/idp`). The answer is ended only in the browser that started it (a cookie for that browser alone), so one carried to another person signs nobody in. An address is believed only where the provider vouches for it: Google and GitHub when they say verified, Microsoft only for a verified domain or a personal account, Meta never; otherwise it is confirmed by a message with a code. Nothing of the provider is kept but which one and which subject. "Passkey only" turns providers off for that account.
- **The providers' secrets** are given to the enclave by the operator with the admin token and kept sealed under the root secret, in an anchored record: not in the image, the table or a log. The operator knows them, having registered the applications; with them nobody can make an ID token or stand in for the provider, whose answers come over TLS the host cannot open. Whoever holds the admin token can replace an application with one of their own at the same provider, which lets them sign in as nobody but themselves there.
- **What a provider's answer puts in the address.** The callback sends the browser to a path of the app with a fragment: `#provider_confirm`, or `#provider_error=<reason>` and, when the enclave still knew the trip, `&provider=<id>`. A fragment reaches no server and no other site. The reason is one of seven fixed codes and the provider one of four fixed identifiers, the one this browser asked for itself, so neither tells a stranger whether an address or a provider account has an account here. Anyone can write an address, so the web app believes nothing of it: a reason and a provider it does not know are dropped, both only ever become fixed words, and "linked" is said only when the server's own list agrees. The worst a made-up address does is show one of those fixed sentences.
- **What the web app keeps in the browser.** The session is the server's `HttpOnly` cookie, which scripts cannot read. No code, token, invitation, address or account id goes to `localStorage`, `sessionStorage`, the address bar or the history, with two exceptions that hold no secret and name nobody. `localStorage` keeps, besides the theme, the day a closing account is removed, while the app sees that account closing (`permaily-closes-at`): once the day has passed and nobody is signed in, the sign-in screen says once that an account used in this browser was deleted on that day, and forgets it. Someone else who uses the same browser profile learns that much and not whose account it was; cancelling the deletion or seeing an account that works forgets it sooner. `sessionStorage` keeps an invitation only while the browser is away at a provider (`permaily-invitation`), written when it is about to leave and taken out as soon as the app loads again, whatever the outcome. An invitation is a one-use code that names nobody, the storage ends with the tab, and a script that could read it could as well read the page's memory, where it was before. Both are read back only as what they are (a date this app wrote, twelve characters of the invitation alphabet); anything else is dropped unread.
- **Deleting an account.** It ends seven days after the person asks, and what is removed does not come back: the newest commit of its record says it is gone, so an earlier copy of the record, of its pointer or of its sessions put back in the table brings nothing back. Whoever runs the storage can delay a removal by taking the account out of the list of what is closing, only until someone tries to use it; and can hide an account from the operator's tool by taking it out of the list of accounts. Neither list can show an account that is not there or remove one that is not due: what counts is the signed record.
- **The operator's tool** (`/v1/admin/accounts`, with the admin token) shows what the enclave reads from signed records, and changes nothing. Whoever holds the admin token sees every account's address, name and state, which whoever runs the storage sees already.
- **Guessing.** A code takes five tries and an address ten wrong codes a day; an address gets five messages an hour (ADR 0017). The counts are in memory: a restart sets them back, and ends the sign-ins that were waiting.
- **Telling who has an account.** Starting a sign-in answers the same, and as fast, for every address; the lookup and the message come after the answer.
- **Sessions.** The cookie is `__Host-permaily-session`, `Secure`, `HttpOnly`, `SameSite=Strict`, and a request that changes something must come from the app's own origin. Accepted: a session that was closed and is put back in the table works again, for whoever still holds its cookie, until its thirty days end (ADR 0017).
- **Invitations** are made with the operator's token, of which the image holds only the hash. Whoever holds it decides who may sign up, and nothing else of an account.

## Invariants

Each one has, or must get, a test that tries to break it.

| Area | Invariant | Test |
|---|---|---|
| Attestation | No document is accepted unless signed under the pinned AWS root, fresh, with the caller's nonce, non-zero PCRs equal to the rebuild, and `user_data` equal to the connection's SPKI hash | `pkg/attestation` tests and `FuzzVerify` |
| Build | The image does not depend on the build machine | CI `reproducible` |
| Host | The parent links no enclave code (NSM, TLS keys, web app) | `depguard` rules in `.golangci.yml` |
| Host | Egress only reaches allowlisted destinations | `TestConnectProxy` |
| Enclave | No configuration is read from the parent at runtime | review (entry point arguments are baked into the image) |
| Production code | Fake attesters and test chains are never linked | `depguard` |
| Web | Every response carries HSTS, CSP, `nosniff`, `no-referrer` | `TestRouter` |
| Keys | A key is used only if this image alone can use it and nobody can change that | `TestSealed`, `TestHandoffRefuses`, `TestRootNeedsItsOwnKey` |
| Storage | An earlier copy of an anchored record is never read: lost, not rolled back | `TestAnchoredRefusesEarlierCopies`, `TestS3CommitsWithAnAdversary`, `TestS3CommitsAgainstS3` (a real bucket), `TestAnchoredNoticesCommitsGoingBack`, `TestCheckAnchors` |
| Keys | The root secret is handed only to a public release newer than the running one | `TestHandoffRefuses`, `TestVerifyIdentity`, `TestVerifyARealRelease`, `TestVerifyRekor2`, `TestTimestamps`, `TestSigstoreHistory`, `TestUpdateKeepsTheAuthorities`, `TestTUFSignatures`, `TestRootFollowsSigstore`, `FuzzVerify`, `FuzzVerifyRekor2`, `FuzzTimestamp`, `FuzzTrustUpdate`, `tests/sigstore-differential` |
| Keys | A record not written by an enclave of the same image or of a public release is refused | `TestRootRefusesPlantedRecords`, `TestRootChecksWhichKeyAnswered` |
| Keys | A debug enclave, a failing store or a deployment that already has a root secret never leads to a new one | `TestRootInDebugMode`, `TestRootDoesNotCreateWhenTheStoreFails`, `TestRootHandoff` |
| TLS | The TLS and ACME account keys leave the enclave only sealed, survive restarts, and are never replaced when storage fails | `TestSealedKeepsTheIdentity`, `TestSealedRejects`, `TestManagerSurvivesRestart`, `TestManagerWaitsForItsIdentity` |
| TLS | CAA records that let another account, CA or a wildcard in, or a logged certificate with another key, fail verification | `TestCheckPinned`, `TestCheckNoWildcard`, `TestClassify`, `TestCheckCertificates` |
| Logs | No log line names an address, a subject, content or a token | `TestMailEndToEnd` (log capture) |
| Logs | Logging never blocks or fails the enclave when the host does not read | `TestLogSinkNeverBlocks`, `TestLogSinkReconnects` |
| Mail | The MX stores nothing that arrived without TLS | `TestRefusesMailWithoutTLS` |
| Mail | An MX or an MTA-STS policy that does not hold the attested key, a policy that is not `enforce` for this MX only, or missing DNS records fail verification | `TestVerifySMTP`, `TestFetchMTASTS`, `TestCheckMail` |
| Mail | The store never holds plaintext senders, subjects, bodies or attachment names | `TestStoreNeverSeesPlaintext` |
| Mail | A sealed value cannot be opened under another key or context | `TestSealOpen` |
| Mail | KMS answers for any key other than the pinned one are rejected | `TestKMSRejectsOtherKey` |
| Mail | The CMS parser never panics or accepts garbage | `FuzzCMS`, `TestCMSRejectsGarbage` |
| Mail | The mailbox API rejects missing or wrong tokens | `TestMailboxAuth` |
| Mail | A key policy that lets anyone use the key without proving the verified image fails verification | `TestAnalyzeProblems`, `TestCheckKMS`, `FuzzAnalyze` |
| Mail | A policy that was opened and closed again while the enclave ran still fails verification | `TestPolicyReaderHistory`, `TestPolicyReaderGaps`, `TestCheckKMSHistory` |
| Mail | The status is accepted only over the attested TLS key | `TestFetchStatus` |
| Mail | Account administrators cannot obtain data keys | manual check (2026-11-09), `docs/acceptance.md` |
| Mail | KMS gives the data key to the allowlisted image and denies it in debug mode | production check (2026-11-10), `docs/acceptance.md` |
| Accounts | Whoever writes to the storage cannot add a way to sign in, a session or an invitation, nor bring an earlier account back | `TestStorageWrittenByHand`, `TestAnotherDeployment`, `TestSignUpRace` |
| Accounts | Starting a sign-in answers the same for every address, and codes cannot be guessed without limit | `TestStartLimits`, `TestWrongCodes`, `TestSignInEnds` |
| Accounts | A request that changes something is refused unless it comes from the app's own origin | `TestAccountsAPIRefusals` |
| Accounts | A passkey is accepted only for the challenge given, the app's origin and host, with the person verified, and with a valid signature of the stored key; no input makes the checks panic | `internal/webauthn` tests, `FuzzRegistered`, `FuzzAsserted`, `tests/webauthn-differential` |
| Accounts | Every failure of making or using a passkey, and of a recovery, is one error that says nothing of the account | `TestPasskeyRefusals`, `TestRecovery`, `TestPasskeysAPIRefusals` |
| Accounts | Whoever writes to the storage cannot add a passkey, bring back a removed passkey or a used recovery code, or turn "passkey only" off; the storage holds recovery codes only by their hash | `TestPasskeysWrittenByHand`, `TestWhatIsStoredOfPasskeys`, `TestRecoveryCodeUsedTwiceAtOnce` |
| Accounts | With "passkey only" on, mail signs nobody in, not even a sign-in started before | `TestPasskeyOnly` |
| Accounts | A provider's answer signs in only the browser that started it, never leads the browser out of the app, and fails with one of a fixed set of reasons | `TestProviderAnswerInAnotherBrowser`, `TestProviderReturnPath`, `TestAppPath`, `TestProviderReasons` |
| Accounts | A provider links by subject, an address it does not vouch for links or makes nothing without the code of a message, and a session links only to its own account | `TestProviderAddressNotVouchedFor`, `TestProviderLinkFromSettings`, `TestProviderConfirmRefusals`, `TestProvidersWrittenByHand` |
| Accounts | An ID token is accepted only if signed by the provider's published key with RS256, for this application, fresh, with the nonce given; no answer of a provider makes the checks panic | `internal/idp` tests and fuzz targets, `tests/idp-differential` |
| Accounts | The providers' secrets are never in an answer, a log or an error, and are stored sealed | `TestProviderApps`, `TestProviderAppsAPI`, `TestProvidersAPIInternalErrors` |
| Host | The host lets the enclave reach exactly the providers the image talks to | `TestEgressAllowsTheProviders` |
| Accounts | An account that was removed does not come back with an earlier copy of anything of it, and its address signs up again as a new account | `TestARemovedAccountDoesNotComeBack`, `TestAnchoredRemovesARecord`, `TestS3CommitsCarryARemoval` |
| Accounts | The callback names the provider of a failed trip only when it still knows the trip, and only ever one of the four fixed identifiers | `TestProviderCallbackNamesTheProvider`, `TestProviderReasons` |
| Web app | What the address says after a provider's answer, or after a sign-in link, only ever becomes fixed words, and leaves the address bar before anything renders | `web/tests/lib/arrival.test.tsx`, `web/tests/pages/auth/provider.test.tsx`, `web/tests/pages/auth/link.test.tsx` |
| Web app | The app leaves for another site only for an `https:` address the server answered, or this machine's own | `web/tests/lib/providers.test.tsx`, `web/tests/pages/auth/provider.test.tsx` |
| Web app | No code, token, invitation, address or recovery code is kept in the browser's storage, the address bar or the history; storage holds the theme, the day a closing account closes, and an invitation only during a trip to a provider | `expectNotKept` in `web/tests/pages/`, `web/tests/lib/closing.test.ts`, `web/tests/pages/auth/closing.test.tsx` |
| Accounts | A removal that fails in the middle never leaves an account half usable, and a finished one leaves nothing of it in the stores | `TestRemovalFailures`, `TestAnchoredRemovalFailures`, `TestRemovingAnAccountThatIsDue` |
| Accounts | The lists of accounts and of what is closing prove nothing: an entry written by hand shows and removes nothing, and one taken away does not keep a due account alive | `TestTheListsProveNothing`, `TestAccountsAdminAPIShowsTheSignedRecords` |
| Accounts | While an account is closing nothing changes but cancelling and signing out | `TestDeletingAnAccount`, `TestRecoveryWhileClosing`, `TestSettingsAPI` |
| Accounts | The operator's routes answer only with the admin token | `TestAccountsAdminAPI`, `TestTheOperatorsTool` |
| Storage | Only the newest version of an account's record stays in the table | `TestAnchoredKeepsOnlyTheNewest`, `TestAnchoredReadsWhileARecordChanges` |
| Accounts | Looking at whose sign-in a link is ends nothing, signs nobody in, tells nothing without the link, and a wrong token counts like a wrong code | `TestLookAtALink`, `TestLookAtALinkAPI` |
| Accounts | No log line, and no error passed on, names an address, a code, an account or a session | `TestAccountsAPIInternalErrors`, `TestMailSystemDown`, `TestRefused`, `TestUnreachable` |
| Accounts | No message leaves while SES says a configuration set applies to what the enclave sends, or does not answer | `TestSendWithWatch`, `TestWatchFailsClosed`, `TestWatchStates`, `TestWatchAddressOfTheDomain`; staging check (2026-11-11), `docs/acceptance.md` |
| Accounts | A configuration set seen at any time since the enclave started, or a reading that is not recent, fails verification | `TestWatchHistory`, `TestCheckSentMail`, `TestCheckStatusSentMail`, `TestMailStateClean` |
| Site | No CSP violations and a strict script policy on every page | `site/e2e/a11y.spec.ts` |

| Storage | A record is written only if nothing was committed for it since it was read, a removal included, and a write that is refused leaves nothing | `TestAnchoredWritesOnlyWhatWasNotChanged`, `TestAnchoredKeepsTheChangesOfTwoEnclaves`, `TestAnchoredDoesNotWriteOverARemoval`, `TestAnchoredMakesARecordOnce`, `TestAnchoredWithJunkAmongTheCommits`, `TestAnchoredOverS3CommitsWithTwoEnclaves`; the limit: `TestAnchoredOverS3CommitsWithADeleteMarkerInBetween` |
| Accounts | Two enclaves that change one account at once undo nothing of each other, bring no removed account back, and give up after a few tries instead of writing over | `TestTwoEnclavesChangeOneAccount`, `TestAChangeDoesNotUndoARemoval`, `TestTwoEnclavesSignOneAddressUp`, `TestAnAccountThatKeepsChanging`, `TestARemovalLeavesAnotherAccountsPointer`, `TestDeletingAskedAgainWhileItIsCancelled` |
## Logging

Never log email content, email addresses, tokens, cookies, codes, presigned URLs or keys, in the enclave or on the host. Enclave logs leave through the untrusted host (vsock port 8003, printed by `permaily-host` into the instance's journal; ADR 0015), so they are public: a log line needs the same care as an API response. `TestMailEndToEnd` fails if a line logged while handling a message names an address, the subject, the body or the token.

## Review checklist

For every change that touches the enclave, the host, auth, storage or the build:

1. Does it add a way for the parent instance or a third party to read or alter user data?
2. Does it read configuration or code from outside the image at runtime?
3. Does it add an outbound destination? Is it on the allowlist and in this document?
4. Does it log anything from the list above?
5. Does every new rejection path have a negative test?
6. Does the image still build identically on x86_64 and arm64?

## TODO

- [ ] Site: move the demos' inline `style` attributes to classes and remove `style-src-attr 'unsafe-inline'` from `site/astro.config.mjs`.

## Reporting vulnerabilities

Follow the [security policy](https://github.com/permaily-org/permaily/security/policy): report privately through the repository's Security tab (private vulnerability reporting), never in a public issue. The policy lives in [permaily-org/.github](https://github.com/permaily-org/.github/blob/main/security.md) and applies to every repository in the organization.
