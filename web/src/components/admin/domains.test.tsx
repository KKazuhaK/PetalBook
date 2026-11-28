// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { newDomain } from '@/api/fixtures/routes/admin'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tdom } from '@/i18n/domains'
import { tt } from '@/i18n/tenants'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { addTenant, tenancyOn } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// A tenant's email domains (3.6 d2) on its page for the site (the Sign-in tab): the records of a new claim with their
// copy buttons, the state badges, "Check now" with its countdown, and switching the feature off, which shows what
// would stop and asks for the number of organisations before it is sent.

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

let tenant = ''
async function open(path: (id: string) => string, scene?: (d: FixtureData, tenantId: string) => void) {
  m = await mount(routes, {
    signIn: true,
    fixtures: {
      mode: 'accounts',
      empty: true,
      data: (d) => {
        tenancyOn(d)
        d.admin.siteHost = 'notes.example.test'
        tenant = addTenant(d, 'acme', 'Acme').id
        scene?.(d, tenant)
      },
    },
    entries: () => [path(tenant)],
  })
}
const at = '2026-11-20T08:00:00Z'
const card = (domain: string) => document.querySelector<HTMLElement>(`[data-slot="domain"][data-domain="${domain}"]`)
const buttonIn = (root: ParentNode | null, label: string) =>
  ([...(root?.querySelectorAll('button') ?? [])].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) ?? null) as HTMLButtonElement | null

describe("a tenant's email domains", () => {
  it('claims a domain and shows its TXT and CNAME records once, each with a copy button', async () => {
    await open((id) => `/admin/tenants/${id}/signin`)
    await settle(() => document.querySelector('[data-slot="domains-empty"]') !== null, 'the empty list')
    expect(document.body.textContent).toContain(tt.detail.tabs.signin)
    const input = document.querySelector<HTMLInputElement>('[data-slot="domain-claim"] input')!
    await type(input, 'acme.example')
    await click(buttonIn(document.querySelector('[data-slot="domain-claim"]'), tdom.add.submit))
    await settle(() => card('acme.example')?.querySelector('[data-slot="domain-records"]') != null, 'the records')
    const c = card('acme.example')!
    const texts = [...c.querySelectorAll('[data-slot="record-text"]')].map((x) => x.textContent)
    expect(texts[0]).toBe('_petalbook.acme.example')
    expect(texts[1]).toMatch(/^petalbook-verify=[a-z2-7]{26}$/)
    expect(texts[3]).toMatch(/^[a-z2-7]{26}\.verify\.notes\.example\.test$/)
    expect(c.querySelectorAll(`button[aria-label^="${tdom.records.copy('').trim().slice(0, 2)}"]`).length).toBe(4)
    expect(c.querySelector('[data-slot="domain-state"]')!.getAttribute('data-state')).toBe('unverified')
    expect(m!.requests.some((r) => r.method === 'POST' && r.url === `/api/admin/tenants/${tenant}/domains`)).toBe(true)
  })

  it('shows each state, and counts down on "Check now" after a check', async () => {
    await open(
      (id) => `/admin/tenants/${id}/signin`,
      (d, id) => {
        d.admin.domains[id] = [
          { ...newDomain('fresh.example', at, '2099-01-01T00:00:00Z') },
          {
            ...newDomain('gone.example', at, at),
            state: 'missing',
            verifiedAt: at,
            missingSince: at,
            nextCheckAt: '2099-01-01T00:00:00Z',
          },
          { ...newDomain('old.example', at, at), state: 'lapsed', lapsedAt: at, codeExpired: true },
          { ...newDomain('ok.example', at, at), state: 'verified', verifiedAt: at, restrictOrgs: true },
        ]
        d.admin.publishedDomains.push('fresh.example')
      },
    )
    await settle(() => card('ok.example') !== null, 'the list')
    const state = (d: string) => card(d)!.querySelector('[data-slot="domain-state"]')!.getAttribute('data-state')
    expect(['fresh.example', 'gone.example', 'old.example', 'ok.example'].map(state)).toEqual([
      'unverified',
      'missing',
      'lapsed',
      'verified',
    ])
    expect(card('ok.example')!.textContent).toContain(tdom.restrictedBadge)
    expect(card('old.example')!.textContent).toContain(tdom.code.expired)
    // An expired code cannot be checked until a new one is made.
    expect(buttonIn(card('old.example'), tdom.check.now)!.disabled).toBe(true)
    // A record that is there verifies the claim, and the button waits a minute.
    await click(buttonIn(card('fresh.example'), tdom.check.now))
    await settle(() => state('fresh.example') === 'verified', 'verified')
    const b = card('fresh.example')!.querySelector<HTMLButtonElement>('[data-slot="check-now"]')!
    expect(b.disabled).toBe(true)
    expect(b.textContent).toMatch(/\d/)
  })

  it('takes a refusal of the minute as a countdown from Retry-After', async () => {
    await open(
      (id) => `/admin/tenants/${id}/signin`,
      (d, id) => {
        d.admin.domains[id] = [newDomain('slow.example', at, '2099-01-01T00:00:00Z')]
      },
    )
    await settle(() => card('slow.example') !== null, 'the list')
    m!.fx.fail(
      'POST',
      '/api/admin/tenants/:id/domains/:domain/verify',
      429,
      { code: 'rate_limited' },
      {
        headers: { 'Retry-After': '42' },
        times: 1,
      },
    )
    await click(buttonIn(card('slow.example'), tdom.check.now))
    await settle(() => card('slow.example')!.textContent!.includes(tdom.check.again(42)), 'the countdown')
  })

  it('switching the feature off while tenants use it shows what stops and asks for the count first', async () => {
    await open(
      () => '/admin/settings',
      (d, id) => {
        d.admin.domains[id] = [{ ...newDomain('acme.example', at, at), state: 'verified', verifiedAt: at }]
      },
    )
    const row = () => document.querySelector<HTMLElement>('[data-slot="setting-row"][data-key="features.domains"]')
    await settle(() => row() !== null, 'the row')
    const sw = row()!.querySelector<HTMLButtonElement>('[role="switch"]')!
    await click(sw)
    await click(buttonIn(row(), tt.settings.save))
    const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')
    await settle(() => dlg()?.querySelector('[data-slot="plan-line"]') != null, 'the preview')
    expect(dlg()!.textContent).toContain('Acme')
    const go = buttonIn(dlg(), tdom.capability.confirm)!
    expect(go.disabled).toBe(true)
    await type(dlg()!.querySelector<HTMLInputElement>('input')!, '1')
    await click(go)
    await settle(() => dlg() === null, 'the dialog closes')
    const puts = m!.requests.filter((r) => r.method === 'PUT' && r.url === '/api/admin/settings/features.domains')
    expect(puts.map((r) => JSON.parse(r.body!).confirm ?? null)).toEqual([null, expect.stringMatching(/^c_/)])
  })

  it('a verified domain takes a join mode; turning it off while requests wait asks first, then cancels them', async () => {
    await open(
      (id) => `/admin/tenants/${id}/signin`,
      (d, id) => {
        d.admin.domains[id] = [
          { ...newDomain('acme.example', at, at), state: 'verified', verifiedAt: at, join: 'request' },
          newDomain('fresh.example', at, '2099-01-01T00:00:00Z'),
        ]
        d.admin.joinRequests[id] = [
          {
            id: '01JRWAIT01',
            username: 'bob',
            name: 'Bob',
            email: 'bob@acme.example',
            domain: 'acme.example',
            state: 'pending',
            created: at,
            expires: '2099-01-01T00:00:00Z',
            decidedAt: null,
            decidedBy: null,
            user: null,
          },
        ]
      },
    )
    await settle(() => card('acme.example') !== null, 'the list')
    // Only a verified domain has the control.
    expect(card('fresh.example')!.querySelector('[data-slot="domain-join-mode"]')).toBeNull()
    expect(card('acme.example')!.querySelector('[data-slot="domain-join"]')!.textContent).toBe(tdom.join.badge.request)
    const select = card('acme.example')!.querySelector<HTMLSelectElement>('[data-slot="domain-join-mode"] select')!
    await type(select, 'off')
    const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')
    await settle(() => dlg()?.textContent?.includes(tdom.joinOff.body(1)) === true, 'the question')
    await click(buttonIn(dlg(), tdom.joinOff.confirm))
    await settle(() => card('acme.example')?.querySelector('[data-slot="domain-join"]') === null, 'off')
    const patches = m!.requests.filter((r) => r.method === 'PATCH' && r.url.endsWith('/domains/acme.example'))
    expect(patches.map((r) => JSON.parse(r.body!).confirm ?? null)).toEqual([null, expect.stringMatching(/^c_/)])
    expect(m!.fx.data.admin.joinRequests[tenant][0].state).toBe('cancelled')
  })

  it('the requests to join: an administrator approves one and denies another', async () => {
    const req = (id: string, username: string) => ({
      id,
      username,
      name: username,
      email: `${username}@acme.example`,
      domain: 'acme.example',
      state: 'pending' as const,
      created: at,
      expires: '2099-01-01T00:00:00Z',
      decidedAt: null,
      decidedBy: null,
      user: null,
    })
    await open(
      (id) => `/admin/tenants/${id}/signin`,
      (d, id) => {
        d.admin.domains[id] = [
          { ...newDomain('acme.example', at, at), state: 'verified', verifiedAt: at, join: 'request' },
        ]
        d.admin.joinRequests[id] = [req('01JRWAIT01', 'bob'), req('01JRWAIT02', 'eve')]
      },
    )
    const rows = () => [...document.querySelectorAll<HTMLElement>('[data-slot="join-request"]')]
    await settle(() => rows().length === 2, 'the requests')
    await click(buttonIn(rows()[0], tdom.requests.approveLabel('bob')))
    await settle(() => rows().length === 1, 'approved')
    await click(buttonIn(rows()[0], tdom.requests.denyLabel('eve')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(buttonIn(document.querySelector('[role="alertdialog"]'), tdom.requests.deny))
    await settle(() => document.querySelector('[data-slot="join-requests-empty"]') !== null, 'none waits')
    expect(m!.fx.data.admin.joinRequests[tenant].map((r) => r.state)).toEqual(['approved', 'denied'])
  })

  it("offers the role of people who join by domain: only roles that are not the administrator's", async () => {
    await open(
      (id) => `/admin/tenants/${id}/signin`,
      (d, id) => {
        d.admin.domains[id] = [
          { ...newDomain('acme.example', at, at), state: 'verified', verifiedAt: at, join: 'auto' },
        ]
      },
    )
    await settle(() => document.querySelector('[data-slot="join-role"]') !== null, 'the role setting')
    const select = document.querySelector<HTMLSelectElement>('[data-slot="join-role"] select')!
    expect([...select.options].map((o) => o.value)).toEqual(['member'])
    expect(select.value).toBe('member')
    expect(document.body.textContent).toContain(tdom.joinRole.title)
  })

  it("the site's list warns of a mixed-script domain, there and in the transfer, which moves it", async () => {
    let beta = ''
    await open(
      () => '/admin/tenants/domains',
      (d, id) => {
        beta = addTenant(d, 'beta', 'Beta').id
        d.admin.domains[id] = [
          {
            ...newDomain('xn--pple-43d.example', at, at),
            display: 'аpple.example',
            mixedScript: true,
            state: 'verified',
            verifiedAt: at,
          },
          { ...newDomain('plain.example', at, at), state: 'verified', verifiedAt: at },
        ]
      },
    )
    const row = (d: string) => document.querySelector<HTMLElement>(`[data-slot="site-domain"][data-domain="${d}"]`)
    await settle(() => row('plain.example') !== null, 'the site list')
    expect(row('xn--pple-43d.example')!.querySelector('[data-slot="mixed-script"]')!.textContent).toContain(
      tdom.mixedBadge,
    )
    expect(row('plain.example')!.querySelector('[data-slot="mixed-script"]')).toBeNull()
    await click(buttonIn(row('xn--pple-43d.example'), tdom.site.transferLabel('аpple.example')))
    const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')
    await settle(() => dlg()?.querySelector('select option[value]:not([value=""])') != null, 'the tenants')
    expect(dlg()!.querySelector('[data-slot="transfer-mixed"]')!.textContent).toContain(tdom.mixedHelp)
    await type(dlg()!.querySelector('select')!, beta)
    await click(buttonIn(dlg(), tdom.site.confirm))
    await settle(() => dlg() === null, 'the dialog closes')
    const post = m!.requests.find((r) => r.method === 'POST' && r.url.endsWith('/assign'))!
    expect([post.url, JSON.parse(post.body!).tenant]).toEqual(['/api/admin/domains/xn--pple-43d.example/assign', beta])
  })
})
