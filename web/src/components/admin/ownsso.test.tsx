// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { makeProvider } from '@/api/fixtures/data/admin'
import { newDomain } from '@/api/fixtures/routes/admin'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tdom } from '@/i18n/domains'
import { tg } from '@/i18n/groups'
import { tsec } from '@/i18n/security'
import { tt } from '@/i18n/tenants'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { addProvider, addTenant, ownRule, tenancyOn } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// An organisation's own single sign-on (3.6 d3) on the tenant's Sign-in tab: its providers (OpenID Connect and SAML,
// no templates, no class or trust switch to choose), a verified domain routed to one of them, the section when the
// feature is off, and the preview of switching features.ownSso off.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const tp = tsec.providers
const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)

const at = '2026-11-20T08:00:00Z'
let tenant = ''
async function open(path: (id: string) => string, scene?: (d: FixtureData, tenantId: string) => void) {
  m = await mount(routes, {
    signIn: true,
    fixtures: {
      mode: 'accounts',
      empty: true,
      data: (d) => {
        tenancyOn(d)
        tenant = addTenant(d, 'acme', 'Acme').id
        scene?.(d, tenant)
      },
    },
    entries: () => [path(tenant)],
  })
}

// A provider of the tenant's own, as its administrators' POST makes one.
function orgProvider(d: FixtureData, owner: string, slug: string, name: string) {
  const p = makeProvider(
    { slug, name, owner, kind: 'oidc', oidc: { issuer: 'https://idp.acme.example', clientId: 'x', clientSecret: 'x' } },
    at,
    d.admin.providers.length + 1,
    `01SS${slug.toUpperCase().padEnd(6, 'X').slice(0, 6)}`,
  )
  d.admin.providers.push(p)
  return p
}

const section = () => document.querySelector<HTMLElement>('section[aria-labelledby="org-providers"]')
const buttonIn = (root: ParentNode | null, label: string) =>
  ([...(root?.querySelectorAll('button') ?? [])].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) ?? null) as HTMLButtonElement | null
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')

describe("an organisation's own single sign-on", () => {
  it('adds an OpenID Connect or SAML provider of its own, without templates or the site’s switches', async () => {
    await open((id) => `/admin/tenants/${id}/signin`)
    await settle(() => section() !== null, 'the section')
    expect(section()!.textContent).toContain(tp.org.title)
    // No verified domain yet: the section says so.
    await settle(() => document.querySelector('[data-slot="org-need-domain"]') !== null, 'the note')
    await click(buttonIn(section(), tp.add))
    await settle(() => dialog()?.querySelector('[data-slot="provider-form"]') != null, 'the form, at once')
    const form = dialog()!.querySelector<HTMLElement>('[data-slot="provider-form"]')!
    expect(dialog()!.querySelector('[data-template]')).toBeNull()
    const kinds = [...form.querySelectorAll<HTMLOptionElement>('#provider-kind option')].map((o) => o.value)
    expect(kinds).toEqual(['oidc', 'saml'])
    expect(form.querySelector('#provider-class')!.closest('.hidden')).not.toBeNull()
    expect(form.textContent).not.toContain(tp.mergeVerifiedEmail)
    // Its slug starts with the organisation's own, and the hint says so.
    expect(form.textContent).toContain(tp.org.slugHelp)
    expect(form.textContent).not.toContain(tp.slugHelp)
    expect(form.querySelector('[data-slot="org-trust"]')!.textContent).toBe(tp.org.trust)
    const field = (label: string) =>
      [...form.querySelectorAll('label')].find((l) => l.textContent?.startsWith(label))!.control as HTMLInputElement
    await type(field(tp.slug), 'acme-sso')
    await type(field(tp.name), 'Acme SSO')
    await type(form.querySelector<HTMLTextAreaElement>('[data-slot="groups-allow"]')!, 'staff\nadmins')
    await click(buttonIn(form, tp.create))
    await settle(() => dialog() === null, 'the dialog closes')
    const post = m!.requests.find((r) => r.method === 'POST' && r.url === `/api/admin/tenants/${tenant}/providers`)!
    const body = JSON.parse(post.body!)
    expect([body.slug, body.kind, body.class, body.groups.allow]).toEqual([
      'acme-sso',
      'oidc',
      'sso',
      ['staff', 'admins'],
    ])
    expect(body.mergeVerifiedEmail).toBeUndefined()
    await settle(() => section()!.querySelector('[data-provider="acme-sso"]') !== null, 'the new row')
  })

  it('routes a verified domain to one of its providers, and the provider lists it', async () => {
    let pid = ''
    await open(
      (id) => `/admin/tenants/${id}/signin`,
      (d, id) => {
        pid = orgProvider(d, id, 'acme-sso', 'Acme SSO').id
        d.admin.domains[id] = [
          { ...newDomain('acme.example', at, at), state: 'verified', verifiedAt: at },
          newDomain('later.example', at, '2099-01-01T00:00:00Z'),
        ]
      },
    )
    const card = (domain: string) => document.querySelector<HTMLElement>(`[data-domain="${domain}"]`)
    await settle(() => card('acme.example')?.querySelector('[data-slot="domain-route"] select') != null, 'the route')
    // Only a verified domain is routed.
    expect(card('later.example')!.querySelector('[data-slot="domain-route"]')).toBeNull()
    expect(document.querySelector('[data-slot="org-need-domain"]')).toBeNull()
    const select = card('acme.example')!.querySelector<HTMLSelectElement>('[data-slot="domain-route"] select')!
    expect(select.getAttribute('aria-label')).toBe(tdom.route.label('acme.example'))
    await type(select, pid)
    await settle(
      () => section()?.querySelector('[data-slot="routed-domains"]')?.textContent === tp.org.domains('acme.example'),
      'the provider shows its domain',
    )
    const patch = m!.requests.find((r) => r.method === 'PATCH' && r.url.endsWith('/domains/acme.example'))!
    expect(JSON.parse(patch.body!)).toEqual({ provider: pid })
  })

  it('says the feature is off, and asks nothing of the providers', async () => {
    await open(
      (id) => `/admin/tenants/${id}/signin`,
      (d, id) => {
        ownRule(d, id, 'features.ownSso', { value: false })
        orgProvider(d, id, 'acme-sso', 'Acme SSO')
      },
    )
    await settle(() => section()?.querySelector('[data-slot="feature-off"]') != null, 'the notice')
    expect(section()!.textContent).toContain(tp.org.featureOff)
    expect(buttonIn(section(), tp.add)).toBeNull()
    expect(m!.requests.some((r) => r.url === `/api/admin/tenants/${tenant}/providers`)).toBe(false)
  })

  it('switching features.ownSso off while a tenant has providers shows them and asks for the count first', async () => {
    await open(
      () => '/admin/settings',
      (d, id) => {
        orgProvider(d, id, 'acme-sso', 'Acme SSO')
      },
    )
    const row = () => document.querySelector<HTMLElement>('[data-slot="setting-row"][data-key="features.ownSso"]')
    await settle(() => row() !== null, 'the row')
    await click(row()!.querySelector<HTMLButtonElement>('[role="switch"]')!)
    await click(buttonIn(row(), tt.settings.save))
    await settle(() => dialog()?.querySelector('[data-slot="plan-line"]') != null, 'the preview')
    expect(dialog()!.querySelector('[data-slot="plan-line"]')!.textContent).toContain(
      tdom.capability.providersLine(1, 1),
    )
    await type(dialog()!.querySelector<HTMLInputElement>('input')!, '1')
    await click(buttonIn(dialog(), tdom.capability.confirm))
    await settle(() => dialog() === null, 'the dialog closes')
    const puts = m!.requests.filter((r) => r.method === 'PUT' && r.url === '/api/admin/settings/features.ownSso')
    expect(puts.map((r) => JSON.parse(r.body!).confirm ?? null)).toEqual([null, expect.stringMatching(/^c_/)])
  })

  it("a site provider's groups allowlist is sent as its lines, and only what changed", async () => {
    await open(
      () => '/admin/security/providers',
      (d) => {
        addProvider(d, 'corp', 'Corp')
      },
    )
    await settle(() => buttonIn(document, tp.editFor('Corp')) !== null, 'the row')
    await click(buttonIn(document, tp.editFor('Corp')))
    await settle(() => dialog()?.querySelector('[data-slot="groups-allow"]') != null, 'the form')
    const label = [...dialog()!.querySelectorAll('label')].find((l) => l.textContent === tg.provider.allow)
    expect(label?.htmlFor).toBe('groups-allow')
    await type(dialog()!.querySelector<HTMLTextAreaElement>('[data-slot="groups-allow"]')!, ' staff \n\nops')
    await click(buttonIn(dialog(), tp.save))
    await settle(() => dialog() === null, 'the dialog closes')
    const patch = m!.requests.find((r) => r.method === 'PATCH' && r.url === '/api/admin/providers/corp')!
    expect(JSON.parse(patch.body!)).toEqual({ groups: { allow: ['staff', 'ops'] } })
  })
})
