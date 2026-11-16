import { generateKeyPairSync, randomBytes } from 'node:crypto'
import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import type { BrowserContext, Page, Route } from '@playwright/test'

/*
  What the real-browser tests play: the vendor, on another site, and the app's API, answered in the browser's context.
  Values are made up; the shapes are the contract's (internal/contracts/connections.go).
*/

export const account = {
  id: 'acc_ana',
  state: 'active',
  address: 'ana@example.org',
  name: 'Ana',
  language: 'en',
  created_at: '2026-03-06T10:00:00Z',
}

function me(confirmed = false) {
  return {
    account,
    session: {
      id: 'ses_here',
      created_at: '2026-11-01T10:00:00Z',
      last_used_at: '2026-11-10T10:00:00Z',
      expires_at: '2026-12-01T10:00:00Z',
      device: 'Chromium',
      current: true,
      ...(confirmed ? { confirmed_until: new Date(Date.now() + 600_000).toISOString() } : {}),
    },
  }
}

const mailbox = {
  id: 'mbx_personal',
  address: 'q7wz3kfn9tr2hmx4vp8c@inbox.permaily.com',
  label: 'Personal',
  state: 'receiving',
  created_at: '2026-11-02T09:00:00Z',
  retention_days: 90,
  retention_options: [7, 30, 90, 180, 365],
  confirmations: [],
}

/** The played vendor: a page with a link to the consent screen, and its callback, which records what it got. */
export class Vendor {
  private server: Server | null = null
  /** What the callback got, each a query string. */
  readonly callbacks: string[] = []
  origin = ''

  async start(): Promise<void> {
    this.server = createServer((req, res) => {
      const url = new URL(req.url ?? '/', 'http://127.0.0.1')
      if (url.pathname === '/callback') {
        this.callbacks.push(url.search.slice(1))
        res.writeHead(200, { 'Content-Type': 'text/html' })
        res.end('<!doctype html><title>Vendor</title><h1>The vendor got an answer</h1>')
        return
      }
      const to = (url.searchParams.get('to') ?? '')
        .replaceAll('&', '&amp;')
        .replaceAll('"', '&quot;')
      res.writeHead(200, { 'Content-Type': 'text/html' })
      res.end(
        `<!doctype html><title>Vendor</title><a id="connect" href="${to}">Connect Permaily</a>`,
      )
    })
    await new Promise<void>((resolve) => this.server?.listen(0, '127.0.0.1', resolve))
    this.origin = `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`
  }

  async stop(): Promise<void> {
    await new Promise<void>((resolve) => this.server?.close(() => resolve()))
  }

  /** The query string a vendor sends the browser to /oauth/authorize with. */
  query(): string {
    return new URLSearchParams({
      response_type: 'code',
      client_id: 'https://claude.ai/oauth/claude-code-client-metadata',
      redirect_uri: `${this.origin}/callback`,
      state: 'st8te-secret',
      code_challenge: 'Q0hBTExFTkdFLWNoYWxsZW5nZS1jaGFsbGVuZ2UtY2hhbGxlbmdl',
      code_challenge_method: 'S256',
      resource: 'https://mcp.permaily.com/mcp',
    }).toString()
  }

  /**
   * Opens the vendor's page and follows its link to the app, as a person does: a cross-site navigation (the vendor is
   * on 127.0.0.1, the app on localhost: two sites). Answers where the browser said the navigation came from.
   */
  async connect(page: Page, app: string, path = '/oauth/authorize'): Promise<string | undefined> {
    const to = `${app}${path}?${this.query()}`
    await page.goto(`${this.origin}/?to=${encodeURIComponent(to)}`)
    const [navigation] = await Promise.all([
      page.waitForRequest(
        (request) => request.isNavigationRequest() && new URL(request.url()).pathname === path,
      ),
      page.click('#connect'),
    ])
    return (await navigation.allHeaders()).referer
  }
}

type Reply = { status: number; body?: unknown }

/**
 * The app's API, played: a session, sign-in by code and by passkey, a fresh proof, the consent routes and the list of
 * connections.
 */
export class PlayedApi {
  signedIn = false
  confirmed = false
  /** The requests the app made, by method and path. */
  readonly asked: Array<{ route: string; body: unknown }> = []
  /** Requests nothing here answers: a test fails on any. */
  readonly unexpected: string[] = []
  /** What the decision route answers instead of deciding, for a stale request. */
  decisionRefusal: string | null = null
  /** The account's connections, as GET /api/v1/connections answers them. */
  connections: unknown[] = []
  private decided = false

  constructor(private readonly vendor: Vendor) {}

  async install(context: BrowserContext, app: string): Promise<void> {
    await context.route(`${app}/api/**`, (route) => this.answer(route))
  }

  private request(): Record<string, unknown> {
    return {
      id: 'pmq_played',
      vendor: { domain: 'claude.ai', verified: 'Anthropic' },
      client_name: 'Claude Code',
      redirect_host: '127.0.0.1',
      loopback: true,
      resource: 'mcp',
      parts: ['sender', 'subject', 'body'],
      since: { new: true },
      ends_in_days: 90,
      mailboxes: [mailbox.id],
      draft: { mailboxes: [mailbox.id], since: { new: true } },
      rules: [
        {
          id: 'rul_default',
          version: 1,
          sentence: [
            {
              lang: 'en',
              text: 'Sign-in codes and password resets',
              lead: [],
              lines: [{ parts: [{ text: 'Sign-in codes and password resets' }] }],
            },
          ],
        },
      ],
      change: 'new',
      snapshot: { change: 'new', ends_at: '2027-02-14T10:00:00Z', draft_id: 'drf_start' },
      model_notice: '2026-11-15',
      expires_at: new Date(Date.now() + 900_000).toISOString(),
    }
  }

  /** Claude Code's connection, as the list of connections answers it. */
  static connection(): Record<string, unknown> {
    return {
      id: 'con_played',
      vendor: { domain: 'claude.ai', verified: 'Anthropic' },
      client_id: 'https://claude.ai/oauth/claude-code-client-metadata',
      client_name: 'Claude Code',
      resource: 'mcp',
      state: 'active',
      parts: ['sender', 'subject', 'body'],
      mailboxes: [mailbox.id],
      since: { new: true, at: '2026-11-01T10:00:00Z' },
      made_at: '2026-11-01T10:00:00Z',
      ends_at: '2027-01-30T10:00:00Z',
      devices: [
        { id: 'dev_one', made_at: '2026-11-01T10:00:00Z', state: 'active', parts: ['sender'] },
      ],
      model_notice: '2026-11-15',
      delivery: 'delivering',
      version: 3,
    }
  }

  private reply(method: string, path: string, body: unknown): Reply {
    const ok = (b?: unknown): Reply => ({ status: b === undefined ? 204 : 200, body: b })
    const no = (status: number, code: string): Reply => ({
      status,
      body: { error: { code, message: 'played' } },
    })
    switch (`${method} ${path}`) {
      case 'GET /api/v1/me':
        return this.signedIn ? ok(me(this.confirmed)) : no(401, 'unauthorized')
      case 'GET /api/v1/auth/providers':
        return ok({ providers: [] })
      case 'POST /api/v1/auth/email/start':
        return ok({ attempt: 'att_played', expires_in: 600 })
      case 'POST /api/v1/auth/email/finish':
        this.signedIn = true
        return ok(me())
      case 'POST /api/v1/auth/passkey/start':
        return ok({
          ceremony: 'cer_played',
          options: {
            challenge: randomBytes(32).toString('base64url'),
            rpId: 'localhost',
            allowCredentials: [],
            userVerification: 'preferred',
            timeout: 60_000,
          },
        })
      case 'POST /api/v1/auth/passkey/finish':
        this.signedIn = true
        return ok(me())
      case 'GET /api/v1/auth/confirm':
        return ok({ ways: ['email'] })
      case 'POST /api/v1/auth/confirm/email/start':
        return ok({ attempt: 'att_confirm', expires_in: 600 })
      case 'POST /api/v1/auth/confirm/email/finish':
        this.confirmed = true
        return ok(me(true))
      case 'GET /api/v1/connections':
        return ok({ connections: this.connections })
      case 'GET /api/v1/mailboxes':
        return ok({ mailboxes: [mailbox], limit: 5 })
      case 'POST /api/v1/oauth/requests':
        if (!this.signedIn) return no(401, 'unauthorized')
        return ok({ request: this.request() })
      case 'POST /api/v1/oauth/requests/pmq_played/matches':
        return ok({
          draft_id: 'drf_start',
          change: 'new',
          snapshot: { change: 'new', ends_at: '2027-02-14T10:00:00Z', draft_id: 'drf_start' },
          page: { messages: [], needs_model: [], unknown: [] },
        })
      case 'POST /api/v1/oauth/requests/pmq_played/decision': {
        // Every refusal played here is a conflict, 409, as the server answers it (internal/httpapi).
        if (this.decisionRefusal) return no(409, this.decisionRefusal)
        if (this.decided) return no(409, 'request_ended')
        const { allow } = body as { allow: boolean }
        const back = `${this.vendor.origin}/callback`
        const iss = 'iss=https%3A%2F%2Fapi.permaily.com'
        if (!allow) {
          this.decided = true
          return ok({ redirect: `${back}?error=access_denied&state=st8te-secret&${iss}` })
        }
        if (!this.confirmed) return no(403, 'confirmation_needed')
        this.decided = true
        return ok({ redirect: `${back}?code=pmc_played&state=st8te-secret&${iss}` })
      }
    }
    this.unexpected.push(`${method} ${path}`)
    return no(404, 'not_found')
  }

  private async answer(route: Route): Promise<void> {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const raw = request.postData()
    const body = raw ? JSON.parse(raw) : undefined
    this.asked.push({ route: `${request.method()} ${path}`, body })
    const reply = this.reply(request.method(), path, body)
    await route.fulfill({
      status: reply.status,
      contentType: 'application/json',
      body: reply.body === undefined ? '' : JSON.stringify(reply.body),
    })
  }
}

/**
 * Gives the browser an authenticator that holds a passkey for this site, which signs in when asked: Chromium's
 * virtual authenticator, through the DevTools protocol.
 */
export async function withPasskey(page: Page): Promise<void> {
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('WebAuthn.enable', { enableUI: false })
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  })
  const { privateKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' })
  await cdp.send('WebAuthn.addCredential', {
    authenticatorId,
    credential: {
      credentialId: randomBytes(16).toString('base64'),
      isResidentCredential: true,
      rpId: 'localhost',
      privateKey: privateKey.export({ format: 'der', type: 'pkcs8' }).toString('base64'),
      userHandle: Buffer.from('acc_ana').toString('base64'),
      signCount: 0,
    },
  })
}
