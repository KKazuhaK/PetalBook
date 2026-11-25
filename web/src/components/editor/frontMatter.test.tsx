// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { FileOpsProvider } from '@/components/files/FileOps'
import { SidebarProvider } from '@/components/ui/sidebar'
import PageEditor from '@/pages/PageEditor'
import { t } from '@/i18n'
import { tp } from '@/i18n/props'
import type { MockServer } from '@/api/mock/server'
import { click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'

// The header of a page is edited by value: the source view holds the body only (and the header's YAML only for an
// administrator who asks), the keys come from the type, the system fields are shown and not editable, and renaming a
// key is one action for the type and all its pages.

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

const send = (server: MockServer, method: string, path: string, body: unknown) =>
  server.handle(method, `/api/spaces/default${path}`, JSON.stringify(body), 'application/json') as { body: any }
const routes = [
  {
    path: '/s/:space/p/:id',
    element: (
      <SidebarProvider>
        <QuickCaptureProvider>
          <FileOpsProvider>
            <PageEditor />
          </FileOpsProvider>
        </QuickCaptureProvider>
      </SidebarProvider>
    ),
  },
]

async function open(mode: 'rich' | 'source' = 'rich') {
  localStorage.setItem('pb.editor.mode', mode)
  let ids: string[] = []
  m = await mount(null, {
    routes,
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
      const a = send(server, 'POST', '/pages', {
        folder: 'Work',
        meta: { title: '一页', status: 'todo' },
        body: '正文\n',
      }).body
      const b = send(server, 'POST', '/pages', {
        folder: 'Work',
        meta: { title: '二页', status: 'done' },
        body: 'x\n',
      }).body
      ids = [a.id, b.id]
      return [`/s/default/p/${a.id}`]
    },
  })
  await settle(() => document.querySelector('[data-slot="system-fields"]') !== null, 'the editor')
  return ids
}
const get = (id: string) => send(m!.server, 'GET', `/pages/${id}`, undefined).body

describe('the source view', () => {
  it('shows the body only, and has the header only when an administrator asks for it', async () => {
    await open('source')
    await settle(() => document.querySelector('.cm-content') !== null, 'the source')
    const text = () => document.querySelector('.cm-content')!.textContent ?? ''
    expect(text()).toContain('正文')
    expect(text()).not.toMatch(/title:/)
    expect(text()).not.toContain('---')
    // The single-user administrator may bring the header in, as an advanced mode.
    const box = document.querySelector<HTMLInputElement>('[data-slot="source-header-toggle"]')!
    expect(box).not.toBeNull()
    await click(box)
    await settle(() => /title:\s*"?一页/.test(text()), 'the header in the text')
    expect(text()).toMatch(/status:\s*"?todo/)
    await click(box)
    await settle(() => !/title:\s*"?一页/.test(text()), 'the body only again')
  })
})

describe('the properties of a page', () => {
  it('lists the keys of the type, and the system fields as text that cannot be changed', async () => {
    const [a] = await open()
    await settle(() => document.querySelector('[data-slot="property-panel"] select') !== null, 'the panel')
    const sys = document.querySelector('[data-slot="system-fields"]')!
    expect(sys.querySelector('[data-field="id"]')!.textContent).toBe(a)
    expect(sys.querySelector('input, textarea, select')).toBeNull()
    // Only values change: there is no way to name a key of one's own in the panel.
    expect(document.querySelector('[data-slot="property-panel"] input[placeholder*="key" i]')).toBeNull()
  })

  it('renames a key for the type and all its pages in one go, for an administrator', async () => {
    const [a, b] = await open()
    await settle(() => document.querySelector('[data-slot="rename-key"]') !== null, 'the rename button')
    await click(document.querySelector('[data-slot="rename-key"]'))
    await settle(() => document.querySelector('[role="dialog"] input') !== null, 'the dialog')
    await type(document.querySelector<HTMLInputElement>('[role="dialog"] input')!, 'state')
    await click(
      [...document.querySelectorAll('[role="dialog"] button')].find((x) => x.textContent === tp.panel.renameConfirm),
    )
    await settle(() => document.querySelector('[role="dialog"]') === null, 'renamed')
    expect(get(a).meta).toMatchObject({ state: 'todo' })
    expect(get(a).meta.status).toBeUndefined()
    expect(get(b).meta).toMatchObject({ state: 'done' })
    expect(t.modes.withHeader).toBeTruthy()
  })

  it('takes the renamed header of the open page as its base, keeping what is typed, so no save brings the old key back', async () => {
    const [a] = await open()
    await settle(() => document.querySelector('[data-field="points"] input') !== null, 'the panel')
    // A value typed and not yet in the file.
    await type(document.querySelector<HTMLInputElement>('[data-field="points"] input')!, '3')
    await click(document.querySelector('[data-slot="rename-key"]'))
    await settle(() => document.querySelector('[role="dialog"] input') !== null, 'the dialog')
    await type(document.querySelector<HTMLInputElement>('[role="dialog"] input')!, 'state')
    await click(
      [...document.querySelectorAll('[role="dialog"] button')].find((x) => x.textContent === tp.panel.renameConfirm),
    )
    await settle(() => document.querySelector('[role="dialog"]') === null, 'renamed')
    // The panel shows the new key with the page's value, and the typed value is still there.
    await settle(() => document.querySelector('[data-field="state"]') !== null, 'the new key in the panel')
    expect(document.querySelector<HTMLInputElement>('[data-field="points"] input')!.value).toBe('3')
    // What is stored next is typed on the renamed file: the new key, never the old one.
    await settle(() => {
      const p = get(a)
      const meta = p.draft && !p.draft.stale ? p.draft.meta : null
      return !!meta && meta.points === 3 && meta.state === 'todo'
    }, 'the draft on the renamed file')
    const p = get(a)
    expect(p.draft.meta.status).toBeUndefined()
    expect(p.draft.baseRev).toBe(p.rev)
    expect(document.querySelector('[role="alertdialog"], [data-slot="conflict"]')).toBeNull()
  })
})

describe('the icon of the open page', () => {
  it('shows the icon at once once it is saved, and the next store keeps it with what is typed', async () => {
    const [a] = await open()
    await settle(() => document.querySelector('[data-field="points"] input') !== null, 'the panel')
    await type(document.querySelector<HTMLInputElement>('[data-field="points"] input')!, '3')
    const button = () => [...document.querySelectorAll('button')].find((b) => b.textContent === t.icons.menu)
    await settle(() => button() !== undefined, 'the icon button')
    await click(button())
    await settle(() => document.querySelector('[data-slot="icon-picker"]') !== null, 'the picker')
    await type(document.querySelector<HTMLInputElement>('[data-slot="icon-picker"] input[type="search"]')!, '日历')
    await click(document.querySelector('[data-slot="icon-picker"] [role="option"][title="calendar"]'))
    await click([...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === t.icons.save))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved')
    expect(get(a).meta.icon).toBe('calendar')
    await settle(() => document.querySelector('svg[data-icon="calendar"]') !== null, 'the icon in the header')
    await settle(() => {
      const p = get(a)
      return !!p.draft && !p.draft.stale && p.draft.meta.points === 3
    }, 'the typed value stored on the new file')
    expect(get(a).draft.meta.icon).toBe('calendar')
  })
})

describe('the presets of the sources', () => {
  it('are edited by an administrator, kept for the space, and suggested after the values pages use', async () => {
    await open()
    await click(document.querySelector('[data-slot="edit-presets"]'))
    await settle(() => document.querySelector('[role="dialog"] textarea') !== null, 'the dialog')
    await type(document.querySelector<HTMLTextAreaElement>('[role="dialog"] textarea')!, '甲\n乙\n甲\n')
    await click([...document.querySelectorAll('[role="dialog"] button')].find((x) => x.textContent === t.icons.save))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved')
    expect(send(m!.server, 'GET', '/appearance', undefined).body.sourcePresets).toEqual(['甲', '乙'])
  })
})
