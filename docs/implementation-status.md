# 开发进度

更新：2026-11-04。用词说明见 [README](../README.md#几个用词)。

服务分三步：**读取** New API 的 MySQL → **转换**成 ACG 资源 → 调用 ACG 管理接口**写入**；同步方式为首次**全量**、之后以从库身份订阅 **binlog 增量**、外加**定期核对**。服务不保存 New API 数据副本，需要关联数据时回源查询（design v3.3）。下表按这个结构列出进度。

## 总览

| 部分 | 状态 | 说明 |
| --- | --- | --- |
| New API 表结构核对 | 源码核对完成，实库核对待办 | 对照上游 v1.0.0-rc.21 源码，三张表共 76 列：34 列迁移，42 列不迁移（额度、日志、运行数据等），逐列原因见 [new-api-schema.md](compatibility/new-api-schema.md)。实际部署是否为定制版、令牌状态等行为尚未在真实实例上验证 |
| **读取**：从 MySQL 读全量、按条件回源查询 | 未开始 | 计划待纠偏完成后编写 |
| **读取**：binlog 增量 | 未开始 | |
| **转换**：规则引擎与 New API 映射规则 | 未开始 | |
| **转换**：ACG 资源定义 | 已完成 | 提供商、聚合路由、分组、用户、API key 五类资源的结构、校验、JSON Schema；资源内容哈希用于判断“有没有变” |
| **写入**：写入流程（对比、排序、写入、崩溃恢复） | 未开始 | 下面“服务自己的 PG 库”已提供它需要的表和事务 |
| **写入**：模拟 ACG（测试用） | 已完成 | 在内存中模仿 ACG：名称唯一、按授权过滤后选路、可人为注入失败，用于测试 |
| **写入**：预览输出（dry_run） | 已完成 | 把将要写入的内容输出为文件，密钥显示为 `***` |
| **写入**：真实 ACG 对接 | 未开始 | 等 ACG 接口定稿 |
| 定期核对 | 未开始 | |
| 配置读取与校验 | 已完成 | 严格校验 YAML（未知字段报错、策略取值检查、只允许通过环境变量名引用密码）；支持 K8s ConfigMap 挂载的目录 |
| 密钥保护 | 已完成 | 密钥在打印、日志、JSON 中一律为 `***`；存储时只存加盐哈希 |
| 服务自己的 PG 库 | 代码完成，未连真实库 | 自动建表；任务锁；binlog 位置（全量开始位置 G0 与增量已处理位置）、待写入队列、写入记录的读写。不保存 New API 数据副本 |
| 任务状态与 `migrator run` | 代码完成，未连真实库 | 阶段：PRECHECK（预检）→ FULL\_SYNC（全量）→ STREAMING（增量）/ POLLING（周期全量），以及 PAUSED、STOPPED；重启后接着执行。预检尚未实现，任务停在 `PRECHECK` |
| 工程与检查 | 已完成 | Git 仓库已推送；golangci-lint、gitleaks 密钥扫描、单元测试均通过 |

## 最近一次检查结果

| 检查 | 结果 |
| --- | --- |
| `go test -race -count=1 ./...` | 全部通过 |
| `go vet ./...`（含 `-tags=integration`） | 通过 |
| `golangci-lint run`（含 `--build-tags=integration`） | 0 issues |
| `make leak-scan`（gitleaks + 测试产物扫描） | 无密钥 |
| JSON Schema 校验（`uv run ... test/schema/check.py`） | 通过 |
| `migrator validate --config-dir configs` | 通过 |
| 需要真实 PG 的测试 | **只编译，未运行** |

本阶段按约定不下载、不启动数据库，所以下面这些都还没有在真实数据库上验证过：建表语句、任务锁的互斥、事务回滚与提交、重启后恢复任务状态、`migrator run` 的完整启动。有测试库后运行：

```sh
MIGRATOR_TEST_PG_DSN='postgres://...' go test -tags=integration ./internal/state/pg/... ./cmd/migrator/...
```

## 纠偏记录（2026-11-04）

按 design v3.3 删除了按旧设计实现的“在 PG 保存 New API 数据副本”的代码：

- 建表脚本去掉 `mirror_rows`、`deps`、`generations` 三张表和 `jobs.active_generation`；`checkpoints` 改为每个任务一行（`g0`、`gtid_set`）。尚无任何部署，直接修改了 001 脚本。测试会检查这三张表不再出现。
- `internal/state` 删除 `RowChange`、`MirrorRow`、`ApplyMirror`、`ReadMirror`；位置接口改为 `SetFullSyncStart(g0)`、`Advance(pos)`、`Position()`。`Advance` 在没有记录 G0 时报错，防止未全量就开始增量。
- 任务状态 SNAPSHOTTING、CATCHING\_UP 合并为 FULL\_SYNC。
- 相关单元测试与集成测试已改写；全部检查通过（集成测试仍只编译）。

## 已知问题与限制

- **旧进程迟到写入**：一个进程失去任务锁后，最长约 2 秒（检测间隔 + 超时）才发现并停止，这期间它仍可能写服务自己的 PG 库。可改进为：每次写入时在 SQL 里确认本进程仍持有锁。尚未实现。
- `migrator run` 一次只运行一个任务（`--job` 指定，或配置中只有一个任务）。
- 任务的配置内容只在首次登记时写入 PG，之后修改配置不会更新这份记录（配置热加载属于定期核对阶段）。
- 预览输出的文件只用于查看，不能用来恢复密钥或恢复模拟 ACG 的状态。
- 密钥扫描对 `new-api/` 上游源码目录做了排除：该目录不入库，其中 10 处命中都是上游自己的测试样例。

## 下一步

1. 编写并实现“读取：从 MySQL 全量读出与按条件查询 New API 数据”。
2. 编写“转换：规则引擎与 New API 映射规则”的计划。
3. 有测试库时运行全部 `integration` 测试。

## 开发环境

Go `go1.26.10 darwin/arm64`。依赖从本机模块缓存离线获取，不关闭校验和检查：

```sh
export GOPATH=/private/tmp/newapi2acg-gopath
export GOMODCACHE=/private/tmp/newapi2acg-mod-cache
export GOCACHE=/private/tmp/newapi2acg-go-cache
export GOPROXY=file:///Users/liam/go/pkg/mod/cache/download
```

工具：gitleaks 8.30.1、golangci-lint 2.15.0（Homebrew）。JSON Schema 校验通过 `uv` 临时环境运行，依赖版本见 `test/schema/requirements.txt`。

New API 上游源码放在 `new-api/`，只读参考，对应 commit `bde9b2f44887d34ec54799ae191d50f97914359e`。

详细的逐批开发记录见 Git 历史。
