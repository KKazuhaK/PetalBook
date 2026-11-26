// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { toast } from 'sonner'
import type { FixtureData } from '@/api/fixtures/data/model'
import { sharedSpace } from '@/api/fixtures/data/model'
import type { Problem, ProblemKind } from '@/api/types'
import { SidebarProvider } from '@/components/ui/sidebar'
import { ct } from '@/i18n/checks'
import { click, installDomShims, locationText, mount, settle, type FxMounted } from '@/testing/harness'
import CheckPage from './CheckPage'

// The check page of a space, against the fixture server. What counts as a problem is the server's to work out: a scene
// puts the problems it needs in the space, as the server would list them.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  toast.dismiss()
  await m?.unmount()
  m = undefined
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/s/:space/check" element={<CheckPage />} />
    </Routes>
  </SidebarProvider>
)
// A problem, as the server lists it (the rev is the file's, which a fix names).
const problem = (
  kind: ProblemKind,
  path: string,
  extra: Partial<Problem> = {},
  detail: Problem['detail'] = {},
): Problem => ({ kind, path, message: path, rev: `rev-${path}`, detail, fix: null, ...extra })
const checks = (d: FixtureData, ...found: Problem[]) => d.spaces[0].checks.push(...found)
const PAGE = (n: string) => `01CHK${n.padStart(5, '0')}`

const page = () => document.body
const text = () => page().textContent ?? ''
const rows = (kind?: string) => [
  ...document.querySelectorAll<HTMLElement>(kind ? `li[data-problem="${kind}"]` : 'li[data-problem]'),
]
const row = (kind: string, path: string) => rows(kind).find((r) => r.dataset.path === path)
const group = (kind: string) => document.querySelector<HTMLElement>(`section[data-kind="${kind}"]`)
const chip = (kind: string) => document.querySelector<HTMLElement>(`[data-chip="${kind}"]`)
const fixButton = (kind: string, path: string) =>
  row(kind, path)?.querySelector<HTMLButtonElement>('button[data-fix]') ?? null
const fixes = () =>
  m!.requests
    .filter((r) => r.method === 'POST' && r.url.endsWith('/checks/fix'))
    .map((r) => JSON.parse(r.body!) as { kind: string; path: string; baseRev: string })
const listed = (fx: FxMounted['fx']) =>
  fx.call('GET', '/api/spaces/default/checks').body as {
    counts: Record<string, number>
    items: { kind: string; path: string; rev: string; id?: string }[]
    next: string
  }
const confirmDialog = () => document.querySelector<HTMLElement>('[role="alertdialog"]')
const buttonIn = (root: ParentNode | null | undefined, label: string) =>
  [...(root?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.trim() === label) as
    HTMLButtonElement | undefined

async function open(scene: (d: FixtureData) => void, extra: { path?: string } = {}) {
  m = await mount(routes, {
    entries: [extra.path ?? '/s/default/check'],
    fixtures: { mode: 'single', data: scene },
  })
  await settle(() => rows().length > 0 || text().includes(ct.empty), 'the check page')
}

// One problem of each of the eight kinds.
function everything(d: FixtureData) {
  checks(
    d,
    problem('type_mismatch', 'Work/不合类型.md', { id: PAGE('2') }, { fields: ['status'] }),
    problem('not_in_type', 'Work/多余字段.md', { id: PAGE('1'), fix: 'remove_fields' }, { fields: ['colour', 'size'] }),
    problem('duplicate_title', 'Notes/同名.md', { id: PAGE('3') }, { paths: ['Notes/同名 2.md', 'Notes/同名.md'] }),
    problem('duplicate_title', 'Notes/同名 2.md', { id: PAGE('4') }, { paths: ['Notes/同名 2.md', 'Notes/同名.md'] }),
    problem('broken_link', 'Notes/断链.md', { id: PAGE('5') }, { target: 'Notes/nowhere.md', line: 3 }),
    problem('missing_asset', 'Notes/缺附件.md', { id: PAGE('6') }, { target: '/assets/NOSUCH/gone.png' }),
    problem('no_id', 'Notes/noid.md', { fix: 'add_id' }),
    problem('bad_header', 'Notes/broken.md', {}, { line: 3 }),
    problem('duplicate_id', 'Notes/twin.md', { id: PAGE('7') }, { paths: ['Notes/占用.md', 'Notes/twin.md'] }),
  )
}

describe('the check page', () => {
  it('says so when there is nothing wrong, with no summary and no groups', async () => {
    await open(() => {})
    expect(text()).toContain(ct.empty)
    expect(document.querySelector('[data-testid="check-empty"]')).not.toBeNull()
    expect(document.querySelector('[data-testid="check-summary"]')).toBeNull()
    expect(rows()).toHaveLength(0)
  })

  it('counts every kind, hides the kinds with none, and groups the problems with a title and a sentence each', async () => {
    await open(everything)
    expect(chip('all')?.textContent).toContain('9')
    for (const k of [
      'no_id',
      'duplicate_id',
      'bad_header',
      'type_mismatch',
      'not_in_type',
      'broken_link',
      'missing_asset',
      'duplicate_title',
    ] as const) {
      expect(chip(k), k).not.toBeNull()
      expect(group(k)?.textContent, k).toContain(ct.kinds[k].title)
      expect(group(k)?.textContent, k).toContain(ct.kinds[k].help)
    }
    expect(chip('duplicate_title')?.textContent).toContain('2')
    expect(group('duplicate_title')?.querySelectorAll('li[data-problem]')).toHaveLength(2)
    // In the order of the kinds.
    expect(rows().map((r) => r.dataset.problem)).toEqual([
      'no_id',
      'duplicate_id',
      'bad_header',
      'type_mismatch',
      'not_in_type',
      'broken_link',
      'missing_asset',
      'duplicate_title',
      'duplicate_title',
    ])
  })

  it('hides a kind that has no problems', async () => {
    await open((d) =>
      checks(
        d,
        problem('not_in_type', 'Work/多余.md', { id: PAGE('1'), fix: 'remove_fields' }, { fields: ['colour'] }),
      ),
    )
    expect(chip('not_in_type')).not.toBeNull()
    expect(chip('broken_link')).toBeNull()
    expect(group('broken_link')).toBeNull()
  })

  it('shows what each kind knows: the page to open, the fields, the target and its line, the header line, the files', async () => {
    await open(everything)
    const idOf = (kind: string, path: string) => listed(m!.fx).items.find((p) => p.kind === kind && p.path === path)?.id
    expect(listed(m!.fx).items.length).toBe(9)

    // no_id: not a page, so the path leads to its folder.
    const noId = row('no_id', 'Notes/noid.md')!
    expect(noId.querySelector('a')?.getAttribute('href')).toBe('/s/default/f/Notes')
    // bad_header: the line.
    expect(row('bad_header', 'Notes/broken.md')?.textContent).toContain(ct.line(3))
    // not_in_type: the fields, listed, and the page to open.
    const notIn = rows('not_in_type')[0]
    expect([...notIn.querySelectorAll('code')].map((c) => c.textContent)).toEqual(['colour', 'size'])
    expect(notIn.querySelector('a')?.getAttribute('href')).toBe(
      `/s/default/p/${idOf('not_in_type', notIn.dataset.path!)}`,
    )
    // type_mismatch: the field.
    expect([...rows('type_mismatch')[0].querySelectorAll('code')].map((c) => c.textContent)).toEqual(['status'])
    // broken_link: where it points and on which line.
    const broken = rows('broken_link')[0]
    expect(broken.querySelector('code')?.textContent).toBe('Notes/nowhere.md')
    const line = (listed(m!.fx).items.find((p) => p.kind === 'broken_link') as unknown as { detail: { line: number } })
      .detail.line
    expect(line).toBeGreaterThan(1)
    expect(broken.textContent).toContain(ct.line(line))
    // missing_asset: the file.
    expect(rows('missing_asset')[0].textContent).toContain('/assets/NOSUCH/gone.png')
    // duplicate_title: both files, as links.
    const dup = rows('duplicate_title')[0]
    expect([...dup.querySelectorAll(':scope ul a')].map((a) => a.textContent)).toEqual([
      'Notes/同名 2.md',
      'Notes/同名.md',
    ])
    expect(dup.querySelector(':scope ul a')?.getAttribute('href')).toBe('/s/default/f/Notes')
    // duplicate_id: the id shared opens the page that has it; the file itself is shown in its folder.
    const twin = row('duplicate_id', 'Notes/twin.md')!
    const shared = idOf('duplicate_id', 'Notes/twin.md')!
    expect(twin.querySelector('a')?.getAttribute('href')).toBe('/s/default/f/Notes')
    expect(twin.textContent).toContain(ct.sharedId(shared))
    expect(twin.querySelector(`a[href="/s/default/p/${shared}"]`)).not.toBeNull()
  })

  it('shows 50 at a time and then the rest', async () => {
    await open((d) => {
      for (let i = 0; i < 105; i++)
        checks(
          d,
          problem(
            'not_in_type',
            `Work/页${String(i).padStart(3, '0')}.md`,
            { id: PAGE(String(i)), fix: 'remove_fields' },
            {
              fields: ['extra'],
            },
          ),
        )
    })
    expect(rows()).toHaveLength(50)
    expect(chip('all')?.textContent).toContain('105')
    const more = () => [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('再显示'))
    expect(more()?.textContent).toBe(ct.more(50))
    await click(more())
    await settle(() => rows().length === 100, 'the second batch')
    expect(more()?.textContent).toBe(ct.more(5))
    await click(more())
    await settle(() => rows().length === 105, 'the last batch')
    expect(more()).toBeUndefined()
    // The cursor of each batch was the one the last gave.
    expect(m!.requests.filter((r) => /\/checks(\?|$)/.test(r.url)).map((r) => /cursor=/.test(r.url))).toEqual([
      false,
      true,
      true,
    ])
  })

  it('filters by kind, asking only for that kind, and goes back to all of them', async () => {
    await open(everything)
    await click(chip('broken_link'))
    await settle(() => rows().length === 1, 'the filter')
    expect(rows()[0].dataset.problem).toBe('broken_link')
    expect(locationText()).toBe('/s/default/check?kind=broken_link')
    expect(m!.requests.some((r) => /checks\?kind=broken_link/.test(r.url))).toBe(true)
    expect(chip('broken_link')?.getAttribute('aria-pressed')).toBe('true')
    await click(chip('all'))
    await settle(() => rows().length === 9, 'all of them again')
    expect(locationText()).toBe('/s/default/check')
  })

  it('fixes a file with no id: sends the rev it was listed with, then the page is not on the list and the counts fall', async () => {
    await open(everything)
    const rev = listed(m!.fx).items.find((p) => p.kind === 'no_id')!.rev
    await click(fixButton('no_id', 'Notes/noid.md'))
    await settle(() => row('no_id', 'Notes/noid.md') === undefined, 'the fix')
    expect(fixes()).toEqual([{ kind: 'no_id', path: 'Notes/noid.md', baseRev: rev }])
    expect(chip('no_id')).toBeNull()
    expect(chip('all')?.textContent).toContain('8')
    await settle(() => text().includes(ct.fix.addIdDone('Notes/noid.md')), 'the notice')
  })

  it('removes the fields a type does not have after a yes: lists them first, and a no sends nothing', async () => {
    await open(everything)
    const p = listed(m!.fx).items.find((x) => x.kind === 'not_in_type')!
    await click(fixButton('not_in_type', p.path))
    await settle(() => confirmDialog() !== null, 'the question')
    expect(confirmDialog()?.textContent).toContain(p.path)
    expect(confirmDialog()?.textContent).toContain('colour, size')
    await click(buttonIn(confirmDialog(), ct.fix.cancel))
    await settle(() => confirmDialog() === null, 'the question closed')
    expect(fixes()).toEqual([])
    await click(fixButton('not_in_type', p.path))
    await settle(() => confirmDialog() !== null, 'the question again')
    await click(buttonIn(confirmDialog(), ct.fix.confirm))
    await settle(() => row('not_in_type', p.path) === undefined, 'the fix')
    expect(fixes()).toEqual([{ kind: 'not_in_type', path: p.path, baseRev: p.rev }])
    expect(chip('not_in_type')).toBeNull()
    await settle(() => text().includes(ct.fix.removeFieldsDone(p.path)), 'the notice')
  })

  it('on a 409 reads the problems again, says the file changed, and the next try goes through with the new rev', async () => {
    await open((d) =>
      checks(
        d,
        problem('not_in_type', 'Work/多余.md', { id: PAGE('1'), fix: 'remove_fields' }, { fields: ['colour'] }),
      ),
    )
    const old = listed(m!.fx).items.find((x) => x.kind === 'not_in_type')!
    // Someone else adds a field behind the page's back: the file has another rev and the field.
    const behind = m!.fx.data.spaces[0].checks[0]
    behind.rev = 'rev-changed'
    behind.detail = { fields: ['colour', 'shape'] }
    await click(fixButton('not_in_type', old.path))
    await click(buttonIn(confirmDialog(), ct.fix.confirm))
    await settle(() => text().includes(ct.fix.conflict), 'the notice')
    expect(fixes()).toHaveLength(1)
    // Read again: the row has both fields now, and the new rev.
    await settle(
      () =>
        [...(row('not_in_type', old.path)?.querySelectorAll('code') ?? [])].map((c) => c.textContent).join() ===
        'colour,shape',
      'the list read again',
    )
    const fresh = listed(m!.fx).items.find((x) => x.kind === 'not_in_type')!
    expect(fresh.rev).not.toBe(old.rev)
    await click(fixButton('not_in_type', old.path))
    await click(buttonIn(confirmDialog(), ct.fix.confirm))
    await settle(() => row('not_in_type', old.path) === undefined, 'the second try')
    expect(fixes()[1]).toEqual({ kind: 'not_in_type', path: old.path, baseRev: fresh.rev })
  })

  it('has no fix buttons for someone who may only read, and says why', async () => {
    m = await mount(routes, {
      signIn: true,
      fixtures: {
        mode: 'accounts',
        data: (d) => {
          sharedSpace(d, 'viewer').checks.push(
            problem('not_in_type', 'Work/多余.md', { id: PAGE('1'), fix: 'remove_fields' }, { fields: ['colour'] }),
          )
        },
      },
      entries: () => {
        localStorage.setItem('pb.space', '01FSHARE1')
        return ['/s/01FSHARE1/check']
      },
    })
    await settle(() => rows().length === 1, 'the list')
    expect(document.querySelectorAll('button[data-fix]')).toHaveLength(0)
    expect(text()).toContain(ct.fix.readOnly)
    expect(rows('not_in_type')[0].textContent).toContain('colour')
  })

  it('says a kind is empty, with a way back to all of them, when the last problem of the filtered kind is fixed', async () => {
    await open(everything, { path: '/s/default/check?kind=no_id' })
    await settle(() => rows().length === 1, 'the filtered list')
    await click(fixButton('no_id', 'Notes/noid.md'))
    await settle(() => text().includes(ct.emptyKind), 'the empty kind')
    await click(buttonIn(page(), ct.showAll))
    await settle(() => rows().length === 8, 'all of them')
  })
})
