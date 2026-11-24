// @vitest-environment jsdom
import { useEffect, useState } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { api } from '@/api/client'
import { DEFAULT_CONFIG, configure, hasCopy } from '@/lib/offline'
import { freshDevice } from '@/lib/offline/testkit'
import { adminRequests, installDomShims, mount, settle, type Mounted } from '@/testing/harness'
import { OfflineSync } from './OfflineSync'

// The app keeps the copies up to date only while the administrators allow offline copies (offline.copies).

beforeAll(installDomShims)
beforeEach(freshDevice)

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <Routes>
    <Route path="*" element={<OfflineSync />} />
  </Routes>
)
// The app starting when the test says so ("start"), after it has left a copy on the device.
function Late() {
  const [on, setOn] = useState(false)
  useEffect(() => {
    const go = () => setOn(true)
    window.addEventListener('start', go)
    return () => window.removeEventListener('start', go)
  }, [])
  return on ? <OfflineSync /> : null
}
const lateRoutes = (
  <Routes>
    <Route path="*" element={<Late />} />
  </Routes>
)
const heads = () => m!.requests.filter((r) => r.url.startsWith('/api/sync/heads')).length
const setting = (value: boolean) => adminRequests(m!.server)('PUT', '/api/admin/settings/offline.copies', { value, lower: { mode: value ? 'allow' : 'lock' } })

describe('the app when offline copies are allowed or not', () => {
  it('polls while allowed; when turned off it deletes this account\'s copies and stops asking', async () => {
    m = await mount(routes, { signIn: true, server: { mode: 'accounts' }, entries: ['/'] })
    const user = (await api.session()).user!.id
    const space = (await api.spaces()).spaces[0].id
    await configure(user, space, { ...DEFAULT_CONFIG })
    await configure('someone-else', space, { ...DEFAULT_CONFIG })
    // A poll of the heads while it is allowed (the network came back).
    window.dispatchEvent(new Event('online'))
    await settle(() => heads() > 0, 'a poll while allowed')
    expect(hasCopy(user, space)).toBe(true)

    setting(false)
    await m.qc.invalidateQueries({ queryKey: ['settings'] })
    await settle(() => !hasCopy(user, space), 'the copy is deleted')
    expect(hasCopy('someone-else', space)).toBe(true)
    const asked = heads()
    await new Promise((r) => setTimeout(r, 120))
    expect(heads()).toBe(asked)
  })

  it('on the next start the copy is deleted before any poll, and nothing is asked', async () => {
    let user = ''
    m = await mount(lateRoutes, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const admin = adminRequests(server)
        admin('PUT', '/api/admin/settings/offline.copies', { value: false, lower: { mode: 'lock' } })
        user = (admin('GET', '/api/session').body as { user: { id: string } }).user.id
        return ['/']
      },
    })
    const space = (await api.spaces()).spaces[0].id
    await configure(user, space, { ...DEFAULT_CONFIG })
    await act(async () => void window.dispatchEvent(new Event('start')))
    await settle(() => !hasCopy(user, space), 'the copy is deleted')
    await new Promise((r) => setTimeout(r, 100))
    expect(heads()).toBe(0)
  })

  it('a server that cannot say (the setting is not asked yet) deletes nothing', async () => {
    m = await mount(routes, { signIn: true, server: { mode: 'accounts' }, entries: ['/'] })
    const user = (await api.session()).user!.id
    const space = (await api.spaces()).spaces[0].id
    await configure(user, space, { ...DEFAULT_CONFIG })
    setting(true)
    await m.qc.invalidateQueries({ queryKey: ['settings'] })
    await new Promise((r) => setTimeout(r, 80))
    expect(hasCopy(user, space)).toBe(true)
  })
})
