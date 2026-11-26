// @vitest-environment jsdom
import { createRoot } from 'react-dom/client'
import { act } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { forgetViewAs, startViewAs } from '@/lib/viewAs'
import { RememberAccount } from './RememberAccount'

const noteSession = vi.fn()
vi.mock('@/lib/lastAccount', () => ({ noteSession: (u: unknown) => noteSession(u) }))
vi.mock('@/api/hooks', () => ({
  useSession: () => ({ data: { mode: 'accounts', user: { name: 'Mei', username: 'mei', email: 'm@example.test' } } }),
}))

afterEach(() => {
  forgetViewAs()
  sessionStorage.clear()
  noteSession.mockClear()
})

async function render() {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  const root = createRoot(document.createElement('div'))
  await act(async () => root.render(<RememberAccount />))
  await act(async () => root.unmount())
}

describe('RememberAccount', () => {
  it('keeps the signed-in person as the last account of this browser', async () => {
    startViewAs({ pathname: '/' })
    await render()
    expect(noteSession).toHaveBeenCalledTimes(1)
  })

  it('keeps nobody in a view as a person: they are not who signs in here', async () => {
    startViewAs({ pathname: '/as/va_1' })
    await render()
    expect(noteSession).not.toHaveBeenCalled()
  })
})
