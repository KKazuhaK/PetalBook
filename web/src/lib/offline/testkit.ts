// What the tests of the offline copy share: a mock server as the network, a fresh IndexedDB, and ways to make
// the network go away.

import { IDBFactory } from 'fake-indexeddb'
import { rememberUser, setOfflineHooks, spaceApi, markSignedIn } from '@/api/client'
import { mockFetch } from '@/api/mock/fetch'
import { createMockServer, type MockOptions, type MockServer } from '@/api/mock/server'
import { installOffline } from './index'
import { forgetOpenStores } from './manager'
import { resetStatus } from './status'

export interface Net {
  server: MockServer
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
  resetStatus(false)
}

// A signed-in single-user server (the one user is "local"), empty, as the network.
export function network(opts: MockOptions = {}): Net {
  freshDevice()
  const server = createMockServer({ empty: true, mode: 'single', now: () => Date.now(), ...opts })
  const real = globalThis.fetch
  const mocked = mockFetch(server)
  const requests: string[] = []
  let isDown = false
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url
    requests.push(`${init?.method ?? 'GET'} ${url}`)
    if (isDown) throw new TypeError('Failed to fetch')
    return mocked(input, init)
  }) as typeof fetch
  markSignedIn()
  rememberUser('local')
  installOffline()
  return {
    server,
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
export const make = (folder: string, title: string, body = `${title} 的正文\n`, extra: Record<string, unknown> = {}) => S().createPage({ folder, meta: { title, ...extra }, body })
