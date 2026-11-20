# 0039 Delivery on new mail and on a schedule: how (webhooks, the queue and the scheduler)

Status: Proposed. Designed on 2026-11-20 for step 6 of F2 (docs/plan.md) while the owner was away, who asked for steps 6 to 9 to be done without waiting; not built. Builds on ADR 0019 ("Delivery, three ways") and amends it ("What changes in ADR 0019 and 0030"); builds on ADR 0016 and 0026 (strict anchored records), 0024 (two enclaves), 0030 (connections, the authorization server, pushed requests, the vendor egress on vsock 8004, the release order, revocation within five seconds) and 0031 (the default rule, receipts). The product choices ADR 0019, 0030 and 0031 left open were made here as the owner asked, each the one that fails closed and is simplest to build and test, and are listed for the owner to confirm or reverse in "Choices made for the owner (to confirm)". Its pull requests are in "Implementation pull requests"; its tests are K1 to K62. A design review comes before any of them.
Date: 2026-11-20

## Context

ADR 0019 decided three ways for mail to reach a vendor: on demand, through the API and MCP (built in step 5, ADR 0030); on new mail, "when a message that matches arrives, Permaily calls the vendor's webhook"; and on a schedule "the vendor asked for and the person approved, in the person's time zone". It decided what a call carries: "a signed notice that there is something to fetch, never mail: the vendor fetches with its token, through the same checks, leaving the same receipts". It left the rest to this step: retries, signatures, what happens when a vendor does not answer.

What step 5 built, and this step must not weaken: a connection is a strict anchored record that cannot be put back or hidden; nothing leaves before its receipt is written and chained (ADR 0037); every read goes through the default rule first, and nothing at all leaves while the model cannot be asked (ADR 0030's owner's decision 9); a revocation stops every byte within five seconds in the other enclave (decisions 6 and 14); the only door to the open internet is the vendor egress (vsock 8004), which today carries a credential-free bodiless `GET` of a vendor's own document; nothing of a person, a token or a formula is logged. The consent request carries no delivery field today: `oauth.Pushed` takes `query`, `mail_since` and `ends_in_days`, and `contracts.AuthorizationRequest` has nothing about delivery (`contracts.ConnectionDelivery` says only whether a connection gets mail now).

## Decision in short

- **A webhook is asked for only in a pushed request**, as a `webhook` parameter (`contracts.WebhookRequest`: its URL, `new_mail`, a `schedule`), so only apps ask for one; MCP clients, which send no pushed request, get none. Its URL must be on **exactly the host of the vendor's `client_id`**, under the same URL rules, so a vendor can only have Permaily call itself, and the consent screen shows one domain. ("Where the webhook comes from")
- **The person accepts it with the request, as a whole**: the screen says the vendor is told when mail it may read arrives, or at the time chosen, and never the mail. The person's time zone comes from the browser on the consent screen, is shown, and is kept sealed in the connection; the vendor is never told it. ("The consent screen")
- **The notice holds no mail**: its type, its identifier, a subscription identifier made for the vendor (never the connection's), the `client_id`, the URL called (`aud`), when it was made, the minute to fetch from, and for a schedule its time. No address, no account, mailbox or message identifier, no count. ("The notice")
- **Signed with Ed25519 as Standard Webhooks' `v1a` says**, by a key derived from the root secret, so both enclaves and every release that receives the secret sign with it; its public half is published at `https://api.permaily.com/.well-known/permaily-webhook-keys` and on `/status`, which `permaily-verify` compares. Replay: a timestamp within five minutes, an identifier taken once, `aud` the vendor's own URL. ("Signatures")
- **A notice of new mail only for mail that would leave**: the arrival is evaluated as a read of the connection would be, the rules first and the model asked where a read would ask it, so a held message (a sign-in code) never makes a notice. Nothing is evaluated, and nothing is sent, while the model cannot be asked. ("New mail")
- **Schedules are two fixed choices**: every day, or every week on one weekday, at a quarter hour, in the person's time zone, from the zones compiled into the image (`time/tzdata`); a time the clocks skip runs when they change, one that happens twice runs the first time; runs missed while the service was down make one late notice. A scheduled notice is sent whether or not mail matches, so it says nothing of the mail. ("Schedules")
- **The queue is sealed entries that decide nothing**: one per connection with its notice waiting, its lease, its watermark of new mail and its schedule's last run, written at the version read so two enclaves share the work; an index of due minutes the workers list; an hourly repair that walks every entry. Whether a notice may be sent is read, before every call, from the connection's strict record. Coalescing: at most one notice a minute per connection; ordering is not promised; at least once, the same identifier in every retry. ("The queue")
- **Calls go through the vendor egress (8004), HTTP/2 only, following no redirect**, a POST of at most 2 KiB with no cookie or credential, ten seconds, at most 4 KiB of the answer read. 2xx is taken; 410 stops the webhook for good; anything else is retried at one, five and fifteen minutes, one, three, six and twelve hours, then every twelve, for 72 hours, then dropped. The person's connection page shows the state, the next run, the last notice taken, and since when and why the calls fail, in fixed words. ("Calling a webhook", "Retries and giving up")
- **Stopping is immediate**: a call starts only within five seconds of a read of the connection that found it active, with an active device, its account open and the model askable, and its notice is written in full by then or not at all; revocation, expiry, the person turning it off, a replacement, an account's deletion and the model going off stop every notice. ("Revocation, the end and removal")
- **A notice is not a disclosure**: it writes no receipt; the vendor's fetch writes them. The consent receipt records the webhook approved; turning it off writes one too. ("Receipts")
- **`/status` and `permaily-verify`** show how notices are signed, the keys, that a notice holds no mail, the same-host rule, HTTP/2 and no redirects, the bounds, and this enclave's calls by fixed outcome; never a URL, a vendor that is not verified, a subscription or a count of webhooks. ("What /status and permaily-verify show")
- **Seven pull requests**: this one with the contract, the pure package `internal/webhook` (the URL rule, the request, schedules with their time zones, the notice, its signature and the key set, a call and its outcomes, the bounds) with its tests, `webhooktest` (a played vendor and a scripted sender), the service's seams and `webhookstest` (the fake connections and the contract the real ones pass); then the sender, the connections' half, the queue and scheduler, the routes and the web app written at the same time; the image last. About three weeks. ("Implementation pull requests")

## Where the webhook comes from

**Options.** (a) A member of the vendor's client metadata document (CIMD), as its redirect URIs are. (b) A parameter of the request, which only a pushed request can carry, as `query` is. (c) A registration of the vendor's, which there is not (ADR 0030 has no registration).

**Decision: (b), the pushed request's `webhook` parameter**, a JSON object of at most 1 KiB with no member but `url`, `new_mail` and `schedule` (`webhook.ParseRequest`, strict: an unknown member, two values, neither `new_mail` nor a schedule are `invalid_request` with no details, as a refused `query` is). Why: a webhook is per connection, not per client: an app routes a notice to its own user by the URL it asked for (`/hooks/permaily/<its user>`), which a document shared by every person cannot say; a pushed request is a back channel the vendor writes, never the address bar, so the URL never sits in a browser's history or a log of one; and it keeps the document's contract as ADR 0030 pinned it (`oauth.Document` ignores unknown members, which would have to become meaningful). (a) is kept as the alternative if a vendor with many people sharing one receiver asks for it. MCP clients send no pushed request and so ask for no webhook; nothing of MCP changes.

**Same origin: exactly the `client_id`'s host** (`webhook.URL`). The URL passes every rule of a `client_id` (`oauth.ClientID`: `https`, no user, no port, not even `:443`, no query, no fragment, a path, no dot segment, a public DNS name of two labels or more, no IP literal, not a reserved name, not the deployment's) and its host equals the `client_id`'s host byte for byte after the same normalisation. Not the registrable domain: telling `hooks.trip-app.com` from `trip-app.com.evil.com` safely needs the public suffix list in the image, which is more trusted data, changes, and is the kind of thing a lookalike exploits; a vendor that receives elsewhere publishes its document on that host. So a vendor can make Permaily call only the host whose document it controls, the screen shows one domain for both, and the egress learns nothing new (it already reaches that host for the document).

## The consent screen

`contracts.AuthorizationRequest.Webhook` (`WebhookAsked`) says the host, whether new mail is told and the schedule. The screen (web, pull request 5) adds one item to "What it asks for", in the request's words: "Tells trip-app.com when new mail it may read arrives" and "Tells trip-app.com every Monday at 08:00, your time (Europe/Madrid)", followed by "It is told only that there is mail to fetch, never the mail; it fetches with this connection, and every message it gets is in your activity." The person accepts or refuses the request as a whole, as ADR 0019 says; the webhook cannot be removed from it on the screen (choice 13). The time zone is the browser's (`Intl.DateTimeFormat().resolvedOptions().timeZone`), preselected in a picker of the zones the image knows, and sent with the approval (`AuthorizationDecision.TimeZone`); an approval of a request with a schedule without a zone the image knows is `time_zone_invalid` (`contracts.ErrTimeZoneInvalid`), and the zone is ignored for any other. It is fixed for the connection: a person who moves asks the vendor for a new request (choice 8). A request without a webhook shows nothing new.

**Asking again** (ADR 0030, the owner's decisions 7 and 10). The webhook is part of the scope: a request whose webhook is absent or the same (`webhook.Same`: the same URL, called for the same things) may be added to the client's connection as another device, and keeps the connection's webhook; any other (a webhook where there was none, another URL, another schedule) is "any other scope" and replaces the connection after fresh consent, the old one's notices ending with it. A new device never changes a webhook: it is per connection, not per device. Renewing keeps it.

## The notice

The body is `contracts.WebhookNotice`, JSON, at most 2 KiB (`webhook.Body`, which checks every member before it is sent; `TestANoticeHoldsOnlyWhatTheADRSays` pins the members):

| Member | What | Why |
|---|---|---|
| `v` | 1 | versioned |
| `type` | `mail.matched` or `schedule.due` | what to do |
| `id` | `ntc_` and 26 base32 characters, the `webhook-id` header too; the same in every retry of one notice | the vendor takes it once |
| `subscription` | `sub_` and 26 base32 characters, made for the connection's webhook, sealed in its record, also in `GET /v1/connection` (`VendorWebhook`) | which of its tokens to fetch with; never the connection's identifier, which the operator sees in the table |
| `client_id`, `aud` | the vendor's client and the URL called | a notice for another URL is refused by the vendor (`webhook.Open`) |
| `created_at` | when it was made; a retry keeps it | |
| `since` | the minute to fetch from: for new mail the minute the earliest message it is about arrived in, for a schedule the scheduled time before (or when the connection was made) | the vendor searches `received since` it (`VendorSearch`), which the `sender` part gives every connection |
| `scheduled_at`, `late` | for a schedule: the time it is for, and whether it is sent more than 15 minutes after it or stands for more than one missed run | |

What it never holds: mail, an address, a name, a subject, an account's, a mailbox's, a connection's or a message's identifier, a count of messages, the person's time zone, a token. What it says is one bit and a minute: that mail this connection may read arrived around then, which the vendor learns anyway when it fetches that mail, and which the call's timing already says. A vendor's logs, a proxy's in front of its receiver and the host see nothing a vendor's token-bearing fetch would not tell the vendor itself.

## Signatures

**Options.** (a) HMAC-SHA256 with a secret per connection (Standard Webhooks' `v1`), handed to the vendor in the token answer and kept sealed in the connection's record (what ADR 0030's table of later steps sketched). (b) Ed25519 with a key of the enclave's (Standard Webhooks' `v1a`), its public half published. (c) A JWS of the body.

**Decision: (b).** The signing key's seed is `Keys.Derive("permaily webhooks v1 wk1")` from the root secret (`webhook.KeyPurpose`), so both enclaves and every release that receives the secret sign with the same key, and no secret is stored per connection, handed through the token answer (whose shape ADR 0030 and every client already have), lost by a vendor or stolen from the table. A vendor verifies with the public key, which the api host publishes as a JWK set at `https://api.permaily.com/.well-known/permaily-webhook-keys` (`webhook.KeySet`, `Cache-Control: public, max-age=3600`, CORS `*`) over the enclave's own certificate (ADR 0012), and `/status` reports the same keys over the attested TLS key: `permaily-verify` fails when the two differ, so anyone can check that a notice was signed by the attested image. A notice proves it came from Permaily's enclave, not which connection's: that is `aud` and `subscription`, which the vendor checks against what it asked for.

**The signed content and headers** are Standard Webhooks', so its libraries verify a notice with no code of ours: `webhook-id` (the notice's `id`), `webhook-timestamp` (Unix seconds of this call), `webhook-signature` (`v1a,` and the base64 signature of `id.timestamp.body`; several, separated by spaces, during a rotation). `webhook.Sign` and `webhook.Verify` are the reference, and the developers' page documents them.

**Replay.** A vendor refuses a timestamp more than five minutes from its clock (`webhook.TimestampTolerance`), keeps the identifiers it took for at least that long and takes each once, and refuses a body whose `aud` is not the URL it was called on or whose `id` is not the header's (`webhook.Open`). A notice replayed to the vendor within its five minutes is a notice it already took; one replayed later is refused; one replayed to another vendor is refused by `aud`. None of them can do more than make a vendor fetch with its own token.

**Rotation.** A key is named (`wk1`). A rotation is a release that adds the next key (`wk2`), publishes both and signs with both for at least 30 days, then a release that signs with the new one alone and keeps publishing the old one for 30 days more. A vendor that cached the set an hour sees the new key before it is used alone. A compromise of the signing key is a compromise of the root secret, which ADR 0013 covers: every key derived from it is replaced.

## New mail

**When.** A notice of new mail is made only when a message arrives that a read of the connection would give: every rule first (the default rule holds sign-in codes and password resets), then the consented query, the mailboxes, the mail since, evaluated together (ADR 0030, "What a read gives"). A message a rule holds, one the formulas do not match and one that is a forwarding confirmation make nothing; so a notice never tells a vendor that something it cannot have arrived.

**The model.** Rules first means asking the model where the read would ask it (the default rule's `about`, the query's own `about` and `mentions`), at arrival, not at the vendor's fetch. Options: (a) ask at arrival, through the same discloser, so the decision is kept (ADR 0028's kept answers, ADR 0031's decisions in strict records) and the vendor's fetch reuses it without a second ask; (b) notify on exact conditions alone and let a message that needs the model wait for the vendor's next fetch or schedule; (c) notify on any new mail of the connection's mailboxes, which would tell the vendor when held mail arrived. (c) is refused: it leaks what the rules hold. **Decision: (a)** (choice 5): it costs one ask per message per condition, about $0.13 per 1,000 messages that need one (ADR 0028), most of which the fetch would have paid anyway; it is bounded by the spend guard and the account's share like every ask, and by the mailbox's delivery limits (600 messages an hour). While the model cannot be asked for the account, nothing is evaluated and the watermark stays (`contracts.ErrModelUnavailable` from `Connections.Pending`): no notice is made, as no mail leaves; when the model is back, what arrived meanwhile is evaluated and told.

**How an arrival is found.** `services/mailboxes` calls `inbox.Arrivals.Arrived(mailbox, received)` after the mark is written (ADR 0026, "The sequence", 6): a hint that returns at once, never fails or slows the delivery, and carries nothing of the message. The webhooks service looks up the connections whose webhook wants that mailbox's new mail (the index `webhooks#mailbox#<mailbox>`) and marks each due now. The worker that takes a connection's entry calls `Connections.Pending(connection, watermark)`: the connection's own service evaluates, through `inbox.Discloser` with the connection's formulas, the messages received after the watermark less `ArrivalLag` (two minutes, more than a delivery's one-minute deadline, so a message whose storing finished after a later one's is still found), skipping those already seen, at most 50 (`ArrivalPage`; more wait for the next pass), and answers whether one would leave, the earliest such one's received time and the watermark after it. The watermark (`webhooks.Watermark`: the newest received time evaluated and the identifiers seen within the lag, at most 64) is sealed in the queue entry, so each message is evaluated once, by either enclave, after a restart. A hint lost (a crash after the mark, a full buffer) is found by the hourly repair, which evaluates every new-mail subscription: a notice can be late by up to an hour, never lost while its connection lives. A message older than the watermark less the lag (stored more than two minutes late) is not told; the vendor finds it at its next fetch.

Evaluating writes no receipt: nothing leaves. It is not a vendor's request, so it counts against no vendor's limits, and the model's asks are the account's, as the default rule's are.

## Schedules

**Fixed choices** (`contracts.WebhookSchedule`, `webhook.CheckSchedule`): every day at `HH:MM`, or every week on one weekday at `HH:MM`, the minutes on a quarter hour; one schedule per connection. Not cron: a cron expression needs a sentence the system writes for it, has corner cases (`*/7`, day-of-month and day-of-week together) a person cannot read, and lets a vendor ask for every minute; two choices are rendered in one sentence and tested exhaustively (choice 7).

**The person's time zone**, by its IANA name, from the zones compiled into the image: `internal/webhook` imports `time/tzdata`, and the enclave has no zoneinfo on disk, so the rules a schedule follows are pinned by the image's Go toolchain and measured with it; a government's change of its zone's rules reaches schedules with the next release built on a toolchain that has it. `webhook.Zone` refuses `Local`, paths and anything but a name of the database.

**Changes of the clocks** (`webhook.Next`, tested across a year in six zones, half-hour and 45-minute offsets and a skipped day included): a wall time that happens twice (01:30 the night the clocks go back) runs the first time only; a wall time the clocks skip (02:30 the night they go forward, or a day a zone skipped, as Samoa did on 2011-12-30) runs at the instant they change, the first one at or after it on the wall. So a schedule runs once on every day it names, never twice, never not at all.

**Missed runs** (`webhook.Due`): when the service was down, or a worker late, past one or more runs, one notice is sent for the latest of them, `late` when it is more than 15 minutes after it or stands for more than one; never one per run missed (choice 9). Its `since` is the run before, so the vendor fetches everything it missed.

**What a run does.** Options: (a) send the notice at every run, whatever the mail; (b) evaluate the connection's mail at the run and send only when something new would leave. **Decision: (a)** (choice 6): a scheduled notice then says nothing of the mail (its timing is the person's schedule), needs no evaluation and no model, and is the simplest to build and test; the vendor fetches since the run before and may find nothing. (b) would tell the vendor, by silence, that nothing matched, and would ask the model on the schedule. ADR 0019's "Permaily runs the query at that time and calls the webhook" becomes "Permaily calls the webhook at that time and the vendor runs its query", which gives the vendor the same mail and tells it less. While the model cannot be asked for the account, a run is skipped (`model_unavailable`), since a fetch would get nothing (choice 12).

## The queue

**Options.** (a) Strict anchored records per connection, as connections' own. (b) Sealed entries written at the version read, deciding nothing, with every authority read from the connection's strict record. (c) Memory only.

**Decision: (b).** (c) loses notices in a restart and cannot be shared by two enclaves. (a) would make every notice a strict write (an S3 commit kept ten years) for state whose going back costs nothing: what the queue could get wrong, if whoever writes the table deleted or put back its entries, is to drop a notice (as the host can, by refusing the call) or to repeat one (which a vendor takes as a fetch more). It can never send a notice to another URL (the URL is read from the connection's record), put anything in one (the body is built from fixed members), or call after a revocation, an end, the person turning it off, a 410 or an account's deletion: before every call, the connection's record, the account's list and its standing are read strictly (`Connections.Subscription`), as a read of mail would. ADR 0016's bar ("every record whose going back matters") is met by where the authority is, not by the queue.

**Entries** (`webhooks.Partition*`), each sealed under `PurposeSeal` and bound to its partition and key, so one moved or forged does not open:

| Entry | Holds | Written |
|---|---|---|
| `webhooks#state#<shard>`, at the connection's identifier (16 shards by a hash of it) | the notice waiting (its identifier, made, `since`, calls made, the next call), a generation counted by each match, the lease (an enclave's random name and until when), the watermark of new mail, the schedule's last run and next one, the last notice taken, since when calls fail and the last outcome | at the version read; made by `Subscribe` |
| `webhooks#due#<minute>`, at the connection's identifier | that its entry is due in that minute | made when a notice is due or a run comes; expires 72 hours after its minute |
| `webhooks#mailbox#<mailbox>`, at the connection's identifier | that its webhook wants that mailbox's new mail | made by `Subscribe`; dropped by the repair once the connection has no webhook |

**`Subscribe`** is called by the connections service when it makes a connection with a webhook, after its record and before its code is issued, and fails the approval when it fails (the pending connection is then fenced by the hourly pass, as any approval that failed half way is): so every webhook has its entry, and no hint is needed for that. It writes the state entry (`CreateNew`'s equivalent, version 0), the mailbox index and the first due minute (now for new mail, the first run for a schedule), and is idempotent.

**Workers.** Each enclave lists the due minutes up to now every five seconds (from the newest it finished, and ten minutes back after a start; the repair covers older ones), and for each connection named: reads its state entry; skips it while another enclave's lease is live; writes its own lease at the version read (refused: the other enclave has it); decides what is due (a notice waiting whose next call has come and that is `MinInterval` past the last notice taken, a new-mail evaluation, a run); reads the subscription; calls; writes the outcome and the next due minute at the version read; deletes the due entry. An enclave that crashes mid-call leaves a lease that ends in 30 seconds, after which the other one calls again with the same notice identifier, which the vendor takes once. At most 16 calls at once and 600 started a minute in each enclave, so a slow vendor delays its own notices and nobody else's for long.

**Coalescing.** A connection has at most one notice waiting. A match while one waits only moves its generation (and `since` back, if earlier); a notice is sent at most once a minute per connection (`MinInterval`); a match during a call leaves the notice waiting for the next minute when the generation moved since it was signed, so nothing that arrived is left untold. 100 messages in a minute make one notice, then at most one a minute after.

**Idempotency and order.** At least once: every retry of one notice carries its identifier, and a new notice is made only once the one before was taken or given up. Ordering is not promised across a connection's notices or between connections; a vendor that needs order fetches by `received`.

**The hourly repair** (`RepairEvery`) walks every state entry of every shard, in each enclave under a sealed lease that only shares the work (as the receipts' audit's, ADR 0037): it drops what belongs to a connection with no webhook (removed, ended for more than a day, replaced), writes a due entry where one is missing, evaluates every new-mail subscription (a lost hint), and corrects a schedule's next run.

## Calling a webhook

**Egress: the vendor egress, vsock 8004, through a client of its own.** Options: (a) the existing vendor egress, with a second client in the enclave (`enclave.WebhookEgressClient(8004)`), as the model has its own; (b) a third listener on the host; (c) the allowlisted proxy (8000), which cannot reach a vendor's host. (b) changes nothing a dishonest host cannot already do and adds a flag, a port and a test; the host sees the same TLS either way. **Decision: (a).** What the egress was promised to carry widens: from a bodiless `GET` of a vendor's document to also a `POST` of at most 2 KiB of a signed notice, to the same host, which holds nothing of the mail (docs/threat-model.md, "Egress allowlist", changes with pull request 1).

**The call** (`webhook.Sender`, implemented by `internal/webhook/send`, pull request 1): `POST` of the URL exactly, `Content-Type: application/json`, the three headers, no cookie, no credential, no `Referer`, no compression asked; HTTPS checked against the roots in the image, TLS 1.2 or later; **HTTP/2 only** (ALPN `h2`), as vendors' documents and the model are (ADR 0030, the owner's decision 11: Go's HTTP/1 transport writes an idle peer's unsolicited bytes to the process's log), so a receiver that speaks only HTTP/1.1 cannot be called until a separately tested path that logs nothing exists (choice 4); **no redirect followed**, wherever it points; at most ten seconds from the dial to the status (`AttemptTimeout`); at most 4 KiB of the answer read, then the connection closed: its status decides and its body never does; nothing of what the vendor's server sends reaches a log. **The notice leaves within five seconds of a read that allowed it, or not at all**: each call carries `WriteBy` (`ReadAt` plus `oauth.ConnectionFresh`), and a call whose dial and handshake have not written the request in full by then is cancelled (`too_late`), so the bound on revocation holds for the notice's bytes, which are written at once (they fit in one frame). Only the vendor's answer may come later.

**Outcomes** (`webhook.Outcome`, fixed): `sent` (a 2xx), `gone` (410), `redirect` (a 3xx), `status` (any other), `no_answer` (not reached, reset, timed out), `refused` (TLS that does not verify, or no HTTP/2), `too_late`. Only these words are counted, kept and logged; a vendor is named in a log only when it is on the verified list, else `other`.

## Retries and giving up

A notice is called at once (once `MinInterval` has passed since the last one taken), then, after each failure, at one, five and fifteen minutes, one, three, six and twelve hours, and every twelve hours after (`webhook.Backoff`), until 72 hours from when it was made (`GiveUpAfter`): about a dozen calls. Then it is dropped (`given_up`); the next match or run makes a new notice and starts again. `410 Gone` is the vendor asking to stop: the connection's record says `stopped` (`Connections.Stop`, at the version read) and no notice is made again for it; only a new request of the client turns it on (choice 14). Nothing else turns a webhook off on its own: a dead receiver costs a dozen calls per notice, which the bounds keep small (choice 10).

**What the person sees** (`contracts.ConnectionWebhook` on the connection's page): the host, what it is told and when (the schedule in their zone), its state (`on`, `off`, `stopped`), the next run, when a notice was last taken, and, while calls fail, since when and why in fixed words (`no_answer`: "trip-app.com didn't answer", `error`, `redirect`, `refused`). A button turns it off, with words that say it is final for this connection and that the vendor can still fetch on demand (`connections.WebhookAPI.WebhookOff`, at the version shown, no fresh proof, since it only protects, and allowed while new terms are not agreed, as revoking is; choice 11). **What the vendor sees** (`contracts.VendorWebhook` in `GET /v1/connection`): its subscription, the URL, what it is told, and the state; never the person's time zone, never why.

## Revocation, the end and removal

A call starts only after `Connections.Subscription` answered `Calls` from a read at most five seconds old in that enclave: the connection active and before its end, at least one device active (nobody else could fetch), the account open or only not agreed to new terms (blocked, closing, gone: no), the webhook `on`, and `delivery` `delivering`. So a revocation (of the connection, or of its last device), an end, a replacement, the person turning it off, a vendor's 410, an account's deletion asked, or the model going off, stops every notice at once in the enclave where it happened and within five seconds in the other; a notice already being written then is written within the same five seconds or cut (`WriteBy`). A notice found waiting for a connection that no longer calls is dropped (`ended`, `no_device`, `off`, `model_unavailable`). Removing an account removes its connections' records (ADR 0030, #152): the queue's entries then read `ErrNoWebhook` and are deleted at their next touch or by the repair within the hour; they hold nothing of the mail meanwhile (times, counters, a watermark of message identifiers, sealed).

## Receipts

A notice is not a disclosure of mail and writes no receipt; what the vendor fetches after it writes the receipts of every read, as any fetch does (choice 15). Two moments are recorded, as consent: the approval's `consent` receipt carries the webhook approved (its host, what it is told, the schedule and the zone), and turning a webhook off writes a `consent` receipt of that change. The evaluation of new mail writes none, since nothing leaves; the model's asks it makes are the account's, as the default rule's, under the consent the connection recorded (docs/legal.md, engineering item 2).

## Bounds

| What | Bound |
|---|---|
| The `webhook` parameter | 1 KiB, one object, no unknown member |
| A webhook's URL | a `client_id`'s rules, 512 bytes, the `client_id`'s exact host |
| Webhooks a connection | one; schedules a webhook: one |
| A notice's body | 2 KiB (two URLs of 512 bytes fit) |
| A call | ten seconds; its request written within five seconds of the read that allowed it; 4 KiB of the answer |
| Notices a connection | one waiting; one taken a minute |
| Calls an enclave | 16 at once, 600 started a minute |
| A notice's life | 72 hours, about a dozen calls |
| An evaluation of new mail | 50 messages, the matcher's budgets, 25 asks of the model |
| What a lost hint delays | an hour (the repair) |
| A message stored late | told if within two minutes of the watermark (`ArrivalLag`) |

## What /status and permaily-verify show

`/status` gains `webhooks` (`attestation.WebhooksStatus`, committed with this design): `signature: "standard-webhooks-v1a"`, the keys (`id`, `x`) and `keys_url`, `notice_holds_mail: false`, `same_host: true`, `redirects: "none"`, `transport: "h2"`, `new_mail: "after_rules"`, `revocation_seconds: 5`, the interval, timeout and give-up in seconds, the schedules (`day`, `week`), and this enclave's calls by outcome and its notices skipped by reason since it started (every key, also at zero). Never a URL, a host that is not verified, a subscription, an account, a connection, or a count of webhooks.

`permaily-verify` prints the section and fails when `notice_holds_mail` is true, `same_host` false, `redirects` not `none`, `transport` not `h2`, `new_mail` not `after_rules`, `revocation_seconds` over 5, or the key set published at `keys_url`, fetched over the attested connection, differs from `/status`'s keys (pull request 6).

## The threat model

What docs/threat-model.md says from this design on (its section "Webhooks (F2 step 6, ADR 0039; designed, not built)" is added with this pull request, and brought to present tense by pull request 6):

- **A vendor** can have Permaily call only its own `client_id` host, only after the person approved it, with a signed notice of at most 2 KiB that holds no mail, at most once a minute per connection, about a dozen times per notice. It cannot choose what a notice says, make Permaily call anyone else, or learn from a notice that held mail arrived. It learns when mail it may read arrives (to the minute), which is what it asked for and the person approved.
- **The host** sees a POST to the vendor's host and when, and its size: so roughly when mail a connection may read arrived for someone connected to that vendor, or that a schedule ran. It can drop or delay a call (the queue retries) and cannot read, forge or redirect one. If dishonest, it can relay the call to a TLS service it reaches that authenticates the vendor's name (ADR 0030, finding 19), which receives a signed notice with nothing of the mail.
- **Whoever writes the table** can delete or put back the queue's entries: a notice dropped, delayed until the repair, or repeated. Never one to another URL, with other content, or after a revocation, an end, the person turning it off or an account's deletion: those are read from strict records before every call.
- **A replay** of a captured notice makes a vendor fetch at most once more with its own token, within five minutes and only at the URL it was meant for.
- **Logs**: kinds and outcomes only; never a URL, a subscription, a notice's identifier, a time zone or a vendor that is not verified. `TestMailEndToEnd` fails if any of them appears.

## The privacy page and data

The privacy page (pull request 6, before the release) says, in present tense once released: a connection may ask to tell its app when new mail it may read arrives, or at a time the person chose; the person decides when they allow it and can turn it off; Permaily then sends the app a signed message from the enclave that there is mail to fetch, with the minute it arrived and nothing of the mail; it asks the model about a new message as soon as it arrives when the default rule or the query needs it; the person's time zone is kept with the connection and never given to the app. docs/data.md gains the rows of "The queue" and the webhook in the connection's record (with this pull request, marked designed); docs/legal.md gains engineering item 16 for the privacy page.

## Cost

At today's prices in `us-east-2`: a notice is about six write units of DynamoDB (the due entry, the lease, the outcome, the next due entry, its deletion; the state entry is under 2 KiB), $7.50 a million, and a strict read of the connection when the enclave's is older than five seconds (an S3 listing and a read), about $5 a million; the call itself is a few KiB of egress. An evaluation of new mail is one query of the mailbox's messages and, for a message that needs it, the model's ask, about $0.13 per 1,000 such messages (ADR 0028), most of which the vendor's fetch reuses. The hourly repair reads every state entry once an hour in one enclave: about $8 a year at 10,000 webhooks, and an evaluation per new-mail subscription, about $20 a year at 10,000. `time/tzdata` adds about 450 KB to the image. Measured on staging by K60, its numbers recorded here.

## What changes in ADR 0019 and 0030

- **ADR 0019**, "Delivery, three ways": a webhook is asked in a pushed request, on the vendor's own host, accepted with the request; on new mail, a notice only for mail that would leave, the model asked at arrival; on a schedule, two fixed choices in the time zone the person chose on the consent screen, and the vendor (not Permaily) runs the query when called: the notice says nothing of the mail. "What is left to design": retries, signatures and a vendor that does not answer are settled here.
- **ADR 0030**: the pushed request takes a fourth parameter (`webhook`); `AuthorizationRequest`, `AuthorizationDecision`, `Connection` and `VendorConnection` gain optional fields; "Asking again" counts the webhook in the scope; the vendor egress also carries notices; the table of later steps' "the webhook's secret sealed in the connection's record" becomes the subscription and the URL sealed there, with no secret (signatures are the enclave's).

## The contract and the seams

### Committed with this design

- `internal/contracts/webhooks.go`: `WebhookEvery` (`EveryDay`, `EveryWeek`), `WebhookSchedule`, `WebhookRequest`, `WebhookAsked`, `WebhookState`, `WebhookFailure`, `ConnectionWebhook`, `VendorWebhook`, `WebhookNoticeType`, `WebhookNoticeVersion`, `WebhookNotice`, the header names, `ErrTimeZoneInvalid`; in `connections.go`, `AuthorizationRequest.Webhook`, `AuthorizationDecision.TimeZone`, `Connection.Webhook`, `VendorConnection.Webhook`, each omitted when empty, so ADR 0030's shapes are unchanged (`TestNoWebhookLeavesTheShapesOfADR0030`); `TestWebhookShapes` and `TestANoticeHoldsOnlyWhatTheADRSays`.
- `web/src/lib/api.ts`: the same shapes (`WebhookEvery`, `WebhookSchedule`, `WebhookAsked`, `WebhookState`, `WebhookFailure`, `ConnectionWebhook`, `AuthorizationRequest.webhook`, `AuthorizationDecision.time_zone`, `Connection.webhook`); the calls and the words with the screens (pull request 5).
- `internal/webhook` (pure, depguard `webhook-is-pure`): the bounds and `Backoff`/`Wait`, the prefixes, the key's identifier, purpose and path; `URL`, `ParseRequest`, `Same`; `CheckSchedule`, `Zone`, `Next`, `Due` over `time/tzdata`; `Body`, `Sign`, `Verify`, `Open`, `KeyFromSeed`, `KeySet`, `ParseKeySet`; `Call`, `Outcome` with its `Failure`, `Sender` (`call.go`); their tests (K1 to K9).
- `internal/webhook/webhooktest`: `Vendor` (a played vendor's receiver that verifies as a vendor must) and `Sender` (a scripted `webhook.Sender`), with their tests (K15).
- `internal/services/webhooks/webhooks.go`: `Config`, `Keys`, `Subscription`, `Watermark` and `SeenMax`, `Evaluation`, `Connections`, `Queue`, `Skip`, `Status`, `KeySet`, the partitions, `Shards`, `DueKept`, `PurposeSeal`, `ErrNoWebhook`. A service imports no `net/http` (depguard `services-know-no-adapter`), so the call is `internal/webhook`'s and its sender an adapter of its own, as `vendors/cimd` is.
- `internal/services/webhooks/webhookstest`: `Fake` (a `webhooks.Connections` of connections a test makes, ends and fills), `Contract` with `World` and `Arrival` (K10 to K14), `FakeWorld`, `Fresh`, with their tests.
- `internal/services/connections/webhooks.go`: `WebhookAPI` (`WebhookOff`) and `PurposeWebhookSeal`; `connectionstest.Fake.WebhookOff`.
- `internal/inbox/arrivals.go`: `Arrivals`.
- `pkg/attestation/status.go`: `Status.Webhooks`, `WebhooksStatus`, `WebhookKey`, `WebhookSignatureV1a`, `WebhookNewMailAfterRules`.
- `.golangci.yml`: `webhook-is-pure`; `webhookstest` and `webhooktest` in `no-fakes-in-production`.

As ADR 0030 did, nothing implements the service, the sender, `Connections` or `WebhookAPI` in the image yet, no route or composition changes, nothing calls `Arrivals`, nothing sets `Status.Webhooks`, and the image is unchanged (nothing of the image imports `internal/webhook`). The exceptions are the pure package every implementation shares (the URL rule, the request, schedules and zones, the notice and its signature), and the fakes with their contract.

### The functions each pull request writes

- **1**: `internal/webhook/send`, `New(hc *http.Client, now func() time.Time) webhook.Sender`; in `internal/enclave`, `WebhookEgressClient(port uint32) *http.Client` (HTTP/2 only, no redirect, the bounds above).
- **2**: in `internal/services/connections`, `*Service` implements `webhooks.Connections` and `WebhookAPI`; `Config.Webhooks webhooks.Queue`; the pushed request's `webhook` (through `webhook.ParseRequest`, in `oauth.Pushed`'s caller or `oauth.Pushed` itself), the request's and the record's webhook, the decision's zone, `ConnectionChange` with `webhook.Same`, `Connection.Webhook` (with the queue's state through a `WebhookStates` interface it defines), `VendorConnection.Webhook`, the consent receipt's webhook.
- **3**: in `internal/services/webhooks`, `New(cfg Config) (*Service, error)` implementing `Queue`, `inbox.Arrivals`, `Status` and `KeySet`; `Run(ctx)` (the workers and the repair; the composition starts it); `States(ctx, connection)` for the person's page; in `internal/services/mailboxes`, the call of `Arrivals` after the mark.
- **4**: in `internal/httpapi`, `POST /api/v1/connections/{id}/webhook/off` (`{"version": n}`) against `connectionstest.Fake`, and `GET /.well-known/permaily-webhook-keys` on the api host against a `webhooks.KeySet`.
- **5**: the consent screen's webhook, the zone picker and `time_zone`, the connection page's webhook and its button.
- **6**: the composition, `/status`, `permaily-verify`, `TestMailEndToEnd`'s webhook half, the docs, the staging check, the release.

## Tests

Numbered K1 to K62 so a review can check each. "Two enclaves" means two services over one store, as in ADR 0030. No test in CI reaches a real vendor; a webhook is played (`webhooktest.Vendor` behind a TLS `httptest` server speaking HTTP/2).

**Committed with this design** (pull request 0)

- **K1.** The contract's shapes (`TestWebhookShapes`), ADR 0030's unchanged without a webhook (`TestNoWebhookLeavesTheShapesOfADR0030`), and the notice's members (`TestANoticeHoldsOnlyWhatTheADRSays`).
- **K2.** `URL`: on the `client_id`'s exact host only; a subdomain, a lookalike, a port (`:443` too), a user, a query, a fragment, a dot segment, an address, the deployment's own and 513 bytes refused (`TestAWebhookIsOnTheVendorsOwnHost`).
- **K3.** `ParseRequest`: strict, bounded, at least one of new mail and a schedule (`TestAWebhookRequestIsStrict`).
- **K4.** `CheckSchedule` and `Zone`: the two choices on quarter hours, nothing else; IANA names of the image only, never `Local` or a path (`TestAScheduleIsOneOfTheFixedChoices`, `TestAZoneIsAnIANANameOfTheImage`).
- **K5.** `Next`: later today, the person's day and not UTC's, a skipped time at the change, a repeated time once, Madrid's, Lord Howe's half hour, weekly, Samoa's skipped day; a year of runs in six zones at six times, each once a day, on the wall time or at a change (`TestTheNextRunIsInThePersonsTimeZone`, `TestAYearOfRunsInEveryKindOfZone`).
- **K6.** `Due`: nothing before the run, on time, late, three missed in one notice, a downtime of 400 days in one (`TestMissedRunsMakeOneLateNotice`).
- **K7.** `Body`, `Sign`, `Verify`, `Open`: within 2 KiB with the longest URLs; every malformed notice refused; Standard Webhooks' `v1a` vector; another body, identifier, timestamp or key, too old, from the future, `v1` alone and none refused; both keys during a rotation; another audience refused (`TestANoticeIsWhatTheADRSaysAndFitsItsBound`, `TestANoticeIsSignedAsStandardWebhooksSays`).
- **K8.** `KeySet` and `ParseKeySet`: golden, round trip, duplicates and other key types refused (`TestTheKeysArePublishedAsAJWKSet`).
- **K9.** `Backoff` gives 10 to 14 calls in 72 hours; `Same` (`TestRetriesBackOffAndAWebhookIsTheSameOnlyWhenEqual`).
- **K10 to K14.** `webhookstest.Contract` on the fake (`TestTheFakeConnectionsKeepTheContract`): K10 the subscription as approved (its own `sub_` identifier, the URL, the schedule, the zone, a fresh read); K11 `Calls` false on revocation, every device revoked, the end, the account closing, the webhook off and the model off; K12 removed is `ErrNoWebhook`, and the vendor's stop is `stopped`; K13 new mail: a held message and one that does not match make no notice, one that leaves does with its time, none twice, one stored late within the lag once; K14 nothing evaluated while the model is off, and what arrived told when it is back. Plus `TestACallStartsOnlyOnAFreshRead`.
- **K15.** The played vendor verifies, opens for its URL and takes each notice once; refuses another key, an old timestamp, another URL or identifier, a cookie and another content type (`TestThePlayedVendorVerifiesAndTakesEachNoticeOnce`); the scripted sender (`TestTheScriptedSenderAnswersInTurn`); outcomes shown as fixed failures (`TestAnOutcomeIsShownAsAFixedFailure`).

**The sender** (pull request 1)

- **K16.** A call is a `POST` with the three headers and `application/json`, no cookie, credential, `Referer` or `Accept-Encoding`; over HTTP/2 only (a receiver of HTTP/1.1 alone is `refused`), TLS checked against the image's roots (a certificate for another name, or self-signed, `refused`).
- **K17.** Outcomes: 200 and 204 `sent`, 410 `gone`, 301, 302, 307 and 308 `redirect` and not followed (to `169.254.169.254` and `localhost` included), 400, 404, 429 and 500 `status`; slow headers past ten seconds, a reset and a refused dial `no_answer`; an answer of 1 MiB read to 4 KiB and closed.
- **K18.** `WriteBy`: a handshake held past it is `too_late` and the receiver sees no request.
- **K19.** The real sender through `webhooktest.Vendor`: every call verified and taken once, during a rotation too.
- **K20.** Nothing a receiver sends reaches a log: unsolicited bytes on an idle connection, a header of 1 MiB, a malformed status; the log holds the outcome alone.
- **K21.** `enclave.WebhookEgressClient` dials only the vendor egress's port, never the allowlisted proxy.
- **K22.** The outcomes counted by fixed key, every key at zero from the start.

**The connections' half** (pull request 2)

- **K23.** The pushed request's `webhook`: what `ParseRequest` refuses is `invalid_request` with no details, before any screen; the parameter in the address bar is ignored; a request without it has no webhook.
- **K24.** `AuthorizationRequest.Webhook` as asked; an approval of a request with a schedule needs a zone the image knows (`time_zone_invalid`), any other's zone is ignored.
- **K25.** The connection's record: whether it has a webhook, what it is told and its state readable and signed; the URL, the subscription and the zone sealed (`PurposeWebhookSeal`): a reader of the table finds none of them.
- **K26.** `Subscribe` is called after the record and before the code; when it fails, no code is issued and the hourly pass fences the pending connection.
- **K27.** Asking again: no webhook or `Same` adds a device and keeps the webhook; another replaces, with a new subscription, and the old one's `Ended`.
- **K28.** `WebhookOff`: at the version shown (`consent_changed` otherwise), final, without a fresh proof, under unagreed terms; its `consent` receipt.
- **K29.** `Stop` at the version read; a 410 for a connection replaced since changes nothing of the new one.
- **K30.** `Connection.Webhook` and `VendorConnection.Webhook` as composed; the vendor is never told the zone.
- **K31.** `webhookstest.Contract` on the real service, with a `World` of two enclaves: a revocation through one is `Calls` false through the other within five seconds.
- **K32.** `Pending` through the real discloser: a message with a sign-in code held with no notice; a message that needs the model asked once at arrival, and the vendor's fetch after the notice reuses the decision without a second ask; the model off is `ErrModelUnavailable`, and the watermark stays.
- **K33.** The approval's `consent` receipt holds the webhook approved and the zone.
- **K34.** Renewing keeps the webhook; removing the account removes it with the record.

**The queue and the scheduler** (pull request 3), against `webhookstest.Fake`, `webhooktest.Sender` and a played clock

- **K35.** `Subscribe` makes the state entry, the mailbox index and the first due minute, once however often it is called.
- **K36.** An arrival makes its notice within the worker's five seconds; a hint lost is found by the repair within the hour.
- **K37.** Coalescing: 100 matches in a minute make one notice, then one a minute for what arrived since; a match during a call is told by the next.
- **K38.** Two enclaves: one call per notice under the lease; one that crashes mid-call is followed by the other's call with the same identifier after 30 seconds, and the vendor takes it once.
- **K39.** Retries at 0, 1, 6 and 21 minutes, 1 h 21, 4 h 21, 10 h 21, 22 h 21 and every 12 hours after; given up at 72 hours, the person's `failing_since` and `failure`; the next match a new identifier.
- **K40.** A 410 calls `Stop` and nothing is called again.
- **K41.** A notice waiting when its connection is revoked, ended, replaced, turned off, or its account closes is not called (`ended`, `off`, `no_device`) and its entry dropped.
- **K42.** The model off: no evaluation, no run (`model_unavailable`); back: what arrived meanwhile is told once.
- **K43.** A weekly and a daily schedule across Madrid's and New York's changes of the clocks, at each run whatever the mail; three days down, one late notice.
- **K44.** A message stored late within the lag told once; none twice through either enclave; past `SeenMax`, at most one notice more.
- **K45.** Sixteen calls at once and 600 a minute in an enclave; a receiver that takes ten seconds delays no other vendor's notices beyond its share.
- **K46.** Whoever writes the table: a state or due entry deleted delays a notice to the repair; one put back repeats at most a notice; one moved to another key or connection does not open; none sends to another URL or after a revocation.
- **K47.** No identifier the test made (accounts, mailboxes, messages, connections), no address and no zone is in any body the sender was given.
- **K48.** Logs hold kinds and outcomes only; a vendor not verified is `other`.
- **K49.** An account removed: its entries and its mailboxes' index entries gone after the next repair.
- **K50.** `/status`'s counts, every key at zero from the start.

**Routes** (pull request 4)

- **K51.** `POST /api/v1/connections/{id}/webhook/off` calls `WebhookOff` with the version sent, is refused cross-site, and works under unagreed terms.
- **K52.** `GET https://api.permaily.com/.well-known/permaily-webhook-keys`: the set, `public, max-age=3600`, CORS `*`, on the api host only.
- **K53.** `GET /v1/connection` and `connection_info` say the webhook as the service gives it.

**Web** (pull request 5)

- **K54.** The consent screen shows the webhook asked, its host, when, "never the mail", the zone preselected from the browser with its picker, and sends `time_zone`; `time_zone_invalid` shown in words.
- **K55.** The connection page shows the state, the next run in the person's zone, the last notice taken, the failure in words, and the button with its words; turning off on a stale page shows it again.
- **K56.** `api.ts`'s shapes against the contract's JSON.

**The image** (pull request 6)

- **K57.** The composition: the zones from `time/tzdata` in the image (every zone the picker offers loads in an enclave build without zoneinfo), the key derived from the root secret, the sender on port 8004 alone, the workers and the repair started, `Arrivals` called after the mark, dev mode with played vendors only.
- **K58.** `/status`'s `webhooks`, golden; `permaily-verify` fails on each case of "What /status and permaily-verify show", and when the published key set differs from `/status`'s.
- **K59.** `TestMailEndToEnd`'s webhook half: a played app approves a request with a webhook through two enclaves; a message arrives; a signed notice reaches the played vendor, which fetches it with its token, and the receipt is written; a sign-in code arrives and no notice is made; a schedule runs; the connection is revoked and no notice follows; the log holds none of K48's list.
- **K60.** The staging check: an HTTPS receiver on the internet the operator runs receives verified notices of new mail and of a schedule within its minute, and none after a revocation; the numbers recorded in "Cost".
- **K61.** The privacy page, docs/data.md, docs/threat-model.md in present tense, docs/verification.md, docs/operations.md and the developers' page (how to verify a notice).
- **K62.** The release: `permaily-verify` passing with the section, from a workstation and with `-require-smtp` from a server.

## Implementation pull requests

Seven. This one first, alone, with the contract, the pure package, the seams and the fakes. Then 1 to 5 are written at the same time against them; 3 needs only `webhookstest`; 2 runs `webhookstest.Contract` on its own service; 6 last. A release takes all of them: step 6 is done when it is deployed, the staging check (K60) passed and `permaily-verify` of the release passes with the section. Every file has one owner; another pull request edits it only where "May edit outside" names it.

| Pull request | Owns | May edit outside, and only that | Must not touch | Depends on |
|---|---|---|---|---|
| 0. Design and seams (this one) | This record; the committed files above; docs/plan.md's step 6, docs/decisions/index.md, docs/threat-model.md's section, docs/data.md's rows and docs/legal.md's item for this design; internal/AGENTS.md's rows | | Everything else | Nothing |
| 1. The sender | `internal/webhook/send` (new) and its tests (K16 to K22) | `internal/enclave/enclave.go` (`WebhookEgressClient`) and its test; docs/threat-model.md's egress row; `scripts/coverage-thresholds.txt` | `internal/services`, `internal/webhook`'s committed files, `internal/hostproxy` (the vendor egress is unchanged) | 0 |
| 2. The connections' half | `internal/services/connections/webhooks.go` beyond the seam and new files for it; the webhook in `docs.go`, `consent.go`, `person.go`, `read.go` (`VendorConnection`) and `lifecycle.go` (K23 to K34) | `internal/oauth` (the pushed request's `webhook`, through `webhook.ParseRequest`) and its tests; `internal/services/receipts/receipts.go` (`Consent`'s webhook, optional); `scripts/coverage-thresholds.txt` | `internal/services/webhooks` beyond calling `Queue`, `internal/httpapi`, `web` | 0 |
| 3. The queue and the scheduler | `internal/services/webhooks` beyond `webhooks.go` (K35 to K50) | `internal/services/mailboxes/deliver.go` (one call of `Arrivals` after the mark, `Config.Arrivals`) and its test; `scripts/coverage-thresholds.txt` | `internal/services/connections`, `internal/httpapi`, `internal/webhook` except by calling it | 0 |
| 4. Routes | in `internal/httpapi`, the webhook's route and the key set (K51 to K53) | `internal/router` (the well-known path on the api host) | `internal/services` | 0 |
| 5. Web | the consent screen's webhook and zone picker, the connection page's webhook, the calls and words in `web/src/lib/api.ts`, their tests (K54 to K56); docs/web.md | | the server | 0 |
| 6. The image | `cmd/permaily-enclave` (the composition), `cmd/permaily-verify`'s section, `TestMailEndToEnd`'s webhook half; docs/operations.md, docs/verification.md, docs/acceptance.md, the threat model in present tense; `site/src/pages/privacy.astro` and the developers' page (K57 to K62) | `internal/httpapi/status.go` (`StatusSources.Webhooks`); `pkg/attestation/status.go` (a `Check` for the section) | the packages of 1 to 5 except by calling them | 1 to 5 |

**Definition of done of each.** Its tests in "Tests" pass; `make check` passes, coverage thresholds included (`internal/webhook` at 95 from this one, raised by each); internal/AGENTS.md and the docs it owns say what it built; an adversarial review of its code, every finding a test named for it. For 6, also the staging check, the release, and `permaily-verify` of the release passing with `webhooks`.

**What truly runs in parallel.** 1 needs `webhook.Call`, `webhook.Outcome` and `webhooktest.Vendor`. 2 needs the contract, `webhook.ParseRequest`, `webhook.Same`, `webhooks.Queue` (a recorder in its tests) and `webhookstest.Contract`. 3 needs `webhookstest.Fake`, `webhooktest.Sender`, `inbox.Arrivals` and `webhook`'s schedule and notice. 4 needs `connectionstest.Fake.WebhookOff` and `webhooks.KeySet`. 5 needs the contract and a played server. None has to invent a notice, a signature, a schedule's rule, an authority or a bound: each is committed here with its test.

**Estimate.** About three weeks (docs/plan.md said a week and a half): the sender half a week, the connections' half a week, the queue and scheduler a week, routes and web half a week together, the image and the staging check half a week, in parallel where the table allows.

## Options considered

- **The webhook in the vendor's document** (CIMD): per client, not per connection, so a vendor cannot route a notice to its own user by the URL, and a meaning for a member `oauth.Document` ignores today. Kept as an alternative (choice 1).
- **The registrable domain instead of the exact host**: needs the public suffix list in the image (choice 2).
- **HMAC with a secret per connection** (Standard Webhooks' `v1`): a secret to hand through the token answer, keep sealed, lose and rotate per connection; nothing attestable (choice 3).
- **A JWS of the body**: what Standard Webhooks' libraries do not verify; no gain.
- **Mail in the notice**: refused by ADR 0019.
- **A count of matches in the notice**: says how much mail arrived beyond what the vendor can fetch, held mail never included but the timing of a burst; refused.
- **Notify on exact conditions before the rules**: tells the vendor that held mail arrived; refused. **Notify on any new mail**: the same, for every message; refused. **Exact conditions only, no model at arrival**: misses a message that needs the model until the next fetch (choice 5).
- **Evaluate at a scheduled run and skip empty ones**: tells by silence, asks the model on the schedule (choice 6).
- **Cron expressions; several schedules a connection**: unreadable corners, every minute possible (choice 7).
- **An account-wide time zone in Settings**: one more setting and its screen; a zone per connection fixed at consent is what the person saw (choice 8).
- **Every missed run sent**: a burst after a downtime with nothing more to fetch (choice 9).
- **Turning a webhook off on its own after days of failures**: one more state for the person to undo (choice 10).
- **A third listener on the host for notices**: changes nothing a dishonest host could not do; one more port (choice 4).
- **HTTP/1.1 for receivers**: Go's transport logs an idle peer's bytes; HTTP/2 only, as for documents and the model (choice 4).
- **Strict anchored records for the queue**: a ten-year commit per notice for state whose going back costs a repeated notice ("The queue").
- **Memory only**: loses notices in a restart and cannot be shared by two enclaves.
- **A receipt per notice**: a chained write per notice for what is not a disclosure (choice 15).
- **Redirects followed to the same host**: a vendor would rather fix its URL; none followed is what documents do.
- **A notice for MCP clients** (a server-sent notification, MCP's own): not what ADR 0019 decided, and stateless MCP keeps no stream; left for when a client asks.

## Choices made for the owner (to confirm)

Made on 2026-11-20 without the owner, as asked: for each, the one that fails closed and is simplest to build and test. Each can be reversed by a later decision; what reversing costs is said.

1. **The webhook's URL comes from the pushed request only**, so only apps ask for one and MCP clients get none. Alternative: a member of the vendor's document (per client), or both. Reversing: a member read by `oauth.Document` and a rule for when both are given.
2. **On exactly the `client_id`'s host.** Alternative: any host under the registrable domain, with the public suffix list in the image. Reversing: the list, its updates and a test of lookalikes.
3. **Ed25519 under Standard Webhooks' `v1a`, one key from the root secret, published and attested.** Alternative: HMAC (`v1`) with a secret per connection given in the token answer. Reversing: the secret's life and the token answer's shape.
4. **Through the existing vendor egress (8004), HTTP/2 only, no redirect followed.** Alternative: a separate listener; a tested HTTP/1.1 path that logs nothing. Reversing: a receiver that speaks only HTTP/1.1 cannot be called until then.
5. **A notice of new mail only for mail that would leave, the model asked at arrival.** Alternative: only what exact conditions settle (no model at arrival, some notices late until the next fetch). Reversing: cheaper in the model, later for mail that needs it.
6. **A scheduled notice at every run, whatever the mail; no evaluation then.** Alternative: only when something new would leave. Reversing: an evaluation and the model at each run, and a vendor that learns by silence.
7. **Two fixed schedules (every day; every week on one weekday), on quarter hours, one a connection.** Alternative: more choices (weekdays only, twice a day), several a connection, cron. Reversing: more sentences and tests; `CheckSchedule` and `Next` are where.
8. **The person's time zone per connection, from the browser on the consent screen, fixed until a new request.** Alternative: an account-wide zone in Settings that every schedule follows. Reversing: a setting, a notice of the change, and every connection's next run recomputed.
9. **Missed runs make one late notice**, whatever the downtime. Alternative: skip runs missed by more than a set time. Reversing: one bound in `Due`.
10. **Retried for 72 hours, then dropped; never turned off on its own.** Alternative: turned off after several days of failures, the person told. Reversing: a state and the person's notice (step 9).
11. **The person turns a webhook off, without a fresh proof, for good; only a new request turns it on.** Alternative: off and on again on the connection's page, on with a fresh proof. Reversing: a route and a check that turning on is what was consented to.
12. **While the model cannot be asked, no notice of either kind** (scheduled ones skipped too). Alternative: send scheduled notices anyway, the vendor then fetching nothing. Reversing: one reason fewer in `Calls`.
13. **The webhook is accepted or refused with the request; it cannot be removed on the screen.** Alternative: allow without the webhook (as "new mail only" is the one thing a person may change today). Reversing: a draft field, its snapshot and a test.
14. **A 410 stops the webhook for good.** Alternative: treat it as any failure. Reversing: one outcome's meaning.
15. **No receipt per notice**; the approval's and the turning off's consent receipts carry the webhook. Alternative: a `notice` receipt per notice taken, chained as receipts are. Reversing: a kind of receipt and its cost (a chained write per notice).
16. **At most one notice a minute per connection**, everything meanwhile coalesced. Alternative: a shorter or longer interval. Reversing: one constant.

## Open questions

1. **Who uses it first.** No client of the five at launch takes webhooks (MCP clients send no pushed request), and no app has asked yet. Building step 6 now, as docs/plan.md orders, or after a first app asks, is the owner's call; this design stands either way.
2. **The developers' page** (`https://permaily.com/developers/`) is where a vendor reads how to ask for a webhook and verify a notice; it does not exist yet (ADR 0030 named it in the server's metadata). Pull request 6 needs it, or this step ships without a public description.
3. **Zones' rules** follow the Go toolchain's `time/tzdata`; a country that changes its rules with little notice is served late until a release. Whether that needs its own release process (a toolchain update alone) is for docs/releasing.md.
4. **Step 9's notices by mail** may want to tell the person when a webhook stopped (410) or has failed for 72 hours; this design shows it on the connection's page only.

## What it rests on

The vendor's TLS, checked in the enclave with the image's roots; Ed25519 and the root secret's confidentiality (ADR 0013); the strict records of connections and the account's list (ADR 0016, 0030) for every authority; the default rule and the decisions kept in strict records (ADR 0031) for what a notice of new mail may say; the Go toolchain's `time/tzdata` for the zones' rules; and Standard Webhooks' format, which vendors' libraries verify.
