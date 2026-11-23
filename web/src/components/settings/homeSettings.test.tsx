// @vitest-environment jsdom
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { keys } from '@/api/hooks'
import type { DailyPrefs } from '@/api/types'
import { t } from '@/i18n'
import { th } from '@/i18n/home'
import { adminRequests, click, installDomShims, locationText, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'
import { sender } from '@/testing/listKit'
import SettingsRoute from './SettingsRoute'

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
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')
const navLinks = () => [...(dialog()?.querySelectorAll('nav a') ?? [])].map((a) => a.textContent)
const panel = () => dialog()?.querySelector<HTMLElement>('[role="tabpanel"]') ?? null
const tab = (lang: string) => dialog()!.querySelector<HTMLElement>(`[role="tab"][data-lang="${lang}"]`)!
const mode = (label: string) => [...dialog()!.querySelectorAll<HTMLInputElement>('input[name="daily-mode"]')].find((r) => r.parentElement?.textContent?.includes(label))!
const select = (label: string) => [...dialog()!.querySelectorAll<HTMLSelectElement>('select')].find((s) => s.parentElement?.parentElement?.querySelector('label')?.textContent === label)!
const save = () => [...dialog()!.querySelectorAll('button')].find((b) => b.textContent === th.settings.save)!
const writes = () => m!.requests.filter((r) => r.method === 'PUT' && r.url.includes('/daily/')).map((r) => ({ url: r.url.replace(/^\/api\/spaces\/[^/]+/, ''), body: JSON.parse(r.body!) }))
const loaded = () => settle(() => panel() !== null, 'the home settings')

describe('the Home section, single-user mode', () => {
  const open = async (scene?: (s: ReturnType<typeof sender>) => void) => {
    m = await mount(routes, {
      entries: (server) => {
        scene?.(sender(server))
        return ['/settings/home']
      },
      server: { mode: 'single' },
    })
    await loaded()
  }

  it('is a section of the space, with a tab for all languages and one for each language of the list', async () => {
    await open()
    expect(navLinks()).toContain(t.settingsUi.sections.home)
    expect([...dialog()!.querySelectorAll('[role="tab"]')].map((x) => x.textContent)).toEqual([th.settings.all, 'zh', 'en'])
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
    expect(writes()[0]).toEqual({ url: '/daily/settings', body: { daily: { folder: '文案', filter: [{ key: 'status', op: 'in', values: ['定稿', '终稿'] }] } } })
    // The server has it: the page starts again from it.
    await settle(() => (mode(th.settings.modes.own) as HTMLInputElement).checked, 'shown as saved')
    expect(sender(m!.server)('GET', '/daily/settings').body.daily.folder).toBe('文案')
  })

  it('each language has its own page: a folder for Chinese, a tag for English, nothing for the rest', async () => {
    await open((s) => {
      s('POST', '/folders', { path: '中文' })
      s('POST', '/pages', { folder: '', meta: { tags: ['quotes'] }, body: 'q\n' })
    })
    await click(tab('zh'))
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
    expect(writes()[0].body).toEqual({ daily: { zh: { folder: '中文' }, en: { tag: 'quotes' } } })
    expect(tab('zh').textContent).toContain('zh')
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
    expect([...dialog()!.querySelectorAll('[role="tab"]')].map((x) => x.textContent)).toEqual([th.settings.all, 'zh', 'en', 'pt-br'])
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
  const asOwner = async (scene?: (admin: ReturnType<typeof adminRequests>, space: string) => void) => {
    let space = ''
    m = await mount(routes, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const admin = adminRequests(server)
        space = (admin('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
        localStorage.setItem('pb.space', space)
        scene?.(admin, space)
        return ['/settings/home']
      },
    })
    await loaded()
    return space
  }
  const API = (id: string, lang: string) => ({ id, name: `接口${id}`, lang, url: 'https://example.org/q', text: 't', refresh: 'daily', rps: 1, enabled: true })

  it('the interfaces made for a language are on its tab; choosing them is saved as the space\'s choice', async () => {
    await asOwner((admin) => {
      admin('PUT', '/api/admin/daily/apis/zhone', API('zhone', 'zh'))
      admin('PUT', '/api/admin/daily/apis/enone', API('enone', 'en'))
      admin('PUT', '/api/admin/daily/apis/anyone', API('anyone', ''))
    })
    await click(tab('zh'))
    await click(mode(th.settings.modes.api))
    const boxes = () => [...dialog()!.querySelectorAll<HTMLInputElement>('[data-slot="daily-apis"] input[type="checkbox"]')]
    expect(boxes()).toHaveLength(1)
    expect(boxes()[0].parentElement!.textContent).toBe(th.settings.apiFor('接口zhone', 'zh'))
    await click(boxes()[0])
    await click(save())
    await settle(() => writes().some((w) => w.url === '/daily/prefs'), 'saved')
    expect(writes().find((w) => w.url === '/daily/prefs')!.body).toEqual({ inherit: true, apis: ['zhone'] })
    expect(writes().some((w) => w.url === '/daily/settings')).toBe(false)
    // The all-languages tab has the one that takes any language.
    await click(tab(''))
    await click(mode(th.settings.modes.api))
    expect(boxes().map((b) => b.parentElement!.textContent)).toEqual([th.settings.apiFor('接口anyone', '')])
  })

  it('an interface left chosen on a language that no longer uses one is let go', async () => {
    await asOwner((admin, space) => {
      admin('PUT', '/api/admin/daily/apis/zhone', API('zhone', 'zh'))
      admin('PUT', `/api/spaces/${space}/daily/prefs`, { inherit: true, apis: ['zhone'] })
    })
    await click(tab('zh'))
    expect(mode(th.settings.modes.api).checked).toBe(true)
    await click(mode(th.settings.modes.default))
    await click(save())
    await settle(() => writes().length === 1, 'saved')
    expect(writes()[0]).toEqual({ url: '/daily/prefs', body: { inherit: true, apis: [] } })
  })

  it('the server\'s default is the last resort, a switch the owner may turn off', async () => {
    await asOwner((admin) => admin('PUT', '/api/admin/daily', { default: { kind: 'list' } }))
    expect(dialog()!.textContent).toContain(th.settings.inheritHelp)
    const toggle = dialog()!.querySelector<HTMLElement>('[role="switch"]')!
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    await click(toggle)
    await click(save())
    await settle(() => writes().length === 1, 'saved')
    expect(writes()[0].body).toEqual({ inherit: false, apis: [] })
  })

  it('what the administrators decided for everyone is shown, not changeable, and says by whom', async () => {
    const space = await asOwner((admin) => {
      admin('PUT', '/api/admin/daily/apis/zhone', API('zhone', 'zh'))
    })
    const prefs = m!.qc.getQueryData<DailyPrefs | null>(keys.dailyPrefs(space))!
    await act(async () => void m!.qc.setQueryData(keys.dailyPrefs(space), { ...prefs, allowed: { own: false, api: false } }))
    await settle(() => mode(th.settings.modes.own).disabled, 'own is locked')
    expect(mode(th.settings.modes.api).disabled).toBe(true)
    expect(mode(th.settings.modes.default).disabled).toBe(false)
    expect(mode(th.settings.modes.own).parentElement!.textContent).toContain('由管理员统一设置')
    expect(mode(th.settings.modes.api).parentElement!.textContent).toContain('由管理员统一设置')
    // One alone.
    await act(async () => void m!.qc.setQueryData(keys.dailyPrefs(space), { ...prefs, allowed: { own: true, api: false } }))
    await settle(() => !mode(th.settings.modes.own).disabled, 'own is free again')
    expect(mode(th.settings.modes.api).disabled).toBe(true)
  })

  describe('who sees it', () => {
    it('an editor does not have the section, and the address falls to the first one', async () => {
      m = await mount(routes, {
        signIn: true,
        server: { mode: 'accounts' },
        entries: (server) => {
          localStorage.setItem('pb.space', shareSpace(server, 'editor').space)
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
        server: { mode: 'accounts' },
        entries: (server) => {
          localStorage.setItem('pb.space', shareSpace(server, 'owner').space)
          return ['/settings/home']
        },
      })
      await loaded()
      expect(navLinks()).toContain(t.settingsUi.sections.home)
    })
  })
})
