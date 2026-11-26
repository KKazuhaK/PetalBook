// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { AdminGate } from '@/components/admin/AdminGate'
import { adminRequests, click, installDomShims, makePerson, mount, settle, type, type Mounted } from '@/testing/harness'
import { tp } from '@/i18n/people'

// Adding people to the organization by file, from the invites page, against the mock server.

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
      <Route path="/admin/*" element={<AdminGate />} />
    </Routes>
  </SidebarProvider>
)
const button = (label: string, root: ParentNode = document) =>
  [...root.querySelectorAll('button')].find((b) => b.textContent?.trim() === label) ?? null
const dialog = () => [...document.querySelectorAll<HTMLElement>('[role="dialog"]')].at(-1)!

async function openDialog() {
  m = await mount(routes, {
    entries: (server) => {
      makePerson(server as never, 'tang')
      adminRequests(server)('POST', '/api/admin/groups', { name: 'Staff', source: 'local' })
      return ['/admin/users/invites']
    },
    signIn: true,
    server: { mode: 'accounts' },
  })
  await settle(() => button(tp.add) !== null, 'the add button')
  await click(button(tp.add))
  await settle(() => document.querySelector('[role="dialog"]') !== null, 'the dialog')
  await click([...dialog().querySelectorAll('[role="tab"]')].find((b) => b.textContent === tp.tabCsv))
}

describe('adding people to the organization by file', () => {
  it('checks the rows, then puts people into groups and makes invitations; with no mail the links are shown once', async () => {
    await openDialog()
    const csv = [
      'email,role,name,groups',
      'new@example.test,member,Newcomer,Staff',
      'tang,member,,Staff',
      'ghost,member,,',
      'new@example.test,member,,',
      'x@other.example,member,,',
      'y@example.test,member,,Nowhere',
    ].join('\n')
    await type(dialog().querySelector<HTMLTextAreaElement>('textarea')!, csv)
    await click(button(tp.csv.check, dialog()))
    await settle(() => dialog().textContent!.includes(tp.status.invited), 'the check')
    for (const word of [tp.status.added, tp.status.invalid, tp.status.duplicate, tp.status.other_tenant])
      expect(dialog().textContent).toContain(word)
    // Mail is not set up: the warning shows before anything is sent, with where to fix it.
    expect(dialog().textContent).toContain(tp.csv.noMailTitle)
    expect(dialog().querySelector(`a[href$="/admin/mail"]`)).not.toBeNull()
    expect(m!.requests.filter((r) => r.method === 'POST' && r.url === '/api/invites')).toHaveLength(0)
    await click(button(tp.csv.apply(2), dialog()))
    await settle(() => dialog().textContent!.includes(tp.statusDone.invited), 'applied')
    // One invitation was made, and its link can be copied (shown once, from this answer).
    expect(button(tp.csv.copyLink, dialog())).not.toBeNull()
    expect(dialog().textContent).toContain(tp.csv.linkOnce)
    const sent = m!.requests.filter((r) => r.url === '/api/admin/members/batch').map((r) => JSON.parse(r.body!).dryRun)
    expect(sent).toEqual([true, false])
  })

  it('refuses more than a thousand rows before sending anything', async () => {
    await openDialog()
    await type(dialog().querySelector<HTMLTextAreaElement>('textarea')!, 'a@example.test\n'.repeat(1001))
    await click(button(tp.csv.check, dialog()))
    await settle(() => dialog().textContent!.includes(tp.csv.tooMany(1000)), 'the refusal')
    expect(m!.requests.filter((r) => r.url === '/api/admin/members/batch')).toHaveLength(0)
  })
})
