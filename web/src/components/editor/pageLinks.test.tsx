// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import type { Editor } from '@tiptap/core'
import { docToMarkdown, type DocJSON } from '@/markdown'
import { typeText } from '@/markdown/testTyping'
import { FileOpsProvider } from '@/components/files/FileOps'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'
import PageEditor from '@/pages/PageEditor'
import { t } from '@/i18n'
import { byLabel, click, installDomShims, locationText, mount, press, settle, shareSpace, type, type Mounted } from '@/testing/harness'

// Links between pages in the page editor: the picker after `[[`, what a link looks like and where it leads,
// the panel of backlinks, and what someone who cannot write has of it. The whole page is mounted over the mock
// server, so the requests, the cache and the editor are the real ones.

beforeAll(() => {
  installDomShims()
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

type Role = 'editor' | 'viewer'
interface Made {
  id: string
  path: string
}
type Make = (title: string, folder?: string, body?: string) => Made
interface Ctx {
  space: string
  make: Make
  remove: (id: string) => void
  // Gives a page another title.
  rename: (id: string, title: string) => void
}
interface Scene {
  space: string
  // The page that was opened.
  open: Made
}

// Opens a page of a space someone else owns, in which the signed-in person has `role`. `build` makes the pages first.
async function openPage(role: Role, build: (c: Ctx) => string | void, expectText?: string): Promise<Scene> {
  let scene!: Scene
  m = await mount(null, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      const { space, owner } = shareSpace(server, role)
      const make: Make = (title, folder = '', body = '正文') => {
        const p = owner.request('POST', `/api/spaces/${space}/pages`, { folder, meta: { title }, body }).body as Made
        return { id: p.id, path: p.path }
      }
      const remove = (id: string) => void owner.request('DELETE', `/api/spaces/${space}/pages/${id}`)
      const rename = (id: string, title: string) => {
        const page = owner.request('GET', `/api/spaces/${space}/pages/${id}`).body as { rev: string; meta: Record<string, unknown>; body: string }
        owner.request('PUT', `/api/spaces/${space}/pages/${id}`, { baseRev: page.rev, meta: { ...page.meta, title }, body: page.body })
      }
      const current = make('当前页', 'Notes', '正文')
      const chosen = build({ space, make, remove, rename })
      scene = { space, open: chosen ? { id: chosen, path: '' } : current }
      return [`/s/${space}/p/${scene.open.id}`]
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
  await settle(() => document.querySelector('.pb-prose') !== null && (expectText ? document.body.textContent?.includes(expectText) === true : true), 'the editor')
  return scene
}

const body = () => document.querySelector<HTMLElement>('.pb-prose')!
const editorOf = () => (body() as unknown as { editor: Editor }).editor
const markdown = () => docToMarkdown(editorOf().getJSON() as DocJSON).trim()
const picker = () => document.querySelector<HTMLElement>('[data-testid="page-link-picker"]')
const options = () => [...document.querySelectorAll<HTMLElement>('[data-testid="page-link-picker"] [role="option"]')]
const optionTexts = () => options().map((o) => o.querySelector('span')?.textContent)
const selected = () => options().findIndex((o) => o.getAttribute('aria-selected') === 'true')

// Types at the end of the text, as the keyboard does.
async function typeAtEnd(text: string) {
  const editor = editorOf()
  await act(async () => {
    editor.commands.focus('end')
    typeText(editor, text)
  })
}
async function key(k: string, init: KeyboardEventInit = {}) {
  const event = new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...init })
  await act(async () => {
    editorOf().view.dom.dispatchEvent(event)
  })
  return event
}
const pickerOptions = (n: number) => settle(() => options().length === n, `${n} pages in the picker`)

describe('the page picker', () => {
  it('opens on [[ with the recent pages, not the page itself', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
      make('春晓', 'Tang')
    })
    expect(picker()).toBeNull()
    await typeAtEnd('[[')
    await pickerOptions(2)
    expect(optionTexts().sort()).toEqual(['暮雪', '春晓'].sort())
    expect(optionTexts()).not.toContain('当前页')
    expect(picker()!.querySelector('[role="listbox"]')!.getAttribute('aria-label')).toBe(t.pageLinks.pickerLabel)
  })

  it('also opens on 【【, which a Chinese input method writes for it', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
    })
    await typeAtEnd('【【')
    await pickerOptions(1)
  })

  it('filters by the title as more is typed', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
      make('春晓', 'Tang')
      make('Cat notes', 'Notes')
    })
    await typeAtEnd('[[暮')
    await settle(() => optionTexts().join() === '暮雪', 'only the matching title')
    await typeAtEnd('雪不存在')
    await settle(() => options().length === 0 && picker()!.textContent!.includes(t.pageLinks.pickerEmpty), 'the empty note')
  })

  it('moves with the arrow keys (round and round) and inserts the chosen page with Enter', async () => {
    await openPage('editor', ({ make }) => {
      make('甲', 'Tang')
      make('乙', 'Tang')
    })
    await typeAtEnd('[[')
    await pickerOptions(2)
    expect(selected()).toBe(0)
    const first = optionTexts()[0]
    const second = optionTexts()[1]
    const down = await key('ArrowDown')
    expect(down.defaultPrevented).toBe(true)
    expect(selected()).toBe(1)
    await key('ArrowDown')
    expect(selected()).toBe(0)
    await key('ArrowUp')
    expect(selected()).toBe(1)
    expect(editorOf().view.dom.getAttribute('aria-activedescendant')).toBe(options()[1].id)
    const enter = await key('Enter')
    expect(enter.defaultPrevented).toBe(true)
    expect(picker()).toBeNull()
    expect(first).not.toBe(second)
    const md = markdown()
    expect(md).toMatch(new RegExp(`^正文\\[${second}\\]\\(\\.\\./Tang/${second}\\.md "[0-9A-HJKMNP-TV-Z]{10}"\\)$`))
    expect(md).not.toContain('[[')
  })

  it('inserts with a click on a row, and what is typed next is not part of the link', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
    })
    await typeAtEnd('[[')
    await pickerOptions(1)
    await click(options()[0])
    expect(picker()).toBeNull()
    await typeAtEnd('，好')
    expect(markdown()).toMatch(/^正文\[暮雪\]\(\.\.\/Tang\/暮雪\.md "[0-9A-HJKMNP-TV-Z]{10}"\)，好$/)
  })

  it('puts the id of the page in the title of the link', async () => {
    let id = ''
    await openPage('editor', ({ make }) => {
      id = make('暮雪', 'Tang').id
    })
    await typeAtEnd('[[')
    await pickerOptions(1)
    await key('Enter')
    expect(markdown()).toBe(`正文[暮雪](../Tang/暮雪.md "${id}")`)
  })

  it('Esc closes it without inserting, and it is not offered again until the brackets are typed again', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
    })
    await typeAtEnd('[[暮')
    await pickerOptions(1)
    const esc = await key('Escape')
    expect(esc.defaultPrevented).toBe(true)
    expect(picker()).toBeNull()
    await typeAtEnd('雪')
    expect(picker()).toBeNull()
    expect(markdown()).toBe('正文\\[\\[暮雪')
    // Another pair of brackets asks again.
    await typeAtEnd('，[[')
    await pickerOptions(1)
  })

  it('does not take Enter when it is not open', async () => {
    await openPage('editor', () => undefined)
    await typeAtEnd('[')
    await key('Enter')
    expect(picker()).toBeNull()
    // Enter is the editor's own: the line is split.
    expect(editorOf().getJSON().content).toHaveLength(2)
  })

  it('does not open in code', async () => {
    await openPage('editor', () => undefined)
    const editor = editorOf()
    await act(async () => {
      editor.commands.focus('end')
      editor.chain().toggleCode().run()
    })
    await typeAtEnd('[[')
    expect(picker()).toBeNull()
  })

  it('does not open while an input method is composing, and opens when the composition ends', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
    })
    const dom = editorOf().view.dom
    await act(async () => {
      editorOf().commands.focus('end')
      dom.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
    })
    await typeAtEnd('[[')
    expect(editorOf().view.composing).toBe(true)
    expect(picker()).toBeNull()
    await act(async () => {
      dom.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: '[[' }))
      await new Promise((r) => setTimeout(r, 30))
    })
    await pickerOptions(1)
  })

  it('leaves the keys of an input method alone while composing', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
    })
    await typeAtEnd('[[')
    await pickerOptions(1)
    const dom = editorOf().view.dom
    await act(async () => {
      dom.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
    })
    // Enter that picks a candidate of the input method (keyCode 229) is not the picker's.
    const enter = await key('Enter', { keyCode: 229, isComposing: true } as KeyboardEventInit)
    expect(enter.defaultPrevented).toBe(false)
    expect(markdown()).toBe('正文\\[\\[')
    expect(picker()).not.toBeNull()
    await act(async () => {
      dom.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 30))
    })
    await key('Enter')
    expect(markdown()).toMatch(/^正文\[暮雪\]/)
  })

  it('closes when the cursor leaves the brackets', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
    })
    await typeAtEnd('[[')
    await pickerOptions(1)
    await act(async () => {
      editorOf().commands.setTextSelection(1)
    })
    expect(picker()).toBeNull()
  })
})

describe('the path of an inserted link', () => {
  // The page being edited is in Notes/sub; each case picks one page and reads the Markdown back.
  async function pick(title: string, folder: string, name = title) {
    let target = { id: '', path: '' }
    await openPage('editor', ({ make }) => {
      target = make(title, folder)
      return make('位置页', 'Notes/sub', '正文').id
    })
    await typeAtEnd(`[[${title}`)
    await settle(() => optionTexts().includes(title), `${title} in the picker`)
    await key('Enter')
    return { md: markdown(), target, name }
  }

  it('goes up out of the folder to a page in another one', async () => {
    const { md, target } = await pick('暮雪', 'Tang')
    expect(md).toBe(`正文[暮雪](../../Tang/暮雪.md "${target.id}")`)
  })

  it('goes up to a sibling folder', async () => {
    const { md, target } = await pick('旁边', 'Notes/other')
    expect(md).toBe(`正文[旁边](../other/旁边.md "${target.id}")`)
  })

  it('stays down in a folder below the page', async () => {
    const { md, target } = await pick('下面', 'Notes/sub/deeper')
    expect(md).toBe(`正文[下面](deeper/下面.md "${target.id}")`)
  })

  it('is just the file name for a page in the same folder', async () => {
    const { md, target } = await pick('同级', 'Notes/sub')
    expect(md).toBe(`正文[同级](同级.md "${target.id}")`)
  })

  it('goes to the root of the space', async () => {
    const { md, target } = await pick('根上', '')
    expect(md).toBe(`正文[根上](../../根上.md "${target.id}")`)
  })

  it('encodes spaces and brackets in the file name', async () => {
    const { md, target } = await pick('My Page (1)', 'Tang')
    expect(md).toBe(`正文[My Page (1)](../../Tang/My%20Page%20%281%29.md "${target.id}")`)
  })
})


const links = () => [...body().querySelectorAll<HTMLAnchorElement>('a')]
const linkNamed = (text: string) => links().find((a) => a.textContent === text)!
const pageLink = (text: string) => linkNamed(text).querySelector<HTMLElement>('.pb-page-link') ?? linkNamed(text).closest<HTMLElement>('.pb-page-link')

describe('how links look and where they lead', () => {
  let ids = { live: '', gone: '', home: '' }
  // A page with a link to a page that is there (under the name it had), one to a page that was deleted, and one to the web.
  const home = (role: Role = 'editor') =>
    openPage(
      role,
      ({ make, remove, rename }) => {
        const live = make('旧名字', 'Tang')
        const gone = make('将被删除', 'Tang')
        const page = make('主页', 'Notes', `见[旧名字](../Tang/旧名字.md "${live.id}")、[没了](../Tang/将被删除.md "${gone.id}")和[外链](https://example.com/a.md)。`)
        rename(live.id, '现在的名字')
        remove(gone.id)
        ids = { live: live.id, gone: gone.id, home: page.id }
        return page.id
      },
      '见',
    )

  it('a link to a page is drawn as a page link, a broken one differently, and a link to the web is neither', async () => {
    await home()
    await settle(() => body().querySelector('.pb-page-link-broken') !== null, 'the links are known')
    expect(pageLink('旧名字')).not.toBeNull()
    expect(pageLink('旧名字')!.classList.contains('pb-page-link-broken')).toBe(false)
    expect(pageLink('没了')!.classList.contains('pb-page-link-broken')).toBe(true)
    expect(linkNamed('外链').closest('.pb-page-link')).toBeNull()
    expect(linkNamed('外链').querySelector('.pb-page-link')).toBeNull()
  })

  it('hovering shows the current title of the target, or says it is broken', async () => {
    await home()
    await settle(() => body().querySelector('.pb-page-link-broken') !== null, 'the links are known')
    // The link text is the title it had when written; the tooltip is the title now.
    expect(pageLink('旧名字')!.getAttribute('title')).toBe(t.pageLinks.hover('现在的名字'))
    expect(pageLink('没了')!.getAttribute('title')).toBe(t.pageLinks.broken)
  })

  it('keeps the title with the id in the Markdown', async () => {
    await home()
    expect(markdown()).toContain(`[旧名字](../Tang/旧名字.md "${ids.live}")`)
    expect(markdown()).toContain(`[没了](../Tang/将被删除.md "${ids.gone}")`)
  })

  it('Ctrl-click opens the page in the app, a plain click only puts the cursor there', async () => {
    await home()
    await settle(() => body().querySelector('.pb-page-link') !== null, 'page links')
    const plain = new MouseEvent('click', { bubbles: true, cancelable: true })
    await act(async () => {
      linkNamed('旧名字').dispatchEvent(plain)
    })
    expect(plain.defaultPrevented).toBe(false)
    expect(locationText()).toContain(ids.home)
    const ctrl = new MouseEvent('click', { bubbles: true, cancelable: true, ctrlKey: true })
    await act(async () => {
      linkNamed('旧名字').dispatchEvent(ctrl)
    })
    expect(ctrl.defaultPrevented).toBe(true)
    await settle(() => locationText().endsWith(`/p/${ids.live}`), 'the page opened in the app')
  })

  it('a broken link does not open anything, and says so', async () => {
    await home()
    await settle(() => body().querySelector('.pb-page-link-broken') !== null, 'the links are known')
    await act(async () => {
      linkNamed('没了').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, metaKey: true }))
    })
    expect(locationText()).toContain(ids.home)
    await settle(() => document.body.textContent?.includes(t.pageLinks.broken) === true, 'the notice')
  })

  it('a link to a link the picker made is a page link at once, before the server knows it', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
    })
    await typeAtEnd('[[')
    await pickerOptions(1)
    await key('Enter')
    expect(body().querySelector('.pb-page-link')).not.toBeNull()
    expect(body().querySelector('.pb-page-link-broken')).toBeNull()
  })

  it('on a page that cannot be written a plain click opens the page, and the link in the popover is not offered', async () => {
    await home('viewer')
    await settle(() => body().querySelector('.pb-page-link') !== null, 'page links')
    const click1 = new MouseEvent('click', { bubbles: true, cancelable: true })
    await act(async () => {
      linkNamed('旧名字').dispatchEvent(click1)
    })
    expect(click1.defaultPrevented).toBe(true)
    await settle(() => locationText().endsWith(`/p/${ids.live}`), 'the page opened in the app')
  })
})

describe('the backlinks panel', () => {
  const panel = () => document.querySelector<HTMLElement>(`aside[aria-label="${t.pageLinks.backlinks}"]`)
  const backlinkRequests = () => m!.requests.filter((r) => r.url.includes('/backlinks'))
  const openPanel = async () => {
    await click(byLabel(t.pageLinks.backlinksOpen))
    await settle(() => panel() !== null && panel()!.querySelector('[role="status"]') === null, 'the panel')
  }

  it('asks for nothing before it is opened, then lists who links here with the paragraph and the total', async () => {
    let target = { id: '' }
    await openPage('viewer', ({ make }) => {
      target = make('被链到', 'Tang')
      make('甲', 'Notes', `前面。\n\n这里提到[被链到](../Tang/被链到.md "${target.id}")的内容。\n\n后面。`)
      make('乙', 'Notes/sub', `[别名](../../Tang/被链到.md "${target.id}")`)
      make('无关', 'Notes', '没有链接')
      return target.id
    }, '正文')
    expect(backlinkRequests()).toEqual([])
    expect(panel()).toBeNull()
    await openPanel()
    expect(backlinkRequests()).toHaveLength(1)
    expect(panel()!.querySelector('[data-testid="backlinks-total"]')!.textContent).toBe(t.pageLinks.backlinksTotal(2))
    const rows = [...panel()!.querySelectorAll('li')]
    expect(rows.map((r) => r.querySelector('a')!.textContent).sort()).toEqual(['乙', '甲'])
    const jia = rows.find((r) => r.textContent!.includes('甲'))!
    expect(jia.textContent).toContain('这里提到被链到的内容')
    expect(jia.textContent).not.toContain('前面')
    expect(jia.querySelector('a')!.getAttribute('href')).toMatch(/\/p\/[0-9A-Z]{10}$/)
    expect(panel()!.textContent).not.toContain('无关')
    // Stays shut or open as it was left, in this browser.
    expect(localStorage.getItem('pb.backlinks')).toBe('1')
  })

  it('says so when nothing links here, and closes', async () => {
    await openPage('editor', () => undefined)
    await openPanel()
    expect(panel()!.textContent).toContain(t.pageLinks.backlinksEmpty)
    expect(panel()!.querySelector('[data-testid="backlinks-total"]')).toBeNull()
    await click(byLabel(t.pageLinks.backlinksClose, ))
    expect(panel()).toBeNull()
    expect(localStorage.getItem('pb.backlinks')).toBe('0')
  })

  it('shows 20, and the rest on request, each page once', async () => {
    await openPage('editor', ({ make }) => {
      const target = make('热门', 'Tang')
      for (let i = 0; i < 23; i++) make(`引用${String(i).padStart(2, '0')}`, 'Notes', `见[热门](../Tang/热门.md "${target.id}")`)
      return target.id
    }, '正文')
    await openPanel()
    expect(panel()!.querySelectorAll('li')).toHaveLength(20)
    expect(panel()!.querySelector('[data-testid="backlinks-total"]')!.textContent).toBe(t.pageLinks.backlinksTotal(23))
    const more = [...panel()!.querySelectorAll('button')].find((b) => b.textContent === t.pageLinks.backlinksMore)!
    await click(more)
    await settle(() => panel()!.querySelectorAll('li').length === 23, 'the rest')
    expect(new Set([...panel()!.querySelectorAll('li a')].map((a) => a.getAttribute('href'))).size).toBe(23)
    expect([...panel()!.querySelectorAll('button')].some((b) => b.textContent === t.pageLinks.backlinksMore)).toBe(false)
    expect(backlinkRequests().map((r) => r.url).filter((u) => u.includes('cursor='))).toHaveLength(1)
  })

  it('a reader has it too', async () => {
    await openPage('viewer', ({ make }) => {
      const target = make('被链到', 'Tang')
      make('甲', 'Notes', `见[被链到](../Tang/被链到.md "${target.id}")`)
      return target.id
    }, '正文')
    await openPanel()
    expect(panel()!.querySelectorAll('li')).toHaveLength(1)
  })
})

describe('someone who cannot write', () => {
  it('cannot open the picker: the text cannot be typed in, and brackets put there open nothing', async () => {
    await openPage('viewer', ({ make }) => {
      make('暮雪', 'Tang')
    })
    expect(body().getAttribute('contenteditable')).toBe('false')
    const editor = editorOf()
    await act(async () => {
      editor.view.dispatch(editor.state.tr.insertText('[[', editor.state.doc.content.size - 1))
    })
    expect(picker()).toBeNull()
    expect(m!.requests.filter((r) => r.method !== 'GET' && !r.url.endsWith('/login'))).toEqual([])
  })

  it('can write, with the same page, once they may', async () => {
    await openPage('editor', ({ make }) => {
      make('暮雪', 'Tang')
    })
    await typeAtEnd('[[')
    await pickerOptions(1)
  })

  it('sees the broken marks and the hover text of a reader of the links', async () => {
    await openPage('viewer', ({ make, remove }) => {
      const gone = make('没了', 'Tang')
      const page = make('主页', 'Notes', `[没了](../Tang/没了.md "${gone.id}")`)
      remove(gone.id)
      return page.id
    }, '没了')
    await settle(() => body().querySelector('.pb-page-link-broken') !== null, 'the broken link')
  })
})

function cursorInText(text: string) {
  const editor = editorOf()
  let at = -1
  editor.state.doc.descendants((node, pos) => {
    if (at < 0 && node.isText && node.text!.includes(text)) at = pos + node.text!.indexOf(text) + 1
  })
  editor.chain().focus().setTextSelection(at).run()
}
async function linkInput() {
  await settle(() => document.querySelector(`input[aria-label="${t.link.edit}"]`) !== null, 'the link popover')
  return document.querySelector<HTMLInputElement>(`input[aria-label="${t.link.edit}"]`)!
}

describe('a link changed or typed by hand', () => {
  it('a new address from the popover takes the id off, so the page goes where it was sent', async () => {
    let muxue = ''
    await openPage(
      'editor',
      ({ make }) => {
        muxue = make('暮雪', 'Tang').id
        make('别处', '')
        return make('主页', '', `见[暮雪](Tang/暮雪.md "${muxue}")。`).id
      },
      '见',
    )
    await act(async () => cursorInText('暮雪'))
    const input = await linkInput()
    await type(input, 'Other.md')
    await press(input, 'Enter')
    expect(markdown()).toBe('见[暮雪](Other.md)。')
  })

  it('a bare page path typed in the popover takes the id of that page when the report knows it', async () => {
    let ids = { muxue: '', there: '' }
    await openPage(
      'editor',
      ({ make }) => {
        const muxue = make('暮雪', 'Tang').id
        const there = make('别处', '').id
        ids = { muxue, there }
        return make('主页', '', `见[暮雪](Tang/暮雪.md "${muxue}")和[别处](别处.md "${there}")。`).id
      },
      '见',
    )
    await settle(() => body().querySelectorAll('.pb-page-link[title]').length === 2, 'the links report')
    await act(async () => cursorInText('暮雪'))
    const input = await linkInput()
    await type(input, '别处.md')
    await press(input, 'Enter')
    expect(markdown()).toBe(`见[暮雪](别处.md "${ids.there}")和[别处](别处.md "${ids.there}")。`)
  })

  it('Ctrl-click on a link the report does not list yet finds the page by its file', async () => {
    let target = ''
    await openPage(
      'editor',
      ({ make }) => {
        target = make('暮雪', 'Tang').id
        return make('主页', 'Notes', '见').id
      },
      '见',
    )
    await settle(() => m!.requests.some((r) => r.url.includes('/links')), 'the links report')
    await act(async () => {
      editorOf().chain().focus('end').insertContent({ type: 'text', text: '暮雪', marks: [{ type: 'link', attrs: { href: '../Tang/暮雪.md' } }] }).run()
    })
    await settle(() => pageLink('暮雪') !== null, 'the page link')
    await act(async () => {
      linkNamed('暮雪').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, ctrlKey: true }))
    })
    await settle(() => locationText().endsWith(`/p/${target}`), 'the page opened in the app')
  })

  it('Ctrl-click on a typed link to a file that is not there says it is broken', async () => {
    const scene = await openPage('editor', () => undefined)
    await act(async () => {
      editorOf().chain().focus('end').insertContent({ type: 'text', text: '无此页', marks: [{ type: 'link', attrs: { href: '../Tang/无此页.md' } }] }).run()
    })
    await act(async () => {
      linkNamed('无此页').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, ctrlKey: true }))
    })
    await settle(() => document.body.textContent?.includes(t.pageLinks.broken) === true, 'the notice')
    expect(locationText()).toContain(scene.open.id)
  })
})

describe('the backlinks panel on a narrow screen', () => {
  const panel = () => document.querySelector<HTMLElement>(`aside[aria-label="${t.pageLinks.backlinks}"]`)
  const backdrop = () => document.querySelector<HTMLElement>('[data-testid="backlinks-backdrop"]')
  let shim: typeof window.matchMedia
  let wide = false
  beforeEach(() => {
    shim = window.matchMedia
    wide = false
    window.matchMedia = ((query: string) => ({ ...shim(query), matches: query === '(min-width: 1024px)' ? wide : shim(query).matches })) as typeof window.matchMedia
  })
  afterEach(() => {
    window.matchMedia = shim
  })
  const build = ({ make }: Ctx) => {
    const target = make('被链到', 'Tang')
    make('甲', 'Notes', `见[被链到](../Tang/被链到.md "${target.id}")`)
    return target.id
  }

  it('does not open by itself, though it was left open', async () => {
    localStorage.setItem('pb.backlinks', '1')
    await openPage('editor', build, '正文')
    expect(panel()).toBeNull()
  })

  it('opens over a backdrop with the focus in it; Esc closes it and the focus goes back', async () => {
    await openPage('editor', build, '正文')
    const toggle = byLabel(t.pageLinks.backlinksOpen)!
    toggle.focus()
    await click(toggle)
    await settle(() => panel() !== null, 'the panel')
    expect(backdrop()).not.toBeNull()
    expect(panel()!.contains(document.activeElement)).toBe(true)
    await press(document, 'Escape')
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(toggle)
  })

  it('a click on the backdrop closes it', async () => {
    await openPage('editor', build, '正文')
    await click(byLabel(t.pageLinks.backlinksOpen))
    await settle(() => backdrop() !== null, 'the backdrop')
    await click(backdrop())
    expect(panel()).toBeNull()
  })

  it('closes when a backlink is followed', async () => {
    await openPage('editor', build, '正文')
    await click(byLabel(t.pageLinks.backlinksOpen))
    await settle(() => panel()?.querySelector('li a') != null, 'the backlinks')
    const before = locationText()
    await click(panel()!.querySelector('li a'))
    await settle(() => locationText() !== before && body()?.textContent?.includes('见') === true, 'the other page')
    expect(panel()).toBeNull()
  })

  it('on a wide screen it opens as it was left, with no backdrop', async () => {
    wide = true
    localStorage.setItem('pb.backlinks', '1')
    await openPage('editor', build, '正文')
    await settle(() => panel() !== null, 'the panel')
    expect(backdrop()).toBeNull()
  })
})
