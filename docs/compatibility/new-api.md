# New API 兼容性登记

依据：design-v3.2.md、实施计划 M0 和用户指定的本地参考仓库。以下为源码核验结果，不等于运行时验证或生产兼容性承诺。

## 已固定的源码基准

| 信息 | 结果 |
| --- | --- |
| 仓库 | `git@github.com:QuantumNous/new-api.git` |
| 本地目录 | `new-api/`（用户已拉取） |
| tag | `v1.0.0-rc.21`，由 `git describe --tags --exact-match` 核验 |
| commit | `bde9b2f44887d34ec54799ae191d50f97914359e` |
| 工作区 | detached HEAD，无已修改或未跟踪文件；只读参考 |
| 实际部署/数据库 | 无，用户明确按该 tag 建立源码契约 |
| 先前版本标识 | `v1.0.0-rc.21-3.1.14` 不作为已验证部署版本 |
| e2e 镜像 | 尚未构建/选择，不能用 latest 替代固定 tag |
| 数据库 DDL | 尚未运行迁移生成；已核对 Go 模型与 GORM 声明 |

原先的 clone 阻塞已由用户手动拉取解决。`references/new-api` 是旧计划路径，不再使用。参考仓库排除在迁移工具主仓库追踪之外，不修改其源码。

## 字段覆盖与契约产物

[源码字段清单](new-api-schema.md) 记录每个持久化字段的 Go 类型、GORM tag 和采集/dropped 理由；[契约草案](../../configs/sources/new-api.yaml) 使用精确版本范围，尚未通过未实现的配置加载器或实际数据库验证。

| 数据集 | 持久化字段 | 契约采集 | dropped | 排除的非持久化字段 |
| --- | ---: | ---: | ---: | --- |
| channels | 30 | 12 | 18 | Keys |
| users | 29 | 8 | 21 | OriginalPassword、VerificationCode、AdminPermissions |
| tokens | 17 | 14 | 3 | 无 |

字段来源：`new-api/model/channel.go:23`、`user.go:23`、`token.go:14`；迁移入口为 `new-api/model/main.go:271` 的 AutoMigrate。表名及类型最终仍需在读取预检中确认；模型声明不能替代现有数据库 introspection。

采集余额相关字段不等于迁移额度：`tokens.remain_quota/unlimited_quota` 是判断鉴权可用性的输入，只应映射有效耗尽状态。不能把原始余额写入 ACG。

## 状态及角色枚举

来自 `new-api/common/constants.go:202`、`:249`、`:254`、`:267`，对应原始查找表已放入 `configs/lookups/newapi-*.yaml`。

| 项目 | 原始值与含义 |
| --- | --- |
| 用户角色 | 0 guest；1 common；10 admin；100 root |
| 用户状态 | 1 enabled；2 disabled |
| 令牌状态 | 1 enabled；2 disabled；3 expired；4 exhausted |
| 渠道状态 | 0 unknown；1 enabled；2 manually disabled；3 auto disabled |

guest 和 unknown 不是不存在的枚举，但也不能默认映射成普通用户或启用渠道。查找表只是源值归一；目标角色映射与支持策略待配置确认。渠道类型枚举在 `new-api/constant/channel.go`，不能将其全部视为 ACG 已支持类型。

## 五个授权问题的源码结论

1. **模型限制开启、列表为空**：普通自动选路请求无模型权限。`model/token.go:343` 生成空集合，`middleware/auth.go:459` 将限制写入上下文，`middleware/distributor.go:59` 查表失败后返回 403。管理员指定渠道分支在该检查前分流，不能宣称所有路径行为相同；迁移设计默认不开放直连。
2. **禁用用户**：`middleware/auth.go:412` 要求用户状态为 enabled，否则返回 403。此路径依赖用户缓存；缓存传播延迟未实测。
3. **令牌过期/耗尽**：`model/token.go:195` 首先要求 status=1，然后检查 `expired_time != -1 && expired_time < now`，以及 `!unlimited_quota && remain_quota <= 0`，任一失败都会拒绝；`middleware/auth.go` 将非数据库错误转为 401。Redis 开启时不一定把 status 写回为 expired/exhausted，因此 status=1 不代表当前可用。过期判断使用严格小于，不是小于等于。
4. **有效分组**：`middleware/auth.go:421` 先取用户分组，令牌非空时校验用户可用分组及 GroupRatio 中的分组存在性，再覆盖；没有“两个都为空就使用 default”的运行时补值。用户模型的创建默认值 default 与旧数据显式空串是不同情况。`auto` 进入另一套动态选组流程。
5. **候选与权重**：`model/ability.go:90` 的数据库路径先按 group/model/enabled 取优先级，再选渠道；重试可下降优先级。`:132` 的随机票数使用 weight+10，且循环判断边界为 `<=0`，不能直接等同于 ACG 原权重抽样。`model/channel_cache.go:114` 的内存路径使用原权重、全零时等权，还存在模型名归一匹配；运行模式需要显式记录。两条路径还对高级自定义渠道检查请求路径。

以上五项均为静态调用链核验，尚无请求/响应实测记录。M0 Task 0.3 运行验收仍未完成。

## 对 v3.2 的实施约束与差异

### 分组授权依赖 options，不能直接忽略

`service/group.go:10` 计算用户可用分组，受全局 UserUsableGroups 和 GroupSpecialUsableGroup 影响；`middleware/auth.go:424` 校验令牌指定分组，`:429` 还检查 GroupRatio 中该分组是否存在。`model/option.go` 加载这些配置。

因此“系统设置不迁移”不能解释成“授权计算不读取设置”。后续需增加只读授权配置输入及其变化依赖，或者对不能确认源授权的令牌预检失败；不能仅凭 token.group 为其新增权限。当前三表契约草案尚未覆盖这些配置，不能作为生产全量授权契约启用。

### auto 分组不能当作普通字面分组

`service/channel_select.go:84`、`service/group.go:45` 结合 AutoGroups、用户可用分组及 cross_group_retry 动态选择分组。现有每个 key 固定两个授权集合不足以复刻顺序与重试行为；首期应识别并报告为未支持，在明确策略前拒绝该类令牌迁移，不能静默生成空授权或全部分组合并授权。

### 模型匹配不总是字面相等

`middleware/distributor.go:72` 调用 `setting/ratio_setting/model_ratio.go:719`，支持特定 gizmo/thinking 名称归一。内存渠道选择也存在精确匹配后的归一回退。M2 需明确此类模型是否支持；当前 `r.model in limits` 与每个裸名一条路由不能声称覆盖全部别名行为。

### 多密钥必须读取 channel_info

`model/channel.go:175` 支持 JSON 数组与换行拆分，但 `:199` 在非多密钥模式直接返回原始 key；多密钥模式只使用状态为 enabled 的条目，状态缺失默认 enabled。单看渠道类型拆分会改变语义，也可能重新启用被禁用的密钥。channel_info 应提取必要字段，禁用原因、轮询游标等不应整体进入报告或内容哈希。

### 软删除、空串与额度边界

- tokens 自身也有 deleted_at；示例 when 只判断用户软删除不充分。失去产出的资源应按 on_source_delete 停用/撤权。
- 用户/令牌分组都为空时不能凭空补 default 并授予权限，建议视为源数据问题。
- 仅依据状态枚举判断 exhausted 不充分。应在归一化层计算实际耗尽标记，CDC 关注标记变化而非每次扣减，避免高频扇出。
- expiry 会随时间到达，即使没有新 binlog 事件也要由目标到期规则生效；时钟及边界行为要纳入目标契约。
- quota/计费和订阅不迁移意味着计费拒绝行为不保证等价，报告需明确范围。

### 高级设置与候选集合

`constant.ChannelTypeAdvancedCustom=58` 的选路依赖 settings 内的路由与请求路径，见 `model/ability.go:158` 和 `model/channel_cache.go:208`。在未适配前不能仅 dropped settings 后仍宣称候选集合一致。其他依赖 other/openai_organization 的渠道同样需先核定支持能力。

## 下一步门禁

- Task 0.1 源码基准已固定；没有部署镜像，digest 暂不适用。
- Task 0.2 已完成模型字段分类和状态枚举；数据库方言生成的真实 DDL、nullable/default 仍待集成验证。
- Task 0.3 已有上述源码证据；HTTP/DB/缓存运行验证未完成。
- Task 0.4 独立验收样本及实际候选集合待构建，不能用映射引擎输出反写预期。
- 上述差异需要体现在 M2 细化计划中；源/目标能力缺失按失败处理，避免默认扩大权限。
