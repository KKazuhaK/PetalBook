// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { FolderList } from '@/pages/Lists'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { tp } from '@/i18n/props'
import { click, installDomShims, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'
import { FileOpsProvider } from './FileOps'

// "Set type…" in a folder's context menu: for those who manage the space, a small
// dialog to pick a type or none.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const shell = (
  <SidebarProvider>
    <QuickCaptureProvider>
      <Routes>
        <Route
          path="/s/:space/f/*"
          element={
            <FileOpsProvider>
              <FolderList />
            </FileOpsProvider>
          }
        />
      </Routes>
    </QuickCaptureProvider>
  </SidebarProvider>
)

const entry = (key: string) => document.querySelector<HTMLElement>(`[data-file-key="${key}"]`)
const menuItems = () => [...document.querySelectorAll('[role="menuitem"]')].map((e) => e.textContent?.trim())
const dialog = () => document.querySelector<HTMLElement>('[data-slot="dialog-content"]')
const json = 'application/json'

const seed = (server: MockServer) => {
  const send = (method: string, path: string, body: unknown) =>
    server.handle(method, `/api/spaces/default${path}`, JSON.stringify(body), json)
  send('PUT', '/types/task', { title: '任务', fields: [{ key: 'status', kind: 'text' }] })
  send('PUT', '/types/memo', { title: '备忘', fields: [{ key: 'note', kind: 'text' }] })
}

async function rightClick(key: string) {
  await settle(() => entry(key) !== null, 'the folder')
  await act(async () => {
    entry(key)!
      .querySelector('a')!
      .dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 40, clientY: 40 }))
  })
  await settle(() => menuItems().length > 0, 'the menu')
}

describe('Set type… on a folder', () => {
  it('opens a dialog with the types and "no type", and binds the folder to the one picked', async () => {
    m = await mount(shell, { entries: (server) => (seed(server), ['/s/default/f']) })
    await rightClick('f:随笔')
    expect(menuItems()).toContain(t.manage.setType)
    await click(
      [...document.querySelectorAll('[role="menuitem"]')].find((e) => e.textContent?.trim() === t.manage.setType),
    )
    await settle(() => dialog() !== null && dialog()!.querySelectorAll('option').length === 3, 'the dialog')
    const select = dialog()!.querySelector('select')!
    expect([...select.options].map((o) => o.textContent)).toEqual([tp.setType.none, '备忘', '任务'])
    expect(select.value).toBe('')
    await type(select, 'task')
    await click([...dialog()!.querySelectorAll('button')].find((b) => b.textContent === tp.setType.confirm))
    await settle(() => dialog() === null, 'the dialog closed')
    const sent = m.requests.find((r) => r.method === 'PUT' && r.url.endsWith('/folders/type'))!
    expect(JSON.parse(sent.body!)).toEqual({ folder: '随笔', type: 'task' })
    expect(
      (m.server.handle('GET', '/api/spaces/default/types', undefined, null).body as { bindings: unknown[] }).bindings,
    ).toEqual([{ folder: '随笔', type: 'task' }])
  })

  it('shows what a folder has and can take it away', async () => {
    m = await mount(shell, {
      entries: (server) => {
        seed(server)
        server.handle('PUT', '/api/spaces/default/folders/type', JSON.stringify({ folder: '随笔', type: 'memo' }), json)
        return ['/s/default/f']
      },
    })
    await rightClick('f:随笔')
    await click(
      [...document.querySelectorAll('[role="menuitem"]')].find((e) => e.textContent?.trim() === t.manage.setType),
    )
    await settle(() => dialog()?.querySelector('select')?.value === 'memo', 'the binding shown')
    await type(dialog()!.querySelector('select')!, '')
    await click([...dialog()!.querySelectorAll('button')].find((b) => b.textContent === tp.setType.confirm))
    await settle(() => dialog() === null, 'the dialog closed')
    const sent = m.requests.filter((r) => r.method === 'PUT' && r.url.endsWith('/folders/type')).at(-1)!
    expect(JSON.parse(sent.body!)).toEqual({ folder: '随笔', type: '' })
    expect(
      (m.server.handle('GET', '/api/spaces/default/types', undefined, null).body as { bindings: unknown[] }).bindings,
    ).toEqual([])
  })

  it('says that a subfolder without a binding takes its type from the folder above', async () => {
    m = await mount(shell, {
      entries: (server) => {
        seed(server)
        server.handle('PUT', '/api/spaces/default/folders/type', JSON.stringify({ folder: '随笔', type: 'memo' }), json)
        return ['/s/default/f/随笔']
      },
    })
    await rightClick('f:随笔/2026')
    await click(
      [...document.querySelectorAll('[role="menuitem"]')].find((e) => e.textContent?.trim() === t.manage.setType),
    )
    await settle(() => dialog()?.textContent?.includes(tp.setType.inherited('备忘', '随笔')) === true, 'the note')
    expect(dialog()!.querySelector('select')!.value).toBe('')
  })

  it('is not on the menu of an editor, who may write pages but not manage the space', async () => {
    m = await mount(shell, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'editor')
        owner.request('POST', `/api/spaces/${space}/folders`, { path: 'Work' })
        return [`/s/${space}/f`]
      },
    })
    await rightClick('f:Work')
    expect(menuItems()).toEqual([
      t.manage.open,
      t.fav.addFolder,
      t.manage.rename,
      t.manage.select,
      t.manage.move,
      t.manage.delete,
    ])
  })
})
