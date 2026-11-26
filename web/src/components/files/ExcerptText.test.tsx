// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { ExcerptText } from '@/components/files/ExcerptText'
import { t } from '@/i18n'
import { installDomShims, mount, type FxMounted } from '@/testing/harness'

beforeAll(installDomShims)
let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const render = async (text: string) => {
  m = await mount(<p data-ex>{<ExcerptText text={text} />}</p>, { fixtures: true })
  return m.host.querySelector('[data-ex]')!
}

describe('ExcerptText', () => {
  it('shows done and undone items as checkboxes', async () => {
    const host = await render('[x] water the plant\n[ ] reply to the landlord')
    const lines = host.querySelectorAll('[data-task]')
    expect(lines).toHaveLength(2)
    expect(lines[0].getAttribute('data-task')).toBe('done')
    expect(lines[0].textContent).toContain('water the plant')
    expect(lines[0].textContent).not.toContain('[x]')
    expect(lines[0].textContent).toContain(t.list.taskDone)
    expect(lines[0].className).toContain('line-through')
    expect(lines[1].getAttribute('data-task')).toBe('todo')
    expect(lines[1].textContent).toContain(t.list.taskTodo)
    expect(lines[1].className).not.toContain('line-through')
    for (const g of host.querySelectorAll('[data-box]')) expect(g.getAttribute('aria-hidden')).toBe('true')
  })

  it('accepts an uppercase X and leading spaces', async () => {
    const host = await render('  [X] big\n    [ ] nested')
    const lines = host.querySelectorAll('[data-task]')
    expect(lines).toHaveLength(2)
    expect(lines[0].getAttribute('data-task')).toBe('done')
  })

  it('leaves a marker in the middle of a line alone', async () => {
    const host = await render('see [x] later\nplain')
    expect(host.querySelector('[data-task]')).toBeNull()
    expect(host.textContent).toBe('see [x] later\nplain')
  })

  it('renders a page with no tasks unchanged', async () => {
    const host = await render('first line\nsecond line')
    expect(host.textContent).toBe('first line\nsecond line')
    expect(host.querySelector('[data-box]')).toBeNull()
  })
})
