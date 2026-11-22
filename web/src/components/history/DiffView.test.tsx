import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it } from 'vitest'
import { markdownToDoc } from '@/markdown'
import { DiffView } from './DiffView'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('DiffView images', () => {
  it('draws images from the mapped address, relative ones from the folder of the page', async () => {
    const doc = markdownToDoc('![a](/assets/01X/a.png)\n\n![b](b.png)\n\n![c](https://example.com/c.png)\n')
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    await act(async () => {
      root.render(<DiffView doc={doc} label="x" onRendered={() => undefined} space="default" folder="notes" />)
    })
    const srcs = Array.from(host.querySelectorAll('img:not(.ProseMirror-separator)')).map((i) => i.getAttribute('src'))
    expect(srcs).toEqual(['/api/spaces/default/files/assets/01X/a.png', '/api/spaces/default/files/notes/b.png', 'https://example.com/c.png'])
    await act(async () => root.unmount())
    host.remove()
  })
})
