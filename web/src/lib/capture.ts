// The text of a quick note that was typed and not saved, kept in the browser
// per person and space, so closing the box by accident loses nothing.

// `user` is the signed-in user's id; single-user mode has none.
export function captureKey(user: string | null | undefined, space: string): string {
  return `pb.capture.${user ?? 'local'}.${space}`
}

export function loadCapture(key: string): string {
  try {
    return localStorage.getItem(key) ?? ''
  } catch {
    return ''
  }
}

// An empty text is no draft: the entry is removed.
export function saveCapture(key: string, text: string): void {
  try {
    if (text === '') localStorage.removeItem(key)
    else localStorage.setItem(key, text)
  } catch {
    // Storage blocked: the text lives in the open box only.
  }
}

// What is stored for a note: no trailing blank space, one final line break, as
// a text file has it. Null when there is nothing to save.
export function noteBody(text: string): string | null {
  const body = text.replace(/\s+$/, '')
  return body.trim() === '' ? null : `${body}\n`
}
