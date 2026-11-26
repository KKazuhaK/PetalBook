// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { adminOf } from '@/api/fixtures/data/scenes'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { ts } from '@/i18n/storage'
import SettingsRoute from './SettingsRoute'

// The attachment repository in a space's sync settings, against the fixture server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
    <Route path="*" element={<div>elsewhere</div>} />
  </Routes>
)
const panel = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')
const text = () => panel()?.textContent ?? ''
const button = (label: string) =>
  [...panel()!.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label)

async function open(follow: boolean) {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts' },
    entries: (server) => {
      const req = adminOf(server)
      const space = (req('POST', '/api/spaces', { name: 'Wiki' }).body as { id: string }).id
      localStorage.setItem('pb.space', space)
      if (follow)
        req('PUT', `/api/spaces/${space}/remote`, {
          url: 'https://github.com/kazuhahub/wiki.git',
          credential: { type: 'token', token: 'x' },
        })
      return ['/settings/sync']
    },
  })
  await settle(() => panel() !== null && text().length > 0, 'the sync settings')
}

describe('the attachment repository in the sync settings', () => {
  it('is offered only to a space that follows a remote', async () => {
    await open(false)
    await settle(() => text().includes('Sync') || text().length > 20, 'settings')
    expect(text()).not.toContain(ts.assets.title)
  })

  it('connects one, moves the attachments with progress, and cannot be taken away after', async () => {
    await open(true)
    const field = () =>
      [...panel()!.querySelectorAll<HTMLInputElement>('input')].find((i) => i.placeholder.includes('-assets.git'))
    await settle(() => field() !== undefined, 'the attachment section')
    const url = field()!
    await type(url, 'https://github.com/kazuhahub/wiki-assets.git')
    await click(button(ts.assets.connect))
    await settle(() => m!.requests.some((r) => r.method === 'PUT' && r.url.endsWith('/remote/assets')), 'the PUT')
    expect(
      JSON.parse(m!.requests.find((r) => r.method === 'PUT' && r.url.endsWith('/remote/assets'))!.body!),
    ).toMatchObject({ url: 'https://github.com/kazuhahub/wiki-assets.git', credential: { type: 'token' } })
    await settle(() => text().includes(ts.assets.state.ok), 'the connected repository')
    await click(button(ts.assets.moveStart))
    await settle(
      () => m!.requests.some((r) => r.method === 'POST' && r.url.endsWith('/remote/assets/move')),
      'the move request',
    )
    await settle(() => panel()!.querySelector('[data-slot="assets-progress"]') !== null, 'the progress')
    // The server finishes the move; the page reads it again.
    const space = localStorage.getItem('pb.space')!
    const assets = m!.fx.data.area.remotes[space].assets!
    assets.move = { state: 'done', files: 1, filesDone: 1, bytes: 3, bytesDone: 3, error: '' }
    await m!.qc.invalidateQueries()
    await settle(() => text().includes(ts.assets.states.done), 'the move to end', 400)
    expect(button(ts.assets.disconnect)).toBeUndefined()
  })
})
