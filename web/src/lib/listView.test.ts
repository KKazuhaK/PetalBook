import { afterEach, describe, expect, it, vi } from 'vitest'
import { captureKey, loadCapture, noteBody, saveCapture } from './capture'
import { cardTitleLine, loadView, saveView, viewKey } from './listView'

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('cardTitleLine', () => {
  it('joins title and subtitle in the 《》｜ template', () => {
    expect(cardTitleLine({ title: '暮雪', subtitle: '下一个路口', untitled: false })).toBe('《暮雪》｜下一个路口')
  })
  it('is just the title without a subtitle', () => {
    expect(cardTitleLine({ title: '暮雪', untitled: false })).toBe('《暮雪》')
  })
  it('is null for an untitled page, even if the label is set', () => {
    expect(cardTitleLine({ untitled: true })).toBeNull()
    expect(cardTitleLine({ subtitle: '只有副标题', untitled: true })).toBeNull()
  })
})

describe('view preference', () => {
  it('keys by space, kind and folder or tag', () => {
    expect(viewKey('s1', 'folder', '随笔/2026')).toBe('pb.view.s1.folder.随笔/2026')
    expect(viewKey('s1', 'tag', '玉兰')).toBe('pb.view.s1.tag.玉兰')
    expect(viewKey('s1', 'recent')).toBe('pb.view.s1.recent')
    // The root folder is a folder of its own, apart from "recent".
    expect(viewKey('s1', 'folder', '')).toBe('pb.view.s1.folder')
  })
  it('defaults to the list, remembers cards, and forgets back to the list', () => {
    const key = viewKey('s1', 'folder', 'a')
    expect(loadView(key)).toBe('list')
    saveView(key, 'cards')
    expect(loadView(key)).toBe('cards')
    expect(loadView(viewKey('s1', 'folder', 'b'))).toBe('list')
    saveView(key, 'list')
    expect(loadView(key)).toBe('list')
  })
  it('remembers the table too, and ignores a stored value it does not know', () => {
    saveView('k', 'table')
    expect(localStorage.getItem('k')).toBe('table')
    expect(loadView('k')).toBe('table')
    localStorage.setItem('k', 'kanban')
    expect(loadView('k')).toBe('list')
  })
  it('survives blocked storage', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('blocked')
      },
      setItem: () => {
        throw new Error('blocked')
      },
      removeItem: () => {
        throw new Error('blocked')
      },
    })
    expect(loadView('k')).toBe('list')
    expect(() => saveView('k', 'cards')).not.toThrow()
    expect(loadCapture('k')).toBe('')
    expect(() => saveCapture('k', 'x')).not.toThrow()
  })
})

describe('quick note draft', () => {
  it('is kept per user and space', () => {
    expect(captureKey('u1', 's1')).toBe('pb.capture.u1.s1')
    expect(captureKey(null, 'default')).toBe('pb.capture.local.default')
    expect(captureKey('u1', 's1')).not.toBe(captureKey('u2', 's1'))
  })
  it('is stored, and removed when emptied', () => {
    const key = captureKey('u1', 's1')
    saveCapture(key, '半句话')
    expect(loadCapture(key)).toBe('半句话')
    saveCapture(key, '')
    expect(localStorage.getItem(key)).toBeNull()
  })
  it('makes the body of the note, or nothing for blank text', () => {
    expect(noteBody('一句话  \n\n')).toBe('一句话\n')
    expect(noteBody('  缩进的第一行\n第二行')).toBe('  缩进的第一行\n第二行\n')
    expect(noteBody(' \n\t ')).toBeNull()
    expect(noteBody('')).toBeNull()
  })
})
