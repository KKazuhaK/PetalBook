# 参与开发 / Contributing

## 推送之前 / Before you push

```bash
make check
```

跑拉取请求（PR）会跑的全部检查：gofmt、`go mod tidy` 是否干净、`go vet`、
SQLite 上的 Go 测试（含 `-race`）、govulncheck、网页端的类型检查、lint 和测试，
以及工作流文件的 actionlint。不需要 PostgreSQL、root 或 Docker。
Go 和网页端两半并行跑，每个目标的输出在它结束时整段打印，最后有一张每个目标耗时的表。

Runs everything a pull request must pass: gofmt, a clean `go mod tidy`,
`go vet`, the Go tests on SQLite with `-race`, govulncheck, the web typecheck,
lint and tests, and actionlint on the workflow files. It needs no PostgreSQL, root or
Docker. The Go and web halves run side by side (`scripts/lanes.sh`); each target's output is
printed whole when it ends, and a table of how long each target took closes the run.

改动存储、文件名处理、构建或发布流程之前，再跑一遍完整的：
Before a risky change (storage, file names, build, release), run the full set:

```bash
make check-full
```

它在 `check` 之外还有不带 `-race` 的完整规模 Go 测试（`make test-full`）、
PostgreSQL 测试（只跑用到状态数据库的包；需要 `initdb`、`pg_ctl`、`psql`）、
大小写折叠的 tmpfs 上的测试（只在 Linux 上，内核 6.13 以上；CI 用 ext4 镜像代替）、`make release`、
契约测试（SQLite 和 PostgreSQL 各跑一遍账号模式，`make contract-postgres`）和六个平台的交叉编译。
That adds the Go tests without `-race` at their full size (`make test-full`), the PostgreSQL tests (only the
packages that use the state database; needs `initdb`, `pg_ctl` and `psql`), the tests on
a case-folding tmpfs (Linux only, kernel 6.13 or newer; CI uses an ext4 image instead), `make release`, the
contract tests (accounts mode runs on SQLite and on PostgreSQL, `make contract-postgres`), and a cross-compile for six platforms.
这些也并行跑：检查、完整规模测试、PostgreSQL 和大小写折叠一起，然后是 release 和契约测试，与交叉编译一起。
They run in lanes too: the checks, the full-size tests, PostgreSQL and case folding together, then the release build
with the contract tests, beside the cross-compile.

每一项检查都只定义在 `Makefile` 里，CI 只是调用这些目标，所以本地通过的，CI 上也通过。
Every check is defined in the `Makefile` only; CI just calls those targets.

## 什么在哪里跑 / What runs where

- **拉取请求 / Pull request**：Go 分三台机器并行：`make test-index`、`make test-space`，
  以及 `make check-go-rest`（gofmt、`go mod tidy`、`go vet` 和其余各包的测试）加 `make vuln`
  （只在改了 Go 相关文件时）；网页端两台：`make check-web-static`（类型检查和 lint）和
  `make web-test`（只在改了网页端、`Makefile` 或 `.github/` 时）；再加 actionlint。
  三台 Go 机器合起来就是 `make test` 加上 `make check-go-rest` 里的检查。
  Go runs on three machines, side by side: `make test-index`, `make test-space`, and
  `make check-go-rest` (gofmt, `go mod tidy`, `go vet` and the tests of every other package) with `make vuln`
  (only when a change can affect Go); the web app on two: `make check-web-static` (typecheck and lint) and
  `make web-test` (only when it changed the web app, the `Makefile` or `.github/`); and actionlint.
  The three Go machines together are `make test` plus the checks in `make check-go-rest`.
- **合并到 main / Push to main**：以上全部，再加不带 `-race` 的完整规模测试（`make test-full`）、
  PostgreSQL（只跑用到状态数据库的包，含 PostgreSQL 上的契约测试）、大小写折叠、契约测试、
  单文件构建（`make release`）加交叉编译、容器镜像构建。
  Everything above, plus the full-size tests without `-race` (`make test-full`), PostgreSQL (only the packages that
  use the state database, including the contract tests on it), case folding, contract tests, the
  single-binary build with cross-compile, and the image build.
- **发布 / Release**（推送 `v*` 标签）：标签必须在 main 上，且 ci 在该提交上已通过；
  然后在 Linux amd64、arm64 和 macOS 上再跑 `make check-go`，构建四个平台的二进制和
  多架构镜像（签名并带来源证明），最后才公开草稿发布。
  The tag must be on main with a passing ci run; `make check-go` runs again on
  Linux amd64, Linux arm64 and macOS; binaries and a signed, attested multi-arch
  image are built; the release is a draft until the very last step.

## 测试的规模与速度 / Test size and speed

少数压力测试和模型测试在 `-race` 下用较小的规模（见 `internal/index`、`internal/space` 里的 `raceDetector`），
完整规模由不带 `-race` 的 `make test-full` 跑，它在 main 上必过。新增这类测试时照此办理：
两种规模都要有。会修改包级变量的测试不能并行；其余独立的测试用 `t.Parallel()`，
但测耗时或内存的（`timed`、`bounded`）要保持串行。等待不要用真实时间：用可配置的短间隔
（例如 `spaces.Options.CloseWait`），不要用 `time.Sleep` 去赌。网页端的模型测试拆在几个
`src/editor/model.*.test.ts` 里（共用 `modelHarness.ts`），好让 vitest 并行跑它们。

A few stress and model tests use a smaller size under `-race` (see `raceDetector` in `internal/index` and
`internal/space`), and the full size runs in `make test-full`, without it, which must pass on main. A new test of that
kind does the same: both sizes. A test that changes a package variable cannot run in parallel; the others that
share nothing use `t.Parallel()`, but those that measure time or memory (`timed`, `bounded`) stay serial.
Do not wait in real time: use a short, configurable interval (for example `spaces.Options.CloseWait`), and no
`time.Sleep` that bets on a duration. The web model test is split over the `src/editor/model.*.test.ts` files
(sharing `modelHarness.ts`) so that vitest runs them side by side.

`make test-index`, `make test-space` and `make test-rest` are `make test` in three parts, for CI;
`make check-go` is all of it, and is what to run on one machine.

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
