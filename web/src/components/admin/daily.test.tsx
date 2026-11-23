// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { ta } from '@/i18n/admin'
import { adminRequests, click, installDomShims, makePerson, mount, settle, type, type Mounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const d = ta.daily
const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/:section?" element={<AdminGate />} />
      <Route path="*" element={<div data-testid="elsewhere" />} />
    </Routes>
  </SidebarProvider>
)
const open = async (scene?: (admin: ReturnType<typeof adminRequests>) => void) => {
  m = await mount(routes, {
    entries: (server) => {
      scene?.(adminRequests(server))
      return ['/admin/daily']
    },
    signIn: true,
    server: { mode: 'accounts' },
  })
  await settle(() => document.querySelector('[data-slot="admin-daily"]') !== null, 'the page')
}
const calls = () => m!.requests.filter((r) => r.method !== 'GET' && r.url.startsWith('/api/admin/daily')).map((r) => ({ method: r.method, url: r.url, body: r.body ? JSON.parse(r.body) : undefined }))
const byText = (selector: string, text: string) => [...document.querySelectorAll<HTMLElement>(selector)].find((e) => e.textContent?.trim() === text) ?? null
// The save button of the interface form when one is open (the sections above have their own).
const formSave = () => document.querySelector<HTMLElement>('[data-slot="api-form"] button[type="submit"]') ?? byText('button', d.save)
const api = (id: string) => document.querySelector<HTMLElement>(`[data-api="${id}"]`)
const input = (id: string) => document.getElementById(id) as HTMLInputElement
const GOOD = { id: 'quotes', name: '语录', lang: 'zh', url: 'https://example.org/q?l={lang}', text: 'hitokoto', source: 'from', author: 'from_who', link: '', refresh: 'daily', refreshMinutes: 0, skipNew: true, rps: 2, enabled: true }

describe('the administrators\' daily text page', () => {
  it('is a section of the administration with its own address, for administrators only', async () => {
    await open()
    expect(document.querySelector('nav[aria-label="' + ta.nav + '"] [aria-current="page"]')?.textContent).toBe(ta.sections.daily)
    await m!.unmount()
    // A member never gets it, nor fetches it.
    m = await mount(routes, { entries: ['/admin/daily'], server: { mode: 'accounts' } })
    const person = makePerson(m.server, 'carol')
    void person
    expect(document.querySelector('[data-slot="admin-daily"]')).toBeNull()
  })

  it('has room for the two switches spaces are limited by, and keeps them off until the server has them', async () => {
    await open()
    const [own, apiSwitch] = [...document.querySelectorAll<HTMLElement>('[data-slot="admin-daily"] [role="switch"]')]
    expect(own.getAttribute('aria-checked')).toBe('true')
    expect(own.hasAttribute('data-disabled')).toBe(true)
    expect(apiSwitch.hasAttribute('data-disabled')).toBe(true)
    expect(document.body.textContent).toContain(d.allowOwn)
    expect(document.body.textContent).toContain(d.allowApi)
  })

  it('the languages are tabs: add one, take one away', async () => {
    await open()
    expect([...document.querySelectorAll('[data-slot="admin-daily"] ul[aria-label="' + d.languagesTitle + '"] li')].map((l) => l.textContent?.trim())).toEqual([d.allLanguages, 'zh', 'en'])
    await type(input('daily-lang'), 'pt-BR')
    await click(byText('button', d.addLanguage))
    await settle(() => calls().some((c) => c.method === 'PUT' && c.url === '/api/admin/daily' && c.body.languages?.includes('pt-br')), 'saved')
    await settle(() => document.querySelector('button[aria-label="' + d.removeLanguage('pt-br') + '"]') !== null, 'the new one')
    await click(document.querySelector('button[aria-label="' + d.removeLanguage('en') + '"]'))
    await settle(() => document.querySelector('button[aria-label="' + d.removeLanguage('en') + '"]') === null, 'gone')
    expect(calls().at(-1)!.body).toEqual({ languages: ['zh', 'pt-br'] })
    // A tab for the new language is among those of the lines.
    expect(document.querySelector('[role="tablist"] [data-lang="pt-br"]')).not.toBeNull()
  })

  it('the pasted lines are saved for the language of the tab, and deleted', async () => {
    await open()
    const lines = [...document.querySelectorAll('textarea')].find((t) => t.getAttribute('aria-label') === d.linesText)!
    await click(document.querySelector('[role="tablist"][aria-label="' + d.linesTitle + '"] [data-lang="en"]'))
    await type(lines, 'Keep going\tSomeone\nAnother one')
    await click(byText('button', d.linesSave))
    await settle(() => calls().some((c) => c.url === '/api/admin/daily/lines'), 'saved')
    expect(calls().find((c) => c.url === '/api/admin/daily/lines')!.body).toEqual({ lang: 'en', text: 'Keep going\tSomeone\nAnother one', format: 'lines' })
    await settle(() => document.body.textContent!.includes(d.linesOfLang(2)), 'two lines')
    await click(byText('button', d.linesDelete))
    await settle(() => calls().some((c) => c.method === 'DELETE' && c.url === '/api/admin/daily/lines?lang=en'), 'deleted')
  })

  it('the default can be the lines, or a space, with the warning that everybody sees it', async () => {
    await open()
    const radios = () => [...document.querySelectorAll<HTMLInputElement>('input[name="daily-default"]')]
    await click(radios()[1])
    await click(formSave())
    await settle(() => calls().some((c) => c.body?.default?.kind === 'list'), 'list default')
    await click(radios()[2])
    expect(document.body.textContent).toContain(d.spaceWarning)
    // Without a space and a source it is not sent.
    await click(formSave())
    await settle(() => document.body.textContent!.includes(d.defaultSpaceIncomplete), 'asked for more')
  })

  describe('an interface', () => {
    it('the presets are only offered: nothing exists until saved, and the form is filled in', async () => {
      await open()
      expect(document.querySelectorAll('[data-api]')).toHaveLength(0)
      await click(byText('button', d.usePreset('一言')))
      expect(input('api-url').value).toContain('international.v1.hitokoto.cn')
      expect(input('api-id').value).toBe('hitokoto')
      expect(input('api-rps').value).toBe('20')
      expect(calls()).toEqual([])
      await click(formSave())
      await settle(() => api('hitokoto') !== null, 'saved')
      expect(calls()[0]).toMatchObject({ method: 'PUT', url: '/api/admin/daily/apis/hitokoto' })
    })

    it('makes any number of them, edits and deletes, and the host is put on the list', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(byText('button', d.addApi))
      await type(input('api-id'), 'second')
      await type(input('api-name'), '第二个')
      await type(input('api-url'), 'https://example.net/x')
      await type(input('api-text'), '[0].q')
      await click(formSave())
      await settle(() => api('second') !== null, 'the second')
      expect(calls().at(-1)!.body).toMatchObject({ id: 'second', name: '第二个', url: 'https://example.net/x', text: '[0].q', refresh: 'daily', enabled: true })
      // The allowed hosts have both.
      const hosts = document.querySelector<HTMLTextAreaElement>('textarea[aria-label="' + d.hostsTitle + '"]')!
      await settle(() => hosts.value.includes('example.net') || [...document.querySelectorAll('textarea')].some((t) => t.value.includes('example.net')), 'host listed')
      // Edit one.
      await click(api('quotes')!.querySelector('button'))
      await settle(() => input('api-name') !== null && input('api-name').value === '语录', 'opened')
      await type(input('api-name'), '语录二')
      await click(formSave())
      await settle(() => api('quotes')?.textContent?.includes('语录二') === true, 'renamed')
      // And delete it, after the question.
      await click(api('quotes')!.querySelector('button'))
      await settle(() => byText('button', d.delete) !== null, 'delete')
      await click(byText('button', d.delete))
      await click(byText('button', d.deleteConfirm))
      await settle(() => api('quotes') === null, 'deleted')
      expect(calls().at(-1)).toMatchObject({ method: 'DELETE', url: '/api/admin/daily/apis/quotes' })
    })

    it('shows a header\'s value never: "saved · change", and an untouched one keeps its value', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, headers: [{ name: 'X-Api-Key', value: 'secret-value-123' }] }))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => document.querySelector('[data-slot="api-header"]') !== null, 'the header')
      const row = document.querySelector<HTMLElement>('[data-slot="api-header"]')!
      expect(row.textContent).toContain(d.headerSet)
      expect(row.textContent).toContain(d.headerChange)
      expect(document.body.innerHTML).not.toContain('secret-value-123')
      expect(row.querySelector('input[type="password"]')).toBeNull()
      expect(row.querySelector<HTMLInputElement>('input')!.value).toBe('X-Api-Key')
      await click(formSave())
      await settle(() => calls().some((c) => c.url === '/api/admin/daily/apis/quotes'), 'saved')
      // Only the name travels: the server keeps what it has.
      expect(calls().find((c) => c.url === '/api/admin/daily/apis/quotes')!.body.headers).toEqual([{ name: 'X-Api-Key' }])
    })

    it('changing a header is typing a new value, which is sent; a new header has a value box', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, headers: [{ name: 'X-Api-Key', value: 'old' }] }))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => byText('button', d.headerChange) !== null, 'change')
      await click(byText('button', d.headerChange))
      const value = document.querySelector<HTMLInputElement>('input[aria-label="' + d.headerValue + '"]')!
      expect(value.type).toBe('password')
      await type(value, 'new-one')
      await click(byText('button', d.headerAdd))
      const boxes = document.querySelectorAll<HTMLInputElement>('[data-slot="api-header"]')
      expect(boxes).toHaveLength(2)
      await click(formSave())
      await settle(() => calls().some((c) => c.url === '/api/admin/daily/apis/quotes'), 'saved')
      // The second row has no name: it is left out.
      expect(calls().find((c) => c.url === '/api/admin/daily/apis/quotes')!.body.headers).toEqual([{ name: 'X-Api-Key', value: 'new-one' }])
    })

    it('"Try it" shows the raw answer and the fields the paths found, for the form as it is', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
      await click(await (async () => { await settle(() => api('quotes') !== null, 'the interface'); return api('quotes')!.querySelector('button') })())
      await settle(() => byText('button', d.tryButton) !== null, 'try')
      await click(byText('button', d.tryButton))
      await settle(() => document.querySelector('[data-slot="try-result"]') !== null, 'the result')
      const r = document.querySelector<HTMLElement>('[data-slot="try-result"]')!
      expect(r.textContent).toContain(d.tryOk(200))
      expect(r.querySelector('pre')!.textContent).toContain('山色空蒙雨亦奇')
      expect(r.textContent).toContain('饮湖上初晴后雨')
      expect(r.textContent).toContain('苏轼')
      const sent = calls().find((c) => c.url === '/api/admin/daily/try')!.body
      expect(sent).toMatchObject({ lang: 'zh', api: { id: 'quotes', url: GOOD.url, text: 'hitokoto' } })
      expect(sent.date).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    })

    it('a path that finds nothing is said, by field; the others are still shown', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => input('api-source') !== null, 'form')
      await type(input('api-source'), 'missing')
      await click(byText('button', d.tryButton))
      await settle(() => document.querySelector('[data-slot="try-result"]') !== null, 'the result')
      const r = document.querySelector<HTMLElement>('[data-slot="try-result"]')!
      expect(r.textContent).toContain('no text or number at')
      expect(r.textContent).toContain('山色空蒙雨亦奇')
      expect(r.textContent).not.toContain(d.tryOk(200))
    })

    it('an interface that did not answer says why', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, url: 'https://example.org/down' }))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => byText('button', d.tryButton) !== null, 'try')
      await click(byText('button', d.tryButton))
      await settle(() => document.querySelector('[data-slot="try-result"]') !== null, 'the result')
      expect(document.querySelector('[data-slot="try-result"]')!.textContent).toContain('status 503')
    })

    it('a refused interface (an address on the network inside) shows the server\'s reason and creates nothing', async () => {
      await open()
      await click(byText('button', d.addApi))
      await type(input('api-id'), 'bad')
      await type(input('api-name'), 'x')
      await type(input('api-url'), 'https://localhost/x')
      await type(input('api-text'), 't')
      await click(formSave())
      await settle(() => document.querySelector('[role="alert"]') !== null, 'the reason')
      expect(document.querySelector('[role="alert"]')!.textContent).toContain('内网')
      expect(document.querySelectorAll('[data-api]')).toHaveLength(0)
    })

    it('every minute counts: the minutes box shows for "every N minutes" and is sent', async () => {
      await open()
      await click(byText('button', d.addApi))
      await type(input('api-id'), 'm1')
      await type(input('api-name'), 'm')
      await type(input('api-url'), 'https://example.org/m')
      await type(input('api-text'), 't')
      await act(async () => void 0)
      const sel = input('api-refresh') as unknown as HTMLSelectElement
      await type(sel, 'minutes')
      await type(input('api-minutes'), '15')
      await click(formSave())
      await settle(() => calls().some((c) => c.url === '/api/admin/daily/apis/m1'), 'saved')
      expect(calls().find((c) => c.url === '/api/admin/daily/apis/m1')!.body).toMatchObject({ refresh: 'minutes', refreshMinutes: 15 })
    })
  })
})
