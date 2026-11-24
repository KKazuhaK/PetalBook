// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { useState } from 'react'
import type { PageMeta } from '@/api/types'
import { tp } from '@/i18n/props'
import { click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import PropertyPanel from './PropertyPanel'

// The panel under a page's subtitle: one control per field of its type, reporting
// the header as the person changes it, and never dropping a value it cannot use.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const TYPE = {
  title: '任务',
  fields: [
    { key: 'name', title: '名称', kind: 'text' },
    { key: 'note', title: '备注', kind: 'longtext' },
    { key: 'status', title: '状态', kind: 'select', options: ['todo', 'doing', 'done'] },
    { key: 'labels', title: '标签组', kind: 'multiselect', options: ['a', 'b', 'c'] },
    { key: 'due', title: '截止', kind: 'date' },
    { key: 'points', title: '点数', kind: 'number' },
    { key: 'done', title: '完成', kind: 'checkbox' },
    { key: 'home', title: '主页', kind: 'url' },
  ],
}

function Harness({
  initial,
  readOnly = false,
  typeName = 'task',
}: {
  initial: PageMeta
  readOnly?: boolean
  typeName?: string
}) {
  const [values, setValues] = useState(initial)
  return (
    <>
      <PropertyPanel typeName={typeName} values={values} onChange={setValues} readOnly={readOnly} />
      <pre data-testid="values">{JSON.stringify(values)}</pre>
    </>
  )
}

async function open(initial: PageMeta, opts: { readOnly?: boolean; type?: unknown; typeName?: string } = {}) {
  m = await mount(<Harness initial={initial} readOnly={opts.readOnly} typeName={opts.typeName} />, {
    entries: (server) => {
      if (opts.type !== null)
        server.handle('PUT', '/api/spaces/default/types/task', JSON.stringify(opts.type ?? TYPE), 'application/json')
      return ['/']
    },
  })
  await settle(() => document.querySelector('[data-slot="property-panel"] [aria-busy]') === null, 'the types')
}

const values = () => JSON.parse(document.querySelector('[data-testid="values"]')!.textContent!) as PageMeta
const control = (label: string) => document.querySelector<HTMLElement>(`[aria-label="${label}"]`)!
const rowKeys = () => [...document.querySelectorAll('[data-field]')].map((e) => e.getAttribute('data-field'))

describe('the property panel', () => {
  it("has one control per field, in the type's order, of the kind the field asks for", async () => {
    await open({ home: 'https://example.com' })
    expect(rowKeys()).toEqual(['name', 'note', 'status', 'labels', 'due', 'points', 'done', 'home'])
    expect(control('名称').tagName).toBe('INPUT')
    expect(control('备注').tagName).toBe('TEXTAREA')
    expect(control('状态').tagName).toBe('SELECT')
    expect([...control('状态').querySelectorAll('option')].map((o) => o.textContent)).toEqual([
      tp.panel.none,
      'todo',
      'doing',
      'done',
    ])
    expect(control('标签组').getAttribute('role')).toBe('group')
    expect([...control('标签组').querySelectorAll('button')].map((b) => b.textContent)).toEqual(['a', 'b', 'c'])
    expect(control('截止').getAttribute('type')).toBe('date')
    expect(control('点数').getAttribute('inputmode')).toBe('decimal')
    expect(control('完成').getAttribute('type')).toBe('checkbox')
    // The address opens in a new tab.
    const link = document.querySelector<HTMLAnchorElement>(`a[aria-label="${tp.panel.openLink('主页')}"]`)!
    expect([link.href, link.target]).toEqual(['https://example.com/', '_blank'])
    expect(document.querySelector('[data-slot="property-panel"]')!.textContent).toContain('任务')
  })

  it('reports each change as the header, with the other keys as they were', async () => {
    await open({ created: '2026-01-01', owner: 'Ann' })
    await type(control('名称') as HTMLInputElement, '写报告')
    await type(control('状态') as HTMLSelectElement, 'doing')
    await click([...control('标签组').querySelectorAll('button')].find((b) => b.textContent === 'b'))
    await click([...control('标签组').querySelectorAll('button')].find((b) => b.textContent === 'c'))
    await type(control('截止') as HTMLInputElement, '2026-12-01')
    await type(control('点数') as HTMLInputElement, '2.5')
    await click(control('完成'))
    await type(control('备注') as HTMLTextAreaElement, '一行\n两行')
    expect(values()).toEqual({
      created: '2026-01-01',
      owner: 'Ann',
      name: '写报告',
      status: 'doing',
      labels: ['b', 'c'],
      due: '2026-12-01',
      points: 2.5,
      done: true,
      note: '一行\n两行',
    })
    // Taking a choice back, and emptying a box, remove the key rather than leave an empty value.
    await click([...control('标签组').querySelectorAll('button')].find((b) => b.textContent === 'b'))
    await click([...control('标签组').querySelectorAll('button')].find((b) => b.textContent === 'c'))
    await type(control('名称') as HTMLInputElement, '')
    await type(control('状态') as HTMLSelectElement, '')
    expect(values()).toEqual({
      created: '2026-01-01',
      owner: 'Ann',
      due: '2026-12-01',
      points: 2.5,
      done: true,
      note: '一行\n两行',
    })
  })

  it('a number that is not one yet is kept as typed, and only a number reaches the header', async () => {
    await open({})
    const box = control('点数') as HTMLInputElement
    await type(box, '1.')
    expect(values().points).toBe(1)
    await type(box, '1.x')
    expect(box.value).toBe('1.x')
    expect(box.getAttribute('aria-invalid')).toBe('true')
    // The last good value stays in the header.
    expect(values().points).toBe(1)
  })

  it('a value that does not fit is shown flagged and kept, and stays when something else changes', async () => {
    const odd = {
      status: 'dropped',
      points: 'many',
      due: 'tomorrow',
      labels: ['a', 'q'],
      done: 'yes',
      name: 5,
      home: 'example',
    }
    await open(odd)
    for (const label of ['状态', '点数', '截止', '标签组', '完成', '名称', '主页']) {
      expect(control(label).getAttribute('aria-invalid'), label).toBe('true')
    }
    // Nothing was dropped or tidied; the select even shows the value it cannot offer.
    expect(values()).toEqual(odd)
    expect((control('状态') as HTMLSelectElement).value).toBe('dropped')
    expect(control('点数').getAttribute('class')).toContain('destructive')
    expect(document.querySelectorAll('[data-wrong="true"]').length).toBeGreaterThanOrEqual(5)
    // The value that is not in the list is a chip of its own, and can be taken off.
    expect(document.querySelector(`button[aria-label="${tp.panel.removeValue('q')}"]`)).not.toBeNull()
    await type(control('备注') as HTMLTextAreaElement, 'x')
    expect(values()).toEqual({ ...odd, note: 'x' })
    await click(document.querySelector(`button[aria-label="${tp.panel.removeValue('q')}"]`))
    expect(values().labels).toEqual(['a'])
    // Picking a good value replaces the wrong one.
    await type(control('状态') as HTMLSelectElement, 'done')
    expect(values().status).toBe('done')
    expect(control('状态').getAttribute('aria-invalid')).toBeNull()
  })

  it('lists header keys that are not in the type in a collapsed group, each with a button that clears it', async () => {
    await open({
      id: 'X',
      title: 'T',
      tags: ['a'],
      created: '2026-01-01',
      type: 'task',
      publish: true,
      slug: 's',
      status: 'todo',
      owner: 'Ann',
      估计: 3,
    })
    const group = document.querySelector<HTMLDetailsElement>('[data-slot="not-in-type"]')!
    expect(group.open).toBe(false)
    expect(group.querySelector('summary')!.textContent).toContain(tp.panel.notInType(2))
    // Built-in keys and the two the server keeps back are not properties.
    expect([...group.querySelectorAll('li')].map((li) => li.getAttribute('data-key'))).toEqual(['owner', '估计'])
    await click(group.querySelector(`button[aria-label="${tp.panel.clearKey('owner')}"]`))
    expect(values()).toEqual({
      id: 'X',
      title: 'T',
      tags: ['a'],
      created: '2026-01-01',
      type: 'task',
      publish: true,
      slug: 's',
      status: 'todo',
      估计: 3,
    })
    expect(document.querySelector('[data-slot="not-in-type"]')!.textContent).not.toContain('owner')
    await click(document.querySelector(`button[aria-label="${tp.panel.clearKey('估计')}"]`))
    // Nothing left to list: no group.
    expect(document.querySelector('[data-slot="not-in-type"]')).toBeNull()
  })

  it('shows a field of a kind it does not know as raw YAML, edited as text, and keeps the value while the text is not YAML', async () => {
    await mountOther()
    const box = control('杂项') as HTMLTextAreaElement
    expect(box.value).toBe('a: 1\nlist:\n  - x\n  - y')
    await type(box, 'a: 2')
    expect(values().extra).toEqual({ a: 2 })
    await type(box, 'a: [1')
    expect(box.getAttribute('aria-invalid')).toBe('true')
    expect(document.body.textContent).toContain(tp.panel.notYaml)
    // The text stays as typed; the header keeps the last value that was YAML.
    expect(box.value).toBe('a: [1')
    expect(values().extra).toEqual({ a: 2 })
    await type(box, '')
    expect('extra' in values()).toBe(false)
  })

  it('read only: the values are there to read, nothing can be changed, and nothing can be cleared', async () => {
    await open({ status: 'doing', points: 3, owner: 'Ann', labels: ['a'], done: true }, { readOnly: true })
    expect((control('状态') as HTMLSelectElement).disabled).toBe(true)
    expect((control('状态') as HTMLSelectElement).value).toBe('doing')
    expect((control('点数') as HTMLInputElement).readOnly).toBe(true)
    expect((control('名称') as HTMLInputElement).readOnly).toBe(true)
    expect((control('完成') as HTMLInputElement).disabled).toBe(true)
    expect([...control('标签组').querySelectorAll('button')].every((b) => b.disabled)).toBe(true)
    expect(document.querySelector('[data-slot="not-in-type"] button')).toBeNull()
    expect(document.querySelector('[data-slot="not-in-type"]')!.textContent).toContain('Ann')
  })

  it("says so when the type is not there, and still lists the page's own keys", async () => {
    await open({ status: 'todo' }, { typeName: 'ghost' })
    expect(document.querySelector('[data-slot="property-panel"]')!.textContent).toContain(tp.panel.missingType('ghost'))
    expect(document.querySelector('[data-slot="not-in-type"]')!.textContent).toContain('status')
  })

  it('shows the error of a type whose file cannot be read instead of fields', async () => {
    await mountOther(true)
    expect(document.querySelector('[data-slot="property-panel"]')!.textContent).toContain('损坏的文件')
    expect(document.querySelectorAll('[data-field]')).toHaveLength(0)
  })
})

// A type with a field of kind `other`, or a damaged file: only the mock can make them.
async function mountOther(damaged = false) {
  m = await mount(<Harness initial={{ extra: { a: 1, list: ['x', 'y'] }, status: 'todo' }} />, {
    entries: (server) => {
      server.handle(
        'PUT',
        '/api/spaces/default/types/task',
        JSON.stringify({ title: '', fields: [{ key: 'extra', title: '杂项', kind: 'text' }] }),
        'application/json',
      )
      if (damaged) server.damageType('default', 'task', '损坏的文件')
      else server.forceKind('default', 'task', 'extra', 'other')
      return ['/']
    },
  })
  await settle(() => document.querySelector('[data-slot="property-panel"] [aria-busy]') === null, 'the types')
}
