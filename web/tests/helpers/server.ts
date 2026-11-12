/**
 * The server, played for the tests: `fetch` is the one boundary that is mocked. A test says what each route
 * answers, and reads back what the app asked. A request no test expected fails the test (tests/setup.ts).
 */

export type Asked = { method: string; path: string; body: unknown }

/** An answer of the server, or `network` for a request that got none. */
export type Reply = { status: number; body?: unknown } | 'network'

type Answer = Reply | ((asked: Asked) => Reply | Promise<Reply>)

/** A 200 with a JSON body, or a 204 without one. */
export function ok(body?: unknown): Reply {
  return body === undefined ? { status: 204 } : { status: 200, body }
}

/** A refusal in the API's own shape. */
export function refused(status: number, code: string): Reply {
  return { status, body: { error: { code, message: 'not for people' } } }
}

export const network: Reply = 'network'

/** An answer a test gives when it chooses: the request waits until then. */
export function later(): { answer: Answer; give: (reply: Reply) => void } {
  let give: (reply: Reply) => void = () => {}
  const waiting = new Promise<Reply>((resolve) => {
    give = resolve
  })
  return { answer: () => waiting, give }
}

export type Account = {
  id: string
  state: 'active' | 'closing'
  address: string
  name: string
  language: string
  created_at: string
  closes_at?: string
  closing_by?: 'person' | 'operator'
}

export const ana: Account = {
  id: 'acc_ana',
  state: 'active',
  address: 'ana@example.org',
  name: 'Ana',
  language: 'en',
  created_at: '2026-03-06T10:00:00Z',
}

export function meOf(account: Account = ana) {
  return {
    account,
    session: {
      id: 'ses_here',
      created_at: '2026-11-01T10:00:00Z',
      last_used_at: '2026-11-10T10:00:00Z',
      expires_at: '2026-12-01T10:00:00Z',
      device: 'Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0',
      current: true,
    },
  }
}

class Server {
  private routes = new Map<string, Answer>()
  /** Every request the app made, in order. */
  calls: Asked[] = []
  /** Requests no test expected. */
  unexpected: string[] = []

  reset() {
    this.routes.clear()
    this.calls = []
    this.unexpected = []
  }

  /** Says what a route answers from now on: `on('GET /api/v1/me', ok(me))`. */
  on(route: string, answer: Answer): this {
    this.routes.set(route, answer)
    return this
  }

  /** Nobody is signed in, and no provider is on. */
  signedOut(): this {
    return this.on('GET /api/v1/me', refused(401, 'unauthorized')).on(
      'GET /api/v1/auth/providers',
      ok({ providers: [] }),
    )
  }

  /** This account is signed in. */
  signedIn(account: Account = ana): this {
    return this.on('GET /api/v1/me', ok(meOf(account)))
  }

  /** What Settings reads of how the account is signed in to. */
  settings(
    has: {
      passkeys?: Array<{ id: string; name: string; created_at?: string; synced?: boolean }>
      passkeyOnly?: boolean
      codesLeft?: number
      sessions?: unknown[]
      on?: string[]
      linked?: Array<{ provider: string; linked_at: string }>
    } = {},
  ): this {
    return this.on(
      'GET /api/v1/passkeys',
      ok({
        passkeys: (has.passkeys ?? []).map((passkey) => ({
          created_at: '2026-03-06T10:00:00Z',
          synced: false,
          ...passkey,
        })),
        passkey_only: has.passkeyOnly ?? false,
        recovery_codes_left: has.codesLeft ?? 0,
      }),
    )
      .on('GET /api/v1/sessions', ok({ sessions: has.sessions ?? [meOf().session] }))
      .on('GET /api/v1/auth/providers', ok({ providers: has.on ?? [] }))
      .on('GET /api/v1/providers', ok({ providers: has.linked ?? [] }))
  }

  /** The requests made to a route. */
  asked(route: string): Asked[] {
    return this.calls.filter((call) => `${call.method} ${call.path}` === route)
  }

  fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const method = init?.method ?? 'GET'
    const path = String(input)
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
    const asked = { method, path, body }
    this.calls.push(asked)
    const answer = this.routes.get(`${method} ${path}`)
    if (answer === undefined) {
      this.unexpected.push(`${method} ${path}`)
      throw new TypeError('no such route in this test')
    }
    const reply = typeof answer === 'function' ? await answer(asked) : answer
    if (reply === 'network') throw new TypeError('Failed to fetch')
    return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), {
      status: reply.status,
      headers: { 'Content-Type': 'application/json' },
    })
  }
}

export const server = new Server()
