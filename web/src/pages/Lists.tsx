import { useParams, useSearchParams } from 'react-router'
import { PageListView } from '@/components/PageListView'
import { paramsFromQuery } from '@/components/search/searchModel'

export function RecentList() {
  return <PageListView source={{ kind: 'recent' }} />
}

// `f/*` and `t/*` carry the path as a splat: the root folder is "/f" with an
// empty splat, and a tag may itself contain slashes.
export function FolderList() {
  const folder = useParams()['*'] ?? ''
  return <PageListView source={{ kind: 'folder', folder }} />
}

export function TagList() {
  const tag = useParams()['*'] ?? ''
  return <PageListView source={{ kind: 'tag', tag }} />
}

// The full results page of the search dialog: the text, and the filters the
// dialog had, in the address.
export function SearchList() {
  const [params] = useSearchParams()
  const { text, extra } = paramsFromQuery(params)
  return <PageListView source={{ kind: 'search', q: text, filters: extra }} />
}
