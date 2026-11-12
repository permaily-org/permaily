import { describe, expect, it, vi } from 'vitest'
import {
  acceptedPasskeys,
  credentialId,
  forgetPasskey,
  getPasskey,
  makePasskey,
  PasskeyError,
  passkeysSupported,
} from '../../src/lib/webauthn.ts'
import {
  answered,
  b64url,
  browserError,
  browserWithPasskeys,
  bytes,
  creationOptions,
  requestOptions,
} from '../helpers/passkeys.ts'

const creation = creationOptions as unknown as PublicKeyCredentialCreationOptionsJSON
const request = requestOptions as unknown as PublicKeyCredentialRequestOptionsJSON

/** The kind of a PasskeyError a call fails with. */
async function problem(call: Promise<unknown>): Promise<string> {
  try {
    await call
  } catch (error) {
    expect(error).toBeInstanceOf(PasskeyError)
    return (error as PasskeyError).kind
  }
  throw new Error('a passkey was given')
}

describe('whether a browser has passkeys', () => {
  it('needs a secure page, PublicKeyCredential and both ceremonies', () => {
    expect(passkeysSupported()).toBe(false)
    browserWithPasskeys()
    expect(passkeysSupported()).toBe(true)
    vi.stubGlobal('isSecureContext', false)
    expect(passkeysSupported()).toBe(false)
    vi.stubGlobal('isSecureContext', true)
    vi.stubGlobal('PublicKeyCredential', undefined)
    expect(passkeysSupported()).toBe(false)
  })

  it('gives no passkey where it has none', async () => {
    expect(await problem(makePasskey(creation))).toBe('unavailable')
    expect(await problem(getPasskey(request))).toBe('unavailable')
  })
})

describe('with a browser that converts for itself', () => {
  it('hands the options to the browser and its JSON to the server', async () => {
    const parsed = { parsed: true }
    const { create, get } = browserWithPasskeys({
      create: async () => answered({ id: 'made' }),
      get: async () => answered({ id: 'used' }),
      statics: {
        parseCreationOptionsFromJSON: vi.fn(() => parsed),
        parseRequestOptionsFromJSON: vi.fn(() => parsed),
      },
    })
    const leaving = new AbortController()
    expect(await makePasskey(creation, leaving.signal)).toEqual({ id: 'made' })
    expect(create).toHaveBeenCalledWith({ publicKey: parsed, signal: leaving.signal })
    expect(await getPasskey(request)).toEqual({ id: 'used' })
    expect(get).toHaveBeenCalledWith({ publicKey: parsed, signal: undefined })
  })
})

describe('with a browser that does not', () => {
  it('turns the options of making a passkey into bytes', async () => {
    const { create } = browserWithPasskeys({ create: async () => answered({ id: 'made' }) })
    await makePasskey(creation)
    const options = create.mock.calls[0][0].publicKey as PublicKeyCredentialCreationOptions
    expect(new Uint8Array(options.challenge as ArrayBuffer)).toEqual(
      new Uint8Array([251, 255, 254, 62, 63]),
    )
    expect(new TextDecoder().decode(options.user.id as ArrayBuffer)).toBe('acc_ana')
    expect(options.user.name).toBe('ana@example.org')
    expect(options.excludeCredentials).toEqual([
      { type: 'public-key', id: new Uint8Array([1, 2, 3]), transports: ['usb'] },
    ])
    expect(options.rp).toEqual({ id: 'app.permaily.test', name: 'Permaily' })
    expect(options.authenticatorSelection).toEqual({
      residentKey: 'required',
      userVerification: 'required',
    })
  })

  it('writes what was made as the server reads it: base64url without padding', async () => {
    browserWithPasskeys({
      create: async () => ({
        type: 'public-key',
        id: 'cred-1',
        rawId: bytes(251, 255),
        authenticatorAttachment: 'platform',
        response: {
          clientDataJSON: bytes(1),
          attestationObject: bytes(62, 63, 64),
          getTransports: () => ['internal', 'hybrid'],
        },
      }),
    })
    expect(await makePasskey({ ...creation, excludeCredentials: undefined })).toEqual({
      id: 'cred-1',
      rawId: '-_8',
      type: 'public-key',
      authenticatorAttachment: 'platform',
      clientExtensionResults: {},
      response: {
        clientDataJSON: 'AQ',
        attestationObject: 'Pj9A',
        transports: ['internal', 'hybrid'],
      },
    })
  })

  it('writes no transports for an authenticator that names none', async () => {
    browserWithPasskeys({
      create: async () => ({
        type: 'public-key',
        id: 'cred-1',
        rawId: bytes(1),
        authenticatorAttachment: null,
        response: { clientDataJSON: bytes(1), attestationObject: bytes(2) },
      }),
    })
    expect(await makePasskey(creation)).toMatchObject({
      authenticatorAttachment: undefined,
      response: { transports: [] },
    })
  })

  it('turns the options of using a passkey into bytes, and writes what was used', async () => {
    const { get } = browserWithPasskeys({
      get: async () => ({
        type: 'public-key',
        id: 'cred-1',
        rawId: bytes(4, 5),
        authenticatorAttachment: 'cross-platform',
        response: {
          clientDataJSON: bytes(1),
          authenticatorData: bytes(2),
          signature: bytes(3),
          userHandle: new TextEncoder().encode('acc_ana').buffer,
        },
      }),
    })
    expect(await getPasskey(request)).toEqual({
      id: 'cred-1',
      rawId: 'BAU',
      type: 'public-key',
      authenticatorAttachment: 'cross-platform',
      clientExtensionResults: {},
      response: {
        clientDataJSON: 'AQ',
        authenticatorData: 'Ag',
        signature: 'Aw',
        userHandle: b64url('acc_ana'),
      },
    })
    const options = get.mock.calls[0][0].publicKey as PublicKeyCredentialRequestOptions
    expect(new Uint8Array(options.challenge as ArrayBuffer)).toEqual(new Uint8Array([9, 8, 7]))
    expect(options.rpId).toBe('app.permaily.test')
    expect(options.allowCredentials).toEqual([
      { type: 'public-key', id: new Uint8Array([4, 5]), transports: undefined },
    ])
  })

  it('writes no account for a passkey that names none', async () => {
    browserWithPasskeys({
      get: async () => ({
        type: 'public-key',
        id: 'cred-1',
        rawId: bytes(1),
        response: {
          clientDataJSON: bytes(1),
          authenticatorData: bytes(2),
          signature: bytes(3),
          userHandle: null,
        },
      }),
    })
    expect(await getPasskey({ ...request, allowCredentials: undefined })).toMatchObject({
      response: { userHandle: undefined },
    })
  })
})

describe('why the browser gave no passkey', () => {
  it.each([
    ['NotAllowedError', 'cancelled', 'cancelled'],
    ['AbortError', 'cancelled', 'cancelled'],
    ['InvalidStateError', 'exists', 'failed'],
    ['SecurityError', 'unavailable', 'unavailable'],
    ['NotSupportedError', 'unavailable', 'unavailable'],
    ['UnknownError', 'failed', 'failed'],
  ])('reads %s as %s when making and %s when using', async (name, making, using) => {
    const refuse = async () => {
      throw browserError(name)
    }
    browserWithPasskeys({ create: refuse, get: refuse })
    expect(await problem(makePasskey(creation))).toBe(making)
    expect(await problem(getPasskey(request))).toBe(using)
  })

  it('reads anything that is not a passkey as a failure', async () => {
    browserWithPasskeys({
      create: async () => null,
      get: async () => ({ type: 'password', id: 'ana', password: 'secret' }),
    })
    expect(await problem(makePasskey(creation))).toBe('failed')
    expect(await problem(getPasskey(request))).toBe('failed')
    browserWithPasskeys({
      create: async () => {
        throw 'not an error'
      },
    })
    expect(await problem(makePasskey(creation))).toBe('failed')
    expect(new PasskeyError('failed').name).toBe('PasskeyError')
  })
})

describe('telling the authenticator what the server has', () => {
  it('says which passkey the server does not have, where the browser can', () => {
    const signalUnknownCredential = vi.fn(async () => {})
    browserWithPasskeys({ statics: { signalUnknownCredential } })
    forgetPasskey('cred-1', 'app.permaily.test')
    expect(signalUnknownCredential).toHaveBeenCalledWith({
      rpId: 'app.permaily.test',
      credentialId: 'cred-1',
    })
    // Without a relying party named, it is the page's own host.
    forgetPasskey('cred-2')
    expect(signalUnknownCredential).toHaveBeenLastCalledWith({
      rpId: window.location.hostname,
      credentialId: 'cred-2',
    })
    forgetPasskey(null)
    expect(signalUnknownCredential).toHaveBeenCalledTimes(2)
  })

  it('says which passkeys the account has, with the account as the server names it', () => {
    const signalAllAcceptedCredentials = vi.fn(async () => {})
    browserWithPasskeys({ statics: { signalAllAcceptedCredentials } })
    acceptedPasskeys('acc_ana', ['cred-1', 'cred-2'])
    expect(signalAllAcceptedCredentials).toHaveBeenCalledWith({
      rpId: window.location.hostname,
      userId: b64url('acc_ana'),
      allAcceptedCredentialIds: ['cred-1', 'cred-2'],
    })
  })

  it('changes nothing and says nothing where the browser cannot, or will not', async () => {
    // No passkeys at all.
    expect(() => forgetPasskey('cred-1')).not.toThrow()
    expect(() => acceptedPasskeys('acc_ana', [])).not.toThrow()
    // Passkeys, but no signals.
    browserWithPasskeys()
    expect(() => forgetPasskey('cred-1')).not.toThrow()
    expect(() => acceptedPasskeys('acc_ana', [])).not.toThrow()
    // Signals that refuse, at once or later.
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    browserWithPasskeys({
      statics: {
        signalUnknownCredential: () => {
          throw browserError('SecurityError')
        },
        signalAllAcceptedCredentials: async () => {
          throw browserError('SecurityError')
        },
      },
    })
    expect(() => forgetPasskey('cred-1')).not.toThrow()
    expect(() => acceptedPasskeys('acc_ana', [])).not.toThrow()
    await new Promise((resolve) => setTimeout(resolve, 0))
    process.off('unhandledRejection', unhandled)
    expect(unhandled).not.toHaveBeenCalled()
    // A signal that answers nothing at all.
    browserWithPasskeys({ statics: { signalUnknownCredential: () => undefined } })
    expect(() => forgetPasskey('cred-1')).not.toThrow()
  })

  it('reads the identifier of a passkey from what the browser answered', () => {
    expect(credentialId({ id: 'cred-1' })).toBe('cred-1')
    for (const answer of [null, undefined, {}, { id: '' }, { id: 7 }, 'cred-1']) {
      expect(credentialId(answer)).toBeNull()
    }
  })
})
