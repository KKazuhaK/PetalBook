// Shared pieces for the tests of the listings of a folder or a tag: the routes, a way to talk to the server
// behind the app's back, and a typed folder with a few pages.

import { Route, Routes } from 'react-router'
import type { FixtureServer } from '@/api/fixtures/server'
import type { MockServer } from '@/api/mock/server'
import { FolderList, TagList } from '@/pages/Lists'
import { FileOpsProvider } from '@/components/files/FileOps'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'

export const listShell = (
  <SidebarProvider>
    <QuickCaptureProvider>
      <Routes>
        <Route
          path="/s/:space/f/*"
          element={
            <FileOpsProvider>
              <FolderList />
            </FileOpsProvider>
          }
        />
        <Route
          path="/s/:space/t/*"
          element={
            <FileOpsProvider>
              <TagList />
            </FileOpsProvider>
          }
        />
      </Routes>
    </QuickCaptureProvider>
  </SidebarProvider>
)

export type Send = (method: string, path: string, body?: unknown) => { status: number; body: any }
// Requests to the single-user space, as another browser would make them (to the mock or the fixture server).
export const sender =
  (server: MockServer | FixtureServer, space = 'default'): Send =>
  (method, path, body) =>
    'on' in server
      ? server.on(space)(method, path, body)
      : (server.handle(
          method,
          `/api/spaces/${space}${path}`,
          body === undefined ? undefined : JSON.stringify(body),
          'application/json',
        ) as { status: number; body: any })

export const TASK = {
  title: '任务',
  fields: [
    { key: 'status', title: '状态', kind: 'select', options: ['todo', 'doing', 'done'] },
    { key: 'points', title: '点数', kind: 'number' },
    { key: 'due', title: '截止', kind: 'date' },
    { key: 'labels', title: '标签组', kind: 'multiselect', options: ['a', 'b', 'c'] },
    { key: 'note', title: '备注', kind: 'text' },
  ],
}

// A typed folder `Work` with four pages (one in each column of a board, and one with no status).
export function seedWork(server: MockServer | FixtureServer, space = 'default', send: Send = sender(server, space)) {
  send('PUT', '/types/task', TASK)
  send('POST', '/folders', { path: 'Work' })
  send('PUT', '/folders/type', { folder: 'Work', type: 'task' })
  const make = (title: string, meta: Record<string, unknown>, body = `${title} 正文\n`) =>
    send('POST', '/pages', { folder: 'Work', meta: { title, ...meta }, body }).body as { id: string; rev: string }
  return {
    A: make('A', { status: 'done', points: 5, due: '2026-03-01', labels: ['a'], tags: ['栀子'], note: '甲' }),
    B: make('B', { status: 'todo', points: 12, due: '2026-01-01', labels: ['b', 'a'] }),
    C: make('C', { status: 'doing', points: 7.5 }),
    D: make('D', {}),
  }
}

// The views of a folder or a tag as the server holds them, and a way to write them.
export function viewsOf(send: Send, scope: string) {
  return send('GET', `/views?${scope}`).body as { views: Record<string, any>[]; rev: string }
}
export function putViews(send: Send, scope: Record<string, string>, views: Record<string, unknown>[]) {
  const cur = viewsOf(
    send,
    Object.entries(scope)
      .map(([k, v]) => `${k}=${encodeURIComponent(v)}`)
      .join('&'),
  )
  return send('PUT', '/views', { ...scope, baseRev: cur.rev, views })
}

// A page as the fixture server holds it now (in any space), and the draft stored for it.
export const peek = (fx: FixtureServer, id: string) => fx.data.spaces.flatMap((s) => s.pages).find((p) => p.id === id)
export const peekDraft = (fx: FixtureServer, id: string) =>
  fx.data.spaces.map((s) => s.drafts[id]).find((d) => d !== undefined)

// Views as a person's hand in space.yml leaves them: no usable id, so each is served as "@n", n its place from 0.
export function handWrittenViews(fx: FixtureServer, folder: string, views: Record<string, unknown>[]) {
  fx.data.spaces[0].views[`folder=${folder}`] = {
    rev: '',
    views: views.map((v, i) => ({ filter: [], hidden: [], group: '', sort: '', ...v, id: `@${i}` })) as never,
  }
}
