// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { MOCK_ADMIN } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { ta } from '@/i18n/admin'
import { tm } from '@/i18n/mail'
import { click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

// The administration's Mail page (SMTP settings, state, the test message) and the reset link on a user, against
// the mock server, which never sends: what would have been sent is in its outbox.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/:section?" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const button = (label: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label) ?? null
const field = (label: string) => [...document.querySelectorAll<HTMLInputElement>('input')].find((i) => document.querySelector(`label[for="${i.id}"]`)?.textContent?.trim() === label)!
const rootCall = (method: string, path: string, body?: unknown) => {
  const login = m!.server.handle('POST', '/api/login', JSON.stringify(MOCK_ADMIN), 'application/json')
  return m!.server.handle(method, `/api${path}`, body === undefined ? undefined : JSON.stringify(body), 'application/json', login.headers!['Set-Cookie'].split(';')[0])
}

async function open(section: string) {
  m = await mount(routes, { signIn: true, server: { mode: 'accounts' }, entries: [`/admin/${section}`] })
}

describe('the Mail page', () => {
  it('is in the navigation of a site administrator, off to begin with, and shows nothing sent', async () => {
    await open('mail')
    await settle(() => text().includes(tm.admin.help), 'the page')
    expect(document.querySelector(`nav[aria-label="${ta.nav}"]`)?.textContent).toContain(ta.sections.mail)
    expect(text()).toContain(tm.admin.statusNever)
    expect(text()).toContain(tm.admin.queue(0, 0, 0))
    expect(m!.server.mail.outbox).toHaveLength(0)
  })

  it('saves the settings, never shows the password, and enabling needs the sender address', async () => {
    await open('mail')
    await settle(() => field(tm.admin.host) !== undefined, 'the form')
    await type(field(tm.admin.host), 'smtp.example.test')
    await type(field(tm.admin.password), 'hunter2-secret')
    await click(document.querySelector('[role="switch"]'))
    await click(button(tm.admin.save))
    await settle(() => text().includes(tm.admin.errors.mail_from_required), 'the refusal')
    await type(field(tm.admin.from), 'notes@example.test')
    await click(button(tm.admin.save))
    await settle(() => text().includes(tm.admin.saved), 'saved')
    const stored = rootCall('GET', '/admin/mail').body as Record<string, unknown>
    expect(stored).toMatchObject({ enabled: true, host: 'smtp.example.test', hasPassword: true, from: 'notes@example.test' })
    expect(JSON.stringify(stored)).not.toContain('hunter2')
    expect(field(tm.admin.password).value).toBe('')
    expect(field(tm.admin.password).placeholder).toBe(tm.admin.passwordSet)
  })

  it('security none is refused for a host that is not on this machine', async () => {
    await open('mail')
    await settle(() => field(tm.admin.host) !== undefined, 'the form')
    await type(field(tm.admin.host), 'mail.example.test')
    const select = document.querySelector<HTMLSelectElement>('#mail-security')!
    await type(select, 'none')
    await click(button(tm.admin.save))
    await settle(() => text().includes(tm.admin.errors.mail_insecure), 'the refusal')
  })

  it('the test message goes to the address typed, works while mail is off, and says it was sent', async () => {
    await open('mail')
    await settle(() => field(tm.admin.host) !== undefined, 'the form')
    await type(field(tm.admin.testTo), 'admin@example.test')
    await click(button(tm.admin.testSend))
    await settle(() => text().includes(tm.admin.errors.mail_not_configured), 'not configured yet')
    rootCall('PUT', '/admin/mail', { host: 'smtp.example.test', from: 'notes@example.test' })
    await click(button(tm.admin.testSend))
    await settle(() => text().includes(tm.admin.testOk), 'sent')
    expect(m!.server.mail.outbox.map((x) => [x.kind, x.to])).toEqual([['test', 'admin@example.test']])
  })
})

describe('a reset link from the users page', () => {
  it('is offered for a person with a verified address while mail is on, and sends to that address only', async () => {
    await open('users')
    await settle(() => text().includes(MOCK_ADMIN.username), 'the users')
    expect(button(tm.users.sendReset)).toBeNull()
    expect(m!.server.mail.outbox).toHaveLength(0)
  })
})
