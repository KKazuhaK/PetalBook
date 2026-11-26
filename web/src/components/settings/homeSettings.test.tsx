// @vitest-environment jsdom
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { addApi, addHitokoto, addJinrishici } from '@/api/fixtures/data/area'
import { shareSpace } from '@/api/fixtures/data/scenes'
import type { FixtureServer } from '@/api/fixtures/server'
import { keys } from '@/api/hooks'
import type { DailyPrefs } from '@/api/types'
import { t } from '@/i18n'
import { th } from '@/i18n/home'
import { click, installDomShims, locationText, mount, settle, type, type FxMounted } from '@/testing/harness'
import { sender } from '@/testing/listKit'
import SettingsRoute from './SettingsRoute'

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
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')
const navLinks = () => [...(dialog()?.querySelectorAll('nav a') ?? [])].map((a) => a.textContent)
const panel = () => dialog()?.querySelector<HTMLElement>('[role="tabpanel"]') ?? null
const tab = (lang: string) => dialog()!.querySelector<HTMLElement>(`[role="tab"][data-lang="${lang}"]`)!
const mode = (label: string) =>
  [...dialog()!.querySelectorAll<HTMLInputElement>('input[name="daily-mode"]')].find((r) =>
    r.parentElement?.textContent?.includes(label),
  )!
const select = (label: string) =>
  [...dialog()!.querySelectorAll<HTMLSelectElement>('select')].find(
    (s) => s.parentElement?.parentElement?.querySelector('label')?.textContent?.replace(/\*$/, '') === label,
  )!
const save = () => [...dialog()!.querySelectorAll('button')].find((b) => b.textContent === th.settings.save)!
const writes = () =>
  m!.requests
    .filter((r) => r.method === 'PUT' && r.url.includes('/daily/'))
    .map((r) => ({ url: r.url.replace(/^\/api\/spaces\/[^/]+/, ''), body: JSON.parse(r.body!) }))
const loaded = () => settle(() => panel() !== null, 'the home settings')

describe('the Home section, single-user mode', () => {
  const open = async (scene?: (s: ReturnType<typeof sender>) => void) => {
    m = await mount(routes, {
      entries: (server) => {
        scene?.(sender(server))
        return ['/settings/home']
      },
      fixtures: { mode: 'single' },
    })
    await loaded()
  }

  it('is a section of the space, with a tab for all languages and one for each language of the list', async () => {
    await open()
    expect(navLinks()).toContain(t.settingsUi.sections.home)
    expect([...dialog()!.querySelectorAll('[role="tab"]')].map((x) => x.textContent)).toEqual([
      th.settings.all,
      'zh-Hans',
      'en',
    ])
    // Nothing of an interface or the server's default in a server with one user.
    expect(dialog()!.querySelector('input[name="daily-mode"][type="radio"]:nth-of-type(3)')).toBeNull()
    expect(dialog()!.textContent).not.toContain(th.settings.inheritTitle)
  })

  it('picks a folder for all languages with a filter, and saves the one source', async () => {
    await open((s) => s('POST', '/folders', { path: '文案' }))
    await click(mode(th.settings.modes.own))
    await settle(() => select(th.settings.valueLabel.folder) !== undefined, 'the folder box')
    // Nothing chosen yet: it is asked for, and nothing is sent.
    await click(save())
    await settle(() => dialog()!.textContent!.includes(th.settings.chooseFirst), 'asked')
    expect(writes()).toEqual([])
    await type(select(th.settings.valueLabel.folder), '文案')
    await click([...dialog()!.querySelectorAll('button')].find((b) => b.textContent === th.settings.addFilter))
    const inputs = () => [...dialog()!.querySelectorAll<HTMLInputElement>('[data-slot="source-filter"] input')]
    await type(inputs()[0], 'status')
    await type(inputs()[1], '定稿, 终稿')
    await click(save())
    await settle(() => writes().length === 1, 'saved')
    expect(writes()[0]).toEqual({
      url: '/daily/settings',
      body: { daily: { folder: '文案', filter: [{ key: 'status', op: 'in', values: ['定稿', '终稿'] }] } },
    })
    // The server has it: the page starts again from it.
    await settle(() => (mode(th.settings.modes.own) as HTMLInputElement).checked, 'shown as saved')
    expect(sender(m!.fx)('GET', '/daily/settings').body.daily.folder).toBe('文案')
  })

  it('each language has its own page: a folder for Chinese, a tag for English, nothing for the rest', async () => {
    await open((s) => {
      s('POST', '/folders', { path: '中文' })
      s('POST', '/pages', { folder: '', meta: { tags: ['quotes'] }, body: 'q\n' })
    })
    await click(tab('zh-Hans'))
    await click(mode(th.settings.modes.own))
    await type(select(th.settings.valueLabel.folder), '中文')
    await click(tab('en'))
    // The English page does not show the Chinese choice.
    expect(mode(th.settings.modes.default).checked).toBe(true)
    await click(mode(th.settings.modes.own))
    await type(select(th.settings.kindLabel), 'tag')
    await settle(() => select(th.settings.valueLabel.tag) !== undefined, 'tags')
    await type(select(th.settings.valueLabel.tag), 'quotes')
    await click(save())
    await settle(() => writes().length === 1, 'saved')
    expect(writes()[0].body).toEqual({ daily: { 'zh-Hans': { folder: '中文' }, en: { tag: 'quotes' } } })
    expect(tab('zh-Hans').textContent).toContain('zh-Hans')
  })

  it('what is stored shows on its tab, and "not set" on a language takes its source away', async () => {
    await open((s) => {
      s('POST', '/folders', { path: 'A' })
      s('POST', '/folders', { path: 'B' })
      s('PUT', '/daily/settings', { daily: { folder: 'A', en: { folder: 'B' } } })
    })
    expect(mode(th.settings.modes.own).checked).toBe(true)
    expect(select(th.settings.valueLabel.folder).value).toBe('A')
    await click(tab('en'))
    expect(select(th.settings.valueLabel.folder).value).toBe('B')
    await click(mode(th.settings.modes.default))
    await click(save())
    await settle(() => writes().length === 1, 'saved')
    expect(writes()[0].body).toEqual({ daily: { folder: 'A' } })
  })

  it('a language that the file has but the list does not still gets a tab', async () => {
    await open((s) => {
      s('POST', '/folders', { path: 'A' })
      s('PUT', '/daily/settings', { daily: { 'pt-br': { folder: 'A' } } })
    })
    expect([...dialog()!.querySelectorAll('[role="tab"]')].map((x) => x.textContent)).toEqual([
      th.settings.all,
      'zh-Hans',
      'en',
      'pt-BR',
    ])
  })

  it('the choices follow the kind: folders, tags or the types of the space', async () => {
    await open()
    await click(mode(th.settings.modes.own))
    await type(select(th.settings.kindLabel), 'tag')
    await settle(() => select(th.settings.valueLabel.tag) !== undefined, 'tags')
    // No tag is in use: type is the way to a source that does not exist.
    await type(select(th.settings.kindLabel), 'type')
    await settle(() => select(th.settings.valueLabel.type) !== undefined, 'types')
    expect(select(th.settings.valueLabel.type).querySelectorAll('option').length).toBeGreaterThanOrEqual(1)
  })
})

describe('the Home section, accounts mode', () => {
  const asOwner = async (scene?: (fx: FixtureServer, space: string) => void) => {
    let space = ''
    m = await mount(routes, {
      signIn: true,
      fixtures: { mode: 'accounts' },
      entries: (fx) => {
        space = fx.data.spaces[0].info.id
        localStorage.setItem('pb.space', space)
        scene?.(fx, space)
        return ['/settings/home']
      },
    })
    await loaded()
    return space
  }

  const addMenu = () =>
    dialog()!.querySelector<HTMLSelectElement>(
      `[data-slot="daily-apis"] select[aria-label="${th.settings.addSource}"]`,
    )!
  const addable = () => [...addMenu().querySelectorAll('option')].map((o) => o.value).filter((v) => v !== '')
  const items = () =>
    [...dialog()!.querySelectorAll('[data-slot="source-list"] > li')].map((e) => e.getAttribute('data-source-kind'))
  const item = (id: string) => dialog()!.querySelector<HTMLElement>(`[data-api-item="${id}"]`)!

  it("the interfaces made for a language are on its tab; choosing them is saved as the space's choice", async () => {
    await asOwner(({ data }) => {
      addApi(data, 'zhone', 'zh-Hans')
      addApi(data, 'enone', 'en')
      addApi(data, 'anyone', '')
    })
    await click(tab('zh-Hans'))
    await click(mode(th.settings.modes.api))
    // The interfaces of the tab, and the other kinds of source.
    expect(addable()).toEqual(['zhone', '@list', '@own'])
    await type(addMenu(), 'zhone')
    expect(items()).toEqual(['api'])
    await click(save())
    await settle(() => writes().some((w) => w.url === '/daily/prefs'), 'saved')
    expect(writes().find((w) => w.url === '/daily/prefs')!.body).toEqual({ inherit: true, apis: ['zhone'] })
    expect(writes().some((w) => w.url === '/daily/settings')).toBe(false)
    // The all-languages tab has the one that takes any language.
    await click(tab(''))
    await click(mode(th.settings.modes.api))
    expect(addable()).toEqual(['anyone', '@list', '@own'])
  })

  it('sources of several kinds in a language: interfaces, the lines, pages of this space, in order', async () => {
    await asOwner(({ data }) => addApi(data, 'zhone', 'zh-Hans'))
    await click(tab('zh-Hans'))
    await click(mode(th.settings.modes.api))
    await type(addMenu(), 'zhone')
    await type(addMenu(), '@list')
    await type(addMenu(), '@own')
    expect(items()).toEqual(['api', 'list', 'own'])
    // The lines can be listed once.
    expect(addable()).toEqual(['@own'])
    // Pages of this space need a source before it is saved.
    await click(save())
    await settle(() => document.body.textContent!.includes(th.settings.chooseFirst), 'asked for a source')
    expect(writes().some((w) => w.url === '/daily/prefs')).toBe(false)
    const own = dialog()!.querySelector<HTMLElement>('[data-source-kind="own"]')!
    expect(own.querySelectorAll('select').length).toBeGreaterThan(0)
    await click(dialog()!.querySelector<HTMLElement>('[data-source-kind="list"] [data-action="up"]')!)
    expect(items()).toEqual(['list', 'api', 'own'])
  })

  it('the categories of a built-in interface and the order of several are saved with the choice', async () => {
    await asOwner(({ data }) => {
      addHitokoto(data)
      addApi(data, 'zhone', 'zh-Hans')
    })
    await click(tab('zh-Hans'))
    await click(mode(th.settings.modes.api))
    await type(addMenu(), 'zhone')
    await type(addMenu(), 'hitokoto')
    const cat = (v: string) =>
      item('hitokoto').querySelector<HTMLInputElement>(`[data-option="categories"] [data-value="${v}"]`)!
    expect(cat('').checked).toBe(true)
    await click(cat('i'))
    await click(cat('k'))
    // Hitokoto was chosen second; moved up it is tried first.
    await click(item('hitokoto').querySelector<HTMLElement>('[data-action="up"]')!)
    await click(save())
    await settle(() => writes().some((w) => w.url === '/daily/prefs'), 'saved')
    expect(writes().find((w) => w.url === '/daily/prefs')!.body).toEqual({
      inherit: true,
      apis: ['hitokoto', 'zhone'],
      options: { hitokoto: { categories: ['i', 'k'] } },
    })
  })

  it('a category that is a path is one choice, and the default is not sent', async () => {
    await asOwner(({ data }) => addJinrishici(data))
    await click(tab('zh-Hans'))
    await click(mode(th.settings.modes.api))
    await type(addMenu(), 'jinrishici')
    const pick = () => dialog()!.querySelector<HTMLSelectElement>('[data-option="category"] select')!
    expect(pick().value).toBe('all')
    await type(pick(), 'shuqing/libie')
    await click(save())
    await settle(() => writes().some((w) => w.url === '/daily/prefs'), 'saved')
    expect(writes().find((w) => w.url === '/daily/prefs')!.body).toEqual({
      inherit: true,
      apis: ['jinrishici'],
      options: { jinrishici: { category: ['shuqing/libie'] } },
    })
  })

  it('an interface that was switched off stays, greyed with the reason, and a save keeps it', async () => {
    await asOwner(({ data }, space) => {
      // Chosen while it was on, and switched off by the administrators afterwards: no longer on offer.
      data.area.daily.prefs[space] = { inherit: true, apis: ['hitokoto'] }
      data.area.daily.site.unavailable.hitokoto = 'disabled'
    })
    // It is on the first tab, in the list, marked.
    expect(mode(th.settings.modes.api).checked).toBe(true)
    const row = dialog()!.querySelector<HTMLElement>('[data-api-item="hitokoto"]')!
    expect(row.getAttribute('data-unavailable')).toBe('disabled')
    expect(row.querySelector('[data-slot="source-off"]')!.textContent).toContain(th.settings.srcOff.slice(1, 8))
    // Another change is saved, and it goes back with it.
    await click(dialog()!.querySelector<HTMLElement>('[role="switch"]')!)
    await click(save())
    await settle(() => writes().some((w) => w.url === '/daily/prefs'), 'saved')
    expect(writes().find((w) => w.url === '/daily/prefs')!.body).toMatchObject({ inherit: false, apis: ['hitokoto'] })
  })

  it('an interface that no longer exists is told, and nothing is shown in its place', async () => {
    await asOwner(({ data }, space) => {
      // The space chose it, and it was deleted afterwards.
      data.area.daily.prefs[space] = { inherit: true, apis: ['zhone'] }
      data.area.daily.site.unavailable.zhone = 'gone'
    })
    expect(dialog()!.querySelector('[data-slot="source-gone"]')!.textContent).toContain('zhone')
    expect(dialog()!.querySelector('[data-api-item="zhone"]')).toBeNull()
  })

  it('an interface left chosen on a language that no longer uses one is let go', async () => {
    await asOwner(({ data }, space) => {
      addApi(data, 'zhone', 'zh-Hans')
      data.area.daily.prefs[space] = { inherit: true, apis: ['zhone'] }
    })
    await click(tab('zh-Hans'))
    expect(mode(th.settings.modes.api).checked).toBe(true)
    await click(mode(th.settings.modes.default))
    await click(save())
    await settle(() => writes().length === 1, 'saved')
    expect(writes()[0]).toEqual({ url: '/daily/prefs', body: { inherit: true, apis: [] } })
  })

  it('a source and the switch saved together: if the second request fails, the choices, the tab and an error stay', async () => {
    await asOwner((fx, space) => {
      fx.data.area.daily.site.default = { kind: 'list' }
      fx.on(space)('POST', '/folders', { path: '文案' })
    })
    await click(tab('en'))
    await click(mode(th.settings.modes.own))
    await settle(() => select(th.settings.valueLabel.folder) !== undefined, 'the folder box')
    await type(select(th.settings.valueLabel.folder), '文案')
    await click(dialog()!.querySelector<HTMLElement>('[role="switch"]'))
    const real = globalThis.fetch
    globalThis.fetch = async (input, init) => {
      if (init?.method === 'PUT' && String(input instanceof Request ? input.url : input).includes('/daily/prefs')) {
        return new Response(JSON.stringify({ error: { code: 'internal', message: 'boom' } }), {
          status: 500,
          headers: { 'content-type': 'application/json' },
        })
      }
      return real(input, init)
    }
    try {
      await click(save())
      await settle(() => dialog()!.textContent!.includes(th.settings.failed), 'the error')
    } finally {
      globalThis.fetch = real
    }
    expect(writes().map((w) => w.url)).toEqual(['/daily/settings'])
    expect(tab('en').getAttribute('aria-selected')).toBe('true')
    expect((mode(th.settings.modes.own) as HTMLInputElement).checked).toBe(true)
    expect(dialog()!.querySelector('[role="switch"]')!.getAttribute('aria-checked')).toBe('false')
  })

  it("the server's default is the last resort, a switch the owner may turn off", async () => {
    await asOwner(({ data }) => void (data.area.daily.site.default = { kind: 'list' }))
    expect(dialog()!.textContent).toContain(th.settings.inheritHelp)
    const toggle = dialog()!.querySelector<HTMLElement>('[role="switch"]')!
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    await click(toggle)
    await click(save())
    await settle(() => writes().length === 1, 'saved')
    expect(writes()[0].body).toEqual({ inherit: false, apis: [] })
  })

  it('what the administrators decided for everyone is shown, not changeable, and says by whom', async () => {
    const space = await asOwner(({ data }) => addApi(data, 'zhone', 'zh-Hans'))
    const prefs = m!.qc.getQueryData<DailyPrefs | null>(keys.dailyPrefs(space))!
    await act(
      async () => void m!.qc.setQueryData(keys.dailyPrefs(space), { ...prefs, allowed: { own: false, api: false } }),
    )
    await settle(() => mode(th.settings.modes.own).disabled, 'own is locked')
    expect(mode(th.settings.modes.api).disabled).toBe(true)
    expect(mode(th.settings.modes.default).disabled).toBe(false)
    expect(mode(th.settings.modes.own).parentElement!.textContent).toContain('由管理员统一设置')
    expect(mode(th.settings.modes.api).parentElement!.textContent).toContain('由管理员统一设置')
    // One alone.
    await act(
      async () => void m!.qc.setQueryData(keys.dailyPrefs(space), { ...prefs, allowed: { own: true, api: false } }),
    )
    await settle(() => !mode(th.settings.modes.own).disabled, 'own is free again')
    expect(mode(th.settings.modes.api).disabled).toBe(true)
  })

  describe('who sees it', () => {
    it('an editor does not have the section, and the address falls to the first one', async () => {
      m = await mount(routes, {
        signIn: true,
        fixtures: { mode: 'accounts' },
        entries: (fx) => {
          localStorage.setItem('pb.space', shareSpace(fx, 'editor').space)
          return ['/settings/home']
        },
      })
      await settle(() => navLinks().length > 0 && locationText() !== '/settings/home', 'redirected')
      expect(navLinks()).not.toContain(t.settingsUi.sections.home)
      expect(locationText()).not.toBe('/settings/home')
    })

    it('an owner does', async () => {
      m = await mount(routes, {
        signIn: true,
        fixtures: { mode: 'accounts' },
        entries: (fx) => {
          localStorage.setItem('pb.space', shareSpace(fx, 'owner').space)
          return ['/settings/home']
        },
      })
      await loaded()
      expect(navLinks()).toContain(t.settingsUi.sections.home)
    })
  })
})
