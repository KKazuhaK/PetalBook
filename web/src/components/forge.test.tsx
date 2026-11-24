// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { adminRequests, click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import { t } from '@/i18n'
import { tf } from '@/i18n/forge'
import { tg } from '@/i18n/github'
import SettingsRoute from '@/components/settings/SettingsRoute'
import { AdminGate } from '@/components/admin/AdminGate'
import { SidebarProvider } from '@/components/ui/sidebar'
import { NewSpaceDialog } from '@/components/layout/NewSpaceDialog'
import { useState } from 'react'

// What the app shows of the hosts with an OAuth application (GitLab, Gitea, Forgejo, Codeberg), against the mock
// server: the administrators' cards, and the account's "connected accounts" row. The words come from the server's
// provider (its name, its capabilities); nothing here depends on a platform's name.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const text = () => document.body.textContent ?? ''
const dialog = () =>
  document.querySelector<HTMLElement>('[role="alertdialog"], [role="dialog"]:not([data-slot="settings-dialog"])')
const button = (label: string, root: ParentNode = document) =>
  [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label)
// The forms of the host cards: the page has other fields of the same names (the App's, the storage rules').
const form = () =>
  document.querySelector<HTMLElement>('[data-testid="forge-add-form"], [data-testid="forge-edit-form"]')!
const field = (label: string) =>
  form().querySelector<HTMLInputElement>(
    `#${[...form().querySelectorAll('label')].find((l) => l.textContent === label)!.getAttribute('for')}`,
  )!
const save = () => button(tf.admin.save, form())
const forge = (body: Record<string, unknown>) => adminRequests(m!.server)('POST', '/api/_mock/forge', body)

const adminRoutes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/:section?" element={<AdminGate />} />
    </Routes>
  </SidebarProvider>
)
const settingsRoutes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
  </Routes>
)
const panel = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')

async function addHost(over: Partial<Record<'id' | 'host' | 'name' | 'id2' | 'secret', string>> = {}) {
  await settle(() => button(tf.admin.add) !== undefined, 'the add button')
  await click(button(tf.admin.add))
  await settle(() => document.querySelector('[data-testid="forge-add-form"]') !== null, 'the form')
  await type(field(tf.admin.host), over.host ?? 'gitlab.acme.example')
  await type(field(tf.admin.id), over.id ?? 'gitlab-acme')
  if (over.name) await type(field(tf.admin.name), over.name)
  await type(field(tf.admin.clientId), 'client-123')
  await type(field(tf.admin.clientSecret), over.secret ?? 'a-secret')
  await click(save())
}

describe('the hosts in the administration', () => {
  it('says there is no host until one is added, and adds one without ever showing its secret again', async () => {
    m = await mount(adminRoutes, { entries: ['/admin/storage'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => text().includes(tf.admin.none), 'the empty list')
    await addHost({ name: 'Acme GitLab' })
    await settle(() => text().includes('Acme GitLab') && text().includes(tf.admin.usage(0, 0)), 'the card')
    const post = m.requests.find((r) => r.method === 'POST' && r.url === '/api/admin/connections')!
    expect(JSON.parse(post.body!)).toEqual({
      id: 'gitlab-acme',
      type: 'gitlab',
      host: 'gitlab.acme.example',
      name: 'Acme GitLab',
      clientId: 'client-123',
      clientSecret: 'a-secret',
    })
    expect(text()).not.toContain('a-secret')
    expect(text()).toContain('/api/connections/gitlab-acme/authorization/callback')
    expect(text()).toContain(tf.admin.partialUnknown)
    expect(text()).toContain(tf.admin.scopes(['api']))
    expect(text()).not.toContain(tf.admin.none)
  })

  it('offers the two kinds of host and sends the one chosen', async () => {
    m = await mount(adminRoutes, { entries: ['/admin/storage'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => button(tf.admin.add) !== undefined, 'the add button')
    await click(button(tf.admin.add))
    await settle(() => document.querySelector('#forge-type') !== null, 'the kind')
    const select = document.querySelector<HTMLSelectElement>('#forge-type')!
    expect([...select.options].map((o) => o.value)).toEqual(['gitlab', 'gitea'])
    await type(select, 'gitea')
    await type(field(tf.admin.host), 'codeberg.org')
    await type(field(tf.admin.id), 'codeberg')
    await type(field(tf.admin.clientId), 'c')
    await type(field(tf.admin.clientSecret), 's')
    await click(save())
    await settle(() => text().includes('codeberg.org') && text().includes(tf.admin.scopes([])), 'the card')
    const post = m.requests.find((r) => r.method === 'POST' && r.url === '/api/admin/connections')!
    expect(JSON.parse(post.body!)).toMatchObject({ id: 'codeberg', type: 'gitea', host: 'codeberg.org' })
  })

  it('asks for what is missing, and says why the host refused in its own words', async () => {
    m = await mount(adminRoutes, { entries: ['/admin/storage'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => button(tf.admin.add) !== undefined, 'the add button')
    await click(button(tf.admin.add))
    await settle(() => document.querySelector('[data-testid="forge-add-form"]') !== null, 'the form')
    await click(save())
    await settle(() => text().includes(tf.admin.needFields), 'the hint')
    expect(m.requests.some((r) => r.method === 'POST' && r.url === '/api/admin/connections')).toBe(false)
    await type(field(tf.admin.host), 'gitlab.acme.example')
    await type(field(tf.admin.id), 'x')
    await type(field(tf.admin.clientId), 'c')
    await type(field(tf.admin.clientSecret), 'wrong')
    await click(save())
    await settle(() => text().includes(tf.admin.errors.reasons.client_rejected), 'the client refusal')
    await type(field(tf.admin.host), 'private.example')
    await type(field(tf.admin.clientSecret), 'right')
    await click(save())
    await settle(() => text().includes(tf.admin.errors.reasons.host_not_public), 'the public-address refusal')
    await type(field(tf.admin.host), 'gitlab.down.example')
    await click(save())
    await settle(() => text().includes(tf.admin.errors.reasons.host_unreachable), 'the unreachable host')
    // The form is still there with what was typed, and nothing was saved.
    expect(field(tf.admin.id).value).toBe('x')
    expect(text()).not.toContain(tf.admin.usage(0, 0))
  })

  it('edits a host: the name and a new secret, and a refused secret is not kept', async () => {
    m = await mount(adminRoutes, { entries: ['/admin/storage'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => button(tf.admin.add) !== undefined, 'the add button')
    await addHost({ name: 'Acme GitLab' })
    await settle(() => button(tf.admin.edit) !== undefined, 'the card')
    await click(button(tf.admin.edit))
    await type(field(tf.admin.name), 'Renamed')
    await type(field(tf.admin.clientSecret), 'wrong')
    await click(save())
    await settle(() => text().includes(tf.admin.errors.reasons.client_rejected), 'the refusal')
    await type(field(tf.admin.clientSecret), 'a-new-secret')
    await click(save())
    await settle(() => text().includes('Renamed') && button(tf.admin.edit) !== undefined, 'the saved card')
    const patches = m.requests.filter((r) => r.method === 'PATCH' && r.url === '/api/admin/connections/gitlab-acme')
    expect(patches.map((r) => JSON.parse(r.body!))).toEqual([
      { name: 'Renamed', clientSecret: 'wrong' },
      { name: 'Renamed', clientSecret: 'a-new-secret' },
    ])
    expect(text()).not.toContain('a-new-secret')
  })

  it('checks a host again and removes it, but not while people are connected', async () => {
    m = await mount(adminRoutes, { entries: ['/admin/storage'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => button(tf.admin.add) !== undefined, 'the add button')
    await addHost()
    await settle(() => button(tf.admin.check) !== undefined, 'the card')
    await click(button(tf.admin.check))
    await settle(
      () => m!.requests.some((r) => r.method === 'POST' && r.url === '/api/admin/connections/gitlab-acme/check'),
      'the check',
    )
    // Someone connects: the host cannot be removed.
    forge({ provider: 'gitlab-acme', connect: 'tang-fg' })
    await m.qc.invalidateQueries()
    await settle(() => text().includes(tf.admin.usage(0, 1)), 'the connection counted')
    await click(button(tf.admin.remove))
    await settle(() => dialog() !== null, 'the dialog')
    expect(dialog()!.textContent).toContain(tf.admin.removeBlocked(0, 1))
    expect(button(tf.admin.remove, dialog()!)!.disabled).toBe(true)
    await click(button(tf.admin.cancel, dialog()!))
    await settle(() => dialog() === null, 'the dialog closing')
    // When nobody is connected it goes.
    adminRequests(m.server)('DELETE', '/api/connections/gitlab-acme/authorization')
    await m.qc.invalidateQueries()
    await settle(() => text().includes(tf.admin.usage(0, 0)), 'the count back to none')
  })
})

describe('the connected accounts of the account page', () => {
  async function open(entry = '/settings/connections') {
    m = await mount(settingsRoutes, { signIn: true, server: { mode: 'accounts' }, entries: [entry] })
    adminRequests(m.server)('POST', '/api/admin/connections', {
      id: 'gitlab-acme',
      type: 'gitlab',
      host: 'gitlab.acme.example',
      name: 'Acme GitLab',
      clientId: 'c',
      clientSecret: 's',
    })
    await m.qc.invalidateQueries()
    await settle(() => panel() !== null, 'the settings')
  }

  it('has a row for the host, in its own name, with one button to connect and no installation words', async () => {
    await open()
    await settle(() => panel()!.textContent!.includes('Acme GitLab'), 'the row')
    expect(panel()!.textContent).toContain(tf.account.notConnected('Acme GitLab'))
    expect(button(tf.account.connect('Acme GitLab'), panel()!)).toBeTruthy()
    expect(button(tg.account.authorizeOnly, panel()!)).toBeUndefined()
    expect(panel()!.textContent).not.toContain('GitHub')
    expect(panel()!.textContent).not.toContain('App')
  })

  it('starts connecting without the installation flag, and says where to come back to', async () => {
    await open()
    await settle(() => button(tf.account.connect('Acme GitLab'), panel()!) !== undefined, 'the button')
    await click(button(tf.account.connect('Acme GitLab'), panel()!))
    await settle(
      () => m!.requests.some((r) => r.url === '/api/connections/gitlab-acme/authorization/start'),
      'the request',
    )
    const req = m!.requests.find((r) => r.url === '/api/connections/gitlab-acme/authorization/start')!
    // No installation page to go through: the flag is left out.
    expect(JSON.parse(req.body!)).toEqual({ return: '/settings/connections' })
  })

  it('shows a connection and disconnects it, with the host named in the words', async () => {
    await open()
    forge({ provider: 'gitlab-acme', connect: 'tang-fg' })
    await m!.qc.invalidateQueries()
    await settle(() => panel()!.textContent!.includes('tang-fg'), 'the connection')
    expect(panel()!.textContent).toContain(tf.account.accountHelp('Acme GitLab'))
    expect(panel()!.textContent).not.toContain(tg.account.needsInstallBody)
    await click(button(tg.account.disconnect, panel()!))
    await settle(() => dialog() !== null, 'the confirmation')
    expect(dialog()!.textContent).toContain(tf.account.disconnectTitle('Acme GitLab'))
    await click(button(tg.account.disconnectConfirm, dialog()!))
    await settle(
      () => m!.requests.some((r) => r.method === 'DELETE' && r.url === '/api/connections/gitlab-acme/authorization'),
      'the request',
    )
  })

  it('shows an expired connection with the words of its host and offers to connect again', async () => {
    await open()
    forge({ provider: 'gitlab-acme', connect: 'tang-fg' })
    forge({ provider: 'gitlab-acme', expire: true })
    await m!.qc.invalidateQueries()
    await settle(() => panel()!.textContent!.includes(tf.account.expiredBody('Acme GitLab')), 'the notice')
    expect(button(tg.account.reconnect, panel()!)).toBeTruthy()
  })

  it('explains a failed attempt in the words of the host it was for, once', async () => {
    await open('/settings/connections?provider=gitlab-acme&connection_error=account_in_use')
    await settle(
      () => panel()!.textContent!.includes(tf.account.errors.account_in_use('Acme GitLab')),
      'the explanation',
    )
    expect(panel()!.textContent).toContain(tf.account.failed('Acme GitLab'))
    expect(panel()!.textContent).not.toContain('GitHub')
  })

  it('offers the host as a place to store a new space, by what it can do', async () => {
    function Harness() {
      const [open, setOpen] = useState(true)
      return <NewSpaceDialog open={open} onOpenChange={setOpen} onCreated={() => {}} />
    }
    m = await mount(<Harness />, { signIn: true, server: { mode: 'accounts' }, entries: ['/'] })
    adminRequests(m.server)('POST', '/api/admin/connections', {
      id: 'gitlab-acme',
      type: 'gitlab',
      host: 'gitlab.acme.example',
      name: 'Acme GitLab',
      clientId: 'c',
      clientSecret: 's',
    })
    await m.qc.invalidateQueries()
    await settle(() => text().includes(tg.newSpace.whereSync('Acme GitLab')), 'the option')
    expect(text()).not.toContain(tg.newSpace.whereSync('GitHub'))
  })
})
