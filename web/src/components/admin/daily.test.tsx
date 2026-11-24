// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
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
const calls = () =>
  m!.requests
    .filter((r) => r.method !== 'GET' && r.url.startsWith('/api/admin/daily'))
    .map((r) => ({ method: r.method, url: r.url, body: r.body ? JSON.parse(r.body) : undefined }))
const byText = (selector: string, text: string) =>
  [...document.querySelectorAll<HTMLElement>(selector)].find((e) => e.textContent?.trim() === text) ?? null
// The save button of the interface form when one is open (the sections above have their own).
const formSave = () =>
  document.querySelector<HTMLElement>('[data-slot="api-form"] button[type="submit"]') ?? byText('button', d.save)
const api = (id: string) => document.querySelector<HTMLElement>(`[data-api="${id}"]`)
const input = (id: string) => document.getElementById(id) as HTMLInputElement
const GOOD = {
  id: 'quotes',
  name: '语录',
  lang: 'zh-Hans',
  url: 'https://example.org/q?l={lang}',
  text: 'hitokoto',
  source: 'from',
  author: 'from_who',
  link: '',
  refresh: 'daily',
  refreshMinutes: 0,
  skipNew: true,
  rps: 2,
  enabled: true,
}

describe("the administrators' daily text page", () => {
  it('is a section of the administration with its own address, for administrators only', async () => {
    await open()
    expect(document.querySelector('nav[aria-label="' + ta.nav + '"] [aria-current="page"]')?.textContent).toBe(
      ta.sections.daily,
    )
    await m!.unmount()
    // A member never gets it, nor fetches it.
    m = await mount(routes, { entries: ['/admin/daily'], server: { mode: 'accounts' } })
    const person = makePerson(m.server, 'carol')
    void person
    expect(document.querySelector('[data-slot="admin-daily"]')).toBeNull()
  })

  it('shows the two switches spaces are limited by, on and usable once the server sends them', async () => {
    await open()
    const [own, apiSwitch] = [...document.querySelectorAll<HTMLElement>('[data-slot="admin-daily"] [role="switch"]')]
    expect(own.getAttribute('aria-checked')).toBe('true')
    expect(own.hasAttribute('data-disabled')).toBe(false)
    expect(apiSwitch.hasAttribute('data-disabled')).toBe(false)
    expect(document.body.textContent).toContain(d.allowOwn)
    expect(document.body.textContent).toContain(d.allowApi)
  })

  it('says what the server changed by itself on an upgrade, until it is dismissed', async () => {
    m = await mount(routes, {
      entries: (server) => {
        server.seedDailyNotices([
          { code: 'builtin_renamed', id: 'zenquotes', to: 'zenquotes-custom', message: 'renamed' },
          { code: 'dropped_language', lang: 'cn', field: 'source', message: 'dropped' },
        ])
        return ['/admin/daily']
      },
      signIn: true,
      server: { mode: 'accounts' },
    })
    await settle(() => document.querySelector('[data-slot="daily-notices"]') !== null, 'the notices')
    const text = document.querySelector('[data-slot="daily-notices"]')!.textContent
    expect(text).toContain(d.noticeRenamed('zenquotes', 'zenquotes-custom'))
    expect(text).toContain(d.noticeDropped('cn'))
    await click(byText('button', d.noticesDismiss))
    await settle(() => document.querySelector('[data-slot="daily-notices"]') === null, 'dismissed')
    expect(calls().at(-1)).toEqual({ method: 'PUT', url: '/api/admin/daily', body: { notices: [] } })
  })

  it('the languages are tabs: pick one from the known ones, search them, type any valid tag, take one away', async () => {
    await open()
    expect(
      [
        ...document.querySelectorAll(
          '[data-slot="admin-daily"] ul[aria-label="' + d.languagesTitle + '"]:not([role="listbox"]) li',
        ),
      ].map((l) => l.textContent?.trim()),
    ).toEqual([d.allLanguages, 'zh-Hans', 'en'])
    // The known languages are offered by tag, native name and English name, searchable.
    const picker = () => document.querySelector<HTMLElement>('[data-slot="admin-daily"] [data-slot="language-picker"]')!
    expect(picker().querySelector('[data-tag="pt-BR"]')).not.toBeNull()
    expect(picker().querySelector('[data-tag="en"]')).toBeNull() // already a tab
    await type(input('daily-lang'), 'portug')
    expect([...picker().querySelectorAll('[data-tag]')].map((b) => b.getAttribute('data-tag'))).toEqual([
      'pt-BR',
      'pt-PT',
    ])
    await type(input('daily-lang'), '繁體')
    expect([...picker().querySelectorAll('[data-tag]')].map((b) => b.getAttribute('data-tag'))).toEqual([
      'zh-Hant',
      'yue-Hant',
    ])
    await type(input('daily-lang'), 'pt-br')
    await click(picker().querySelector('[data-tag="pt-BR"]'))
    await settle(
      () =>
        calls().some((c) => c.method === 'PUT' && c.url === '/api/admin/daily' && c.body.languages?.includes('pt-BR')),
      'saved',
    )
    await settle(
      () => document.querySelector('button[aria-label="' + d.removeLanguage('pt-BR') + '"]') !== null,
      'the new one',
    )
    // A valid tag that is not among the known ones may be typed, and is offered canonically.
    await type(input('daily-lang'), 'SV-fi')
    expect(picker().querySelector('[data-tag="sv-FI"]')!.textContent).toBe(d.languageTyped('sv-FI'))
    await click(picker().querySelector('[data-tag="sv-FI"]'))
    await settle(
      () => document.querySelector('button[aria-label="' + d.removeLanguage('sv-FI') + '"]') !== null,
      'the typed one',
    )
    // What is no tag is not offered.
    await type(input('daily-lang'), 'zz_q')
    expect(picker().querySelector('[data-slot="language-none"]')!.textContent).toBe(d.languageBad)
    await click(document.querySelector('button[aria-label="' + d.removeLanguage('en') + '"]'))
    await settle(() => document.querySelector('button[aria-label="' + d.removeLanguage('en') + '"]') === null, 'gone')
    expect(calls().at(-1)!.body).toEqual({ languages: ['zh-Hans', 'pt-BR', 'sv-FI'] })
    // A tab for the new language is among those of the lines.
    expect(document.querySelector('[role="tablist"] [data-lang="pt-BR"]')).not.toBeNull()
  })

  it('the pasted lines are saved for the language of the tab, and deleted', async () => {
    await open()
    const lines = [...document.querySelectorAll('textarea')].find((t) => t.getAttribute('aria-label') === d.linesText)!
    await click(document.querySelector('[role="tablist"][aria-label="' + d.linesTitle + '"] [data-lang="en"]'))
    await type(lines, 'Keep going\tSomeone\nAnother one')
    await click(byText('button', d.linesSave))
    await settle(() => calls().some((c) => c.url === '/api/admin/daily/lines'), 'saved')
    expect(calls().find((c) => c.url === '/api/admin/daily/lines')!.body).toEqual({
      lang: 'en',
      text: 'Keep going\tSomeone\nAnother one',
      format: 'lines',
    })
    await settle(() => document.body.textContent!.includes(d.linesOfLang(2)), 'two lines')
    await click(byText('button', d.linesDelete))
    await settle(
      () => calls().some((c) => c.method === 'DELETE' && c.url === '/api/admin/daily/lines?lang=en'),
      'deleted',
    )
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

  it('the default can be an interface for each language, with one for the rest', async () => {
    await open((admin) => {
      admin('PUT', '/api/admin/daily/apis/quotes', GOOD)
      admin('PUT', '/api/admin/daily/apis/english', { ...GOOD, id: 'english', name: 'English quotes', lang: 'en' })
    })
    await settle(() => api('english') !== null, 'the interfaces')
    const radios = () => [...document.querySelectorAll<HTMLInputElement>('input[name="daily-default"]')]
    await click(radios()[3])
    const select = (l: string) => document.getElementById(`daily-default-api-${l}`) as unknown as HTMLSelectElement
    // Nothing chosen is not sent.
    await click(formSave())
    await settle(() => document.body.textContent!.includes(d.defaultApiIncomplete), 'asked for more')
    await type(select('zh-Hans'), 'quotes')
    await type(select('rest'), 'english')
    await click(formSave())
    await settle(() => calls().some((c) => c.body?.default?.kind === 'api'), 'saved')
    expect(calls().find((c) => c.body?.default?.kind === 'api')!.body).toEqual({
      default: { kind: 'api', apis: { 'zh-Hans': ['quotes'], '': ['english'] } },
    })
    await settle(() => document.getElementById('daily-default-api-zh-Hans') !== null, 'still there')
    expect(document.querySelector('[data-lang="zh-Hans"] [data-api-item="quotes"]')).not.toBeNull()
  })

  it('a language lists several interfaces in order, and a built-in one has its categories', async () => {
    await open((admin) => {
      admin('PUT', '/api/admin/daily/apis/quotes', GOOD)
      admin('PUT', '/api/admin/daily/providers/hitokoto', { enabled: true })
    })
    await settle(() => api('quotes') !== null, 'the interfaces')
    await click([...document.querySelectorAll<HTMLInputElement>('input[name="daily-default"]')][3])
    const select = (l: string) => document.getElementById(`daily-default-api-${l}`) as unknown as HTMLSelectElement
    await type(select('zh-Hans'), 'quotes')
    await type(select('zh-Hans'), 'hitokoto')
    const items = () =>
      [...document.querySelectorAll('[data-lang="zh-Hans"] [data-api-item]')].map((e) =>
        e.getAttribute('data-api-item'),
      )
    expect(items()).toEqual(['quotes', 'hitokoto'])
    // Moved up, it goes first.
    const up = document.querySelector<HTMLElement>('[data-api-item="hitokoto"] [data-action="up"]')!
    await click(up)
    expect(items()).toEqual(['hitokoto', 'quotes'])
    // Categories: none is "All"; two are sent in the order they are declared.
    const box = (v: string) =>
      document.querySelector<HTMLInputElement>(
        `[data-api-item="hitokoto"] [data-option="categories"] [data-value="${v}"]`,
      )!
    expect(box('').checked).toBe(true)
    await click(box('i'))
    await click(box('d'))
    expect(box('').checked).toBe(false)
    await click(formSave())
    await settle(() => calls().some((c) => c.body?.default?.kind === 'api'), 'saved')
    expect(calls().find((c) => c.body?.default?.kind === 'api')!.body).toEqual({
      default: {
        kind: 'api',
        apis: { 'zh-Hans': ['hitokoto', 'quotes'] },
        options: { hitokoto: { categories: ['d', 'i'] } },
      },
    })
    // "All" takes the choice back.
    await click(box(''))
    expect(box('d').checked).toBe(false)
  })

  it('lines are split into text, author and source for a preview that can be corrected before it is kept', async () => {
    await open()
    const lines = [...document.querySelectorAll('textarea')].find((t) => t.getAttribute('aria-label') === d.linesText)!
    await type(lines, '人到情多情转薄，而今真个不多情。 —— 纳兰性德《山花子》\nI think — therefore I am')
    await click(byText('button', d.linesPreview))
    await settle(() => document.querySelectorAll('[data-slot="lines-row"]').length === 2, 'two rows')
    const field = (row: number, name: string) =>
      document
        .querySelectorAll('[data-slot="lines-row"]')
        [row].querySelector<HTMLInputElement>(`[data-field="${name}"]`)!
    expect(field(0, 'author').value).toBe('纳兰性德')
    expect(field(0, 'source').value).toBe('山花子')
    // A dash inside a sentence is not a split.
    expect(field(1, 'author').value).toBe('')
    await type(field(1, 'author'), 'Descartes')
    await click(byText('button', d.linesSave))
    await settle(() => calls().some((c) => c.url === '/api/admin/daily/lines' && c.method === 'PUT'), 'saved')
    expect(calls().find((c) => c.url === '/api/admin/daily/lines' && c.method === 'PUT')!.body).toEqual({
      lang: '',
      lines: [
        { text: '人到情多情转薄，而今真个不多情。', author: '纳兰性德', source: '山花子' },
        { text: 'I think — therefore I am', author: 'Descartes' },
      ],
    })
  })

  it('一言 is offered for Chinese and for all the other languages, never for English', async () => {
    await open((admin) => admin('PUT', '/api/admin/daily/providers/hitokoto', { enabled: true }))
    await settle(() => document.querySelectorAll('input[name="daily-default"]').length === 4, 'the default editor')
    await click([...document.querySelectorAll<HTMLInputElement>('input[name="daily-default"]')][3])
    const offered = (l: string) =>
      [...document.getElementById(`daily-default-api-${l}`)!.querySelectorAll('option')].map((o) => o.value)
    expect(offered('en')).not.toContain('hitokoto')
    expect(offered('zh-Hans')).toContain('hitokoto')
    expect(offered('rest')).toContain('hitokoto')
  })

  it('the default can list sources of several kinds for a language, one text at a time', async () => {
    await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
    await settle(() => api('quotes') !== null, 'the interface')
    await click([...document.querySelectorAll<HTMLInputElement>('input[name="daily-default"]')][3])
    const add = (l: string) => document.getElementById(`daily-default-api-${l}`) as unknown as HTMLSelectElement
    await type(add('rest'), 'quotes')
    await type(add('rest'), '@list')
    // The lines can be in the list once.
    expect([...add('rest').querySelectorAll('option')].some((o) => o.value === '@list')).toBe(false)
    expect(
      [...document.querySelectorAll('[data-lang=""] [data-source-kind]')].map((e) =>
        e.getAttribute('data-source-kind'),
      ),
    ).toEqual(['api', 'list'])
    await click(document.querySelector<HTMLElement>('[data-lang=""] [data-source-kind="list"] [data-action="up"]')!)
    await click(formSave())
    await settle(() => calls().some((c) => c.body?.default?.kind === 'sources'), 'saved')
    expect(calls().find((c) => c.body?.default?.kind === 'sources')!.body).toEqual({
      default: { kind: 'sources', sources: { '': [{ kind: 'list' }, { kind: 'api', id: 'quotes' }] } },
    })
  })

  describe('the built-in interfaces', () => {
    it('are listed with switches, off until switched on, and nothing is edited in them', async () => {
      await open()
      const rows = [...document.querySelectorAll<HTMLElement>('[data-slot="builtin"] [data-provider]')]
      expect(rows.map((r) => r.getAttribute('data-provider'))).toEqual([
        'hitokoto',
        'zenquotes',
        'favqs',
        'animechan',
        'dummyjson',
        'jinrishici',
        'shanbay',
        'iciba',
      ])
      expect(rows[0].textContent).toContain('一言')
      expect(rows[0].textContent).toContain(d.builtinLanguages('zh-Hans, zh-Hant'))
      expect(rows[0].querySelector('a')!.getAttribute('href')).toBe('https://hitokoto.cn')
      const sw = (id: string) => document.querySelector<HTMLElement>(`[data-provider="${id}"] [role="switch"]`)!
      expect(sw('hitokoto').getAttribute('aria-checked')).toBe('false')
      expect(document.querySelectorAll('[data-api]')).toHaveLength(0)
      expect(byText('button', d.addApi)).not.toBeNull()
      expect(document.body.textContent).not.toContain('usePreset')
      expect(calls()).toEqual([])
      await click(sw('hitokoto'))
      await settle(() => sw('hitokoto').getAttribute('aria-checked') === 'true', 'on')
      expect(calls()).toEqual([{ method: 'PUT', url: '/api/admin/daily/providers/hitokoto', body: { enabled: true } }])
      // It is an interface among the others for the default, and not one to edit.
      expect(document.querySelectorAll('[data-api]')).toHaveLength(0)
      await click(sw('hitokoto'))
      await settle(() => sw('hitokoto').getAttribute('aria-checked') === 'false', 'off')
      expect(calls().at(-1)!.body).toEqual({ enabled: false })
    })
  })

  describe('an interface', () => {
    it('makes any number of them, edits and deletes', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(byText('button', d.addApi))
      await type(input('api-id'), 'second')
      await type(input('api-name'), '第二个')
      await type(input('api-url'), 'https://example.net/x')
      await type(input('api-text'), '[0].q')
      await click(formSave())
      await settle(() => api('second') !== null, 'the second')
      expect(calls().at(-1)!.body).toMatchObject({
        id: 'second',
        name: '第二个',
        url: 'https://example.net/x',
        text: '[0].q',
        refresh: 'daily',
        enabled: true,
      })
      // There is no list of hosts to keep.
      expect(document.querySelector('textarea[aria-label]')?.getAttribute('aria-label')).not.toBe('hosts')
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
      await open((admin) =>
        admin('PUT', '/api/admin/daily/apis/quotes', {
          ...GOOD,
          headers: [{ name: 'X-Api-Key', value: 'secret-value-123' }],
        }),
      )
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
      expect(calls().find((c) => c.url === '/api/admin/daily/apis/quotes')!.body.headers).toEqual([
        { name: 'X-Api-Key' },
      ])
    })

    it('changing a header is typing a new value, which is sent; a new header has a value box', async () => {
      await open((admin) =>
        admin('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, headers: [{ name: 'X-Api-Key', value: 'old' }] }),
      )
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
      expect(calls().find((c) => c.url === '/api/admin/daily/apis/quotes')!.body.headers).toEqual([
        { name: 'X-Api-Key', value: 'new-one' },
      ])
    })

    it('pressing Change and saving without typing keeps the stored value: only the name is sent', async () => {
      await open((admin) =>
        admin('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, headers: [{ name: 'X-Api-Key', value: 'old' }] }),
      )
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => byText('button', d.headerChange) !== null, 'change')
      await click(byText('button', d.headerChange))
      expect(document.querySelector<HTMLInputElement>('input[aria-label="' + d.headerValue + '"]')!.placeholder).toBe(
        d.headerKeep,
      )
      await click(formSave())
      await settle(() => calls().some((c) => c.url === '/api/admin/daily/apis/quotes'), 'saved')
      expect(calls().find((c) => c.url === '/api/admin/daily/apis/quotes')!.body.headers).toEqual([
        { name: 'X-Api-Key' },
      ])
    })

    it('another address than the saved one means the saved header values are not kept: they are typed again', async () => {
      await open((admin) =>
        admin('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, headers: [{ name: 'X-Api-Key', value: 'old' }] }),
      )
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => byText('button', d.headerChange) !== null, 'change')
      // The same host, another path: still another address.
      await type(input('api-url'), 'https://example.org/other?l={lang}')
      const row = document.querySelector<HTMLElement>('[data-slot="api-header"]')!
      expect(row.textContent).not.toContain(d.headerSet)
      expect(byText('button', d.headerChange)).toBeNull()
      const value = row.querySelector<HTMLInputElement>('input[type="password"]')!
      expect(value.placeholder).toBe(d.headerValue)
      expect(document.querySelector('[data-slot="header-again"]')?.textContent).toBe(d.headerAgain)
      // Nothing is sent without a value.
      await click(formSave())
      expect(document.querySelector('[data-slot="api-form"]')!.textContent).toContain(d.headerNeedsValue('X-Api-Key'))
      expect(calls().filter((c) => c.url === '/api/admin/daily/apis/quotes')).toHaveLength(0)
      await type(value, 'fresh')
      await click(formSave())
      await settle(() => calls().filter((c) => c.url === '/api/admin/daily/apis/quotes').length === 1, 'saved')
      expect(calls().filter((c) => c.url === '/api/admin/daily/apis/quotes')[0].body.headers).toEqual([
        { name: 'X-Api-Key', value: 'fresh' },
      ])
    })

    it('an address put back as saved gets the saved value back', async () => {
      await open((admin) =>
        admin('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, headers: [{ name: 'X-Api-Key', value: 'old' }] }),
      )
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => input('api-url') !== null, 'form')
      await type(input('api-url'), 'https://example.org/other')
      await type(input('api-url'), GOOD.url)
      expect(document.querySelector('[data-slot="api-header"]')!.textContent).toContain(d.headerSet)
      expect(document.querySelector('[data-slot="header-again"]')).toBeNull()
    })

    it("the link template says that request headers cannot be used in it, and a refusal by code is shown in the page's language", async () => {
      await open()
      await click(byText('button', d.addApi))
      await settle(() => input('api-link') !== null, 'form')
      expect(input('api-link').parentElement!.textContent).toContain(d.fieldLinkNoHeaders)
      await type(input('api-id'), 'quotes')
      await type(input('api-name'), 'Quotes')
      await type(input('api-url'), 'https://example.org/q')
      await type(input('api-text'), 'hitokoto')
      await type(input('api-link'), 'https://example.org/{header.X-Api-Key}')
      await click(formSave())
      await settle(() => document.body.textContent!.includes(d.linkUsesHeader), 'the refusal')
    })

    it("needs no list of hosts: a trial of another address is just made, and a refusal by code is said in the page's language", async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => input('api-url') !== null, 'form')
      await type(input('api-url'), 'https://new.example.net/q')
      expect(document.querySelector('[data-slot="try-host"]')).toBeNull()
      expect(document.body.textContent).not.toContain('允许列表')
      await click(byText('button', d.tryButton))
      await settle(() => document.querySelector('[data-slot="try-result"]') !== null, 'the result')
      expect(document.querySelector('[data-slot="try-result"]')!.textContent).toContain(d.tryOk(200))
      expect(calls().some((c) => c.url === '/api/admin/daily' && c.body?.hosts)).toBe(false)
    })

    it('the language is picked from the known ones (or typed), and a language mapping turns tags into codes', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, langMap: { 'zh-Hans': 'zh' } }))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => input('api-url') !== null, 'form')
      const form = () => document.querySelector<HTMLElement>('[data-slot="api-form"]')!
      expect(form().querySelector('[data-slot="api-lang-value"]')!.textContent).toBe('zh-Hans')
      await type(input('api-lang'), 'japan')
      await click(form().querySelector('[data-slot="language-picker"] [data-tag="ja"]'))
      expect(form().querySelector('[data-slot="api-lang-value"]')!.textContent).toBe('ja')
      // The mapping, as rows; one is there already, and a row with nothing in it is left out.
      const rows = () => [...form().querySelectorAll<HTMLElement>('[data-slot="api-langmap-row"]')]
      expect(rows()).toHaveLength(1)
      expect(rows()[0].querySelectorAll('input')[0].value).toBe('zh-Hans')
      expect(rows()[0].querySelectorAll('input')[1].value).toBe('zh')
      await click(byText('button', d.langMapAdd))
      await type(rows()[1].querySelectorAll('input')[0], 'ZH-hant')
      await type(rows()[1].querySelectorAll('input')[1], 'zh_tw')
      await click(byText('button', d.langMapAdd))
      await click(formSave())
      await settle(() => calls().some((c) => c.url === '/api/admin/daily/apis/quotes'), 'saved')
      expect(calls().find((c) => c.url === '/api/admin/daily/apis/quotes')!.body).toMatchObject({
        lang: 'ja',
        langMap: { 'zh-Hans': 'zh', 'zh-Hant': 'zh_tw' },
      })
      // A language that is not a tag is refused by code, in the page's words.
      await click(api('quotes')!.querySelector('button'))
      await settle(() => rows().length > 0, 'form again')
      await type(rows()[0].querySelectorAll('input')[0], 'chinese')
      await click(formSave())
      await settle(() => form().textContent!.includes(d.badLanguage), 'the refusal')
    })

    it('"Try it" shows the raw answer and the fields the paths found, for the form as it is', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
      await click(
        await (async () => {
          await settle(() => api('quotes') !== null, 'the interface')
          return api('quotes')!.querySelector('button')
        })(),
      )
      await settle(() => byText('button', d.tryButton) !== null, 'try')
      await click(byText('button', d.tryButton))
      await settle(() => document.querySelector('[data-slot="try-result"]') !== null, 'the result')
      const r = document.querySelector<HTMLElement>('[data-slot="try-result"]')!
      expect(r.textContent).toContain(d.tryOk(200))
      expect(r.querySelector('pre')!.textContent).toContain('山色空蒙雨亦奇')
      expect(r.textContent).toContain('饮湖上初晴后雨')
      expect(r.textContent).toContain('苏轼')
      const sent = calls().find((c) => c.url === '/api/admin/daily/try')!.body
      expect(sent).toMatchObject({ lang: 'zh-Hans', api: { id: 'quotes', url: GOOD.url, text: 'hitokoto' } })
      expect(sent.date).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    })

    it('a text path that finds nothing is said; the others are still shown', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => input('api-text') !== null, 'form')
      await type(input('api-text'), 'missing')
      await click(byText('button', d.tryButton))
      await settle(() => document.querySelector('[data-slot="try-result"]') !== null, 'the result')
      const r = document.querySelector<HTMLElement>('[data-slot="try-result"]')!
      expect(r.textContent).toContain(d.pathError(d.fieldText))
      expect(r.textContent).not.toContain(d.tryOk(200))
    })

    it('a source or author with no value (a null) is no error: the text and the rest are shown', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => input('api-author') !== null, 'form')
      await type(input('api-author'), 'null')
      await click(byText('button', d.tryButton))
      await settle(() => document.querySelector('[data-slot="try-result"]') !== null, 'the result')
      const r = document.querySelector<HTMLElement>('[data-slot="try-result"]')!
      expect(r.textContent).toContain(d.tryOk(200))
      expect(r.textContent).toContain('山色空蒙雨亦奇')
      expect(r.textContent).not.toContain(d.pathError(d.fieldAuthor))
      expect(r.querySelector('.text-destructive')).toBeNull()
    })

    it('an optional path error from the server is shown as no value, and a refusal for one is no refusal', async () => {
      const { TryResult } = await import('./DailyPanel')
      const { createRoot } = await import('react-dom/client')
      const host = document.createElement('div')
      document.body.append(host)
      const root = createRoot(host)
      await act(async () => {
        root.render(
          <TryResult
            result={{
              ok: false,
              status: 200,
              error: 'path_error',
              field: 'author',
              message: 'no text or number at from_who',
              quote: { text: 'x' },
              pathErrors: [{ field: 'author', code: 'path_error', message: 'no text or number at from_who' }],
            }}
          />,
        )
      })
      expect(host.textContent).toContain(d.tryOk(200))
      expect(host.textContent).not.toContain(d.pathError(d.fieldAuthor))
      expect(host.querySelector('.text-destructive')).toBeNull()
      await act(async () => root.unmount())
      host.remove()
    })

    it('marks what is required with a star and aria-required, and what is optional in words', async () => {
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', GOOD))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => input('api-text') !== null, 'form')
      const label = (id: string) => document.querySelector<HTMLElement>(`label[for="${id}"]`)!
      for (const id of ['api-id', 'api-name', 'api-url', 'api-text']) {
        expect(label(id).querySelector('[data-slot="required-mark"]')?.textContent).toBe('*')
        expect(label(id).querySelector('[data-slot="optional-mark"]')).toBeNull()
        expect(input(id).getAttribute('aria-required')).toBe('true')
      }
      for (const id of ['api-source', 'api-author', 'api-link']) {
        expect(label(id).querySelector('[data-slot="optional-mark"]')?.textContent).toBe(t.common.optionalMark)
        expect(label(id).querySelector('[data-slot="required-mark"]')).toBeNull()
        expect(input(id).getAttribute('aria-required')).toBeNull()
      }
      expect(document.querySelector('[data-slot="api-form"] legend [data-slot="optional-mark"]')).not.toBeNull()
      // The other forms of the page: the language tag, the interface-independent default's space.
      expect(document.getElementById('daily-lang')!.getAttribute('aria-required')).toBe('true')
      expect(label('daily-lang').querySelector('[data-slot="required-mark"]')).not.toBeNull()
    })

    it('nothing on the page can be wider than a phone: long text wraps and the raw answer scrolls inside itself', async () => {
      const long = 'https://example.org/' + 'a'.repeat(300)
      await open((admin) => admin('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, url: long }))
      await settle(() => api('quotes') !== null, 'the interface')
      await click(api('quotes')!.querySelector('button'))
      await settle(() => byText('button', d.tryButton) !== null, 'try')
      await click(byText('button', d.tryButton))
      await settle(() => document.querySelector('[data-slot="try-result"]') !== null, 'the result')
      const page = document.querySelector<HTMLElement>('[data-slot="admin-daily"]')!
      expect(page.className).toContain('min-w-0')
      const has = (el: Element | null, ...c: string[]) => c.every((x) => el?.classList.contains(x))
      // The address the trial used, and the raw answer.
      expect(has(document.querySelector('[data-slot="try-result"] p.font-mono'), 'break-all')).toBe(true)
      expect(
        has(document.querySelector('[data-slot="try-result"] pre'), 'max-w-full', 'overflow-auto', 'whitespace-pre'),
      ).toBe(true)
      // The inputs fill their column and may shrink; the editor's grids have a column that may shrink.
      for (const id of ['api-id', 'api-name', 'api-url', 'api-text', 'api-source', 'api-author', 'api-link'])
        expect(has(input(id), 'w-full', 'min-w-0')).toBe(true)
      expect(has(document.querySelector('[data-slot="api-form"]'), 'grid-cols-1', 'min-w-0')).toBe(true)
      expect(has(document.querySelector('[data-slot="api-try"]'), 'grid-cols-1', 'min-w-0')).toBe(true)
      // Help texts wrap.
      const helps = [
        ...document.querySelectorAll('[data-slot="api-form"] p.text-xs.text-muted-foreground:not(.font-mono)'),
      ]
      expect(helps.length).toBeGreaterThan(2)
      for (const h of helps) expect(h.classList.contains('break-words')).toBe(true)
      expect(has(page.querySelector(':scope > p'), 'break-words')).toBe(true)
      for (const s of page.querySelectorAll('section')) expect(s.classList.contains('grid-cols-1')).toBe(true)
      // The language pickers and the built-in interfaces wrap and shrink too.
      for (const l of page.querySelectorAll('[data-slot="language-picker"] ul'))
        expect(has(l, 'min-w-0', 'max-w-sm')).toBe(true)
      for (const li of page.querySelectorAll('[data-slot="builtin"] li')) expect(has(li, 'min-w-0')).toBe(true)
      for (const b of page.querySelectorAll('[data-slot="builtin"] a'))
        expect(b.classList.contains('break-all')).toBe(true)
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

    it("a refused interface (an address on the network inside) shows the server's reason and creates nothing", async () => {
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
      expect(calls().find((c) => c.url === '/api/admin/daily/apis/m1')!.body).toMatchObject({
        refresh: 'minutes',
        refreshMinutes: 15,
      })
    })
  })
})
