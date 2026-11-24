// The stores that are open in this tab, and the things done to a copy as a whole: choosing what a space keeps,
// deleting it, deleting everything an account has on this device.

import { deleteDb } from './idb'
import { DB_PREFIX, dbName, dropEntry, entryOf, spacesOf } from './registry'
import { DEFAULT_CONFIG, OfflineStore, type OfflineConfig } from './store'

const open = new Map<string, Promise<OfflineStore>>()
const keyOf = (user: string, space: string) => `${user}\u0000${space}`

// The store of a space, opened (and made, if the space had none and `create` says so).
export async function storeFor(user: string, space: string, create = true): Promise<OfflineStore> {
  const k = keyOf(user, space)
  let p = open.get(k)
  if (p) {
    const s = await p.catch(() => undefined)
    if (s && !s.closed) return s
    if (open.get(k) === p) open.delete(k)
  }
  p = OfflineStore.open(user, space, create)
  open.set(k, p)
  try {
    return await p
  } catch (e) {
    open.delete(k)
    throw e
  }
}

// The store of a space that has a copy on this device; undefined when it has none. Nothing is made: a copy cleared
// while this was opening it is not brought back as an empty database.
export async function existingStore(user: string, space: string): Promise<OfflineStore | undefined> {
  if (!entryOf(user, space)) return undefined
  try {
    return await storeFor(user, space, false)
  } catch {
    return undefined
  }
}

export const hasCopy = (user: string, space: string) => entryOf(user, space) !== undefined

// Chooses what the space keeps. `null` is nothing: the copy is deleted.
export async function configure(
  user: string,
  space: string,
  cfg: OfflineConfig | null,
): Promise<OfflineStore | undefined> {
  if (cfg === null) {
    await dropCopy(user, space)
    return undefined
  }
  const store = await storeFor(user, space)
  await store.setConfig({ ...DEFAULT_CONFIG, ...cfg })
  await store.refreshRegistry()
  return store
}

export async function dropCopy(user: string, space: string) {
  const k = keyOf(user, space)
  const p = open.get(k)
  open.delete(k)
  if (p) {
    try {
      await (await p).destroy()
      return
    } catch {
      // Not openable: deleted by name below.
    }
  }
  await deleteDb(dbName(user, space))
  dropEntry(user, space)
}

// Signing out (or offline copies turned off): everything this account keeps on the device goes, the databases that
// the registry does not know of included (it lives in localStorage, which can be cleared apart from IndexedDB).
export async function clearAccount(user: string) {
  const names = new Set(spacesOf(user).map((s) => dbName(user, s)))
  if (typeof indexedDB.databases === 'function') {
    try {
      for (const d of await indexedDB.databases())
        if (d.name?.startsWith(`${DB_PREFIX}:${encodeURIComponent(user)}:`)) names.add(d.name)
    } catch {
      // A browser that cannot list databases: the registry's are deleted.
    }
  }
  for (const [k, p] of [...open]) {
    if (k.startsWith(`${user}\u0000`)) {
      open.delete(k)
      // A sync under way is stopped and waited out before its database is deleted.
      await p.then((s) => s.closeAndWait()).catch(() => undefined)
    }
  }
  for (const n of names) await deleteDb(n).catch(() => undefined)
  for (const s of spacesOf(user)) dropEntry(user, s)
}

// For tests: forget the open stores (their databases are closed by the caller).
export function forgetOpenStores() {
  open.clear()
}
