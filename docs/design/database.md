# Database

## 5. 数据库

### 5.1 状态库（SQLite 或 PostgreSQL）

用户、外部身份（提供方加 subject）、密码和 passkey、组和成员、空间和成员、
会话、API token、OAuth 授权、远端凭据（加密）、草稿、邀请。

- SQLite 用纯 Go 驱动，保持单个可执行文件。
- 两种数据库从第一期起都在 CI 里跑测试，避免事后补兼容。
- 接 PostgreSQL 的收益是托管备份和现成运维，不是横向扩展。

### 5.2 索引库（始终本地 SQLite）

页面元数据、全文索引、标签、链接关系。可以随时删掉，从文件重建。

- 中文全文搜索用「逐字切分」：入库前在每个汉字两边加空格，让 SQLite 自带
  的分词器把每个字当成一个词，查询时转成短语。三字符分词查不了两个字的
  词，已排除。
- 全局搜索跨当前用户可见的所有空间，结果按空间权限过滤。
- 列表、文件夹树、标签、搜索都查索引，不遍历磁盘，不逐个读文件。
- 索引记录它对应的提交。每次 fetch 快进后，比较新旧两个提交之间改了哪些
  文件，只重建这些页面的索引：新增的加入，删除的移除，改名的靠 `id` 认出是
  同一篇。别人直接在 GitHub 上改动就是走这条路径，最多延迟一个 fetch 周期
  （默认 60 秒）；配置了 webhook 时可以立即触发。
- 只有索引缺失、版本不符，或记录的提交已不在历史里时才全量重建。
- 不引入 Redis：服务是单实例，会话在状态库，热数据放进程内缓存即可。
- 实测（5 万篇、6220 万字，见 `spikes/search-scale/`）：文件夹和标签列表
  0.1 到 15 ms；搜索少见的词 0.2 ms，搜索命中上万篇的常见词约 200 ms；
  保存一篇后更新索引 0.7 ms；全量重建 29 秒；索引大小是文本的 87%。
- 已知要改进的：逐字切分会跨标点匹配，入库时要把标点换成占位词。

### 5.3 数据库基线（2026-11-24）

3.5 合并后按所有者的要求（第 17 节「数据库」）对状态库做了一次整体审查：在一个造出来的库上量
（200 个租户、3 万用户、6 万空间、100 万条审计、一个 5 万篇的索引库），两种数据库都看了查询计划。
这一节记下改了什么、为什么、量出来的数。**开发库和演示库都要删掉重建**：旧库启动时会被拒绝，
不会被转换。

- **一个基线，不写迁移。** 0001 到 0020 合成一个文件 `internal/store/schema.sql`，SQLite 和
  PostgreSQL 共用；只在某一种库上跑的语句前面写一行 `-- dialect: postgres`（或 `sqlite`）。
  `tenant_id` 现在是真正的外键（用户、空间、组、邀请、注册、登录提供方、用量、GitHub 安装的
  共享、回收站、导出、批量任务）；审计日志的 `tenant_id` 故意不是外键，记录要比租户活得久。
  删租户时：回收站里还有东西就拒绝（API 文档本来就这么写，代码之前漏了）；已清除的人的墓碑、
  导出、批量任务移到默认租户，导出和任务变成全站管理员的（`site_only`），照常过期和清理。
  只有一行的 `settings` 表并进 `site_settings`（键 `first_user`）。7 和 18 的 Go 步骤、
  「关外键重建表」那套机制、`tenants_migrate.go` 和模拟旧库升级的测试都删了；默认租户在建库
  时一起建。
- **文本列一律 `COLLATE "C"`。** 原来 PostgreSQL 的排序和游标比较靠每条查询临时加
  `COLLATE "C"`（`bytewise()`），列本身的索引是默认排序规则的，用不上：3 万用户上按用户名
  翻页 14–18 ms（全表扫描再排序），列声明成 `COLLATE "C"` 后 0.04 ms；空间列表按 `name_key`
  排序在 en_US 库里和 SQLite 的顺序还不一样。现在每个 TEXT 列都声明 `COLLATE "C"`（SQLite
  上写成 `COLLATE BINARY`，就是它的默认），两边按字节排、游标含义一样，`bytewise()` 删掉。
  启动时读 `pg_database` 的区域：不是 C、C.UTF-8 或 builtin 提供方就在日志里警告（不拒绝，
  列已经是 C 了）；README 写了建库语句
  `CREATE DATABASE … LOCALE_PROVIDER builtin BUILTIN_LOCALE 'C.UTF-8'`。
- **按邮箱找人走索引。** `UsersByEmail`（登录、重置密码、SSO 按邮箱合并都用它）原来对租户里
  每个人的地址做一次小写化再比，3 万人 88 ms。现在 `users` 有 `email_key`（Go 的 `EmailKey`，
  非 ASCII 的地址是空串、谁也匹配不上）和 `name_key`（`FoldName`），每次写名字或地址都在同一条
  语句里写键（有个测试读整个包的 SQL 检查这一点）；查找走部分索引
  `users_email (tenant_id, email_key) WHERE email_key <> ''`。搜人（`SearchUsers`、存储页的
  人员列表、组成员、空间可见的人）把查询词在 Go 里折叠后比 `username` 和 `name_key`，不再用
  PostgreSQL 的 `LOWER()`（C 库里它不折叠非 ASCII），所以 `STRASSE` 能找到 `Straße`，两种库
  结果一样；API 文档相应补了一句。
- **索引的增删。** 补上删人时级联要用的子表索引 `drafts_user`、`invites_created_by`、
  `provider_leftovers_user`。删掉重复的：`spaces_last_used`（`spaces_used` 已覆盖）、
  `spaces_usage_user`（是 `spaces_owner` 的前缀）、`users_tenant`（`UsersWithQuota` 加上
  `deleted_at IS NULL` 走 `users_live`；令牌和授权列表的租户条件改成按主键逐行查用户）；
  `asset_remotes_move` 改成部分索引。用量表的 12 个列表索引只收人的行（`WHERE kind = 'user'`）：
  按租户的去掉了开头的 `kind`；跨租户的保留 `kind` 在前，因为 SQLite 的规划器没有前导列上的
  等值条件时把部分索引当全表扫描估价，宁可用主键再排序。
- **PostgreSQL 的 fillfactor。** `usage`、`spaces`、`users`、`sessions`、`remotes` 设
  `fillfactor = 85`：更新没碰索引列时可以是 HOT 更新。量过：默认 100 时 5000 次这样的更新
  0 次 HOT，80 时 4891 次。
- **锁行不写行。** `LockSpace`、`LockGroup`、`LockFavorites` 原来用 `UPDATE … SET id = id`：
  SQLite 上每次 262 µs（改写 16 个索引项、查外键），PostgreSQL 上每次留下一个死元组，5000 次
  689 ms 对 65 ms。现在 PostgreSQL 用 `SELECT … FOR NO KEY UPDATE`（和那条 UPDATE 同一级别的
  锁：互斥不变，又不挡引用这一行的外键检查，`FOR UPDATE` 会挡），SQLite 只查行在不在（写事务
  从开始就拿着唯一的写锁）。各开一个事务锁 5000 次：SQLite 每次 176 µs → 23 µs，PostgreSQL
  156 µs → 136 µs（主要是往返），不再有死元组。
- **后台测量的队列单独一张表。** 测量反复走遍每个空间，原来每次都写带索引的 `spaces.size_at`，
  还给所有者和租户的用量行盖时间戳，什么都没变也写：PostgreSQL 上 1 万次更新 0 次 HOT，空间表
  的索引从 57 MB 涨到 62 MB。现在队列是 `space_measure (space_id, tenant_id, size_at)`，索引
  `(size_at, space_id)` 给「下一个该量谁」、`(tenant_id, size_at)` 给「租户最近一次测量」；
  `spaces.size_at` 和它的索引删掉。只有字节数或页数变了才写 `spaces` 和用量行；人的行在变化时
  盖时间，第一次量到他的空间时也盖一次；租户的行不再盖，`measuredAt` 取它的空间在队列里最新的
  时间（一次索引查找）。建空间的每条路径都入队，恢复的空间重新入队，每日重算把漏掉的补上。
  2000 个空间上 5000 次「什么都没变」的测量：PostgreSQL 每次 636 µs → 422 µs，空间表索引
  1.68 MB → 2.04 MB 变成不涨，空间和用量行的新版本 10600 个变成 0；SQLite 每次 389 µs → 222 µs。
- **审计日志按 id 范围分批删。** id 是 12 位十六进制的毫秒时间加序号，所以「早于某时」就是主键
  的一段：`audit_log_at` 删掉，`TrimAudit` 每批删 5000 行、每批一条语句（不在事务里时各自是
  一个事务）。原来一条语句删 67.6 万行要 8.2 s，一直占着 SQLite 唯一的写连接；现在 27 万行
  SQLite 1.8 s（54 批，每批约 33 ms），PostgreSQL 0.66 s。新加 `audit_log_tenant_kind
  (tenant_id, kind, id)`，租户按类型筛审计原来要读整个租户的日志（100 万行时 414 ms）。
- **连接。** PostgreSQL `SetMaxIdleConns(20)`（Go 默认只留 2 个空闲，一阵请求过后刚开的连接就
  被关掉，下一阵再重开）。SQLite 加一个 4 个连接的只读池（`query_only`）：`Store.Q()` 上不在
  事务里的 SELECT 走它，其余语句（包括 PRAGMA 和带 RETURNING 的）走唯一的写连接。规则（写在
  `Store.Q` 的注释里）：WAL 下一次读能看到它开始之前提交的所有写，所以先在 `Q` 上写、再在 `Q`
  上读，读得到自己写的；要和某次写在同一状态下读的（读—判断—写，或者写和读之间不许插进别的
  写）放进同一个 `Tx`，`Tx` 在写连接上，读也在里面；`Tx` 里只用它自己给的 `Q`。之前 SQLite
  只有一个连接，`Tx` 里用外面的 `Q` 会卡死，所以现有代码里没有这种用法。
- **`bulk_jobs` 定期清理。** 原来从不删。每日任务删结束（done、failed、interrupted）超过 30 天
  的任务，走新索引 `bulk_jobs_updated`；排队中和运行中的永远不删。
- **索引库的页缓存。** 每个连接 64 MB 页缓存，一个索引 15 个连接、最多同时开 64 个空间，最坏
  约 61 GB。现在写连接保留 64 MB，读连接各 8 MB，并用 256 MB 的内存映射读文件（`mmap_size`，
  modernc 的 SQLite 支持，上限 2 GB），共享的页只在操作系统缓存里存一份；私有缓存最坏是每个
  索引 176 MB、64 个约 11 GB。代价：映射的文件读出 I/O 错误时进程会收到 SIGBUS，索引库在本地
  盘上，可以接受。
- **`session_ends` 的时间索引**（#39 加的表）已经有 `session_ends_ended`，每日清理
  `DELETE … WHERE ended < ?` 走它；测试用查询计划确认。
- **测试**：每项行为改动都有测试；性能项各有 EXPLAIN 测试，两种库都跑，回退了就会失败：按邮箱
  查人、PostgreSQL 上的按字节排序翻页、审计按租户和类型、测量队列、级联用的子表索引、
  `session_ends` 清理、批量任务清理（`internal/store/db_explain_test.go` 等）。

和各自功能一起做的（3.6 及以后）：审计的类别和高频事件的 `activity_log`；`layered_settings` 的
`set_by`、`site_lock` 和 `(layer, key)` 索引；3.7 的 `tenant_members` 排序索引、个人空间不进
默认租户的列表索引；通知的表（3.10 第四部分：`notifications` 和 `notification_seen`）和自定义角色的表；3.10 给 `/admin/users` 分页。

### 5.4 结构变更的规则：正式发布前不写迁移

所有者 2026-11-24 定的规则。

- **正式发布之前，一行迁移代码都不写。** 结构只有一个基线 `internal/store/schema.sql`，有改动
  就直接改它，不加编号的增量文件，也没有 Go 的升级步骤。建库时把基线语句（不含注释）的哈希
  记在 `schema_baseline` 的唯一一行里；启动时库里的哈希和这份代码的不一样（或者是还在用编号
  迁移的旧版本建的，有 `schema_migrations` 表），就拒绝启动，提示「这个库是早先的开发版本建的，
  请重建」，**从不尝试转换**。模拟旧库、模拟升级的测试一律不要。
- **正式发布之后，升级代码和结构分开放。** 位置已经留好：`internal/store/upgrade/`（现在是空包，
  只有说明规则的注释）。每个发布之间的升级一步一个文件放在那里，基线始终描述当前版本的新库，
  升级步骤只负责把旧库带到它。
- **其他升级和兼容代码也一样**（所有者 2026-11-24：「其他升级代码也是，要能在合适的时候丢掉」）。
  发布前，我们自己的数据、设置、接口和客户端存储改了格式就直接改，不读旧格式。按这条删掉的有：
  每日文案设置的升级和 `notices`、`PUT /api/admin/daily` 忽略的 `hosts`、按旧代码存的行、
  一个字符串当列表读（默认的 `apis` 和 `sources`）、空间选择存成的纯 id 列表；2.2 之前放在
  `.petalbook/cache` 的状态和 `.petalbook/tmp` 的搬迁、旧状态库草稿的导入；网页端 1.1 的旧地址、
  旧的设置地址、本地草稿缺 `source` 的读法、首页记录里的纯数字、离线库的版本升级、对旧服务器的
  迁就。发布之后，兼容代码放在标明的边界后面，过一个大版本就删。
- **只支持从上一个大版本升级。** 进入新的大版本时，新基线已经包含一切，上一个大版本的升级
  步骤可以整个删掉。
