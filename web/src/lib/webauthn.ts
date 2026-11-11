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
    window.isSecureContext &&
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
