// @vitest-environment jsdom
import { afterEach, expect, it } from 'vitest'
import { act, createRef, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { useSourceView } from './useSourceView'
import type { SourceHandle } from './SourceEditor'
import { LiveDraft } from '@/editor/liveDraft'
import { fieldsOf, type SaveController } from '@/editor/saveController'
import type { Page } from '@/api/types'

// Whether the header is part of the text is known to everything that reads the text in the same commit that changes
// it, children's effects included (the source view reads its first text when it is made, before its parent's effects).

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
let root: Root | undefined
afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
})

const page = { id: 'P', rev: 'r1', meta: { id: 'P', title: 'T' }, body: 'body\n' } as unknown as Page
const controller = { page, shownBody: page.body, markDirty() {} } as unknown as SaveController
const live = new LiveDraft(fieldsOf(page.meta), () => '')

function Reader({ read, seen }: { read: () => string; seen: string[] }) {
  useEffect(() => {
    seen.push(read())
  })
  return null
}

function Host({ withHeader, seen }: { withHeader: boolean; seen: string[] }) {
  const sv = useSourceView({
    id: 'P',
    mode: 'source',
    live,
    controller,
    editor: null,
    setFields: () => {},
    richScroller: createRef<HTMLElement>(),
    source: createRef<SourceHandle>(),
    draftRef: 'test/P',
    withHeader,
  })
  return <Reader read={sv.currentText} seen={seen} />
}

it('reads the header into the text in the very commit that asks for it', async () => {
  const seen: string[] = []
  const el = document.createElement('div')
  root = createRoot(el)
  await act(async () => root!.render(<Host withHeader={false} seen={seen} />))
  expect(seen.at(-1)).toBe('body\n')
  await act(async () => root!.render(<Host withHeader seen={seen} />))
  expect(seen.at(-1)).toMatch(/^---\n[\s\S]*title: T[\s\S]*---\nbody\n$/)
  await act(async () => root!.render(<Host withHeader={false} seen={seen} />))
  expect(seen.at(-1)).toBe('body\n')
})
