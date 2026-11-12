import { vi } from 'vitest'

/** Bytes as base64url without padding, as the server and the browser write them. */
export function b64url(bytes: ArrayBuffer | Uint8Array | string): string {
  const view =
    typeof bytes === 'string'
      ? new TextEncoder().encode(bytes)
      : bytes instanceof Uint8Array
        ? bytes
        : new Uint8Array(bytes)
  return btoa(String.fromCharCode(...view))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')
}

export function bytes(...values: number[]): ArrayBuffer {
  return new Uint8Array(values).buffer
}

/** An error as a browser throws it from `navigator.credentials`. */
export function browserError(name: string): Error {
  const error = new Error('for developers, never for people')
  error.name = name
  return error
}

type Asked = (options: CredentialCreationOptions & CredentialRequestOptions) => Promise<unknown>

/**
 * A browser that has passkeys, until the test ends: a secure page, `PublicKeyCredential` with the methods a
 * test gives it, and `navigator.credentials` that answers what the test says.
 */
export function browserWithPasskeys(
  has: {
    create?: Asked
    get?: Asked
    /** Static methods of `PublicKeyCredential`: the JSON helpers and the signals, where a browser has them. */
    statics?: Record<string, unknown>
  } = {},
) {
  const create = vi.fn<Asked>(has.create ?? (async () => null))
  const get = vi.fn<Asked>(has.get ?? (async () => null))
  // A function, as the browser's own is, with the static methods this browser has.
  const credentialClass = Object.assign(() => {}, has.statics)
  vi.stubGlobal('isSecureContext', true)
  vi.stubGlobal('PublicKeyCredential', credentialClass)
  // `navigator` is jsdom's own object: what is put on it is taken off when the test ends (tests/setup.ts).
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: { create, get },
  })
  return { create, get }
}

/** What a browser with `toJSON()` answers: the JSON goes to the server as it is. */
export function answered(json: Record<string, unknown>) {
  return { type: 'public-key', toJSON: () => json }
}

/** The options the server gives for making a passkey, as JSON. */
export const creationOptions = {
  rp: { id: 'app.permaily.test', name: 'Permaily' },
  user: { id: b64url('acc_ana'), name: 'ana@example.org', displayName: 'Ana' },
  challenge: b64url(bytes(251, 255, 254, 62, 63)),
  pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
  timeout: 300000,
  excludeCredentials: [{ type: 'public-key', id: b64url(bytes(1, 2, 3)), transports: ['usb'] }],
  authenticatorSelection: { residentKey: 'required', userVerification: 'required' },
  attestation: 'none',
}

/** The options the server gives for using one. */
export const requestOptions = {
  challenge: b64url(bytes(9, 8, 7)),
  timeout: 300000,
  rpId: 'app.permaily.test',
  allowCredentials: [{ type: 'public-key', id: b64url(bytes(4, 5)) }],
  userVerification: 'required',
}
