/**
 * The web app's own API, under /api/v1/ on the app's host. The session is a cookie only the server can read,
 * so nothing here holds one. Requests are same origin and JSON, which is what the server lets through.
 */

export type Account = {
  id: string
  /** `closing` once the person asked to delete the account, until its day comes or they cancel. */
  state: 'active' | 'closing'
  address: string
  /** Empty when the person gave none. */
  name: string
  language: string
  created_at: string
  /** When a closing account is removed. There only while it is closing. */
  closes_at?: string
  /**
   * Who asked for the deletion, while it is closing: the person, or Permaily's operator. A server from before
   * says nothing, and anything but `operator` reads as the person. Either way the person can cancel.
   */
  closing_by?: 'person' | 'operator'
  /**
   * Whether the account is one of the service's admins: `super` for the one super admin, `admin` for the ones
   * it made. There also while the account is not "passkey only", when the admin panel does not work.
   */
  admin?: 'super' | 'admin'
  /**
   * Mail no longer signs in to the account: passkeys do, and recovery codes. The admin panel works only while
   * it is on. A server from before says nothing, which reads as off.
   */
  passkey_only?: boolean
  /**
   * The version of the terms of service and the privacy policy the person agreed to. Absent when they agreed
   * to none, as an account made before there were terms.
   */
  terms?: string
  /**
   * The version of the model's notice the person agreed to (ADR 0028): what is sent to OpenAI to decide the
   * conditions a model decides, and what OpenAI may keep. Absent when they agreed to none.
   */
  model_notice?: string
}

export type Session = {
  id: string
  created_at: string
  last_used_at: string
  expires_at: string
  device: string
  current: boolean
  /**
   * Until when the person's proof of who they are is fresh: a sign-in, a passkey or a code by mail, for ten
   * minutes. Absent when it is not. The changes that decide who gets into an account need it.
   */
  confirmed_until?: string
}

export type Me = {
  account: Account
  session: Session
  /**
   * The version of the terms in force. When the account's is another, the person agrees to this one before
   * anything else of the app. A server from before says nothing, and nothing is asked.
   */
  terms?: string
}

export type Passkey = {
  /** The credential's identifier, as the browser names it: base64url without padding. */
  id: string
  name: string
  created_at: string
  /** Kept by a provider that copies it to the person's other devices. */
  synced: boolean
}

export type PasskeyList = {
  passkeys: Passkey[]
  /** Mail no longer signs in to the account: passkeys do, and recovery codes. */
  passkey_only: boolean
  recovery_codes_left: number
}

/**
 * A passkey being made or used, between the options the browser takes and its answer. `ceremony` names it
 * on the server, which keeps it for a few minutes and takes one answer. Neither is ever stored here.
 */
export type PasskeyCeremony<Options> = { ceremony: string; options: Options }

export type EmailStarted = {
  /** Names the sign-in that was started. The code is entered against it. */
  attempt: string
  /** Seconds the code and the link work for. */
  expires_in: number
}

/** The providers a person can sign in with, in the order they are always offered in. */
export const providers = ['google', 'github', 'meta', 'microsoft'] as const

export type Provider = (typeof providers)[number]

/** Where to send the browser to sign in at a provider, and for how many seconds its answer is taken. */
export type ProviderStarted = { url: string; expires_in: number }

/**
 * A sign-in with a provider that waits in this browser: the provider said who the person is there, and not
 * that an address is theirs. `address` is what the provider gave, if anything: it proves nothing.
 */
export type ProviderPending = {
  provider: Provider
  address: string
  /** The path of the app the person was going to. */
  return_to: string
  /** Seconds left to confirm an address. */
  expires_in: number
}

/** A provider account that signs in to the account. Nothing else of it is kept, so nothing else is shown. */
export type LinkedProvider = { provider: Provider; linked_at: string }

/**
 * Mailboxes and the inbox (ADR 0026), the shapes of `internal/contracts/mailboxes.go`. The calls that use them
 * come with the screens that do.
 */

/**
 * `waiting` until mail other than Gmail's confirmations arrives; `paused` while Permaily's operator stops it;
 * `unreadable` when its record can't be read: it shows only its id and `created_at`, and can only be disconnected.
 */
export type MailboxState = 'waiting' | 'receiving' | 'paused' | 'unreadable'

/** A forwarding confirmation Gmail sent to the mailbox. Anyone can send one that looks like it. */
export type ForwardingConfirmation = {
  /** Empty when the message carries none (Gmail's carry none since 2026), and when `for_another_address`. */
  code: string
  /** The Gmail account that asked, as the message says. */
  requester?: string
  received_at: string
  /** Google signed it and it names this mailbox: Google confirms that `requester` asked to forward here. */
  verified: boolean
  /** Google signed it for another forwarding address, and someone sent it here: it says nothing of this mailbox. */
  for_another_address?: boolean
  /** The message that carried it, in the mailbox's inbox. */
  message?: string
  /**
   * The link that confirms the request, read from the message: only an https URL on `mail-settings.google.com`
   * under `/mail/vf-`. Absent when it carries none, and when `for_another_address`. Shown as text to copy, never
   * in an `href`.
   */
  link?: string
}

export type Mailbox = {
  id: string
  /** Where the person forwards their mail, at inbox.permaily.com. Empty when `unreadable`. */
  address: string
  /** The person's own name for it. Empty when they gave none. */
  label: string
  state: MailboxState
  created_at: string
  /** Days mail received from now on is kept, one of `retention_options`. */
  retention_days: number
  retention_options: number[]
  /** When the first mail that was not a confirmation arrived: nothing from before it is here. */
  receiving_since?: string
  paused_at?: string
  /**
   * The first Gmail account Google confirmed asked to forward here, while the mailbox waited. Not that forwarding is
   * on, nor that later mail comes from it. Absent: shown as not verified.
   */
  source?: { provider: 'gmail'; address: string; verified_at: string }
  /** The newest first, ten at most, for thirty days each. */
  confirmations: ForwardingConfirmation[]
}

export type MailboxList = { mailboxes: Mailbox[]; limit: number }

export type MailAddress = { name?: string; address: string }

export type MessageSummary = {
  /** Time-ordered: a later message has a greater one. */
  id: string
  received_at: string
  expires_at: string
  size: number
  from: MailAddress[]
  subject: string
  snippet: string
  /** How many attachments it has. */
  attachments: number
  confirmation: boolean
}

/**
 * A page reads a bounded number of stored items (500 for a list, 2,000 for a search), so it may hold fewer messages
 * than asked for, or none, and still have `next`: show "Older" or "Search older mail" whenever `next` is there.
 */
export type MessagePage = {
  messages: MessageSummary[]
  /** What to ask the next page with (`after`): the last item the page read. Absent when nothing older is left. */
  next?: string
  /** For a search: when the oldest message this page looked at arrived; absent when it looked at none. */
  searched_to?: string
}

export type MessageAttachment = { index: number; name: string; type: string; size: number }

export type Message = {
  id: string
  received_at: string
  expires_at: string
  size: number
  from: MailAddress[]
  to: MailAddress[]
  cc: MailAddress[]
  reply_to: MailAddress[]
  subject: string
  /** The Date header as the sender wrote it. */
  date: string
  /** The text, always: 256 KiB at most. The text view and search use it. */
  text: string
  text_from: 'plain' | 'html' | 'none'
  text_truncated: boolean
  attachments: MessageAttachment[]
  /** DKIM checks: `result` is `pass` or `fail: <reason>`. */
  auth: { method: string; domain: string; result: string }[]
  confirmation: boolean
  /** Not readable as mail: only the raw message can be downloaded. */
  unparsed: boolean
  /** Its HTML, when it has a text/html part. Absent: show the text, without the switch. */
  html?: MessageHTML
}

/**
 * What the app is told of a message's HTML (ADR 0026, "Showing a message's HTML"). The HTML itself is never here:
 * when `status` is `shown`, a sandboxed frame loads it from `.../messages/{message}/html`, a new frame per message.
 */
export type MessageHTML = {
  /**
   * `too_large` and `unreadable`: show the text, say why, offer the original. `unavailable`: the enclave could not
   * render it now (busy, or past its deadline); show the text and offer to try again.
   */
  status: 'shown' | 'too_large' | 'unreadable' | 'unavailable'
  /** Images the message would have fetched from elsewhere, never loaded: "3 remote images were not loaded". */
  remote_images: number
  /** Other remote references taken out: stylesheets, fonts, frames, media. */
  remote_other: number
  /** Images that travel in the message and are shown; and the ones not shown, with why in `inline_images_omitted_by`. */
  inline_images: number
  inline_images_omitted: number
  inline_images_omitted_by: MessageImagesOmitted
  /** Links that open the link page. */
  links: number
}

/** Why images that travel in a message were not shown; the three add up to `inline_images_omitted`. */
export type MessageImagesOmitted = {
  /** Over a limit of bytes, of a side, of pixels, or of how many a message shows. */
  too_large: number
  /** An animated GIF, PNG or WebP: none is shown. */
  animated: number
  /** Not PNG, JPEG, GIF or WebP by its bytes (SVG included), a part that is not there, or one that does not read. */
  unsupported: number
}

/**
 * Where a link of a message leads, for the link page (`GET .../messages/{message}/links/{link}`). The page parses `url`
 * with `new URL()`, shows and goes where that parse says, and offers nothing when its `host` is not this `host`.
 */
export type MessageLink = {
  /** The link's token as the route names it, `<n>.<tag>`: a renumbered link answers `link_changed`, never another address. */
  link: string
  /** http or https, ASCII, as the server wrote it again: the only address the link page leaves for. */
  url: string
  /** Its host (`xn--` for other characters, so a look-alike shows) and `:port` when not the default, as `URL.host` writes it. */
  host: string
  /** The host the link's text named, when it named another one. */
  shown?: string
}

/** Whether `link` has the shape of a link's token: a number below 1,000 without leading zeros, a dot, 16 base64url characters. */
export function isMessageLinkToken(link: string): boolean {
  return /^(0|[1-9][0-9]{0,2})\.[A-Za-z0-9_-]{16}$/.test(link)
}

/**
 * The query language (ADR 0027), the shapes of `internal/contracts/query.go`. A formula is an AND of groups, each
 * an OR of conditions; an empty `all` matches every message. The calls that use them come with the screens that do.
 */

export type QueryVariable =
  | 'from_address'
  | 'from_domain'
  | 'recipient_address'
  | 'recipient_domain'
  | 'signed_by'
  | 'subject'
  | 'body'
  | 'message'
  | 'attachments'
  | 'attachment_name'
  | 'attachment_type'
  | 'list'
  | 'received'
  | 'size'
  | 'mailbox'

/** `about` and `mentions` are decided by a model; every other operator is exact. */
export type QueryOperator =
  | 'is'
  | 'contains'
  | 'exists'
  | 'since'
  | 'before'
  | 'at_least'
  | 'below'
  | 'about'
  | 'mentions'

/** A variable, an operator and a value (absent for `exists`), negated when `not` is set. */
export type QueryCondition = {
  var: QueryVariable
  op: QueryOperator
  value?: string
  not?: boolean
}

export type QueryFormula = { v: 1; all: { any: QueryCondition[] }[] }

/** Where a formula was refused and why, in the `details` of an `invalid_formula` error. Never a value of it. */
export type FormulaProblem = { at: string; reason: string }

/**
 * Why the person's `invalid_client` refused a vendor's request (ADR 0030, `contracts.ClientProblem`): one of three fixed
 * reasons, never the vendor's words or address. Pull request 8 gives each its words.
 */
export const CLIENT_PROBLEMS = [
  'client_id_invalid',
  'client_document_invalid',
  'redirect_uri_invalid',
] as const
export type ClientProblem = (typeof CLIENT_PROBLEMS)[number]

/** The `details` of an `invalid_client` error: its reason alone, with no `at`. */
export type ClientProblemDetails = { reason: ClientProblem }

/**
 * The `details` of an API error (`contracts.ErrorDetails`): with `invalid_formula` a `FormulaProblem`, which always has
 * `at`; with `invalid_client` a `ClientProblemDetails`, which never has it.
 */
export type ErrorDetails = FormulaProblem | ClientProblemDetails

/** The reason in an `invalid_client` answer's `details`, when it is one of the three and there is no `at`. */
export function clientProblemOf(details: unknown): ClientProblem | undefined {
  if (typeof details !== 'object' || details === null || 'at' in details) return undefined
  const { reason } = details as { reason?: unknown }
  return CLIENT_PROBLEMS.find((p) => p === reason)
}

/**
 * Fixed words of a template, or a value of the formula (`value`), shown set apart and isolated. `model` marks the
 * parts of a condition a model decides. A mailbox is a value part: its label, its address, or, once disconnected,
 * its identifier between the fixed parts "a mailbox you disconnected (" and ")".
 */
export type SentencePart = { text: string; value?: boolean; model?: boolean }

/**
 * What a person reads of a formula, written by the server: show `lead` and `lines`, never words of the app's own.
 * With one group `lead` is empty and the one line is the whole sentence.
 */
export type QuerySentence = {
  lang: string
  text: string
  lead: SentencePart[]
  lines: { parts: SentencePart[] }[]
}

/** A formula the server accepted: its canonical form, its identifier, its sentence, whether a model decides any of it. */
export type FormulaCheck = {
  formula: QueryFormula
  id: string
  sentence: QuerySentence
  model: boolean
}

/**
 * A page of what a formula matches in one mailbox, for the owner's own previews only (never a vendor's result: its
 * `next`, `searched_to`, `needs_model` and `unknown` name messages outside any approved scope). `needs_model` and
 * `unknown` are messages it did not settle; `next` goes on below the last message the page considered.
 */
export type MatchPage = {
  messages: MessageSummary[]
  needs_model: string[]
  unknown: string[]
  next?: string
  searched_to?: string
  /**
   * Only when the request asked the model (ADR 0028): `off` when the model has no key or the provider refused it,
   * otherwise `limit` when a spend limit held some back, otherwise `asked`. Held messages stay in `needs_model`.
   */
  model?: MatchModel
  /** Only on the consent screen's preview with the model (ADR 0030): what a rule holds back. Not in `messages`. */
  held?: string[]
}

/** What became of asking the model on a page of matches (ADR 0028). */
export type MatchModel = 'asked' | 'limit' | 'off'

/*
  Connections (ADR 0030). The shapes of internal/contracts/connections.go; the calls are below the mailboxes' ("Connections
  (ADR 0030)"), and each refusal is an `ApiErrorCode` with its words in src/pages/auth/messages.ts.
*/

/** A part of a message a connection reads. Every connection reads `sender`; `attachments` only when asked for. */
export type ConnectionPart = 'sender' | 'subject' | 'body' | 'attachments'

/** What a connection's tokens are for: the API for apps, or MCP for agents. */
export type ConnectionResource = 'api' | 'mcp'

/**
 * Who asks: the host of its document's URL, and the owner the image's list names for it. Nothing else of a vendor is
 * believed; `client_name` is its own claim.
 */
export type ConnectionVendor = { domain: string; verified?: string }

/** New mail only (from the approval, `at` then set in a connection), mail since `at`, or with neither all the mail kept. */
export type MailSince = { new: boolean; at?: string }

/** A rule every connection's reads apply first, and its sentence. */
export type RuleSummary = { id: string; version: number; sentence: QuerySentence[] }

/**
 * The scope the person is about to approve: sent whole with every preview and with the decision. `mailboxes` is never
 * empty and never means all. New mail only is mail received from the approval on, so its preview is empty.
 */
export type ConsentDraft = { mailboxes: string[]; since: MailSince }

/**
 * What approving does to the client's connection: makes one, adds this request as another device of it (the same scope
 * or a narrower one; its other devices keep working), or replaces it (any other scope; its tokens stop).
 */
export type ConnectionChange = 'new' | 'add_device' | 'replace'

/** What the consent screen shows of a vendor's request. Without `query`, the person chooses the mailboxes. */
export type AuthorizationRequest = {
  id: string
  vendor: ConnectionVendor
  client_name: string
  redirect_host: string
  /** The browser goes back to this computer: any program on it can use this vendor's name. */
  loopback: boolean
  resource: ConnectionResource
  parts: ConnectionPart[]
  query?: FormulaCheck
  since: MailSince
  ends_in_days: number
  /** The webhook the vendor asked for (ADR 0039), accepted or refused with the request; absent without one. */
  webhook?: WebhookAsked
  /** What the picker offers. */
  mailboxes: string[]
  /** Where the screen starts: every mailbox and new mail only, or the client's connection's. */
  draft: ConsentDraft
  rules: RuleSummary[]
  /** The client's active connection, and what approving the starting draft does to it. */
  connection?: Connection
  change: ConnectionChange
  /** What approving the starting draft does, as shown: sent back with the approval. */
  snapshot: ConsentSnapshot
  /** Sent back with an approval; refused (`model_notice_changed`) when another is in force. */
  model_notice: string
  expires_at: string
}

/** A request to show, or where to send the browser back with the vendor's error. Exactly one is set. */
export type AuthorizationStarted = { request?: AuthorizationRequest; redirect?: string }

/** A page of what a request would read under `draft` in one of its mailboxes; `after` needs the page before's `draft_id`. */
export type ConsentPreview = {
  draft: ConsentDraft
  draft_id?: string
  mailbox: string
  after?: string
  limit?: number
  ask_model?: boolean
}

/** A preview's answer: the draft's identifier, what approving it does (and its snapshot), and the page. */
export type ConsentPreviewPage = {
  draft_id: string
  change: ConnectionChange
  snapshot: ConsentSnapshot
  page: MatchPage
}

/**
 * What the screen showed approving would do (ADR 0030, N5): the client's connection and its record's version, the change
 * and the end. An approval sends it back; when what approving does has changed since, the server answers
 * `consent_changed` and commits nothing, and the screen loads the request again for a new confirmation.
 */
export type ConsentSnapshot = {
  connection?: string
  version?: number
  change: ConnectionChange
  ends_at: string
  draft_id: string
}

/** The person's decision: an approval sends the draft, the notice and the snapshot the screen showed. */
export type AuthorizationDecision = {
  allow: boolean
  draft?: ConsentDraft
  model_notice?: string
  snapshot?: ConsentSnapshot
  /** The person's IANA time zone, required to allow a request whose webhook has a schedule (`time_zone_invalid`). */
  time_zone?: string
}

/** A renewal: the notice and the connection's version the screen showed (`consent_changed` when it changed since). */
export type ConnectionRenewal = { model_notice: string; version: number }

/**
 * The refusals an approval or a renewal may answer that the consent screen and the connections page handle (each an
 * `ApiErrorCode`, with its words): a fresh proof needed, the notice or what approving does
 * changed since the screen was shown (both reload it), the connection's 16 devices (the person removes one on its page;
 * none is removed for them), the account's connections, a request that ended.
 */
export const CONSENT_REFUSALS = [
  'confirmation_needed',
  'model_notice_changed',
  'consent_changed',
  'too_many_devices',
  'too_many_connections',
  'request_ended',
  'connection_ended',
] as const
export type ConsentRefusal = (typeof CONSENT_REFUSALS)[number]

/** Where the browser goes back to the vendor, and nowhere else. */
export type AuthorizationAnswer = { redirect: string }

export type ConnectionState = 'active' | 'ended'

export type EndReason = 'person' | 'vendor' | 'reuse' | 'replaced' | 'account' | 'expired'

/** Whether a connection gets mail now: nothing leaves while the model cannot be asked or a new notice waits for the person. */
export type ConnectionDelivery = 'delivering' | 'model_unavailable' | 'model_notice_changed'

/** How often a scheduled notice is sent (ADR 0039): every day, or every week on one weekday. */
export type WebhookEvery = 'day' | 'week'

/** When a scheduled notice is sent, as wall time in the person's zone: `at` is "HH:MM" on a quarter hour; `on` a weekday in lower case for `week`. */
export type WebhookSchedule = { every: WebhookEvery; on?: string; at: string }

/** What the consent screen shows of a request's webhook: the host it is called on (the vendor's own) and when. */
export type WebhookAsked = { host: string; new_mail?: boolean; schedule?: WebhookSchedule }

/** Whether a connection's webhook is called: on, turned off by the person, or stopped by the vendor (both final). */
export type WebhookState = 'on' | 'off' | 'stopped'

/** Why the last calls of a webhook failed, in fixed words. */
export type WebhookFailure = 'no_answer' | 'error' | 'redirect' | 'refused'

/** A connection's webhook as the person's app shows it. */
export type ConnectionWebhook = {
  host: string
  new_mail?: boolean
  schedule?: WebhookSchedule
  time_zone?: string
  state: WebhookState
  next_at?: string
  last_sent_at?: string
  failing_since?: string
  failure?: WebhookFailure
}

/** One device of a connection: the tokens one approval started, with the scope confirmed for it. */
export type ConnectionDevice = {
  id: string
  made_at: string
  state: ConnectionState
  parts: ConnectionPart[]
  narrower?: boolean
  ended_at?: string
  end_reason?: EndReason
}

/** A connection as the person's app shows it. */
export type Connection = {
  id: string
  vendor: ConnectionVendor
  client_id: string
  client_name: string
  resource: ConnectionResource
  state: ConnectionState
  parts: ConnectionPart[]
  query?: FormulaCheck
  mailboxes: string[]
  since: MailSince
  made_at: string
  ends_at: string
  ended_at?: string
  end_reason?: EndReason
  replaced_by?: string
  devices: ConnectionDevice[]
  model_notice: string
  delivery: ConnectionDelivery
  /** Its webhook (ADR 0039), absent when it has none. */
  webhook?: ConnectionWebhook
  /** Its record's version, which a renewal sends back. */
  version: number
}

/** The account's connections: the active ones and the ones that ended in the last 30 days. */
export type ConnectionList = { connections: Connection[] }

/** The codes the server answers with, plus `network` for a request that got no answer. */
export type ApiErrorCode =
  | 'invalid_address'
  | 'invalid_invitation'
  | 'invalid_code'
  | 'too_many_requests'
  | 'unauthorized'
  | 'not_found'
  | 'invalid_passkey'
  | 'invalid_name'
  | 'too_many_passkeys'
  | 'last_passkey'
  | 'passkey_needed'
  | 'passkey_only_off'
  | 'account_closing'
  | 'provider_off'
  | 'provider_failed'
  | 'provider_unavailable'
  | 'provider_in_use'
  | 'provider_linked'
  | 'invitation_needed'
  | 'last_way_in'
  | 'account_blocked'
  | 'address_in_use'
  | 'confirmation_needed'
  | 'passkey_required'
  | 'passkey_only_needed'
  | 'super_admin_only'
  | 'not_cancellable'
  | 'invalid_provider_app'
  | 'terms_needed'
  | 'model_notice_needed'
  | 'too_many_mailboxes'
  | 'invalid_label'
  | 'invalid_retention'
  | 'invalid_query'
  | 'invalid_formula'
  | 'too_many_messages'
  | 'mailbox_unreadable'
  | 'link_changed'
  | 'busy'
  | 'invalid_client'
  | 'vendor_unreachable'
  | 'request_ended'
  | 'request_too_large'
  | 'connection_ended'
  | 'model_notice_changed'
  | 'consent_changed'
  | 'too_many_devices'
  | 'too_many_connections'
  | 'invalid_cursor'
  | 'invalid_before'
  | 'receipts_unreadable'
  | 'network'
  | 'other'

const known: ReadonlySet<string> = new Set([
  'invalid_address',
  'invalid_invitation',
  'invalid_code',
  'too_many_requests',
  'unauthorized',
  'not_found',
  'invalid_passkey',
  'invalid_name',
  'too_many_passkeys',
  'last_passkey',
  'passkey_needed',
  'passkey_only_off',
  'account_closing',
  'provider_off',
  'provider_failed',
  'provider_unavailable',
  'provider_in_use',
  'provider_linked',
  'invitation_needed',
  'last_way_in',
  'account_blocked',
  'address_in_use',
  'confirmation_needed',
  'passkey_required',
  'passkey_only_needed',
  'super_admin_only',
  'not_cancellable',
  'invalid_provider_app',
  'terms_needed',
  'model_notice_needed',
  'too_many_mailboxes',
  'invalid_label',
  'invalid_retention',
  'invalid_query',
  'invalid_formula',
  'too_many_messages',
  'mailbox_unreadable',
  'link_changed',
  'busy',
  'invalid_client',
  'vendor_unreachable',
  'request_ended',
  'request_too_large',
  'connection_ended',
  'model_notice_changed',
  'consent_changed',
  'too_many_devices',
  'too_many_connections',
  'invalid_cursor',
  'invalid_before',
  'receipts_unreadable',
])

export class ApiError extends Error {
  readonly code: ApiErrorCode
  /** Where a refused formula was refused and why: only with `invalid_formula`, and only when the server said both. */
  readonly problem?: FormulaProblem
  constructor(code: ApiErrorCode, problem?: FormulaProblem) {
    super(code)
    this.name = 'ApiError'
    this.code = code
    if (problem) this.problem = problem
  }
}

/** The `details` of an `invalid_formula` answer, when they have the shape of one: a path and a reason, both text. */
function problemOf(details: unknown): FormulaProblem | undefined {
  if (typeof details !== 'object' || details === null) return undefined
  const { at, reason } = details as { at?: unknown; reason?: unknown }
  return typeof at === 'string' && typeof reason === 'string' ? { at, reason } : undefined
}

/** The code of a failure, whatever was thrown. */
export function errorCode(error: unknown): ApiErrorCode {
  return error instanceof ApiError ? error.code : 'other'
}

let closingHeard: (() => void) | null = null

/**
 * Whom to tell that a request was refused because the account is being deleted: the session, which then shows
 * that and nothing else. Any request can be the one that finds out, such as a change made in a tab that was
 * open before the deletion was asked for in another.
 */
export function onAccountClosing(listener: (() => void) | null) {
  closingHeard = listener
}

let termsHeard: (() => void) | null = null

/**
 * Whom to tell that a request of someone signed in was refused because the terms changed and they did not
 * agree to the new ones yet: the session, which asks again and shows the screen that asks for the agreement.
 */
export function onTermsNeeded(listener: (() => void) | null) {
  termsHeard = listener
}

/** How long a request may take before it counts as no answer. */
const timeout = 20_000

async function request<T>(
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  body?: unknown,
): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      method,
      credentials: 'same-origin',
      cache: 'no-store',
      headers:
        body === undefined
          ? { Accept: 'application/json' }
          : { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(timeout),
    })
  } catch {
    throw new ApiError('network')
  }
  if (response.ok) {
    if (response.status === 204) return undefined as T
    try {
      return (await response.json()) as T
    } catch {
      throw new ApiError('other')
    }
  }
  let code: unknown
  let details: unknown
  try {
    const error = (await response.json())?.error
    code = error?.code
    details = error?.details
  } catch {
    // An answer that is not the API's own, such as a proxy's error page.
  }
  if (code === 'account_closing') closingHeard?.()
  if (code === 'terms_needed' && response.status === 400) termsHeard?.()
  if (code === 'invalid_formula') throw new ApiError('invalid_formula', problemOf(details))
  if (typeof code === 'string' && known.has(code)) throw new ApiError(code as ApiErrorCode)
  if (response.status === 401) throw new ApiError('unauthorized')
  if (response.status === 404) throw new ApiError('not_found')
  if (response.status === 429) throw new ApiError('too_many_requests')
  throw new ApiError('other')
}

/**
 * Asks for the sign-in message. The answer is the same whether the address has an account or not. With an
 * invitation it is a sign-up, which carries the version of the terms the person agreed to.
 */
export function startEmail(
  address: string,
  invitation?: string,
  terms?: string,
): Promise<EmailStarted> {
  return request(
    'POST',
    '/api/v1/auth/email/start',
    invitation ? { address, invitation, ...(terms ? { terms } : {}) } : { address },
  )
}

/**
 * Asks whose sign-in the link of a message would end. It ends nothing: the link still works afterwards, once.
 * The address shown to the person is this answer, never anything read from the link.
 */
export async function lookEmail(attempt: string, token: string): Promise<{ address: string }> {
  const answer = await request<{ address?: unknown }>('POST', '/api/v1/auth/email/look', {
    attempt,
    token,
  })
  if (typeof answer?.address !== 'string' || answer.address === '') throw new ApiError('other')
  return { address: answer.address }
}

/** Ends a sign-in with the code of the message or the token of its link. The server sets the session cookie. */
export function finishEmail(
  attempt: string,
  proof: { code: string } | { token: string },
): Promise<Me> {
  return request('POST', '/api/v1/auth/email/finish', { attempt, ...proof })
}

export function me(): Promise<Me> {
  return request('GET', '/api/v1/me')
}

export function signOut(): Promise<void> {
  return request('POST', '/api/v1/auth/sign-out')
}

/** An account as the server answered it, or a failure: a change is never taken from an answer that is not one. */
async function account(answer: Promise<Partial<Account> | null>): Promise<Account> {
  const got = await answer
  if (typeof got?.id !== 'string' || typeof got.address !== 'string') throw new ApiError('other')
  return { ...(got as Account), name: typeof got.name === 'string' ? got.name : '' }
}

/** Changes the account's name. An empty one takes the name away. */
export function renameAccount(name: string): Promise<Account> {
  return account(request('PATCH', '/api/v1/account', { name }))
}

/**
 * Asks to delete the account. It is closing from now, every other device is signed out, and it is removed
 * when `closes_at` comes. Asking again changes nothing: the day stays.
 */
export function deleteAccount(): Promise<Account> {
  return account(request('POST', '/api/v1/account/delete'))
}

/** Agrees to the terms in force, by the version the person was shown. */
export function agreeTerms(version: string): Promise<Account> {
  return account(request('POST', '/api/v1/account/terms', { version }))
}

/**
 * Agrees to the model's notice in force (`MODEL_NOTICE_VERSION` in src/lib/model-notice.ts), by the version the
 * person was shown (ADR 0028): what asking the model about their mail needs. Another version is
 * `model_notice_needed`.
 */
export function agreeModelNotice(version: string): Promise<Account> {
  return account(request('POST', '/api/v1/account/model-notice', { version }))
}

/** Keeps an account that was closing: it is active again, as it was. */
export function cancelDeletion(): Promise<Account> {
  return account(request('POST', '/api/v1/account/delete/cancel'))
}

/**
 * Asks to change the account's contact address: the server sends a six-digit code to the new one, and no
 * link. The answer is the same whether or not the address belongs to someone else. Nothing changes yet.
 */
export function startAddressChange(address: string): Promise<EmailStarted> {
  return request('POST', '/api/v1/account/address/start', { address })
}

/**
 * Ends the change with the code of that message. The answer is the account with its new address; the server
 * tells the old address and the new one. Only here, after the code, does it say that the address belongs
 * to another account (`address_in_use`).
 */
export function finishAddressChange(attempt: string, code: string): Promise<Account> {
  return account(request('POST', '/api/v1/account/address/finish', { attempt, code }))
}

/** Starts a sign-in with a passkey. Nothing is asked: the passkey says which account it is for. */
export function startPasskeySignIn(): Promise<
  PasskeyCeremony<PublicKeyCredentialRequestOptionsJSON>
> {
  return request('POST', '/api/v1/auth/passkey/start')
}

/** Ends a sign-in with the browser's answer. The server sets the session cookie. */
export function finishPasskeySignIn(ceremony: string, credential: unknown): Promise<Me> {
  return request('POST', '/api/v1/auth/passkey/finish', { ceremony, credential })
}

/**
 * Signs in with the account's address and one recovery code, which works once. Every refusal is the same
 * answer, whether the address has an account or not.
 */
export function recover(address: string, code: string): Promise<Me> {
  return request('POST', '/api/v1/auth/recovery', { address, code })
}

/** The account's passkeys, whether it signs in with passkeys only, and how many recovery codes are left. */
export async function passkeys(): Promise<PasskeyList> {
  const answer = await request<Partial<PasskeyList> | null>('GET', '/api/v1/passkeys')
  if (!Array.isArray(answer?.passkeys)) throw new ApiError('other')
  return {
    passkeys: answer.passkeys,
    passkey_only: answer.passkey_only === true,
    recovery_codes_left:
      typeof answer.recovery_codes_left === 'number' ? answer.recovery_codes_left : 0,
  }
}

/** Starts making a passkey for the account that is signed in. */
export function startPasskey(): Promise<PasskeyCeremony<PublicKeyCredentialCreationOptionsJSON>> {
  return request('POST', '/api/v1/passkeys/start')
}

/** Ends making a passkey with the browser's answer and the name the person gave it. */
export function finishPasskey(
  ceremony: string,
  name: string,
  credential: unknown,
): Promise<Passkey> {
  return request('POST', '/api/v1/passkeys/finish', { ceremony, name, credential })
}

export function renamePasskey(id: string, name: string): Promise<Passkey> {
  return request('PATCH', `/api/v1/passkeys/${encodeURIComponent(id)}`, { name })
}

/** Takes a passkey out of the account. The server refuses the last one while "passkey only" is on. */
export function removePasskey(id: string): Promise<void> {
  return request('DELETE', `/api/v1/passkeys/${encodeURIComponent(id)}`)
}

/**
 * Turns "passkey only" on or off. Turning it on answers with the recovery codes, this once:
 * the server keeps only their hash.
 */
export async function setPasskeyOnly(
  on: boolean,
): Promise<{ passkey_only: boolean; recovery_codes: string[] }> {
  const answer = await request<{ passkey_only?: unknown; recovery_codes?: unknown } | null>(
    'POST',
    '/api/v1/account/passkey-only',
    { on },
  )
  return { passkey_only: answer?.passkey_only === true, recovery_codes: codes(answer) }
}

/** Makes new recovery codes, shown this once. The ones before them stop working. */
export async function newRecoveryCodes(): Promise<string[]> {
  const made = codes(
    await request<{ recovery_codes?: unknown } | null>('POST', '/api/v1/account/recovery-codes'),
  )
  if (made.length === 0) throw new ApiError('other')
  return made
}

function codes(answer: { recovery_codes?: unknown } | null): string[] {
  const list = answer?.recovery_codes
  return Array.isArray(list) ? list.filter((code): code is string => typeof code === 'string') : []
}

/** Every device signed in to the account. */
export async function sessions(): Promise<Session[]> {
  const answer = await request<{ sessions?: unknown } | null>('GET', '/api/v1/sessions')
  if (!Array.isArray(answer?.sessions)) throw new ApiError('other')
  return answer.sessions as Session[]
}

/** Signs one device out. */
export function closeSession(id: string): Promise<void> {
  return request('DELETE', `/api/v1/sessions/${encodeURIComponent(id)}`)
}

function isProvider(name: unknown): name is Provider {
  return (providers as readonly unknown[]).includes(name)
}

/** The providers that are on, in the order they are offered in. One this app does not know is left out. */
export async function providersOn(): Promise<Provider[]> {
  const answer = await request<{ providers?: unknown } | null>('GET', '/api/v1/auth/providers')
  if (!Array.isArray(answer?.providers)) throw new ApiError('other')
  const on: unknown[] = answer.providers
  return providers.filter((provider) => on.includes(provider))
}

function started(answer: Partial<ProviderStarted> | null): ProviderStarted {
  if (typeof answer?.url !== 'string' || answer.url === '') throw new ApiError('other')
  return {
    url: answer.url,
    expires_in: typeof answer.expires_in === 'number' ? answer.expires_in : 0,
  }
}

/**
 * Starts a sign-in with a provider. The answer is where to send the browser; the server gives this browser a
 * cookie, and takes the provider's answer only where that cookie is. `invitation` is for whoever has no account
 * yet, and `returnTo` is the path of the app to come back to.
 */
export async function startProvider(
  provider: Provider,
  how: { invitation?: string; returnTo?: string; terms?: string } = {},
): Promise<ProviderStarted> {
  return started(
    await request('POST', '/api/v1/auth/provider/start', {
      provider,
      ...(how.invitation ? { invitation: how.invitation } : {}),
      ...(how.returnTo ? { return_to: how.returnTo } : {}),
      ...(how.invitation && how.terms ? { terms: how.terms } : {}),
    }),
  )
}

/** What waits in this browser for an address to be confirmed. `provider_failed` when nothing does. */
export async function pendingProvider(): Promise<ProviderPending> {
  const answer = await request<Partial<ProviderPending> | null>(
    'GET',
    '/api/v1/auth/provider/pending',
  )
  if (!isProvider(answer?.provider)) throw new ApiError('other')
  return {
    provider: answer.provider,
    address: typeof answer.address === 'string' ? answer.address : '',
    return_to: typeof answer.return_to === 'string' ? answer.return_to : '/',
    expires_in: typeof answer.expires_in === 'number' ? answer.expires_in : 0,
  }
}

/**
 * Asks for the message that confirms an address for the sign-in that waits in this browser. It is a sign-in
 * by mail: the answer is the same whether the address has an account or not.
 */
export function startProviderConfirm(address: string): Promise<EmailStarted> {
  return request('POST', '/api/v1/auth/provider/confirm/start', { address })
}

/**
 * Ends the sign-in that waits in this browser with the code of that message: the provider account is linked
 * to the account of the address, and the server sets the session cookie.
 */
export function finishProviderConfirm(attempt: string, code: string): Promise<Me> {
  return request('POST', '/api/v1/auth/provider/confirm/finish', { attempt, code })
}

/** The provider accounts that sign in to the account, the oldest first. */
export async function linkedProviders(): Promise<LinkedProvider[]> {
  const answer = await request<{ providers?: unknown } | null>('GET', '/api/v1/providers')
  if (!Array.isArray(answer?.providers)) throw new ApiError('other')
  return (answer.providers as Array<Partial<LinkedProvider> | null>).filter(
    (linked): linked is LinkedProvider =>
      isProvider(linked?.provider) && typeof linked.linked_at === 'string',
  )
}

/** Starts linking a provider account to the account that is signed in. The browser comes back to `returnTo`. */
export async function linkProvider(
  provider: Provider,
  returnTo?: string,
): Promise<ProviderStarted> {
  return started(
    await request(
      'POST',
      '/api/v1/providers/start',
      returnTo ? { provider, return_to: returnTo } : { provider },
    ),
  )
}

/** Takes a provider account out of the account. The server refuses the only way left to sign in. */
export function unlinkProvider(provider: Provider): Promise<void> {
  return request('DELETE', `/api/v1/providers/${encodeURIComponent(provider)}`)
}

/** A way the account can show it is the person's again: one of its passkeys, or a code sent to its address. */
export type ConfirmWay = 'passkey' | 'email'

export type ConfirmWays = {
  /** The ways this account can confirm now, a passkey first. */
  ways: ConfirmWay[]
  /** Until when the proof is fresh, when it is. */
  confirmed_until?: string
}

/** How the account that is signed in can show again that it is the person's. */
export async function confirmWays(): Promise<ConfirmWays> {
  const answer = await request<{ ways?: unknown; confirmed_until?: unknown } | null>(
    'GET',
    '/api/v1/auth/confirm',
  )
  if (!Array.isArray(answer?.ways)) throw new ApiError('other')
  const ways: unknown[] = answer.ways
  return {
    ways: (['passkey', 'email'] as const).filter((way) => ways.includes(way)),
    ...(typeof answer.confirmed_until === 'string'
      ? { confirmed_until: answer.confirmed_until }
      : {}),
  }
}

/** Starts confirming with one of the account's own passkeys. `passkey_needed` when it has none. */
export function startConfirmPasskey(): Promise<
  PasskeyCeremony<PublicKeyCredentialRequestOptionsJSON>
> {
  return request('POST', '/api/v1/auth/confirm/passkey/start')
}

/** Ends confirming with the browser's answer. The session is confirmed for ten minutes. */
export function finishConfirmPasskey(ceremony: string, credential: unknown): Promise<Me> {
  return request('POST', '/api/v1/auth/confirm/passkey/finish', { ceremony, credential })
}

/** Sends a six-digit code to the account's address. `passkey_required` when the account confirms with a passkey. */
export function startConfirmEmail(): Promise<EmailStarted> {
  return request('POST', '/api/v1/auth/confirm/email/start')
}

/** Ends confirming with the code of that message. */
export function finishConfirmEmail(attempt: string, code: string): Promise<Me> {
  return request('POST', '/api/v1/auth/confirm/email/finish', { attempt, code })
}

// ---------------------------------------------------------------------------------------------------------------
// Mailboxes and the inbox: the person's own calls (ADR 0026, "The API"). The admin panel's are elsewhere.
// ---------------------------------------------------------------------------------------------------------------

const mailboxStates: ReadonlySet<string> = new Set(['waiting', 'receiving', 'paused', 'unreadable'])

/** A mailbox as the server answered it, or a failure. An `unreadable` one has only its id and `created_at`. */
function mailboxOf(answer: Partial<Mailbox> | null): Mailbox {
  if (typeof answer?.id !== 'string' || !mailboxStates.has(answer.state as string)) {
    throw new ApiError('other')
  }
  return {
    ...(answer as Mailbox),
    address: typeof answer.address === 'string' ? answer.address : '',
    label: typeof answer.label === 'string' ? answer.label : '',
    retention_options: Array.isArray(answer.retention_options) ? answer.retention_options : [],
    confirmations: Array.isArray(answer.confirmations) ? answer.confirmations : [],
  }
}

function mailboxPath(id: string): string {
  return `/api/v1/mailboxes/${encodeURIComponent(id)}`
}

function messagePath(mailbox: string, message: string): string {
  return `${mailboxPath(mailbox)}/messages/${encodeURIComponent(message)}`
}

/** The account's mailboxes, the oldest first, without the ones being disconnected, and how many it may have. */
export async function mailboxes(): Promise<MailboxList> {
  const answer = await request<Partial<MailboxList> | null>('GET', '/api/v1/mailboxes')
  if (!Array.isArray(answer?.mailboxes)) throw new ApiError('other')
  return {
    mailboxes: answer.mailboxes.map(mailboxOf),
    limit: typeof answer.limit === 'number' ? answer.limit : answer.mailboxes.length,
  }
}

/** Makes a mailbox with a new address. The label may be empty. */
export async function createMailbox(label: string): Promise<Mailbox> {
  return mailboxOf(await request('POST', '/api/v1/mailboxes', { label }))
}

export async function mailbox(id: string): Promise<Mailbox> {
  return mailboxOf(await request('GET', mailboxPath(id)))
}

/** Changes what the person may change of a mailbox. A new period applies to mail received from now on. */
export async function updateMailbox(
  id: string,
  change: { label?: string; retention_days?: number },
): Promise<Mailbox> {
  return mailboxOf(await request('PATCH', mailboxPath(id), change))
}

/**
 * Disconnects a mailbox: its address stops accepting mail and its mail is deleted. Needs a fresh proof of who
 * the person is. A forked one answers `mailbox_unreadable` and stays.
 */
export function disconnectMailbox(id: string): Promise<void> {
  return request('DELETE', mailboxPath(id))
}

/** How many messages a page asks for. */
export const messagesPerPage = 50

/**
 * A page of a mailbox's messages, the newest first, or with `q` a page of what a search found. Read the page's
 * `next` to go on, even when it holds no message.
 */
export async function messages(
  id: string,
  page: { after?: string; q?: string } = {},
): Promise<MessagePage> {
  const query = new URLSearchParams({ limit: String(messagesPerPage) })
  if (page.after) query.set('after', page.after)
  if (page.q) query.set('q', page.q)
  const answer = await request<Partial<MessagePage> | null>(
    'GET',
    `${mailboxPath(id)}/messages?${query}`,
  )
  if (!Array.isArray(answer?.messages)) throw new ApiError('other')
  return {
    messages: answer.messages,
    ...(typeof answer.next === 'string' && answer.next !== '' ? { next: answer.next } : {}),
    ...(typeof answer.searched_to === 'string' ? { searched_to: answer.searched_to } : {}),
  }
}

/** One message: its text always, and what it says of its HTML when it has some. */
export async function message(mailbox: string, id: string): Promise<Message> {
  const answer = await request<Partial<Message> | null>('GET', messagePath(mailbox, id))
  if (typeof answer?.id !== 'string') throw new ApiError('other')
  const list = <T>(value: T[] | undefined): T[] => (Array.isArray(value) ? value : [])
  return {
    ...(answer as Message),
    from: list(answer.from),
    to: list(answer.to),
    cc: list(answer.cc),
    reply_to: list(answer.reply_to),
    attachments: list(answer.attachments),
    auth: list(answer.auth),
    text: typeof answer.text === 'string' ? answer.text : '',
    subject: typeof answer.subject === 'string' ? answer.subject : '',
  }
}

/** Where the raw message is downloaded from, as a file: never opened in the page. */
export function rawMessageUrl(mailbox: string, id: string): string {
  return `${messagePath(mailbox, id)}/raw`
}

/** Where an attachment is downloaded from, as a file of any type: never opened in the page. */
export function attachmentUrl(mailbox: string, id: string, index: number): string {
  return `${messagePath(mailbox, id)}/attachments/${index}`
}

/**
 * Where the formatted view's frame loads a message's HTML from: a document the enclave writes from the message,
 * sandboxed by the frame's attribute and its own policy. Only an `<iframe src>` asks for it; the app never reads it.
 */
export function messageHtmlUrl(mailbox: string, id: string): string {
  return `${messagePath(mailbox, id)}/html`
}

/**
 * Where a link of a message's HTML leads, for the link page. `link` is the token of the link page's address, which
 * anyone can write: one without the shape of a token is answered `not_found` without asking.
 */
export async function messageLink(mailbox: string, id: string, link: string): Promise<MessageLink> {
  if (!isMessageLinkToken(link)) throw new ApiError('not_found')
  const answer = await request<Partial<MessageLink> | null>(
    'GET',
    `${messagePath(mailbox, id)}/links/${link}`,
  )
  if (typeof answer?.url !== 'string' || typeof answer.host !== 'string')
    throw new ApiError('other')
  return {
    link,
    url: answer.url,
    host: answer.host,
    ...(typeof answer.shown === 'string' && answer.shown !== '' ? { shown: answer.shown } : {}),
  }
}

export function deleteMessage(mailbox: string, id: string): Promise<void> {
  return request('DELETE', messagePath(mailbox, id))
}

/** The most messages one request deletes. */
export const maxDeleted = 100

/** Deletes up to a hundred messages, and answers how many of them were there. */
export async function deleteMessages(mailbox: string, ids: string[]): Promise<number> {
  const answer = await request<{ deleted?: unknown } | null>(
    'POST',
    `${mailboxPath(mailbox)}/messages/delete`,
    { ids },
  )
  return typeof answer?.deleted === 'number' ? answer.deleted : 0
}

/**
 * A formula's canonical form, its identifier and the sentence a person reads of it, naming the account's
 * mailboxes (ADR 0027). A refusal is `invalid_formula` with its `problem`: where and why, never a value of it.
 */
export async function checkQuery(formula: QueryFormula): Promise<FormulaCheck> {
  const answer = await request<Partial<FormulaCheck> | null>('POST', '/api/v1/queries/check', {
    formula,
  })
  if (typeof answer?.id !== 'string' || typeof answer.sentence?.text !== 'string')
    throw new ApiError('other')
  return answer as FormulaCheck
}

/**
 * A page of what a formula matches in one mailbox, newest first: the owner's own preview, never a vendor's result.
 * Read the page's `next` to go on, even when it holds no message. With `askModel` the server asks the model for the
 * messages only a model can decide (ADR 0028), which needs the person's agreement to the model's notice
 * (`model_notice_needed` without it), and the page says what became of it in `model`.
 */
export async function matches(
  mailbox: string,
  formula: QueryFormula,
  page: { after?: string; limit?: number; askModel?: boolean } = {},
): Promise<MatchPage> {
  const answer = await request<Partial<MatchPage> | null>(
    'POST',
    `${mailboxPath(mailbox)}/matches`,
    {
      formula,
      ...(page.after ? { after: page.after } : {}),
      limit: page.limit ?? messagesPerPage,
      ...(page.askModel ? { ask_model: true } : {}),
    },
  )
  return matchPageOf(answer)
}

/** Whether a page's `model` is one of the three the server says; anything else is dropped. */
function isMatchModel(value: unknown): value is MatchModel {
  return value === 'asked' || value === 'limit' || value === 'off'
}

/** A page of matches as the server answered it, with every list there. */
function matchPageOf(answer: Partial<MatchPage> | null | undefined): MatchPage {
  if (!Array.isArray(answer?.messages)) throw new ApiError('other')
  const list = (value: unknown): string[] => (Array.isArray(value) ? value : [])
  return {
    messages: answer.messages,
    needs_model: list(answer.needs_model),
    unknown: list(answer.unknown),
    ...(typeof answer.next === 'string' && answer.next !== '' ? { next: answer.next } : {}),
    ...(typeof answer.searched_to === 'string' ? { searched_to: answer.searched_to } : {}),
    ...(isMatchModel(answer.model) ? { model: answer.model } : {}),
    ...(Array.isArray(answer.held) ? { held: answer.held } : {}),
  }
}

// ---------------------------------------------------------------------------------------------------------------
// Connections (ADR 0030): the consent screen and the person's connections, in the person's session.
// ---------------------------------------------------------------------------------------------------------------

function requestPath(id: string): string {
  return `/api/v1/oauth/requests/${encodeURIComponent(id)}`
}

function connectionPath(id: string): string {
  return `/api/v1/connections/${encodeURIComponent(id)}`
}

/**
 * Hands the server the query string a vendor sent the browser to /oauth/authorize with, exactly as the address bar
 * had it after "?" (`src/lib/oauth.ts`). The answer is the request to show, or where to send the browser back to the
 * vendor with its error. A request refused before its redirect URI is proven is `invalid_client`,
 * `vendor_unreachable` or `request_too_large`: shown, never redirected.
 */
export async function startAuthorization(query: string): Promise<AuthorizationStarted> {
  const answer = await request<AuthorizationStarted | null>('POST', '/api/v1/oauth/requests', {
    query,
  })
  if (typeof answer?.redirect === 'string' && answer.redirect !== '') {
    return { redirect: answer.redirect }
  }
  const shown = answer?.request
  if (typeof shown?.id !== 'string' || typeof shown.vendor?.domain !== 'string') {
    throw new ApiError('other')
  }
  return { request: shown }
}

/**
 * A page of what a request would read under the whole draft, in one of the draft's mailboxes, newest first, and what
 * approving that draft does. A next page carries the `draft_id` the page before answered (`invalid_cursor` when the
 * draft changed since: start again from the newest).
 */
export async function previewConsent(
  id: string,
  preview: ConsentPreview,
): Promise<ConsentPreviewPage> {
  const answer = await request<Partial<ConsentPreviewPage> | null>(
    'POST',
    `${requestPath(id)}/matches`,
    preview,
  )
  if (typeof answer?.draft_id !== 'string' || typeof answer.snapshot?.change !== 'string') {
    throw new ApiError('other')
  }
  return {
    draft_id: answer.draft_id,
    change: answer.change ?? answer.snapshot.change,
    snapshot: answer.snapshot,
    page: matchPageOf(answer.page),
  }
}

/**
 * Allows or denies a request. Allowing sends the draft, the model's notice and the snapshot the screen showed, and
 * needs a fresh proof (`confirmation_needed`, then `useConfirmed()`); it is refused `model_notice_changed` or
 * `consent_changed` when what the screen showed is no longer so. The answer is where the browser goes back to the
 * vendor, and nowhere else.
 */
export async function decideAuthorization(
  id: string,
  decision: AuthorizationDecision,
): Promise<AuthorizationAnswer> {
  const answer = await request<Partial<AuthorizationAnswer> | null>(
    'POST',
    `${requestPath(id)}/decision`,
    decision,
  )
  if (typeof answer?.redirect !== 'string' || answer.redirect === '') throw new ApiError('other')
  return { redirect: answer.redirect }
}

/** A connection as the server answered it, with every list there. */
function connectionOf(answer: Partial<Connection> | null | undefined): Connection {
  if (typeof answer?.id !== 'string' || typeof answer.vendor?.domain !== 'string') {
    throw new ApiError('other')
  }
  const list = <T>(value: T[] | undefined): T[] => (Array.isArray(value) ? value : [])
  return {
    ...(answer as Connection),
    parts: list(answer.parts),
    mailboxes: list(answer.mailboxes),
    devices: list(answer.devices).map((device) => ({ ...device, parts: list(device.parts) })),
    delivery: answer.delivery ?? 'delivering',
  }
}

/** The account's connections: the active ones, and the ones that ended in the last 30 days. */
export async function connections(): Promise<ConnectionList> {
  const answer = await request<Partial<ConnectionList> | null>('GET', '/api/v1/connections')
  if (!Array.isArray(answer?.connections)) throw new ApiError('other')
  return { connections: answer.connections.map(connectionOf) }
}

export async function connection(id: string): Promise<Connection> {
  return connectionOf(await request('GET', connectionPath(id)))
}

/** Revokes a whole connection: every device of it stops at once. Needs no fresh proof: it only protects. */
export async function revokeConnection(id: string): Promise<Connection> {
  return connectionOf(await request('POST', `${connectionPath(id)}/revoke`))
}

/** Revokes one device of a connection; the others keep working. */
export async function revokeDevice(id: string, device: string): Promise<Connection> {
  return connectionOf(
    await request('POST', `${connectionPath(id)}/devices/${encodeURIComponent(device)}/revoke`),
  )
}

/**
 * Renews a connection for as many days again, from now, under the model's notice in force. Needs a fresh proof; is
 * refused `consent_changed` when the connection changed since the page showed it, and `connection_ended` past its end.
 */
export async function renewConnection(id: string, renewal: ConnectionRenewal): Promise<Connection> {
  return connectionOf(await request('POST', `${connectionPath(id)}/renew`, renewal))
}

// ---------------------------------------------------------------------------------------------------------------
// End of the person's mailbox calls.
// ---------------------------------------------------------------------------------------------------------------

/** An account as the admin panel sees it. Nothing of its mail, and nothing that signs in to it. */
export type AccountRecord = {
  id: string
  state: 'active' | 'closing'
  address: string
  name: string
  language: string
  created_at: string
  address_verified_at: string
  closes_at?: string
  closing_by?: 'person' | 'operator'
  /** Since when the operator blocks the account. There only while it is blocked. */
  blocked_at?: string
  passkeys: number
  passkey_only: boolean
  sessions: number
  providers: Provider[]
  admin?: 'super' | 'admin'
}

export type AccountPage = {
  accounts: AccountRecord[]
  /** Accounts the server has and could not read. */
  unreadable: number
  /** Where the next page starts. Absent on the last one. */
  next?: string
}

/** What an admin can do to an account, by the end of its path. */
export type AccountAction = 'block' | 'unblock' | 'delete' | 'delete/cancel' | 'sessions/close'

function accountPage(answer: Partial<AccountPage> | null): AccountPage {
  if (!Array.isArray(answer?.accounts)) throw new ApiError('other')
  return {
    accounts: answer.accounts,
    unreadable: typeof answer.unreadable === 'number' ? answer.unreadable : 0,
    ...(typeof answer.next === 'string' && answer.next !== '' ? { next: answer.next } : {}),
  }
}

/** The accounts, the oldest first, a page at a time. */
export async function adminAccounts(after?: string, limit = 50): Promise<AccountPage> {
  const query = new URLSearchParams({ limit: String(limit) })
  if (after) query.set('after', after)
  return accountPage(await request('GET', `/api/v1/admin/accounts?${query}`))
}

/** The account of exactly this address: none or one. */
export async function adminFindAccount(address: string): Promise<AccountPage> {
  const query = new URLSearchParams({ address })
  return accountPage(await request('GET', `/api/v1/admin/accounts?${query}`))
}

export function adminAccount(id: string): Promise<AccountRecord> {
  return request('GET', `/api/v1/admin/accounts/${encodeURIComponent(id)}`)
}

/** Does something to an account. The answer is the account after it. */
export function adminAccountAction(id: string, action: AccountAction): Promise<AccountRecord> {
  return request('POST', `/api/v1/admin/accounts/${encodeURIComponent(id)}/${action}`)
}

/** An invitation not used yet. Its code is never answered again. */
export type InvitationRecord = { id: string; created_at: string; expires_at: string }

export async function adminInvitations(): Promise<{
  invitations: InvitationRecord[]
  truncated: boolean
}> {
  const answer = await request<{ invitations?: unknown; truncated?: unknown } | null>(
    'GET',
    '/api/v1/admin/invitations',
  )
  if (!Array.isArray(answer?.invitations)) throw new ApiError('other')
  return { invitations: answer.invitations, truncated: answer.truncated === true }
}

export type InvitationsMade = { invitations: string[]; ids: string[]; expires_at: string }

/** Makes invitations. Their codes are in this answer and nowhere else, ever. */
export async function makeInvitations(count: number): Promise<InvitationsMade> {
  const answer = await request<Partial<InvitationsMade> | null>(
    'POST',
    '/api/v1/admin/invitations',
    { count },
  )
  if (!Array.isArray(answer?.invitations) || answer.invitations.length === 0) {
    throw new ApiError('other')
  }
  return {
    invitations: answer.invitations,
    ids: Array.isArray(answer.ids) ? answer.ids : [],
    expires_at: typeof answer.expires_at === 'string' ? answer.expires_at : '',
  }
}

export function revokeInvitation(id: string): Promise<void> {
  return request('DELETE', `/api/v1/admin/invitations/${encodeURIComponent(id)}`)
}

/** A provider's application that is set. Its secret is never answered. */
export type ProviderApp = { provider: Provider; client_id: string }

export async function providerApps(): Promise<ProviderApp[]> {
  const answer = await request<{ providers?: unknown } | null>('GET', '/api/v1/admin/providers')
  if (!Array.isArray(answer?.providers)) throw new ApiError('other')
  return (answer.providers as Array<Partial<ProviderApp> | null>).filter(
    (app): app is ProviderApp => isProvider(app?.provider) && typeof app.client_id === 'string',
  )
}

export function setProviderApp(
  provider: Provider,
  clientId: string,
  clientSecret: string,
): Promise<void> {
  return request('PUT', `/api/v1/admin/providers/${provider}`, {
    client_id: clientId,
    client_secret: clientSecret,
  })
}

export function removeProviderApp(provider: Provider): Promise<void> {
  return request('DELETE', `/api/v1/admin/providers/${provider}`)
}

export type AdminRecord = {
  id: string
  address: string
  name: string
  super: boolean
  since?: string
}

/** The admins, as the server answered them: the list, or after a change the list as it is now. */
async function adminList(answer: Promise<{ admins?: unknown } | null>): Promise<AdminRecord[]> {
  const got = await answer
  if (!Array.isArray(got?.admins)) throw new ApiError('other')
  return got.admins
}

export function admins(): Promise<AdminRecord[]> {
  return adminList(request('GET', '/api/v1/admin/admins'))
}

/** Makes an account an admin, and answers the admins after the change. */
export function makeAdmin(id: string): Promise<AdminRecord[]> {
  return adminList(request('PUT', `/api/v1/admin/admins/${encodeURIComponent(id)}`))
}

/** Makes an admin an account like any other, and answers the admins after the change. */
export function removeAdmin(id: string): Promise<AdminRecord[]> {
  return adminList(request('DELETE', `/api/v1/admin/admins/${encodeURIComponent(id)}`))
}

/** One thing an admin did. `kind` may be one this app does not know yet. */
export type LogEntry = {
  at: string
  kind: string
  admin: { id: string; address: string }
  /** The account it was done to, when it was done to one. Its address is gone with the account. */
  account?: { id: string; address?: string }
  /** The invitation it was about, made or revoked: its identifier, which says nothing of its code. */
  invitation?: string
  /** The provider whose application was set or removed. A server from before says nothing. */
  provider?: string
}

export async function adminLog(
  after?: string,
  limit = 50,
): Promise<{
  entries: LogEntry[]
  next?: string
}> {
  const query = new URLSearchParams({ limit: String(limit) })
  if (after) query.set('after', after)
  const answer = await request<{ entries?: unknown; next?: unknown } | null>(
    'GET',
    `/api/v1/admin/log?${query}`,
  )
  if (!Array.isArray(answer?.entries)) throw new ApiError('other')
  return {
    entries: answer.entries,
    ...(typeof answer.next === 'string' && answer.next !== '' ? { next: answer.next } : {}),
  }
}

export type AdminStats = {
  accounts: { active: number; closing: number; blocked: number; total: number }
  invitations: { unused: number }
  /** The mailboxes (ADR 0026), absent from a server that does not count them. */
  mailboxes?: MailboxCounts
  /** False when some accounts or mailboxes could not be read: the numbers are at least these. */
  complete: boolean
}

export async function adminStats(): Promise<AdminStats> {
  const answer = await request<
    (Omit<Partial<AdminStats>, 'mailboxes'> & { mailboxes?: Partial<MailboxCounts> }) | null
  >('GET', '/api/v1/admin/stats')
  if (typeof answer?.accounts !== 'object' || answer.accounts === null) throw new ApiError('other')
  return {
    accounts: answer.accounts,
    invitations: { unused: answer.invitations?.unused ?? 0 },
    complete: answer.complete !== false,
    ...(typeof answer.mailboxes?.total === 'number'
      ? { mailboxes: { total: answer.mailboxes.total } }
      : {}),
  }
}

// ---------------------------------------------------------------------------------------------------------------
// Admin: mailboxes (ADR 0026, "What the operator and admins see and do"). What the admin panel sees of a mailbox
// is its signed record: never its address, its label, the Gmail account behind it or its mail. It can pause and
// resume one, and nothing else.
// ---------------------------------------------------------------------------------------------------------------

/** A mailbox as the admin panel sees it. An `unreadable` one has only its id, its account and `created_at`. */
export type MailboxRecord = {
  id: string
  account: string
  state: MailboxState
  created_at: string
  retention_days: number
  receiving_since?: string
  /** Since when it refuses new mail. There only while it is paused. */
  paused_at?: string
  /** Google confirmed which Gmail account asked to forward to it. */
  verified: boolean
}

/** How many mailboxes the deployment has, from an index that proves nothing: it may be off by one or two. */
export type MailboxCounts = { total: number }

function mailboxRecords(answer: { mailboxes?: unknown } | null): MailboxRecord[] {
  if (!Array.isArray(answer?.mailboxes)) throw new ApiError('other')
  return (answer.mailboxes as Array<Partial<MailboxRecord> | null>).filter(
    (record): record is MailboxRecord =>
      typeof record?.id === 'string' && mailboxStates.has(record.state as string),
  )
}

function mailboxRecordOf(answer: Partial<MailboxRecord> | null): MailboxRecord {
  const [record] = mailboxRecords({ mailboxes: [answer] })
  if (!record) throw new ApiError('other')
  return record
}

/** How many mailboxes an account has, or undefined from a server that does not say. */
export function accountMailboxes(record: AccountRecord): number | undefined {
  const count = (record as { mailboxes?: unknown }).mailboxes
  return typeof count === 'number' ? count : undefined
}

/** The mailbox an entry of the admin log was about, when it was about one: its id, and nothing else. */
export function logEntryMailbox(entry: LogEntry): string | undefined {
  const mailbox = (entry as { mailbox?: unknown }).mailbox
  return typeof mailbox === 'string' && mailbox !== '' ? mailbox : undefined
}

/** An account's mailboxes, the oldest first. */
export async function adminAccountMailboxes(account: string): Promise<MailboxRecord[]> {
  return mailboxRecords(
    await request('GET', `/api/v1/admin/accounts/${encodeURIComponent(account)}/mailboxes`),
  )
}

/** The mailbox of exactly this address, for a report of abuse that names it: none or one. */
export async function adminFindMailbox(address: string): Promise<MailboxRecord | null> {
  const query = new URLSearchParams({ address })
  return mailboxRecords(await request('GET', `/api/v1/admin/mailboxes?${query}`))[0] ?? null
}

/**
 * A pause or a resume: the mailbox after it, and whether this call changed it. One that found the mailbox as asked
 * (`changed: false`) told nobody and was counted nowhere.
 */
export type MailboxChange = MailboxRecord & { changed?: boolean }

/** Pauses or resumes a mailbox. The answer is the mailbox after it, and whether the call changed it. */
export async function adminMailboxAction(
  id: string,
  action: 'pause' | 'resume',
): Promise<MailboxChange> {
  const { changed, ...record } = mailboxRecordOf(
    await request('POST', `/api/v1/admin/mailboxes/${encodeURIComponent(id)}/${action}`),
  ) as MailboxRecord & { changed?: unknown }
  return typeof changed === 'boolean' ? { ...record, changed } : record
}

/*
  Receipts (ADR 0037, pull request 3). The shapes of internal/contracts/receipts.go: the head of the account's chain and
  the walk's pages, with what was found of each link and receipt, and the export that permaily-verify -receipts checks
  offline. The Activity screen that shows them is step 8's.
*/

/** Where a receipt is on its account's chain: its link's number and its index in that link. */
export type ReceiptsPosition = { n: number; index: number }

/** What was found of a link: `ok`, `prev`, `expired` (read, and checked), or `lost`, `forked`, `unreadable`. */
export type LinkVerdict = 'ok' | 'prev' | 'expired' | 'lost' | 'forked' | 'unreadable'

/** What was found of a receipt a link names. */
export type ReceiptVerdict = 'ok' | 'missing' | 'unreadable' | 'changed'

/**
 * The newest link of the account's chain. When it is `ok`, its number and `sha256` are what the person notes, to check
 * an export later with `permaily-verify -receipts-head <n>:<sha256>`.
 */
export type ReceiptsHead = { n: number; verdict?: LinkVerdict; sha256?: string; at?: string }

/** One receipt a link names: the receipt itself only when it is `ok`. */
export type ReceiptsEntry = {
  index: number
  id: string
  sha256: string
  verdict: ReceiptVerdict
  receipt?: unknown
}

/** One link of a page: what it says only when it could be read; `recovered` receipts were linked late. */
export type ReceiptsLink = {
  n: number
  verdict: LinkVerdict
  kind?: 'receipts' | 'adopted' | 'recovered'
  at?: string
  prev?: string
  break?: string
  before?: string
  more?: boolean
  receipts: ReceiptsEntry[]
}

/** A page of the account's receipts, the newest first. Ask the next from `next` until `done`. */
export type ReceiptsPage = {
  head: ReceiptsHead
  links: ReceiptsLink[]
  next: ReceiptsPosition
  done: boolean
}

function isPosition(p: unknown): p is ReceiptsPosition {
  return (
    typeof (p as ReceiptsPosition)?.n === 'number' &&
    typeof (p as ReceiptsPosition)?.index === 'number'
  )
}

function isHead(h: unknown): h is ReceiptsHead {
  return typeof (h as ReceiptsHead)?.n === 'number'
}

function receiptsQuery(before?: ReceiptsPosition, limit?: number): string {
  const query = new URLSearchParams()
  if (before) query.set('before', `${before.n}.${before.index}`)
  if (limit !== undefined) query.set('limit', String(limit))
  const s = query.toString()
  return s === '' ? '' : `?${s}`
}

/** A page of the account's receipts: from the head, or `before` where the page before ended; at most `limit`. */
export async function receipts(
  page: { before?: ReceiptsPosition; limit?: number } = {},
): Promise<ReceiptsPage> {
  const answer = await request<Partial<ReceiptsPage> | null>(
    'GET',
    `/api/v1/receipts${receiptsQuery(page.before, page.limit)}`,
  )
  if (!Array.isArray(answer?.links) || !isHead(answer.head) || !isPosition(answer.next))
    throw new ApiError('other')
  return {
    head: answer.head,
    links: answer.links.map((l) => ({ ...l, receipts: l.receipts ?? [] })),
    next: answer.next,
    done: answer.done === true,
  }
}

/** A page of an export as the server answers it: what permaily-verify -receipts reads, one a line. */
type ExportPage = { head: ReceiptsHead; links: unknown[]; next: ReceiptsPosition; done?: boolean }

/** Says whether a is strictly before b on the chain, as the next page's start always is. */
function below(a: ReceiptsPosition, b: ReceiptsPosition): boolean {
  return a.n < b.n || (a.n === b.n && a.index < b.index)
}

/**
 * Every page of the account's export, in order, one JSON object a line: the file `permaily-verify -receipts` checks.
 * `head` is the chain's head when it began, for the person to note beside the file, never in it.
 */
export async function exportReceipts(): Promise<{ file: string; head: ReceiptsHead }> {
  const lines: string[] = []
  let head: ReceiptsHead | undefined
  let before: ReceiptsPosition | undefined
  for (;;) {
    const page = await request<Partial<ExportPage> | null>(
      'GET',
      `/api/v1/receipts/export${receiptsQuery(before)}`,
    )
    if (!Array.isArray(page?.links) || !isHead(page.head) || !isPosition(page.next))
      throw new ApiError('other')
    head ??= page.head
    lines.push(`${JSON.stringify(page)}\n`)
    if (page.done === true) return { file: lines.join(''), head }
    // A page that does not move on would be asked for ever.
    if (before && !below(page.next, before)) throw new ApiError('other')
    before = page.next
  }
}
