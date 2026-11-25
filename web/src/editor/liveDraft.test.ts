import { describe, expect, it, vi } from 'vitest'
import { LiveDraft } from './liveDraft'

const editor = () => ({ isDestroyed: false, getJSON: vi.fn(() => ({ type: 'doc' })) })

describe('LiveDraft', () => {
  it('reports no body until the body has been edited', () => {
    const toMd = vi.fn(() => 'md')
    const d = new LiveDraft({ title: 'T', subtitle: '', source: [], tags: [] }, toMd)
    d.attachEditor(editor())
    expect(d.read()).toEqual({ title: 'T', subtitle: '', source: [], tags: [], body: undefined })
    expect(toMd).not.toHaveBeenCalled()
  })

  it('converts once per distinct edit state, however often it is read', () => {
    const toMd = vi.fn(() => 'md')
    const d = new LiveDraft({ title: '', subtitle: '', source: [], tags: [] }, toMd)
    d.attachEditor(editor())
    d.bodyChanged()
    d.bodyChanged()
    d.read()
    d.read()
    expect(toMd).toHaveBeenCalledTimes(1)
    d.bodyChanged()
    expect(d.read().body).toBe('md')
    expect(toMd).toHaveBeenCalledTimes(2)
  })

  it('keeps the last conversion when the editor is already gone', () => {
    const d = new LiveDraft({ title: '', subtitle: '', source: [], tags: [] }, () => 'last')
    const ed = editor()
    d.attachEditor(ed)
    d.bodyChanged()
    d.read()
    ed.isDestroyed = true
    expect(d.read().body).toBe('last')
  })

  it('reset forgets body edits and takes the new fields', () => {
    const d = new LiveDraft({ title: 'a', subtitle: '', source: [], tags: [] }, () => 'md')
    d.attachEditor(editor())
    d.bodyChanged()
    d.reset({ title: 'b', subtitle: 's', source: [], tags: ['x'] })
    expect(d.read()).toEqual({ title: 'b', subtitle: 's', source: [], tags: ['x'], body: undefined })
  })
})

describe('LiveDraft without a usable editor', () => {
  it('fails loudly if the body was edited but the editor is gone and nothing was converted', () => {
    const d = new LiveDraft({ title: '', subtitle: '', source: [], tags: [] }, () => 'md')
    d.attachEditor({ isDestroyed: true, getJSON: () => ({ type: 'doc' }) })
    d.bodyChanged()
    expect(() => d.read()).toThrow(/no longer available/)
  })
})
