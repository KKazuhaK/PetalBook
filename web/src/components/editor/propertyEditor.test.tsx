// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { FileOpsProvider } from '@/components/files/FileOps'
import { SidebarProvider } from '@/components/ui/sidebar'
import PageEditor from '@/pages/PageEditor'
import { t } from '@/i18n'
import { tp } from '@/i18n/props'
import type { MockServer } from '@/api/mock/server'
import { byLabel, click, installDomShims, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'

// How the panel is tied to the page editor: it is its own chunk, asked for only by
// a page that has a type; what is changed in it goes into the same draft as the
// title and the tags, and from there into the file.

// Counts the times the panel's code is loaded (the factory runs on the first import).
const chunk = vi.hoisted(() => ({ loaded: 0 }))
vi.mock('@/components/editor/PropertyPanel', async (importOriginal) => {
  chunk.loaded++
  return importOriginal()
})

beforeAll(() => {
  installDomShims()
  if (!Range.prototype.getClientRects) {
    Range.prototype.getClientRects = () => [] as unknown as DOMRectList
    Range.prototype.getBoundingClientRect = () => new DOMRect()
  }
})
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const json = 'application/json'
const send = (server: MockServer, method: string, path: string, body: unknown) =>
  server.handle(method, `/api/spaces/default${path}`, JSON.stringify(body), json)

const routes = (element: React.ReactNode) => [
  {
    path: '/s/:space/p/:id',
    element: (
      <SidebarProvider>
        <QuickCaptureProvider>
          <FileOpsProvider>{element}</FileOpsProvider>
        </QuickCaptureProvider>
      </SidebarProvider>
    ),
  },
]

// Single-user mode: a type bound to `Work`, a page in it, and one outside it.
async function open(which: 'typed' | 'untyped', meta: Record<string, unknown> = { status: 'todo', owner: 'Ann' }) {
  let id = ''
  m = await mount(null, {
    routes: routes(<PageEditor />),
    entries: (server) => {
      send(server, 'PUT', '/types/task', {
        title: '任务',
        fields: [
          { key: 'status', title: '状态', kind: 'select', options: ['todo', 'doing', 'done'] },
          { key: 'points', title: '点数', kind: 'number' },
        ],
      })
      send(server, 'POST', '/folders', { path: 'Work' })
      send(server, 'PUT', '/folders/type', { folder: 'Work', type: 'task' })
      const page = send(server, 'POST', '/pages', {
        folder: which === 'typed' ? 'Work' : 'Other',
        meta: { title: '一页', ...meta },
        body: '正文\n',
      }).body as { id: string }
      id = page.id
      return [`/s/default/p/${id}`]
    },
  })
  await settle(
    () => document.querySelector('.pb-prose') !== null && document.body.textContent?.includes('正文') === true,
    'the editor',
  )
  return id
}

describe('the property panel in the page editor', () => {
  // This one runs first on purpose: the chunk is counted from the start of the file.
  it('is not loaded, and the types are not asked for, by a page without a type', async () => {
    await open('untyped')
    await new Promise((r) => setTimeout(r, 100))
    expect(document.querySelector('[data-slot="property-panel"]')).toBeNull()
    expect(chunk.loaded).toBe(0)
    expect(m!.requests.some((r) => r.url.endsWith('/types'))).toBe(false)
  })

  it('shows below the title for a page that takes its type from its folder', async () => {
    await open('typed')
    await settle(() => document.querySelector('[data-slot="property-panel"] select') !== null, 'the panel')
    expect(chunk.loaded).toBe(1)
    expect(document.querySelector('[data-slot="property-panel"]')!.textContent).toContain('任务')
    expect((byLabel('状态') as HTMLSelectElement).value).toBe('todo')
    // Placed after the title, the subtitle and the tags, before the text.
    const panel = document.querySelector('[data-slot="property-panel"]')!
    expect(
      document.querySelector('.pb-title')!.compareDocumentPosition(panel) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(
      panel.compareDocumentPosition(document.querySelector('.pb-prose')!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(panel.querySelector('[data-slot="not-in-type"]')!.textContent).toContain('owner')
  })

  it('puts a change into the draft with the title, and saving writes it, leaving the other keys as they were', async () => {
    const id = await open('typed', { status: 'todo', owner: 'Ann', points: 'lots' })
    await settle(() => byLabel('状态') !== null, 'the panel')
    await type(byLabel('状态') as HTMLSelectElement, 'doing')
    await click(byLabel(t.editor.saveVersion))
    await settle(() => (m!.server.peek(id)?.meta.status ?? '') === 'doing', 'the file')
    const meta = m!.server.peek(id)!.meta
    // The value that does not fit the field is kept, as are the key that is not in the type and the title.
    expect(meta).toMatchObject({ title: '一页', status: 'doing', owner: 'Ann', points: 'lots' })
    expect(byLabel('点数')!.getAttribute('aria-invalid')).toBe('true')
    // No type was written into the header: the page takes its folder's.
    expect('type' in meta).toBe(false)
  })

  it('clearing a key that is not in the type removes it from the file', async () => {
    const id = await open('typed')
    await settle(() => document.querySelector('[data-slot="not-in-type"]') !== null, 'the group')
    await click(byLabel(tp.panel.clearKey('owner')))
    await click(byLabel(t.editor.saveVersion))
    await settle(() => !('owner' in (m!.server.peek(id)?.meta ?? { owner: 1 })), 'the file')
    expect(m!.server.peek(id)!.meta.status).toBe('todo')
  })

  it('a change goes to the server as a draft before anything is saved', async () => {
    const id = await open('typed')
    await settle(() => byLabel('点数') !== null, 'the panel')
    await type(byLabel('点数') as HTMLInputElement, '7')
    await settle(() => m!.server.peekDraft('local', id)?.meta.points === 7, 'the draft', 300)
    // The file is as it was until the draft is committed.
    expect(m!.server.peek(id)!.meta.points).toBeUndefined()
  })
})

describe('the property panel for someone who may only read', () => {
  it('is shown with its values, read only, and nothing is sent', async () => {
    m = await mount(null, {
      signIn: true,
      server: { mode: 'accounts' },
      routes: routes(<PageEditor />),
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'viewer')
        owner.request('PUT', `/api/spaces/${space}/types/task`, {
          title: '任务',
          fields: [{ key: 'status', title: '状态', kind: 'select', options: ['todo', 'doing'] }],
        })
        owner.request('POST', `/api/spaces/${space}/folders`, { path: 'Work' })
        owner.request('PUT', `/api/spaces/${space}/folders/type`, { folder: 'Work', type: 'task' })
        const page = owner.request('POST', `/api/spaces/${space}/pages`, {
          folder: 'Work',
          meta: { title: '共享页', status: 'doing', owner: 'Ann' },
          body: '正文',
        }).body as { id: string }
        return [`/s/${space}/p/${page.id}`]
      },
    })
    await settle(() => document.querySelector('[data-slot="property-panel"] select') !== null, 'the panel')
    expect((byLabel('状态') as HTMLSelectElement).disabled).toBe(true)
    expect((byLabel('状态') as HTMLSelectElement).value).toBe('doing')
    expect(document.querySelector('[data-slot="not-in-type"] button')).toBeNull()
    expect(m.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login')).toEqual([])
  })
})
