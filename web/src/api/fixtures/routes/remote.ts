// The remote repository a space follows, and the separate repository for its attachments. Connecting stores what was
// sent; nothing here reaches a remote, and the answer is always "in sync". A test that needs a failure or a move in
// progress says so with `fx.fail`, `fx.reply` or by changing the record in `data.area.remotes`.

import type { AssetsRemote, Remote, RemoteCredential } from '../../types.ts'
import { Refusal, rfc3339, type Ctx, type Route } from '../kit.ts'
import { spaceOf } from './files.ts'

const KEY = {
  publicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPetalBookFixtureKey petalbook',
  fingerprint: 'SHA256:fixturefixturefixturefixturefixturefixture00',
}

function credentialOf(input: Record<string, unknown> | undefined): RemoteCredential {
  const type = input?.type
  if (type === 'ssh') return { type: 'ssh', ...KEY }
  if (type === 'connection')
    return {
      type: 'connection',
      provider: String(input?.provider ?? ''),
      via: 'user',
      user: { id: '', username: '' },
      login: '',
    } as RemoteCredential
  return { type: 'token', username: typeof input?.username === 'string' ? input.username : '' }
}

function remoteFrom(c: Ctx): Remote {
  const body = c.body as { url: string; branch?: string; credential?: Record<string, unknown> }
  return {
    url: body.url,
    branch: body.branch || 'main',
    provider: 'git',
    credential: credentialOf(body.credential),
    state: 'ok',
    lastSync: rfc3339(c.now()),
    error: '',
    errorCode: '',
    rewritten: null,
  }
}

const recordOf = (c: Ctx) => (c.data.area.remotes[spaceOf(c).info.id] ??= { remote: null, assets: null })

export const remoteRoutes: Route[] = [
  [
    'GET',
    '/api/spaces/:space/remote',
    (c): Remote => {
      const r = recordOf(c).remote
      if (!r) throw new Refusal(404, 'not_found', 'the space follows no remote repository')
      return r
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/remote',
    (c): Remote => {
      const rec = recordOf(c)
      rec.remote = remoteFrom(c)
      spaceOf(c).info.sync = 'ok'
      return rec.remote
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/remote',
    (c) => {
      const rec = recordOf(c)
      rec.remote = null
      rec.assets = null
      spaceOf(c).info.sync = 'local'
    },
  ],
  ['POST', '/api/spaces/:space/remote/key', () => KEY],
  [
    'POST',
    '/api/spaces/:space/remote/sync',
    (c): Remote => {
      const r = recordOf(c).remote
      if (!r) throw new Refusal(404, 'not_found', 'the space follows no remote repository')
      r.lastSync = rfc3339(c.now())
      return r
    },
  ],
  [
    'POST',
    '/api/spaces/:space/remote/accept',
    (c): Remote => {
      const r = recordOf(c).remote
      if (!r) throw new Refusal(404, 'not_found', 'the space follows no remote repository')
      r.state = 'ok'
      r.rewritten = null
      return r
    },
  ],

  // attachments
  [
    'GET',
    '/api/spaces/:space/remote/assets',
    (c): AssetsRemote => {
      const a = recordOf(c).assets
      if (!a) throw new Refusal(404, 'not_found', 'the space has no attachment repository')
      return a
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/remote/assets',
    (c): AssetsRemote => {
      const rec = recordOf(c)
      rec.assets = { ...remoteFrom(c), move: null }
      return rec.assets
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/remote/assets',
    (c) => {
      recordOf(c).assets = null
    },
  ],
  [
    'POST',
    '/api/spaces/:space/remote/assets/move',
    (c): AssetsRemote => {
      const a = recordOf(c).assets
      if (!a) throw new Refusal(404, 'not_found', 'the space has no attachment repository')
      a.move = { state: 'copying', files: 1, filesDone: 0, bytes: 3, bytesDone: 0, error: '' }
      return a
    },
  ],
]
