import type { GitHubManifestStart } from '@/api/types'

// The manifest flow has the browser, not the server, post the manifest to GitHub: GitHub shows the person what
// will be made and asks them to confirm, then sends the browser back to the server with a code.
// Only an https address (or a loopback one, for tests) is posted to.
export function postManifest(start: GitHubManifestStart): void {
  const url = new URL(start.url)
  const loopback = url.hostname === 'localhost' || url.hostname === '127.0.0.1' || url.hostname === '[::1]'
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback))
    throw new Error('the manifest address is not https')
  const form = document.createElement('form')
  form.method = 'POST'
  form.action = url.toString()
  form.hidden = true
  const field = document.createElement('input')
  field.type = 'hidden'
  field.name = 'manifest'
  field.value = JSON.stringify(start.manifest)
  form.append(field)
  document.body.append(form)
  form.submit()
}
