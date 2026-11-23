import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { api, spaceApi } from '@/api/client'
import { MOCK_ADMIN } from '@/api/mock/server'
import { DEFAULT_CONFIG } from './store'
import { configure, existingStore, hasCopy } from './manager'
import { pollOnce, startPolling, syncNow, onSynced } from './poller'
import { isOffline } from './status'
import { network, type Net } from './testkit'
import { syncSpace } from './sync'

let net: Net
let me = ''
beforeEach(async () => {
  net = network({ mode: 'accounts', oidc: null })
  me = (await api.login({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })).user.id
})
afterEach(() => net.restore())

const make = (space: string, title: string) => spaceApi(space).createPage({ meta: { title }, body: `${title}\n` })

async function twoSpaces() {
  const a = (await api.spaces()).spaces[0].id
  const b = (await api.createSpace('Second')).id
  for (const s of [a, b]) {
    await make(s, `in ${s}`)
    const store = (await configure(me, s, { ...DEFAULT_CONFIG }))!
    await syncSpace(store)
  }
  return { a, b }
}

describe('polling the heads', () => {
  it('asks the heads of all the spaces in one request, and the changes of none when nothing moved', async () => {
    const { a, b } = await twoSpaces()
    const before = net.requests.length
    await pollOnce(me)
    const sent = net.requests.slice(before)
    expect(sent).toHaveLength(1)
    expect(decodeURIComponent(sent[0])).toBe(`GET /api/sync/heads?space=${a}&space=${b}`.replace(a + '&', a + '&'))
    expect(sent[0]).toContain('/api/sync/heads')
    expect(isOffline()).toBe(false)
  })

  it('asks the changes of the space whose head moved, and of that one only', async () => {
    const { a, b } = await twoSpaces()
    await make(b, 'new in b')
    const synced: string[] = []
    const off = onSynced((e) => synced.push(`${e.space}:${e.result.changed}`))
    const before = net.requests.length
    await pollOnce(me)
    off()
    const sent = net.requests.slice(before).map((r) => r.replace(/\?.*/, ''))
    expect(sent.filter((r) => r.endsWith('/changes'))).toEqual([`GET /api/spaces/${b}/changes`])
    expect(sent.some((r) => r.includes(`/spaces/${a}/`))).toBe(false)
    expect(synced).toEqual([`${b}:true`])
    expect((await (await existingStore(me, b))!.catalog()).length).toBe(2)
    // Caught up: the next round is the heads alone.
    const again = net.requests.length
    await pollOnce(me)
    expect(net.requests.slice(again)).toHaveLength(1)
  })

  it('does nothing, and asks nothing, when this account keeps no copy', async () => {
    await pollOnce(me)
    expect(net.requests.filter((r) => /sync\/heads/.test(r))).toHaveLength(0)
  })

  it('a failure to reach the server says offline and changes nothing; the next round that gets through says online', async () => {
    const { b } = await twoSpaces()
    await make(b, 'x')
    net.down()
    await pollOnce(me)
    expect(isOffline()).toBe(true)
    expect((await (await existingStore(me, b))!.catalog()).length).toBe(1)
    net.up()
    await pollOnce(me)
    expect(isOffline()).toBe(false)
    expect((await (await existingStore(me, b))!.catalog()).length).toBe(2)
  })

  it('a space switched off is kept, read only, not asked about again, and syncs again when it is back', async () => {
    const { a, b } = await twoSpaces()
    await api.updateSpace(a, {}).catch(() => undefined)
    const adm = (path: string) => fetch(`/api/admin/spaces/${b}/${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })
    expect((await adm('disable')).status).toBe(204)
    await pollOnce(me)
    const st = await (await existingStore(me, b))!.state()
    expect(st.disabled).toBe(true)
    expect(hasCopy(me, b)).toBe(true)
    // Not asked about: its heads are missing, and nothing else is sent for it.
    const before = net.requests.length
    await pollOnce(me)
    expect(net.requests.slice(before).filter((r) => r.includes(`/spaces/${b}/`))).toEqual([])
    expect((await adm('enable')).status).toBe(204)
    await pollOnce(me)
    expect((await (await existingStore(me, b))!.state()).disabled).toBe(false)
  })

  it('a space the person cannot read any more is deleted from the device', async () => {
    const { b } = await twoSpaces()
    // The server refuses the heads of it, and answers 404 to the changes (the person was removed).
    const real = globalThis.fetch
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/sync/heads')) return new Response(JSON.stringify({ heads: {}, missing: [b] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      if (url.includes(`/spaces/${b}/changes`)) return new Response(JSON.stringify({ error: 'not_found', message: 'no such space' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
      return real(input, init)
    }) as typeof fetch
    await pollOnce(me)
    globalThis.fetch = real
    expect(hasCopy(me, b)).toBe(false)
  })

  it('syncNow is one at a time per space', async () => {
    const { b } = await twoSpaces()
    await make(b, 'more')
    const [r1, r2] = await Promise.all([syncNow(me, b), syncNow(me, b)])
    expect([r1.changed, r2.changed].filter(Boolean)).toHaveLength(1)
  })
})

describe('the timer', () => {
  const visibility = (v: 'visible' | 'hidden') => {
    Object.defineProperty(document, 'visibilityState', { value: v, configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
  }
  const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))
  afterEach(() => visibility('visible'))
  const heads = () => net.count(/sync\/heads/)

  it('asks at once, then every period while the page is visible, and not while it is hidden', async () => {
    await twoSpaces()
    const base = heads()
    const stop = startPolling(me, { every: 40 })
    await wait(25)
    expect(heads() - base).toBe(1)
    await wait(120)
    expect(heads() - base).toBeGreaterThanOrEqual(3)
    visibility('hidden')
    await wait(20)
    const frozen = heads()
    await wait(150)
    expect(heads()).toBe(frozen)
    // Back to the foreground: at once, without waiting for the period.
    visibility('visible')
    await wait(25)
    expect(heads()).toBeGreaterThan(frozen)
    stop()
    const stopped = heads()
    await wait(120)
    expect(heads()).toBe(stopped)
  })

  it('asks at once when the network returns', async () => {
    await twoSpaces()
    const stop = startPolling(me, { every: 60_000 })
    await wait(30)
    const base = heads()
    window.dispatchEvent(new Event('online'))
    await wait(30)
    expect(heads()).toBe(base + 1)
    stop()
  })
})
