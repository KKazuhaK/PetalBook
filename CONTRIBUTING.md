# 参与开发 / Contributing

## 推送之前 / Before you push

```bash
make check
```

跑拉取请求（PR）会跑的全部检查：gofmt、`go mod tidy` 是否干净、`go vet`、
SQLite 上的 Go 测试（含 `-race`）、govulncheck、网页端的类型检查、lint 和测试，
以及工作流文件的 actionlint。不需要 PostgreSQL、root 或 Docker；依赖已装好时大约两分钟。

Runs everything a pull request must pass: gofmt, a clean `go mod tidy`,
`go vet`, the Go tests on SQLite with `-race`, govulncheck, the web typecheck,
lint and tests, and actionlint on the workflow files. It needs no PostgreSQL, root or
Docker, and takes about two minutes once dependencies are installed.

改动存储、文件名处理、构建或发布流程之前，再跑一遍完整的：
Before a risky change (storage, file names, build, release), run the full set:

```bash
make check-full
```

它在 `check` 之外还有 PostgreSQL 测试（需要 `initdb`、`pg_ctl`、`psql`）、
大小写折叠的 tmpfs 上的测试（只在 Linux 上，内核 6.13 以上；CI 用 ext4 镜像代替）、`make release`、
契约测试和六个平台的交叉编译。
That adds the PostgreSQL tests (needs `initdb`, `pg_ctl` and `psql`), the tests on
a case-folding tmpfs (Linux only, kernel 6.13 or newer; CI uses an ext4 image instead), `make release`, the
contract tests, and a cross-compile for six platforms.

每一项检查都只定义在 `Makefile` 里，CI 只是调用这些目标，所以本地通过的，CI 上也通过。
Every check is defined in the `Makefile` only; CI just calls those targets.

## 什么在哪里跑 / What runs where

- **拉取请求 / Pull request**：`make check-go` 和 `make vuln`（只在改了 Go 相关文件时）、
  `make check-web`（只在改了网页端、`Makefile` 或 `.github/` 时）、actionlint。
  Go and web jobs are skipped when a change cannot affect them.
- **合并到 main / Push to main**：以上全部，再加 PostgreSQL、大小写折叠、契约测试、
  单文件构建（`make release`）加交叉编译、容器镜像构建。
  Everything above, plus PostgreSQL, case folding, contract tests, the
  single-binary build with cross-compile, and the image build.
- **发布 / Release**（推送 `v*` 标签）：标签必须在 main 上，且 ci 在该提交上已通过；
  然后在 Linux amd64、arm64 和 macOS 上再跑 `make check-go`，构建四个平台的二进制和
  多架构镜像（签名并带来源证明），最后才公开草稿发布。
  The tag must be on main with a passing ci run; `make check-go` runs again on
  Linux amd64, Linux arm64 and macOS; binaries and a signed, attested multi-arch
  image are built; the release is a draft until the very last step.

## 分支与合并 / Branches and merging

main 受保护：所有改动都通过拉取请求进入，唯一必需的检查是 `ci`
（它汇总上面所有任务，被跳过的任务不算失败）。
`main` is protected. Every change goes through a pull request, and the one
required check is `ci`, which gathers all the jobs above (a skipped job is not
a failure).

## 版本 / Versioning

语义化版本，标签形如 `vX.Y.Z`，预发布 `vX.Y.Z-rc.N`。目前是 0.x：
存储格式和 API 仍可能变化，所以次版本号之间不保证兼容。
SemVer tags: `vX.Y.Z`, and `vX.Y.Z-rc.N` for pre-releases. The project is 0.x:
the storage format and the API may still change, so minor versions do not
promise compatibility.
