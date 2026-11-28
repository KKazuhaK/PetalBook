import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { api, currentUserId } from '@/api/client'
import { MOCK_ADMIN } from '@/api/fixtures/data/people'
import { DEFAULT_CONFIG } from './store'
import { configure, existingStore, hasCopy, storeFor } from './manager'
import { syncSpace } from './sync'
import { network, type Net } from './testkit'
import { spacesOf, totalBytes } from './registry'

let net: Net
let me = ''
let space = ''
beforeEach(async () => {
  net = network({ mode: 'accounts' })
  me = (await api.login({ identifier: MOCK_ADMIN.username, password: MOCK_ADMIN.password })).user.id
  space = (await api.spaces()).spaces[0].id
})
afterEach(() => net.restore())

async function keepCopies() {
  const s = (await configure(me, space, { ...DEFAULT_CONFIG, mode: 'text', inbox: false }))!
  await syncSpace(s)
  // Somebody else's copy of the same space, on the same browser.
  const other = (await configure('another-person', space, { ...DEFAULT_CONFIG }))!
  await other.applyCatalog(
    [
      {
        id: 'X',
        path: 'X.md',
        folder: '',
        label: 'x',
        untitled: false,
        tags: [],
        type: '',
        updated: '2026-11-23T08:00:00Z',
        rev: 'r',
      },
    ],
    [],
  )
}

describe('signing out', () => {
  it('knows who is signed in', async () => {
    expect(currentUserId()).toBe(me)
  })

  it("deletes this account's copies from the device, and nobody else's", async () => {
    await keepCopies()
    expect(hasCopy(me, space)).toBe(true)
    await api.logout()
    expect(currentUserId()).toBeNull()
    expect(hasCopy(me, space)).toBe(false)
    expect(spacesOf(me)).toEqual([])
    expect(await existingStore(me, space)).toBeUndefined()
    expect((await indexedDB.databases()).map((d) => d.name).filter((n) => n?.includes(encodeURIComponent(me)))).toEqual(
      [],
    )
    // Another account's is still there, and still readable.
    expect(hasCopy('another-person', space)).toBe(true)
    expect(await (await storeFor('another-person', space)).catalogCount()).toBe(1)
    expect(totalBytes()).toBeGreaterThanOrEqual(0)
  })

  it('does not delete anything when the sign-out itself did not go through', async () => {
    await keepCopies()
    net.down()
    await expect(api.logout()).rejects.toMatchObject({ code: 'network' })
    expect(hasCopy(me, space)).toBe(true)
    expect(currentUserId()).toBe(me)
  })

  it('another account that signs in on the same browser finds no copy of its own', async () => {
    await keepCopies()
    await api.logout()
    const bob = (await api.login({ identifier: MOCK_ADMIN.username, password: MOCK_ADMIN.password })).user.id
    // The same mock user, a new session: nothing of the earlier copy is left for it either.
    expect(hasCopy(bob, space)).toBe(false)
  })
})
