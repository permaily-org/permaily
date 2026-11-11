/**
 * The web app's own API, under /api/v1/ on the app's host. The session is a cookie only the server can read,
 * so nothing here holds one. Requests are same origin and JSON, which is what the server lets through.
 */

export type Account = {
  id: string
  state: string
  address: string
  name: string
  language: string
  created_at: string
}

export type Session = {
  id: string
  created_at: string
  last_used_at: string
  expires_at: string
  device: string
  current: boolean
}

export type Me = { account: Account; session: Session }

export type EmailStarted = {
  /** Names the sign-in that was started. The code is entered against it. */
  attempt: string
  /** Seconds the code and the link work for. */
  expires_in: number
}

/** The codes the server answers with, plus `network` for a request that got no answer. */
export type ApiErrorCode =
  | 'invalid_address'
  | 'invalid_invitation'
  | 'invalid_code'
  | 'too_many_requests'
  | 'unauthorized'
  | 'network'
  | 'other'

const known: ReadonlySet<string> = new Set([
  'invalid_address',
  'invalid_invitation',
  'invalid_code',
  'too_many_requests',
  'unauthorized',
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

/** How long a request may take before it counts as no answer. */
const timeout = 20_000

async function request<T>(method: 'GET' | 'POST', path: string, body?: unknown): Promise<T> {
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
  if (typeof code === 'string' && known.has(code)) throw new ApiError(code as ApiErrorCode)
  if (response.status === 401) throw new ApiError('unauthorized')
  if (response.status === 429) throw new ApiError('too_many_requests')
  throw new ApiError('other')
}

/** Asks for the sign-in message. The answer is the same whether the address has an account or not. */
export function startEmail(address: string, invitation?: string): Promise<EmailStarted> {
  return request(
    'POST',
    '/api/v1/auth/email/start',
    invitation ? { address, invitation } : { address },
  )
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
