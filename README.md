# New API → ACG

按 [design v3.2](design-v3.2.md) 开发的单向迁移与持续同步服务。
实施任务见 [实施计划](docs/plans/2026-11-04-newapi-to-acg-implementation.md)。

## 当前实现

- 五类逻辑资源、来源标识、规范化哈希和 JSON Schema。
- 严格 YAML 配置加载、引用检查、配置哈希，支持 ConfigMap 投射符号链接。
- SecretValue 脱敏、HMAC 指纹、报告短指纹。
- 服务状态机和可取消 runner；内存目标的授权、路由、故障注入和幂等回执。
- 文件目标原子发布脱敏报告；CLI `validate` 校验配置结构及引用。
- CLI `run`：迁移状态库、登记任务、获取租约后驱动状态机；预检尚未实现，任务停留在 PRECHECK 并记录原因。
- 上游 `new-api/` 固定为 `v1.0.0-rc.21`；源契约和字段清单见 [兼容性登记](docs/compatibility/new-api.md)。

PG 表迁移、租约、核心短事务和任务状态仓储已实现，尚未连接真实 PG 验证；源读取器、预检、映射与真实 ACG 适配器尚未实现。文件报告不能用于恢复完整密钥或跨进程恢复模拟状态。详见 [开发进度](docs/implementation-status.md)。

## 本地验证

Go 语言版本为 1.26。YAML 依赖已锁定在 `go.mod`/`go.sum`。

```sh
make test vet test-leak
go test -race ./...
go run ./cmd/migrator validate --config-dir configs
```

独立 JSON Schema 结构校验需要 `test/schema/requirements.txt` 中的 Python 依赖：

```sh
make test-schema PYTHON=/path/to/python
```

此门禁覆盖共享正反例与配置样例；URL、时间、跨资源引用等语义由 Go 校验，不声称 Python format 插件已验证这些语义。

```sh
make lint       # 需要 golangci-lint v2
make leak-scan  # 运行产物测试和 gitleaks 扫描
```

工具缺失时门禁失败，不静默跳过。当前无数据库或端到端用例，不能将带 tag 运行单元测试解释为集成验证。按用户要求，不下载或启动数据库容器。

## 依赖

已锁定 `go.yaml.in/yaml/v3 v3.0.4`，通过本机模块与校验和缓存离线验证；独立 Schema 测试使用 jsonschema 4.26.0。CEL、go-mysql、TiDB parser、pgx 等后续依赖尚未接入。

## Git 与远程

约定模块路径：`github.com/hyperbolic-c/newapi2ACG`。
用户指定远程：`git@github.com:hyperbolic-c/newapi2ACG.git`。

当前会话无法创建工作区 `.git`，也无法通过 SSH 访问 GitHub；因此尚未初始化本地仓库、配置 origin、创建开发分支、提交或推送。以下是恢复权限后的操作说明，并非已执行成功的操作：

```sh
git ls-remote git@github.com:hyperbolic-c/newapi2ACG.git
```

先确认远程是否已有历史。有历史时先检出并合并当前本地文件；远程为空时再初始化 `feat/m1-foundation` 分支、添加 origin 并按计划提交。不要覆盖远程历史或直接强推。

## 密钥约定

`secrets.Value` 只负责防止常规输出泄漏，不是内存加密。仅源转换和目标请求构造应显式调用 `Reveal()`；原值不能存入镜像、资源快照、日志或状态库。

指纹盐由环境变量提供，必须跨重启保持一致。完整指纹用于比较，前八位仅用于报告。序列化后的脱敏占位符不能反序列化成可用密钥，恢复写入必须回源读取并校验指纹。

## PG 状态库迁移（Task 1.5）

`internal/state/pg.Migrate(ctx, db)` 使用 `database/sql` 连接和内嵌 SQL；
不依赖外部迁移 CLI。当前 `001_init.sql` 创建 design 8.7 的 15 张业务表，
以及独立的 `migrator.schema_migrations` 元数据表。
后续 `002_resource_intents.sql` 增加每资源当前意图登记表。
迁移在单一事务中获取固定 advisory transaction lock，串行执行 DDL 和版本登记；
重复运行校验 SHA-256，不重新建表。已应用的 SQL 不应原地修改。
失败回滚；提交结果未知时重新执行并检查数据库记录。应用账号需要创建 schema、表和序列的权限。

驱动暂用已从本机缓存校验并锁定的 `github.com/lib/pq v1.12.3`，集成测试通过该驱动执行；
迁移 API 不绑定驱动，后续接入 pgx 不改变 SQL 迁移协议。`migrator run` 启动时执行迁移。

```sh
go test ./internal/state/pg/...
# 只编译集成测试，不连接数据库：
go test -tags=integration ./internal/state/pg/... -run '^$'
# 有可用的独立测试 PG 后，提供 URL 格式 DSN：
MIGRATOR_TEST_PG_DSN="$YOUR_TEST_PG_DSN" go test -tags=integration ./internal/state/pg/...
```

集成测试要求测试账号具有 CREATEDB 权限；每个用例新建临时数据库，结束后删除，
不会迁移或删除 DSN 指向的数据库。未提供 DSN 时显式失败。
`test/e2e/compose.yaml` 是可选测试配置，本轮未下载镜像或启动容器。
离线测试验证执行流程、回滚和校验和检查，不证明 PostgreSQL SQL 语法和约束已在真实服务器验证。
JSONB 列不能自动判定秘密数据，写入层仍须执行资源脱敏和密钥指纹化。

## 运行服务（Task 1.11）

```sh
export MIGRATOR_STATE_DSN='postgres://...'   # 由目标配置的 state_dsn_env 指定变量名
go run ./cmd/migrator run --config-dir configs [--job NAME]
```

启动顺序：加载并校验配置 → 连接状态库并执行迁移 → 登记任务（已存在则不改动状态与 config_hash）
→ 获取任务租约（被占用时按 `--lease-retry` 等待）→ 驱动状态机。
预检尚未实现，任务停留在 PRECHECK，原因写入 `migrator.jobs.reason`，按 `--work-retry` 重试。
PAUSED/STOPPED 任务持有租约空闲等待，避免 Deployment 反复重启。SIGINT/SIGTERM 正常退出（0）并释放租约；
租约丢失或持久化状态没有对应处理器时以 1 退出。日志和错误只出现环境变量名，不出现 DSN。

## PG 任务锁（Task 1.6）

`pg.AcquireLease(ctx, dsn, jobID, options)` 为任务创建专用 PG 会话并非阻塞获取
session advisory lock；同一数据库、同一任务只能有一个持有者，冲突返回 `ErrLeaseHeld`。
锁键由固定命名空间和 jobID 的 SHA-256 前 64 位生成；极低概率哈希冲突只会导致额外互斥。
租约连接独立于状态库连接池，禁止事务模式连接池代理，需直连 PG 或使用 session 模式。

`Lease.Context()` 可直接交给服务 runner。健康查询失败或超时会取消该上下文，
不会重连后继续工作；父上下文取消也结束租约。`Close()` 可重复、并发调用，
先取消工作上下文，再等待专用连接销毁以释放锁。任务执行器仍必须响应取消。
健康检测延迟上界为检查间隔加查询超时（另加调度延迟）；默认各为一秒。
租约不提供目标端 fencing，已经发往 ACG 的旧请求仍需依赖目标条件写入及后续收敛。

离线测试覆盖健康失败/超时、父上下文取消、并发关闭、竞争失败后的连接清理。
真实 PG 用例已编写但未执行：同任务竞争、不同任务并行、关闭后重获锁、
`pg_terminate_backend` 后取消上下文并重新获取锁。

健康检查采用独立超时监控；即使驱动探测没有响应 context，租约也会取消。
专用 dialer 保留会话 socket，关闭租约先关闭底层 socket，再清理 SQL 会话，
避免驱动查询阻塞时无法销毁连接。此路径有不响应取消的探测和阻塞 socket 回归测试。

## PG 短事务（Task 1.7）

`pg.Store.Begin(ctx, jobID, targetID)` 返回绑定任务和目标的 repeatable-read 事务。
当前实现镜像读写、GTID 集合读写、版本化队列、操作意图及结果回执、last_applied 读取。
GTID 集合的解析与包含关系由后续源消费者保证；Advance 只原子保存传入的已应用集合。
任一方法失败后 Commit 会回滚，调用方需在失败或序列化冲突后重建整个短事务。
目标 API 调用不能放在这些事务内；状态库账号、任务租约由上层负责配置和持有。

队列读取返回入队版本；再次入队获取新版本，Dequeue 仅删除相同版本。
意图按 OpID 绑定目标、资源、操作、内容哈希和幂等内容键；重放不能重置已完成状态。
结果携带 `state.AppliedResult`（变更、结果、配置哈希），事务先锁定并核验原意图，
成功后同步更新 operations、id_map、last_applied 及 API key 授权集合。
成功回执不可降级；同一成功操作重放不会再次覆盖资源状态。
Retryable 映射为持久化 unknown，保留队列，由后续收敛器重新观察目标。
更新前的 `Prev` 在意图事务中保存脱敏快照，避免目标调用之后才记录旧状态。
资源结果不自动出队，调用方必须在同一结果事务中提交携带原版本号的 Dequeue。

镜像写入必须来自已执行列白名单和秘密字段剥离的源适配器；JSONB 不能识别任意字符串是否为原始密钥。
密钥指纹必须是完整 64 位十六进制 HMAC；原始错误文本不写入状态表。
用户初始化密码回执、命名分配、依赖索引和失败队列留给对应后续实现，未提供空实现。
当前离线测试与真实 PG 用例源码均已提供；真实 PG 事务可见性、约束和恢复语义尚未实测。

结果回执还必须匹配 `resource_intents` 中当前操作；旧操作晚到不能覆盖较新的资源状态。
旧意图重放不改变当前指针。迁移 002 不推断已有未决操作的先后顺序；它们需观察目标后
重新计算并登记新意图，不能直接补记旧回执。JSON 镜像数字读取为 json.Number，映射层按契约解析。
目前携带 User.Initialization 的操作明确报错，待初始化完成标记接入后开放。

新意图注册时，旧 pending/unknown 意图在同一事务中变为 failed，error_code 为 superseded；
它表示被新意图取代，不进入资源失败队列。当前资源同时重新入队，以便重新观察并收敛。
迟到旧回执不更新当前资源状态，也不会将旧操作重新打开为 pending/unknown。
