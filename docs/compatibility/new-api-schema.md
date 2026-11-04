# New API 源码字段清单

源码 tag：`v1.0.0-rc.21`；commit：`bde9b2f44887d34ec54799ae191d50f97914359e`。

该清单来自 GORM 模型声明，非实际数据库 introspection。SQL 类型、可空性、历史迁移效果仍需数据库探测；非指针 Go 字段也不能据此推断数据库 NOT NULL。`required` 表示契约要求列存在，`nullable` 表示读取器必须处理 SQL NULL。

完整 Go 类型和 GORM tag 如下；未声明的物理类型/默认值不猜测。迁移入口见 `new-api/model/main.go:271`。

## channels

来源：`new-api/model/channel.go` 的 `Channel`；持久化字段 30 个。

| 数据库列 | Go 字段 | Go 类型 | GORM 声明 | 处理 |
| --- | --- | --- | --- | --- |
| `id` | `Id` | `int` | `未显式声明` | 采集（见用途说明） |
| `type` | `Type` | `int` | `default:0` | 采集（见用途说明） |
| `key` | `Key` | `string` | `not null` | 采集（见用途说明） |
| `openai_organization` | `OpenAIOrganization` | `*string` | `未显式声明` | dropped：供应商请求组织标识；目标支持未确认，非空时必须报告行为损失 |
| `test_model` | `TestModel` | `*string` | `未显式声明` | dropped：源渠道测试设置，不迁移 |
| `status` | `Status` | `int` | `default:1` | 采集（见用途说明） |
| `name` | `Name` | `string` | `index` | 采集（见用途说明） |
| `weight` | `Weight` | `*uint` | `default:0` | 采集（见用途说明） |
| `created_time` | `CreatedTime` | `int64` | `bigint` | dropped：源创建时间不迁移 |
| `test_time` | `TestTime` | `int64` | `bigint` | dropped：健康检查运行数据不迁移 |
| `response_time` | `ResponseTime` | `int` | `未显式声明` | dropped：运行指标不迁移 |
| `base_url` | `BaseURL` | `*string` | `column:base_url;default:''` | 采集（见用途说明） |
| `other` | `Other` | `string` | `未显式声明` | dropped：渠道类型专属参数；目标支持未确认，非空时阻止声明等价 |
| `balance` | `Balance` | `float64` | `未显式声明` | dropped：余额不迁移 |
| `balance_updated_time` | `BalanceUpdatedTime` | `int64` | `bigint` | dropped：余额刷新时间不迁移 |
| `models` | `Models` | `string` | `未显式声明` | 采集（见用途说明） |
| `group` | `Group` | `string` | `type:varchar(64);default:'default'` | 采集（见用途说明） |
| `used_quota` | `UsedQuota` | `int64` | `bigint;default:0` | dropped：累计用量不迁移 |
| `model_mapping` | `ModelMapping` | `*string` | `type:text` | 采集（见用途说明） |
| `status_code_mapping` | `StatusCodeMapping` | `*string` | `type:varchar(1024);default:''` | dropped：状态码覆盖未适配，报告非空值 |
| `priority` | `Priority` | `*int64` | `bigint;default:0` | 采集（见用途说明） |
| `auto_ban` | `AutoBan` | `*int` | `default:1` | dropped：不复刻自动禁用策略，当前状态仍迁移 |
| `other_info` | `OtherInfo` | `string` | `未显式声明` | dropped：源运行附加信息不迁移；禁止原样日志输出 |
| `tag` | `Tag` | `*string` | `index` | dropped：源管理标签不迁移 |
| `setting` | `Setting` | `*string` | `type:text` | dropped：渠道高级设置未适配，报告非空值 |
| `param_override` | `ParamOverride` | `*string` | `type:text` | dropped：参数覆盖未适配，报告非空值 |
| `header_override` | `HeaderOverride` | `*string` | `type:text` | dropped：请求头覆盖未适配；报告仅字段存在与损失，不输出可能含凭据的原文 |
| `remark` | `Remark` | `*string` | `type:varchar(255)` | dropped：渠道备注暂不迁移 |
| `channel_info` | `ChannelInfo` | `ChannelInfo` | `type:json` | 采集（见用途说明） |
| `settings` | `OtherSettings` | `string` | `column:settings` | dropped：类型专属设置未适配；type=58 等依赖此列的类型应预检拒绝 |

不持久化字段：`Keys`。

## users

来源：`new-api/model/user.go` 的 `User`；持久化字段 29 个。

| 数据库列 | Go 字段 | Go 类型 | GORM 声明 | 处理 |
| --- | --- | --- | --- | --- |
| `id` | `Id` | `int` | `未显式声明` | 采集（见用途说明） |
| `username` | `Username` | `string` | `unique;index` | 采集（见用途说明） |
| `password` | `Password` | `string` | `not null;` | dropped：源密码哈希禁止读取，使用目标首次初始化动作 |
| `display_name` | `DisplayName` | `string` | `index` | 采集（见用途说明） |
| `role` | `Role` | `int` | `type:int;default:1` | 采集（见用途说明） |
| `status` | `Status` | `int` | `type:int;default:1` | 采集（见用途说明） |
| `email` | `Email` | `string` | `index` | 采集（见用途说明） |
| `github_id` | `GitHubId` | `string` | `column:github_id;index` | dropped：第三方绑定不迁移 |
| `discord_id` | `DiscordId` | `string` | `column:discord_id;index` | dropped：第三方绑定不迁移 |
| `oidc_id` | `OidcId` | `string` | `column:oidc_id;index` | dropped：第三方绑定不迁移 |
| `wechat_id` | `WeChatId` | `string` | `column:wechat_id;index` | dropped：第三方绑定不迁移 |
| `telegram_id` | `TelegramId` | `string` | `column:telegram_id;index` | dropped：第三方绑定不迁移 |
| `access_token` | `AccessToken` | `*string` | `type:char(32);column:access_token;uniqueIndex` | dropped：管理令牌禁止读取 |
| `quota` | `Quota` | `int` | `type:int;default:0` | dropped：用户余额不迁移，计费拒绝行为不保证等价 |
| `used_quota` | `UsedQuota` | `int` | `type:int;default:0;column:used_quota` | dropped：累计用量不迁移 |
| `request_count` | `RequestCount` | `int` | `type:int;default:0;` | dropped：请求计数不迁移 |
| `group` | `Group` | `string` | `type:varchar(64);default:'default'` | 采集（见用途说明） |
| `aff_code` | `AffCode` | `string` | `type:varchar(32);column:aff_code;uniqueIndex` | dropped：邀请体系不迁移 |
| `aff_count` | `AffCount` | `int` | `type:int;default:0;column:aff_count` | dropped：邀请体系不迁移 |
| `aff_quota` | `AffQuota` | `int` | `type:int;default:0;column:aff_quota` | dropped：邀请体系不迁移 |
| `aff_history` | `AffHistoryQuota` | `int` | `type:int;default:0;column:aff_history` | dropped：邀请体系不迁移 |
| `inviter_id` | `InviterId` | `int` | `type:int;column:inviter_id;index` | dropped：邀请体系不迁移 |
| `deleted_at` | `DeletedAt` | `gorm.DeletedAt` | `index` | 采集（见用途说明） |
| `linux_do_id` | `LinuxDOId` | `string` | `column:linux_do_id;index` | dropped：第三方绑定不迁移 |
| `setting` | `Setting` | `string` | `type:text;column:setting` | dropped：用户偏好及计费设置不迁移 |
| `remark` | `Remark` | `string` | `type:varchar(255)` | dropped：用户备注暂不迁移 |
| `stripe_customer` | `StripeCustomer` | `string` | `type:varchar(64);column:stripe_customer;index` | dropped：支付客户绑定不迁移 |
| `created_at` | `CreatedAt` | `int64` | `autoCreateTime;column:created_at` | dropped：源创建时间不迁移 |
| `last_login_at` | `LastLoginAt` | `int64` | `default:0;column:last_login_at` | dropped：登录运行数据不迁移 |

不持久化字段：`OriginalPassword`, `VerificationCode`, `AdminPermissions`。

## tokens

来源：`new-api/model/token.go` 的 `Token`；持久化字段 17 个。

| 数据库列 | Go 字段 | Go 类型 | GORM 声明 | 处理 |
| --- | --- | --- | --- | --- |
| `id` | `Id` | `int` | `未显式声明` | 采集（见用途说明） |
| `user_id` | `UserId` | `int` | `index` | 采集（见用途说明） |
| `key` | `Key` | `string` | `type:varchar(128);uniqueIndex` | 采集（见用途说明） |
| `status` | `Status` | `int` | `default:1` | 采集（见用途说明） |
| `name` | `Name` | `string` | `index` | 采集（见用途说明） |
| `created_time` | `CreatedTime` | `int64` | `bigint` | dropped：源创建时间不迁移 |
| `accessed_time` | `AccessedTime` | `int64` | `bigint` | dropped：访问运行数据不迁移 |
| `expired_time` | `ExpiredTime` | `int64` | `bigint;default:-1` | 采集（见用途说明） |
| `remain_quota` | `RemainQuota` | `int` | `default:0` | 采集（见用途说明） |
| `unlimited_quota` | `UnlimitedQuota` | `bool` | `未显式声明` | 采集（见用途说明） |
| `model_limits_enabled` | `ModelLimitsEnabled` | `bool` | `未显式声明` | 采集（见用途说明） |
| `model_limits` | `ModelLimits` | `string` | `type:text` | 采集（见用途说明） |
| `allow_ips` | `AllowIps` | `*string` | `default:''` | 采集（见用途说明） |
| `used_quota` | `UsedQuota` | `int` | `default:0` | dropped：累计用量不迁移 |
| `group` | `Group` | `string` | `default:''` | 采集（见用途说明） |
| `cross_group_retry` | `CrossGroupRetry` | `bool` | `未显式声明` | 采集（见用途说明） |
| `deleted_at` | `DeletedAt` | `gorm.DeletedAt` | `index` | 采集（见用途说明） |

不持久化字段：无。

## 采集字段的用途与约束

- channels 的基础字段用于 Provider、路由和分组；`channel_info` 用于单 key 状态及拆分模式，不能整体丢弃。轮询索引变化不应触发全量资源重写。
- users 的八个字段用于用户身份、角色、状态、分组和软删除过滤。
- tokens 的 `remain_quota`、`unlimited_quota` 仅用于识别有效耗尽状态，不导入余额；不能仅看 status=4。CDC 后续应比较归一后的耗尽标记，避免每次额度扣减扇出。
- tokens 的 `cross_group_retry` 仅用于识别无法等价迁移的 auto 分组语义；未实现策略前不能静默忽略。
- 两张用户相关表的 deleted_at 都参与删除判断；只过滤 users.deleted_at 会错误迁移已软删除的 token。
- 新增读取字段必须在映射中使用或作为明确预检输入；配置加载器尚未实现，此 YAML 是待验证的契约草案。
- 此清单覆盖模型字段，不覆盖所有 options/abilities 的影响；授权配置依赖和设计差异见 new-api.md。
