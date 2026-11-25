# 参与开发 / Contributing

## 推送之前 / Before you push

```bash
make hooks   # 只需一次 / once per clone
make check
```

`make hooks` 打开 `.githooks/pre-commit`：提交时对暂存的 Go 文件跑 `gofmt -w`，
对暂存的网页端文件先跑 `prettier --write` 再跑 `eslint --fix`，并把结果重新暂存（只暂存了一部分的文件只检查，不改写）。
`make hooks` turns on `.githooks/pre-commit`: on commit it runs `gofmt -w` on the staged Go
files, and `prettier --write` then `eslint --fix` on the staged web files, and stages the result (a file that is
staged in part is only checked, never rewritten).

`make check` 跑拉取请求（PR）会跑的全部检查：gofmt、`go mod tidy` 是否干净、`go vet`、
SQLite 上的 Go 测试（含 `-race`）、govulncheck、网页端的类型检查、lint 和测试、
工作流文件的 actionlint，以及对整个 git 历史的密钥扫描（gitleaks）。不需要 PostgreSQL、root 或 Docker。
各部分并行跑，每个目标的输出在它结束时整段打印，最后有一张每个目标耗时的表。

`make check` runs everything a pull request must pass: gofmt, a clean `go mod tidy`,
`go vet`, the Go tests on SQLite with `-race`, govulncheck, the web typecheck,
lint and tests, actionlint on the workflow files, and a secret scan (gitleaks) of the
whole git history. It needs no PostgreSQL, root or Docker. The parts run side by side
(`scripts/lanes.sh`); each target's output is printed whole when it ends, and a table of
how long each target took closes the run.

改动存储、文件名处理、构建或发布流程之前，再跑一遍完整的：
Before a risky change (storage, file names, build, release), run the full set:

```bash
make check-full
```

它在 `check` 之外还有 PostgreSQL 测试（只跑用到状态数据库的包；需要 `initdb`、`pg_ctl`、`psql`）、
大小写折叠的 tmpfs 上的测试（只在 Linux 上，内核 6.13 以上；CI 用 ext4 镜像代替）、两个平台的交叉编译（发布时才构建全部平台）、
`make release`，以及契约测试（SQLite 和 PostgreSQL 各跑一遍账号模式，`make contract-postgres`）。
That adds the PostgreSQL tests (only the packages that use the state database; needs `initdb`, `pg_ctl`
and `psql`), the tests on a case-folding tmpfs (Linux only, kernel 6.13 or newer; CI uses an ext4 image
instead), a cross-compile for two platforms (a release builds them all), `make release`, and the contract tests (accounts mode runs on
SQLite and on PostgreSQL, `make contract-postgres`).

每一项检查都只定义在 `Makefile` 里，CI 只是调用这些目标，所以本地通过的，CI 上也通过。
Every check is defined in the `Makefile` only; CI just calls those targets.

## 什么在哪里跑 / What runs where

- **拉取请求 / Pull request**：四个任务。`lint`（`make lint`：gofmt、`go mod tidy`、`go vet`、网页端类型检查和
  ESLint、actionlint、govulncheck，以及对这个 PR 的提交做 gitleaks 扫描）总是跑；`go`（`make test`，全部 Go
  测试，带 `-race`）和 `postgres`（见下）只在改了 Go 相关文件时跑；`web`（`make web-test`）只在改了网页端、`Makefile` 或 `.github/` 时跑。
  Four jobs. `lint` (`make lint`: gofmt, `go mod tidy`, `go vet`, the web typecheck and ESLint, actionlint,
  govulncheck, and a gitleaks scan of the pull request's commits) always runs; `go` (`make test`, all the Go
  tests with `-race`) and `postgres` (below) only when a change can affect Go; `web` (`make web-test`) only when it changed the web
  app, the `Makefile` or `.github/`.
- **合并到 main / Push to main**：以上几个，再加 `compat` 和 `release`；`postgres` 总是跑。`postgres`（`make test-postgres`，只跑用到状态数据库的包的 PostgreSQL 那一半，
  再加 `make contract` 和 `make contract-postgres` 两种契约测试）、`compat`（大小写折叠的文件系统上的测试，
  交叉编译，以及不带 `-race` 的 `make test-fullsize`，即全尺寸的压力测试）、`release`（`make release` 单文件构建，加容器镜像构建）。
  Those, plus `compat` and `release`; `postgres` always runs. `postgres` (`make test-postgres`, the PostgreSQL half of the packages that use the state database,
  then the contract tests on SQLite and on PostgreSQL), `compat` (the tests on a case-folding file system, the
  cross-compile, and `make test-fullsize`: the full-size stress tests, without `-race`), and `release` (the single-binary build `make release`, and the image build).
- **发布 / Release**（推送 `v*` 标签）：标签必须在 main 上，且 ci 在该提交上已通过；
  然后在 Linux amd64、arm64 和 macOS 上再跑 `make check-go`，构建四个平台的二进制和
  多架构镜像（签名并带来源证明），最后才公开草稿发布。
  The tag must be on main with a passing ci run; `make check-go` runs again on
  Linux amd64, Linux arm64 and macOS; binaries and a signed, attested multi-arch
  image are built; the release is a draft until the very last step.

## 密钥扫描 / Secret scan

`make secrets` 用固定版本的 gitleaks 扫描 git 历史，输出已遮蔽。测试里故意放的假密钥写在
`.gitleaks.toml` 里，每条只对应一条规则和一个文件；新增这类测试数据时，先想想能不能写得不像密钥，
不行再往那里加一条。
`make secrets` scans the git history with a pinned gitleaks and redacts its output. The made-up keys that
tests hold on purpose are listed in `.gitleaks.toml`, each for one rule and one file; when a new test needs
one, first see whether it can be written so that it does not look like a key, and only then add an entry.

## 测试的规模与速度 / Test size and speed

少数压力测试和模型测试在 `-race` 下用较小的规模（见 `internal/index`、`internal/space` 里的 `raceDetector`）；
CI 和 `make check` 都带 `-race`，所以跑的就是这个规模。需要完整规模时在本地跑
`go test ./internal/index ./internal/space`。新增这类测试时照此办理：两种规模都要有。
会修改包级变量的测试不能并行；其余独立的测试用 `t.Parallel()`，
但测耗时或内存的（`timed`、`bounded`）要保持串行。等待不要用真实时间：用可配置的短间隔
（例如 `spaces.Options.CloseWait`），不要用 `time.Sleep` 去赌。网页端的模型测试拆在几个
`src/editor/model.*.test.ts` 里（共用 `modelHarness.ts`），好让 vitest 并行跑它们。

A few stress and model tests use a smaller size under `-race` (see `raceDetector` in `internal/index` and
`internal/space`); CI and `make check` both use `-race`, so that is the size they run. For the full size run
`make test-fullsize` (that is `go test ./internal/index ./internal/space` without `-race`); the `compat` job
runs it on every push to main. A new test of that kind does the same: both sizes. A test
that changes a package variable cannot run in parallel; the others that
share nothing use `t.Parallel()`, but those that measure time or memory (`timed`, `bounded`) stay serial.
Do not wait in real time: use a short, configurable interval (for example `spaces.Options.CloseWait`), and no
`time.Sleep` that bets on a duration. The web model test is split over the `src/editor/model.*.test.ts` files
(sharing `modelHarness.ts`) so that vitest runs them side by side.

`make test` is all the Go tests; `make check-go` adds gofmt, `go mod tidy` and `go vet`, and is what a
release runs on each platform.

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
