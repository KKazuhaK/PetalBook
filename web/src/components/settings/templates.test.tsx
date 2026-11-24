// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { tt } from '@/i18n/templates'
import { click, installDomShims, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// The templates section of a space's settings, against the mock server.

// Counts the times the section's code is loaded (the factory runs on the first import).
const chunk = vi.hoisted(() => ({ loaded: 0 }))
vi.mock('./TemplatesPanel', async (importOriginal) => {
  chunk.loaded++
  return importOriginal()
})

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
  </Routes>
)
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')!
const panelText = () => dialog().textContent ?? ''
const button = (label: string) =>
  [...dialog().querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) as HTMLButtonElement | undefined
const row = (name: string) => dialog().querySelector<HTMLElement>(`li[data-template="${name}"]`)
const field = (id: string) =>
  dialog().querySelector<HTMLInputElement & HTMLTextAreaElement & HTMLSelectElement>(`#${id}`)!
const puts = () =>
  m!.requests.filter((r) => r.method === 'PUT' && /\/templates\/[^/]+$/.test(r.url) && !r.url.endsWith('/defaults'))
const lastPut = () =>
  JSON.parse(puts().at(-1)!.body!) as { baseRev: string; meta: Record<string, unknown>; body: string }
const defaultsPuts = () =>
  m!.requests.filter((r) => r.method === 'PUT' && r.url.endsWith('/templates/defaults')).map((r) => JSON.parse(r.body!))
const json = 'application/json'
const send = (server: MockServer, method: string, path: string, body: unknown) =>
  server.handle(method, `/api/spaces/default${path}`, JSON.stringify(body), json)
const listed = () =>
  m!.server.handle('GET', '/api/spaces/default/templates', undefined, null).body as {
    templates: { name: string; rev: string; meta: Record<string, unknown> }[]
    defaults: { folders: Record<string, string>; quickNote: string }
  }

const DIARY = {
  baseRev: '',
  meta: { title: '{{date}} 日记', tags: ['日记'], status: 'draft' },
  body: '## 今天\n{{time}}\n',
}
const seed = (s: MockServer) => {
  send(s, 'PUT', '/templates/日记', DIARY)
  send(s, 'PUT', '/templates/会议', { baseRev: '', meta: { title: '会议' }, body: '议程\n' })
  send(s, 'POST', '/folders', { path: 'Work' })
}

async function openSingle(scene: (server: MockServer) => void = seed) {
  m = await mount(routes, { entries: (server) => (scene(server), ['/settings/templates']), server: { mode: 'single' } })
  await settle(() => dialog() !== null && panelText().includes(tt.settings.title), 'the section')
}

async function openAs(role: 'editor' | 'viewer') {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      const { space, owner } = shareSpace(server, role)
      owner.request('PUT', `/api/spaces/${space}/templates/日记`, DIARY)
      owner.request('POST', `/api/spaces/${space}/folders`, { path: 'Work' })
      owner.request('PUT', `/api/spaces/${space}/templates/defaults`, { folder: 'Work', template: '日记' })
      localStorage.setItem('pb.space', space)
      return ['/settings/templates']
    },
  })
  await settle(() => dialog() !== null && panelText().includes(tt.settings.title), 'the section')
}

const alertDialog = () => document.querySelector('[role="alertdialog"]')

describe('the templates section: someone who manages the space', () => {
  it('is its own chunk: another section does not load it, this one does', async () => {
    m = await mount(routes, { entries: ['/settings/general'], server: { mode: 'single' } })
    await settle(() => dialog() !== null && panelText().length > 0, 'the dialog')
    expect(chunk.loaded).toBe(0)
    await m.unmount()
    m = undefined
    await openSingle()
    expect(chunk.loaded).toBe(1)
  })

  it('lists the templates, says where each is the default, and names the variables in the help', async () => {
    await openSingle((s) => {
      seed(s)
      send(s, 'PUT', '/templates/defaults', { folder: 'Work', template: '日记' })
      send(s, 'PUT', '/templates/defaults', { quickNote: '会议' })
    })
    expect(row('日记')!.textContent).toContain(tt.settings.folderBadge(1))
    expect(row('会议')!.textContent).toContain(tt.settings.quickBadge)
    for (const v of ['{{date}}', '{{time}}', '{{datetime}}']) expect(panelText()).toContain(v)
    expect(dialog().querySelector('li[data-default="Work"]')!.textContent).toContain('日记')
    expect(field('template-quick').value).toBe('会议')
  })

  it('says so when there are no templates', async () => {
    await openSingle(() => {})
    expect(panelText()).toContain(tt.settings.none)
    expect(panelText()).toContain(tt.defaults.noFolders)
  })

  it('makes a template: the name is checked first, and what is sent is the header and the body', async () => {
    await openSingle()
    await click(button(tt.settings.newTemplate))
    const create = () => button(tt.settings.create)!
    expect(create().disabled).toBe(true)
    await type(field('template-name'), '日记')
    expect(panelText()).toContain(tt.settings.nameExists)
    expect(create().disabled).toBe(true)
    await type(field('template-name'), 'a/b')
    expect(panelText()).toContain(tt.settings.nameChars)
    await type(field('template-name'), '周报')
    expect(create().disabled).toBe(false)
    await type(field('template-title'), ' 周报 {{date}} ')
    await type(field('template-tags'), '周报, 工作，周报')
    await type(field('template-body'), '## 本周\n')
    await click(create())
    await settle(() => puts().length === 1, 'the request')
    expect(puts()[0].url).toContain('/templates/%E5%91%A8%E6%8A%A5')
    expect(lastPut()).toEqual({
      baseRev: '',
      meta: { title: '周报 {{date}}', tags: ['周报', '工作'] },
      body: '## 本周\n',
    })
    await settle(() => row('周报') !== null, 'the template in the list')
  })

  it('offers the types of the space, and sends the one picked', async () => {
    await openSingle((s) => {
      seed(s)
      send(s, 'PUT', '/types/task', { title: '任务', fields: [{ key: 'status', kind: 'text' }] })
    })
    await click(button(tt.settings.newTemplate))
    await type(field('template-name'), 'todo')
    expect([...field('template-type').options].map((o) => o.value)).toEqual(['', 'task'])
    await type(field('template-type'), 'task')
    await click(button(tt.settings.create))
    await settle(() => puts().length === 1, 'the request')
    expect(lastPut().meta).toEqual({ type: 'task' })
  })

  it('opens a template with its body, changes it from its rev and keeps the header fields the form does not have', async () => {
    await openSingle()
    await click(row('日记')!.querySelector('button'))
    await settle(() => field('template-body') !== null && field('template-body').value !== '', 'the body')
    expect(field('template-body').value).toBe('## 今天\n{{time}}\n')
    expect(field('template-title').value).toBe('{{date}} 日记')
    expect(field('template-tags').value).toBe('日记')
    expect(field('template-name').readOnly).toBe(true)
    expect(panelText()).toContain(tt.settings.otherFields(1))
    expect(button(tt.settings.save)!.disabled).toBe(true)
    const rev = listed().templates.find((x) => x.name === '日记')!.rev
    await type(field('template-body'), '## 今天\n{{datetime}}\n')
    await click(button(tt.settings.save))
    await settle(() => puts().length === 1, 'the request')
    expect(lastPut()).toEqual({
      baseRev: rev,
      meta: { status: 'draft', title: '{{date}} 日记', tags: ['日记'] },
      body: '## 今天\n{{datetime}}\n',
    })
    await settle(() => button(tt.settings.save)?.disabled === true, 'saved')
    expect(listed().templates.find((x) => x.name === '日记')!.rev).not.toBe(rev)
  })

  it('does not save over a change someone else made meanwhile: it says so, and the template is read again', async () => {
    await openSingle()
    await click(row('日记')!.querySelector('button'))
    await settle(() => field('template-body') !== null && field('template-body').value !== '', 'the body')
    await type(field('template-body'), '我的正文')
    const rev = listed().templates.find((x) => x.name === '日记')!.rev
    expect(
      send(m!.server, 'PUT', '/templates/日记', { baseRev: rev, meta: { title: '别人的标题' }, body: '别人的正文' })
        .status,
    ).toBe(200)
    await click(button(tt.settings.save))
    await settle(() => document.body.textContent!.includes(tt.settings.changedMeanwhile), 'the message')
    // The form is made again from the template as it is now, not from what was typed.
    await settle(
      () => field('template-title')?.value === '别人的标题' && field('template-body').value === '别人的正文',
      'the template read again',
    )
    expect(listed().templates.find((x) => x.name === '日记')!.meta.title).toBe('别人的标题')
  })

  it('deletes a template only after a question that says what goes with it', async () => {
    await openSingle((s) => {
      seed(s)
      send(s, 'PUT', '/templates/defaults', { folder: 'Work', template: '日记' })
      send(s, 'PUT', '/templates/defaults', { quickNote: '日记' })
    })
    await click(row('日记')!.querySelector('button'))
    await settle(() => button(tt.settings.delete) !== undefined, 'the editor')
    await click(button(tt.settings.delete))
    await settle(() => alertDialog() !== null, 'the question')
    expect(alertDialog()!.textContent).toContain(tt.settings.deleteHelp(1, true))
    expect(m!.requests.some((r) => r.method === 'DELETE')).toBe(false)
    await click([...alertDialog()!.querySelectorAll('button')].find((b) => b.textContent === tt.cancel))
    await settle(() => alertDialog() === null, 'the question closed')
    expect(m!.requests.some((r) => r.method === 'DELETE')).toBe(false)
    await click(button(tt.settings.delete))
    await settle(() => alertDialog() !== null, 'the question')
    await click([...alertDialog()!.querySelectorAll('button')].find((b) => b.textContent === tt.settings.deleteConfirm))
    await settle(() => row('日记') === null, 'the template gone')
    expect(
      m!.requests.some(
        (r) => r.method === 'DELETE' && /\/templates\/%E6%97%A5%E8%AE%B0\?baseRev=[0-9a-f]{40}$/.test(r.url),
      ),
    ).toBe(true)
    // The defaults that named it went with it.
    await settle(() => dialog().querySelector('li[data-default]') === null, 'the folder default gone')
    expect(field('template-quick').value).toBe('')
    expect(listed().defaults).toEqual({ folders: {}, quickNote: '' })
  })

  it('does not delete what someone else changed meanwhile, and says so', async () => {
    await openSingle()
    await click(row('会议')!.querySelector('button'))
    await settle(() => button(tt.settings.delete) !== undefined, 'the editor')
    const rev = listed().templates.find((x) => x.name === '会议')!.rev
    send(m!.server, 'PUT', '/templates/会议', { baseRev: rev, meta: { title: '新会议' }, body: '新' })
    await click(button(tt.settings.delete))
    await settle(() => alertDialog() !== null, 'the question')
    await click([...alertDialog()!.querySelectorAll('button')].find((b) => b.textContent === tt.settings.deleteConfirm))
    await settle(() => document.body.textContent!.includes(tt.settings.deleteChanged), 'the message')
    expect(listed().templates.map((x) => x.name)).toContain('会议')
  })

  it('sets the default template of a folder from the list, takes it away, and sets the one of quick notes', async () => {
    await openSingle()
    expect(button(tt.defaults.setFolder)!.disabled).toBe(true)
    await settle(
      () => dialog().querySelector(`select[aria-label="${tt.defaults.folder}"] option[value="Work"]`) !== null,
      'the folders',
    )
    await type(dialog().querySelector<HTMLSelectElement>(`select[aria-label="${tt.defaults.folder}"]`)!, 'Work')
    await type(dialog().querySelector<HTMLSelectElement>(`select[aria-label="${tt.defaults.template}"]`)!, '会议')
    await click(button(tt.defaults.setFolder))
    await settle(() => dialog().querySelector('li[data-default="Work"]') !== null, 'the default in the list')
    expect(defaultsPuts()).toEqual([{ folder: 'Work', template: '会议' }])
    await click(button(tt.defaults.removeOf('Work')))
    await settle(() => dialog().querySelector('li[data-default="Work"]') === null, 'the default gone')
    expect(defaultsPuts().at(-1)).toEqual({ folder: 'Work', template: '' })
    await type(field('template-quick'), '日记')
    await settle(() => listed().defaults.quickNote === '日记', 'the quick note default')
    expect(defaultsPuts().at(-1)).toEqual({ quickNote: '日记' })
    await type(field('template-quick'), '')
    await settle(() => listed().defaults.quickNote === '', 'the quick note default gone')
    // Only folders can have one: the root is not offered.
    const folders = [
      ...dialog().querySelectorAll<HTMLOptionElement>(`select[aria-label="${tt.defaults.folder}"] option`),
    ].map((o) => o.value)
    expect(folders.slice(1)).not.toContain('')
    expect(folders).toContain('Work')
  })
})

describe('the templates section: what the server refuses', () => {
  it('says that a space has at most 200 templates, in words', async () => {
    await openSingle((s) => {
      for (let i = 0; i < 200; i++) send(s, 'PUT', `/templates/t${i}`, { baseRev: '', meta: {}, body: '' })
    })
    await click(button(tt.settings.newTemplate))
    await type(field('template-name'), '多一个')
    await click(button(tt.settings.create))
    await settle(() => dialog().querySelector('form [role="alert"]') !== null, 'the message')
    expect(dialog().querySelector('form [role="alert"]')!.textContent).toBe(tt.errors.template_limit)
  })

  it('says that a template is too large, in words', async () => {
    await openSingle()
    await click(button(tt.settings.newTemplate))
    await type(field('template-name'), '大')
    await type(field('template-body'), 'x'.repeat(257 * 1024))
    await click(button(tt.settings.create))
    await settle(() => dialog().querySelector('form [role="alert"]') !== null, 'the message')
    expect(dialog().querySelector('form [role="alert"]')!.textContent).toBe(tt.errors.template_too_large)
  })

  it('says that a template is not there when it is set as a default after it was deleted', async () => {
    await openSingle()
    // Someone deletes it after this page was read.
    const rev = listed().templates.find((x) => x.name === '会议')!.rev
    expect(
      m!.server.handle('DELETE', `/api/spaces/default/templates/会议?baseRev=${rev}`, undefined, json).status,
    ).toBe(204)
    await type(field('template-quick'), '会议')
    await settle(() => document.body.textContent!.includes(tt.errors.template_not_found), 'the message')
    // What the server has is read again: the box is back to what it was.
    await settle(() => field('template-quick').value === '', 'read again')
  })
})

describe('the templates section: someone who cannot manage the space', () => {
  it.each(['editor', 'viewer'] as const)(
    '%s reads the templates, and finds nothing to change them with',
    async (role) => {
      await openAs(role)
      expect(panelText()).toContain(tt.settings.helpReadOnly)
      expect(row('日记')).not.toBeNull()
      expect(button(tt.settings.newTemplate)).toBeUndefined()
      await click(row('日记')!.querySelector('button'))
      // The header and the body are text, not boxes; no save, delete or defaults to set.
      await settle(() => row('日记')!.textContent!.includes('## 今天'), 'the body')
      expect(row('日记')!.textContent).toContain('{{date}} 日记')
      expect(row('日记')!.querySelector('input, textarea, select')).toBeNull()
      expect(button(tt.settings.save)).toBeUndefined()
      expect(button(tt.settings.delete)).toBeUndefined()
      expect(button(tt.defaults.removeOf('Work'))).toBeUndefined()
      expect(button(tt.defaults.setFolder)).toBeUndefined()
      expect(field('template-quick').disabled).toBe(true)
      // The default is shown all the same.
      expect(dialog().querySelector('li[data-default="Work"]')).not.toBeNull()
      expect(m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login')).toEqual([])
    },
  )
})
