# PetalBook

自托管的个人写作与笔记应用：内容是 Git 仓库里的 Markdown 文件，通过网页
所见即所得地编辑，不需要懂 Markdown。

A self-hosted writing and notes app. Pages are Markdown files in a Git
repository, edited in the browser without needing to know Markdown.

**状态：早期开发中。** 里程碑 1.1（单用户、本地空间、不登录）和 1.2
（账号登录、多个空间、草稿存库）可以运行。远端同步、图片和历史
记录还没有做。

启动服务（会先编译）：

```bash
make run SPACE=~/notes
```

多用户模式用 `--data` 代替 `--space`：第一次启动时日志里会给出创建管理员的链接，
没人能登录时可以用 `petalbook user add|passwd|list` 直接改数据目录。状态库默认是
SQLite，设置 `PETALBOOK_DATABASE` 可以换成 PostgreSQL。`petalbook serve --help`
列出全部选项。

```bash
./petalbook serve --data ~/petalbook-data --public-url https://notes.example.com
```

网页端目前要单独启动，之后会打包进同一个程序：

```bash
npm --prefix web install
```

```bash
npm --prefix web run dev
```

然后打开 http://localhost:5173 。

- [设计与分期计划](docs/design.md)
- [HTTP 接口](docs/api.md)
- 验证
  - [Markdown 往返与浏览器编辑](spikes/markdown-roundtrip/README.md)
  - [搜索与列表的规模](spikes/search-scale/README.md)
  - [Go 端渲染与前端的一致性](spikes/render-parity/README.md)

PetalBook 是 [Rackbook](https://github.com/KazuhaHub/rackbook) 的姊妹项目，
登录协议层共用 [authcore](https://github.com/KazuhaHub/authcore)。

## 许可证

[AGPL-3.0](LICENSE)
