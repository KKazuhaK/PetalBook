// @vitest-environment jsdom
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { sharedSpace } from '@/api/fixtures/data/model'
import type { FixtureServer } from '@/api/fixtures/server'
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
  type,
  type FxMounted,
} from '@/testing/harness'
import { FileOpsProvider } from './FileOps'

// Making a page from a template, and a folder's default template, against the fixture server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const send = (fx: FixtureServer, method: string, path: string, body: unknown, space = 'default') =>
  fx.on(space)(method, path, body)
const seed = (s: FixtureServer) => {
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

async function open(folder: string, scene: (s: FixtureServer) => void = seed) {
  m = await mount(view(folder), {
    fixtures: true,
    entries: (server) => (scene(server), [`/s/default/f/${encodeURIComponent(folder)}`]),
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

  it('is a quick note in the inbox: no question, and the request names no template (the server applies the quick note default)', async () => {
    const inbox = 'Inbox'
    await open(inbox, (s) => {
      seed(s)
      send(s, 'PUT', '/templates/defaults', { quickNote: '会议' })
    })
    await click(newButton())
    await settle(() => posts().length === 1, 'the request')
    expect(posts()[0]).toEqual({ folder: inbox, body: '', tz })
    expect(picker()).toBeNull()
    await settle(() => locationText().includes('/p/'), 'the page is opened')
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
    // The server says the template is gone by the time the request arrives.
    m!.fx.fail('POST', '/api/spaces/default/pages', 404, { code: 'template_not_found' })
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
    m!.fx.call('GET', '/api/spaces/default/templates').body as {
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
    m!.fx.fail('PUT', '/api/spaces/default/templates/defaults', 404, { code: 'template_not_found' })
    await click(
      [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === tt.setDefault.confirm),
    )
    await settle(() => document.body.textContent!.includes(tt.errors.template_not_found), 'the message')
  })

  it('is not in the menu of someone who writes pages but does not manage the space', async () => {
    let space = ''
    m = await mount(view(''), {
      signIn: true,
      fixtures: { mode: 'accounts' },
      entries: (server) => {
        const shared = sharedSpace(server.data, 'editor')
        space = shared.info.id
        send(server, 'POST', '/folders', { path: 'Work' }, space)
        send(server, 'PUT', '/templates/日记', { baseRev: '', meta: {}, body: '' }, space)
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
