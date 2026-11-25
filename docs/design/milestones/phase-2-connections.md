# Milestones: phase 2, connections and sign-in (2.7, 2.8, 2.7b)

**2.7 GitHub App、用户授权、自动建仓库**

- 新建空间时选「在 GitHub 上建一个仓库」：组织下用 GitHub App 的安装令牌，
  个人账号下用 GitHub 用户授权（网页授权，存刷新令牌并自动续期）。
- 4.3 里文档没写清的两点，已在 2026-11-23 用真实 GitHub 实测，结论写在 4.3。
- 凭据照旧加密存库，令牌只经环境变量交给 `git`。

**2.7 的约定（2026-11-23）**：接口和错误码先写进 `docs/api.md` 的「GitHub」
一节、「Spaces」的 `POST /api/spaces`、「Remote repository」的 Remote 对象和
`PUT …/remote`，以及「Administration」的「GitHub App」，前后端照它并行做。

- **一个服务器一个 App，只支持 github.com**。App 的配置存状态库（不用命令行
  参数，和 2.8 把 OIDC 搬进管理后台的方向一致）：App ID、私钥、client id、
  client secret；私钥和 secret 用服务器密钥加密（`Seal("site", …)`），任何
  接口都不返回，只显示「已保存」和私钥公钥部分的指纹。保存前向 GitHub 核对
  （`GET /app` 验私钥和 App ID，用一个编造的令牌问
  `POST /applications/{client_id}/token` 验 client id 和 secret，`404` 表示
  这对是对的），核对不过就不存。App 缺少的权限（Contents 写、Metadata 读、
  Administration 写）只提示不拒绝。换成另一个 App 时，只要还有空间或用户在用
  就拒绝（`409 provider_in_use`）；同一个 App 换私钥或 secret 随时可以。
- **令牌**：照搬 Rackbook `internal/githubapp`。安装令牌只在内存里，同步用的
  按单个仓库收窄（`repository_ids`、`contents: write`），建仓库用的是
  `administration: write`、不限仓库、只为这一次请求；到期前 5 分钟才换，同一
  令牌的并发请求合成一次，网络请求时不持锁，缓存最多 1 万个。交给 `git` 的
  方式和现有的 `token` 凭据完全一样（环境变量里的 credential helper，限定在
  `https://github.com`），所以 `gitstore` 不用改。只有打开着的空间会被拉取，
  一个在用的空间最多每 55 分钟换一次令牌，关着的空间不花钱。
- **用户授权**：网页授权，`state` 加 PKCE（S256）。回调和 OIDC 回调一样是
  跨站规则的例外，要求是同一个用户的会话。每个 PetalBook 用户最多一个连接，
  一个 GitHub 账号只能连一个 PetalBook 用户。访问令牌和刷新令牌分别加密存库；
  访问令牌在快到期（5 分钟内）用到时续期，每个用户同一时间只续一次，新的一对
  先写库再用（GitHub 每次续期都换掉刷新令牌，旧的立即失效）；每天把 30 天内
  要过期的刷新令牌续一次，所以几个月不打开空间的人也不会掉线。刷新令牌过期
  或被撤销时，连接变成 `expired`，用它同步的空间进入 `error`、错误码
  `connection_expired`：读照常，写回 `503` 不改动任何东西，草稿照存，
  重新连接后立即重试并补提交，不丢东西。断开连接时尽力向 GitHub 撤销授权，
  库里和内存里的令牌无论如何都删掉。
- **谁的令牌建仓库**：能用用户自己的令牌就用（个人账号下 `POST /user/repos`、
  用户连接能看到的组织下 `POST /orgs/{org}/repos`），这样 GitHub 自己会检查
  这个人在这个组织里能不能建仓库、能不能建私有仓库。管理员可以把某个组织的
  安装共享给某几个租户，这些租户的成员即使没连 GitHub，也能用安装令牌在那里
  建仓库和连接仓库；默认不共享，否则任何成员都能借 App 在组织里建仓库。
- **谁的令牌同步**：组织下的仓库用安装令牌（`via: "app"`），不依赖某个人一直
  在、一直连着；个人账号下，App 装在这个账号上且能访问新仓库时也用安装令牌，
  否则用这个人的用户令牌（`via: "user"`）。用户令牌同步的空间，绑定的人不能
  登录、没了 `manage` 或断开连接后就停（`connection_missing`），
  免得被移出空间的人的令牌还在被用。
- **新建空间时建仓库**：扩展 `POST /api/spaces`，加 `storage.sync: {provider: "github", owner, name,
  private, description, create}`（2.7b 前叫 `github`），不另开接口。顺序：不联网的检查（字段、
  `--max-spaces`（3.3 起是空间数配额）、owner、令牌在不在）→ 在磁盘上建好空间（还没有记录、谁也
  看不到）→ 在 GitHub 上建空仓库（`auto_init: false`，空间的第一个提交就是
  仓库的第一个提交）→ 选定并验证同步用的凭据 → 推送 `main`（刚建的仓库有时
  几秒内找不到，「找不到」和 5xx 在 15 秒内最多试 4 次，凭据被拒不重试）→
  一个事务里写空间、所有者、远端，再让空间可见。建仓库之前失败：哪里都没留下
  东西；建仓库之后失败：删掉磁盘上的空间，GitHub 上的空仓库**留着不删**
  （从不删除远端的东西，而且可能本来就没有删除权限），错误回答里带 `repo`，
  同一个请求改成 `create: false` 就能把新空间连到这个空仓库上。进程在中间崩溃
  也一样，只是没有回答。
- **留下的空仓库有提醒（所有者定，2026-11-23）**：建仓库之后失败时，仓库照旧
  留着，同时给这个人记一条提醒，之后在账号页的 GitHub 一栏里看得到（也可以放
  在空间列表旁），写着仓库名和链接，有两个动作：「连上它再试一次」（就是用
  `POST /api/spaces` 带 `create: false`，名字和仓库取自提醒，不另开接口）和
  「我已经删了」（`DELETE /api/connections/github/leftovers/{repoId}`，没有这条提醒也回
  `204`）。按用户存：`github_leftovers(user_id, repo_id, full_name, html_url,
  space_name, created)`，主键是 `(user_id, repo_id)`，用户删了跟着删；同一个
  仓库再失败只换成新的一条；每人最多留最新的 20 条。清掉的时机：本人处理，
  或者**任何空间连上了这个仓库**（`PutRemote` 在同一个事务里按 `gh_repo_id`
  删掉所有人的这条提醒，所以重试成功、手动 `PUT …/remote` 都会清）。提醒放在
  `GET /api/connections` 的 `leftovers` 里，这个接口本来就是账号页和建空间对话框
  要读的，不多一次请求。崩溃没有回答，也记不下来，所以崩溃留下的仓库不在
  其中（见上）。
- **「仅选定仓库」的安装**：不赌 GitHub 会不会自动把 App 建的仓库加进安装。
  建完就为新仓库单独换一次令牌试；够不着、而调用者的连接能看到这个安装时，
  用用户令牌把仓库加进安装（`PUT /user/installations/{id}/repositories/{repoId}`）
  再试；还不行时，个人账号退回 `via: "user"`，组织下回
  `409 repo_not_in_installation`（带 `repo`）。Administration 是组织级
  还是仓库级：也不赌，GitHub 拒绝建仓库时原样回 `403 provider_refused`，
  `message` 转述 GitHub 的原因。两种结果下接口都不用改，实测只决定界面上
  提示怎么写、默认走哪条路。
- **已有空间切到 GitHub**：`PUT …/remote` 的凭据多一种
  `{"type": "connection", "provider": "github", "via": "app" | "user"}`；已经在跟随远端的空间 `PUT` 时
  原地换地址和凭据，同一个仓库历史相同，只换凭据，旧的密钥或令牌删掉。SSH
  形式的 github.com 地址接受，存成 https 形式。`via: "app"` 要求调用者的连接
  能推送到这个仓库或安装已共享给自己的租户，否则任何有 `manage` 的人都能把空间接到 App
  够得着的任意仓库。
- **Remote 对象**加 `errorCode`：把现有的内部分类（`auth`、`hostkey`、`clone`、
  `rescued`）露出来，再加四个 GitHub 的（`connection_expired`、
  `connection_missing`、`installation_gone`、
  `provider_not_configured`）。处于这几种状态的空间每 10 分钟重试一次，原因
  消除时（重新连接、管理员保存 App）立即重试。
- **列表都分页，从不全取**：安装列表、可建仓库的账号、可连接的仓库，每页最多
  一次 GitHub 请求（30 条），游标不透明；仓库列表 GitHub 不能按名字筛，所以
  不提供 `q`，界面上可以直接输入 `owner/name`。
- **状态库**（SQLite 和 PostgreSQL 都要测）：`remotes` 加 `cred_type = 'connection'`、`provider`
  和 `gh_via`、`gh_installation_id`、`gh_repo_id`、`gh_user_id` 几列；新表
  `github_authorizations`（每个用户一行，GitHub 账号 id 唯一，两个令牌加密，
  过期时间、状态、版本号用来做「只替换续期前的那一对」）；
  `github_installations`（安装 id、账号）和 `github_installation_tenants`（安装 ×
  租户：共享给了哪些租户）；App 的配置放 `site_settings`。
- **给 3.1 留的位置**：`GET/PUT /api/admin/storage` 的
  `remotes: {require: null | "any" | 一个提供方 id, scope: "new" | "all", graceDays}`
  和错误码 `409 remote_required` 只在 `docs/api.md` 里预留，不实现。建空间和
  建仓库已经是「要么整个成功、要么不留空间」的一步，3.1 只要在第一步前加
  检查。
- **和 3.3 的租户合在一起（2026-11-23）**：
  - App 的设置和安装列表是全站的：`/api/admin/connections/github…` 只给全站管理员，租户
    管理员和其他全站专属的接口一样得到 `403 forbidden`；管理后台的「存储与同步」
    页里的连接也只对全站管理员显示。设置照旧在 `site_settings`，没有租户层。
  - **共享安装按租户算（所有者定，2026-11-23，取代之前的全站共享）**：全站管理员
    把某个组织的安装共享给**挑出来的几个租户**（`PUT /api/admin/connections/github/installations/{id}`，
    `{tenants: [id]}`，整组替换，`[]` 不共享），只有这些租户的成员能用安装令牌在
    那里建仓库、连接仓库，也只有他们在 `GET /api/connections/github/owners` 和 `repos` 里看
    得到它；别的租户只有自己的 GitHub 连接够得着时才能用这个组织。共享的决定
    仍在全站管理员手里，租户管理员碰不到（`/api/admin/connections/github…` 全站专属）。
    存在状态库里：0013 里 `github_installations` 没有共享列，另有
    `github_installation_tenants(installation_id, tenant_id)`，主键是两列，
    `tenant_id` 有索引，安装被忘掉时（删除 App）跟着删；`tenant_id` 不是外键，
    因为 `tenants` 表在 0018 里，两个迁移顺序都要成立，删租户时由代码删它的
    共享行（`DeleteTenant`）。SQLite 和 PostgreSQL 都测。库里的查询走
    `Scope`（`ListGitHubInstallationsOf`、`GitHubInstallationOf`、
    `GitHubInstallationShared`：零值作用域什么也看不到），服务按用户所在的租户
    取作用域；作用域测试的白名单里旧的「全站共享」三条去掉，换成两条给全站
    管理员用的写和读（`SetGitHubInstallationTenants`、`GitHubInstallationTenants`），
    和 `UserTenant`；扫描的表里加了 `github_installation_tenants`。隔离测试
    加了一个真正的跨租户用例：共享给 B 之后，B 的成员看得到、能建，A 的成员
    看不到、建不了（`owner_not_allowed`）、列仓库 `404`，也没有仓库被建。
    管理后台的安装行上挑租户。
  - 用户的 GitHub 连接属于用户，所以跟着用户落在它的租户里，不另加
    `tenant_id`。一个 GitHub 账号只能连一个 PetalBook 用户，这条**跨租户**
    检查（GitHub 账号 id 在全站唯一）；回答只有 `account_in_use`，不说是谁、
    在哪个租户。
  - 带 GitHub 仓库新建的空间属于创建者的租户，和普通新建一样走 3.3 的空间数
    配额（`403 quota_exceeded`，正在建的也算，按人、按租户各记一份）和用量
    钩子。`via: "user"` 的凭据必须是空间所在租户的人：只有能打开任何空间的
    全站管理员会碰到，`PUT …/remote` 在问 GitHub 之前就回 `403 forbidden`；
    显示远端时也只在空间的租户里查这个人。
  - 状态库里跨用户的方法（每天的续期、App 自己的计数、删除 App 时撤销全部
    连接、安装列表）都是全站的，写进了作用域测试的白名单并注明原因；作用域
    测试也把 `github_authorizations`、`github_installations`、`remotes` 算进去。
  - 迁移：0013 不碰 0018 改的表（0018 给 users、spaces 加列、重建 groups），
    所以两种顺序都对：本分支上迁移过的库合并后补跑 0014、0016、0018；main 上
    迁移过的库补跑 0013。两种顺序都有测试（SQLite 和 PostgreSQL）。
  - 隔离测试里每个 2.7 的路由都有条目：`/api/admin/connections/github…` 按全站专属测，
    `/api/connections/github…` 是调用者自己的连接，或共享给调用者租户的安装。

**2.7 做出来的样子**：

- 迁移是 `0013_github.sql`（`0011_api_tokens.sql`、`0012_oauth.sql` 是 feat/mcp
  的）。迁移按各自的编号记录，缺的那几个在下次启动时补上，所以只在这个分支上
  迁移过的库，合并后会补跑 0011、0012；编号本身不能撞，所以这里用 0013。
  迁移条数的测试跟着文件走，不写死。SQLite 和 PostgreSQL 的测试都跑。
- 代码分三层：`internal/githubapp` 是对 GitHub 的客户端（JWT、安装令牌缓存、
  用户授权、REST 调用，基地址可配，默认 `api.github.com` 和 `github.com`，
  测试用 `githubapptest` 里的假 GitHub，不连真的）；`internal/github` 是服务
  器怎么用这个 App（设置、授权与续期、可建仓库的账号、选凭据、给 Git 的令牌）；
  `internal/spaces` 里是建空间与切换（`CreateWithSync`）。给 Git 的令牌还是走
  `gitstore.Token` 的环境变量 credential helper，`gitstore` 只多了一个能说明
  「凭据本身拿不到」的错误类型。
- 基地址有两个隐藏的启动参数 `--github-api-url`、`--github-url`，只给测试用
  （只支持 github.com 这一条没变）。
- 建空间时上限按「已有的加正在建的」算，所以并发请求不会越过上限（3.3 起是
  空间数配额，按人和按租户都这样算）。
  首次推送的重试间隔是 1、3、7 秒（最多四次），总共一个 15 秒的期限，等待和
  每次推送本身都算在里面；期限到了正在进行的推送会被停下。只重试「not
  found」、GitHub 的 5xx 和连不上；GitHub 对不认的凭据在私有仓库上也回
  「Repository not found」，分不出来，靠期限兜住。其他认证失败不重试。
- 暂存的空间有一个标记文件 `staging/<id>`（先于目录建，`Commit` 或 `Abort`
  时删）。启动时，标记超过 15 分钟、又没有 `spaces` 行的，删掉它的目录和
  标记；有行的（崩溃在事务之后）只删标记。只看有标记的 id，所以真正的空间、
  不是这段代码暂存的目录都不会碰。
- 要问所有者的六个问题，都按括号里的默认做了。

已知问题：

- 两个进程连同一个 PostgreSQL 时，可能同时拿同一个刷新令牌去续期：GitHub 只
  认第一次，另一个会被当成授权失效，连接就断了。现在服务只跑一个实例，所以
  可以接受。以后要多进程，需要在「续期并替换」外面加一把按用户的锁（
  `SELECT … FOR UPDATE`，或 advisory lock）。
- 断开后马上重新连接，新行的 `version` 又从 1 开始；如果断开前开始的一次续期
  还在等 GitHub 回复，它会通过版本检查，把新连接标成过期（通常要重新连一次），
  在吊销旧授权也失败时还可能写回旧账号的令牌。要等一次续期跨过整个「断开 +
  浏览器授权」才会发生。可以在写回前再看一次进程内的代数，或让版本号在重连后
  继续递增。
- 用某人的 GitHub 连接同步的空间，如果服务器的 App 的 client 没了（管理后台
  在还有人连着时不让删，所以几乎碰不到），写入会答 `409 provider_not_configured`
  而不是文档说的 `503 unavailable`：凭据错误里包着 GitHub 的错误，`fail()` 先
  认出了后者。不丢东西；以后让凭据错误不再包这个错误，或者按场合排序。

2.7 不做的：

- 设备码流程（网页用不上，留给第 4 期的客户端）。
- Webhook（App 被卸载要等下次换令牌被拒时才发现）；用推送事件代替每分钟
  拉取，留给 3.1 一起看。
- GitHub Enterprise Server、多个 App、GitHub 登录（登录在 2.8，和这里的
  「连接 GitHub 账号」是两回事，库里分开存）。
- 单用户模式：没有账号，这一节全部 `404`。

要问所有者的（括号里是没回复时的默认）：

1. **实测那两点**（4.3 留下的）：2026-11-23 已用真实 GitHub 实测，结论写回 4.3。
2. 建仓库之后失败时，空仓库留着还是尽量删掉？（默认留着，回答里给出仓库，
   用 `create: false` 重试；2026-11-23 定：留着，并给这个人留一条之后看得到
   的提醒，见上。）
3. 组织的安装默认不共享，只有自己连了 GitHub、GitHub 也允许的人才能在组织下
   建仓库，可以吗？（默认不共享，管理员逐个打开；2026-11-23 定：共享按租户挑。）
4. 新建仓库默认私有，是否允许建公开仓库？（默认允许选，默认私有。）
5. 同步用的安装令牌按单个仓库收窄（更安全），而不是一个安装一个令牌（换得
   更少）。空间很多时前者每个在用的空间每小时换一次。（默认按单个仓库。）
6. 断开 GitHub 连接时，还有空间在用它也照样断开，只提前提示受影响的空间数，
   可以吗？（默认可以：撤销是用户的权利，空间不丢东西。）

**2.8 passkey、SAML、多个登录提供方、登录验证码**

- passkey 和 SAML 用 authcore 的 `passkey`、`saml`。passkey 本地账号和外部
  账号都能绑；管理员可以要求某些人必须用 passkey。
- 把「一个 OIDC 提供方」改成「多个」：登录页每个一个按钮，账号的「安全」
  里逐个关联和解除；GitHub 登录单独写适配（见 1.7 的「多种登录方式」）。
- 同一个邮箱从不同提供方登录时不自动合并，规则同 1.7。
- **登录页、验证码、频率限制和锁定**（所有者 2026-11-22 提出：做成和
  KazuhaHub 的 Report Portal、Passwall-Sub-Panel 一样，包括 SSO）。两个项目
  的做法一致的地方照搬；不一致的地方以较新、也用 authcore 的 Report Portal
  为准，再加上 Passwall-Sub-Panel 的按 IP 每分钟请求数限制。
  - **登录页的四种状态**，由「登录方式」（`login_mode`）决定，只管页面怎么
    显示：
    1. `dual` 密码和 SSO（默认）：用户名、密码、（需要时）验证码、登录；下面
       「或者用以下方式登录」，每个提供方一个按钮「用 {名称} 登录」；忘记
       密码等链接。
    2. `sso_first` SSO 优先：提供方按钮在上，密码表单收起成一个链接「改用
       密码登录」，点了才展开。
    3. `sso_redirect` 只用 SSO（自动跳转）：只有一个提供方时直接跳过去，
       显示「正在前往 {名称}…」；有多个时不跳，列出按钮和「改用密码登录」。
       地址带 `?local=1` 或 SSO 失败回来带 `?sso_error=` 时不跳，免得失败后
       来回跳。
    4. `local_only` 只用密码：只有表单，没有 SSO 那一块。
    没有启用任何提供方时，不管设置成什么都按 `local_only` 显示。
  - **拒绝密码登录**（`sso_only`）是另一个开关，管服务端收不收密码：开了以后
    非管理员用密码登录一律拒绝（403 `local_login_refused`），管理员不受限，
    作为 SSO 坏了时的后门。保存时检查：开放注册时不能开；没有一个能用密码
    登录的管理员时不能开。这个开关不在公开接口里出现，免得别人知道哪些账号
    值得猜。没有提供方时它不起作用。
  - **提供方**可以有多个（OIDC 和 SAML），各有 `slug`、名称、图标；登录页读
    `GET /api/auth/providers`（提供方列表、`login_mode`、算好的 `local` 和
    `sso` 两个布尔值），点按钮是整页跳到 `/api/auth/{oidc|saml}/{slug}/start`。
    SSO 失败回到 `/login?sso_error=<码>`，页面显示对应的原因。
  - **验证码**用 authcore 的 `captcha`：自托管图片（默认，不出网）、
    Turnstile、reCAPTCHA、hCaptcha。
    - 用在哪：登录、接受邀请、首次设置管理员，以后的找回密码和公开注册；
      每一处单独开关。**SSO 不加验证码**（交给身份提供方）。
    - 什么时候要：每处可选「每次都要」或「错几次之后才要」（默认后者，
      默认 3 次，按 IP 和按用户名各算，任一到了就要）。
    - 页面向 `GET /api/captcha?ctx=login&account=<用户名>` 问要不要，要就同时
      拿到题目（图片），用户名改了再问一次。答错回 400，错误码
      `captcha_failed`，页面换一道题。验证码在算密码哈希之前查，答错不算
      密码错。第三方校验出错按失败处理（宁可拦下）。
    - 第三方的密钥加密存库，管理页只显示「已保存 · 更换」；换提供方时不沿用
      旧密钥；选第三方时站点密钥和密钥都必填。第三方校验核对返回的主机名
      是不是本服务的地址。
    - 题目存在内存里，最多一万道，5 分钟过期，每道只能答一次。
  - **频率限制和锁定**：
    - 按 IP 每分钟请求数（默认 10），用在登录、接受邀请、首次设置、取验证码、
      passkey 登录、SAML 回调上，超了 429，带 `Retry-After`。
    - 失败次数上限：在一个时间窗内（默认 15 分钟）按 IP、按用户名、按
      IP+用户名分别计数，上限默认 10；按 IP 的在算哈希之前查，按用户名的
      只在密码错了之后查，所以密码对的人不会被别人的失败挡住。超了 429
      `rate_limited`，带 `Retry-After`。现在的做法按这个改。
    - 锁定（默认关）：到了上限后锁一段时间（默认 15 分钟），范围可选
      「IP + 用户名」（默认，推荐）、「只按 IP」、「只按用户名」（会让别人
      能锁住某个账号，界面上要警告）。
    - 计数在内存里（单实例），重启清零；锁定和策略修改记审计日志。
  - **管理后台「安全」页**：登录（登录方式、拒绝密码登录，没有提供方时提示）、
    验证码（提供方、密钥、各处开关、触发方式和次数）、锁定（开关、时长、
    范围）、限制（每分钟请求数、失败上限、时间窗、会话时长）。这些设置存在
    状态库，改了立即生效，不用重启；现在只能用命令行参数配的 OIDC 也搬过来。
  - 单用户模式没有登录，以上都不涉及。

**2.8 的约定**（2026-11-23）：接口和错误码写在 `docs/api.md` 的「Signing in」
「Account」「Users and invites」和「Administration」的「Security settings」
「Sign-in providers」里，前后端照它做。

定下来的：

- **身份的键不变**：仍是「签发方 + 主体」（OIDC 的 issuer + `sub`，SAML 的
  IdP entity ID + `NameID`，GitHub 的地址 + 数字 id，不用可改的登录名）。所以
  现有的 `identities` 表不用迁移，命令行配的那个 OIDC 搬进库后，大家已绑的身份
  照样能用。两个提供方不能用同一个签发方（`provider_issuer_in_use`），一个
  身份只属于一个提供方；删了再用同一签发方加回来，身份也回来。
- **旧接口保留**：`POST /api/auth/oidc/start` 和 `GET /api/auth/oidc/callback`
  不变，只服务标了 `legacyCallback` 的那一个 OIDC 提供方，它的回调地址还是
  旧的，IdP 那边不用改。2.8 第一次启动时，若带着 `--oidc-*` 且库里从没导入过，
  就建一个 slug 为 `sso` 的提供方（密钥加密存库、`legacyCallback` 开、自动建号
  沿用 `--oidc-auto-create`），登录方式保持 `dual`、`sso_only` 关，所以没人会被
  挡在外面。之后以库为准：参数和库里不一致（或那个提供方被删了）只在日志和
  安全页提示 `flags_ignored`，参数不再起作用，后面的版本删掉这些参数。
- **开始登录两种写法**：登录页的按钮整页跳到
  `GET /api/auth/{oidc|saml|github}/{slug}/start`（只用于登录，跨站规则照旧）；
  首次设置、接受邀请、关联身份要带令牌或会话，用 `POST …/start` 拿地址（和
  2.8 之前的 `POST /api/auth/oidc/start` 一样），令牌不进地址栏。
- **SAML**：ACS 收的是 IdP 从别的站点提交的表单，`Lax` 的 cookie 不会带上，
  所以这次登录的 cookie 是 `__Secure-petalbook_saml_<slug>`、`SameSite=None`、
  只限 ACS 路径，SAML 因此必须是 `https` 的公开地址。ACS 不受「只收 JSON」和
  跨站规则限制，靠 `RelayState` 等于 cookie、`InResponseTo` 对得上这次登录来防
  伪造。临时（transient）`NameID` 拒绝。IdP 发起的登录默认关。
- **GitHub 适配放进 2.8**：2.8 的计划里本来就写了，它也不依赖 2.7（只是一次
  OAuth 授权码交换加读 `/user`、`/user/emails`、`/user/orgs`，用户令牌只在这次
  请求里用）；2.7 先合进来的话共用它的 HTTP 客户端。GitHub 提供方开自动建号时
  必须指定组织，否则任何有 GitHub 账号的人都能建号。
- **邮箱合并**：默认从不按邮箱合并。提供方开了 `mergeVerifiedEmail` 时，只有
  提供方声明已验证（OIDC 的 `email_verified`、GitHub 的已验证主邮箱；SAML 没有
  这个声明，开了就等于管理员担保 IdP 给的每个邮箱），**而且本地账号的邮箱也是
  已验证的**（来自担保过的提供方，或管理员设置、确认过；用户自己改邮箱会清掉
  这个标记），才合并到唯一的那个账号；从不合并到管理员账号、停用的账号、已有
  同一提供方身份的账号；对上两个账号时按 `no_account`。不然别人把自己账号的
  邮箱改成某人的，等那人第一次用 SSO 登录就进了别人的账号。
- **`sso_only`**：开了以后非管理员用密码**和 passkey** 都拒绝
  （`local_login_refused`，在核对密码之后才判断），接受 `member` 邀请时也不能
  用密码（否则建出来的账号永远登不进）。只有登录页真的给出了 SSO 按钮时才
  生效（照 Report Portal：`local_only` 下按钮被藏起来，再拒绝密码就是把门锁死）。
  开着的时候，改角色、停用导致没有「有密码的启用管理员」也拒绝（409）。
- **passkey**：本地账号和外部账号都能用，单独就能登录（不是第二因子），所以
  登录时要求用户验证（PIN 或生物识别），注册时优先可发现凭据。用户句柄就是
  用户 id。每人最多 20 个。注册要求 10 分钟内登录过（照 Rackbook）。按用户名
  开始时，不存在或没有 passkey 的用户返回一个由用户名和服务器密钥算出的假
  凭据 id，看起来和有一个 passkey 的人一样。计数器倒退（疑似被复制）直接拒绝，
  不更新存的计数，记审计日志。公开地址的主机名就是 RP ID，换了主机名旧的
  passkey 都不能用，启动时提示。
- **要求 passkey**：可以按人（`PATCH /api/users/{id}` 的 `passkeyRequired`）或按
  角色（`passkeys.requireRoles`）。这样的人有 passkey 时，密码对了也回
  `403 passkey_required`，登录页让他用 passkey；还没有 passkey 时，密码登录
  进到一个被扣住的会话（`GET /api/session` 的 `hold: "passkey"`），只能注册
  passkey 和退出，其他请求都是 `403 passkey_required`，注册第一个后立即放开。
  经 SSO 登录不扣（由 IdP 决定怎么验证）。丢了 passkey 由管理员删掉，下次密码
  登录再被扣住去注册新的。
- **验证码**：四种提供方里自托管图片不出网，是默认。上下文是登录、接受邀请
  （检查和接受都算）、首次设置，`forgot`、`register` 先占名字，一直回
  `required: false`。每处单独开关，各自选「每次」或「错 N 次后」（默认后者、
  3 次），按 IP 算，登录还按用户名算。默认都关（两个参考项目都是默认关）。
- **频率限制和失败上限**：现在的 `failLimiter`（免费次数加翻倍等待）和
  首次设置、邀请用的 120 次每分钟换成新的做法：按 IP 每分钟 10 次，登录、首次
  设置、检查和接受邀请、取验证码、passkey 开始和完成、SAML ACS 共用一份额度；
  失败按 IP、用户名、IP+用户名各数一份，固定窗口从第一次失败算起；SSO 的开始
  沿用自己的每分钟 30 次。登录的判断顺序：每分钟次数 → 验证码 → IP 失败数 →
  锁定 → 密码哈希 → 错了才看用户名和组合的失败数 → `sso_only` → 要求 passkey。
  错的首次设置令牌、邀请令牌算 IP 的失败。
- **内存有上限**：每分钟计数、三张失败表各最多 1 万个键，按最久未见先丢
  （被锁定的最后才丢）；验证码题目、passkey 仪式各最多 1 万个、5 分钟；SSO
  进行中的登录 1 万个、10 分钟；SAML 用过的断言 id 最多 65536 个。全在一个
  进程里，重启清零。
- **设置和提供方缓存在内存里**：启动读一次，管理员改了立刻重读，只重建改了的
  那个提供方；命令行改的（`petalbook security reset`）最迟 5 分钟内生效（每 5
  分钟查一次修订号，不是每个请求查）。`GET /api/auth/providers` 不查库。OIDC
  发现文档和 SAML 元数据存库也放内存，登录时不去取；按地址配的元数据每 24 小时
  后台重取一次。
- **每次登录的查询**：passkey 登录一次查询（按凭据 id）加一次写（计数器和最后
  使用时间），用户来自会话缓存；SAML 校验不查库（元数据、重放缓存在内存），
  之后和 OIDC 一样一次身份查询。是否被要求 passkey、有没有 passkey 跟着缓存的
  用户走，扣住会话不需要每个请求查库。
- **审计日志是新加的**（之前没有）：状态库里一张表，登录成功和失败（不存在的
  用户名也记）、超限（每个键每个窗口只记一次）、锁定、计数器倒退、身份绑定和
  解除（注明是用户、邮箱匹配还是管理员）、passkey 增删、安全设置和提供方的
  修改（只记改了的字段，密钥只写 `"changed"`）、管理员改用户。后台批量写，队列
  最多 1 万条，满了丢弃并记一条 `dropped`；保留 10 万条或 90 天。
  `GET /api/admin/audit` 分页读。
- **保存时检查**：`sso_only` 没有带密码的启用管理员时拒绝
  （`local_admin_required`），开放注册时拒绝（注册还没有，先留着这条）；第三方
  验证码要站点密钥和密钥（换提供方要在同一次请求里给新密钥，不沿用旧的），
  还要有 `--public-url`（核对主机名用）；passkey 用不了时不能要求 passkey，
  有人被要求时不能关掉 passkey。所有字段先全部校验再存，超出范围拒绝而不是
  改成边界值。
- **救急命令** `petalbook security reset --data DIR`：登录方式改回 `dual`，关掉
  `sso_only`、锁定、所有验证码，清空按角色的 passkey 要求；提供方和人不动。
- JSON 字段按 `docs/api.md` 的惯例用 camelCase（`loginMode`、`ssoOnly`），取值
  还是 `dual`、`sso_first` 这些；SSO 失败的参数从 `error=` 改成 `sso_error=`，
  关联失败回 `/settings?sso_error=`。

照搬的：

- **Report Portal**：登录方式和 `sso_only` 分成两个轴，及其退化规则（没有提供方
  按 `local_only`、`sso_only` 不生效、管理员不受限）；`local_admin_required`、
  `sso_only_with_registration` 两个保存检查；`GET /api/auth/providers` 的形状；
  图标只能是内置的，不能是外部地址；`/api/auth/{oidc|saml}/{slug}/…` 的路径；
  `sso_error` 的码（包括 `saml_*` 那一组）；SAML 的 `SameSite=None`、只限 ACS
  路径的 cookie 和拒绝临时 `NameID`；`GET /api/captcha` 的形状、`captcha_failed`
  回 400、验证码在哈希之前查、出错按失败处理、核对主机名、密钥不跨提供方沿用；
  失败按 IP、用户名、组合三份计数，IP 的在哈希之前、用户名的在密码错之后；锁定
  的三种范围和对「只按用户名」的警告；不存在的用户名也记审计；passkey 计数器
  倒退时拒绝且不写回；「被要求但还没有」时扣住会话、只放行一张固定的路由表，
  以及「要求了却没法满足」时拒绝保存；部分更新、先全部校验再存。
- **Passwall-Sub-Panel**：按 IP 每分钟请求数，登录相关接口（含取验证码、
  passkey、SAML ACS）共用一份，默认 10，改了立即生效。另外加了它没有的
  `Retry-After`。
- **Rackbook**：passkey 仪式 id 放在查询参数里、完成时请求体就是浏览器给的凭据；
  要求可发现凭据和用户验证（无密码登录）；注册前要 10 分钟内登录过；用 authcore
  自带的有上限的内存仪式存储。Rackbook 没有接 SAML，SAML 只参考 Report Portal。
- 两个项目不一致、按 Report Portal 的：计数放内存（Passwall 每次登录查审计
  事件表）；锁定沿用失败上限（Passwall 另有一个阈值）；验证码按处开关（Passwall
  只有一个开关）；要求了却没有可用方式时不放行（Passwall 会自动放开）。和
  Report Portal 不同、按本文上面所有者定的：验证码每处都能选「错 N 次后」
  （Report Portal 只有登录能选）；锁定默认 15 分钟（Report Portal 是 30）。

不做的：

- TOTP 等第二因子（passkey 在这里是独立的登录方式）；SCIM；SAML 单点登出、
  加密断言、签名密钥轮换的接口；按 IdP 的组或属性分配角色（第 3 期的组）；按
  提供方设会话时长；reCAPTCHA v3 的分数；OIDC 多租户的 `common` 签发方（要填
  具体租户）；多实例之间共享计数；找回密码和公开注册本身（只占上下文名）。

待所有者确认（括号里是现在按的默认）：

1. `sso_only` 开着时，非管理员的 passkey 登录也拒绝吗？（拒绝：`sso_only` 的
   意思是由 IdP 决定谁能进，passkey 会绕过 IdP 那边的停用。）
2. 被要求 passkey 的人经 SSO 登录时也要扣住吗？（不扣，两个参考项目都只管
   本地密码账号。）
3. 按 IP 每分钟 10 次、这几个接口共用，办公室共用一个出口地址时会不会太紧？
   一次登录是取验证码加提交两次请求。（按设计定的 10；`--trusted-proxy` 要配对。）
4. 验证码各处默认关？（关，两个参考项目一致；开了以后默认错 3 次才要。）
5. GitHub 登录放进 2.8？（放进，自动建号必须指定组织。）
6. 邮箱合并还要求本地账号的邮箱是已验证的，并且从不合并到管理员账号？
   （要求；这样现有账号要合并，得由管理员在用户管理里确认邮箱。）
7. `--oidc-*` 导入一次后以库为准，参数只告警不生效？（是，后面的版本删参数。）
8. 按用户名开始 passkey 登录时，有多个 passkey 的人能被看出数量，可以接受吗？
   （接受；不接受的话只留免用户名的方式。）
9. 审计日志保留 10 万条或 90 天？（是。）
10. JSON 里用 `loginMode`、`ssoOnly` 而不是设计里写的 `login_mode`？
    （用 camelCase，和其余接口一致。）

**2.8 的现状（2026-11-23）**：上面的方案都做了，接口见 `docs/api.md` 的
「Signing in」「Account」「Users and invites」和「Administration」。「待所有者
确认」里的十条都按括号里的默认做了。实现时定下来的：

- **库**：passkey、SAML、验证码、OIDC 都用 authcore 的 `passkey`、`saml`、
  `captcha`、`oidc`，版本没变（`f3886de`）。authcore 的 `saml` 要求使用方在自己的
  `go.mod` 里重复它对 `crewjam/saml` 的 `replace`（换成 `openctemio/saml` 的固定
  提交），已经加上；测试里的虚拟认证器用 `descope/virtualwebauthn`，假 IdP 用
  `crewjam/saml` 的 `IdentityProvider`。
- **存储**：迁移 `0014_signin.sql`（主线到 0010，`feat/mcp` 用 0011、0012，
  `feat/github-app` 用 0013）。新表 `sign_in_providers`（密钥用 `secret.Box` 加密，
  发现文档和元数据存 `cache` 列）、`passkeys`（凭据 JSON、`rp_id`、`synced`）、
  `audit_log`；`users` 加 `email_verified`、`passkey_required`，`sessions` 加
  `held`。安全设置是 `site_settings` 里键为 `security` 的一行，它的 `updated` 就是
  修订号：命令行改了之后，运行中的服务最迟 `CacheTTL`（5 分钟）读到，每个请求
  只比较内存里的时间，不查库。迁移数量的测试改成数文件。
- **旧参数**：第一次启动时 `--oidc-*` 建出 slug 为 `sso` 的提供方（`legacyCallback`
  开、启用、`autoCreate` 沿用参数），并在 `site_settings` 记一行 `oidc_imported`。
  以后参数和库里不一致、或那个提供方被删了，只写日志并在安全页提示
  `flags_ignored`；删了之后再带着参数启动也不会重新建。身份表没改，原来绑定的
  身份照样能用。登录方式保持 `dual`、`ssoOnly` 关，没有人会被挡在外面。
- **登录的判断顺序**照合同：每分钟次数、验证码、IP 失败数、锁定、密码哈希、
  错了才记用户名和组合的失败、`sso_only`、要求 passkey。IP 那一项是「先占位、
  成功了退回」（和以前一样），所以一批同时来的错误密码最多只有上限个进到哈希。
  验证码答错、要求 passkey、`sso_only` 都不算密码错。被锁住的请求在审计日志里记
  `login_failed`，原因 `rate_limited`；锁定本身记 `lockout`。谁都能编出来的失败
  不让它刷满审计日志：验证码答错、地址被锁或到了上限，每个地址（IPv6 按 /64）
  每种原因每分钟最多记一行；没开始过的 passkey 仪式和对不上任何登录的 SSO 回调
  不记。
- **SAML**：元数据里只给签名证书，不给加密证书，因为我们拒绝加密断言，而 IdP
  看到加密证书就会加密。关联（`link`）时 ACS 收到的是别的站点的 POST，会话
  cookie（`Lax`）不会带上，所以由发起这次尝试的记录里的用户 id 决定是谁，那条
  记录只有已登录的人才能建。尝试的 cookie 是 `__Secure-`，兄弟子域名也能种，
  所以关联时另发一个只属于本主机的 `__Host-petalbook_saml_link_<slug>`
  （`SameSite=None; Secure; Path=/`），记录里存它的哈希，ACS 对上了才关联。
  按地址配的元数据每 24 小时（从启动算起）后台重取，取不到时继续用库里上一次
  好的那份。
- **passkey**：按用户名开始的仪式记在内存里（和仪式一样 5 分钟、最多一万条），
  结束时才知道是哪个用户；假凭据 id 用 HMAC 算，密钥第一次用时随机生成并加密存
  在 `site_settings`，重启后不变。计数器倒退由凭据存储的写回拒绝，写审计
  `passkey_clone`，存的计数不动。每个 passkey 记着注册时的主机（`rp_id`），
  换了主机名之后安全页提示 `passkey_host_changed`，启动时也写日志。
- **用户的 `passkeyRequired`**：响应里是有效值（本人被要求，或者角色被要求），
  `PATCH` 改的只是本人的标记。
- **GitHub**：用授权码加 PKCE；组织只看 `/user/orgs` 的前 500 个（5 页）。
- **提供方创建**：够得着就按请求里的 `enabled`（默认开）；够不着照合同存下来、
  默认关。两个提供方不能用同一个签发方，SAML 的签发方在取到元数据之后才知道，
  所以那时再检查。
- **内存**：每分钟计数、三张失败表各一万个键（被锁的最后才丢）；验证码题目、
  passkey 仪式各一万个、5 分钟（authcore 自带的有界存储）；SSO 进行中的登录
  一万个、10 分钟；SAML 用过的断言 id 65536 个；审计队列一万条，满了丢弃并在
  下一批写一行 `dropped`。

测试：每条流程在 SQLite 和 PostgreSQL 上都跑（`scripts/test-postgres.sh`），
用假的 OIDC 提供方、假的 SAML IdP、假的 GitHub、假的 siteverify 和虚拟认证器。
覆盖：从旧参数升级不锁人、`sso_only` 的保存检查和各种拒绝、验证码在哈希之前、
限制的顺序和三种锁定范围、SAML 的攻击用例（RelayState 或 cookie 不对、另一个
`Destination`、没签名或被改过、SHA-1、两个或加密的断言、重复属性、临时或没有
`NameID`、重放、IdP 发起）、邮箱合并的每个排除条件、要求 passkey 的扣住和放开、
发现文档不在登录时去取。

**2.8 已知问题**

- 真浏览器里的 WebAuthn 和第三方验证码的真实脚本没有测过，只有虚拟认证器和
  假的 siteverify。
- `sso_only_with_registration` 这条保存检查先留着，公开注册还没有。
- SAML 的签名密钥不能轮换（合同里本来就不做），证书 10 年后过期要重建提供方。
- OIDC 提供方的发现文档存库之后不会自己更新，要点「刷新」。
- `GET /api/captcha?account=` 的回答能看出某个用户名最近是不是错了好几次（要不要
  验证码按用户名的失败数定）。透露的东西不多，接受。
- `ssoOnly` 开着时新建一个会启用的提供方（`enabled` 没给或是 `true`），而且没有
  带密码的启用管理员，会直接以 `400 local_admin_required` 拒绝，而不是先存成停用
  的。用 `PATCH` 启用已有提供方的那条有测试，新建的这条没有。
- 合并主线（2.6）时定下：被扣住的会话（要先注册 passkey）不能建 API 令牌，也不能
  在授权页同意（都是 `403 passkey_required`）；之前建好的令牌和 OAuth 授权不受
  扣住影响，照常可用，要停就撤销（见 `docs/api.md`「Held sessions, API tokens and
  OAuth grants」）。这样令牌请求不用每次查 passkey。审计日志加了 `token_created`、
  `token_revoked`、`grant_revoked`；OAuth 授权是在换令牌（`/oauth/token`）时才建的，
  不在接口层，`grant_created` 留到以后。
- 和 2.7 合在一起时定下：被扣住的会话碰不到 GitHub 的任何接口（查看、连接、
  断开、列出所有者和仓库、GitHub 的回调、带仓库建空间，都是
  `403 passkey_required`），扣住之前连好的 GitHub 账号不受影响。GitHub 登录
  （`/api/auth/github/{slug}/…`）和为仓库连接 GitHub 账号（`/api/connections/github/…`）
  在跨站例外里也分开列。审计日志的三个事件种类（`connection_provider_changed`、
  `account_connected`、`account_disconnected`）在 2.7b 里加上了，连带前端的种类列表、
  文案、模拟服务器和接口文档的表。
- passkey 和单点登录把返回地址（`return`）交给服务器，服务器只收 2048 字节以内的，
  超过就回首页（密码登录在页面里跳转，没有这个限制）。`/oauth/authorize`
  的地址一般远短于此，但客户端用很长的 `client_id`（元数据文档地址）和 `state`
  时可能超过，那时登录后不会回到授权流程，要在客户端重新开始。

**2.7b 的约定（2026-11-23）**：通用的连接，以及「存储与同步」。接口和错误码写在
`docs/api.md` 的「Connections」一节（2.7 的「GitHub」一节改成了「The GitHub provider」，
管理员的那一节在「Administration」的「Connections」）。所有者 2026-11-23 定：这是 2.7 的
修复之后、2.7 合并之前的一个里程碑，在 2.7 的分支上做，和 2.7 一起合并（作为 2.7 那个 PR 的
最后一部分，在分支上单独审一次）。3.1 的约定在 `feat/storage` 里；那里的路由、错误码、
`github` 字段按下面的对照表改名。

定下来的：

- **命名：「存储与同步」**（所有者 2026-11-23 定）。界面和文档里不再有「代码托管」
  或单独的「GitHub」一节：
  - 管理页「存储与同步」把 3.1 的「存储」页和 2.7 的「GitHub」管理页并成一页：
    上半是「存在哪里」的规则（3.1 的要求远端、宽限期、只在本地的空间的上限、允许的
    主机；3.5 的站点存储预算和存储压力，见「3.5 的约定」）；下半是「连接」：每个平台
    一张卡片（品牌图标、配好没有、是否支持部分同步、GitHub 的 App 和安装列表、
    以后 GitLab／Gitea 的 OAuth 应用）。2.7 和 3.1 谁后合并，谁负责把两页并起来。
  - 账号页的那一栏叫「已连接的账号」，每个平台一行（GitHub 一行，以后 GitLab 等各
    一行），2.7 的「空仓库提醒」放在对应平台那一行下面。
  - 新建空间时的选择叫「存在哪里」：这台服务器／同步到 GitHub／同步到 GitLab／…
    （只列出全站配好、这个人能用的平台），最后是「其他（按地址）」。
  - 对普通用户界面上不说 Git 和仓库：说「同步到 GitHub」「在 GitHub 上的位置」
    「同步出了问题」。管理页和「其他（按地址）」这个高级选项里照实说 Git、仓库、
    部署密钥。
- **后端一个提供方接口**：一个提供方说明自己（类型、主机、显示名、能力）、连接一个
  人（开始授权、回调、续期、断开）、列出可建仓库的地方和已有的仓库、建仓库、给 Git
  的凭据、检查自己的配置。服务器里的提供方在内存里一张表，按 id 查。
  - **GitHub 是第一个实现**，2.7 的行为全部照旧：一键用 App Manifest 建 App、安装和
    授权在 GitHub 的同一趟流程里完成、组织的安装按租户共享、令牌按单个仓库收窄。
    `internal/githubapp` 不动，只在它上面加一层实现这个接口。
  - **GitLab**（gitlab.com 和自托管）和 **Gitea／Forgejo／Codeberg** 在它们自己的
    里程碑 3.8 里做：每个主机一个 OAuth 应用，由管理员配，用户连一次。
  - **按地址的普通 Git**（部署密钥或令牌）保留，是类型 `git` 的提供方：没有「连接」，
    凭据仍是每个空间一份。
  - 能力（`capabilities`）每个提供方一组：能不能建仓库、有没有安装（只有 GitHub）、
    能不能连个人账号、能不能当租户的存储目标、是否支持部分同步（3.5 起自动检测，
    见下）。界面只按能力显示选项，不按平台名字写死。
- **接口是通用的**（所有者 2026-11-23 定：还没发布，选干净的通用形状，不留别名）：
  - `GET /api/connections` 列出提供方（类型、能力、这个人的连接状态、空仓库提醒）；
    2.7 的 `/api/github/…` 变成 `/api/connections/github/…`，`/api/admin/github…`
    变成 `/api/admin/connections/github…`；租户的（3.6）变成
    `/api/admin/tenants/{tenant}/connections/{provider}…`。
  - `POST /api/spaces` 的 `github` 变成 `storage.sync`：`{provider, owner, name,
    private, create, assets}`；不带就是「这台服务器」。
  - Remote 的凭据 `type: "github"` 变成 `type: "connection"`，带 `provider` 和 `via`
    （`app`、`user`，3.6 加 `tenant`）。
  - 错误码去掉 `github_` 前缀、带 `provider`：比如 `github_authorization_expired` 变成
    `connection_expired`，`github_not_configured` 变成 `provider_not_configured`，
    `github_repo_not_in_installation` 变成 `repo_not_in_installation`。对照表在
    `docs/api.md`。
  - 3.1 的 `remotes.require` 取 `null`、`"any"` 或一个提供方 id（取代 `"github"`）；
    3.1 的 `github.assets` 变成 `storage.sync.assets`。
- **3.6 的租户存储目标用同一个接口**：存储目标就是「一个提供方 + 一个位置（GitHub 的
  组织、GitLab 的组、Gitea 的组织）+ 租户的凭据」。GitLab／Gitea 的目标要等 3.8。
  3.6 的能力 `integrations.github` 改名 `integrations.connections`，分层键
  `github.useSiteApp` 改名 `connections.useSiteApps`。
- **状态库**：GitHub 自己的表（`github_authorizations`、`github_installations`、
  `github_installation_tenants`）名字不变，是 GitHub 实现的；`remotes` 的
  `cred_type` 取 `ssh`、`token`、`connection`，加一列 `provider`。迁移用合并时下一个
  空着的编号，SQLite 和 PostgreSQL 都测；v0.x 不做升级兼容：2.7b 之前建的开发库
  必须删掉重建，迁移不转换里面的旧行（例如 `cred_type = 'github'`）。
- **测试**：提供方接口有一套共用的合同测试（假的提供方跑一遍，GitHub 实现跑一遍，
  3.8 的两个实现以后也跑这一套）。

2.7b 不做的：GitLab、Gitea 的实现（3.8）；GitHub 以外的品牌图标（3.9；GitHub 的图标 2.7b 里做，
用 Simple Icons 的 CC0 路径内联进网页，不连外网）；提供方的自动发现（管理员手动加）。

所有者 2026-11-23 定的：

1. 「存储与同步」取代「代码托管」和单独的「GitHub」：管理页一页，账号页「已连接的
   账号」，新空间「存在哪里」；对普通用户不说 Git 和仓库。
2. 后端一个提供方接口，GitHub（2.7 的 App）是第一个实现；GitLab、Gitea／Forgejo／
   Codeberg 在自己的里程碑（3.8）；按地址的普通 Git 保留；接口通用，GitHub 专属的
   路由改成按提供方的，不留别名；3.6 的租户存储目标用同一个接口。

**2.7b 做出来的样子**：

- **接口在 `internal/connections`**：`Provider` 接口、`Registry`（按 id 查，内存里一张表）、
  通用的 `Error`（带 `Provider`）和一套共用的合同测试（`connections/conntest`：假的提供方跑一遍，
  GitHub 实现跑一遍）。`git`（按地址的普通 Git）是一个没有连接的提供方，`repos` 回空列表。
  GitHub 的实现是 `internal/github` 的 `Service`，在 `internal/githubapp` 上加一层，行为照旧。
- **网页**：管理页「存储与同步」（`/admin/storage`）放连接，一个平台一张卡片，3.1 的存储规则和
  3.5 的预算以后放在卡片上面；账号页「已连接的账号」（`/settings/connections`）；新建空间的
  「存在哪里」。普通用户的界面上没有 Git、仓库这两个词；GitHub 的图标是 Simple Icons 的路径，
  内联在网页里。
- **迁移**：0013 还没合并，就地改：`remotes` 加 `provider`，`cred_type` 的值是 `connection`。
  开发库里已经跑过旧的 0013 的，要删了重建（v0.x 不做升级兼容）。
- **审计**：加了 `connection_provider_changed`、`account_connected`、`account_disconnected`。
- **顺手修了 2.7 评审里的五个问题**：
  1. 一键建 App 时，GitHub 已经换出了 App 但保存失败（GitHub 连不上）：不再说「什么都没存」。
     换出的密钥和 secret 加密留在内存里十分钟，管理员可以点「再试一次保存」
     （`POST /api/admin/connections/github/manifest/retry`）；过期了就告诉管理员 App 已经在
     GitHub 上、名字和地址，让他手动填。
  2. 自动重新检查 App 时，只在库里的记录还是读到的那条时才写回（比较并替换），
     所以不会盖掉刚保存的新 App。
  3. 「这个人没装 App」的结果缓存一分钟，列所有者不会用光 App 的速率限制，日志也不会每次都记。
  4. 用户令牌在没装 App 的组织下建仓库被拒：答 `app_not_installed`（带 `installUrl`）或
     `provider_refused`，不再答 `app_missing_permission`。
  5. 一键建 App 的尝试有自己的小表（最多 64 条，满了最早的先走），连接账号的尝试挤不掉它。

**第 2 期之外，但 1.7 之后要记着的**

- 1.7 的已知问题（见上文「1.7 的现状」），各自碰到相关代码时再处理。
- 4.5 的「本地放不下」四步，和「强制跟随远端」「独立附件仓库」一起
  作为第 3 期开头的里程碑 3.1（见下）。
