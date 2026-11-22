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
- 验证
  - [Markdown 往返与浏览器编辑](spikes/markdown-roundtrip/README.md)
  - [搜索与列表的规模](spikes/search-scale/README.md)
  - [Go 端渲染与前端的一致性](spikes/render-parity/README.md)

PetalBook 是 [Rackbook](https://github.com/KazuhaHub/rackbook) 的姊妹项目，
登录协议层共用 [authcore](https://github.com/KazuhaHub/authcore)。

## 许可证

[AGPL-3.0](LICENSE)
