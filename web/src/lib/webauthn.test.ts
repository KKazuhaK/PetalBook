import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  b64urlToBytes,
  bytesToB64url,
  createCredential,
  credentialToJSON,
  creationOptionsFromJSON,
  getCredential,
  passkeyFailure,
  requestOptionsFromJSON,
} from './webauthn'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('base64url', () => {
  it('round-trips bytes, with the url alphabet and no padding', () => {
    const bytes = new Uint8Array([251, 255, 254, 0, 1])
    const s = bytesToB64url(bytes.buffer)
    expect(s).toBe('-__-AAE')
    expect([...b64urlToBytes(s)]).toEqual([...bytes])
  })
})

describe('options from JSON, by hand when the browser has no converter', () => {
  it('decodes the challenge and the allowed credentials of a sign-in', () => {
    const o = requestOptionsFromJSON({
      challenge: 'AQID',
      rpId: 'x.test',
      allowCredentials: [{ type: 'public-key', id: 'BAUG' }],
    })
    expect([...new Uint8Array(o.challenge as ArrayBuffer)]).toEqual([1, 2, 3])
    expect([...new Uint8Array(o.allowCredentials![0].id as ArrayBuffer)]).toEqual([4, 5, 6])
    expect(o.rpId).toBe('x.test')
  })
  it('decodes the user handle and the excluded credentials of a registration', () => {
    const o = creationOptionsFromJSON({
      challenge: 'AQID',
      user: { id: 'BAUG', name: 'a', displayName: 'A' },
      excludeCredentials: [{ type: 'public-key', id: 'BwgJ' }],
    })
    expect([...new Uint8Array(o.user.id as ArrayBuffer)]).toEqual([4, 5, 6])
    expect([...new Uint8Array(o.excludeCredentials![0].id as ArrayBuffer)]).toEqual([7, 8, 9])
    expect(o.user.name).toBe('a')
  })
  it('uses the browser converter when there is one', () => {
    const parse = vi.fn(() => ({ challenge: new ArrayBuffer(0) }))
    vi.stubGlobal('PublicKeyCredential', { parseRequestOptionsFromJSON: parse })
    requestOptionsFromJSON({ challenge: 'AQID' })
    expect(parse).toHaveBeenCalledWith({ challenge: 'AQID' })
  })
})

describe('credentials to JSON', () => {
  it('uses toJSON when the credential has it', () => {
    expect(credentialToJSON({ toJSON: () => ({ id: 'abc' }) } as unknown as PublicKeyCredential)).toEqual({ id: 'abc' })
  })
  it('builds the same shape by hand otherwise', () => {
    const buf = (...b: number[]) => new Uint8Array(b).buffer
    const cred = {
      id: 'abc',
      rawId: buf(1, 2, 3),
      type: 'public-key',
      response: { clientDataJSON: buf(1), authenticatorData: buf(2), signature: buf(3), userHandle: buf(4) },
      getClientExtensionResults: () => ({}),
    }
    const j = credentialToJSON(cred as unknown as PublicKeyCredential) as {
      id: string
      rawId: string
      response: Record<string, string>
    }
    expect(j.rawId).toBe('AQID')
    expect(j.response).toMatchObject({
      clientDataJSON: 'AQ',
      authenticatorData: 'Ag',
      signature: 'Aw',
      userHandle: 'BA',
    })
  })
})

describe('the ceremonies', () => {
  it('get passes the decoded options to the browser and returns the JSON of what it gave', async () => {
    const get = vi.fn(async () => ({ toJSON: () => ({ id: 'k' }) }))
    vi.stubGlobal('navigator', { credentials: { get } })
    expect(await getCredential({ challenge: 'AQID' })).toEqual({ id: 'k' })
    const arg = (get.mock.calls[0] as unknown as [{ publicKey: PublicKeyCredentialRequestOptions }])[0]
    expect([...new Uint8Array(arg.publicKey.challenge as ArrayBuffer)]).toEqual([1, 2, 3])
  })
  it('create does the same with the creation options', async () => {
    const create = vi.fn(async () => ({ toJSON: () => ({ id: 'n' }) }))
    vi.stubGlobal('navigator', { credentials: { create } })
    expect(await createCredential({ challenge: 'AQID', user: { id: 'BAUG' } })).toEqual({ id: 'n' })
  })
  it('a prompt closed by the person is "cancelled", and a browser without passkeys "unsupported"', () => {
    vi.stubGlobal('navigator', { credentials: {} })
    vi.stubGlobal('PublicKeyCredential', {})
    expect(passkeyFailure({ name: 'NotAllowedError' })).toBe('cancelled')
    expect(passkeyFailure(new Error('x'))).toBe('failed')
    vi.stubGlobal('PublicKeyCredential', undefined)
    expect(passkeyFailure(new Error('x'))).toBe('unsupported')
  })
})
