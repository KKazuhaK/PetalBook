// A small promise wrapper over IndexedDB: what the offline store needs and nothing more.

export const req = <T>(r: IDBRequest<T>): Promise<T> =>
  new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result)
    r.onerror = () => reject(r.error ?? new Error('IndexedDB request failed'))
  })

// Resolves when the transaction has committed; rejects when it aborted.
export const committed = (tx: IDBTransaction): Promise<void> =>
  new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error ?? new Error('IndexedDB transaction failed'))
    tx.onabort = () => reject(tx.error ?? new Error('IndexedDB transaction aborted'))
  })

export function openDb(name: string, version: number, upgrade: (db: IDBDatabase, oldVersion: number) => void): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const r = indexedDB.open(name, version)
    r.onupgradeneeded = (e) => upgrade(r.result, e.oldVersion)
    r.onsuccess = () => resolve(r.result)
    r.onerror = () => reject(r.error ?? new Error('IndexedDB could not be opened'))
    r.onblocked = () => reject(new Error('IndexedDB is blocked by another tab'))
  })
}

export function deleteDb(name: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const r = indexedDB.deleteDatabase(name)
    r.onsuccess = () => resolve()
    r.onerror = () => reject(r.error ?? new Error('IndexedDB could not be deleted'))
    // Another tab holds it open; it closes when that tab next looks (versionchange), so wait for it.
    r.onblocked = () => undefined
  })
}

// Every key and value of a store, one batch of `step` at a time (a cursor), so a store of tens of thousands of
// records is never held whole in one request.
export async function eachRecord<T>(store: IDBObjectStore | IDBIndex, fn: (value: T, key: IDBValidKey) => void | false): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const r = store.openCursor()
    r.onsuccess = () => {
      const c = r.result
      if (!c) return resolve()
      if (fn(c.value as T, c.primaryKey) === false) return resolve()
      c.continue()
    }
    r.onerror = () => reject(r.error ?? new Error('IndexedDB cursor failed'))
  })
}
