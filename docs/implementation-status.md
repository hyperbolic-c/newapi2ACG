# 开发进度

依据 [design-core.md](../design-core.md) 第十一节的四个阶段。更新：2026-11-05。

| 阶段 | 状态 | 说明 |
| --- | --- | --- |
| 一：项目骨架 + 分批全量读取 | 代码完成 | 配置加载与校验；统一事件（`internal/event`）；MySQL 快照：短暂全局读锁对齐 binlog 位置、同一事务内按主键分批读取；Mock 目标 |
| 二：Binlog 订阅 + PG 检查点 | 代码完成 | go-mysql `replication` 订阅，按事务组装批次（含压缩事务）；DDL 触及同步表即停止；位点丢失转重新全量；PG 三张状态表、租约与写入隔离、检查点与资源对应表同事务提交 |
| 三：周期对账 + 任务调度 | 代码完成 | 状态机（initializing / snapshotting / catching_up / streaming / reconciling / paused / failed）；对账暂停增量、新快照 P1、与目标比较修复、删除检测（report / delete）、从 P1 继续 |
| 四：正式对接 ACG | 未开始 | 等待 ACG 数据模型与管理 API；届时实现 `target/acg` 与 New API → ACG 映射规则，替换 `mock` 和 `passthrough` |

## 验证

| 检查 | 结果 |
| --- | --- |
| `go test -race ./...` | 通过。同步机制在内存模拟中覆盖：全量期间并发写入、多行事务、目标暂时失败、响应丢失后用 Inspect 确认、状态提交失败后重放不重复、永久错误停止并可人工恢复、断线重连、位点丢失重新全量、DDL 停止、暂停/恢复、对账修复人工改动与删除、对账不被旧数据覆盖、第二实例等待租约、租约丢失停止、密钥不进日志 |
| `golangci-lint`（两种标记）、`go vet` | 0 issues |
| 真实 MySQL / PG 集成测试（`tests/integration`） | **只编译，未运行**（本阶段不启动数据库）。有测试库时运行 `make test-integration` |

## 已知限制

- 快照对齐 binlog 位置使用 `FLUSH TABLES WITH READ LOCK`，需要 RELOAD 权限；拿不到该权限的云数据库暂不支持。
- 单列主键分页；复合主键、非索引主键尚未支持（design-core §4.3）。
- ENUM / SET / BIT 列被预检拒绝。
- DDL 检测是保守的：语句中出现同步表名即停止，需人工确认后 `recover`（必要时 `--resnapshot`）。
- 删除检测只考虑本任务创建的资源；`delete_policy` 默认 `report`。

## 下一步

1. 有测试库时运行集成测试，修正真实环境中的差异。
2. ACG 数据模型与管理 API 确定后，实现 `target/acg`（Apply / Inspect）和 New API → ACG 映射规则。
