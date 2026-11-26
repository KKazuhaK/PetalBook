// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { t } from '@/i18n'
import { ol } from '@/i18n/offline'
import { DEFAULT_CAP_BYTES, DEFAULT_CONFIG, configure, existingStore, hasCopy } from '@/lib/offline'
import { freshDevice } from '@/lib/offline/testkit'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { sender } from '@/testing/listKit'
import SettingsRoute from './SettingsRoute'
import { formatBytes } from '@/lib/bytes'

beforeAll(installDomShims)
beforeEach(freshDevice)

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
  </Routes>
)
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')!
const radio = (label: string) =>
  [...dialog().querySelectorAll<HTMLInputElement>('input[name="offline-mode"]')].find((r) =>
    r.parentElement?.textContent?.includes(label),
  )!
const check = (label: string) =>
  [...dialog().querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find((c) =>
    c.parentElement?.textContent?.includes(label),
  )!
const status = () => dialog().querySelector<HTMLElement>('[data-slot="offline-status"]')?.textContent ?? ''
const select = (label: string) => dialog().querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)
const button = (text: string) =>
  [...dialog().querySelectorAll('button')].find((b) => b.textContent === text) as HTMLButtonElement

// How many pages the space holds now.
const pageCount = () => m!.fx.data.spaces[0].pages.length

async function open(scene?: (s: ReturnType<typeof sender>) => void) {
  m = await mount(routes, {
    entries: (server) => {
      scene?.(sender(server))
      return ['/settings/offline']
    },
    fixtures: { mode: 'single' },
  })
  await settle(() => document.querySelector('input[name="offline-mode"]') !== null, 'the offline settings')
}

describe('the Offline section', () => {
  it('is a section of the space, says the data is not encrypted, and starts with nothing kept', async () => {
    await open()
    expect([...dialog().querySelectorAll('nav a')].map((a) => a.textContent)).toContain(t.settingsUi.sections.offline)
    expect(
      [...dialog().querySelectorAll('input[name="offline-mode"]')].map((r) => r.parentElement?.textContent),
    ).toEqual([
      `${ol.modes.off}${ol.modeHelp.off}`,
      `${ol.modes.catalog}${ol.modeHelp.catalog}`,
      `${ol.modes.text}${ol.modeHelp.text}`,
    ])
    expect(radio(ol.modes.off).checked).toBe(true)
    expect(dialog().querySelector('[data-slot="offline-unencrypted"]')!.textContent).toBe(ol.unencryptedSingle)
    expect(dialog().textContent).not.toContain(ol.statusTitle)
    // Nothing was made on the device just by looking.
    expect(hasCopy('local', 'default')).toBe(false)
    expect(m!.requests.filter((r) => /sync|changes/.test(r.url))).toEqual([])
  })

  it('chooses the catalogue only: the copy is made, synced at once, and its numbers are shown', async () => {
    await open()
    const pages = pageCount()
    expect(pages).toBeGreaterThan(3)
    await click(radio(ol.modes.catalog))
    await settle(() => status().includes(ol.catalogCount(pages)), 'the catalogue count')
    expect(radio(ol.modes.catalog).checked).toBe(true)
    // No scope for the text, and no text kept.
    expect(dialog().textContent).not.toContain(ol.scopeTitle)
    expect(status()).not.toContain(ol.textCount(0))
    const store = (await existingStore('local', 'default'))!
    expect((await store.config())!.mode).toBe('catalog')
    expect(await store.catalogCount()).toBe(pages)
    // The storage used: this space, and the device against its cap.
    expect(status()).toContain(ol.usedHere(formatBytes(0)))
    expect(status()).toContain(ol.usedDevice(formatBytes(0), formatBytes(DEFAULT_CAP_BYTES)))
    expect(dialog().querySelector('[role="progressbar"]')).not.toBeNull()
  })

  it('chooses catalogue and text: the scope appears with the defaults, and what is kept is counted in bytes', async () => {
    await open(
      (s) =>
        void s('POST', '/pages', {
          folder: 'Inbox',
          meta: { title: '收件箱里的页' },
          body: '这页的正文在收件箱里。\n',
        }),
    )
    await click(radio(ol.modes.text))
    await settle(() => dialog().textContent!.includes(ol.scopeTitle), 'the scope')
    expect(check(ol.favourites).checked).toBe(true)
    expect(check(ol.inboxNamed('Inbox')).checked).toBe(true)
    expect(check(ol.recent(200)).checked).toBe(true)
    expect(dialog().textContent).toContain(ol.scopeHelp(formatBytes(DEFAULT_CAP_BYTES)))
    await settle(() => status().includes(ol.textCount(1)), 'the text of the inbox page')
    const store = (await existingStore('local', 'default'))!
    expect((await store.textMetas()).length).toBe(1)
    expect(status()).toContain(ol.usedHere(formatBytes(await store.bytes())))
    expect(await store.bytes()).toBeGreaterThan(400)
    expect(status()).not.toContain(ol.usedHere(formatBytes(0)))
  })

  it('chooses folders and tags to keep, and takes them out again; each change is kept and synced', async () => {
    await open((s) => {
      s('POST', '/folders', { path: '选定' })
      s('POST', '/pages', { folder: '选定', meta: { title: '选定里的页', tags: ['留着'] }, body: '内容\n' })
    })
    await click(radio(ol.modes.text))
    await settle(() => select(ol.addFolder) !== null, 'the folder box')
    await type(select(ol.addFolder)!, '选定')
    await settle(
      () =>
        dialog().textContent!.includes(ol.remove('选定').replace(/^.* /, '')) &&
        [...dialog().querySelectorAll('button')].some((b) => b.getAttribute('aria-label') === ol.remove('选定')),
      'the folder chosen',
    )
    const store = (await existingStore('local', 'default'))!
    await settle(() => status().includes(ol.textCount(1)), 'the page in the folder')
    expect((await store.config())!.folders).toEqual(['选定'])
    // It is not offered again; the tag is.
    expect(select(ol.addFolder)!.textContent).not.toContain('选定')
    await type(select(ol.addTag)!, '留着')
    await settle(
      () => [...dialog().querySelectorAll('button')].some((b) => b.getAttribute('aria-label') === ol.remove('留着')),
      'the tag chosen',
    )
    expect((await store.config())!.tags).toEqual(['留着'])
    // Take the folder out: its page is only kept while the tag holds it.
    await click(
      [...dialog().querySelectorAll('button')].find((b) => b.getAttribute('aria-label') === ol.remove('选定')),
    )
    await settle(
      () => ![...dialog().querySelectorAll('button')].some((b) => b.getAttribute('aria-label') === ol.remove('选定')),
      'the folder taken out',
    )
    expect((await store.config())!.folders).toEqual([])
    expect((await store.textMetas()).length).toBe(1)
    // Turn the favourites and the recent ones off: the inbox and the tag stay.
    await settle(() => !check(ol.favourites).disabled, 'the boxes')
    await click(check(ol.favourites))
    await settle(() => !check(ol.favourites).checked && !check(ol.recent(200)).disabled, 'the favourites off')
    await click(check(ol.recent(200)))
    await settle(() => !check(ol.recent(200)).checked, 'the recent ones off')
    expect(await store.config()).toMatchObject({ favourites: false, recent: false, inbox: true, tags: ['留着'] })
  })

  it('clears the copy of the space, and choosing nothing does too', async () => {
    await open()
    await click(radio(ol.modes.catalog))
    await settle(() => status().includes(ol.catalogCount(pageCount())), 'the copy')
    await click(button(ol.clear))
    await settle(() => !hasCopy('local', 'default'), 'the copy cleared')
    await settle(() => radio(ol.modes.off).checked, 'nothing chosen again')
    expect(dialog().textContent).not.toContain(ol.statusTitle)
    expect((await indexedDB.databases()).filter((d) => d.name?.startsWith('petalbook-offline'))).toEqual([])
    // Choose again, then choose nothing.
    await click(radio(ol.modes.text))
    await settle(() => hasCopy('local', 'default') && status().includes(ol.textCount(0)), 'the copy again')
    await click(radio(ol.modes.off))
    await settle(() => !hasCopy('local', 'default'), 'nothing kept')
    expect(radio(ol.modes.off).checked).toBe(true)
  })

  it('syncs now on request, and says so while it does', async () => {
    await open()
    await click(radio(ol.modes.catalog))
    await settle(() => status().includes(ol.catalogCount(pageCount())), 'the copy')
    sender(m!.fx)('POST', '/pages', { meta: { title: '后来的页' }, body: 'x\n' })
    await settle(() => !button(ol.syncNow).disabled, 'the button')
    await click(button(ol.syncNow))
    await settle(() => status().includes(ol.catalogCount(pageCount())), 'the new page')
  })

  it('shows what went wrong, that the space was switched off and that the room ran out, from the state of the copy', async () => {
    const store = (await configure('local', 'default', { ...DEFAULT_CONFIG, mode: 'text' }))!
    await store.setState({
      complete: true,
      syncedAt: Date.UTC(2026, 10, 23, 8, 0),
      overCap: true,
      error: 'boom',
      disabled: true,
    })
    await open()
    await settle(() => status().includes(ol.neverSynced) === false, 'the status')
    expect(status()).toContain(ol.syncedAt(new Date(Date.UTC(2026, 10, 23, 8, 0)).toLocaleString()))
    expect(status()).toContain(ol.overCap)
    expect(status()).toContain(ol.disabled)
    expect(status()).toContain(ol.lastError('boom'))
  })

  it('is only a note, with no controls, when the administrators turn offline copies off', async () => {
    m = await mount(routes, {
      signIn: true,
      fixtures: { mode: 'accounts' },
      entries: (server) => {
        // An administrator switched the feature off.
        server.data.features['features.offline'] = false
        return ['/settings/offline']
      },
    })
    await settle(() => dialog()?.querySelector('[data-slot="offline-off"]') !== null, 'the note')
    expect(dialog().querySelector('[data-slot="offline-off"]')!.textContent).toBe(ol.offByAdmin)
    expect(dialog().querySelector('input[name="offline-mode"]')).toBeNull()
    expect(dialog().querySelector('[data-slot="offline-status"]')).toBeNull()
  })

  it('says so when this browser cannot keep data', async () => {
    const real = globalThis.indexedDB
    // @ts-expect-error a browser without IndexedDB
    delete globalThis.indexedDB
    try {
      m = await mount(routes, { entries: ['/settings/offline'], fixtures: { mode: 'single' } })
      await settle(() => dialog()?.textContent?.includes(ol.unsupported) === true, 'the notice')
    } finally {
      globalThis.indexedDB = real
    }
  })
})
