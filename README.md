# New API → ACG 迁移服务

把 New API 中的渠道、分组、用户和令牌迁移到公司内部算力网关 ACG，并在切换完成前保持两边一致。

设计见 [design-v3.3.md](design-v3.3.md)，开发计划见 [docs/plans/](docs/plans/)，进度见 [docs/implementation-status.md](docs/implementation-status.md)。

## 它做什么

整个服务就是三步：

1. **读取**：从 New API 的数据库读出渠道、用户、令牌。
2. **转换**：按 `configs/mappings/` 中的规则，把它们转换成 ACG 的提供商、路由、用户、API key。
3. **写入**：调用 ACG 管理接口写进去。已经写过且没变化的不再写。

服务不保存 New API 数据的副本，只保存同步所需的少量状态（binlog 位置、New API 记录与 ACG 资源的对应关系、上次写入的内容、写入记录）。

同步分三种方式，自动衔接：

| 方式 | 何时 | 做什么 |
| --- | --- | --- |
| 全量 | 首次启动 | 读出全部数据，转换后全部写入 ACG |
| 增量 | 全量之后持续运行 | 以 MySQL 从库身份订阅 binlog。某一行变了，找出受影响的 ACG 资源，回源 MySQL 查出它们当前的数据，转换后写入 |
| 定期核对 | 每隔一段时间（默认 6 小时） | 重新全量读取，和 ACG 实际状态比较，补上增量漏掉的或被人手改过的部分 |

## 几个用词

| 用词 | 含义 |
| --- | --- |
| 待写入队列 | 需要重新写入 ACG 的资源列表。全量时全部资源入队，增量时只有受影响的入队。写入时总是回源查当前数据重新计算，所以同一资源重复入队没有副作用 |
| 写入记录 | 每次写 ACG 前先记下“要写什么”，写完记下结果。进程中途崩溃后，按记录判断哪些已经写成功，避免重复写 |
| 任务锁 | 保证同一个迁移任务同一时刻只有一个进程在执行 |
| 密钥哈希 | 服务不保存渠道密钥和令牌原文，只保存它们的加盐哈希值，用来判断密钥有没有变化；写入 ACG 时从源库读取原文，只在内存中使用 |
| 模拟 ACG | 一个在内存里模仿 ACG 行为的测试替身，ACG 接口定稿前用它测试整个流程 |
| 预览输出 | 不写 ACG，只把“将要写入的内容”输出到文件，供人工检查（`dry_run: true`） |

## 当前进度

已完成（详细见 [进度](docs/implementation-status.md)）：

- **配置**：读取并校验 `configs/` 下的 YAML（源库连接、要读的表和字段、迁移策略、目标配置）。
- **ACG 资源定义**：提供商、聚合路由、分组、用户、API key 五类资源的结构和校验规则。
- **密钥保护**：密钥在日志、报告、JSON 输出中一律显示为 `***`，存储时只存哈希。
- **服务骨架**：`migrator run` 能启动、建好自己的 PG 表、拿到任务锁、记录任务状态，重启后从上次状态继续。其中按旧设计保存源数据副本的部分将被删除（见[开发计划](docs/plans/2026-11-04-newapi-to-acg-implementation.md)“纠偏”一节）。
- **测试用的写入目标**：模拟 ACG、预览输出。

尚未完成：从 MySQL 读取、转换规则引擎、写入流程、增量同步、定期核对、真实 ACG 对接。

目前所有涉及真实数据库的测试都只编译、未运行（本阶段不启动数据库）。

## 使用

```sh
# 校验配置
go run ./cmd/migrator validate --config-dir configs

# 运行服务。服务自己的 PG 库地址放在环境变量里，变量名由 configs/targets/*.yaml 的 state_dsn_env 指定
export MIGRATOR_STATE_DSN='postgres://...'
go run ./cmd/migrator run --config-dir configs [--job NAME]
```

`run` 启动后依次：校验配置 → 连接服务自己的 PG 库并建表 → 登记任务 → 获取任务锁（被其他进程占用时每 5 秒重试）→ 按任务状态执行。

目前“预检”这一步还没实现，所以任务会停在 `PRECHECK` 状态，原因写在 `migrator.jobs.reason` 里，每 30 秒重试一次。收到 Ctrl-C / SIGTERM 时正常退出并释放任务锁。日志里只会出现环境变量名，不会出现数据库地址或密码。

## 开发与测试

Go 1.26。离线环境下先设置模块缓存（见 [进度文档的“开发环境”](docs/implementation-status.md#开发环境)）。

```sh
go test -race ./...        # 单元测试
make lint                  # golangci-lint
make leak-scan             # 检查仓库和测试产物中是否有密钥
uv run --no-project --with-requirements test/schema/requirements.txt python test/schema/check.py   # JSON Schema 校验
```

需要真实数据库的测试带 `integration` 标记，有测试库时这样运行：

```sh
MIGRATOR_TEST_PG_DSN='postgres://...' go test -tags=integration ./...
```

测试会自己创建临时数据库并在结束后删除，不会改动 DSN 指向的数据库；测试账号需要建库权限。

## 目录

| 目录 | 内容 |
| --- | --- |
| `cmd/migrator` | 命令行入口：`validate`、`run` |
| `internal/config` | 配置读取与校验 |
| `internal/model` | ACG 五类资源的定义 |
| `internal/secrets` | 密钥保护与哈希 |
| `internal/service` | 任务状态流转（预检 → 全量 → 增量 …） |
| `internal/state/pg` | 服务自己的 PG 库：建表、任务锁、binlog 位置、待写入队列、写入记录 |
| `internal/target` | 写入目标：`memory`（模拟 ACG）、`file`（预览输出） |
| `configs/` | 配置样例 |
| `docs/compatibility` | New API 表结构核对结果 |
| `new-api/` | New API 上游源码，只读参考，不入库 |
