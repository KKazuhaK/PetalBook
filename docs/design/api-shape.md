# API shape

## 9. API 形状

REST，前端只通过它与后端交互，将来的客户端复用。

- `GET /api/spaces`，`POST /api/spaces`
- `GET /api/spaces/{s}/tree`：文件夹树
- `GET /api/spaces/{s}/pages?folder=&tag=&type=&q=`
- `GET /api/spaces/{s}/pages/{id}`：返回内容和当前版本
- `PUT /api/spaces/{s}/pages/{id}/draft`：带 `baseRev`
- `POST /api/spaces/{s}/pages/{id}/commit`
- `GET /api/spaces/{s}/pages/{id}/history`
- `POST /api/spaces/{s}/assets`
- `GET /api/spaces/{s}/changes?since={rev}`：某版本之后改了什么（同步用）
- `GET /api/search?q=`：跨当前用户可见的所有空间

`baseRev` 和 `changes` 从第一期就按离线同步的需要设计，即使当时只有在线
网页在用。
