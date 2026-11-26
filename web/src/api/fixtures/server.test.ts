import { describe, expect, it } from 'vitest'
import { createFixtureServer } from './server'

describe('the fixture server', () => {
  it('lists, creates, reads, changes and deletes in its collections, and has no rules about it', () => {
    const fx = createFixtureServer({ empty: true })
    const on = fx.on()
    const made = on('POST', '/pages', { folder: 'a', meta: { title: '一' }, body: 'x\n' })
    expect(made.status).toBe(201)
    const id = made.body.id as string
    expect(on('GET', '/pages').body.items.map((p: { id: string }) => p.id)).toEqual([id])
    // No validation: a folder path the real server refuses is kept as sent.
    expect(on('PUT', `/pages/${id}`, { baseRev: made.body.rev, meta: made.body.meta, body: 'y\n' }).status).toBe(200)
    expect(on('GET', `/pages/${id}`).body.body).toBe('y\n')
    expect(on('DELETE', `/pages/${id}`).status).toBe(204)
    expect(on('GET', `/pages/${id}`).status).toBe(404)
  })

  it('says a refusal or an exact answer when a test declares one, for as many requests as it says', () => {
    const fx = createFixtureServer()
    fx.fail('GET', '/api/spaces/:space/tree', 403, { code: 'forbidden', message: 'no' }, { times: 1 })
    expect(fx.call('GET', '/api/spaces/default/tree')).toEqual({
      status: 403,
      body: { error: 'forbidden', message: 'no' },
    })
    expect(fx.call('GET', '/api/spaces/default/tree').status).toBe(200)
    fx.reply('GET', '/api/notifications/unread', { count: 7 })
    expect(fx.call('GET', '/api/notifications/unread').body).toEqual({ count: 7 })
    fx.clear()
    expect(fx.call('GET', '/api/notifications/unread').body).toEqual({ count: 0 })
  })

  it('notes what was sent, and answers a path it has no route for with a 404 that says so', () => {
    const fx = createFixtureServer()
    const r = fx.call('GET', '/api/nothing/here?x=1')
    expect(r.status).toBe(404)
    expect(r.body.message).toContain('no route')
    expect(fx.requests).toEqual([expect.objectContaining({ method: 'GET', url: '/api/nothing/here?x=1' })])
  })

  it('keeps the draft protocol: a store names the draft it replaces, and a commit names the version', () => {
    const fx = createFixtureServer({ empty: true })
    const on = fx.on()
    const page = on('POST', '/pages', { meta: { title: 'T' }, body: 'F0\n' }).body
    const put = (prev: string, body: string) =>
      on('PUT', `/pages/${page.id}/draft`, { prev, baseRev: page.rev, meta: page.meta, body })
    const first = put('', 'one\n')
    expect(first.status).toBe(200)
    // Replacing a draft that is not the one there is a conflict that carries the draft as it is.
    const stale = put('', 'two\n')
    expect([stale.status, stale.body.draft.version]).toEqual([409, first.body.version])
    expect(on('POST', `/pages/${page.id}/commit`, { version: first.body.version }).body.body).toBe('one\n')
    expect(on('POST', `/pages/${page.id}/commit`, { version: first.body.version }).status).toBe(409)
  })

  it('ends a session for a reason the page can read', () => {
    const fx = createFixtureServer({ mode: 'accounts', signedIn: true })
    expect(fx.call('GET', '/api/session').body.user.username).toBe('admin')
    fx.endSession('expired')
    expect(fx.call('GET', '/api/session').body).toMatchObject({ user: null, endedReason: 'expired' })
  })
})

describe('the fixture server and binary bodies', () => {
  it('passes an upload through as bytes, not as JSON', () => {
    const fx = createFixtureServer()
    const page = fx.data.spaces[0].pages[0]
    const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe, 0x00, 0x80])
    const r = fx.handle(
      'POST',
      `/api/spaces/default/pages/${page.id}/assets?name=a.png`,
      new TextDecoder().decode(bytes),
      new Headers({ 'content-type': 'image/png' }),
      bytes,
    )
    expect(r.status).toBe(201)
    expect(r.body).toMatchObject({ name: 'a.png', size: bytes.length })
  })
})
