// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { useState } from 'react'
import type { SpaceInfo } from '@/api/types'
import { adminRequests, byLabel, click, installDomShims, locationText, makePerson, mount, settle, type, type Mounted } from '@/testing/harness'
import { keys } from '@/api/hooks'
import { MOCK_ADMIN } from '@/api/mock/server'
import { t } from '@/i18n'
import { tg } from '@/i18n/github'
import { ta } from '@/i18n/admin'
import SettingsRoute from '@/components/settings/SettingsRoute'
import { AdminGate } from '@/components/admin/AdminGate'
import { SidebarProvider } from '@/components/ui/sidebar'
import { NewSpaceDialog } from '@/components/layout/NewSpaceDialog'

// What the app shows of GitHub, against the mock server: the new-space dialog, the account's
// connection, a space's remote settings and the administrators' App.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const text = () => document.body.textContent ?? ''
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')
const button = (label: string, root: ParentNode = document) => [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label)
const posts = () => m!.requests.filter((r) => r.method === 'POST' && r.url === '/api/spaces')
const hook = (body: Record<string, unknown>) => adminRequests(m!.server)('POST', '/api/_mock/github', body)

function Harness({ onCreated }: { onCreated: (s: SpaceInfo) => void }) {
  const [open, setOpen] = useState(true)
  return <NewSpaceDialog open={open} onOpenChange={setOpen} onCreated={onCreated} />
}

async function openNewSpace(github: 'off' | 'app' | 'connected') {
  const created: SpaceInfo[] = []
  m = await mount(<Harness onCreated={(s) => created.push(s)} />, {
    signIn: true,
    server: { mode: 'accounts', github: { configured: github !== 'off' } },
    entries: ['/'],
  })
  if (github === 'connected') {
    hook({ connect: 'tang-gh' })
    await m.qc.invalidateQueries()
  }
  await settle(() => dialog() !== null, 'the dialog')
  return created
}
const nameField = () => dialog()!.querySelector<HTMLInputElement>('input[autocomplete="off"]')!
const checkbox = (label: string) => [...dialog()!.querySelectorAll('label')].find((l) => l.textContent?.includes(label))!.querySelector<HTMLInputElement>('input[type="checkbox"]')!

describe('the new-space dialog', () => {
  it('offers no repository while the server has no GitHub App', async () => {
    await openNewSpace('off')
    expect(text()).not.toContain(tg.newSpace.option)
    await type(nameField(), 'Plain')
    await click(button(t.spaces.create))
    await settle(() => posts().length === 1, 'the request')
    expect(JSON.parse(posts()[0].body!)).toEqual({ name: 'Plain', inbox: t.inbox.folderName })
  })

  it('asks a person who has not connected GitHub to do so first', async () => {
    await openNewSpace('app')
    await settle(() => text().includes(tg.newSpace.option), 'the option')
    await click(checkbox(tg.newSpace.option))
    await settle(() => text().includes(tg.newSpace.ownerNone), 'the hint')
    expect(button(tg.newSpace.connect)).toBeTruthy()
  })

  it('makes a space with a repository: the owner, a name taken from the space, private by default', async () => {
    const created = await openNewSpace('connected')
    await settle(() => text().includes(tg.newSpace.option), 'the option')
    await type(nameField(), 'Tang Notes')
    await click(checkbox(tg.newSpace.option))
    await settle(() => dialog()!.querySelector('select') !== null, 'the owners')
    const owners = [...dialog()!.querySelectorAll('select option')].map((o) => o.getAttribute('value'))
    expect(owners).toEqual(['tang-gh', 'kazuhahub', 'tang-poetry'])
    // The repository name follows the space name until it is edited.
    const repo = () => dialog()!.querySelector<HTMLInputElement>('input[spellcheck="false"]')!
    expect(repo().value).toBe('Tang-Notes')
    await type(dialog()!.querySelector('select')!, 'kazuhahub')
    await type(repo(), 'tang-notes')
    expect(checkbox(tg.newSpace.private).checked).toBe(true)
    await type(dialog()!.querySelector('textarea')!, 'Notes on Tang poetry')
    await click(button(t.spaces.create))
    await settle(() => created.length === 1, 'the space')
    expect(JSON.parse(posts()[0].body!)).toEqual({ name: 'Tang Notes', inbox: t.inbox.folderName, github: { owner: 'kazuhahub', name: 'tang-notes', private: true, description: 'Notes on Tang poetry', create: true } })
    expect(created[0].sync).toBe('ok')
    await settle(() => dialog() === null, 'the dialog closing')
  })

  it('checks the repository name before asking', async () => {
    await openNewSpace('connected')
    await settle(() => text().includes(tg.newSpace.option), 'the option')
    await type(nameField(), 'X')
    await click(checkbox(tg.newSpace.option))
    await settle(() => dialog()!.querySelector('select') !== null, 'the owners')
    await type(dialog()!.querySelector<HTMLInputElement>('input[spellcheck="false"]')!, 'bad name.git')
    await click(button(t.spaces.create))
    await settle(() => text().includes(tg.newSpace.repoInvalid), 'the message')
    expect(posts()).toHaveLength(0)
  })

  it('offers the existing repository when the name is taken, and uses it without creating', async () => {
    const created = await openNewSpace('connected')
    await settle(() => text().includes(tg.newSpace.option), 'the option')
    await type(nameField(), 'Wiki')
    await click(checkbox(tg.newSpace.option))
    await settle(() => dialog()!.querySelector('select') !== null, 'the owners')
    await type(dialog()!.querySelector('select')!, 'kazuhahub')
    await type(dialog()!.querySelector<HTMLInputElement>('input[spellcheck="false"]')!, 'wiki')
    await click(button(t.spaces.create))
    await settle(() => text().includes(tg.newSpace.errors.nameTaken), 'the refusal')
    expect(created).toHaveLength(0)
    await click(button(tg.newSpace.useExisting))
    await settle(() => created.length === 1, 'the space')
    expect(JSON.parse(posts()[1].body!).github).toMatchObject({ owner: 'kazuhahub', name: 'wiki', create: false })
  })

  it('names the empty repository a failed push left, and connects to it on a second try', async () => {
    const created = await openNewSpace('connected')
    await settle(() => text().includes(tg.newSpace.option), 'the option')
    hook({ trap: 'pushFails' })
    await type(nameField(), 'Slow')
    await click(checkbox(tg.newSpace.option))
    await settle(() => dialog()!.querySelector('select') !== null, 'the owners')
    await type(dialog()!.querySelector('select')!, 'kazuhahub')
    await type(dialog()!.querySelector<HTMLInputElement>('input[spellcheck="false"]')!, 'slow')
    await click(button(t.spaces.create))
    await settle(() => text().includes(tg.newSpace.leftBehind('kazuhahub/slow')), 'the empty repository')
    expect(text()).toContain(tg.newSpace.errors.pushFailed)
    hook({ trap: 'pushFails', on: false })
    await click(button(tg.newSpace.useLeft))
    await settle(() => created.length === 1, 'the space')
    expect(JSON.parse(posts()[1].body!).github).toMatchObject({ owner: 'kazuhahub', name: 'slow', create: false })
  })

  it('shows GitHub\'s refusal in its own words and keeps the form', async () => {
    const created = await openNewSpace('connected')
    await settle(() => text().includes(tg.newSpace.option), 'the option')
    hook({ trap: 'noPermission' })
    await type(nameField(), 'No')
    await click(checkbox(tg.newSpace.option))
    await settle(() => dialog()!.querySelector('select') !== null, 'the owners')
    await click(button(t.spaces.create))
    await settle(() => text().includes('GitHub 拒绝建仓库'), 'the refusal')
    expect(created).toHaveLength(0)
    expect(nameField().value).toBe('No')
  })

  it('marks the own account that needs the App, blocks creating there and installs in one step', async () => {
    await openNewSpace('connected')
    await settle(() => text().includes(tg.newSpace.option), 'the option')
    hook({ trap: 'personalNeedsInstall' })
    await m!.qc.invalidateQueries()
    await type(nameField(), 'Mine')
    await click(checkbox(tg.newSpace.option))
    await settle(() => dialog()!.querySelector('select') !== null, 'the owners')
    await settle(() => text().includes(tg.newSpace.needsInstall), 'the mark')
    expect(text()).toContain(tg.newSpace.needsInstallHelp('tang-gh'))
    // The other accounts are not marked.
    const marked = [...dialog()!.querySelectorAll('select option')].filter((o) => o.textContent!.includes(tg.newSpace.needsInstall))
    expect(marked).toHaveLength(1)
    await click(button(t.spaces.create))
    await settle(() => text().includes(tg.newSpace.errors.notInstalled('tang-gh')), 'the refusal')
    expect(posts()).toHaveLength(0)
    await click(button(tg.newSpace.installOnMine))
    await settle(() => m!.requests.some((r) => r.url === '/api/github/authorization/start'), 'the request')
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/github/authorization/start')!.body!)).toEqual({ return: '/', install: true })
  })

  it('names the permission the App lacks instead of GitHub\'s refusal', async () => {
    await openNewSpace('connected')
    await settle(() => text().includes(tg.newSpace.option), 'the option')
    hook({ trap: 'missingPermission' })
    await type(nameField(), 'Perm')
    await click(checkbox(tg.newSpace.option))
    await settle(() => dialog()!.querySelector('select') !== null, 'the owners')
    await click(button(t.spaces.create))
    await settle(() => text().includes(tg.newSpace.errors.missingPermission(['administration'])), 'the message')
  })

  it('fits a phone: one column, fields that may shrink, long names that break', async () => {
    await openNewSpace('connected')
    await settle(() => text().includes(tg.newSpace.option), 'the option')
    await click(checkbox(tg.newSpace.option))
    await settle(() => dialog()!.querySelector('select') !== null, 'the owners')
    const form = dialog()!.querySelector('form')!
    expect(form.className).toContain('grid-cols-1')
    expect(dialog()!.querySelector('fieldset')!.className).toContain('min-w-0')
    expect(dialog()!.querySelector<HTMLInputElement>('input[spellcheck="false"]')!.className).toContain('min-w-0')
    expect(dialog()!.querySelector('textarea')!.className).toMatch(/w-full min-w-0/)
  })
})

// --- the account's connection ---------------------------------------------------------

const settingsRoutes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
  </Routes>
)
const panel = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')

async function openSettings(github: 'off' | 'app' | 'connected' | 'expired', entry = '/settings/github', extra: { userAuth?: boolean } = {}) {
  void extra
  m = await mount(settingsRoutes, { signIn: true, server: { mode: 'accounts', github: { configured: github !== 'off' } }, entries: [entry] })
  if (github === 'connected' || github === 'expired') {
    hook({ connect: 'tang-gh' })
    if (github === 'expired') hook({ expire: true })
    await m.qc.invalidateQueries()
  }
  await settle(() => panel() !== null, 'the settings')
}

describe('the GitHub section of the account', () => {
  it('says so when the server has no App, and where an administrator sets it up', async () => {
    await openSettings('off')
    await settle(() => panel()!.textContent!.includes(tg.account.notConfigured), 'the note')
    expect(panel()!.textContent).toContain(tg.account.notConfiguredAdmin)
  })

  it('starts connecting: asks the server for the address and says where to come back to', async () => {
    await openSettings('app')
    await settle(() => button(tg.account.connect, panel()!) !== undefined, 'the button')
    await click(button(tg.account.connect, panel()!))
    await settle(() => m!.requests.some((r) => r.url === '/api/github/authorization/start'), 'the request')
    const req = m!.requests.find((r) => r.url === '/api/github/authorization/start')!
    // Connecting goes through the App's installation page, which authorises in the same flow.
    expect(JSON.parse(req.body!)).toEqual({ return: '/settings/github', install: true })
  })

  it('can authorize alone when the App is installed already', async () => {
    await openSettings('app')
    await settle(() => button(tg.account.authorizeOnly, panel()!) !== undefined, 'the button')
    await click(button(tg.account.authorizeOnly, panel()!))
    await settle(() => m!.requests.some((r) => r.url === '/api/github/authorization/start'), 'the request')
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/github/authorization/start')!.body!)).toEqual({ return: '/settings/github' })
  })

  it('offers to install the App on the person\'s own account when it is not there, and connects in the same step', async () => {
    await openSettings('connected')
    hook({ trap: 'personalNeedsInstall' })
    await m!.qc.invalidateQueries()
    await settle(() => panel()!.textContent!.includes(tg.account.needsInstallTitle('tang-gh')), 'the notice')
    expect(panel()!.textContent).toContain(tg.account.needsInstallBody)
    await click(button(tg.account.installOnMine, panel()!))
    await settle(() => m!.requests.some((r) => r.url === '/api/github/authorization/start'), 'the request')
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/github/authorization/start')!.body!)).toEqual({ return: '/settings/github', install: true })
  })

  it('says nothing about installing when the App is on the account', async () => {
    await openSettings('connected')
    await settle(() => panel()!.textContent!.includes('tang-gh'), 'the account')
    expect(panel()!.textContent).not.toContain(tg.account.needsInstallBody)
  })

  it('explains an installation that sent no code, and one that waits for approval', async () => {
    await openSettings('app', '/settings?github_error=installed')
    await settle(() => panel()!.textContent!.includes(tg.account.errors.installed), 'the explanation')
    await m!.unmount()
    m = undefined
    await openSettings('app', '/settings?github_error=requested')
    await settle(() => panel()!.textContent!.includes(tg.account.errors.requested), 'the explanation')
  })

  it('shows a connection, and warns how many spaces use it before disconnecting', async () => {
    await openSettings('connected')
    await settle(() => panel()!.textContent!.includes('tang-gh'), 'the account')
    expect(panel()!.textContent).toContain(tg.account.stateOk)
    // A space of the person's syncs through the connection.
    const space = (adminRequests(m!.server)('POST', '/api/spaces', { name: 'Via me', github: { owner: 'tang-gh', name: 'journal-x' } }).body as { id: string }).id
    expect(space).toBeTruthy()
    await m!.qc.invalidateQueries()
    await settle(() => panel()!.textContent!.includes(tg.account.spaces(1)), 'the count')
    await click(button(tg.account.disconnect, panel()!))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(tg.account.disconnectWarn(1))
    await click([...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === tg.account.disconnectConfirm))
    await settle(() => m!.requests.some((r) => r.method === 'DELETE' && r.url === '/api/github/authorization'), 'the request')
    await settle(() => button(tg.account.connect, panel()!) !== undefined, 'not connected any more')
  })

  it('shows an expired connection and offers to connect again', async () => {
    await openSettings('expired')
    await settle(() => panel()!.textContent!.includes(tg.account.expiredTitle), 'the notice')
    expect(button(tg.account.reconnect, panel()!)).toBeTruthy()
  })

  it('explains a failed connection once, from the address GitHub sent the browser back to', async () => {
    await openSettings('app', '/settings?github_error=account_in_use')
    await settle(() => panel()!.textContent!.includes(tg.account.errors.account_in_use), 'the explanation')
    expect([...panel()!.querySelectorAll('h2')].map((h) => h.textContent)).toContain(t.settingsUi.sections.github)
    await settle(() => !locationText().includes('github_error'), 'the address cleaned')
    expect(locationText()).toBe('/settings/github')
    // A code nobody made is not a message.
    await m!.unmount()
    m = undefined
    await openSettings('app', '/settings/github?github_error=constructor')
    await settle(() => panel()!.textContent!.includes(tg.account.errors.unknown), 'the generic message')
  })
})

// --- a space's remote ------------------------------------------------------------------

// A space whose repository was made but could not be used: the person is left a reminder.
function leaveARepository(name: string) {
  hook({ trap: 'pushFails', on: true })
  const r = adminRequests(m!.server)
  expect(() => r('POST', '/api/spaces', { name: 'Poems', github: { owner: 'tang-gh', name } })).toThrow(/github_push_failed/)
  hook({ trap: 'pushFails', on: false })
}

describe('the reminder of an empty repository a failed space left on GitHub', () => {
  it('lists it with its name and link, and connects a space to it when asked to try again', async () => {
    await openSettings('connected')
    await settle(() => panel()!.textContent!.includes('tang-gh'), 'the account')
    expect(panel()!.textContent).not.toContain(tg.account.leftoverTitle)
    leaveARepository('poems-repo')
    await m!.qc.invalidateQueries()
    await settle(() => panel()!.textContent!.includes(tg.account.leftoverTitle), 'the reminder')
    expect(panel()!.textContent).toContain('tang-gh/poems-repo')
    expect(panel()!.textContent).toContain('Poems')
    const link = [...panel()!.querySelectorAll<HTMLAnchorElement>('a')].find((x) => x.textContent?.includes('tang-gh/poems-repo'))!
    expect(link.getAttribute('href')).toBe('https://github.com/tang-gh/poems-repo')
    expect(link.rel).toContain('noopener')
    await click(button(tg.account.leftoverRetry, panel()!))
    await settle(() => !panel()!.textContent!.includes(tg.account.leftoverTitle), 'the reminder going')
    const post = m!.requests.filter((r) => r.method === 'POST' && r.url === '/api/spaces').at(-1)!
    expect(JSON.parse(post.body!)).toEqual({ name: 'Poems', github: { owner: 'tang-gh', name: 'poems-repo', create: false } })
    // The space is there, on the repository.
    const spaces = (adminRequests(m!.server)('GET', '/api/spaces').body as { spaces: { name: string }[] }).spaces
    expect(spaces.filter((x) => x.name === 'Poems')).toHaveLength(1)
  })

  it('keeps the reminder and says why when trying again fails', async () => {
    await openSettings('connected')
    await settle(() => panel()!.textContent!.includes('tang-gh'), 'the account')
    leaveARepository('stuck-repo')
    await m!.qc.invalidateQueries()
    await settle(() => button(tg.account.leftoverRetry, panel()!) !== undefined, 'the reminder')
    hook({ expire: true })
    await click(button(tg.account.leftoverRetry, panel()!))
    await settle(() => m!.requests.filter((r) => r.method === 'POST' && r.url === '/api/spaces').length === 1, 'the try')
    await settle(() => document.body.textContent!.includes(tg.account.leftoverFailed), 'the failure')
    expect(panel()!.textContent).toContain('tang-gh/stuck-repo')
  })

  it('forgets it when the person says they deleted the repository', async () => {
    await openSettings('connected')
    await settle(() => panel()!.textContent!.includes('tang-gh'), 'the account')
    leaveARepository('gone-repo')
    await m!.qc.invalidateQueries()
    await settle(() => button(tg.account.leftoverDismiss, panel()!) !== undefined, 'the reminder')
    await click(button(tg.account.leftoverDismiss, panel()!))
    await settle(() => !panel()!.textContent!.includes(tg.account.leftoverTitle), 'the reminder going')
    expect(m!.requests.some((r) => r.method === 'DELETE' && /^\/api\/github\/leftovers\/\d+$/.test(r.url))).toBe(true)
    const spaces = (adminRequests(m!.server)('GET', '/api/spaces').body as { spaces: { name: string }[] }).spaces
    expect(spaces.some((x) => x.name === 'Poems')).toBe(false)
  })
})

describe('the sync settings of a space', () => {
  async function openSync(github: 'connected' | 'app') {
    m = await mount(settingsRoutes, {
      signIn: true,
      server: { mode: 'accounts', github: { configured: true } },
      entries: (server) => {
        const space = (adminRequests(server)('POST', '/api/spaces', { name: 'Wiki' }).body as { id: string }).id
        localStorage.setItem('pb.space', space)
        if (github === 'connected') adminRequests(server)('POST', '/api/_mock/github', { connect: 'tang-gh' })
        return ['/settings/sync']
      },
    })
    await settle(() => panel()?.textContent?.includes(t.remote.intro) === true, 'the sync settings')
    await settle(() => [...panel()!.querySelectorAll('label')].some((l) => l.textContent?.includes(tg.sync.github)), 'the GitHub choice')
  }
  const radio = (label: string) => [...panel()!.querySelectorAll('label')].find((l) => l.textContent?.includes(label))!.querySelector<HTMLInputElement>('input[type="radio"]')!

  it('can switch to the App: only the address and which access, and the PUT carries no secret', async () => {
    await openSync('connected')
    await click(radio(tg.sync.github))
    await settle(() => panel()!.textContent!.includes(tg.sync.viaApp), 'the choices')
    const url = panel()!.querySelector<HTMLInputElement>('input[inputmode="url"]')!
    await type(url, 'kazuhahub/wiki')
    await click(button(t.remote.connect, panel()!))
    await settle(() => m!.requests.some((r) => r.method === 'PUT' && r.url.endsWith('/remote')), 'the request')
    const put = m!.requests.find((r) => r.method === 'PUT' && r.url.endsWith('/remote'))!
    expect(JSON.parse(put.body!)).toEqual({ url: 'https://github.com/kazuhahub/wiki.git', credential: { type: 'github', via: 'app' } })
    await settle(() => panel()!.textContent!.includes(tg.sync.viaAppOf('kazuhahub')), 'the connected remote')
    expect(panel()!.textContent).not.toMatch(/ghs_|ghu_|token/i)
  })

  it('can switch to the user\'s own connection, and chooses a repository from a list', async () => {
    await openSync('connected')
    await click(radio(tg.sync.github))
    await settle(() => panel()!.textContent!.includes(tg.sync.viaUser), 'the choices')
    await click(radio(tg.sync.viaUser))
    await click(button(tg.sync.showPicker, panel()!))
    await settle(() => panel()!.querySelectorAll('ul button').length > 0, 'the repositories')
    const first = panel()!.querySelector<HTMLButtonElement>('ul button')!
    const full = first.firstChild!.textContent!
    await click(first)
    expect(panel()!.querySelector<HTMLInputElement>('input[inputmode="url"]')!.value).toBe(full)
    await click(button(t.remote.connect, panel()!))
    await settle(() => m!.requests.some((r) => r.method === 'PUT'), 'the request')
    expect(JSON.parse(m!.requests.find((r) => r.method === 'PUT')!.body!).credential).toEqual({ type: 'github', via: 'user' })
  })

  it('names the GitHub problem of a space in error and offers what fixes it', async () => {
    await openSync('connected')
    const space = localStorage.getItem('pb.space')!
    const admin = adminRequests(m!.server)
    admin('PUT', `/api/spaces/${space}/remote`, { url: 'https://github.com/tang-gh/journal.git', credential: { type: 'github', via: 'user' } })
    admin('POST', '/api/_mock/github', { expire: true })
    await m!.qc.invalidateQueries()
    await settle(() => panel()!.textContent!.includes(tg.sync.codes.github_authorization_expired.title), 'the notice')
    expect(panel()!.textContent).toContain(tg.sync.keptSafe)
    expect(button(tg.sync.reconnect, panel()!)).toBeTruthy()
  })
})

// --- the administrators' App -------------------------------------------------------------

describe('the GitHub section of the administration', () => {
  const routes = (
    <SidebarProvider>
      <Routes>
        <Route path="/admin/:section?" element={<AdminGate />} />
      </Routes>
    </SidebarProvider>
  )
  const field = (label: string) => document.querySelector<HTMLInputElement | HTMLTextAreaElement>(`#${[...document.querySelectorAll('label')].find((l) => l.textContent === label)!.getAttribute('for')}`)!

  it('saves the App, shows only what is safe to show, and keeps the key when it is left empty', async () => {
    m = await mount(routes, { entries: ['/admin/github'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => document.querySelector('#github-private-key') !== null && [...document.querySelectorAll('label')].some((l) => l.textContent === tg.admin.appId), 'the form')
    await type(field(tg.admin.appId), '1234567')
    await type(field(tg.admin.privateKey), '-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----')
    await type(field(tg.admin.clientId), 'Iv23liMockClientId')
    await type(field(tg.admin.clientSecret), 'a-secret')
    await click(button(tg.admin.save))
    await settle(() => text().includes(tg.admin.fingerprint), 'the saved App')
    expect(text()).not.toContain('a-secret')
    expect(text()).not.toContain('BEGIN RSA')
    expect(field(tg.admin.privateKey).value).toBe('')
    expect(text()).toContain(tg.admin.privateKeyKept)
    // The second save sends no key and no secret.
    await click(button(tg.admin.save))
    await settle(() => m!.requests.filter((r) => r.method === 'PUT' && r.url === '/api/admin/github').length === 2, 'the second save')
    const second = JSON.parse(m!.requests.filter((r) => r.method === 'PUT' && r.url === '/api/admin/github')[1].body!)
    expect(second).toEqual({ appId: '1234567', clientId: 'Iv23liMockClientId' })
    expect(text()).toContain(ta.sections.github)
  })

  it('says why GitHub refused the key or the secret', async () => {
    m = await mount(routes, { entries: ['/admin/github'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => document.querySelector('#github-private-key') !== null && [...document.querySelectorAll('label')].some((l) => l.textContent === tg.admin.appId), 'the form')
    await type(field(tg.admin.appId), '1234567')
    await type(field(tg.admin.privateKey), 'nonsense')
    await click(button(tg.admin.save))
    await settle(() => text().includes(tg.admin.errors.codes.github_key_invalid), 'the key refusal')
    await type(field(tg.admin.privateKey), '-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----')
    await type(field(tg.admin.clientId), 'Iv23liMockClientId')
    await type(field(tg.admin.clientSecret), 'wrong')
    await click(button(tg.admin.save))
    await settle(() => text().includes(tg.admin.errors.codes.github_client_rejected), 'the secret refusal')
  })

  it('lists the installations and shares an organisation\'s with the tenants picked', async () => {
    m = await mount(routes, { entries: ['/admin/github'], signIn: true, server: { mode: 'accounts', github: { configured: true } } })
    const admin = adminRequests(m.server)
    await settle(() => document.querySelectorAll('li [data-testid="share-choose"]').length === 3, 'the installations')
    // The rows have cached the tenants (just the default one); a tenant made after that
    // must still be offered when the dialog opens.
    await settle(() => m!.qc.getQueryData(keys.tenants) !== undefined, 'the tenants cached by the rows')
    admin('PUT', '/api/admin/tenancy', { enabled: true })
    const second = (admin('POST', '/api/admin/tenants', { slug: 'acme', name: 'Acme' }).body as { id: string }).id
    const choose = (login: string) => byLabel(tg.admin.chooseFor(login))!
    expect(text()).toContain(tg.admin.notShared)
    await click(choose('kazuhahub'))
    await settle(() => dialog() !== null && dialog()!.querySelectorAll('input[type="checkbox"]').length === 2, 'the tenants to pick from')
    const box = (name: string) => [...dialog()!.querySelectorAll('label')].find((l) => l.textContent?.includes(name))!.querySelector<HTMLInputElement>('input[type="checkbox"]')!
    expect(box('Acme').checked).toBe(false)
    await click(box('Acme'))
    await click(button(tg.admin.shareSave, dialog()!)!)
    await settle(() => dialog() === null, 'the dialog closing')
    expect(m.requests.some((r) => r.method === 'PUT' && r.url === '/api/admin/github/installations/5001' && r.body === JSON.stringify({ tenants: [second] }))).toBe(true)
    await settle(() => text().includes(tg.admin.sharedWith('Acme')), 'the row naming the tenant')
    // A member of the default tenant cannot use it; one of a tenant it is shared with could.
    const person = makePerson(m.server, 'carol')
    expect((person.request('GET', '/api/github/owners').body as { items: unknown[] }).items).toEqual([])
    // Share it with the default tenant as well: carol's owner list shows it.
    await click(choose('kazuhahub'))
    await settle(() => dialog() !== null && dialog()!.querySelectorAll('input[type="checkbox"]').length === 2, 'the tenants again')
    expect(box('Acme').checked).toBe(true)
    await click(box('Default'))
    await click(button(tg.admin.shareSave, dialog()!)!)
    await settle(() => dialog() === null, 'the dialog closing again')
    const owners = person.request('GET', '/api/github/owners').body as { items: { login: string; via: string }[] }
    expect(owners.items).toEqual([{ login: 'kazuhahub', type: 'Organization', via: 'app', installationId: 5001, needsInstall: false }])
    expect(MOCK_ADMIN.username).toBeTruthy()
  })

  it('refuses to remove the App while a space syncs through it', async () => {
    m = await mount(routes, { entries: ['/admin/github'], signIn: true, server: { mode: 'accounts', github: { configured: true } } })
    await settle(() => text().includes(tg.admin.title), 'the section')
    hook({ connect: 'root-gh' })
    adminRequests(m.server)('POST', '/api/spaces', { name: 'On GitHub', github: { owner: 'root-gh', name: 'x' } })
    await m.qc.invalidateQueries()
    await settle(() => text().includes(tg.admin.usageValue(1, 1)), 'the usage')
    await click(button(tg.admin.delete))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(tg.admin.deleteBlocked(1))
    const confirm = [...document.querySelectorAll<HTMLButtonElement>('[role="alertdialog"] button')].find((b) => b.textContent === tg.admin.deleteConfirm)!
    expect(confirm.disabled).toBe(true)
  })
})

describe('the App in the administration: re-checking, warnings and making it on GitHub', () => {
  const routes = (
    <SidebarProvider>
      <Routes>
        <Route path="/admin/:section?" element={<AdminGate />} />
      </Routes>
    </SidebarProvider>
  )
  const configured = { mode: 'accounts' as const, github: { configured: true } }

  it('shows the permissions of the last check until Re-check reads them from GitHub again', async () => {
    m = await mount(routes, { entries: ['/admin/github'], signIn: true, server: configured })
    await settle(() => text().includes(tg.admin.recheck), 'the card')
    expect(text()).not.toContain(tg.admin.missing(['contents']))
    // GitHub's side changed: the card does not know yet.
    hook({ side: { permissions: { administration: 'write', metadata: 'read' }, gaps: [{ installationId: 5001, account: 'kazuhahub', missing: ['contents'] }] } })
    await m.qc.invalidateQueries()
    expect(text()).not.toContain(tg.admin.missing(['contents']))
    await click(button(tg.admin.recheck))
    await settle(() => m!.requests.some((r) => r.method === 'POST' && r.url === '/api/admin/github/check'), 'the request')
    await settle(() => text().includes(tg.admin.missing(['contents'])), 'the missing permission')
    expect(text()).toContain(tg.admin.gaps(['kazuhahub (contents)']))
  })

  it('warns when the App can only be installed on one account, and says when GitHub did not say', async () => {
    m = await mount(routes, { entries: ['/admin/github'], signIn: true, server: configured })
    await settle(() => button(tg.admin.recheck) !== undefined, 'the card')
    expect(text()).not.toContain(tg.admin.anyAccountNo)
    hook({ side: { anyAccount: false } })
    await click(button(tg.admin.recheck))
    await settle(() => text().includes(tg.admin.anyAccountNo), 'the warning')
    await settle(() => button(tg.admin.recheck) !== undefined, 'the button again')
    hook({ side: { anyAccount: null } })
    await click(button(tg.admin.recheck))
    await settle(() => text().includes(tg.admin.anyAccountUnknown) && !text().includes(tg.admin.anyAccountNo), 'the note')
  })

  it('makes the App on GitHub: asks the server for the manifest and posts it from the browser', async () => {
    const submit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => {})
    try {
      m = await mount(routes, { entries: ['/admin/github'], signIn: true, server: { mode: 'accounts' } })
      await settle(() => button(tg.admin.createButton) !== undefined, 'the button')
      await type([...document.querySelectorAll<HTMLInputElement>('input')].find((i) => i.closest('div')?.textContent?.includes(tg.admin.createOrg) && i.id !== '')!, 'kazuhahub')
      await click(button(tg.admin.createButton))
      await settle(() => submit.mock.calls.length === 1, 'the form posted')
      const req = m.requests.find((r) => r.method === 'POST' && r.url === '/api/admin/github/manifest')!
      expect(JSON.parse(req.body!)).toEqual({ organization: 'kazuhahub' })
      const form = submit.mock.contexts[0] as HTMLFormElement
      expect(form.method).toBe('post')
      expect(form.action).toMatch(/^https:\/\/github\.com\/organizations\/kazuhahub\/settings\/apps\/new\?state=/)
      const manifest = JSON.parse((form.elements.namedItem('manifest') as HTMLInputElement).value)
      expect(manifest).toMatchObject({ public: true, request_oauth_on_install: true, hook_attributes: { active: false }, default_permissions: { administration: 'write', contents: 'write', metadata: 'read' } })
      expect(manifest.callback_urls).toEqual(['https://notes.example.test/api/github/authorization/callback'])
      expect(manifest.redirect_url).toBe('https://notes.example.test/api/github/manifest/callback')
    } finally {
      submit.mockRestore()
    }
  })

  it('does not offer to replace an App that is in use', async () => {
    m = await mount(routes, { entries: ['/admin/github'], signIn: true, server: configured })
    await settle(() => text().includes(tg.admin.title), 'the section')
    hook({ connect: 'root-gh' })
    await m.qc.invalidateQueries()
    await settle(() => text().includes(tg.admin.createInUse), 'the note')
    expect(button(tg.admin.createButton)!.disabled).toBe(true)
  })

  it('says how the manifest flow ended, once, from the address GitHub sent the browser back to', async () => {
    m = await mount(routes, { entries: ['/admin/github?github_app=created'], signIn: true, server: configured })
    await settle(() => text().includes(tg.admin.created), 'the notice')
    await settle(() => !locationText().includes('github_app'), 'the address cleaned')
    expect(locationText()).toBe('/admin/github')
    await m.unmount()
    m = undefined
    m = await mount(routes, { entries: ['/admin/github?github_app_error=app_in_use'], signIn: true, server: configured })
    await settle(() => text().includes(tg.admin.returned.app_in_use), 'the explanation')
    await m.unmount()
    m = undefined
    m = await mount(routes, { entries: ['/admin/github?github_app_error=constructor'], signIn: true, server: configured })
    await settle(() => text().includes(tg.admin.returned.unknown), 'the generic message')
  })

  it('plays the manifest flow in the mock: the state is bound to the administrator\'s browser and works once', async () => {
    m = await mount(routes, { entries: ['/admin/github'], signIn: true, server: { mode: 'accounts' } })
    const server = m.server
    const json = 'application/json'
    const login = server.handle('POST', '/api/login', JSON.stringify({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password }), json, null)
    const session = String(login.headers?.['Set-Cookie']).split(';')[0]
    const start = server.handle('POST', '/api/admin/github/manifest', '{}', json, session)
    expect(start.status).toBe(200)
    const flow = String(start.headers?.['Set-Cookie']).split(';')[0]
    const state = new URL((start.body as { url: string }).url).searchParams.get('state')!
    const back = `/api/github/manifest/callback?code=a-code&state=${state}`
    // Without the browser's cookie nothing happens; with it the App is saved, once.
    expect(server.handle('GET', back, undefined, null, session).headers?.Location).toBe('/admin/github?github_app_error=expired')
    expect(server.handle('GET', '/api/admin/github', undefined, null, session).body).toMatchObject({ configured: false })
    expect(server.handle('GET', back, undefined, null, `${session}; ${flow}`).headers?.Location).toBe('/admin/github?github_app=created')
    expect(server.handle('GET', '/api/admin/github', undefined, null, session).body).toMatchObject({ configured: true, appId: '7654321', userAuthorization: true })
    expect(server.handle('GET', back, undefined, null, `${session}; ${flow}`).headers?.Location).toBe('/admin/github?github_app_error=expired')
  })
})

