// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { loadMode } from '@/source/useSourceView'
import { UserMenu } from '@/components/layout/UserMenu'
import { AppSidebar } from '@/components/layout/AppSidebar'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { FileOpsProvider } from '@/components/files/FileOps'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'
import { byLabel, click, installDomShims, locationState, locationText, mount, press, settle, type, type Mounted } from '@/testing/harness'
import SettingsRoute from './SettingsRoute'
import { SpaceSettingsRedirect } from './SpaceSettingsRedirect'

// The dialog is built only when it is open, and each section is a different
// piece of code: each one is opened here.

beforeAll(installDomShims)
beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-palette')
  document.documentElement.classList.remove('dark')
})

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
    <Route path="/s/:space/settings" element={<SpaceSettingsRedirect />} />
    <Route path="*" element={<div data-testid="elsewhere">elsewhere</div>} />
  </Routes>
)

const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')
const navLinks = () => [...(dialog()?.querySelectorAll('nav a') ?? [])].map((a) => a.textContent)
const heading = () => dialog()?.querySelector('header h2')?.textContent
const panelText = () => dialog()?.textContent ?? ''

describe('the settings dialog, accounts mode, administrator', () => {
  const open = async (path: string) => {
    m = await mount(routes, { entries: [path], signIn: true, server: { mode: 'accounts', oidc: { name: 'Company SSO' } } })
    await settle(() => dialog() !== null && navLinks().length > 0, 'dialog')
  }

  it('lists the sections in groups', async () => {
    await open('/settings/profile')
    expect(navLinks()).toEqual([t.settingsUi.sections.profile, t.settingsUi.sections.preferences, t.settingsUi.sections.security, t.settingsUi.sections.tokens, t.settingsUi.sections.connections, t.settingsUi.sections.usage, t.settingsUi.sections.general, t.settingsUi.sections.types, t.settingsUi.sections.templates, t.settingsUi.sections.home, t.settingsUi.sections.members, t.settingsUi.sections.sync, t.settingsUi.sections.offline, t.settingsUi.sections.import])
    const groups = [...dialog()!.querySelectorAll('nav [role="group"]')].map((g) => g.getAttribute('aria-label'))
    expect(groups).toEqual([t.settingsUi.groups.account, t.settingsUi.groups.space])
  })

  it('profile: only the name and the email', async () => {
    await open('/settings/profile')
    await settle(() => dialog()?.querySelector('input[autocomplete="name"]') !== null, 'profile loaded')
    expect(heading()).toBe(t.settingsUi.sections.profile)
    expect(dialog()!.querySelector<HTMLInputElement>('input[autocomplete="name"]')!.value).toBe('管理员')
    expect(dialog()!.querySelector('input[type="password"]')).toBeNull()
    expect(panelText()).not.toContain(t.settings.identities)
    expect(dialog()!.querySelector('[aria-current="page"]')!.textContent).toBe(t.settingsUi.sections.profile)
  })

  it('security: the password form is not on the page until the button is pressed', async () => {
    await open('/settings/security')
    await settle(() => panelText().includes(t.settings.identities), 'security loaded')
    expect(heading()).toBe(t.settingsUi.sections.security)
    expect(panelText()).toContain(ts.identities.link('Company SSO'))
    expect(document.querySelector('input[type="password"]')).toBeNull()
    await click([...dialog()!.querySelectorAll('button')].find((b) => b.textContent === t.settings.changePassword))
    await settle(() => document.querySelector('[data-slot="dialog-content"]') !== null, 'password dialog')
    const box = document.querySelector<HTMLElement>('[data-slot="dialog-content"]')!
    expect(box.textContent).toContain(t.settings.current)
    expect(box.textContent).toContain(t.settings.newPassword)
    expect(box.textContent).toContain(t.auth.confirmPassword)
    expect(box.querySelectorAll('input[type="password"]')).toHaveLength(3)
  })

  it('the navigation has an icon for each entry', async () => {
    await open('/settings/profile')
    const items = [...dialog()!.querySelectorAll('nav a')]
    expect(items).toHaveLength(14)
    for (const a of items) expect(a.querySelector('[data-slot="section-icon"]'), a.textContent ?? '').not.toBeNull()
  })

  it('preferences: appearance, language and the editor', async () => {
    await open('/settings/preferences')
    await settle(() => dialog()?.querySelector('[data-palette-option]') !== null, 'preferences')
    expect(heading()).toBe(t.settingsUi.sections.preferences)
    expect(dialog()!.querySelectorAll('[data-palette-option]')).toHaveLength(6)
    expect(dialog()!.querySelector('#pref-language')).not.toBeNull()
    expect(dialog()!.querySelector('[role="switch"]')).not.toBeNull()
    expect(dialog()!.querySelector('#pref-edit-mode')).not.toBeNull()
  })

  it('general: the space name and its inbox folder, both editable for the owner', async () => {
    await open('/settings/general')
    await settle(() => panelText().includes(t.inboxSetting.title), 'general')
    const inputs = [...dialog()!.querySelectorAll<HTMLInputElement>('input[type="text"], input:not([type])')]
    expect(inputs.map((i) => i.value)).toEqual(['我的笔记', expect.any(String)])
    expect(inputs.every((i) => !i.readOnly)).toBe(true)
  })

  it('general: renaming the space saves it', async () => {
    await open('/settings/general')
    await settle(() => panelText().includes(t.spaces.nameLabel), 'general')
    const name = dialog()!.querySelector<HTMLInputElement>('input')!
    await type(name, '新名字')
    await click([...dialog()!.querySelectorAll('button')].find((b) => b.textContent === t.spaces.save))
    await settle(() => m!.requests.some((r) => r.method === 'PATCH' && r.url.startsWith('/api/spaces/')), 'rename request')
    expect(JSON.parse(m!.requests.find((r) => r.method === 'PATCH')!.body!)).toEqual({ name: '新名字' })
  })

  it('sync: the remote repository, as before', async () => {
    await open('/settings/sync')
    await settle(() => panelText().includes(t.remote.intro), 'sync')
    expect(heading()).toBe(t.settingsUi.sections.sync)
    expect(panelText()).toContain(t.remote.intro)
    expect(dialog()!.querySelector('input')).not.toBeNull()
  })

  it('import: the way to the import page', async () => {
    await open('/settings/import')
    await settle(() => panelText().includes(t.import.settingsTitle), 'import')
    expect(dialog()!.querySelector('a[href$="/import"]')).not.toBeNull()
  })

  it('/settings with no section opens the first one', async () => {
    await open('/settings')
    await settle(() => locationText().startsWith('/settings/profile'), 'redirected')
    expect(locationText()).toBe('/settings/profile')
  })

  it('a link that failed comes back to plain /settings with the reason: it is shown where the linked sign-ins are', async () => {
    await open('/settings?sso_error=idp_error')
    await settle(() => panelText().includes(ts.identities.linkFailed), 'message')
    await settle(() => locationText() === '/settings/security', 'on security, error taken out of the address')
  })

  it('a failed sign-in link lands on security and says why, once', async () => {
    await open('/settings/security?sso_error=idp_error')
    await settle(() => panelText().includes(ts.identities.linkFailed), 'message')
    await settle(() => locationText() === '/settings/security', 'error taken out of the address')
  })

  it('a section that does not exist goes to the first', async () => {
    await open('/settings/bogus')
    await settle(() => locationText() === '/settings/profile', 'redirected')
  })

  it('the old address of a space\'s settings lands on the right section (users and invites moved to /admin, see admin.test.tsx)', async () => {
    await open('/s/default/settings')
    await settle(() => locationText() === '/settings/sync', '/s/default/settings')
  })

  it('moving between sections replaces the entry, so one Close leaves settings', async () => {
    m = await mount(routes, { entries: ['/s/default/recent', { pathname: '/settings/profile', state: { from: '/s/default/recent' } }], index: 1, signIn: true, server: { mode: 'accounts' } })
    await settle(() => navLinks().length > 0, 'dialog')
    await click([...dialog()!.querySelectorAll('nav a')].find((a) => a.textContent === t.settingsUi.sections.preferences))
    await settle(() => locationText() === '/settings/preferences', 'navigated')
    // The place to return to travels along.
    expect(locationState()).toEqual({ from: '/s/default/recent' })
    await click([...dialog()!.querySelectorAll('button')].find((b) => b.textContent === t.settingsUi.close))
    await settle(() => document.querySelector('[data-testid="elsewhere"]') !== null, 'closed')
    expect(locationText()).toBe('/s/default/recent')
  })

  it('Escape closes it; a deep link with nowhere to return to closes to the top', async () => {
    await open('/settings/preferences')
    await press(dialog()!, 'Escape')
    await settle(() => document.querySelector('[data-testid="elsewhere"]') !== null, 'closed')
    expect(locationText()).toBe('/')
  })
})

describe('the settings dialog, single-user mode', () => {
  const open = async (path: string) => {
    m = await mount(routes, { entries: [path], server: { mode: 'single' } })
    await settle(() => dialog() !== null && navLinks().length > 0, 'dialog')
  }

  it('shows only what applies', async () => {
    await open('/settings/preferences')
    expect(navLinks()).toEqual([t.settingsUi.sections.preferences, t.settingsUi.sections.general, t.settingsUi.sections.types, t.settingsUi.sections.templates, t.settingsUi.sections.home, t.settingsUi.sections.offline, t.settingsUi.sections.import])
  })

  it('/settings opens the preferences; the account, sync and users sections are not there', async () => {
    await open('/settings')
    await settle(() => locationText() === '/settings/preferences', 'default')
    for (const gone of ['profile', 'sync', 'users']) {
      await m!.unmount()
      await open(`/settings/${gone}`)
      await settle(() => locationText() === '/settings/preferences', gone)
    }
  })

  it('general: the name and the inbox are shown, not editable', async () => {
    await open('/settings/general')
    await settle(() => panelText().includes(t.inboxSetting.title), 'general')
    const inputs = [...dialog()!.querySelectorAll<HTMLInputElement>('input')]
    expect(inputs).toHaveLength(2)
    expect(inputs.every((i) => i.readOnly)).toBe(true)
    expect(panelText()).toContain(t.inboxSetting.readOnly)
  })

  it('/s/<space>/settings goes to general', async () => {
    await open('/s/default/settings')
    await settle(() => locationText() === '/settings/general', 'redirect')
  })
})

describe('the preferences', () => {
  const open = async () => {
    m = await mount(routes, { entries: ['/settings/preferences'], server: { mode: 'single' } })
    await settle(() => dialog()?.querySelector('[data-palette-option]') !== null, 'preferences')
  }
  const palette = (id: string) => dialog()!.querySelector<HTMLElement>(`[data-palette-option="${id}"]`)!

  it('choosing a palette applies it to the page and remembers it; Default takes it off again', async () => {
    await open()
    expect(palette('default').getAttribute('aria-checked')).toBe('true')
    await click(palette('nord'))
    expect(document.documentElement.getAttribute('data-palette')).toBe('nord')
    expect(localStorage.getItem('pb.palette')).toBe('nord')
    expect(palette('nord').getAttribute('aria-checked')).toBe('true')
    await click(palette('default'))
    expect(document.documentElement.hasAttribute('data-palette')).toBe(false)
    expect(localStorage.getItem('pb.palette')).toBeNull()
  })

  it('light, dark and system', async () => {
    await open()
    const radio = (label: string) => [...dialog()!.querySelectorAll<HTMLElement>('[role="radio"]')].find((b) => b.textContent === label)!
    await click(radio(t.theme.dark))
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(localStorage.getItem('theme')).toBe('dark')
    await click(radio(t.theme.light))
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })

  it('the picture upload switch and the default edit mode go to the places the editor reads', async () => {
    await open()
    const toggle = dialog()!.querySelector<HTMLElement>('[role="switch"]')!
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    await click(toggle)
    expect(localStorage.getItem('pb.upload.original')).toBe('1')
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    await type(dialog()!.querySelector<HTMLSelectElement>('#pref-edit-mode')!, 'split')
    // The editor's own reader sees what was set here.
    expect(loadMode()).toBe('split')
  })

  it('the language is a select with each language under its own name', async () => {
    await open()
    const options = [...dialog()!.querySelectorAll<HTMLOptionElement>('#pref-language option')].map((o) => o.textContent)
    expect(options).toEqual(['中文', 'English'])
  })

  it('survives storage that refuses', async () => {
    await open()
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    await click(palette('gruvbox'))
    expect(document.documentElement.getAttribute('data-palette')).toBe('gruvbox')
    vi.restoreAllMocks()
  })
})

describe('where settings are opened from', () => {
  it('the user menu has Settings (no language or theme items) and Administration for an administrator', async () => {
    m = await mount(
      <>
        <UserMenu />
        {routes}
      </>,
      { entries: ['/s/default/recent'], signIn: true, server: { mode: 'accounts' } },
    )
    const trigger = () => document.querySelector<HTMLElement>('[aria-haspopup="menu"]')
    await settle(() => trigger() !== null, 'user menu')
    await click(trigger())
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'menu open')
    const items = [...document.querySelectorAll('[role="menuitem"], [role="menuitemradio"]')].map((e) => e.textContent?.trim())
    expect(items).toEqual([t.userMenu.settings, t.userMenu.admin, t.userMenu.signOut])
    await click(byLabelText(t.userMenu.admin))
    await settle(() => locationText() === '/admin', 'administration')
  })

  it('the sidebar footer has a settings button and no language switch', async () => {
    m = await mount(
      <SidebarProvider>
        <QuickCaptureProvider>
          <FileOpsProvider>
            <AppSidebar />
          </FileOpsProvider>
        </QuickCaptureProvider>
        {routes}
      </SidebarProvider>,
      { entries: ['/s/default/recent'], server: { mode: 'single' } },
    )
    await settle(() => byLabel(t.nav.settings) !== null, 'footer')
    expect(byLabel(t.language.label)).toBeNull()
    // The small theme toggle stays.
    expect(byLabel(t.theme.toggle)).not.toBeNull()
    await click(byLabel(t.nav.settings))
    await settle(() => locationText() === '/settings/preferences', 'opened')
    expect(locationState()).toEqual({ from: '/s/default/recent' })
  })
})

function byLabelText(text: string): Element | null {
  return [...document.querySelectorAll('[role="menuitem"]')].find((e) => e.textContent?.trim() === text) ?? null
}

void act
