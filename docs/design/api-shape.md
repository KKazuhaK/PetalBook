# API shape

## 9. API shape

REST. The frontend talks to the backend only through it, and future clients
reuse it.

- `GET /api/spaces`, `POST /api/spaces`
- `GET /api/spaces/{s}/tree`: the folder tree
- `GET /api/spaces/{s}/pages?folder=&tag=&type=&q=`
- `GET /api/spaces/{s}/pages/{id}`: returns the content and the current version
- `PUT /api/spaces/{s}/pages/{id}/draft`: carries `baseRev`
- `POST /api/spaces/{s}/pages/{id}/commit`
- `GET /api/spaces/{s}/pages/{id}/history`
- `POST /api/spaces/{s}/assets`
- `GET /api/spaces/{s}/changes?since={rev}`: what changed after a given
  version (used for sync)
- `GET /api/search?q=`: across all spaces visible to the current user

`baseRev` and `changes` are designed from the first phase for offline sync,
even though only the online web app uses them at that point.
