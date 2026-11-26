// What the tests of the offline copy share: a fixture server as the network, a fresh IndexedDB, and ways to make
// the network go away.

import { IDBFactory } from 'fake-indexeddb'
import { rememberUser, setOfflineHooks, spaceApi, markSignedIn } from '@/api/client'
import type { FixtureOptions } from '@/api/fixtures/data/model'
import { fixtureFetch } from '@/api/fixtures/fetch'
import { createFixtureServer, type FixtureServer } from '@/api/fixtures/server'
import { installOffline } from './index'
import { forgetOpenStores } from './manager'
import { resetBackoff } from './poller'
import { resetStatus } from './status'

export interface Net {
  fx: FixtureServer
  requests: string[]
  // Calls to the API of this kind (method and path without the query).
  count: (pattern: RegExp) => number
  down: () => void
  up: () => void
  restore: () => void
}

export function freshDevice(): void {
  globalThis.indexedDB = new IDBFactory()
  localStorage.clear()
  forgetOpenStores()
  resetBackoff()
  resetStatus(false)
}

// A signed-in single-user server (the one user is "local"), empty, as the network; `{ mode: 'accounts' }` for accounts.
export function network(opts: FixtureOptions = {}): Net {
  freshDevice()
  const fx = createFixtureServer({ empty: true, now: () => Date.now(), ...opts })
  const real = globalThis.fetch
  const served = fixtureFetch(fx)
  const requests: string[] = []
  let isDown = false
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url
    requests.push(`${init?.method ?? 'GET'} ${url}`)
    if (isDown) throw new TypeError('Failed to fetch')
    return served(input, init)
  }) as typeof fetch
  markSignedIn()
  rememberUser(fx.data.auth.signedIn)
  installOffline()
  return {
    fx,
    requests,
    count: (p) => requests.filter((r) => p.test(r)).length,
    down: () => void (isDown = true),
    up: () => void (isDown = false),
    restore: () => {
      globalThis.fetch = real
      setOfflineHooks({})
      rememberUser(null)
    },
  }
}

export const S = () => spaceApi('default')
export const make = (folder: string, title: string, body = `${title} 的正文\n`, extra: Record<string, unknown> = {}) =>
  S().createPage({ folder, meta: { title, ...extra }, body })
