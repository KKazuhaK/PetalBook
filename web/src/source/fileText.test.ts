import { describe, expect, it } from 'vitest'
import { composeFile, headerText, minimalChange, parseHeader, splitFile } from './fileText'
import { buildSave } from '@/editor/saveController'
import type { Page } from '@/api/types'

const ID = '01JD3K8Q7Z'

describe('splitFile', () => {
  it('splits header and body where the server does', () => {
    const r = splitFile('---\nid: x\ntitle: 暮雪\n---\n正文\n第二行\n')
    expect(r).toEqual({ ok: true, header: 'id: x\ntitle: 暮雪\n', body: '正文\n第二行\n', bodyLine: 5 })
  })
  it('accepts trailing spaces on the fences and ... as the closing one', () => {
    expect(splitFile('---  \nid: x\n... \nbody')).toMatchObject({ ok: true, header: 'id: x\n', body: 'body' })
  })
  it('a --- inside the body is body', () => {
    expect(splitFile('---\nid: x\n---\na\n\n---\n\nb\n')).toMatchObject({ ok: true, body: 'a\n\n---\n\nb\n' })
  })
  it('an empty header and an empty body', () => {
    expect(splitFile('---\n---\n')).toEqual({ ok: true, header: '', body: '', bodyLine: 3 })
    expect(splitFile('---\n---')).toMatchObject({ ok: true, header: '', body: '' })
  })
  it('reports a missing or unclosed header', () => {
    expect(splitFile('title: x\n')).toMatchObject({ ok: false, problem: { code: 'no_header', line: 1 } })
    expect(splitFile('---\nid: x\nbody\n')).toMatchObject({ ok: false, problem: { code: 'unterminated' } })
  })
  it('reads CRLF as LF', () => {
    expect(splitFile('---\r\nid: x\r\n---\r\nbody\r\n')).toMatchObject({ ok: true, header: 'id: x\n', body: 'body\n' })
  })
  it('composeFile is its inverse', () => {
    for (const [h, b] of [['id: x\n', 'body\n'], ['', ''], ['id: x\ntags: [a]\n', '\n\nx']] as const) {
      expect(splitFile(composeFile(h, b))).toMatchObject({ ok: true, header: h, body: b })
    }
    expect(composeFile('id: x', 'b')).toBe('---\nid: x\n---\nb')
  })
})

describe('parseHeader', () => {
  it('reads the named fields and keeps the rest as others', () => {
    const r = parseHeader(`id: ${ID}\ntitle: 暮雪\nsubtitle: 下一个路口\nsource: 《某本书》\ntags: [玉兰, 黄昏]\ncreated: 2025-12-03\nseries: 冬季\n`, ID)
    expect(r).toMatchObject({
      ok: true,
      fields: { title: '暮雪', subtitle: '下一个路口', source: '《某本书》', tags: ['玉兰', '黄昏'], others: { created: '2025-12-03', series: '冬季' } },
    })
  })
  it('keeps an all-digit id as text', () => {
    expect(parseHeader('id: 0123456789\n', '0123456789')).toMatchObject({ ok: true, meta: { id: '0123456789' } })
    expect(parseHeader('id: "0123456789"\n', '0123456789')).toMatchObject({ ok: true })
  })
  it('refuses a changed or missing id, pointing at the line', () => {
    expect(parseHeader(`title: x\nid: SOMETHINGEL\n`, ID)).toMatchObject({ ok: false, problem: { code: 'id', line: 3 } })
    expect(parseHeader(`title: x\n`, ID)).toMatchObject({ ok: false, problem: { code: 'id' } })
    expect(parseHeader('', ID)).toMatchObject({ ok: false, problem: { code: 'id' } })
  })
  it('points at the line of a YAML error', () => {
    const r = parseHeader(`id: ${ID}\ntitle: 暮雪\ntags: [a, b\nseries: x\n`, ID)
    expect(r.ok).toBe(false)
    if (!r.ok) {
      expect(r.problem.code).toBe('bad_yaml')
      expect(r.problem.line).toBeGreaterThanOrEqual(4)
    }
  })
  it('refuses a header that is not key: value lines, and a repeated key', () => {
    expect(parseHeader('- a\n- b\n', ID)).toMatchObject({ ok: false, problem: { code: 'not_mapping' } })
    expect(parseHeader(`id: ${ID}\ntitle: a\ntitle: b\n`, ID)).toMatchObject({ ok: false, problem: { code: 'bad_yaml' } })
  })
  it('a title that is a number reads as its text', () => {
    expect(parseHeader(`id: ${ID}\ntitle: 1984\n`, ID)).toMatchObject({ ok: true, fields: { title: '1984' } })
  })
})

describe('header edits reach a save', () => {
  const page: Page = { id: ID, path: 'a.md', folder: '', rev: 'r1', updated: '', meta: { id: ID, title: '暮雪', created: '2025-12-03', series: '冬季' }, body: 'x\n' } as Page
  const saveOf = (header: string) => {
    const r = parseHeader(header, ID)
    if (!r.ok) throw new Error(r.problem.message)
    return buildSave(page, { ...r.fields, body: undefined }, () => page.body)
  }
  it('the header as served is no change, in any key order', () => {
    expect(saveOf(headerText(page.meta))).toBeNull()
    expect(saveOf(`series: 冬季\ncreated: 2025-12-03\ntitle: 暮雪\nid: ${ID}\n`)).toBeNull()
  })
  it('an added, changed or removed other key is sent', () => {
    expect(saveOf(`id: ${ID}\ntitle: 暮雪\ncreated: 2025-12-03\nseries: 冬季\nstatus: 定稿\n`)?.meta).toEqual({ id: ID, title: '暮雪', created: '2025-12-03', series: '冬季', status: '定稿' })
    expect(saveOf(`id: ${ID}\ntitle: 暮雪\ncreated: 2025-12-03\nseries: 夏季\n`)?.meta).toMatchObject({ series: '夏季' })
    expect(saveOf(`id: ${ID}\ntitle: 暮雪\ncreated: 2025-12-03\n`)?.meta).toEqual({ id: ID, title: '暮雪', created: '2025-12-03' })
  })
  it('a named field edited in the header is sent like one edited in its box', () => {
    expect(saveOf(`id: ${ID}\ntitle: 残阳\ncreated: 2025-12-03\nseries: 冬季\ntags: [a]\n`)?.meta).toMatchObject({ title: '残阳', tags: ['a'], series: '冬季' })
    expect(saveOf(`id: ${ID}\ncreated: 2025-12-03\nseries: 冬季\n`)?.meta).not.toHaveProperty('title')
  })
})

describe('headerText', () => {
  it('reads back as the same data', () => {
    const meta = { id: ID, title: '暮雪: 一', tags: ['a', '花/栀子'], n: 3, nested: { a: [1, 2] }, empty: '' }
    const r = parseHeader(headerText(meta), ID)
    expect(r).toMatchObject({ ok: true, meta })
  })
})

describe('minimalChange', () => {
  const apply = (from: string, c: { from: number; to: number; insert: string } | null) => (c ? from.slice(0, c.from) + c.insert + from.slice(c.to) : from)
  it('covers only what differs', () => {
    expect(minimalChange('abc', 'abc')).toBeNull()
    expect(minimalChange('hello world', 'hello brave world')).toEqual({ from: 6, to: 6, insert: 'brave ' })
    expect(minimalChange('aXb', 'aYb')).toEqual({ from: 1, to: 2, insert: 'Y' })
  })
  it('always produces the target', () => {
    for (const [a, b] of [['', 'x'], ['x', ''], ['aaa', 'aa'], ['aa', 'aaa'], ['abcabc', 'abc'], ['😀a', '😁a'], ['a😀', 'a😁'], ['一二三', '一三']] as const) {
      expect(apply(a, minimalChange(a, b))).toBe(b)
    }
  })
})
