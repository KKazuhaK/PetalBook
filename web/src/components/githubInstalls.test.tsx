// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { githubApp, githubInstallations, githubProvider } from '@/api/fixtures/data/connections'
import { tc } from '@/i18n/console'
import { tg } from '@/i18n/github'
import { AdminGate } from '@/components/admin/AdminGate'
import SettingsRoute from '@/components/settings/SettingsRoute'
import { SidebarProvider } from '@/components/ui/sidebar'
import { addTenant, tenancyOn } from '@/testing/adminScene'
import type { FixtureServer } from '@/api/fixtures/server'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'

// The installations of the site's GitHub App that people and organisations connect themselves: the person's own in
// their settings, the organisation's in its console, "Install on GitHub", what each shows and disconnecting after
// saying which spaces lose access.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.restoreAllMocks()
})

const text = () => document.body.textContent ?? ''
const button = (label: string, root: ParentNode = document) =>
  [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label)
const byAria = (label: string) => document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)
const alert = () => document.querySelector<HTMLElement>('[role="alertdialog"]')
const row = (id: number) => document.querySelector<HTMLElement>(`[data-installation="${id}"]`)

// The person's settings, with the App set up and their own account's installation.
async function openMine(scene: (d: FixtureServer['data']) => void = () => {}) {
  m = await mount(
    <Routes>
      <Route path="/settings/:section?" element={<SettingsRoute />} />
    </Routes>,
    {
      signIn: true,
      fixtures: {
        mode: 'accounts',
        data: (d) => {
          githubProvider(d, 'connected')
          githubApp(d)
          githubInstallations(d)
          scene(d)
        },
      },
      entries: ['/settings/connections'],
    },
  )
}

describe("a person's own GitHub installations", () => {
  it('lists only their own, with the spaces on it, and installs on their account', async () => {
    const assign = vi.fn()
    vi.spyOn(window, 'location', 'get').mockReturnValue({ ...window.location, assign })
    await openMine()
    await settle(() => row(5101) !== null, 'their installation')
    expect(row(5101)!.textContent).toContain('tang-gh')
    expect(row(5101)!.textContent).toContain(tg.installs.ownerPerson)
    expect(row(5101)!.textContent).toContain(tg.installs.spaces(2))
    // The organisation's are not theirs to see here.
    expect(row(5001)).toBeNull()
    await click(button(tg.installs.install))
    await settle(() => m!.requests.some((r) => r.url === '/api/account/github/install'), 'the start')
    const req = m!.requests.find((r) => r.url === '/api/account/github/install')!
    expect(JSON.parse(req.body!)).toEqual({ return: '/settings/connections' })
    await settle(() => assign.mock.calls.length === 1, 'the trip to GitHub')
    expect(assign.mock.calls[0][0]).toContain('/installations/new')
  })

  it('shows its repositories on request', async () => {
    await openMine()
    await settle(() => row(5101) !== null, 'their installation')
    await click(button(tg.installs.repos, row(5101)!))
    await settle(() => row(5101)!.textContent!.includes('tang-gh/poems'), 'the repositories')
    expect(row(5101)!.textContent).toContain(tg.installs.private)
  })

  it('names the spaces that lose access before disconnecting', async () => {
    await openMine()
    await settle(() => row(5101) !== null, 'their installation')
    await click(byAria(tg.installs.disconnectFor('tang-gh'))!)
    await settle(() => alert()?.querySelector('[data-testid="affected-spaces"]') != null, 'the affected spaces')
    expect(alert()!.textContent).toContain(tg.installs.disconnectSpaces(2))
    expect(alert()!.textContent).toContain('Tang poems')
    expect(alert()!.textContent).toContain('Reading list')
    expect(m!.requests.some((r) => r.method === 'DELETE')).toBe(false)
    await click(button(tg.installs.disconnectConfirm, alert()!))
    await settle(() => m!.requests.some((r) => r.method === 'DELETE'), 'the disconnection')
    expect(m!.requests.find((r) => r.method === 'DELETE')!.url).toBe('/api/account/github/installations/5101')
    await settle(() => row(5101) === null, 'the row gone')
    expect(text()).toContain(tg.installs.none)
  })

  it('offers no install while the site has GitHub connections off', async () => {
    await openMine((d) => {
      d.features['features.githubConnect'] = false
    })
    await settle(() => text().includes(tg.installs.off), 'the note')
    expect(button(tg.installs.install)).toBeUndefined()
    // What is there can still be seen and disconnected.
    await settle(() => row(5101) !== null, 'their installation')
  })

  it('says what went wrong when an installation came back refused', async () => {
    m = await mount(
      <Routes>
        <Route path="/settings/:section?" element={<SettingsRoute />} />
      </Routes>,
      {
        signIn: true,
        fixtures: { mode: 'accounts', data: (d) => void githubProvider(d, 'connected') },
        entries: ['/settings?provider=github&connection_error=installation_taken'],
      },
    )
    await settle(() => text().includes(tg.account.errors.installation_taken), 'the explanation')
  })
})

const adminRoutes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
    </Routes>
  </SidebarProvider>
)

describe("an organisation's GitHub installations, in its console", () => {
  let tenant = ''
  async function openOrg(path: (id: string) => string, scene: (d: FixtureServer['data']) => void = () => {}) {
    m = await mount(adminRoutes, {
      signIn: true,
      fixtures: {
        mode: 'accounts',
        data: (d) => {
          tenancyOn(d)
          tenant = addTenant(d, 'acme', 'Acme').id
          githubProvider(d, 'connected')
          githubApp(d)
          githubInstallations(d)
          scene(d)
        },
      },
      entries: () => [path(tenant)],
    })
  }

  it("is a tab of the organisation's console, not of the site's", async () => {
    await openOrg(() => '/admin/storage/manage')
    await settle(() => document.querySelector('[role="tablist"], nav') !== null, 'the console')
    expect([...document.querySelectorAll('a')].some((a) => a.textContent?.trim() === tc.tabs.storage.github)).toBe(
      false,
    )
  })

  it("is not a tab of the default tenant's console, which is no organisation", async () => {
    await openOrg(() => '/admin/t/0000000000/storage/manage')
    await settle(() => text().includes(tc.tabs.storage.manage), 'the console in the default tenant')
    expect([...document.querySelectorAll('a')].some((a) => a.textContent?.trim() === tc.tabs.storage.github)).toBe(
      false,
    )
  })

  it('names the GitHub organisation, installs, lists it as the organisation’s and disconnects it', async () => {
    const assign = vi.fn()
    vi.spyOn(window, 'location', 'get').mockReturnValue({ ...window.location, assign })
    await openOrg((id) => `/admin/t/${id}/storage/github`)
    await settle(() => row(5001) !== null, "the organisation's installations")
    expect(row(5001)!.textContent).toContain(tg.installs.ownerOrg)
    expect(row(5101)).toBeNull()
    // The organisation's name is asked first, and checked.
    await click(button(tg.installs.install))
    await settle(() => text().includes(tg.installs.accountInvalid), 'the refusal of an empty name')
    expect(m!.requests.some((r) => r.url === '/api/admin/github/install')).toBe(false)
    await type(document.querySelector<HTMLInputElement>('form input')!, 'kazuhahub')
    await click(button(tg.installs.install))
    await settle(() => m!.requests.some((r) => r.url === '/api/admin/github/install'), 'the start')
    const req = m!.requests.find((r) => r.url === '/api/admin/github/install')!
    expect(JSON.parse(req.body!)).toEqual({ return: `/admin/t/${tenant}/storage/github`, account: 'kazuhahub' })
    expect(req.scope).toBe(`tenant:${tenant}`)
    await settle(() => assign.mock.calls.length === 1, 'the trip to GitHub')
    // Disconnecting names the space that loses access.
    await click(byAria(tg.installs.disconnectFor('kazuhahub'))!)
    await settle(() => alert()?.textContent?.includes('Team handbook') === true, 'the affected space')
    await click(button(tg.installs.disconnectConfirm, alert()!))
    await settle(() => m!.requests.some((r) => r.method === 'DELETE'), 'the disconnection')
    const del = m!.requests.find((r) => r.method === 'DELETE')!
    expect(del.url).toBe('/api/admin/github/installations/5001')
    expect(del.scope).toBe(`tenant:${tenant}`)
  })

  it('shows what GitHub sent back when it was not connected', async () => {
    await openOrg((id) => `/admin/t/${id}/storage/github?connection_error=installation_taken`)
    await settle(() => text().includes(tg.installs.returned.installation_taken), 'the explanation')
  })
})
