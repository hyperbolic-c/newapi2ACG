# 实施状态

更新日期：2026-11-04（Task 1.8/1.11 批次后）。依据修订后的实施计划和 design v3.2。按用户要求，本批未下载或启动数据库容器。

## 任务进度

| 任务 | 状态 | 实现与边界 |
| --- | --- | --- |
| Task 0 工程初始化 | 已完成 | 工程、工具配置、依赖锁定；Git 仓库已推送 `origin/main`；lint、gitleaks 门禁通过 |
| M0 源契约 | 源码核验完成，运行核验待办 | 本地 tag v1.0.0-rc.21，76 个持久化字段分类，34 个采集、42 个 dropped；无数据库/HTTP 实测 |
| 1.1 资源标识 | 已实现 | kind、实例前缀、opaque local、文本/JSON 往返 |
| 1.2 资源与 Schema | 已实现 | 五类资源、严格解码、规范化哈希；Go 与独立 JSON Schema 共享正反例 |
| 1.3 密钥 | 已实现 | 显式 Reveal、格式化/JSON/slog 脱敏、HMAC 和短指纹；修复 Fingerprinter 格式化泄露内部盐 |
| 1.4 配置 | 已实现基础加载 | 严格 YAML、默认值、引用检查、配置哈希；支持安全 ConfigMap 投射；映射编译留待 M2，不接受未支持的配置类别 |
| 1.5 PG 表结构 | 代码完成，真实 PG 验证待办 | 内嵌迁移、15 张业务表、版本校验、事务锁；离线测试通过，集成测试已编译 |
| 1.6 PG 任务锁 | 代码完成，真实 PG 验证待办 | 专用会话 advisory lock、健康超时取消、并发安全关闭；离线测试通过 |
| 1.7 PG 短事务协议 | 核心代码完成，真实 PG 验证待办 | 镜像/位点、版本化队列、意图及结果事务；命名、依赖和失败队列留待后续 |
| 1.8 服务状态机 | 代码完成，真实 PG 验证待办 | 状态转移、失败退避、租约取消、阶段恢复；持久状态校验；PG 任务状态仓储已接入，跨实例恢复集成用例仅编译 |
| 1.9 内存目标 | 已实现 | 五类资源、授权/路由、故障注入、回执、共享契约测试；不是实际 ACG 等价证明 |
| 1.10 文件目标 | 已实现 | 确定性脱敏报告、临时文件原子替换、发布失败不提交模拟状态；不承诺断电持久性或从报告恢复状态 |
| 1.11 CLI | 代码完成，真实 PG 验证待办 | validate；run 完成迁移、登记、租约等待、状态机驱动与信号退出；预检为显式未实现，任务停在 PRECHECK |
| 1.12 泄漏门禁 | 部分完成 | 生成产物扫描与 gitleaks 仓库扫描通过；PG 全文本扫描依赖真实 PG，尚未完成 |
| M2～M5 | 未开始 | 各里程碑开工前补齐细化计划 |

## 本批审查修复

- 配置加载支持 ConfigMap 的公开符号链接，同时拒绝逃逸配置根目录和目录循环；校验源字段默认值类型。
- 幂等回执绑定 OpID 的不可变操作意图。新意图使用新 OpID，允许 A→B→A 与删除后重建；旧回执重试不覆盖较新状态。同一 OpID 跨批次换资源被拒绝。
- 同批重复 OpID 整批拒绝；文件输出保留被冲突请求引用的原操作记录。
- 文件目标能力查询与发布使用同一锁，竞态回归通过。
- design v3.2 第 8.4 节澄清：远程有效幂等键必须包含 OpID、操作类型和内容键，不能仅按历史内容永久去重。

## 最新验证

| 检查 | 结果 |
| --- | --- |
| `go test -race -count=1 ./...` | 全部现有测试包通过，包括泄漏测试 |
| `go vet ./...` | 通过 |
| 独立 `test/schema/check.py` | Schema 文档、19 个共享用例、4 份配置样例通过结构校验 |
| `go run ./cmd/migrator validate --config-dir configs` | structural_validation_passed；1 job、1 source、1 instance、1 target、4 lookups |

Python Schema 门禁不包含格式插件语义验证；URL、时间及跨资源引用由 Go 侧校验。泄漏测试只覆盖当前实现生成的产物，不代表未来 PG、读取器或 ACG 请求路径已覆盖。

Go 使用 `go1.26.10 darwin/arm64`。本次离线验证使用：

```sh
export GOPATH=/private/tmp/newapi2acg-gopath
export GOMODCACHE=/private/tmp/newapi2acg-mod-cache
export GOCACHE=/private/tmp/newapi2acg-go-cache
export GOPROXY=file:///Users/liam/go/pkg/mod/cache/download
```

`go.yaml.in/yaml/v3 v3.0.4` 已通过现有模块与校验和缓存锁定，未关闭校验。Schema 测试使用本机缓存的 jsonschema 4.26.0，完整版本见 `test/schema/requirements.txt`。

## 尚未完成的门禁

- PG、双实例抢锁、持久恢复、源数据库与真实 ACG 集成验证均未执行，M1 总体验收未完成。

PG 任务状态仓储及 CLI run 已接入（见下方 Task 1.8/1.11 批次）。M1 剩余：真实 PG 集成验证、PG 全文本泄漏扫描、golangci-lint/gitleaks 门禁、Git 初始化。

本地上游参考保持只读：`new-api/` 对应 commit `bde9b2f44887d34ec54799ae191d50f97914359e`。源码差异详见 `docs/compatibility/new-api.md` 与 `new-api-schema.md`；源码核验不能代替部署版本或运行行为核验。

## Task 1.5 代码批次

- 新增 `internal/state/pg/migrations/001_init.sql` 和 `migrate.go`；所有业务表带 job_id，目标相关表带 target_id，镜像相关表带 generation。
- naming_registry 的 target/kind/name 唯一约束跨任务生效；pending 使用全局序列避免出队再入队的版本复用。
- 集成测试覆盖空库、幂等迁移、跨任务名称唯一性；使用独立临时数据库，缺少 DSN 时明确失败。本轮仅编译，未连接真实 PG。
- 驱动 `github.com/lib/pq v1.12.3` 通过本机模块及 sumdb 缓存验证并锁定；未关闭模块校验。迁移 API 使用 database/sql，不绑定驱动。
- 添加可选 compose 配置但未执行；用户“不下载数据库容器”的限制保持不变。
- 最新验证：全量 `go test -race -count=1 ./...` 通过；`go vet -tags=integration ./...` 通过；带 integration tag 的 DSN 隔离离线回归通过。真实 PG 三项集成用例未运行。
- 独立审查发现并修复测试 DSN 的 dbname/database 参数覆盖临时数据库问题；迁移前增加 current_database() 核对。

## Task 1.6 代码批次

- 新增 `pg.AcquireLease`、`Lease.Context/Close`；使用独立数据库连接管理器和专用会话，销毁物理连接释放锁，不归还给状态库连接池。
- 失效、健康查询超时和父上下文取消均停止租约；不自动重连延长旧租约。检测窗口为检查间隔加查询超时。
- 离线测试验证取消、超时、并发关闭、冲突/无效请求清理和稳定任务锁键。真实 PG 三项用例及不同任务可并行检查已编写，仅编译，未执行。
- 不下载镜像或启动数据库。CLI run 尚未接入；真实 PG 验证和目标端 fencing 仍不能由这些离线测试证明。
- 审查修复：驱动查询不响应取消时，独立超时监控仍取消租约；关闭底层 socket 后再销毁 SQL 会话，避免等待查询锁而无法释放连接。新增卡死探测和 socket 关闭回归。
- 最新验证：全量竞态测试通过；带 integration tag 的 go vet 通过；集成测试编译及 DSN 隔离离线用例通过。真实 PG 用例未运行。

## Task 1.7 代码批次

- 新增 `internal/state/{store,types}.go` 与 PG Store/Tx，repeatable-read 短事务绑定 job/target；失败后禁止提交部分批次。
- 镜像读写与 GTID 位点同事务；JSON 数字保留 json.Number，后续映射按契约解析，避免整数精度损失。
- 队列使用单调入队版本，出队条件包含版本；删除再入队也不复用旧版本。
- 操作意图不可更换绑定，结果核对原意图，成功时同步更新 ID、完整逻辑资源及授权回执；目标调用仍在事务之外。
- 新增迁移 002 的 resource_intents 表，保存当前资源意图。旧意图重放不推进指针，迟到旧结果不能覆盖新状态。迁移 001 原文未修改；升级前遗留的未决意图无当前指针时保守拒绝补记，需要观察目标并重新计算新意图。
- 密码初始化回执尚未实现，明确拒绝 User.Initialization；不静默丢失初始化标记。依赖、命名、失败队列和 CLI run 尚未接入。
- 离线单元/竞态测试通过，带 integration tag 的 go vet 与编译通过。真实 PG 用例覆盖回滚、可见性、版本出队、意图重放、结果回滚和迟到回执，但本轮未执行。
- 无数据库镜像下载或容器启动；无 Git 提交或推送。

- 复审修复：新意图将旧 pending/unknown 操作终结为 failed/superseded，并原子重新入队当前资源，避免旧操作永久阻塞切换门禁。迟到回执不改变当前资源；对应离线和集成回归已补齐。

## Task 1.8 / 1.11 代码批次：PG 任务状态仓储与 CLI run

- `service.JobState.Validate`：只接受状态机可达的状态组合（活动状态无 resume_from；PAUSED 必须带活动 resume_from；原因长度上限 512）。从 PRECHECK 出发穷举转移，验证所有可达状态都通过校验。
- `service.ReasonError`：处理器可显式提供固定的安全原因写入 `jobs.reason`；普通错误仍只写“<状态> work failed”，驱动/目标错误文本不会落库。
- `pg.JobRepository`（`internal/state/pg/jobs.go`）实现 `service.Repository`：
  - `Ensure` 仅在任务不存在时以 PRECHECK 插入，不覆盖已有状态与 config_hash；配置版本切换留给热加载协议（design 8.3）。
  - `Load` 对缺失任务返回 `ErrJobNotFound`，对不合法的持久状态拒绝运行；`Save` 写前校验，只更新 state/resume_from/reason，影响行数不为 1 时报错。驱动错误全部脱敏。
- `migrator run`（`cmd/migrator/run.go`）：加载并校验配置 → 按目标 `state_dsn_env` 读取 DSN（缺失时只报变量名，不连接）→ 连接状态库并迁移 → 登记任务 → 租约被占用时等待重试 → 驱动 runner。
  - 预检尚未实现：处理器返回 `ReasonError`，任务停在 PRECHECK 并记录原因，不会在桩实现上推进到 SNAPSHOTTING。
  - PAUSED/STOPPED 时持有租约空闲，避免 Deployment 反复重启；恢复操作随 M4 管理接口提供。
  - 退出码：SIGINT/SIGTERM 为 0 并释放租约与连接池；租约丢失、持久状态无对应处理器、状态库不可用为 1；参数或任务选择错误为 2。
- 集成测试共用 `internal/state/pg/pgtest`（integration tag）创建临时数据库；pg 包原有集成测试改为调用它，行为不变。
- 新增集成用例（仅编译，未执行）：`TestJobRepositoryPersistsAcrossInstances`、`TestJobRepositoryRejectsCorruptPersistedState`、`cmd/migrator` 的 `TestRunEmptyJob`（登记、PRECHECK 原因、第二实例拿不到租约、退出后租约释放）。

验证（本批，离线）：

| 检查 | 结果 |
| --- | --- |
| `gofmt -l cmd internal test` | 无输出 |
| `go vet ./...`、`go vet -tags=integration ./...` | 通过 |
| `go test -race -count=1 ./...` | 全部通过，含新增 CLI、仓储、状态校验用例 |
| `go test -tags=integration -run 'TestIsolatedDatabaseURL\|Offline\|DSN' ./...` | 所有集成测试编译通过；离线可运行的 DSN 隔离用例通过 |
| `migrator run`（未设 DSN） | 报 `environment variable MIGRATOR_STATE_DSN is not set`，退出 1 |
| `migrator run`（DSN 指向 127.0.0.1:1 的关闭端口） | `state store unavailable: connect state database failed`，退出 1，日志无 DSN 内容 |

未完成 / 已知边界：

- 真实 PG 未执行：迁移 SQL、仓储、租约、`TestRunEmptyJob` 都只经过编译与离线替身验证。
- 状态写入未做 PG 侧 fencing：失去租约的旧实例在检测窗口内（检查间隔 + 查询超时）仍可能写 `jobs` 或状态事务。可选改进：租约记录 `pg_backend_pid()`，状态写入以 `pg_locks` 中该会话仍持有任务锁为条件。
- 单进程只运行一个任务（`--job` 或唯一任务）；多任务调度未实现。
- 任务定义（`jobs.definition`）只在首次登记时写入，配置变更后不更新。

## 下一步

0. ~~初始化 Git、lint、gitleaks~~：已完成，见“仓库与工具门禁”。
1. 有可用测试 PG 时运行：`MIGRATOR_TEST_PG_DSN=... go test -tags=integration ./internal/state/pg/... ./cmd/migrator/...`，补齐 M1 PG 验收（迁移、锁、短事务、仓储、空任务启动）。
2. Task 1.12：泄漏扫描扩展到 PG 全部文本列（依赖第 1 步的真实 PG）。
4. M1 验收后，按计划先写 M2 细化计划（`docs/plans/<日期>-m2-*.md`），再从契约探测与 MySQL 快照世代开始。

## 仓库与工具门禁

- Git：`main` 已推送到 `git@github.com:hyperbolic-c/newapi2ACG.git`。首批两个提交：设计与计划文档；M1 骨架实现。`new-api/` 上游参考副本保持 gitignore，不入库。
- 工具：Homebrew 安装 gitleaks 8.30.1、golangci-lint 2.15.0。
- gitleaks：默认规则；仅对 gitignore 的上游参考副本（`new-api/`、`references/new-api/`）设路径白名单，此前 10 条命中全部来自该目录的上游测试样例。项目自身代码与文档无命中。
- golangci-lint（standard 规则集）：修复 26 处问题，无 nolint。尽力清理路径改为显式 `_ =` 并注明原因；测试中验证释放/回滚的调用改为检查错误；`run` 关闭租约失败时记录告警；修正一处自比较断言。errcheck 只豁免写 stderr 的 `fmt.Fprint*`。
- 当前结果：`golangci-lint run` 与 `--build-tags=integration` 均 0 issues；`make leak-scan` no leaks；`go test -race ./...` 全部通过；Schema 门禁通过 `uv run --no-project --with-requirements test/schema/requirements.txt python test/schema/check.py` 运行并通过。
