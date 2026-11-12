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

/** `waiting` until mail other than Gmail's confirmations arrives; `paused` while Permaily's operator stops it. */
export type MailboxState = 'waiting' | 'receiving' | 'paused'

/** A forwarding confirmation Gmail sent to the mailbox. `verified` only when Google signed it. */
export type ForwardingConfirmation = {
  code: string
  /** The Gmail account that asked, as the message says. */
  requester?: string
  received_at: string
  verified: boolean
  /** The message that carried it, in the mailbox's inbox. */
  message?: string
}

export type Mailbox = {
  id: string
  /** Where the person forwards their mail, at inbox.permaily.com. */
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
  /** The Gmail account Google said asked to forward here. Absent: the label is shown as not verified. */
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

export type MessagePage = {
  messages: MessageSummary[]
  /** What to ask the next page with (`after`). Absent when nothing older is left to list or search. */
  next?: string
  /** For a search: when the oldest message this page looked at arrived. */
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
  /** `too_large` and `unreadable`: show the text, say why, offer the original. */
  status: 'shown' | 'too_large' | 'unreadable'
  /** Images the message would have fetched from elsewhere, never loaded: "3 remote images were not loaded". */
  remote_images: number
  /** Other remote references taken out: stylesheets, fonts, frames, media. */
  remote_other: number
  /** Images that travel in the message and are shown; and the ones not shown (SVG, wrong type, too large). */
  inline_images: number
  inline_images_omitted: number
  /** Links that open the link page. */
  links: number
}

/** Where a link of a message leads, for the link page (`GET .../messages/{message}/links/{n}`). */
export type MessageLink = {
  /** http or https, as the server wrote it: the only address the link page leaves for. */
  url: string
  /** The host in ASCII (`xn--` for other characters), so a look-alike shows. */
  host: string
  /** The host the link's text named, when it named another one. */
  shown?: string
}

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
  | 'too_many_mailboxes'
  | 'invalid_label'
  | 'invalid_retention'
  | 'invalid_query'
  | 'too_many_messages'
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
  'too_many_mailboxes',
  'invalid_label',
  'invalid_retention',
  'invalid_query',
  'too_many_messages',
])

export class ApiError extends Error {
  readonly code: ApiErrorCode
  constructor(code: ApiErrorCode) {
    super(code)
    this.name = 'ApiError'
    this.code = code
  }
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
  try {
    code = (await response.json())?.error?.code
  } catch {
    // An answer that is not the API's own, such as a proxy's error page.
  }
  if (code === 'account_closing') closingHeard?.()
  if (code === 'terms_needed' && response.status === 400) termsHeard?.()
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
  /** False when some accounts could not be read: the numbers are at least these. */
  complete: boolean
}

export async function adminStats(): Promise<AdminStats> {
  const answer = await request<Partial<AdminStats> | null>('GET', '/api/v1/admin/stats')
  if (typeof answer?.accounts !== 'object' || answer.accounts === null) throw new ApiError('other')
  return {
    accounts: answer.accounts,
    invitations: { unused: answer.invitations?.unused ?? 0 },
    complete: answer.complete !== false,
  }
}
