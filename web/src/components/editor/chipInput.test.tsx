// @vitest-environment jsdom
import { act, useState } from 'react'
import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { ChipInput } from './ChipInput'
import { SourcesEditor } from './SourceEditor'
import { click, installDomShims, mount, press, type, type FxMounted } from '@/testing/harness'

// The one input of the tags and the sources: chips, and suggestions under it, the used ones before the presets.

beforeAll(installDomShims)
let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

function Probe({ start = [] as string[] }) {
  const [values, setValues] = useState(start)
  return (
    <>
      <output data-testid="values">{JSON.stringify(values)}</output>
      <SourcesEditor
        sources={values}
        onChange={setValues}
        used={[
          { source: '书', pages: 5 },
          { source: '书评', pages: 1 },
        ]}
        presets={['原创', '书', '电影', '音乐']}
      />
    </>
  )
}
const input = () => document.querySelector<HTMLInputElement>('input[role="combobox"]')!
const values = () => JSON.parse(document.querySelector('[data-testid="values"]')!.textContent!) as string[]
const options = () => [...document.querySelectorAll('[role="option"]')].map((o) => o.textContent)
const focus = async () => {
  const el = input()
  await act(async () => el.dispatchEvent(new FocusEvent('focusin', { bubbles: true })))
}

describe('the chip input', () => {
  it('names the suggestion the arrows are on to assistive technology', async () => {
    m = await mount(<Probe />, { fixtures: true })
    await focus()
    expect(input().getAttribute('aria-activedescendant')).toBeNull()
    await press(input(), 'ArrowDown')
    await press(input(), 'ArrowDown')
    const id = input().getAttribute('aria-activedescendant')
    expect(id).toBeTruthy()
    const option = document.getElementById(id!)!
    expect(option.getAttribute('role')).toBe('option')
    expect(option.getAttribute('aria-selected')).toBe('true')
    expect(option.textContent).toBe('书评1')
    // Every option has an id of its own.
    const ids = [...document.querySelectorAll('[role="option"]')].map((o) => o.id)
    expect(new Set(ids).size).toBe(ids.length)
    expect(ids.every(Boolean)).toBe(true)
  })

  it('has a remove button of the same look whose touch target is at least 40px', async () => {
    m = await mount(<Probe start={['书']} />, { fixtures: true })
    const remove = document.querySelector<HTMLButtonElement>('[role="group"] button')!
    // 16px drawn; on a touch screen a 12px margin all round makes it 40px to hit.
    expect(remove.className).toMatch(/\bsize-4\b/)
    expect(remove.className).toMatch(/\brelative\b/)
    expect(remove.className).toMatch(/pointer-coarse:after:-inset-3\b/)
    expect(remove.className).toMatch(/\bafter:absolute\b/)
  })

  it('lists the used values first, then the presets, and filters as it is typed', async () => {
    m = await mount(<Probe />, { fixtures: true })
    await focus()
    expect(options()).toEqual(['书5', '书评1', '原创', '电影', '音乐'])
    await type(input(), '书')
    expect(options()).toEqual(['书5', '书评1'])
    await type(input(), '电')
    expect(options()).toEqual(['电影'])
  })

  it('Tab completes the first suggestion and Enter adds it as a chip', async () => {
    m = await mount(<Probe />, { fixtures: true })
    await focus()
    await type(input(), '音')
    await press(input(), 'Tab')
    expect(input().value).toBe('音乐')
    await press(input(), 'Enter')
    expect(values()).toEqual(['音乐'])
    expect(input().value).toBe('')
  })

  it('the arrow keys pick, Enter adds, and a value that is not suggested can be typed', async () => {
    m = await mount(<Probe />, { fixtures: true })
    await focus()
    await press(input(), 'ArrowDown')
    await press(input(), 'ArrowDown')
    await press(input(), 'Enter')
    expect(values()).toEqual(['书评'])
    await type(input(), '我自己的来源')
    await press(input(), 'Enter')
    expect(values()).toEqual(['书评', '我自己的来源'])
  })

  it('the mouse picks too, and a value already there is not offered again', async () => {
    m = await mount(<Probe start={['书']} />, { fixtures: true })
    await focus()
    expect(options()).not.toContain('书5')
    const pick = [...document.querySelectorAll('[role="option"]')].find((o) => o.textContent === '电影')!
    await act(async () => pick.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true })))
    expect(values()).toEqual(['书', '电影'])
  })

  it('Backspace in the empty box takes off the last chip, and Escape closes the list', async () => {
    m = await mount(<Probe start={['书', '电影']} />, { fixtures: true })
    await focus()
    expect(document.querySelector('[role="listbox"]')).not.toBeNull()
    await press(input(), 'Escape')
    expect(document.querySelector('[role="listbox"]')).toBeNull()
    await press(input(), 'Backspace')
    expect(values()).toEqual(['书'])
    await type(input(), 'x')
    await press(input(), 'Backspace')
    expect(values()).toEqual(['书']) // the box was not empty
  })

  it('a comma ends a chip, a chip has a button that removes it, and the list is as wide as the row on a phone', async () => {
    m = await mount(<Probe />, { fixtures: true })
    await focus()
    await type(input(), '对话,')
    expect(values()).toEqual(['对话'])
    await click(document.querySelector('button[aria-label]'))
    expect(values()).toEqual([])
    const list = document.querySelector('[role="listbox"]')!
    expect(list.className).toMatch(/w-full/)
    expect(list.className).toMatch(/sm:w-64/)
  })

  it('shows chips only, and no input, when it cannot be changed', async () => {
    m = await mount(
      <ChipInput
        values={['书']}
        onChange={() => {}}
        used={[]}
        icon={null}
        placeholder="p"
        label="l"
        removeLabel={(v) => v}
        readOnly
      />,
      { fixtures: true },
    )
    expect(document.querySelector('input')).toBeNull()
    expect(document.body.textContent).toContain('书')
  })
})
