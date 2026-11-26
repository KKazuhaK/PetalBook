// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureServer } from '@/api/fixtures/server'
import { tp } from '@/i18n/props'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { shareSpace } from '@/api/fixtures/data/scenes'
import SettingsRoute from './SettingsRoute'

// The types section of a space's settings, against the fixture server.

// Counts the times the section's code is loaded (the factory runs on the first import).
const chunk = vi.hoisted(() => ({ loaded: 0 }))
vi.mock('./TypesPanel', async (importOriginal) => {
  chunk.loaded++
  return importOriginal()
})

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
  </Routes>
)
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')!
const panelText = () => dialog().textContent ?? ''
const button = (label: string) =>
  [...dialog().querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) as HTMLButtonElement | undefined
const typeRow = (name: string) => dialog().querySelector<HTMLElement>(`li[data-type="${name}"]`)
const rowsOf = () => [...dialog().querySelectorAll<HTMLElement>('li[data-field]')]
const fieldKeys = () => rowsOf().map((r) => r.querySelector<HTMLInputElement>('input')!.value)
const puts = () => m!.requests.filter((r) => r.method === 'PUT' && /\/types\//.test(r.url))
const lastPut = () => JSON.parse(puts().at(-1)!.body!) as { title: string; fields: Record<string, unknown>[] }
const send = (server: FixtureServer, method: string, path: string, body: unknown) => server.on()(method, path, body)

const TASK = {
  title: '任务',
  fields: [
    { key: 'status', title: '状态', kind: 'select', options: ['todo', 'doing', 'done'] },
    { key: 'points', kind: 'number' },
    { key: 'due', title: '截止', kind: 'date' },
  ],
}

async function openSingle(seed: (server: FixtureServer) => void = (s) => void send(s, 'PUT', '/types/task', TASK)) {
  m = await mount(routes, { entries: (server) => (seed(server), ['/settings/types']), fixtures: { mode: 'single' } })
  await settle(() => dialog() !== null && panelText().includes(tp.types.title), 'the section')
}

async function openAs(role: 'editor' | 'viewer') {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts' },
    entries: (server) => {
      const { space, owner } = shareSpace(server, role)
      owner.request('PUT', `/api/spaces/${space}/types/task`, TASK)
      owner.request('POST', `/api/spaces/${space}/folders`, { path: 'Work' })
      owner.request('PUT', `/api/spaces/${space}/folders/type`, { folder: 'Work', type: 'task' })
      localStorage.setItem('pb.space', space)
      return ['/settings/types']
    },
  })
  await settle(() => dialog() !== null && panelText().includes(tp.types.title), 'the section')
}

describe('the types section: someone who manages the space', () => {
  it('is its own chunk: another section does not load it, this one does', async () => {
    m = await mount(routes, { entries: ['/settings/general'], fixtures: { mode: 'single' } })
    await settle(() => dialog() !== null && panelText().length > 0, 'the dialog')
    expect(chunk.loaded).toBe(0)
    await m.unmount()
    m = undefined
    await openSingle()
    expect(chunk.loaded).toBe(1)
  })

  it('lists the types with their names and the number of fields, and opens one to edit', async () => {
    await openSingle()
    expect(typeRow('task')!.textContent).toContain('任务')
    expect(typeRow('task')!.textContent).toContain('task')
    expect(typeRow('task')!.textContent).toContain(tp.types.fieldCount(3))
    await click(typeRow('task')!.querySelector('button'))
    expect(fieldKeys()).toEqual(['status', 'points', 'due'])
    // The name and the keys of the fields there are cannot be changed.
    expect((dialog().querySelector('#type-name') as HTMLInputElement).readOnly).toBe(true)
    expect(rowsOf().every((r) => r.querySelector<HTMLInputElement>('input')!.readOnly)).toBe(true)
    expect(rowsOf()[0].querySelector<HTMLTextAreaElement>('textarea')!.value).toBe('todo\ndoing\ndone')
    expect(rowsOf()[0].querySelector<HTMLSelectElement>('select')!.value).toBe('select')
  })

  it('makes a type: the name is explained as fixed, the fields have a kind and options, and what is sent is the whole type', async () => {
    await openSingle((s) => void send(s, 'PUT', '/types/task', TASK))
    await click(button(tp.types.newType))
    expect(panelText()).toContain(tp.types.nameHelp)
    const create = () => button(tp.types.create)!
    // Nothing to make yet: no name.
    expect(create().disabled).toBe(true)
    await type(dialog().querySelector<HTMLInputElement>('#type-name')!, 'Task')
    // The same name as another in another case is refused before anything is sent.
    expect(panelText()).toContain(tp.types.nameExists)
    expect(create().disabled).toBe(true)
    await type(dialog().querySelector<HTMLInputElement>('#type-name')!, '文案')
    await type(dialog().querySelector<HTMLInputElement>('#type-title')!, '  写作  ')
    await click(button(tp.types.addField))
    await click(button(tp.types.addField))
    const [a, b] = rowsOf()
    await type(a.querySelectorAll('input')[0], '阶段')
    await type(a.querySelectorAll('input')[1], '写作阶段')
    await type(a.querySelector('select')!, 'select')
    // A select with no options is not saved.
    expect(create().disabled).toBe(true)
    expect(panelText()).toContain(tp.types.noOptions)
    await type(rowsOf()[0].querySelector('textarea')!, '草稿\n 定稿 \n')
    await type(b.querySelectorAll('input')[0], '字数')
    await type(b.querySelector('select')!, 'number')
    expect(create().disabled).toBe(false)
    await click(create())
    await settle(() => puts().length === 1, 'the request')
    expect(puts()[0].url).toContain('/types/%E6%96%87%E6%A1%88')
    expect(lastPut()).toEqual({
      baseRev: '',
      title: '写作',
      fields: [
        { key: '阶段', title: '写作阶段', kind: 'select', options: ['草稿', '定稿'] },
        { key: '字数', title: '', kind: 'number' },
      ],
    })
    await settle(() => typeRow('文案') !== null, 'the type in the list')
    expect(typeRow('文案')!.textContent).toContain('写作')
  })

  it('refuses keys the server would refuse, and keys used twice, before sending', async () => {
    await openSingle()
    await click(button(tp.types.newType))
    await type(dialog().querySelector<HTMLInputElement>('#type-name')!, 'plain')
    await click(button(tp.types.addField))
    await type(rowsOf()[0].querySelectorAll('input')[0], 'tags')
    expect(panelText()).toContain(tp.types.keyBuiltin)
    expect(button(tp.types.create)!.disabled).toBe(true)
    await type(rowsOf()[0].querySelectorAll('input')[0], 'a:b')
    expect(panelText()).toContain(tp.types.keyChars)
    await type(rowsOf()[0].querySelectorAll('input')[0], 'k')
    await click(button(tp.types.addField))
    await type(rowsOf()[1].querySelectorAll('input')[0], 'k')
    expect(panelText()).toContain(tp.types.keyTwice)
    await type(rowsOf()[1].querySelectorAll('input')[0], 'k2')
    expect(button(tp.types.create)!.disabled).toBe(false)
    expect(puts()).toHaveLength(0)
  })

  it("changes a type: the title, a field's title, kind and options, a field added, one removed, and the order", async () => {
    await openSingle()
    await click(typeRow('task')!.querySelector('button'))
    expect(button(tp.types.save)!.disabled).toBe(true)
    await type(dialog().querySelector<HTMLInputElement>('#type-title')!, '任务单')
    await type(rowsOf()[1].querySelectorAll('input')[1], '点数')
    await type(rowsOf()[0].querySelector('textarea')!, 'todo\ndoing\ndone\ndropped')
    await type(rowsOf()[2].querySelector('select')!, 'text')
    // Move "due" to the top, then drop "points".
    await click(rowsOf()[2].querySelector(`button[aria-label="${tp.types.moveUp('截止')}"]`))
    await click(rowsOf()[1].querySelector(`button[aria-label="${tp.types.moveUp('截止')}"]`))
    expect(fieldKeys()).toEqual(['due', 'status', 'points'])
    await click(rowsOf()[2].querySelector(`button[aria-label="${tp.types.removeField('点数')}"]`))
    await click(button(tp.types.addField))
    await type(rowsOf()[2].querySelectorAll('input')[0], 'owner')
    expect(fieldKeys()).toEqual(['due', 'status', 'owner'])
    const rev = (m!.fx.handle('GET', '/api/spaces/default/types', undefined, null).body as { types: { rev: string }[] })
      .types[0].rev
    await click(button(tp.types.save))
    await settle(() => puts().length === 1, 'the request')
    expect(lastPut()).toEqual({
      baseRev: rev,
      title: '任务单',
      fields: [
        { key: 'due', title: '截止', kind: 'text' },
        { key: 'status', title: '状态', kind: 'select', options: ['todo', 'doing', 'done', 'dropped'] },
        { key: 'owner', title: '', kind: 'text' },
      ],
    })
    // What the server now has is what the list shows.
    await settle(() => typeRow('task')!.textContent!.includes('任务单'), 'the new title')
    const listed = (
      m!.fx.handle('GET', '/api/spaces/default/types', undefined, null).body as {
        types: { fields: { key: string }[] }[]
      }
    ).types[0]
    expect(listed.fields.map((f) => f.key)).toEqual(['due', 'status', 'owner'])
  })

  it('deletes a type only after a question that says what happens, and the bindings go with it', async () => {
    await openSingle((s) => {
      send(s, 'PUT', '/types/task', TASK)
      send(s, 'POST', '/folders', { path: 'Work' })
      send(s, 'PUT', '/folders/type', { folder: 'Work', type: 'task' })
    })
    // The bindings are listed.
    expect(dialog().querySelector('li[data-binding="Work"]')!.textContent).toContain('任务')
    await click(typeRow('task')!.querySelector('button'))
    await click(button(tp.types.delete))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(tp.types.deleteHelp(1))
    expect(m!.requests.some((r) => r.method === 'DELETE')).toBe(false)
    await click([...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === tp.cancel))
    await settle(() => document.querySelector('[role="alertdialog"]') === null, 'the question closed')
    expect(m!.requests.some((r) => r.method === 'DELETE')).toBe(false)
    await click(button(tp.types.delete))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find(
        (b) => b.textContent === tp.types.deleteConfirm,
      ),
    )
    await settle(() => typeRow('task') === null, 'the type gone')
    expect(m!.requests.some((r) => r.method === 'DELETE' && /\/types\/task\?baseRev=[0-9a-f]{40}$/.test(r.url))).toBe(
      true,
    )
    await settle(() => dialog().querySelector('li[data-binding]') === null, 'the binding gone')
    expect(panelText()).toContain(tp.types.none)
  })

  it('does not save over a change someone else made meanwhile: it says so, and the type is read again', async () => {
    await openSingle()
    await click(typeRow('task')!.querySelector('button'))
    await type(dialog().querySelector<HTMLInputElement>('#type-title')!, '我的标题')
    // Someone else changes the type after the form was opened.
    const rev = (m!.fx.call('GET', '/api/spaces/default/types').body as { types: { rev: string }[] }).types[0].rev
    const changed = send(m!.fx, 'PUT', '/types/task', {
      baseRev: rev,
      title: '别人的标题',
      fields: TASK.fields.map((f) => ({ key: f.key })),
    })
    expect(changed.status).toBe(200)
    // The server refuses a save made from the old rev, and sends the type as it is now.
    m!.fx.fail('PUT', '/api/spaces/default/types/task', 409, { code: 'conflict', current: changed.body }, { times: 1 })
    await click(button(tp.types.save))
    await settle(() => document.body.textContent!.includes(tp.types.changedMeanwhile), 'the message')
    expect(puts().filter((r) => r.url.includes('/types/task')).length).toBeGreaterThan(0)
    // The form is made again from the type as it is now, not from what was typed.
    await settle(() => typeRow('task')!.textContent!.includes('别人的标题'), 'the type read again')
    expect(m!.fx.call('GET', '/api/spaces/default/types').body).toMatchObject({ types: [{ title: '别人的标题' }] })
  })

  it('takes a binding away from the list of bindings', async () => {
    await openSingle((s) => {
      send(s, 'PUT', '/types/task', TASK)
      send(s, 'POST', '/folders', { path: 'Work' })
      send(s, 'PUT', '/folders/type', { folder: 'Work', type: 'task' })
    })
    await click(button(tp.bindings.unbindOf('Work')))
    await settle(() => dialog().querySelector('li[data-binding]') === null, 'the binding gone')
    const sent = m!.requests.find((r) => r.method === 'PUT' && r.url.endsWith('/folders/type'))!
    expect(JSON.parse(sent.body!)).toEqual({ folder: 'Work', type: '' })
    // The type itself is still there.
    expect(typeRow('task')).not.toBeNull()
  })

  it('shows the error of a type whose file cannot be read, instead of an editor, and lets it be deleted', async () => {
    await openSingle((s) => {
      send(s, 'PUT', '/types/task', TASK)
      s.data.spaces[0].types.push({
        name: 'broken',
        title: '',
        rev: 'unreadable',
        fields: [],
        error: 'broken.json is not JSON; it has to be fixed by hand',
      })
    })
    expect(typeRow('broken')!.textContent).toContain(tp.types.broken)
    await click(typeRow('broken')!.querySelector('button'))
    expect(typeRow('broken')!.querySelector('[role="alert"]')!.textContent).toContain('fixed by hand')
    expect(typeRow('broken')!.querySelector('input')).toBeNull()
    expect(typeRow('broken')!.querySelector('li[data-field]')).toBeNull()
    expect(button(tp.types.delete)).toBeDefined()
    // The others are untouched by it.
    await click(typeRow('task')!.querySelector('button'))
    expect(fieldKeys()).toEqual(['status', 'points', 'due'])
  })

  it('says what the server said when it refuses a type', async () => {
    await openSingle()
    await click(typeRow('task')!.querySelector('button'))
    // A damaged file appears after the page was read: the server refuses the write, in its own words.
    m!.fx.fail('PUT', '/api/spaces/default/types/task', 400, {
      code: 'invalid',
      message: '这个类型的文件坏了，要手动修好才能改',
    })
    await type(dialog().querySelector<HTMLInputElement>('#type-title')!, '改')
    await click(button(tp.types.save))
    await settle(() => dialog().querySelector('form [role="alert"]') !== null, 'the message')
    expect(dialog().querySelector('form [role="alert"]')!.textContent).toContain('手动修好')
  })
})

describe('the types section: someone who cannot manage the space', () => {
  it.each(['editor', 'viewer'] as const)('%s reads the types, and finds nothing to change them with', async (role) => {
    await openAs(role)
    expect(panelText()).toContain(tp.types.helpReadOnly)
    expect(typeRow('task')).not.toBeNull()
    expect(button(tp.types.newType)).toBeUndefined()
    await click(typeRow('task')!.querySelector('button'))
    // The fields are text, not boxes; no save, delete or unbinding.
    expect(typeRow('task')!.textContent).toContain('状态')
    expect(typeRow('task')!.textContent).toContain('todo, doing, done')
    expect(typeRow('task')!.querySelector('input, textarea, select')).toBeNull()
    expect(button(tp.types.save)).toBeUndefined()
    expect(button(tp.types.delete)).toBeUndefined()
    expect(button(tp.bindings.unbindOf('Work'))).toBeUndefined()
    // The binding is shown all the same.
    expect(dialog().querySelector('li[data-binding="Work"]')).not.toBeNull()
    expect(m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login')).toEqual([])
  })
})
