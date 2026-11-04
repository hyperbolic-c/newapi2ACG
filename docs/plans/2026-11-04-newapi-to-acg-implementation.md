# New API → ACG 实施计划（对应 design v3.3）

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> 只有“工程初始化”“表结构与能力核对”“服务骨架”三个阶段已细化到可直接执行的步骤。后面的阶段（MySQL 全量迁移、binlog 增量同步、定期核对与运维、对接真实 ACG）是任务级路线图：开工前先用 superpowers:writing-plans 写出 `docs/plans/<日期>-<功能名>.md` 细化计划，再执行。不要直接按路线图条目编码。

**Goal:** 按 `design-v3.3.md` 实现迁移服务。先交付可验证的 MySQL 全量与增量闭环，再补齐其他读取器、运维能力与真实 ACG 适配器。

**Architecture:** 读取 → 转换 → 写入。读取：首次全量一致性读，之后以 MySQL 从库身份订阅 binlog；binlog 订阅、全量读取、回源查询都连接 New API 的同一个 MySQL 实例（优先只读从库，design 9.2）。binlog 消费者由每行变更前后的值确定受影响的 ACG 资源，与 binlog 位置一起写入待写入队列；写入器回源 MySQL 查询这些资源当前的数据，转换后与上次写入内容比较，有变化才通过 ACG 管理接口写入，写前登记、写后记录结果。服务不保存 New API 数据副本（design 二、9.6、11.3）。

**Tech Stack:** Go；PostgreSQL；CEL（cel-go）；YAML + JSON Schema；MySQL ROW/FULL/GTID；go-mysql-org/go-mysql replication 与 TiDB parser；Docker Compose；Kubernetes。依赖版本在 Task 0 验证并锁定，本计划不写死版本号。

---

## 1. 范围与约定

**依据：** `design-v3.3.md`。v3.1 评审发现的问题（撤权先于授权、令牌与用户状态、多实例冲突、密码只初始化一次、写入崩溃恢复等）已并入设计；v3.3 去掉了 v3.2 的“PG 中保存源数据副本”做法。本计划只引用章节，不重复论证。

**本期迁移：** 分组、渠道、用户、apikey 及授权。不迁移额度、计费、日志、历史用量、系统访问令牌和源密码哈希。PostgreSQL、SQLite、管理 API 全量读取属于本期，排在 MySQL 闭环之后。PostgreSQL CDC、LiteLLM、Higress、多实例共享聚合路由不在本期。

**目录：** 工程直接建在仓库根目录，不嵌套 `gateway-migrator/`。

**每个任务的固定节奏：**

1. 先写行为测试，运行并确认失败的原因与预期一致。
2. 写最小实现，让测试通过。
3. 运行任务列出的验证命令，确认输出符合“预期”。
4. 提交，提交信息使用任务给出的标题。

**测试约定：**

- 测试文件与被测文件同目录，`_test.go` 后缀；测试只断言外部行为，不断言内部调用次数。
- build tag：默认单元测试无 tag；需要 PG 或 MySQL 的用 `integration`；需要完整 compose 环境的用 `e2e`；需要真实 ACG 的用 `acg`。
- 固定样例的期望结果由人工按源端语义编写，或由独立的参考算法生成，不由被测代码生成后回写。

## 2. 开工前提

| 前提 | 阻塞范围 | 负责 |
| --- | --- | --- |
| 实际部署的 New API 分支与版本（官方或定制版，见 2026-10-29 会议） | 表结构与能力核对阶段及之后所有涉及表结构的任务 | 业务方 |
| 生产库脱敏的表结构与样例 | 表结构与能力核对阶段的任务 0.2、0.4 | DBA / 业务方 |
| 确定服务连接的 New API MySQL 实例：优先只读从库，地址指向固定实例（不能是负载均衡地址）；该实例满足 design 9.2（log\_bin、从库需 log\_replica\_updates、ROW/FULL、GTID、binlog 保留时间）；申请 server\_id 和账号权限 | MySQL 全量迁移（预检）、binlog 增量同步 | DBA |
| ACG 能力逐项结论（design 7.3） | 对接真实 ACG；之前各阶段不受阻塞 | ACG 团队 |
| design 12.2 中的业务决策 | 对应策略的默认值；不阻塞编码，阻塞生产启用 | 业务方 |

缺少前两项时，可以先完成工程初始化和服务骨架；MySQL 全量迁移以官方最新稳定版的表结构作为临时字段清单，待表结构核对结论到位后修正。

---

## Task 0：工程初始化

**创建：** `.gitignore`、`go.mod`、`Makefile`、`.golangci.yml`、`.gitleaks.toml`、`README.md`

1. `git init`；`.gitignore` 覆盖 `.DS_Store`、`bin/`、`*.env`、`.env*`、`test/output/`、`*.local.yaml`。
2. 确认本机 Go 版本：`go version`，在 `go.mod` 中写 `go` 指令为该次要版本。`go mod init`，模块路径与团队仓库确认，暂用 `newapi2acg`。
3. 在 Go 官方源核实并锁定以下依赖的当前稳定版：cel-go、go-mysql、TiDB parser、pgx、yaml、JSON Schema 校验库。版本写入 `go.mod`，验证记录写入 `README.md` 的“依赖”一节。
4. `Makefile` 目标：
   - `test`：`go test ./...`
   - `test-integration`：`go test -tags=integration ./...`
   - `test-e2e`：`go test -tags=e2e ./test/e2e/...`
   - `lint`：`go vet ./...` 与 golangci-lint
   - `leak-scan`：gitleaks 扫描仓库与 `test/output/`
5. 运行 `make test lint leak-scan`。预期：没有测试，命令以 0 退出。
6. 把现有设计文档一并纳入首次提交（`design*.md`、`AI 算力网关*.md`、`会议.md`、`docs/`）。
7. 提交：`chore: initialize Go project and tooling`。

---

## 表结构与能力核对

这一阶段产出的是文档和测试数据，不写业务代码。

### Task 0.1：确认源端分支与版本

**创建：** `docs/compatibility/new-api.md`

1. 记录实际部署的 New API 来源（官方 / 定制版仓库地址）、版本号、镜像 digest；定制版需列出与官方版在 channels、users、tokens 三张表上的差异。
2. 选定 e2e 使用的镜像：与生产同源同版本；无法获取定制版镜像时，记录差异与风险。
3. 提交：`docs: record New API source version`。

### Task 0.2：采集表结构与枚举

**修改：** `docs/compatibility/new-api.md`；**创建：** `configs/sources/new-api.yaml`

1. 采集三张表的完整列定义（类型、默认值、是否可空），写入文档。
2. 从该版本源码中摘录渠道状态、用户状态、令牌状态、用户角色的全部取值及含义。
3. 按 design 3.1 写出字段清单：每列要么声明，要么列入 `dropped` 并写原因。
4. 提交：`docs: capture New API schema and enums`。

### Task 0.3：验证授权语义

**修改：** `docs/compatibility/new-api.md`

在与生产同版本的 New API 实例上实测并记录（附请求与响应）：

1. 令牌 `model_limits_enabled=true` 且限制列表为空时，请求任意模型的结果。
2. 用户被禁用后，其令牌能否继续调用。
3. 令牌已过期、额度耗尽时的鉴权结果。
4. 令牌分组为空、用户分组为空时，实际使用的分组。
5. 同一模型多渠道、不同优先级与权重时的候选集合规则。

结论与 design 5.6 不一致时，先修改 design，再继续。提交：`docs: verify New API token and user semantics`。

### Task 0.4：验收样本

**创建：** `test/fixtures/newapi/basic/`、`test/fixtures/newapi/basic/expected-candidates.yaml`

1. 脱敏样本至少包含：两个分组、三个渠道、两个被多个渠道共用的模型、一个跨分组令牌、一个开启模型限制的令牌、一个限制列表为空的令牌、一个禁用用户、一个软删除用户、重名渠道、大小写冲突渠道名、多密钥渠道。
2. 对每个“令牌 × 模型”写出 New API 实际的候选渠道集合，作为后续等价校验的独立期望。
3. 提交：`test: add New API acceptance fixtures`。

### Task 0.5：ACG 能力登记

**创建：** `docs/compatibility/acg.md`、`configs/targets/acg-test.yaml`

1. 按 design 7.3 与 12.2 逐项登记：已确认 / 待确认 / 不支持；未确认的一律写 `unknown`，不预设 `true`。
2. 提交：`docs: register ACG capability status`。

**验收：** 字段清单中每个范围内的字段都有映射或 dropped 原因；额度和高级设置造成的行为损失都已写明；0.3 中的五个问题都有实测结论。

---

## 服务骨架

### Task 1.1：资源标识

**创建：** `internal/model/identity.go`、`internal/model/identity_test.go`

1. 测试：
   - `TestIdentityRoundTrip`：`newapi-prod/Provider/channel:12` 解析后再格式化，结果不变。
   - `TestIdentityRequiresInstance`：缺少实例部分时解析报错。
   - `TestIdentityRejectsUnknownKind`：kind 不属于五类资源时报错。
   - `TestIdentityLocalPartIsOpaque`：局部标识中的 `:`、`/` 不影响 kind 与 instance 的解析。
2. 实现 `type Identity struct{ Instance string; Kind Kind; Local string }` 及 `Parse`、`String`；`Kind` 为 Group、Provider、AggregateRoute、User、ApiKey。
3. `go test ./internal/model/...`，预期 PASS。
4. 提交：`feat(model): add typed resource identity`。

### Task 1.2：逻辑资源与规范化哈希

**创建：** `internal/model/{resource,canonical}.go` 及测试、`schemas/acg/v1/*.json`

1. 测试：
   - `TestHashIgnoresSetOrder`：provider\_refs、route\_refs、members、api\_keys 指纹顺序不同，哈希相同。
   - `TestHashDedupsSets`：集合中的重复元素不影响哈希。
   - `TestHashExcludesInitOnlyFields`：User 的初始密码相关字段不参与哈希（design 5.5、6.1）。
   - `TestHashChangesOnManagedField`：任一托管字段变化，哈希变化。
   - `TestResourceValidate`：必填字段缺失、引用 kind 错误时校验失败。
   - `TestSchemaAgreesWithGoTypes`：每类资源的样例同时通过 Go 校验与 JSON Schema 校验，删除一个必填字段后两者都失败。
2. 实现五类资源结构体、统一元数据（source\_identity、migration\_job、content\_hash）、`Canonicalize` 与 `Hash`；授权只表达两个引用集合（design 7.1）。
3. `go test ./internal/model/...`，预期 PASS。
4. 提交：`feat(model): add acg/v1 resources and canonical hash`。

### Task 1.3：SecretValue 与指纹

**创建：** `internal/secrets/{value,fingerprint}.go` 及测试

1. 测试：
   - `TestSecretFormatRedacted`：`%v`、`%s`、`%+v`、`%#v`、`%q` 均输出 `***`。
   - `TestSecretJSONRedacted`：作为结构体字段 JSON 序列化时输出 `"***"`。
   - `TestSecretSlogRedacted`：经 `log/slog` 的 text 与 JSON handler 输出时不含原值。
   - `TestSecretErrorWrapRedacted`：`fmt.Errorf("%w ... %v", err, secret)` 的结果不含原值。
   - `TestFingerprintSalted`：同一值在不同盐下指纹不同，同盐下稳定；指纹以十六进制输出，报告只显示前 8 位。
2. 实现 `secrets.Value`（只有 `Reveal()` 能取原值）与 HMAC-SHA256 指纹；盐从环境变量名读取。
3. `go test ./internal/secrets/...`，预期 PASS。
4. 提交：`feat(secrets): add redacting SecretValue and fingerprint`。

### Task 1.4：配置加载与校验

**创建：** `internal/config/{load,validate}.go` 及测试、`schemas/config/{source,instance,job,target}.json`、`configs/jobs/example.yaml`

1. 测试：
   - `TestUnknownFieldRejected`：任务配置中出现未知字段时报错，并指出文件与字段路径。
   - `TestPolicyEnumValidated`：`on_source_delete`、`prune`、`drift`、`exhausted_token`、`cross_instance_conflict` 取值越界时报错。
   - `TestSecretsOnlyByEnvName`：DSN、凭据只允许 `*_env` 形式，配置文件出现字面值时报错。
   - `TestConfigHashStable`：同一组配置文件不论读取顺序，`config_hash` 都相同。
2. 实现 YAML 严格解析、JSON Schema 校验和 `config_hash`；默认值与 design 8.6 一致。
3. `go test ./internal/config/...`，预期 PASS。
4. 提交：`feat(config): load and validate job configuration`。

### Task 1.5：PG 状态库表结构

**创建：** `test/e2e/compose.yaml`（先只包含 postgres）、`internal/state/pg/migrations/001_init.sql`、`internal/state/pg/migrate.go` 及集成测试

1. 测试（`integration`）：
   - `TestMigrateFromEmpty`：空库执行后，design 8.7 列出的全部表存在。
   - `TestMigrateIdempotent`：重复执行不报错，不重复建表。
   - `TestNamingRegistryUnique`：同一 target + kind + 名称插入两次，违反唯一约束。
2. 按 design 8.7 建表；除全局表外均带 `job_id`；`pending` 带入队版本号；`operations` 带状态；`id_map` 带 `password_initialized_at`。表结构迁移的实现方式（内嵌 SQL 自带执行器或第三方库）在本任务中确定并写入 README。
3. `docker compose -f test/e2e/compose.yaml up -d postgres`；`go test -tags=integration ./internal/state/pg/...`，预期 PASS。
4. 提交：`feat(state): add migrator schema`。

### Task 1.6：任务锁

**创建：** `internal/state/pg/lock.go` 及集成测试

1. 测试（`integration`）：
   - `TestOnlyOneLeaseHolder`：两个 store 同时请求同一任务的锁，只有一个成功。
   - `TestLeaseLostCancelsContext`：从 PG 端终止持锁连接（`pg_terminate_backend`）后，`Lease.Context()` 在一个健康检查周期内被取消。
   - `TestLeaseReleasedOnClose`：释放后另一 store 可获取。
2. 实现：锁使用独立于连接池的专用连接；定期检查连接健康；连接失效即取消 Lease 的 context。
3. `go test -tags=integration ./internal/state/pg/... -run Lease`，预期 PASS。
4. 提交：`feat(state): add advisory lock lease`。

### Task 1.7：StateTx 基础语义

**创建：** `internal/state/{store,types}.go`、`internal/state/pg/store.go` 及集成测试

1. 测试（`integration`）：
   - `TestRollbackDoesNotAdvance`：在事务中入队并推进 binlog 位置后回滚，队列与位置都不变。
   - `TestEnqueueAndCheckpointAtomic`：提交后入队与位置同时可见。
   - `TestDequeueSkipsReenqueued`：取出 X（版本 1）后 X 再次入队（版本 2），按版本 1 出队不生效，X 仍在队列中。
   - `TestIntentVisibleAfterCommit`：登记的 pending 操作在新连接中可见。
2. 实现 design 11.2 中 `StateTx` 的入队、位置、写入登记、结果、出队相关方法；本任务不实现映射。
3. `go test -tags=integration ./internal/state/...`，预期 PASS。
4. 提交：`feat(state): implement transactional state store`。

### Task 1.8：服务状态机

**创建：** `internal/service/{runner,state_machine}.go` 及测试

1. 测试：
   - `TestTransitionsFollowTable`：只允许 design 8.1 表中列出的转换，其他转换报错。
   - `TestResumeFromPersistedState`（`integration`）：状态写为 STREAMING 后重启 runner，从 STREAMING 继续，不回到 PRECHECK。
   - `TestPrecheckFailureStaysInPrecheck`：预检失败停在 PRECHECK，并记录原因。
   - `TestLeaseLossStopsRunner`：Lease 被取消后，runner 在超时内退出全部工作协程。
2. 实现状态机与 runner 框架；各状态的工作函数暂为可注入的空实现。
3. `go test ./internal/service/...` 与 `go test -tags=integration ./internal/service/...`，预期 PASS。
4. 提交：`feat(service): add job state machine and runner`。

### Task 1.9：目标接口与共用接口测试

**创建：** `internal/target/target.go`、`test/contract/target_contract.go`、`internal/target/memory/{target,routing}.go` 及测试

1. 在 `test/contract` 中编写对任意 Target 都成立的接口测试函数，供各实现调用：
   - 同一幂等键重放不产生重复资源。
   - 同一来源标识的重放视为成功；被其他资源占用的名称或 key 返回冲突。
   - 一批变更中部分失败时，已成功项保留，结果按 OpID 关联。
   - Observe 能区分“字段不存在”和“字段不可读”。
2. 内存目标额外测试：
   - `TestRoutingFiltersByGrantBeforeSelection`：先按 apikey 的提供商授权过滤成员，再取最高优先级、按权重选择，跳过禁用成员。
   - `TestFaultInjection`：可按 OpID 注入失败、超时、“已生效但响应丢失”。
3. 实现 Target 接口（design 7.2）与内存目标。
4. `go test ./internal/target/... ./test/contract/...`，预期 PASS。
5. 提交：`feat(target): add target contract and memory target`。

### Task 1.10：文件目标与报告骨架

**创建：** `internal/target/file/target.go`、`internal/report/report.go` 及测试

1. 测试：
   - `TestFileOutputDeterministic`：相同输入两次输出字节一致。
   - `TestFileOutputRedacted`：输出中只有密钥指纹前 8 位，没有原值。
   - `TestFileWriteAtomic`：写入中途失败不留下半个文件。
   - 调用 Task 1.9 的共用接口测试。
2. 实现：临时文件加原子重命名；文件目标的状态与真实目标按 target 隔离。
3. `go test ./internal/target/file/... ./internal/report/...`，预期 PASS。
4. 提交：`feat(target): add file target and report skeleton`。

### Task 1.11：命令行入口

**创建：** `cmd/migrator/main.go`

1. 测试（`integration`）：`TestRunEmptyJob`：以只包含示例任务的配置启动 `run`，能获取锁、进入 PRECHECK，收到 SIGTERM 后正常退出并释放锁。
2. 实现 `run`、`validate` 子命令。
3. `go run ./cmd/migrator validate --config-dir configs`，预期以 0 退出；配置中加入未知字段后再运行，预期报出精确的文件与字段路径。
4. 提交：`feat(cmd): add run and validate commands`。

### Task 1.12：泄漏扫描门禁

**创建：** `test/leak/leak_test.go`

1. 测试：用 fixtures 中的全部密钥原值作为特征，扫描本次测试运行产生的日志、文件目标输出、报告和 PG 中的所有文本列，任何命中都失败。
2. 把 `go test ./test/leak/...` 加入 `make leak-scan`。
3. `make leak-scan`，预期 PASS。
4. 提交：`test: add secret leak gate`。

**验收：** 服务能启动、抢锁、推进空任务；重启后从 PG 恢复状态；双实例只有一个持锁；全部产物通过泄漏扫描。

---

## 纠偏：删除偏离 v3.3 的实现（已完成 2026-11-04，见进度文档“纠偏记录”）

服务骨架阶段按 v3.2 实现了“PG 中保存源数据副本”的部分，v3.3 已取消该做法。这些代码直接删除，不保留兼容层（尚无任何部署，迁移脚本可以直接改）：

| 删除或修改 | 位置 | 改为 |
| --- | --- | --- |
| 表 `mirror_rows`、`deps`、`generations`；`jobs.active_generation`；按副本版本分的 `checkpoints` | `internal/state/pg/migrations/001_init.sql` | `checkpoints` 每个任务一行：`g0`（全量开始位置）、`gtid_set`（增量已处理位置） |
| `RowChange`、`MirrorRow` 类型；`ApplyMirror`、`ReadMirror` | `internal/state/{types,store}.go`、`internal/state/pg/store.go` 及其测试 | 删除 |
| `Advance(gen, pos)`、`Position(gen)` | 同上 | `Advance(pos)`、`Position()`；新增 `SetFullSyncStart(g0)` |
| 状态 `SNAPSHOTTING`、`CATCHING_UP` 及事件 `SnapshotCollected`、`ActivateStream`、`ActivatePoll` | `internal/service/state_machine.go`、表 `jobs` 的 CHECK 约束 | 状态 `FULL_SYNC`；事件 `PrecheckPassed`（→ FULL\_SYNC）、`FullSyncDone`（→ STREAMING）、`FullSyncDonePoll`（→ POLLING） |
| 计划文件 `2026-11-04-mysql-full-read.md` 中的副本版本、存副本任务 | `docs/plans/` | 重写为“读取：从 MySQL 全量读出与按条件查询 New API 数据” |

验收：`go test -race ./...`、两种 tag 的 vet 与 lint、泄漏扫描全部通过；代码中不再出现 mirror、generation 相关符号（`grep -rn 'Mirror\|generation' internal cmd` 无结果）。

## MySQL 全量迁移（开工前先写细化计划）

按 读取 → 转换 → 写入 拆成三份细化计划：[读取：从 MySQL 全量读出与按条件查询 New API 数据](2026-11-04-mysql-read.md)（已写）、转换：规则引擎与 New API 映射规则、写入：全量写入 ACG。

目标：源库静止时，全量结果通过固定样例对照与候选渠道等价校验；中断后重跑无重复资源。

| 任务 | 内容 | 关键测试 | design |
| --- | --- | --- | --- |
| 2.1 字段核对与类型转换 | `source/contract`：缺列阻止、未知列告警、tinyint/JSON/时间转换 | 保留字列名、缺列、未知列、转换失败即报错且不回显原值 | 3.1–3.4 |
| 2.2 MySQL 全量读取 | `source/mysql`：一个只读一致性读事务读完全部表、主键分页、读取前记录 G0 和所连实例的 `@@server_uuid` | 分页不漏行；读取期间并发修改不可见；连接中断即报错 | 3.3、9.4 |
| 2.3 按条件回源查询 | `source/mysql`：`Query` 在一个一致性读事务里按条件查询（按 ID、按分组、按用户、按模型），密钥以 `secrets.Value` 返回；每次查询校验 `@@server_uuid` 与记录的实例一致 | 条件生成安全 SQL；结果与全量读取的类型转换一致；实例不一致时报错并暂停任务 | 9.2、9.6、11.2 |
| 2.4 规则文件解析与表达式 | `mapping/dsl`、`mapping/cel`：字段类型声明、`${}` 写法、成本限制、规则依赖顺序 | 引用不存在的字段在加载时报错并给出路径；循环引用报错 | 4.1–4.2 |
| 2.5 规则执行 | `mapping/eval`：join、let、when、for\_each、group\_by、ref\_select；`Full` 与 `Compute` | 缺失 / null / 空串三种情况；when 变假时出现在 Removed | 4.1–4.4 |
| 2.6 New API 映射规则 | `configs/mappings/newapi-to-acg/*`、`configs/lookups/*`、固定样例对照 | 先分组/用户/提供商，再路由，最后授权；覆盖表结构核对阶段准备的样本 | 5.3–5.6 |
| 2.7 资源命名 | `mapping/naming`：首次全量一次性分配、重名全部追加 ID、名字冻结、别名表 | 输入顺序不影响命名；与 ACG 中非迁移资源冲突进入失败队列 | 5.3 |
| 2.8 比较与写入顺序 | `engine/diff.go`、`engine/planner.go` | 三方比较全部分支（含“双方改成相同新值”）；依赖顺序；apikey 先撤后授、新建先禁用 | 8.3、8.4 |
| 2.9 写入器与崩溃恢复 | `engine/writer.go`、`engine/recovery.go` | 写入登记先于调用 ACG 提交；结果未知时先查 ACG 再决定；回源重算后无变化不写；密码只初始化一次 | 6.1、11.3 |
| 2.10 全量流程 | `pipeline/full.go`：记录 G0 → 一致性读 → 转换 → 全部资源入队 → 写入器写完 | 首次全量结果等于固定样例；写入中途强杀后重跑，无重复资源 | 8.1、9.4 |
| 2.11 预览报告 | `report`：路由名、改名清单、不迁移字段、保真度、冲突 | 报告确定且脱敏；dry\_run 切换为真实写入时重新读取 ACG 现状 | 8.1 |
| 2.12 源库预检 | `source/mysql/precheck.go`：读取 `log_bin`、`log_replica_updates`、`binlog_format`、`binlog_row_image`、`gtid_mode`、`binlog_expire_logs_seconds`、表引擎、账号权限；比较多条连接的 `@@server_uuid` | 每一项不满足都停在 PRECHECK 并给出具体原因；模拟负载均衡（两条连接 uuid 不同）时拒绝 | 8.1、9.2 |

本阶段验收只覆盖读取期间源库不变的情况；源库有并发写入时的正确性由 binlog 增量同步阶段保证。第一份可演示交付物：配置校验 + 五类资源样例对照 + MySQL 全量 + 脱敏预览报告。

## binlog 增量同步（开工前先写细化计划）

目标：全量读取期间及之后的源库变更都能同步到 ACG；增量后 ACG 内容等于对源库当前数据全量转换的结果；逐点强杀后不丢不重。

| 任务 | 内容 | 关键测试 | design |
| --- | --- | --- | --- |
| 3.1 GTID 集合 | `source/position.go`：解析、包含、合并、差集 | 多 UUID、区间合并、包含判断 | 9.5 |
| 3.2 事务组装 | `source/mysql/binlog.go`：XID 与 QueryEvent 边界、跨表事务、重连续传；实例配置增加 `access.binlog.server_id`；binlog 连接的 `@@server_uuid` 必须与查询连接一致 | 回滚事务不输出；只输出完整事务；重连后不重复不遗漏；重连到不同实例时拒绝继续 | 9.2、9.3、9.5 |
| 3.3 列过滤 | `source/mysql/filter.go`：只比较字段清单中的列 | 只改额度的事务只推进位置 | 9.7 |
| 3.4 DDL 处理 | `source/mysql/ddl.go`：TiDB parser，解析失败按保守策略 | 加列后重新核对通过并继续；缺列进入 PAUSED | 9.7 |
| 3.5 受影响资源 | `mapping/affected`：由变更前后的值得到资源标识与“分组/用户下全部令牌待重算”标记 | 渠道换组、模型增删、空分组首个渠道、最后一个成员移除、用户启停 | 9.6 |
| 3.6 消费者 | `pipeline/stream.go`：每个源事务一个 PG 事务（入队 + 位置）、背压 | 位置与入队原子；超过 max\_pending 时暂停 | 9.5、11.3 |
| 3.7 全量与增量衔接 | 从 G0 订阅 | 全量读取期间插入、删除、重复修改同一行，增量处理后 ACG 与源库一致 | 9.4 |
| 3.8 失败隔离 | `engine/retry.go`：失败队列去重、阻塞传播、重试时回源重算 | 删除或配置变化后，重试不重建过时资源 | 8.2 |
| 3.9 端到端增量与恢复 | `test/e2e/{incremental,recovery}_test.go`，compose 增加 MySQL（GTID）与 New API | 随机变更序列属性测试；按 design 11.4 的每个提交点逐点强杀 | 11.4 |

第一份可运行的同步版本必须通过本阶段的崩溃恢复测试。

## 定期核对、其他数据源与运维（开工前先写细化计划）

| 任务 | 内容 | 关键测试 | design |
| --- | --- | --- | --- |
| 4.1 定期核对 | `engine/reconcile.go`：全量读取 → 与上次写入、ACG 现状三方比较 → 差异资源入队 | 与增量同时运行不回退；待写入或在途资源不误判 drift | 8.3 |
| 4.2 配置热加载 | `config/reload.go` | 坏配置保留旧版本；切换后受影响资源入队，last\_applied 带新 config\_hash | 8.3 |
| 4.3 其他全量读取器 | `source/{postgres,sqlite,api}`、`pipeline/poll.go` | 与 MySQL 共用字段核对与类型转换测试；API 连续多轮缺失才按删除处理 | 9.9 |
| 4.4 管理接口与审计 | `service/admin.go`、`cmd/migrator/admin.go` | 鉴权；所有变更操作写审计；pause / resume / stop | 8.8、10.3 |
| 4.5 resync 与回滚 | `engine/rollback.go` | resync 保留归属与命名；回滚前必须 STOPPED；无法恢复的密钥写入报告 | 8.8 |
| 4.6 指标与探针 | `service/{metrics,health}.go` | 指标覆盖 design 8.5 所列项；GTID 差以事务数表示 | 8.5 |
| 4.7 部署 | `deploy/k8s/*`、`docs/runbook.md` | 单副本 Recreate；首次能力检查失败停在 PRECHECK 并给出原因 | 8.5 |
| 4.8 切换检查 | `admin cutover-check` | 按 design 8.9 第 5 步逐项检查，任一不满足即拒绝 | 8.9 |
| 4.9 规模测试 | `test/e2e/scale_test.go` | 正常与峰值规模下记录内存、事务时长、回源查询负载与写入速率，回填 8.6 阈值 | 11.4 |

## 对接真实 ACG（ACG 接口定稿后，先写细化计划）

| 任务 | 内容 | 关键测试 |
| --- | --- | --- |
| 5.1 能力探测 | `target/acg/capabilities.go`：对照 `docs/compatibility/acg.md` | 缺少安全幂等、授权隔离或密码保护时，预检阻止对应资源 |
| 5.2 观测与写入 | `target/acg/{client,observe,apply}.go` | 通过 `test/contract` 共用接口测试；密钥写入回执；部分成功 |
| 5.3 语义验证 | `test/e2e/acg_test.go` | 原模型名、原 key、前缀、多密钥、IP 限制、过期、强制改密、先过滤授权再选路 |
| 5.4 切换演练 | 在测试环境完成全量、增量、对账、cutover-check 和一次完整切换 | 按 design 5.7 两层等价校验 |

模拟目标通过不等于真实 ACG 等价。

---

## 3. 验收矩阵

| 门禁 | 必须证明的行为 | 验证方式 |
| --- | --- | --- |
| 表结构与能力核对 | 字段全覆盖；授权语义有实测结论 | 兼容性文档评审 |
| 服务骨架 | 重启恢复状态；任务互斥；产物脱敏 | PG 集成测试、写入目标共用接口测试、泄漏扫描 |
| MySQL 全量迁移 | 五类资源和授权正确；命名确定；中断续跑幂等 | 固定样例对照、独立候选集合、全量端到端 |
| binlog 增量同步 | 增量后 ACG 内容等于全量转换结果；故障恢复后一致 | 随机事件序列、逐提交点故障注入 |
| 定期核对与运维 | 对账不覆盖新状态；坏配置不生效；各读取器一致；切换检查可靠 | 并发对账、热加载、跨驱动样例、部署与切换演练 |
| 对接真实 ACG | 真实候选渠道与授权等价；客户端切换符合报告 | ACG 共用接口测试、业务流量演练 |

选路验收分两层：先比较启用且被授权的候选成员集合，再比较最高优先级集合与权重；每个 apikey 单独比较，不能只比较“分组 × 模型”，也不能用“多次随机请求大致正常”代替集合等价证明。

**依赖顺序：** 工程初始化 → 表结构与能力核对（可与服务骨架并行）→ 服务骨架 → MySQL 全量迁移 → binlog 增量同步 → 定期核对与运维 → 对接真实 ACG。与 ACG 团队的能力确认从第一阶段开始持续进行，对接真实 ACG 之前的各阶段都不需要等 ACG 接口定稿。

**最终交付：** Go 服务、五类资源 schema、New API 配置包、四种读取方式、模拟 / 文件 / ACG 目标、自动化测试、K8s 清单、对账与改名报告、兼容性矩阵和切换 runbook。
