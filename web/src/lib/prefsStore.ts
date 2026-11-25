// Where a person's own preferences for a screen are kept (the columns of a table, their widths, the sort): behind one
// interface so the place can change. Today it is this browser (localStorage, every access wrapped, since a private
// window or a full disk makes it throw); the plan is the person's own personal space, a file `.petalbook/prefs.yml`
// written in batches by quiet commits that page history hides, with this browser as the fallback when there is no
// personal space or nobody has signed in yet. Nothing of it is ever in a database.

export interface PrefsStore {
  // The saved value of a key, or null when there is none or it cannot be read.
  read(key: string): unknown
  write(key: string, value: unknown): void
  remove(key: string): void
}

export const browserPrefs: PrefsStore = {
  read(key) {
    try {
      const raw = localStorage.getItem(key)
      return raw === null ? null : (JSON.parse(raw) as unknown)
    } catch {
      return null
    }
  },
  write(key, value) {
    try {
      localStorage.setItem(key, JSON.stringify(value))
    } catch {
      // The choice holds until the page is closed.
    }
  },
  remove(key) {
    try {
      localStorage.removeItem(key)
    } catch {
      // Nothing to forget.
    }
  },
}

let current: PrefsStore = browserPrefs
export const prefsStore = (): PrefsStore => current
// For the day the personal repository can hold them, and for tests.
export const setPrefsStore = (store: PrefsStore): void => {
  current = store
}
