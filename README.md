# PetalBook

自托管的个人写作与笔记应用：内容是 Git 仓库里的 Markdown 文件，通过网页
所见即所得地编辑，不需要懂 Markdown。

A self-hosted writing and notes app. Pages are Markdown files in a Git
repository, edited in the browser without needing to know Markdown.

**状态：早期开发中。** 第 1 期（自己能日用）的功能都已做完：账号和空间、
跟随远端 Git 仓库、草稿与版本历史、图片附件、卡片视图、快速记录、三种编辑
模式、Markdown 导入、中英文界面。还没有经过长时间的实际使用。

**Status: early.** Everything planned for the first phase is in place:
accounts and spaces, following a remote Git repository, drafts and page
history, attachments, card view, quick notes, three ways to edit, Markdown
import, and a Chinese and English interface. It has not seen long real use yet.

## 运行 / Run

**一个程序（含网页端）/ One program, web app included.** 需要 Go 和 Node.js：

```bash
make release
```

```bash
./petalbook serve --space ~/notes
```

然后打开 http://127.0.0.1:8080 。网页端已嵌入程序，单用户模式只监听本机。
没有 Node.js 时 `make build` 只编译 API，此时 `/` 会提示网页端未内置。
The web app is embedded in the binary; `make build` (no Node.js) builds the API only.

多用户模式用 `--data` 代替 `--space`：第一次启动时日志里会给出创建管理员的链接，
没人能登录时可以用 `petalbook user add|passwd|list` 直接改数据目录。状态库默认是
SQLite，设置 `PETALBOOK_DATABASE` 可以换成 PostgreSQL。`petalbook serve --help`
列出全部选项（每个选项也可以用 `PETALBOOK_` 加大写选项名的环境变量设置）。

```bash
./petalbook serve --data ~/petalbook-data --public-url https://notes.example.com
```

**第一次启动的预设 / First-start profile.** `--profile cn` 只在状态库为空（第一次启动）时写入一组更严格的站点默认值：
对外共享（API 令牌、MCP 等让内容离开组织的功能）默认关闭，GitHub 保持开启（国内可访问，只是慢）。
这些只是普通的站点默认值，管理员之后可以逐项修改；之后不能切换预设，状态库已建好时同样的 `--profile` 只记一行日志，不同的会被拒绝。
`--profile cn` writes a stricter set of site defaults once, when the state database is empty (the first start):
sharing that lets content leave the organisation (API tokens, MCP) is off by default, and GitHub stays on (reachable
from China, only slow). They are ordinary site values that administrators can change one by one; profiles cannot be
switched later: on a database that is already set up the same `--profile` is ignored with a log line, a different one is refused.

```bash
./petalbook serve --data ~/petalbook-data --public-url https://notes.example.com --profile cn
```

**PostgreSQL.** 给 PetalBook 单独建一个库，用内置的 `C.UTF-8` 区域（PostgreSQL 17 起），
这样排序和大小写都不随操作系统的区域走，和 SQLite 一致；库的区域不是 C 时启动日志会警告
（表里的文本列都声明了 `COLLATE "C"`，数据照样对，但别的地方会受区域影响）。
Make a database of its own with the builtin `C.UTF-8` locale (PostgreSQL 17 and later), so that sorting
and case do not follow the operating system's locale; the server warns at start when the database's
locale is not C (every text column is `COLLATE "C"`, so the data is right anyway).

```sql
CREATE DATABASE petalbook OWNER petalbook TEMPLATE template0 ENCODING 'UTF8' LOCALE_PROVIDER builtin BUILTIN_LOCALE 'C.UTF-8';
```

```bash
PETALBOOK_DATABASE='postgres://petalbook:…@db.example.com/petalbook' ./petalbook serve --data ~/petalbook-data
```

（PostgreSQL 16 及更早：`CREATE DATABASE petalbook TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C.UTF-8'`，
或 `LOCALE 'C'`。/ On 16 and earlier use `LC_COLLATE 'C'`.)

正式发布之前，状态库的结构只有一个基线（`internal/store/schema.sql`），改动直接改它，不写迁移；
库里记着它是哪个基线建的，用别的基线建的开发库、演示库启动时会被拒绝，要删掉重建。
Until the first release the state database has one baseline schema, edited in place with no migrations;
a development or demonstration database made from another baseline is refused at start and must be made again.

**容器镜像 / Container image.** 镜像里有 `git` 和 `ssh`，以非 root 用户运行，数据放在卷 `/data`，
默认命令是 `petalbook serve --data /data --listen :8080`（多用户模式）。
`--public-url` 是人们在浏览器里打开的地址，必须给（可以用环境变量 `PETALBOOK_PUBLIC_URL`），
否则浏览器的请求会被当作跨站请求拒绝；放在 https 反向代理后面时写代理的地址。
The default command is accounts mode on port 8080; set the address people open as `--public-url`.

```bash
docker build -t petalbook .
```

```bash
docker run -d --name petalbook -p 8080:8080 -v petalbook-data:/data \
  -e PETALBOOK_PUBLIC_URL=https://notes.example.com petalbook
```

换命令时把参数写在镜像名后面，比如 `docker run --rm -v petalbook-data:/data petalbook user list --data /data`；
要把宿主机目录挂到 `/data`，用 `--user` 指定能写它的用户。发布版本的二进制和镜像由 `v*` 标签触发
（`.github/workflows/release.yml`）。

**开发 / Development.** API 和网页端分开启动，网页端的开发服务器会把 `/api` 转到 API：

```bash
make run SPACE=~/notes
```

```bash
npm --prefix web install
```

```bash
npm --prefix web run dev
```

然后打开 http://localhost:5173 。网页端可以安装成应用（浏览器的「安装」或「添加到主屏幕」），
不支持离线使用。

- [设计与分期计划](docs/design.md)
- [HTTP 接口](docs/api.md)
- [参与开发 / Contributing](CONTRIBUTING.md)：推送前跑 `make check` / run `make check` before pushing
- 验证
  - [Markdown 往返与浏览器编辑](spikes/markdown-roundtrip/README.md)
  - [搜索与列表的规模](spikes/search-scale/README.md)
  - [Go 端渲染与前端的一致性](spikes/render-parity/README.md)

PetalBook 是 [Rackbook](https://github.com/KazuhaHub/rackbook) 的姊妹项目，
登录协议层共用 [authcore](https://github.com/KazuhaHub/authcore)。

## 许可证

[AGPL-3.0](LICENSE)
