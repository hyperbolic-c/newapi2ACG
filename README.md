# ACG 数据迁移工具

把 New API 等外部算力网关的数据迁移到公司内部 ACG 算力网关，并在切换前保持同步。设计方案：[design-core.md](design-core.md)（唯一设计文档）。进度：[docs/implementation-status.md](docs/implementation-status.md)。

## 它做什么

主链路是 **读取 → 转换 → 写入**，由三种机制驱动，三者共用同一条处理流水线：

| 机制 | 做什么 |
| --- | --- |
| 全量 | 在一个一致性快照里按主键分批读出全部数据，转换后写入 ACG，记下快照对应的 binlog 位置 |
| 增量 | 以 MySQL 从库身份订阅 binlog，每个源事务作为一批处理，ACG 写入成功后才保存检查点 |
| 周期对账 | 定期暂停增量、重新建快照，与 ACG 现状比较并修复差异，再从新快照的位置继续增量 |

服务不保存源数据副本。PostgreSQL 中只有三张表：任务状态与执行租约、检查点、源→目标资源对应关系（design-core §8）。

## 当前状态

框架与同步机制已实现；**业务字段映射和 ACG 写入接口尚未实现**（等待 ACG 数据模型与管理 API，design-core §1.1、§11 阶段四）。现阶段：

- 映射使用 `passthrough`：每行源数据原样变成一个同名目标资源，用于验证整条链路。
- 写入目标只有 `mock`（内存模拟，可注入故障）。`acg-api` 会明确报“尚未实现”。

## 使用

```sh
export NEWAPI_MYSQL_DSN='migrator:***@10.0.0.5:3306/newapi'   # go-mysql 格式：用户:密码@主机:端口/库名
export ACG_PG_DSN='postgres://migrator:***@pg:5432/acg?sslmode=disable'
export MIGRATOR_HASH_KEY='...'                                  # 资源对应表中 HMAC 用的密钥，重启后必须不变

go run ./cmd/migrator validate --config configs/jobs/newapi-to-acg.yaml
go run ./cmd/migrator run      --config configs/jobs/newapi-to-acg.yaml [--metrics-addr :9090]
go run ./cmd/migrator status   --config configs/jobs/newapi-to-acg.yaml
go run ./cmd/migrator pause    --config ...                 # 暂停
go run ./cmd/migrator resume   --config ...                 # 从 paused / failed 恢复
go run ./cmd/migrator recover  --config ... --resnapshot    # 恢复并丢弃检查点，下次启动重新全量
```

`--metrics-addr` 开启 `/healthz` 和 `/debug/vars`（同步阶段、已应用位置、批次数、重试次数、对账修复数等）。

## 源端 MySQL 要求

全量读取、binlog 订阅、周期对账都连接配置中的同一个 MySQL 实例（优先只读从库）。启动时会逐项检查，不满足即停在 `initializing` 并给出原因：

- `log_bin=ON`、`binlog_format=ROW`、`binlog_row_image=FULL`、`binlog_row_metadata=FULL`、`binlog_row_value_options` 为空
- 连接的是从库时 `log_replica_updates=ON`
- 账号权限：SELECT、RELOAD（快照时短暂加全局读锁以对齐 binlog 位置）、REPLICATION SLAVE、REPLICATION CLIENT
- `server_id` 与复制拓扑中其他实例不冲突；同步的表是 InnoDB，且不含 ENUM / SET / BIT 列（快照与 binlog 对这些类型的编码不同，暂不支持）

## 开发

Go 1.26，MySQL 访问统一使用 go-mysql（`client` 读快照、`replication` 订阅 binlog）。

```sh
make test              # 单元测试（含同步机制的端到端模拟：故障、断线、位点丢失、对账、租约）
make lint              # vet + golangci-lint（含 integration 标记）
make leak-scan         # gitleaks
make test-integration  # 需要 MIGRATOR_TEST_PG_DSN、MIGRATOR_TEST_MYSQL_DSN
```

目录与 design-core §9 一致：`internal/{event,config,source/mysql,mapping,target,state,sync,observability,app}`、`migrations/postgres`、`configs/jobs`、`tests/integration`。`new-api/` 是上游源码的只读参考，不入库。
