// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { MOCK_ADMIN, type MockServer } from '@/api/mock/server'
import { adminRequests, click, installDomShims, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'
import { oa } from '@/i18n/oauth'
import ConsentPage from './ConsentPage'

// The page an MCP client sends the person to, against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const leave = vi.fn<(url: string) => void>()
beforeEach(() => leave.mockReset())

const routes = (
  <Routes>
    <Route path="/oauth/consent" element={<ConsentPage leave={leave} />} />
    <Route path="*" element={<div>elsewhere</div>} />
  </Routes>
)
const text = () => document.body.textContent ?? ''
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label) ?? null
const boxes = () => [...document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')]
const decisions = () => m!.requests.filter((r) => r.method === 'POST' && r.url === '/api/oauth/consent').map((r) => JSON.parse(r.body!))

let req = ''
const open = async (start: Parameters<MockServer['startAuthorization']>[1] = {}, entry?: string) => {
  m = await mount(routes, {
    entries: (server) => {
      req = server.startAuthorization(MOCK_ADMIN.username, start)
      return [entry ?? `/oauth/consent?req=${req}`]
    },
    signIn: true,
    server: { mode: 'accounts', oauth: true },
  })
}

describe('the consent page', () => {
  it('says who asks and where the answer goes, and starts with the personal space ticked', async () => {
    await open({ clientName: 'Notes helper', redirectUri: 'http://localhost:7777/callback' })
    await settle(() => text().includes('Notes helper'), 'the page')
    expect(document.querySelector('h1')!.textContent).toBe(oa.consent.titleFor('Notes helper'))
    expect(text()).toContain(oa.consent.nameNote)
    expect(document.querySelector('[data-slot="consent-client"]')!.textContent).toContain('localhost:7777')
    expect(text()).toContain(oa.consent.redirectHelp('localhost:7777'))
    expect(boxes()).toHaveLength(1)
    expect(boxes()[0].checked).toBe(true)
    expect(document.querySelector('[data-slot="punycode"]')).toBeNull()
    // Read is chosen to start with, and write is offered because it was asked for.
    expect(document.querySelector<HTMLSelectElement>('#consent-access')!.value).toBe('read')
    expect(text()).toContain(oa.consent.signedInAs(MOCK_ADMIN.name ?? MOCK_ADMIN.username))
  })

  it('allows with the spaces and the access chosen, and goes to the address the server built', async () => {
    await open()
    await settle(() => boxes().length === 1, 'the page')
    await type(document.querySelector<HTMLSelectElement>('#consent-access')!, 'write')
    await click(button(oa.consent.allow))
    await settle(() => leave.mock.calls.length === 1, 'the way out')
    const d = decisions()
    expect(d).toHaveLength(1)
    expect(d[0]).toMatchObject({ approve: true, access: 'write' })
    expect(d[0].spaces).toHaveLength(1)
    const to = new URL(leave.mock.calls[0][0])
    expect(to.origin + to.pathname).toBe('http://localhost:7777/callback')
    expect(to.searchParams.get('code')).toBeTruthy()
    expect(to.searchParams.get('state')).toBe('st4te')
    expect(to.searchParams.get('iss')).toBeTruthy()
    // It cannot be sent twice.
    expect(document.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true)
    expect(text()).toContain(oa.consent.going)
  })

  it('asks for a space before it allows anything', async () => {
    await open()
    await settle(() => boxes().length === 1, 'the page')
    await click(boxes()[0])
    await click(button(oa.consent.allow))
    expect(text()).toContain(oa.consent.spaceRequired)
    expect(decisions()).toEqual([])
    expect(leave).not.toHaveBeenCalled()
  })

  it('denies, and the client is told so', async () => {
    await open()
    await settle(() => boxes().length === 1, 'the page')
    await click(button(oa.consent.deny))
    await settle(() => leave.mock.calls.length === 1, 'the way out')
    expect(decisions()).toEqual([{ req: expect.any(String), approve: false }])
    const to = new URL(leave.mock.calls[0][0])
    expect(to.searchParams.get('error')).toBe('access_denied')
    expect(to.searchParams.get('code')).toBeNull()
  })

  it('offers no write when only read was asked for', async () => {
    await open({ scopes: ['read'] })
    await settle(() => boxes().length === 1, 'the page')
    expect(document.querySelector('#consent-access')).toBeNull()
    expect(text()).toContain(oa.consent.readOnlyAsked)
    await click(button(oa.consent.allow))
    await settle(() => leave.mock.calls.length === 1, 'the way out')
    expect(decisions()[0]).toMatchObject({ approve: true, access: 'read' })
  })

  it('warns of a punycode address and shows the host of a metadata document client', async () => {
    await open({ redirectUri: 'https://xn--e1afmkfd.example/cb', clientId: 'https://app.example/client.json' })
    await settle(() => document.querySelector('[data-slot="punycode"]') !== null, 'the warning')
    expect(document.querySelector('[data-slot="punycode"]')!.textContent).toBe(oa.consent.punycode)
    expect(document.querySelector('[data-slot="consent-client"]')!.textContent).toContain('app.example')
  })

  it('lists the spaces the person is in, and a name that is hostile or long is only text', async () => {
    const hostile = '<img src=x onerror=alert(1)>'.padEnd(90, 'x')
    m = await mount(routes, {
      entries: (server) => {
        shareSpace(server, 'editor', 'Team')
        return [`/oauth/consent?req=${server.startAuthorization(MOCK_ADMIN.username, { clientName: hostile })}`]
      },
      signIn: true,
      server: { mode: 'accounts', oauth: true },
    })
    await settle(() => boxes().length === 2, 'both spaces')
    expect(boxes().map((b) => b.checked).sort()).toEqual([false, true]) // only the personal one is ticked
    expect(text()).toContain('Team')
    expect(text()).toContain(hostile)
    expect(document.querySelector('img[src="x"]')).toBeNull()
    // Ticking the shared space too, and allowing, names both.
    await click(boxes().find((b) => !b.checked))
    await click(button(oa.consent.allow))
    await settle(() => decisions().length === 1, 'the answer')
    expect(decisions()[0].spaces).toHaveLength(2)
  })

  it('says a request that is gone or never was is gone, without asking anything else', async () => {
    await open({}, '/oauth/consent?req=nope')
    await settle(() => text().includes(oa.consent.expired), 'the message')
    expect(boxes()).toHaveLength(0)
    await m!.unmount()
    m = await mount(routes, { entries: ['/oauth/consent'], signIn: true, server: { mode: 'accounts', oauth: true } })
    await settle(() => text().includes(oa.consent.expired), 'the message')
    expect(m.requests.filter((r) => r.url.startsWith('/api/oauth/consent'))).toEqual([])
  })

  it('a request decided already is gone the second time', async () => {
    await open()
    await settle(() => boxes().length === 1, 'the page')
    await click(button(oa.consent.allow))
    await settle(() => leave.mock.calls.length === 1, 'the way out')
    expect(() => adminRequests(m!.server)('POST', '/api/oauth/consent', { req, approve: true, spaces: [], access: 'read' })).toThrow(/404/)
  })

  it('holds a phone: one narrow column, and what can be long breaks', async () => {
    await open({ clientName: 'n'.repeat(100) })
    await settle(() => boxes().length === 1, 'the page')
    const main = document.querySelector('main')!
    expect(main.className).toMatch(/max-w-\[400px\]/)
    const form = document.querySelector('form')!
    expect(form.className).toMatch(/min-w-0/)
    expect(form.className).toMatch(/grid-cols-1/)
    for (const code of document.querySelectorAll('[data-slot="consent-client"] code')) expect(code.className).toMatch(/break-all/)
    expect(document.querySelector('[data-slot="consent-client"] dd.font-medium')!.className).toMatch(/break-words/)
    for (const b of [button(oa.consent.allow)!, button(oa.consent.deny)!]) expect(b.className).toMatch(/max-sm:w-full/)
  })
})
