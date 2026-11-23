import { describe, expect, it } from 'vitest'
import { dailyCodeMessage } from '@/components/admin/dailyCodes'
import { adminEn } from '@/i18n/admin/en'
import { adminZhCN } from '@/i18n/admin/zh-CN'
import { homeEn } from '@/i18n/home/en'
import { homeZhCN } from '@/i18n/home/zh-CN'
import { createMockServer } from './server'

// A refusal of the daily endpoints says why with a code; the mock answers the way the server does.
const admin = () => {
  const s = createMockServer({ mode: 'accounts' })
  const login = s.handle('POST', '/api/login', JSON.stringify({ username: 'admin', password: 'petalbook-admin' }), 'application/json', null)
  const cookie = login.headers?.['Set-Cookie']?.split(';')[0]
  return (method: string, url: string, body?: unknown) => s.handle(method, url, body === undefined ? undefined : JSON.stringify(body), method === 'GET' ? null : 'application/json', cookie)
}
const GOOD = { name: 'Quotes', lang: 'en', url: 'https://quotes.example.org/api/{lang}', text: '[0].q', source: '[0].s', author: '[0].a', link: '', refresh: 'daily', skipNew: true, rps: 2, headers: [{ name: 'X-Api-Key', value: 'v1' }] }
const put = (call: ReturnType<typeof admin>, over: Record<string, unknown>) => call('PUT', '/api/admin/daily/apis/quotes', { ...GOOD, ...over })

describe('the mock gives the stable codes', () => {
  it('a header without a value is header_missing, and a changed address asks for kept values again (header_reenter)', () => {
    const call = admin()
    expect(put(call, { headers: [{ name: 'X-New' }] })).toMatchObject({ status: 400, body: { error: 'header_missing', header: 'X-New' } })
    expect(put(call, {}).status).toBe(200)
    // The same address keeps the value; another one drops it.
    expect(put(call, { headers: [{ name: 'X-Api-Key' }] }).status).toBe(200)
    expect(put(call, { url: 'https://quotes.example.org/other/{lang}', headers: [{ name: 'X-Api-Key' }] })).toMatchObject({ status: 400, body: { error: 'header_reenter', header: 'X-Api-Key' } })
    expect(put(call, { url: 'https://quotes.example.org/other/{lang}', headers: [{ name: 'X-Api-Key', value: 'v2' }] }).status).toBe(200)
  })

  it('paths and the link: path_error and path_reveals_secret with the field, link_uses_header', () => {
    const call = admin()
    expect(put(call, { author: 'a b' })).toMatchObject({ status: 400, body: { error: 'path_error', field: 'author' } })
    expect(put(call, { source: 'headers.secret' })).toMatchObject({ status: 400, body: { error: 'path_reveals_secret', field: 'source' } })
    expect(put(call, { link: 'https://q.example.org/{header.X-Api-Key}' })).toMatchObject({ status: 400, body: { error: 'link_uses_header' } })
    expect(put(call, { link: 'https://q.example.org/{[0].id}' }).status).toBe(200)
  })

  it('a try says host_not_allowed with the host, not_json and too_large: a 400 with what the trial showed in result', () => {
    const call = admin()
    const tryIt = (url: string) => call('POST', '/api/admin/daily/try', { api: { ...GOOD, url }, lang: 'en', date: '2026-11-23' })
    expect(tryIt('https://elsewhere.example.net/q')).toMatchObject({ status: 400, body: { error: 'host_not_allowed', host: 'elsewhere.example.net', result: { ok: false } } })
    put(call, {})
    expect(tryIt('https://quotes.example.org/page.html')).toMatchObject({ status: 400, body: { error: 'not_json', result: { ok: false } } })
    expect(tryIt('https://quotes.example.org/huge')).toMatchObject({ status: 400, body: { error: 'too_large', result: { ok: false } } })
    expect(tryIt('https://quotes.example.org/down')).toMatchObject({ status: 200, body: { ok: false, status: 503 } })
  })

  it('a try reports a path that finds nothing as path_error with the field, and pathErrors in result', () => {
    const call = admin()
    put(call, {})
    const r = call('POST', '/api/admin/daily/try', { api: { ...GOOD, author: 'missing' }, lang: 'en', date: '2026-11-23' })
    expect(r).toMatchObject({ status: 400, body: { error: 'path_error', field: 'author', result: { ok: false, raw: expect.any(String) } } })
    expect((r.body as { result: { pathErrors: unknown } }).result.pathErrors).toEqual([{ field: 'author', code: 'path_error', message: expect.any(String) }])
  })

  it('a skip beyond the limit is skip_too_large; a malformed date stays invalid; out_of_range is for another day with an external source', () => {
    const call = admin()
    const space = (call('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
    const daily = (q: string) => call('GET', `/api/spaces/${space}/daily?${q}`)
    expect(daily('date=2026-11-23&skip=99999999')).toMatchObject({ status: 400, body: { error: 'skip_too_large' } })
    expect(daily('date=1969-12-31')).toMatchObject({ status: 400, body: { error: 'invalid' } })
    put(call, {})
    expect(call('PUT', `/api/spaces/${space}/daily/prefs`, { inherit: true, apis: ['quotes'] }).status).toBe(200)
    expect(daily('date=2001-01-01')).toMatchObject({ status: 200, body: { page: null, reason: 'out_of_range' } })
  })
})

describe('what each code says, in both languages', () => {
  const cases = [
    { code: 'host_not_allowed', host: 'a.example' },
    { code: 'header_reenter', header: 'X-Api-Key' },
    { code: 'header_missing', header: 'X-Api-Key' },
    { code: 'not_json' },
    { code: 'too_large' },
    { code: 'path_error', field: 'source' },
    { code: 'path_reveals_secret', field: 'author' },
    { code: 'link_uses_header' },
  ]
  const expected = (w: typeof adminEn.daily, c: (typeof cases)[number]): string => {
    switch (c.code) {
      case 'host_not_allowed': return w.tryHostNotAllowed('a.example')
      case 'header_reenter': return w.headerNeedsValue('X-Api-Key')
      case 'header_missing': return w.headerValueMissing('X-Api-Key')
      case 'not_json': return w.tryNotJson
      case 'too_large': return w.tryTooLarge
      case 'path_error': return w.pathError(w.fieldSource)
      case 'path_reveals_secret': return w.pathRevealsSecret(w.fieldAuthor)
      default: return w.linkUsesHeader
    }
  }
  for (const [name, w] of [['English', adminEn.daily], ['Chinese', adminZhCN.daily]] as const) {
    it(`${name}: every code has its own words, with what it is about`, () => {
      const said = cases.map((c) => dailyCodeMessage(c, w as typeof adminEn.daily))
      expect(said).toEqual(cases.map((c) => expected(w as typeof adminEn.daily, c)))
      expect(new Set(said).size).toBe(cases.length)
      expect(said[0]).toContain('a.example')
      expect(said[1]).toContain('X-Api-Key')
      expect(said[5]).toContain(w.fieldSource)
    })
  }

  it('a code that is not known, or none, is left to the server\'s own words', () => {
    expect(dailyCodeMessage({ code: 'something_new' }, adminEn.daily)).toBeNull()
    expect(dailyCodeMessage(undefined, adminEn.daily)).toBeNull()
    expect(dailyCodeMessage({}, adminZhCN.daily as typeof adminEn.daily)).toBeNull()
  })

  it('skip_too_large has its note in both languages', () => {
    expect(homeEn.daily.skipLimit).not.toBe('')
    expect(homeZhCN.daily.skipLimit).not.toBe('')
    expect(homeEn.daily.skipLimit).not.toBe(homeZhCN.daily.skipLimit)
  })
})
