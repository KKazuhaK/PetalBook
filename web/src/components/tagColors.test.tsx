// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { spaceApi } from '@/api/client'
import type { TagColor } from '@/api/types'
import { keys, useTagColors } from '@/api/hooks'
import { PageListView } from '@/components/PageListView'
import { FileOpsProvider } from '@/components/files/FileOps'
import { AppSidebar } from '@/components/layout/AppSidebar'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'
import { TAG_COLORS } from '@/lib/tagColors'
import { t } from '@/i18n'
import { byLabel, click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import { TagChip } from './TagChip'

// The colour menu is built only when opened, so it is opened here: by the
// right-click, and by the "…" on a tag.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const sidebar = (
  <SidebarProvider>
    <QuickCaptureProvider>
      <FileOpsProvider>
        <AppSidebar />
      </FileOpsProvider>
    </QuickCaptureProvider>
  </SidebarProvider>
)

const tagLink = (name: string) => document.querySelector<HTMLElement>(`[role="listitem"][data-tag="${name}"] a`)
const menuItems = () => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')]
const contextMenu = (el: Element) =>
  act(async () => {
    el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 30, clientY: 30 }))
  })
const putRequests = () => m!.requests.filter((r) => r.method === 'PUT' && r.url.includes('/tags/'))

function ColorsProbe() {
  const colors = useTagColors()
  return <div data-testid="colors">{[...colors].map(([k, v]) => `${k}=${v}`).join(',')}</div>
}

describe('tag chips', () => {
  const ui = (
    <Routes>
      <Route
        path="/s/:space/*"
        element={
          <>
            <TagChip tag="随笔" />
            <TagChip tag="日常" />
            <TagChip tag="随笔" color={null} />
            <TagChip tag="日常" color="teal" count={3} />
            <ColorsProbe />
          </>
        }
      />
    </Routes>
  )
  it('wear the colour the space gave the tag; a given colour wins, null forces none', async () => {
    m = await mount(ui, { entries: ['/s/default/recent'], server: { mode: 'single' } })
    await settle(() => document.querySelector('[data-testid="colors"]')?.textContent !== '', 'colours loaded')
    const chips = [...document.querySelectorAll<HTMLElement>('span[data-tag-color], span.rounded-full')].filter((e) =>
      /随笔|日常/.test(e.textContent ?? ''),
    )
    expect(chips).toHaveLength(4)
    expect(chips[0].className).toContain('tag-rose')
    // No colour of its own: the quiet outline.
    expect(chips[1].className).not.toMatch(/tag-/)
    expect(chips[2].className).not.toMatch(/tag-/)
    expect(chips[3].className).toContain('tag-teal')
    expect(chips[3].textContent).toBe('日常3')
    expect(document.querySelector('[data-testid="colors"]')!.textContent).toBe('随笔=rose,待办=amber')
  })

  it('a custom-coloured tag is tinted through its own hue; an unknown colour from the server is none', async () => {
    m = await mount(
      <Routes>
        <Route
          path="/s/:space/*"
          element={
            <>
              <TagChip tag="定制" color="#A1B2C3" />
              <TagChip tag="乱码" color={'banana' as TagColor} />
            </>
          }
        />
      </Routes>,
      { entries: ['/s/default/recent'], server: { mode: 'single' } },
    )
    const chip = (name: string) =>
      [...document.querySelectorAll<HTMLElement>('span.rounded-full')].find((e) => e.textContent === name)!
    expect(chip('定制').className).toContain('tag-custom')
    expect(chip('定制').style.getPropertyValue('--tag-hue')).toBe('#a1b2c3')
    expect(chip('乱码').className).not.toMatch(/tag-/)
    expect(chip('乱码').style.getPropertyValue('--tag-hue')).toBe('')
  })

  it("the listing shows each page's tags in their colours", async () => {
    m = await mount(
      <Routes>
        <Route
          path="/s/:space/f/*"
          element={
            <SidebarProvider>
              <QuickCaptureProvider>
                <FileOpsProvider>
                  <PageListView source={{ kind: 'folder', folder: '随笔' }} />
                </FileOpsProvider>
              </QuickCaptureProvider>
            </SidebarProvider>
          }
        />
      </Routes>,
      { entries: ['/s/default/f/随笔'], server: { mode: 'single' } },
    )
    await settle(() => document.querySelector('[class*="tag-rose"]') !== null, 'coloured tag in the listing')
  })
})

describe('the sidebar tags', () => {
  it('draw each tag in its colour', async () => {
    m = await mount(sidebar, { entries: ['/s/default/recent'], server: { mode: 'single' } })
    await settle(() => tagLink('随笔') !== null && tagLink('随笔')!.className.includes('tag-rose'), 'rose tag')
    expect(tagLink('待办')!.className).toContain('tag-amber')
    expect(tagLink('日常')!.className).not.toMatch(/tag-/)
  })

  it('a right-click opens the colour menu: the ten colours, "custom" and "no colour"', async () => {
    m = await mount(sidebar, { entries: ['/s/default/recent'], server: { mode: 'single' } })
    await settle(() => tagLink('日常') !== null, 'tags')
    await contextMenu(tagLink('日常')!)
    await settle(() => menuItems().length > 0, 'menu')
    const items = menuItems()
    expect(items).toHaveLength(12)
    expect(items.slice(0, 10).map((i) => i.getAttribute('aria-label'))).toEqual(
      TAG_COLORS.map((c) => t.tagColor.names[c]),
    )
    expect(items[10].textContent).toBe(t.tagColor.custom)
    expect(items[11].textContent).toBe(t.tagColor.none)
    // It names the tag, and "no colour" has nothing to do for a tag without one.
    expect(document.querySelector('[role="group"]')!.getAttribute('aria-label')).toBe(t.tagColor.menu('日常'))
    expect(items[11].getAttribute('aria-disabled') ?? items[11].getAttribute('data-disabled')).not.toBeNull()
  })

  it('choosing a colour sets it on the server and repaints the tag; "no colour" takes it off', async () => {
    m = await mount(sidebar, { entries: ['/s/default/recent'], server: { mode: 'single' } })
    await settle(() => tagLink('日常') !== null, 'tags')
    await contextMenu(tagLink('日常')!)
    await settle(() => menuItems().length > 0, 'menu')
    await click(byLabel(t.tagColor.names.blue))
    await settle(() => putRequests().length === 1, 'PUT sent')
    expect(putRequests()[0].url).toBe(`/api/spaces/default/tags/${encodeURIComponent('日常')}`)
    expect(JSON.parse(putRequests()[0].body!)).toEqual({ color: 'blue' })
    await settle(() => tagLink('日常')!.className.includes('tag-blue'), 'repainted')
    // The menu is closed; open it again: the current colour is marked, and it can be removed.
    await settle(() => menuItems().length === 0, 'menu closed')
    await contextMenu(tagLink('日常')!)
    await settle(() => menuItems().length > 0, 'menu again')
    expect(byLabel(t.tagColor.names.blue)!.getAttribute('aria-current')).toBe('true')
    await click(menuItems().find((i) => i.textContent === t.tagColor.none))
    await settle(() => putRequests().length === 2, 'second PUT')
    expect(JSON.parse(putRequests()[1].body!)).toEqual({ color: null })
    await settle(() => !tagLink('日常')!.className.includes('tag-'), 'plain again')
  })

  it('Custom… opens a hex dialog; Apply sends the lower-case colour and the tag is tinted and marked', async () => {
    m = await mount(sidebar, { entries: ['/s/default/recent'], server: { mode: 'single' } })
    await settle(() => tagLink('日常') !== null, 'tags')
    await contextMenu(tagLink('日常')!)
    await settle(() => menuItems().length > 0, 'menu')
    await click(menuItems().find((i) => i.textContent === t.tagColor.custom))
    await settle(() => byLabel(t.tagColor.hex) !== null, 'hex field')
    const apply = () =>
      [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent === t.tagColor.apply)!
    // A bad value cannot be applied; the `#` is optional.
    await type(byLabel(t.tagColor.hex) as HTMLInputElement, 'xyz')
    expect(apply().disabled).toBe(true)
    await type(byLabel(t.tagColor.hex) as HTMLInputElement, 'A1B2C3')
    expect(apply().disabled).toBe(false)
    expect((byLabel(t.tagColor.picker) as HTMLInputElement).value).toBe('#a1b2c3')
    await click(apply())
    await settle(() => putRequests().length === 1, 'PUT sent')
    expect(JSON.parse(putRequests()[0].body!)).toEqual({ color: '#a1b2c3' })
    await settle(() => tagLink('日常')!.className.includes('tag-custom'), 'repainted')
    expect(tagLink('日常')!.style.getPropertyValue('--tag-hue')).toBe('#a1b2c3')
    await settle(() => byLabel(t.tagColor.hex) === null, 'dialog closed')
    await contextMenu(tagLink('日常')!)
    await settle(() => menuItems().length > 0, 'menu again')
    const custom = menuItems().find((i) => i.textContent?.includes(t.tagColor.custom))!
    expect(custom.getAttribute('aria-current')).toBe('true')
    expect(custom.textContent).toContain('#a1b2c3')
    expect(byLabel(t.tagColor.names.blue)!.getAttribute('aria-current')).toBeNull()
  })

  it('the "…" on a tag opens the same menu', async () => {
    m = await mount(sidebar, { entries: ['/s/default/recent'], server: { mode: 'single' } })
    await settle(() => tagLink('日常') !== null, 'tags')
    await click(byLabel(t.tagColor.more('日常')))
    await settle(() => menuItems().length === 12, 'menu')
  })

  it('a failed change says so and leaves the colour', async () => {
    m = await mount(sidebar, { entries: ['/s/default/recent'], server: { mode: 'single' } })
    await settle(() => tagLink('日常') !== null, 'tags')
    await contextMenu(tagLink('日常')!)
    await settle(() => menuItems().length > 0, 'menu')
    const real = globalThis.fetch
    globalThis.fetch = async (input, init) =>
      init?.method === 'PUT'
        ? new Response(JSON.stringify({ error: 'invalid', message: '不行' }), {
            status: 400,
            headers: { 'Content-Type': 'application/json' },
          })
        : real(input, init)
    await click(byLabel(t.tagColor.names.red))
    await settle(() => document.body.textContent!.includes(t.tagColor.failed), 'toast')
    expect(tagLink('日常')!.className).not.toMatch(/tag-/)
    globalThis.fetch = real
  })

  it('with many tags there is a filter box, and the list scrolls by itself', async () => {
    m = await mount(sidebar, { entries: ['/s/default/recent'], server: { mode: 'single' } })
    await settle(() => tagLink('随笔') !== null, 'tags')
    // A few tags: no filter box.
    expect(document.querySelector(`input[aria-label="${t.nav.filterTags}"]`)).toBeNull()
    for (let i = 0; i < 25; i++)
      await spaceApi('default').createPage({ meta: { title: `多${i}`, tags: [`批量${String(i).padStart(2, '0')}`] } })
    await act(async () => {
      await m!.qc.invalidateQueries({ queryKey: keys.tags('default') })
    })
    await settle(() => document.querySelector(`input[aria-label="${t.nav.filterTags}"]`) !== null, 'filter box')
    const list = document.querySelector('[role="list"]')!
    // Its own scroll, with a ceiling.
    expect(list.parentElement!.className).toMatch(/max-h-/)
    expect(list.parentElement!.className).toMatch(/overflow-y-auto/)
    expect(list.querySelectorAll('[role="listitem"]').length).toBeGreaterThan(25)
    await type(document.querySelector<HTMLInputElement>(`input[aria-label="${t.nav.filterTags}"]`)!, '批量0')
    await settle(() => list.querySelectorAll('[role="listitem"]').length === 10, 'filtered to 批量00..09')
    await type(document.querySelector<HTMLInputElement>(`input[aria-label="${t.nav.filterTags}"]`)!, '没有这个标签')
    await settle(() => list.textContent!.includes(t.nav.noTagMatch), 'nothing matches')
  })
})
