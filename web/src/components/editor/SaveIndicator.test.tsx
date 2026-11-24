import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ApiError } from '@/api/client'
import { SaveIndicator } from './SaveIndicator'

const failed = (e: Error) => ({ state: 'error' as const, error: e })
const noop = () => undefined

describe('SaveIndicator when the remote cannot be reached', () => {
  it('says the draft is safe on the server, not that saving failed', () => {
    const html = renderToStaticMarkup(
      <SaveIndicator
        snapshot={failed(new ApiError(503, 'unavailable', '远端仓库现在连不上'))}
        draftSafe
        onRetry={noop}
        onOpenConflict={noop}
      />,
    )
    expect(html).toContain('远端连不上，草稿已存在服务器')
    expect(html).toContain('远端仓库现在连不上')
    expect(html).not.toContain('保存失败')
  })
  it('says the text is still in this browser when the server has not got it', () => {
    const html = renderToStaticMarkup(
      <SaveIndicator
        snapshot={failed(new ApiError(503, 'unavailable', 'x'))}
        draftSafe={false}
        onRetry={noop}
        onOpenConflict={noop}
      />,
    )
    expect(html).toContain('远端连不上，内容还在本机')
  })
  it('other errors keep their words', () => {
    const html = renderToStaticMarkup(
      <SaveIndicator
        snapshot={failed(new ApiError(500, 'internal', 'boom'))}
        draftSafe
        onRetry={noop}
        onOpenConflict={noop}
      />,
    )
    expect(html).toContain('保存失败')
  })
})
