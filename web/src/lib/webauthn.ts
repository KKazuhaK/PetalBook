// WebAuthn in the browser: the server speaks the JSON form of the options and
// of the credentials (base64url strings); `navigator.credentials` wants
// ArrayBuffers. The browser's own converters are used when it has them, and a
// manual one stands in when it does not.

type Json = Record<string, unknown>

export function b64urlToBytes(s: string): Uint8Array<ArrayBuffer> {
  const pad = '='.repeat((4 - (s.length % 4)) % 4)
  const bin = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'))
  const out = new Uint8Array(new ArrayBuffer(bin.length))
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

export function bytesToB64url(buf: ArrayBuffer | null | undefined): string {
  if (!buf) return ''
  let bin = ''
  for (const b of new Uint8Array(buf)) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

const str = (v: unknown) => (typeof v === 'string' ? v : '')
const decodeDescriptors = (list: unknown) => (Array.isArray(list) ? list.map((d) => ({ ...(d as Json), id: b64urlToBytes(str((d as Json).id)) })) : undefined)

// `PublicKeyCredentialRequestOptionsJSON` to what `credentials.get` takes.
export function requestOptionsFromJSON(json: Json): PublicKeyCredentialRequestOptions {
  const native = (globalThis as { PublicKeyCredential?: { parseRequestOptionsFromJSON?: (j: unknown) => PublicKeyCredentialRequestOptions } }).PublicKeyCredential
  if (typeof native?.parseRequestOptionsFromJSON === 'function') return native.parseRequestOptionsFromJSON(json)
  const out = { ...json, challenge: b64urlToBytes(str(json.challenge)) } as unknown as PublicKeyCredentialRequestOptions
  const allow = decodeDescriptors(json.allowCredentials)
  if (allow) (out as { allowCredentials?: unknown }).allowCredentials = allow
  return out
}

// `PublicKeyCredentialCreationOptionsJSON` to what `credentials.create` takes.
export function creationOptionsFromJSON(json: Json): PublicKeyCredentialCreationOptions {
  const native = (globalThis as { PublicKeyCredential?: { parseCreationOptionsFromJSON?: (j: unknown) => PublicKeyCredentialCreationOptions } }).PublicKeyCredential
  if (typeof native?.parseCreationOptionsFromJSON === 'function') return native.parseCreationOptionsFromJSON(json)
  const user = (json.user ?? {}) as Json
  const out = { ...json, challenge: b64urlToBytes(str(json.challenge)), user: { ...user, id: b64urlToBytes(str(user.id)) } } as unknown as PublicKeyCredentialCreationOptions
  const exclude = decodeDescriptors(json.excludeCredentials)
  if (exclude) (out as { excludeCredentials?: unknown }).excludeCredentials = exclude
  return out
}

// A credential as the JSON the server reads (`toJSON()`, or the same shape by hand).
export function credentialToJSON(cred: PublicKeyCredential): Json {
  const withJson = cred as unknown as { toJSON?: () => Json }
  if (typeof withJson.toJSON === 'function') return withJson.toJSON()
  const r = cred.response as AuthenticatorResponse & Partial<AuthenticatorAssertionResponse & AuthenticatorAttestationResponse>
  const response: Json = { clientDataJSON: bytesToB64url(r.clientDataJSON) }
  if (r.authenticatorData) response.authenticatorData = bytesToB64url(r.authenticatorData)
  if (r.signature) response.signature = bytesToB64url(r.signature)
  if (r.userHandle) response.userHandle = bytesToB64url(r.userHandle)
  if (r.attestationObject) response.attestationObject = bytesToB64url(r.attestationObject)
  if (typeof r.getTransports === 'function') response.transports = r.getTransports()
  const ext = typeof cred.getClientExtensionResults === 'function' ? cred.getClientExtensionResults() : {}
  const out: Json = { id: cred.id, rawId: bytesToB64url(cred.rawId), type: cred.type, response, clientExtensionResults: ext }
  if (cred.authenticatorAttachment) out.authenticatorAttachment = cred.authenticatorAttachment
  return out
}

export const passkeysSupported = () => typeof navigator !== 'undefined' && !!navigator.credentials && typeof (globalThis as { PublicKeyCredential?: unknown }).PublicKeyCredential !== 'undefined'

export type PasskeyFailure = 'cancelled' | 'unsupported' | 'failed'
// What went wrong in the browser's part of a ceremony: the person closing the prompt is not an error.
export function passkeyFailure(e: unknown): PasskeyFailure {
  if (!passkeysSupported()) return 'unsupported'
  const name = (e as { name?: string } | null)?.name
  return name === 'NotAllowedError' || name === 'AbortError' ? 'cancelled' : 'failed'
}

export async function getCredential(options: Json): Promise<Json> {
  const cred = await navigator.credentials.get({ publicKey: requestOptionsFromJSON((options.publicKey as Json | undefined) ?? options) })
  if (!cred) throw new DOMException('no credential', 'NotAllowedError')
  return credentialToJSON(cred as PublicKeyCredential)
}

export async function createCredential(options: Json): Promise<Json> {
  const cred = await navigator.credentials.create({ publicKey: creationOptionsFromJSON((options.publicKey as Json | undefined) ?? options) })
  if (!cred) throw new DOMException('no credential', 'NotAllowedError')
  return credentialToJSON(cred as PublicKeyCredential)
}
