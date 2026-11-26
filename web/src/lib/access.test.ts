import { describe, expect, it } from 'vitest'
import ts from 'typescript'
// Node's own modules: the web app has no Node typings, and the test runs in Node.
// @ts-expect-error TS2591
import { readdirSync, readFileSync } from 'node:fs'
import type { SessionUser } from '@/api/types'
import { acrossSite, adminKindOf, administers, can } from './access'

const user = (o: Partial<SessionUser>): SessionUser =>
  ({
    id: 'u',
    username: 'u',
    name: 'U',
    email: '',
    emailVerified: false,
    role: 'member',
    disabled: false,
    passkeyRequired: false,
    created: '',
    ...o,
  }) as SessionUser

describe('can', () => {
  it('is the keys the session lists, and nothing else', () => {
    const custom = user({ role: 'member', permissions: ['tenant.spaces.create', 'tenant.audit.read'] })
    expect(can(custom, 'tenant.audit.read')).toBe(true)
    expect(can(custom, 'tenant.users.read')).toBe(false)
    expect(administers(custom)).toBe(true)
    expect(administers(user({ permissions: ['tenant.spaces.create'] }))).toBe(false)
    // The role says nothing once the keys are known.
    expect(can(user({ role: 'admin', siteAdmin: true, permissions: [] }), 'site.settings.manage')).toBe(false)
    expect(can(null, 'tenant.spaces.create')).toBe(false)
  })

  it('holds nothing while the permissions are not known: the role is never a guess', () => {
    for (const u of [
      user({ role: 'admin', siteAdmin: true }),
      user({ role: 'admin', siteAdmin: false }),
      user({ role: 'member' }),
    ]) {
      expect([can(u, 'tenant.spaces.create'), can(u, 'tenant.users.read'), acrossSite(u), administers(u)]).toEqual([
        false,
        false,
        false,
        false,
      ])
    }
  })

  it('reads a site administrator, a tenant administrator and a member from their keys', () => {
    const site = user({ role: 'admin', siteAdmin: true, permissions: ['tenant.users.read', 'site.tenants.manage'] })
    const ten = user({ role: 'admin', siteAdmin: false, permissions: ['tenant.users.read'] })
    const mem = user({ role: 'member', permissions: ['tenant.spaces.create'] })
    expect([acrossSite(site), acrossSite(ten), can(ten, 'tenant.users.read'), administers(ten)]).toEqual([
      true,
      false,
      true,
      true,
    ])
    expect([can(mem, 'tenant.spaces.create'), can(mem, 'tenant.users.read'), administers(mem)]).toEqual([
      true,
      false,
      false,
    ])
    expect(adminKindOf({ role: 'admin', siteAdmin: false })).toBe('tenant')
    expect(adminKindOf({ role: 'member' })).toBe(null)
  })
})

// A check of the caller's role name or site-administrator flag anywhere but this helper (and the mock server, which
// plays the server) would decide in the app what the server's keys decide: it fails here. Read from the syntax, so
// a copy of the field ({ siteAdmin: on }, checked={u.siteAdmin}) or a presence check (!== undefined) is not one.
const allowed = ['src/lib/access.ts', 'src/api/mock/']

function sources(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true }) as { name: string; isDirectory(): boolean }[]) {
    const p = `${dir}/${e.name}`
    if (e.isDirectory()) sources(p, out)
    else if (/\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)) out.push(p)
  }
  return out
}

function roleChecks(file: string, text: string): string[] {
  const sf = ts.createSourceFile(
    file,
    text,
    ts.ScriptTarget.Latest,
    false,
    file.endsWith('x') ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  )
  const found: string[] = []
  const at = (n: ts.Node) => found.push(`${file}:${sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1}`)
  const strip = (e: ts.Expression): ts.Expression => {
    while (ts.isParenthesizedExpression(e) || ts.isNonNullExpression(e)) e = e.expression
    return e
  }
  const named = (e: ts.Expression, name: string): boolean => {
    e = strip(e)
    if (ts.isIdentifier(e)) return e.text === name
    if (ts.isPropertyAccessExpression(e)) return e.name.text === name
    return false
  }
  const isSiteAdmin = (e: ts.Expression) => named(e, 'siteAdmin')
  const isRole = (e: ts.Expression) => named(e, 'role')
  const isAdmin = (e: ts.Expression) =>
    ts.isStringLiteralLike(strip(e)) && (strip(e) as ts.StringLiteral).text === 'admin'
  const isNothing = (e: ts.Expression) => {
    e = strip(e)
    return (ts.isIdentifier(e) && e.text === 'undefined') || e.kind === ts.SyntaxKind.NullKeyword
  }
  const cond = (e: ts.Expression) => {
    if (isSiteAdmin(e)) at(e)
  }
  const visit = (n: ts.Node) => {
    if (ts.isBinaryExpression(n)) {
      const op = n.operatorToken.kind
      if (
        op === ts.SyntaxKind.EqualsEqualsEqualsToken ||
        op === ts.SyntaxKind.ExclamationEqualsEqualsToken ||
        op === ts.SyntaxKind.EqualsEqualsToken ||
        op === ts.SyntaxKind.ExclamationEqualsToken
      ) {
        if ((isRole(n.left) && isAdmin(n.right)) || (isRole(n.right) && isAdmin(n.left))) at(n)
        if (!isNothing(n.left) && !isNothing(n.right)) {
          cond(n.left)
          cond(n.right)
        }
      } else if (op === ts.SyntaxKind.AmpersandAmpersandToken || op === ts.SyntaxKind.BarBarToken) {
        cond(n.left)
        cond(n.right)
      }
    } else if (ts.isPrefixUnaryExpression(n) && n.operator === ts.SyntaxKind.ExclamationToken) cond(n.operand)
    else if (ts.isConditionalExpression(n)) cond(n.condition)
    else if (ts.isIfStatement(n) || ts.isWhileStatement(n)) cond(n.expression)
    ts.forEachChild(n, visit)
  }
  visit(sf)
  return found
}

describe('role checks', () => {
  it('finds what it should, and leaves copies alone', () => {
    const src = `
      if (u.role === 'admin') {}
      const a = 'admin' !== role
      const b = me?.siteAdmin !== false
      const c = siteAdmin && x
      const d = !u.siteAdmin
      const e = u.siteAdmin ? 1 : 2
      const f = { siteAdmin: on }
      const g = u.siteAdmin !== undefined
      const h = u.role === 'member'
      save({ siteAdmin: u.siteAdmin })`
    expect(roleChecks('x.ts', src)).toHaveLength(6)
  })

  it('are made in the access helper only', () => {
    const found: string[] = []
    for (const file of sources('src')) {
      if (allowed.some((a) => file.startsWith(a))) continue
      const text = readFileSync(file, 'utf8') as string
      // Only a file that names either is parsed.
      if (!text.includes('siteAdmin') && !text.includes("'admin'")) continue
      found.push(...roleChecks(file, text))
    }
    expect(found, "ask lib/access.ts (the session's permission keys) instead").toEqual([])
  })
})
