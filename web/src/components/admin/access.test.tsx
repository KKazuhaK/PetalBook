// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { byLabel, click, type, installDomShims, mount, settle, type FxMounted } from '@/testing/harness'
import { addGrant, addPerson, addToken } from '@/testing/adminScene'
import { oa } from '@/i18n/oauth'
import { tc } from '@/i18n/console'
import { tk } from '@/i18n/tokens'
import { tt } from '@/i18n/tenants'
import { AdminGate } from './AdminGate'

// The administration's "machine access" page, against the fixture server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const items = () => [...document.querySelectorAll<HTMLElement>('[data-slot="token-item"]')]
const SETTING = '/api/admin/settings/'
// The writes the page made: the key and the body (the two switches are the settings features.tokens and features.mcp).
const puts = (m: FxMounted) =>
  m.requests
    .filter((r) => r.method === 'PUT' && r.url.startsWith(SETTING))
    .map((r) => [r.url.slice(SETTING.length), JSON.parse(r.body!)])
// A feature's row: its switch, its rule for the organizations, and its save button.
const row = (key: string) => document.querySelector<HTMLElement>(`[data-key="${key}"]`)!
const ruleOf = (key: string) => row(key).querySelectorAll<HTMLSelectElement>('select')[0]
const saveOf = (key: string) =>
  [...row(key).querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.includes(tt.settings.save))!
const checked = (label: string) => byLabel(label)!.getAttribute('aria-checked')
const siteValues = (m: FxMounted) => {
  const list = (
    m.fx.call('GET', '/api/admin/settings').body as { settings: { key: string; site: { value: unknown } }[] }
  ).settings
  const of = (key: string) => list.find((x) => x.key === key)!.site.value
  return { tokens: of('features.tokens'), mcp: of('features.mcp') }
}
const open = () => mount(routes, { entries: ['/admin/security/access'], signIn: true, fixtures: { mode: 'accounts' } })

describe('machine access', () => {
  it('has a page in the administration, with the switch on', async () => {
    m = await open()
    await settle(() => document.querySelector('[role="switch"]') !== null, 'the switch')
    expect(document.querySelector('nav [aria-current="page"]')?.textContent).toBe(tc.sections.security)
    expect(document.querySelector('[role="switch"]')!.getAttribute('aria-checked')).toBe('true')
    expect(text()).toContain(tk.admin.empty)
  })

  it('switches API tokens off, forced for every organization unless the rule says otherwise', async () => {
    m = await open()
    await settle(() => document.querySelector('[role="switch"]') !== null, 'the switch')
    expect(ruleOf('features.tokens').value).toBe('allow')
    await click(byLabel(tk.admin.switchLabel))
    expect(checked(tk.admin.switchLabel)).toBe('false')
    // Off proposes a lock: off for everybody, as the switch always was.
    expect(ruleOf('features.tokens').value).toBe('lock')
    await click(saveOf('features.tokens'))
    await settle(() => puts(m!).length === 1, 'saved')
    expect(puts(m)).toEqual([['features.tokens', { value: false, lower: { mode: 'lock' } }]])
    expect(siteValues(m)).toEqual({ tokens: false, mcp: true })
    // On again, and the organizations may choose for themselves again.
    await settle(
      () => ruleOf('features.tokens').value === 'lock' && checked(tk.admin.switchLabel) === 'false',
      'read back',
    )
    await click(byLabel(tk.admin.switchLabel))
    await type(ruleOf('features.tokens'), 'allow')
    await click(saveOf('features.tokens'))
    await settle(() => puts(m!).length === 2, 'saved again')
    expect(puts(m)[1]).toEqual(['features.tokens', { value: true, lower: { mode: 'allow' } }])
    expect(siteValues(m)).toEqual({ tokens: true, mcp: true })
  })

  it('makes off only the default when the administrator allows the organizations to choose', async () => {
    m = await open()
    await settle(() => document.querySelector('[role="switch"]') !== null, 'the switch')
    await click(byLabel(tk.admin.switchLabel))
    await type(ruleOf('features.tokens'), 'allow')
    // On and off again does not lock what the administrator chose.
    await click(byLabel(tk.admin.switchLabel))
    await click(byLabel(tk.admin.switchLabel))
    expect(ruleOf('features.tokens').value).toBe('allow')
    await click(saveOf('features.tokens'))
    await settle(() => puts(m!).length === 1, 'saved')
    expect(puts(m)).toEqual([['features.tokens', { value: false }]])
  })

  it("lists everyone's tokens with their owners and revokes one", async () => {
    m = await mount(routes, {
      entries: ['/admin/security/access'],
      signIn: true,
      fixtures: {
        mode: 'accounts',
        data: (d) => {
          addToken(d, addPerson(d, 'carol'), 'carol script')
          addToken(d, d.auth.users[0], 'admin script', { access: 'write' })
        },
      },
    })
    await settle(() => items().length === 2, 'both tokens')
    const carol = items().find((i) => i.textContent?.includes('carol script'))!
    expect(carol.textContent).toContain('carol')
    await click(byLabel(tk.revokeLabel('carol script')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(
      [...document.querySelectorAll<HTMLButtonElement>('[role="alertdialog"] button')].find(
        (b) => b.textContent?.trim() === tk.revokeConfirm,
      ),
    )
    await settle(
      () => items().find((i) => i.textContent?.includes('carol script'))?.dataset.state === 'revoked',
      'revoked',
    )
    expect(m.requests.filter((r) => r.method === 'DELETE' && r.url.startsWith('/api/admin/tokens/'))).toHaveLength(1)
  })

  it('reads the list a page at a time', async () => {
    m = await mount(routes, {
      entries: ['/admin/security/access'],
      signIn: true,
      fixtures: {
        mode: 'accounts',
        data: (d) => {
          for (let i = 0; i < 50; i++) addToken(d, d.auth.users[0], `t${i}`)
          addToken(d, addPerson(d, 'dave'), 'dave')
        },
      },
    })
    await settle(() => items().length === 50, 'the first page')
    const more = [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) =>
      b.textContent?.includes(tk.admin.loadMore),
    )!
    await click(more)
    await settle(() => items().length === 51, 'the second page')
    expect([...document.querySelectorAll('button')].some((b) => b.textContent?.includes(tk.admin.loadMore))).toBe(false)
  })
})

const grantItems = () => [...document.querySelectorAll<HTMLElement>('[data-slot="grant-item"]')]
describe('MCP and OAuth in the machine access', () => {
  it('has its own switch, and says nothing of the authorization server', async () => {
    m = await mount(routes, {
      entries: ['/admin/security/access'],
      signIn: true,
      fixtures: { mode: 'accounts' },
    })
    await settle(() => document.querySelector(`[aria-label="${oa.admin.mcpLabel}"]`) !== null, 'the switch')
    expect(document.querySelector(`[aria-label="${oa.admin.mcpLabel}"]`)!.getAttribute('aria-checked')).toBe('true')
    expect(document.querySelector('[data-slot="oauth-status"]')).toBeNull()
    expect(document.querySelectorAll('[role="switch"]')).toHaveLength(2)
    await click(byLabel(oa.admin.mcpLabel))
    await click(saveOf('features.mcp'))
    await settle(() => puts(m!).length === 1, 'saved')
    expect(puts(m)).toEqual([['features.mcp', { value: false, lower: { mode: 'lock' } }]])
    // The tokens' switch is its own.
    expect(siteValues(m)).toEqual({ tokens: true, mcp: false })
  })

  it('has the two switches and no list of apps when the server has no public address', async () => {
    m = await mount(routes, {
      // The server has no authorization server to list the apps of: it says there is no such endpoint.
      entries: (fx) => {
        fx.fail('GET', '/api/admin/grants', 404, { code: 'not_found' })
        return ['/admin/security/access']
      },
      signIn: true,
      fixtures: { mode: 'accounts' },
    })
    await settle(() => document.querySelectorAll('[role="switch"]').length === 2, 'the two switches')
    await new Promise((r) => setTimeout(r, 50))
    expect(text()).not.toContain(oa.admin.all)
  })

  it("lists everyone's connected apps with their owners and disconnects one", async () => {
    m = await mount(routes, {
      entries: ['/admin/security/access'],
      signIn: true,
      fixtures: {
        mode: 'accounts',
        data: (d) => {
          addGrant(d, addPerson(d, 'carol'), 'carol app')
          addGrant(d, addPerson(d, 'dave'), 'dave app')
        },
      },
    })
    await settle(() => grantItems().length === 2, 'both apps')
    const carol = grantItems().find((i) => i.textContent?.includes('carol app'))!
    expect(carol.textContent).toContain(`${oa.admin.owner}: `)
    expect(carol.textContent).toContain('carol')
    await click(byLabel(oa.yours.revokeLabel('carol app')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(
      [...document.querySelectorAll<HTMLButtonElement>('[role="alertdialog"] button')].find(
        (b) => b.textContent?.trim() === oa.yours.revokeConfirm,
      ),
    )
    await settle(() => grantItems().length === 1, 'one left')
    expect(grantItems()[0].textContent).toContain('dave app')
    expect(m.requests.filter((r) => r.method === 'DELETE' && r.url.startsWith('/api/admin/grants/'))).toHaveLength(1)
  })

  it('keeps listing them while MCP is off: they are kept, and may be disconnected', async () => {
    m = await mount(routes, {
      entries: ['/admin/security/access'],
      signIn: true,
      fixtures: { mode: 'accounts', data: (d) => void addGrant(d, addPerson(d, 'erin'), 'erin app') },
    })
    await settle(() => grantItems().length === 1, 'the app')
    await click(byLabel(oa.admin.mcpLabel))
    await click(saveOf('features.mcp'))
    await settle(() => puts(m!).length === 1, 'off')
    expect(grantItems()).toHaveLength(1)
  })
})
