// A per-browser preference: upload pictures as they are, without compressing them.
// Off by default. Stored in localStorage; where that is not available the
// preference simply stays off.

export const UPLOAD_ORIGINAL_KEY = 'pb.upload.original'

export function getUploadOriginal(): boolean {
  try {
    return localStorage.getItem(UPLOAD_ORIGINAL_KEY) === '1'
  } catch {
    return false
  }
}

export function setUploadOriginal(on: boolean) {
  try {
    if (on) localStorage.setItem(UPLOAD_ORIGINAL_KEY, '1')
    else localStorage.removeItem(UPLOAD_ORIGINAL_KEY)
  } catch {
    // Not stored; the choice lasts until the page is left.
  }
}
