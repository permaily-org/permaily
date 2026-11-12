/**
 * Passkeys, as the browser offers them (WebAuthn). The server sends the options and reads the answer in the JSON
 * shapes of the standard, with binary values as base64url without padding. A browser that has
 * `parseCreationOptionsFromJSON`, `parseRequestOptionsFromJSON` and `toJSON()` does the conversion itself;
 * for one that has not, it is done here by hand.
 *
 * Nothing of a ceremony is kept: the options, the answer and the server's name for the ceremony live in the
 * caller's memory until the answer is sent, and never go to storage, the address or a log.
 */

/**
 * Why the browser gave no passkey.
 * - `cancelled`: the person closed the prompt, or it ended by itself. Browsers do not say which.
 * - `exists`: the authenticator already holds a passkey for this account, so it made no other.
 * - `unavailable`: the browser cannot do this on this page at all.
 * - `failed`: anything else.
 */
export type PasskeyProblem = 'cancelled' | 'exists' | 'unavailable' | 'failed'

export class PasskeyError extends Error {
  readonly kind: PasskeyProblem
  constructor(kind: PasskeyProblem) {
    super(kind)
    this.name = 'PasskeyError'
    this.kind = kind
  }
}

/** Whether this browser can make and use passkeys on this page. Without it, no screen offers them. */
export function passkeysSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    window.isSecureContext === true &&
    typeof window.PublicKeyCredential === 'function' &&
    typeof navigator.credentials?.create === 'function' &&
    typeof navigator.credentials?.get === 'function'
  )
}

function fromBase64url(text: string): Uint8Array<ArrayBuffer> {
  const base64 = text.replace(/-/g, '+').replace(/_/g, '/')
  const binary = atob(base64.padEnd(Math.ceil(base64.length / 4) * 4, '='))
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes
}

function toBase64url(buffer: ArrayBuffer): string {
  let binary = ''
  for (const byte of new Uint8Array(buffer)) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function descriptors(
  list: PublicKeyCredentialDescriptorJSON[] | undefined,
): PublicKeyCredentialDescriptor[] | undefined {
  return list?.map((credential) => ({
    type: 'public-key',
    id: fromBase64url(credential.id),
    transports: credential.transports as AuthenticatorTransport[] | undefined,
  }))
}

function creationOptions(
  json: PublicKeyCredentialCreationOptionsJSON,
): PublicKeyCredentialCreationOptions {
  if (typeof PublicKeyCredential.parseCreationOptionsFromJSON === 'function') {
    return PublicKeyCredential.parseCreationOptionsFromJSON(json)
  }
  return {
    rp: json.rp,
    user: { ...json.user, id: fromBase64url(json.user.id) },
    challenge: fromBase64url(json.challenge),
    pubKeyCredParams: json.pubKeyCredParams,
    timeout: json.timeout,
    excludeCredentials: descriptors(json.excludeCredentials),
    authenticatorSelection: json.authenticatorSelection,
    attestation: json.attestation as AttestationConveyancePreference | undefined,
  }
}

function requestOptions(
  json: PublicKeyCredentialRequestOptionsJSON,
): PublicKeyCredentialRequestOptions {
  if (typeof PublicKeyCredential.parseRequestOptionsFromJSON === 'function') {
    return PublicKeyCredential.parseRequestOptionsFromJSON(json)
  }
  return {
    challenge: fromBase64url(json.challenge),
    timeout: json.timeout,
    rpId: json.rpId,
    allowCredentials: descriptors(json.allowCredentials),
    userVerification: json.userVerification as UserVerificationRequirement | undefined,
  }
}

/** The browser's answer as JSON: its own `toJSON()` when it has one, the same shape by hand when not. */
function answer(credential: PublicKeyCredential): unknown {
  if (typeof credential.toJSON === 'function') return credential.toJSON()
  const common = {
    id: credential.id,
    rawId: toBase64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: {},
  }
  const response = credential.response
  if ('attestationObject' in response) {
    const made = response as AuthenticatorAttestationResponse
    return {
      ...common,
      response: {
        clientDataJSON: toBase64url(made.clientDataJSON),
        attestationObject: toBase64url(made.attestationObject),
        transports: typeof made.getTransports === 'function' ? made.getTransports() : [],
      },
    }
  }
  const used = response as AuthenticatorAssertionResponse
  return {
    ...common,
    response: {
      clientDataJSON: toBase64url(used.clientDataJSON),
      authenticatorData: toBase64url(used.authenticatorData),
      signature: toBase64url(used.signature),
      userHandle: used.userHandle ? toBase64url(used.userHandle) : undefined,
    },
  }
}

/** A text as base64url without padding, the way the server writes the bytes of an account's id. */
function textToBase64url(text: string): string {
  return toBase64url(new TextEncoder().encode(text).buffer as ArrayBuffer)
}

/** The relying party of this page's passkeys: the server makes them for the app's own host. */
function relyingParty(): string {
  return window.location.hostname
}

/**
 * Runs one of the browser's signals to authenticators, where it has it. They are a courtesy to the person's
 * password manager: a browser without them, or one that refuses, changes nothing and says nothing.
 */
function signal(name: 'signalUnknownCredential' | 'signalAllAcceptedCredentials', options: object) {
  try {
    if (typeof window === 'undefined' || typeof window.PublicKeyCredential !== 'function') return
    const tell = (
      window.PublicKeyCredential as unknown as Record<
        string,
        ((options: object) => Promise<void>) | undefined
      >
    )[name]
    if (typeof tell !== 'function') return
    void Promise.resolve(tell.call(window.PublicKeyCredential, options)).catch(() => {})
  } catch {
    // Not for the person to hear about.
  }
}

/** The identifier of a passkey in what the browser answered when it was made, or null when it has none. */
export function credentialId(credential: unknown): string | null {
  const id = (credential as { id?: unknown } | null)?.id
  return typeof id === 'string' && id !== '' ? id : null
}

/**
 * Tells the authenticator that the server does not have a passkey, so it stops offering it: one the server
 * refused when it was made, or one the person removed in Settings. Only for a passkey the server is known
 * not to have: a sign-in that failed says no such thing, since the wait may just have been too long.
 */
export function forgetPasskey(id: string | null, rpId: string = relyingParty()): void {
  if (!id) return
  signal('signalUnknownCredential', { rpId, credentialId: id })
}

/**
 * Tells the authenticator which passkeys the account has, as the server just listed them, so it stops
 * offering the ones removed from another device. `accountId` is the account's id, which is what the server
 * gives the browser as the user of a passkey.
 */
export function acceptedPasskeys(accountId: string, ids: string[]): void {
  signal('signalAllAcceptedCredentials', {
    rpId: relyingParty(),
    userId: textToBase64url(accountId),
    allAcceptedCredentialIds: ids,
  })
}

/** What went wrong, from what the browser threw. Its text is not kept: it is for developers, not for people. */
function problem(error: unknown, making: boolean): PasskeyError {
  const name = error instanceof Error ? error.name : ''
  if (name === 'NotAllowedError' || name === 'AbortError') return new PasskeyError('cancelled')
  if (name === 'InvalidStateError' && making) return new PasskeyError('exists')
  if (name === 'SecurityError' || name === 'NotSupportedError') {
    return new PasskeyError('unavailable')
  }
  return new PasskeyError('failed')
}

function taken(credential: Credential | null): unknown {
  if (credential?.type !== 'public-key') throw new PasskeyError('failed')
  return answer(credential as PublicKeyCredential)
}

/**
 * Asks the browser to make a passkey with the server's options, and returns its answer for the server.
 * Throws a PasskeyError when no passkey was made.
 */
export async function makePasskey(
  options: PublicKeyCredentialCreationOptionsJSON,
  signal?: AbortSignal,
): Promise<unknown> {
  if (!passkeysSupported()) throw new PasskeyError('unavailable')
  try {
    return taken(
      await navigator.credentials.create({ publicKey: creationOptions(options), signal }),
    )
  } catch (error) {
    throw error instanceof PasskeyError ? error : problem(error, true)
  }
}

/**
 * Asks the browser for one of the person's passkeys with the server's options, and returns its answer for the
 * server. The browser shows which passkeys it has: no address is asked. Throws a PasskeyError when none was used.
 */
export async function getPasskey(
  options: PublicKeyCredentialRequestOptionsJSON,
  signal?: AbortSignal,
): Promise<unknown> {
  if (!passkeysSupported()) throw new PasskeyError('unavailable')
  try {
    return taken(await navigator.credentials.get({ publicKey: requestOptions(options), signal }))
  } catch (error) {
    throw error instanceof PasskeyError ? error : problem(error, false)
  }
}
