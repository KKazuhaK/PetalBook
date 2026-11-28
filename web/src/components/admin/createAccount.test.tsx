// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tc } from '@/i18n/console'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import type { FixtureData } from '@/api/fixtures/data/model'
import { addPerson, asTenantAdmin } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// "Create account" on the Users page: an administrator makes an account by hand (the way in when registration is
// closed). The dialog asks for the person and the role (and, across the site, the tenant), and shows the one-time
// set-password link once, with whether it was mailed.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const d = tc.users.createDialog
const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const dlg = () => document.querySelector<HTMLElement>('[data-slot="create-account-dialog"]')
const button = (label: string, within: ParentNode = document) =>
  [...within.querySelectorAll('button')].find((b) => b.textContent === label) as HTMLButtonElement
const input = (label: string) => {
  const l = [...dlg()!.querySelectorAll('label')].find((x) => x.textContent!.startsWith(label))!
  return dlg()!.querySelector<HTMLInputElement>(`#${CSS.escape(l.htmlFor)}`)!
}
const posts = () => m!.requests.filter((r) => r.method === 'POST' && r.url === '/api/admin/users')

async function open(scene?: (d: FixtureData) => unknown, options: { fx?: (m: FxMounted) => void } = {}) {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts', data: (data) => void scene?.(data) },
    entries: ['/admin/users'],
  })
  options.fx?.(m)
  await settle(() => button(tc.users.create) !== undefined, 'the button')
}

describe('Create account', () => {
  it('sits beside Invite a user, and opens a dialog with the tenant to pick across the site', async () => {
    await open()
    const header = document.querySelector('[data-slot="admin-page-actions"]')!
    expect([...header.querySelectorAll('a, button')].map((x) => x.textContent)).toEqual([
      tc.users.create,
      tc.users.invite,
    ])
    await click(button(tc.users.create))
    await settle(() => dlg() !== null, 'the dialog')
    const tenant = dlg()!.querySelector<HTMLSelectElement>('#create-account-tenant')
    expect(tenant).not.toBeNull()
    await settle(() => tenant!.options.length > 0, 'the tenants')
    expect(tenant!.selectedOptions[0].textContent).toContain('Default')
    const role = dlg()!.querySelector<HTMLSelectElement>('#create-account-role')!
    expect([...role.options].map((o) => o.value)).toEqual(['member', 'admin'])
  })

  it('makes the account, then shows the link once with a copy button, and the list gets the person', async () => {
    const copied = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: copied }, configurable: true })
    await open()
    await click(button(tc.users.create))
    await settle(() => dlg() !== null, 'the dialog')
    await type(input(d.username), 'erin')
    await type(input(d.name), 'Erin')
    await type(input(d.email), 'erin@example.test')
    await type(dlg()!.querySelector<HTMLSelectElement>('#create-account-role')!, 'admin')
    await settle(
      () => dlg()!.querySelector<HTMLSelectElement>('#create-account-tenant')!.options.length > 0,
      'the tenants',
    )
    await click(button(d.submit, dlg()!))
    await settle(() => dlg()!.textContent!.includes(d.linkTitle), 'the link')
    expect(JSON.parse(posts()[0].body!)).toMatchObject({
      username: 'erin',
      name: 'Erin',
      email: 'erin@example.test',
      role: 'admin',
    })
    expect(JSON.parse(posts()[0].body!).tenant).toBeTruthy()
    const link = dlg()!.querySelector<HTMLInputElement>('input[readonly]')!.value
    expect(link).toMatch(/\/reset-password#/)
    expect(dlg()!.textContent).toContain(d.created('Erin'))
    // Mail is off: the administrator is told to hand it over.
    expect(dlg()!.querySelector('[data-slot="created-mail"]')!.textContent).toBe(d.mailNone)
    await click(button(d.copy, dlg()!))
    await settle(() => button(d.copied, dlg()!) !== undefined, 'copied')
    expect(copied).toHaveBeenCalledWith(link)
    await click(button(d.done, dlg()!))
    await settle(() => dlg() === null, 'closed')
    await settle(() => document.body.textContent!.includes('erin'), 'the person in the list')
  })

  it('asks whether an administrator of the default tenant is a site administrator, and always sends the answer', async () => {
    await open()
    await click(button(tc.users.create))
    await settle(() => dlg() !== null, 'the dialog')
    await settle(
      () => dlg()!.querySelector<HTMLSelectElement>('#create-account-tenant')!.options.length > 0,
      'the tenants',
    )
    expect(dlg()!.querySelector('[data-slot="site-admin-choice"]')).toBeNull()
    await type(dlg()!.querySelector<HTMLSelectElement>('#create-account-role')!, 'admin')
    await settle(() => dlg()!.querySelector('[data-slot="site-admin-choice"]') !== null, 'the choice')
    expect(dlg()!.querySelector('[data-slot="site-admin-warning"]')).toBeNull()
    await type(input(d.username), 'erin')
    await click(button(d.submit, dlg()!))
    await settle(() => dlg()!.textContent!.includes(d.linkTitle), 'the link')
    // Not ticked: a plain administrator of the default tenant, said so.
    expect(JSON.parse(posts()[0].body!).siteAdmin).toBe(false)
  })

  it('warns when the site administrator box is ticked, and sends true', async () => {
    await open()
    await click(button(tc.users.create))
    await settle(() => dlg() !== null, 'the dialog')
    await settle(
      () => dlg()!.querySelector<HTMLSelectElement>('#create-account-tenant')!.options.length > 0,
      'the tenants',
    )
    await type(dlg()!.querySelector<HTMLSelectElement>('#create-account-role')!, 'admin')
    await settle(() => dlg()!.querySelector('[data-slot="site-admin-choice"]') !== null, 'the choice')
    await click(dlg()!.querySelector('[data-slot="site-admin-choice"] input'))
    await settle(() => dlg()!.querySelector('[data-slot="site-admin-warning"]') !== null, 'the warning')
    expect(dlg()!.querySelector('[data-slot="site-admin-warning"]')!.textContent).toBe(d.siteAdminWarning)
    await type(input(d.username), 'erin')
    await click(button(d.submit, dlg()!))
    await settle(() => dlg()!.textContent!.includes(d.linkTitle), 'the link')
    expect(JSON.parse(posts()[0].body!).siteAdmin).toBe(true)
  })

  it('says the link was mailed when the server did', async () => {
    await open(undefined, {
      fx: (mm) => mm.fx.reply('POST', '/api/admin/users', mailedAnswer('sent'), { status: 201 }),
    })
    await click(button(tc.users.create))
    await settle(() => dlg() !== null, 'the dialog')
    await type(input(d.username), 'erin')
    await click(button(d.submit, dlg()!))
    await settle(() => dlg()!.querySelector('[data-slot="created-mail"]') !== null, 'the answer')
    expect(dlg()!.querySelector('[data-slot="created-mail"]')!.textContent).toBe(d.mailSent('erin@example.test'))
  })

  it('has no link to show for a person who signs in with the organisation SSO', async () => {
    await open(undefined, {
      fx: (mm) =>
        mm.fx.reply(
          'POST',
          '/api/admin/users',
          { ...mailedAnswer('none'), signIn: 'sso', link: null, expires: null },
          { status: 201 },
        ),
    })
    await click(button(tc.users.create))
    await settle(() => dlg() !== null, 'the dialog')
    await type(input(d.username), 'erin')
    await click(button(d.submit, dlg()!))
    await settle(() => dlg()!.querySelector('[data-slot="created-sso"]') !== null, 'the answer')
    expect(dlg()!.querySelector('[data-slot="created-sso"]')!.textContent).toBe(d.sso)
    expect(dlg()!.querySelector('input[readonly]')).toBeNull()
  })

  it('shows the server refusal in the dialog and keeps what was typed', async () => {
    await open((data) => void addPerson(data, 'erin'))
    await click(button(tc.users.create))
    await settle(() => dlg() !== null, 'the dialog')
    await type(input(d.username), 'erin')
    await click(button(d.submit, dlg()!))
    await settle(() => dlg()!.querySelector('[role="alert"]') !== null, 'the refusal')
    expect(input(d.username).value).toBe('erin')
    expect(dlg()!.textContent).not.toContain(d.linkTitle)
  })

  it('needs a username before it asks the server', async () => {
    await open()
    await click(button(tc.users.create))
    await settle(() => dlg() !== null, 'the dialog')
    expect(button(d.submit, dlg()!).disabled).toBe(true)
    expect(posts()).toHaveLength(0)
  })

  it('inside an organisation the tenant is the scope: no tenant to pick', async () => {
    await open((data) => asTenantAdmin(data))
    await click(button(tc.users.create))
    await settle(() => dlg() !== null, 'the dialog')
    expect(dlg()!.querySelector('#create-account-tenant')).toBeNull()
    await type(input(d.username), 'erin')
    await click(button(d.submit, dlg()!))
    await settle(() => dlg()!.textContent!.includes(d.linkTitle), 'the link')
    expect(JSON.parse(posts()[0].body!).tenant).toBeUndefined()
  })
})

function mailedAnswer(mail: 'sent' | 'none' | 'limited') {
  return {
    user: {
      id: '01FXERIN001',
      username: 'erin',
      name: 'erin',
      email: 'erin@example.test',
      role: 'member',
      disabled: false,
      created: '2026-11-27T08:00:00Z',
      emailVerified: false,
      passkeyRequired: false,
      siteAdmin: false,
    },
    signIn: 'link',
    link: 'https://notes.example.test/reset-password#abc',
    expires: '2026-12-04T08:00:00Z',
    mail,
  }
}
