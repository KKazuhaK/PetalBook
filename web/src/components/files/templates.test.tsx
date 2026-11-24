// @vitest-environment jsdom
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { PageListView } from '@/components/PageListView'
import { QuickCaptureFab, QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { tt } from '@/i18n/templates'
import { tzOffset } from '@/lib/templates'
import {
  click,
  installDomShims,
  locationText,
  LocationProbe,
  mount,
  settle,
  shareSpace,
  type,
  type Mounted,
} from '@/testing/harness'
import { FileOpsProvider } from './FileOps'

// Making a page from a template, and a folder's default template, against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const json = 'application/json'
const send = (server: MockServer, method: string, path: string, body: unknown, space = 'default') =>
  server.handle(method, `/api/spaces/${space}${path}`, JSON.stringify(body), json)
const seed = (s: MockServer) => {
  send(s, 'PUT', '/templates/日记', {
    baseRev: '',
    meta: { title: '{{date}} 日记', tags: ['日记'] },
    body: '## 今天\n',
  })
  send(s, 'PUT', '/templates/会议', { baseRev: '', meta: { title: '会议' }, body: '议程\n' })
  send(s, 'PUT', '/templates/defaults', { folder: '随笔', template: '日记' })
}

const view = (folder: string) => (
  <>
    <LocationProbe />
    <QuickCaptureProvider>
      <QuickCaptureFab />
      <SidebarProvider>
        <Routes>
          <Route
            path="/s/:space/f/*"
            element={
              <FileOpsProvider>
                <PageListView source={{ kind: 'folder', folder }} />
              </FileOpsProvider>
            }
          />
          <Route path="*" element={null} />
        </Routes>
      </SidebarProvider>
    </QuickCaptureProvider>
  </>
)

async function open(folder: string, scene: (s: MockServer) => void = seed) {
  m = await mount(view(folder), {
    entries: (server) => (scene(server), [`/s/default/f/${encodeURIComponent(folder)}`]),
    server: { mode: 'single' },
  })
  await settle(() => newButton() !== null, 'the listing')
}

const newButton = () => document.querySelector<HTMLButtonElement>(`button[aria-label="${t.list.newPage}"]`)
const picker = () => document.querySelector<HTMLSelectElement>('#new-page-template')
const posts = () =>
  m!.requests
    .filter((r) => r.method === 'POST' && r.url === '/api/spaces/default/pages')
    .map((r) => JSON.parse(r.body!) as Record<string, unknown>)
const tz = tzOffset()
const confirmButton = () =>
  [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === tt.pick.confirm)

describe('a new page', () => {
  it('is made at once, with the time zone and no template, when the space has none', async () => {
    await open('随笔', () => {})
    await click(newButton())
    await settle(() => posts().length === 1, 'the request')
    expect(posts()[0]).toEqual({ folder: '随笔', body: '', tz })
    expect(picker()).toBeNull()
    await settle(() => locationText().includes('/p/'), 'the page is opened')
  })

  it('asks for a template when the space has some, with the default of the folder selected', async () => {
    await open('随笔')
    await click(newButton())
    await settle(() => picker() !== null, 'the choice')
    expect(picker()!.value).toBe('日记')
    expect([...picker()!.options].map((o) => o.value)).toEqual(['', '会议', '日记'])
    expect([...picker()!.options][0].textContent).toBe(tt.pick.blank)
    expect(posts()).toEqual([])
    await click(confirmButton())
    await settle(() => posts().length === 1, 'the request')
    expect(posts()[0]).toEqual({ folder: '随笔', body: '', tz, template: '日记' })
    await settle(() => locationText().includes('/p/'), 'the page is opened')
    // The page starts from the template, with the variables filled in.
    const id = /\/p\/([^/?]+)/.exec(locationText())![1]
    const page = m!.server.handle('GET', `/api/spaces/default/pages/${id}`, undefined, null).body as {
      body: string
      meta: { title: string; tags: string[] }
    }
    expect(page.body).toBe('## 今天\n')
    expect(page.meta.title).toMatch(/^\d{4}-\d{2}-\d{2} 日记$/)
    expect(page.meta.tags).toEqual(['日记'])
  })

  it('sends "" for a blank page, so the default of the folder is not applied', async () => {
    await open('随笔')
    await click(newButton())
    await settle(() => picker() !== null, 'the choice')
    await type(picker()!, '')
    await click(confirmButton())
    await settle(() => posts().length === 1, 'the request')
    expect(posts()[0]).toEqual({ folder: '随笔', body: '', tz, template: '' })
    await settle(() => locationText().includes('/p/'), 'the page is opened')
    const id = /\/p\/([^/?]+)/.exec(locationText())![1]
    expect(m!.server.handle('GET', `/api/spaces/default/pages/${id}`, undefined, null).body).toMatchObject({ body: '' })
  })

  it('has no template selected in a folder that has no default, and sends the one picked', async () => {
    await open('归档')
    await click(newButton())
    await settle(() => picker() !== null, 'the choice')
    expect(picker()!.value).toBe('')
    await type(picker()!, '会议')
    await click(confirmButton())
    await settle(() => posts().length === 1, 'the request')
    expect(posts()[0]).toEqual({ folder: '归档', body: '', tz, template: '会议' })
  })

  it('is a quick note in the inbox: no question, the server uses the quick note default', async () => {
    m = await mount(view(''), { entries: ['/s/default/f/'], server: { mode: 'single' } })
    const inbox = (m.server.handle('GET', '/api/spaces', undefined, null).body as { spaces: { inbox: string }[] })
      .spaces[0].inbox
    await m.unmount()
    m = undefined
    await open(inbox, (s) => {
      seed(s)
      send(s, 'PUT', '/templates/defaults', { quickNote: '会议' })
    })
    await click(newButton())
    await settle(() => posts().length === 1, 'the request')
    expect(posts()[0]).toEqual({ folder: inbox, body: '', tz })
    expect(picker()).toBeNull()
    await settle(() => locationText().includes('/p/'), 'the page is opened')
    const id = /\/p\/([^/?]+)/.exec(locationText())![1]
    expect(m!.server.handle('GET', `/api/spaces/default/pages/${id}`, undefined, null).body).toMatchObject({
      body: '议程\n',
    })
  })

  it('is a quick note too from the quick note box: the time zone is sent and no template is named', async () => {
    await open('随笔')
    await click(document.querySelector(`button[aria-label="${t.capture.fab}"]`))
    await settle(() => document.querySelector('[role="dialog"] textarea') !== null, 'the box')
    await type(document.querySelector<HTMLTextAreaElement>('[role="dialog"] textarea')!, '路上想到的')
    await click([...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === t.capture.save))
    await settle(() => posts().length === 1, 'the request')
    expect(posts()[0]).toMatchObject({ body: '路上想到的\n', tz })
    expect(posts()[0]).not.toHaveProperty('template')
  })

  it('tells a template that went away meanwhile in words, and makes no page', async () => {
    await open('归档')
    await click(newButton())
    await settle(() => picker() !== null, 'the choice')
    await type(picker()!, '会议')
    const rev = (
      m!.server.handle('GET', '/api/spaces/default/templates', undefined, null).body as {
        templates: { name: string; rev: string }[]
      }
    ).templates.find((x) => x.name === '会议')!.rev
    expect(
      m!.server.handle('DELETE', `/api/spaces/default/templates/会议?baseRev=${rev}`, undefined, json).status,
    ).toBe(204)
    await click(confirmButton())
    await settle(() => document.body.textContent!.includes(tt.errors.template_not_found), 'the message')
    expect(locationText()).not.toContain('/p/')
    expect(picker()).toBeNull()
  })
})

const openMenu = async (folder: string) => {
  await settle(() => document.querySelector(`[data-file-key="f:${folder}"] a`) !== null, 'folders')
  await act(async () => {
    document
      .querySelector(`[data-file-key="f:${folder}"] a`)!
      .dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 40, clientY: 40 }))
  })
  await settle(() => document.querySelector('[role="menuitem"]') !== null, 'the menu')
}
const menuItem = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((e) => e.textContent?.trim() === label)
const defaultSelect = () => document.querySelector<HTMLSelectElement>('#set-template')
const defaultsPuts = () =>
  m!.requests.filter((r) => r.method === 'PUT' && r.url.endsWith('/templates/defaults')).map((r) => JSON.parse(r.body!))
const defaultsOf = () =>
  (
    m!.server.handle('GET', '/api/spaces/default/templates', undefined, null).body as {
      defaults: { folders: Record<string, string> }
    }
  ).defaults.folders

describe('the default template of a folder, from its menu', () => {
  it('shows the one it has, sets another, and takes it away', async () => {
    await open('', seed)
    await openMenu('随笔')
    await click(menuItem(t.manage.setTemplate))
    await settle(
      () => defaultSelect() !== null && defaultSelect()!.value === '日记',
      'the dialog with the default selected',
    )
    expect([...defaultSelect()!.options].map((o) => o.value)).toEqual(['', '会议', '日记'])
    await type(defaultSelect()!, '会议')
    await click(
      [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === tt.setDefault.confirm),
    )
    await settle(() => defaultsOf()['随笔'] === '会议', 'the default changed')
    expect(defaultsPuts()).toEqual([{ folder: '随笔', template: '会议' }])
    await settle(() => defaultSelect() === null, 'the dialog closed')
    // Again, to take it away.
    await openMenu('随笔')
    await click(menuItem(t.manage.setTemplate))
    await settle(() => defaultSelect() !== null && defaultSelect()!.value === '会议', 'the dialog')
    await type(defaultSelect()!, '')
    await click(
      [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === tt.setDefault.confirm),
    )
    await settle(() => defaultsOf()['随笔'] === undefined, 'the default gone')
    expect(defaultsPuts().at(-1)).toEqual({ folder: '随笔', template: '' })
  })

  it('says in words that a template is gone when it was deleted before the choice was sent', async () => {
    await open('', seed)
    await openMenu('归档')
    await click(menuItem(t.manage.setTemplate))
    await settle(() => defaultSelect() !== null && defaultSelect()!.options.length === 3, 'the dialog')
    await type(defaultSelect()!, '会议')
    const rev = (
      m!.server.handle('GET', '/api/spaces/default/templates', undefined, null).body as {
        templates: { name: string; rev: string }[]
      }
    ).templates.find((x) => x.name === '会议')!.rev
    m!.server.handle('DELETE', `/api/spaces/default/templates/会议?baseRev=${rev}`, undefined, json)
    await click(
      [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === tt.setDefault.confirm),
    )
    await settle(() => document.body.textContent!.includes(tt.errors.template_not_found), 'the message')
  })

  it('is not in the menu of someone who writes pages but does not manage the space', async () => {
    let space = ''
    m = await mount(view(''), {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const shared = shareSpace(server, 'editor')
        space = shared.space
        shared.owner.request('POST', `/api/spaces/${space}/folders`, { path: 'Work' })
        shared.owner.request('PUT', `/api/spaces/${space}/templates/日记`, { baseRev: '', meta: {}, body: '' })
        localStorage.setItem('pb.space', space)
        return [`/s/${space}/f/`]
      },
    })
    await openMenu('Work')
    expect(menuItem(t.manage.setTemplate)).toBeUndefined()
    expect(menuItem(t.manage.rename)).toBeDefined()
    // Making a page still offers the templates (reading them is reading the space).
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    await click(newButton())
    await settle(() => picker() !== null, 'the choice')
    expect(m!.requests.filter((r) => r.method === 'PUT')).toEqual([])
  })
})
