// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { act } from 'react'
import { SidebarProvider } from '@/components/ui/sidebar'
import { AppShell } from '@/components/layout/AppShell'
import { adminRequests, click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import { PageIcon } from './PageIcon'
import IconPickerBody from './IconPickerBody'
import { ALL_NAMES } from '@/lib/icons/lucideAll'
import { ICON_COLORS } from '@/lib/icons/colors'
import { readRecent } from '@/lib/icons/recent'
import { t } from '@/i18n'
import { CORE_ICONS } from '@/lib/icons/coreIcons'
// Node's own module: the web app has no Node typings, and the test runs in Node.
// @ts-expect-error TS2591
import { readdirSync, readFileSync, statSync } from 'node:fs'

// Icons and colours: the full Lucide set is a chunk of its own, the picker searches its names, tags and Chinese
// aliases, eight colours have a light and a dark shade, and a folder's choice is kept and drawn.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())
let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const index = readFileSync('src/index.css', 'utf8')

describe('the icon of a name', () => {
  it('draws a core icon at once, an emoji as text, and an uncommon icon once the full set has loaded', async () => {
    m = await mount(
      <>
        <PageIcon icon="book-open" color="teal" />
        <PageIcon icon="🌸" />
        <PageIcon icon="atom" />
        <PageIcon icon="drum" color="red" />
      </>,
    )
    expect(document.querySelector('[data-icon="book-open"]')).not.toBeNull()
    expect((document.querySelector('[data-icon="book-open"]') as SVGElement).style.color).toBe('var(--icon-teal)')
    expect(document.querySelector('[data-icon="🌸"]')!.textContent).toBe('🌸')
    // "drum" is not among the core icons: it appears when the whole set is there.
    expect(CORE_ICONS['drum']).toBeUndefined()
    await settle(() => document.querySelector('svg[data-icon="drum"]') !== null, 'the uncommon icon')
  })

  it('has a light and a dark shade for each of the eight colours', () => {
    expect(ICON_COLORS).toHaveLength(8)
    for (const c of ICON_COLORS) {
      const light = new RegExp(`:root \\{[^}]*--icon-${c}: #[0-9a-f]{6}`)
      const dark = new RegExp(`\\.dark \\{[^}]*--icon-${c}: #[0-9a-f]{6}`)
      expect(index).toMatch(light)
      expect(index).toMatch(dark)
    }
  })
})

describe('a name that is a key of every object', () => {
  // `constructor` looks like a Lucide name; read from a plain object it would be Object's own constructor, which React
  // cannot draw. Such a name, from a file written by hand, is no icon.
  const names = ['constructor', 'valueof', 'tostring', '__proto__', 'toString', 'hasOwnProperty', 'isprototypeof']

  it('is drawn as no icon, and the fallback is drawn instead', async () => {
    m = await mount(
      <>
        {names.map((n) => (
          <PageIcon key={n} icon={n} fallback={<span data-fallback={n} />} />
        ))}
        <PageIcon icon="book-open" color="constructor" />
        <PageIcon icon="drum" />
      </>,
    )
    await settle(() => document.querySelector('svg[data-icon="drum"]') !== null, 'the whole set')
    expect(document.querySelector('svg[data-icon="constructor"]')).toBeNull()
    expect(document.querySelector('[data-fallback="constructor"]')).not.toBeNull()
    expect(document.querySelector('[data-fallback="valueof"]')).not.toBeNull()
    expect((document.querySelector('[data-icon="book-open"]') as SVGElement).style.color).toBe('')
  })

  it('in the recent icons does not break the picker', async () => {
    localStorage.setItem('pb.icons.recent', JSON.stringify(['constructor', 'valueof', 'book-open']))
    m = await mount(<IconPickerBody icon="" color="" onChange={() => {}} />)
    const recent = document.querySelector('[data-slot="icon-picker"] section [role="listbox"]')!
    const titles = [...recent.querySelectorAll('[role="option"]')].map((o) => o.getAttribute('title'))
    expect(titles).toEqual(['book-open'])
  })

  it('is refused by the server, whose list of names is the one the app ships', () => {
    const server = readFileSync('../internal/icons/lucide.txt', 'utf8').trim().split('\n')
    expect(server, 'run `node scripts/lucide-names.mjs > ../internal/icons/lucide.txt`').toEqual(ALL_NAMES)
    expect(server).not.toContain('constructor')
  })
})

describe('the chunk of the whole set', () => {
  it('is imported statically only by the picker (itself a lazy chunk) and dynamically by the hook', async () => {
    const files: string[] = []
    const walk = (d: string) => {
      for (const f of readdirSync(d)) {
        const p = `${d}/${f}`
        if (statSync(p).isDirectory()) walk(p)
        else if (/\.(ts|tsx)$/.test(f) && !/\.test\./.test(f)) files.push(p)
      }
    }
    walk('src')
    const users = files.filter((f) => /from '[^']*lucideAll'|from '\.\/lucideAll'/.test(readFileSync(f, 'utf8')))
    expect(users).toEqual(['src/components/icons/IconPickerBody.tsx'])
    expect(readFileSync('src/components/icons/IconPicker.tsx', 'utf8')).toContain(
      "lazy(() => import('./IconPickerBody'))",
    )
    expect(readFileSync('src/lib/icons/useIcons.ts', 'utf8')).toContain("import('./lucideAll')")
  })
})

describe('the picker', () => {
  const routes = (
    <SidebarProvider>
      <Routes>
        <Route path="/*" element={<AppShell />} />
      </Routes>
    </SidebarProvider>
  )
  const openPicker = async () => {
    m = await mount(routes, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const ask = adminRequests(server)
        const spaces = (ask('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces
        ask('POST', `/api/spaces/${spaces[0].id}/folders`, { path: 'Tang' })
        return [`/s/${spaces[0].id}/f/Tang`]
      },
    })
    await settle(() => document.querySelector('[data-file-key="f:Tang"]') !== null, 'the folder in the tree')
  }
  const menuFor = async () => {
    const row = document.querySelector('[data-file-key="f:Tang"]')!
    await act(async () => {
      row.dispatchEvent(
        new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 10, clientY: 10, button: 2 }),
      )
    })
    await settle(
      () => [...document.querySelectorAll('[role="menuitem"]')].some((i) => i.textContent === t.icons.menu),
      'the menu',
    )
    await click([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent === t.icons.menu))
    await settle(() => document.querySelector('[data-slot="icon-picker"]') !== null, 'the picker')
  }
  const search = (q: string) => type(document.querySelector<HTMLInputElement>('input[type="search"]')!, q)
  const names = () =>
    [...document.querySelectorAll('[data-slot="icon-picker"] [role="option"]')].map((o) => o.getAttribute('title'))

  it('searches the names, the tags and the Chinese aliases', async () => {
    await openPicker()
    await menuFor()
    await search('book open')
    expect(names()).toContain('book-open')
    // A Lucide tag: "feather" is found by its tag "writing"/"quill" rather than by its name alone.
    await search('栀子')
    expect(names()).toContain('flower')
    await search('日历')
    expect(names()).toContain('calendar')
    await search('qqqqzzzz')
    expect(document.querySelector('[data-slot="icon-picker"]')!.textContent).toContain(t.icons.noResults)
  })

  it('keeps what is chosen in space.yml, draws it in the tree, and lists it first next time', async () => {
    await openPicker()
    await menuFor()
    await search('日历')
    await click(document.querySelector('[data-slot="icon-picker"] [role="option"][title="calendar"]'))
    await click(document.querySelector('[role="radio"][aria-label*="蓝"], [role="radio"][aria-label*="Blue"]'))
    await click([...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === t.icons.save))
    await settle(
      () => document.querySelector('[data-file-key="f:Tang"] svg[data-icon="calendar"]') !== null,
      'the icon in the tree',
    )
    expect(
      (document.querySelector('[data-file-key="f:Tang"] svg[data-icon="calendar"]') as SVGElement).style.color,
    ).toBe('var(--icon-blue)')
    const spaces = (adminRequests(m!.server)('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces
    const a = adminRequests(m!.server)('GET', `/api/spaces/${spaces[0].id}/appearance`).body as { folders: unknown[] }
    expect(a.folders).toEqual([{ folder: 'Tang', icon: 'calendar', color: 'blue' }])
    expect(readRecent()[0]).toBe('calendar')
  })
})
