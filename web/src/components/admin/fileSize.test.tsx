// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Route, Routes } from 'react-router'
import { ApiError, api, spaceApi } from '@/api/client'
import type { DraftInput, DraftReceipt, Page } from '@/api/types'
import { SaveIndicator } from '@/components/editor/SaveIndicator'
import { SidebarProvider } from '@/components/ui/sidebar'
import { SaveController, type Draft } from '@/editor/saveController'
import { t } from '@/i18n'
import { tg } from '@/i18n/manage'
import { tt } from '@/i18n/tenants'
import { fileTooLargeText } from '@/lib/fileTooLarge'
import { failureText } from '@/lib/failure'
import { adminRequests, click, installDomShims, makePerson, mount, settle, type, type Mounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

// files.maxBytes: the tenant's setting in the settings page, the refusal 413 file_too_large and what is said of it, and
// that what a person wrote is kept when a save is refused.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const refusal = (limit = 2048, size?: number, name?: string) =>
  new ApiError(413, 'file_too_large', 'too large', undefined, undefined, undefined, undefined, {
    error: 'file_too_large',
    limit,
    ...(size === undefined ? {} : { size }),
    ...(name ? { name } : {}),
  })

describe('the message of a file that is over the limit', () => {
  it('names the file, its size and the limit, and says the text is not lost', () => {
    const text = fileTooLargeText(refusal(2048, 5120, 'street.jpg'))!
    expect(text).toBe(tg.size.tooLarge('street.jpg', '2 KB', '5 KB'))
    expect(text).toContain('2 KB')
    expect(text).toContain('street.jpg')
    // A body cut off at the limit has no size.
    expect(fileTooLargeText(refusal(2048, undefined, 'a.png'))).toBe(tg.size.tooLarge('a.png', '2 KB', ''))
    // Without a name it still says the limit.
    expect(fileTooLargeText(refusal(2048))).toBe(tg.size.tooLargeNoName('2 KB'))
    expect(fileTooLargeText(new ApiError(413, 'invalid', 'x'))).toBeUndefined()
    // The upload of a file in a page says it the same way.
    expect(failureText(refusal(2048, 5120, 'street.jpg'))).toBe(text)
  })

  it('shows in the place that says whether the page is saved, with the limit and that the text is still here', () => {
    const html = renderToStaticMarkup(
      <SaveIndicator
        snapshot={{ state: 'error', error: refusal(2048, 5120, '页') }}
        draftSafe={false}
        onRetry={() => undefined}
        onOpenConflict={() => undefined}
      />,
    )
    expect(html).toContain(tg.size.saveShort('2 KB'))
    expect(html).toContain(tg.size.tooLarge('页', '2 KB', '5 KB'))
    expect(html).not.toContain(t.editor.save.error)
  })
})

describe('a save that is refused for its size', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  const page: Page = {
    id: '01JD3K8Q7Z',
    path: 'a.md',
    folder: '',
    rev: 'rev1',
    updated: '2026-11-21T08:30:00Z',
    meta: { id: '01JD3K8Q7Z', title: '标题', created: '2026-01-01', tags: [] },
    body: '一\n',
  }

  it('keeps what was typed: the draft stays unsaved, the state says why, and the same text goes through once the limit allows', async () => {
    let allowed = false
    const putDraft = vi.fn(async (_id: string, input: DraftInput): Promise<DraftReceipt> => {
      if (!allowed) throw refusal(2048, 5000, '标题')
      return { version: 'v1', baseRev: input.baseRev, updated: 'u1', stale: false }
    })
    let current: Draft = { title: '标题', subtitle: '', tags: [], body: '一\n' }
    const c = new SaveController({
      page,
      read: () => current,
      api: {
        putDraft,
        deleteDraft: vi.fn(async () => undefined),
        commit: vi.fn(async () => page),
        getPage: vi.fn(async () => page),
      },
      scope: 'sp',
      onConflict: vi.fn(),
      onCommitted: vi.fn(),
      onStored: vi.fn(),
      onExternalChange: vi.fn(),
    })
    c.attach()
    current = { ...current, body: '很长很长的内容\n' }
    c.markDirty()
    await vi.advanceTimersByTimeAsync(2000)
    expect(putDraft).toHaveBeenCalledTimes(1)
    expect(c.state).toBe('error')
    expect((c.getSnapshot().error as ApiError).code).toBe('file_too_large')
    // The text is still counted as not on the server, and it is not asked again by itself in a loop.
    expect(c.hasUnsaved()).toBe(true)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(putDraft).toHaveBeenCalledTimes(1)
    // The limit was raised: trying again stores the same text.
    allowed = true
    await c.retry()
    await vi.advanceTimersByTimeAsync(0)
    expect(putDraft).toHaveBeenCalledTimes(2)
    expect(putDraft.mock.calls[1][1].body).toBe('很长很长的内容\n')
    expect(c.state).toBe('drafted')
    c.detach?.()
  })
})

describe("the mock server refuses over the tenant's limit, and the setting is in the settings page", () => {
  const routes = (
    <SidebarProvider>
      <Routes>
        <Route path="/admin/:section?" element={<AdminGate />} />
      </Routes>
    </SidebarProvider>
  )
  const asTenantAdmin = (server: Mounted['server']) => {
    const admin = adminRequests(server)
    makePerson(server, 'second', 'admin')
    const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find(
      (u) => u.username === 'admin',
    )!
    admin('PATCH', `/api/users/${me.id}`, { siteAdmin: false })
  }
  const rowOf = (key: string) => document.querySelector<HTMLElement>(`[data-slot="setting-row"][data-key="${key}"]`)!
  const text = () => document.body.textContent ?? ''

  it('lets a tenant administrator set it up to the ceiling, and refuses a value above it', async () => {
    m = await mount(routes, {
      signIn: true,
      server: { mode: 'accounts', maxUpload: 10 * 1024 * 1024 },
      entries: (s) => (asTenantAdmin(s), ['/admin/settings']),
    })
    await settle(() => rowOf('files.maxBytes') !== null, 'the setting')
    const row = rowOf('files.maxBytes')
    expect(row.textContent).toContain(tt.settings.names['files.maxBytes'])
    // The site's value is the flag, and the ceiling: the tenant is told it may go no higher.
    expect(row.textContent).toContain(tt.settings.atMost(10 * 1024 * 1024))
    const follow = row.querySelector<HTMLElement>(
      `[role="switch"][aria-label="${tt.settings.names['files.maxBytes']}: ${tt.settings.follow}"]`,
    )!
    await click(follow)
    const input = row.querySelector<HTMLInputElement>(`input[aria-label="${tt.settings.names['files.maxBytes']}"]`)!
    await type(input, '20971520')
    await click([...row.querySelectorAll('button')].find((b) => b.textContent === tt.settings.save))
    await settle(() => row.textContent!.includes(tt.settings.errors.setting_restricted), 'the refusal')
    await type(
      row.querySelector<HTMLInputElement>(`input[aria-label="${tt.settings.names['files.maxBytes']}"]`)!,
      '2048',
    )
    await click([...row.querySelectorAll('button')].find((b) => b.textContent === tt.settings.save))
    await settle(() => row.textContent!.includes(tt.settings.saved), 'saved')
    // What the app is told it may upload follows the tenant's value.
    expect((await api.meta()).maxUpload).toBe(2048)
    // The recycle days are the site's: a tenant administrator is not offered them.
    expect(text()).not.toContain(tt.settings.names['recycle.days'])
  })

  it('refuses a page, a draft and an upload over the limit with 413 and the numbers, and a smaller write of an over-limit page', async () => {
    m = await mount(routes, { signIn: true, server: { mode: 'accounts' }, entries: ['/admin/settings'] })
    const admin = adminRequests(m.server)
    const tenant = (admin('GET', '/api/session').body as { user: { tenant: { id: string } } }).user.tenant.id
    admin('PUT', `/api/admin/tenants/${tenant}/settings/files.maxBytes`, { value: 1024 })
    const space = m.server.spaceIds()[0]
    const pagesApi = spaceApi(space)
    const refused = await pagesApi
      .createPage({ meta: { title: '大页' }, body: 'x'.repeat(2000) })
      .catch((e: unknown) => e as ApiError)
    expect(refused).toBeInstanceOf(ApiError)
    expect([(refused as ApiError).status, (refused as ApiError).code]).toEqual([413, 'file_too_large'])
    expect((refused as ApiError).body).toMatchObject({ limit: 1024, name: '大页' })
    expect((refused as ApiError).body).toHaveProperty('size')
    const small = await pagesApi.createPage({ meta: { title: '小页' }, body: 'x' })
    const grow = await pagesApi
      .savePage(small.id, { baseRev: small.rev, meta: small.meta, body: 'y'.repeat(2000) })
      .catch((e: unknown) => e as ApiError)
    expect((grow as ApiError).code).toBe('file_too_large')
    const upload = await pagesApi
      .uploadAsset(small.id, new Blob([new Uint8Array(2000)]), 'big.bin')
      .catch((e: unknown) => e as ApiError)
    expect([(upload as ApiError).status, (upload as ApiError).code]).toEqual([413, 'file_too_large'])
    expect((upload as ApiError).body).toMatchObject({ limit: 1024, size: 2000, name: 'big.bin' })
    expect((await pagesApi.uploadAsset(small.id, new Blob([new Uint8Array(100)]), 'ok.bin')).path).toContain('ok')
  })
})
