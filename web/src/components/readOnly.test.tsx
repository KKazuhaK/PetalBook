// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Outlet, Route, Routes } from 'react-router'
import { keys } from '@/api/hooks'
import { draftRef, loadDraft, saveDraft } from '@/editor/draftStore'
import { FileOpsProvider } from '@/components/files/FileOps'
import { AppSidebar } from '@/components/layout/AppSidebar'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'
import PageEditor from '@/pages/PageEditor'
import { FolderList } from '@/pages/Lists'
import { t } from '@/i18n'
import { adminRequests, byLabel, click, installDomShims, mount, settle, shareSpace, type Mounted } from '@/testing/harness'

// Without the `write` permission nothing that changes the space is offered: the
// controls are not there (not merely disabled), and the editor will not type.
// With it, the same screen has them all; each test here has both sides.

beforeAll(() => {
  installDomShims()
  // ProseMirror asks for rectangles when it moves the cursor into view.
  if (!Range.prototype.getClientRects) {
    Range.prototype.getClientRects = () => [] as unknown as DOMRectList
    Range.prototype.getBoundingClientRect = () => new DOMRect()
  }
})
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const shell = (
  <SidebarProvider>
    <QuickCaptureProvider>
      <Routes>
        <Route
          path="/s/:space"
          element={
            <FileOpsProvider>
              <AppSidebar />
              <Outlet />
            </FileOpsProvider>
          }
        >
          <Route path="f/*" element={<FolderList />} />
        </Route>
      </Routes>
    </QuickCaptureProvider>
  </SidebarProvider>
)

type Role = 'owner' | 'editor' | 'viewer'

// The space of someone else, with a tagged page in it, and the signed-in administrator in `role`.
async function openList(role: Role) {
  m = await mount(shell, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      const { space, owner } = shareSpace(server, role)
      owner.request('POST', `/api/spaces/${space}/pages`, { meta: { title: '共享页', tags: ['蓝'] }, body: '正文\n' })
      return [`/s/${space}/f`]
    },
  })
  await settle(() => document.body.textContent?.includes('共享页') === true && document.querySelector('[role="list"] a') !== null, 'the list and the tags')
}

// Whatever was sent that is not a read (the sign-in the harness did first apart).
const writes = () => m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login')
const hasButtonText = (text: string) => [...document.querySelectorAll('button')].some((b) => b.textContent?.includes(text))
const hasLink = (label: string) => document.querySelector(`a[aria-label="${label}"]`) !== null

describe('the file list and the sidebar', () => {
  it('an owner and an editor have every entry point', async () => {
    for (const role of ['owner', 'editor'] as const) {
      await openList(role)
      expect(byLabel(t.list.newPage), role).not.toBeNull()
      // The new-folder button of the top bar, and the one over the folder tree.
      expect(document.querySelectorAll(`[aria-label="${t.manage.newFolder}"], [aria-label="${t.nav.newFolder}"]`).length, role).toBeGreaterThanOrEqual(2)
      expect(hasLink(t.nav.import), role).toBe(true)
      expect(hasButtonText(t.nav.quickNote), role).toBe(true)
      expect(byLabel(t.tagColor.more('蓝')), role).not.toBeNull()
      expect(document.querySelector('a[draggable="true"]'), role).not.toBeNull()
      expect(document.querySelector('[role="checkbox"]'), role).not.toBeNull()
      await m!.unmount()
      m = undefined
    }
  })

  it('a viewer has no new page, new folder, import, quick note, tag colour, dragging or selecting; the page is still there to open', async () => {
    await openList('viewer')
    expect(byLabel(t.list.newPage)).toBeNull()
    expect(byLabel(t.manage.newFolder)).toBeNull()
    expect(byLabel(t.nav.newFolder)).toBeNull()
    expect(hasLink(t.nav.import)).toBe(false)
    expect(hasButtonText(t.nav.quickNote)).toBe(false)
    expect(byLabel(t.capture.fab)).toBeNull()
    expect(byLabel(t.tagColor.more('蓝'))).toBeNull()
    expect(document.querySelector('[draggable="true"]')).toBeNull()
    expect(document.querySelector('[role="checkbox"]')).toBeNull()
    // The tag is shown, and links to its pages.
    expect(document.querySelector('[role="list"] a')?.textContent).toContain('蓝')
    expect(document.querySelector('a[href*="/p/"]')?.textContent).toContain('共享页')
  })

  it('a viewer gets no menu on right-click, and no key deletes anything', async () => {
    await openList('viewer')
    const row = document.querySelector('li[data-file-key]')
    // No context menu is built around the list at all.
    expect(row).not.toBeNull()
    await click(row!.querySelector('a'))
    expect(document.querySelector('[data-slot="context-menu-content"]')).toBeNull()
    expect(writes()).toEqual([])
  })

  it('the settings of a space only a viewer can read: rename is off in the switcher', async () => {
    await openList('viewer')
    await click(document.querySelector('[aria-label^="' + t.spaces.switcher + '"]'))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the space menu')
    const item = (text: string) => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((e) => e.textContent?.trim() === text)!
    expect(item(t.spaces.rename).getAttribute('aria-disabled')).toBe('true')
    // The settings are open to everyone: the members at least are theirs to see.
    expect(item(t.spaces.settings).getAttribute('aria-disabled')).not.toBe('true')
  })
})

describe('the page editor', () => {
  async function openPage(role: Role) {
    let path = ''
    m = await mount(null, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, role)
        const page = owner.request('POST', `/api/spaces/${space}/pages`, { meta: { title: '共享页' }, body: '正文' }).body as { id: string }
        path = `/s/${space}/p/${page.id}`
        return [path]
      },
      routes: [
        {
          path: '/s/:space/p/:id',
          element: (
            <SidebarProvider>
              <QuickCaptureProvider>
                <FileOpsProvider>
                  <PageEditor />
                </FileOpsProvider>
              </QuickCaptureProvider>
            </SidebarProvider>
          ),
        },
      ],
    })
    await settle(() => document.querySelector('.pb-prose') !== null && document.body.textContent?.includes('正文') === true, 'the editor').catch((e) => {
      console.log(document.body.innerHTML.slice(0, 1500), m!.requests.map((r) => r.method + r.url))
      throw e
    })
  }
  const body = () => document.querySelector<HTMLElement>('.pb-prose')!
  const title = () => document.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${t.editor.titlePlaceholder}"]`)!
  const menuLabels = async () => {
    await click(byLabel(t.editor.more))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the page menu')
    return [...document.querySelectorAll('[role="menuitem"]')].map((e) => e.textContent?.trim())
  }

  it('an editor can type, save a version, move and delete', async () => {
    await openPage('editor')
    expect(body().getAttribute('contenteditable')).toBe('true')
    expect(title().readOnly).toBe(false)
    expect(document.querySelector('[role="toolbar"]')).not.toBeNull()
    expect(byLabel(t.editor.saveVersion)).not.toBeNull()
    expect(await menuLabels()).toEqual([t.history.open, t.editor.move, t.editor.delete])
  })

  it('a viewer reads the page: the body and the fields cannot be typed in, and there is no toolbar, save, move or delete', async () => {
    await openPage('viewer')
    expect(body().getAttribute('contenteditable')).toBe('false')
    expect(title().readOnly).toBe(true)
    expect(title().value).toBe('共享页')
    expect(document.querySelector('[role="toolbar"]')).toBeNull()
    expect(byLabel(t.editor.saveVersion)).toBeNull()
    expect(document.body.textContent).toContain(t.permissions.readOnly)
    // Only the history is on the menu: looking at versions is reading.
    expect(await menuLabels()).toEqual([t.history.open])
    // Nothing was sent that changes the page: no draft, no save.
    expect(writes()).toEqual([])
  })

  it('a viewer has no tag box and no empty subtitle or source field to type in', async () => {
    await openPage('viewer')
    expect(document.querySelector(`input[aria-label="${t.editor.tagsPlaceholder}"]`)).toBeNull()
    expect(document.querySelector(`textarea[aria-label="${t.editor.subtitlePlaceholder}"]`)).toBeNull()
    expect(document.querySelector(`textarea[aria-label="${t.editor.sourceLabel}"]`)).toBeNull()
  })
})

describe('gaining write while a page is open', () => {
  it('keeps the draft this person has, and offers it as on any open', async () => {
    let space = ''
    let pageId = ''
    let owner!: ReturnType<typeof shareSpace>['owner']
    let adminId = ''
    m = await mount(null, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const shared = shareSpace(server, 'viewer')
        space = shared.space
        owner = shared.owner
        const page = owner.request('POST', `/api/spaces/${space}/pages`, { meta: { title: '共享页' }, body: '正文' }).body as { id: string; rev: string }
        pageId = page.id
        adminId = (adminRequests(server)('GET', '/api/session').body as { user: { id: string } }).user.id
        // What this browser kept from when the person could write.
        saveDraft(draftRef(adminId, space, pageId), {
          baseRev: page.rev,
          fields: { title: '共享页', subtitle: '', tags: [] },
          bodyText: '我的草稿',
          savedAt: Date.now(),
          token: 'tok-1',
        })
        return [`/s/${space}/p/${pageId}`]
      },
      routes: [
        {
          path: '/s/:space/p/:id',
          element: (
            <SidebarProvider>
              <QuickCaptureProvider>
                <FileOpsProvider>
                  <PageEditor />
                </FileOpsProvider>
              </QuickCaptureProvider>
            </SidebarProvider>
          ),
        },
      ],
    })
    const editable = () => document.querySelector('.pb-prose')?.getAttribute('contenteditable')
    await settle(() => editable() === 'false' && document.body.textContent?.includes('正文') === true, 'the page as a viewer')
    const admin = adminRequests(m.server)
    expect(loadDraft(draftRef(adminId, space, pageId))?.bodyText).toBe('我的草稿')

    owner.request('PATCH', `/api/spaces/${space}/members/${adminId}`, { role: 'editor' })
    await act(async () => {
      await m!.qc.invalidateQueries({ queryKey: keys.spaces })
    })
    await settle(() => editable() === 'true', 'the editor opens for writing')
    // The draft is offered back, as on a normal open, and reaches the server.
    await settle(() => document.body.textContent?.includes('我的草稿') === true, 'the draft is shown')
    await settle(() => (admin('GET', `/api/spaces/${space}/pages/${pageId}`).body as { draft?: { body: string } }).draft?.body.includes('我的草稿') === true, 'the draft reaches the server')
    expect(m.requests.some((r) => r.method === 'DELETE' && r.url.includes('/draft'))).toBe(false)
  })
})
