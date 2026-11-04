# AI 算力网关数据迁移工具——修订版架构设计（v2）

Oct 28, 2026 · @Liam

## 修订说明

v2 将首期范围收敛为 New API → 内部算力网关（下称 ACG），并补齐 v1 评审中发现的缺口。LiteLLM、Higress 保留扩展点，后续接入。用户账号迁移，密码统一重置；历史日志和用量数据不迁移。

| 主题 | v1 | v2 |
| --- | --- | --- |
| 首期源项目 | New API、LiteLLM、Higress 分阶段 | 仅 New API，其余后续 |
| 数据范围 | 以上游渠道为主 | 增加分组、用户、令牌、额度；日志与用量不迁移 |
| 用户密码 | 未涉及 | 统一重置为初始密码 User@123，强制首次登录修改 |
| 读取层 | 手写 SQL | 逻辑数据集契约，按方言生成 SQL，源版本探测，一致性快照 |
| 映射 DSL | 字段、拆分、引用 | 增加 join、when、group\_by、保真度声明、漏映射告警 |
| 模型映射 | 每个渠道每个模型一个部署 | 逻辑模型与部署分离，保留 model\_mapping、group、优先级与权重 |
| 密钥 | 脱敏与恢复并存，未定方案 | 密钥不落盘，断点重读源端；以 HMAC 指纹比对 |
| 执行引擎 | 预览、幂等、断点续跑 | 增加延迟绑定、失败传播、计划指纹、三方对比、prune、回滚 |
| 目标网关 | 未列要求 | 列出前置能力清单，阶段一逐项确认 |

## 一、设计目标与范围

本期目标：把一个或多个 New API 实例的渠道配置、上游密钥、分组、用户、令牌和额度，以可预览、可重跑、可回滚的方式迁移到 ACG。

### 1.1 设计目标

- 框架化：读取、映射、导入、执行四层保持通用，New API 的差异只体现在配置（sources、mappings、lookups）和少量扩展函数中。
- 多数据源：New API 支持 MySQL、PostgreSQL、SQLite 直连读取，以及管理 API 读取。
- 可靠：默认只预览，冲突默认失败，资源级幂等，失败可续跑，按任务回滚。
- 可扩展：后续接入 LiteLLM、Higress 时优先只新增配置；缺少能力时补充通用能力，不在引擎中加项目分支。

非目标：历史日志、用量统计、充值与兑换码记录；双向迁移；复刻源网关的运行逻辑（例如渠道健康检查）。

### 1.2 范围矩阵

处理方式分为：原值迁移、转换迁移、近似迁移（语义有损，需在报告中标出）、重置、不迁移。字段名以实际部署的 New API 版本为准，阶段一核对。

| 数据 | New API 来源 | 处理方式 | ACG 资源 | 说明 |
| --- | --- | --- | --- | --- |
| 渠道基本信息 | channels：name、type、base\_url、status | 转换迁移 | Provider | base\_url 为空时按渠道类型取默认地址 |
| 上游密钥 | channels.key | 原值迁移 | ProviderCredential | 支持多密钥拆分；部分类型的 key 是 JSON 或组合格式 |
| 模型列表 | channels.models | 转换迁移 | Model、ModelDeployment | 同名模型跨渠道聚合为一个逻辑模型 |
| 模型重定向 | channels.model\_mapping | 转换迁移 | ModelDeployment.upstream\_model | 不迁移会转发到错误的上游模型 |
| 渠道分组 | channels.group | 转换迁移 | Group 关联 | 决定访问权限，必须迁移 |
| 优先级与权重 | channels.priority、weight | 近似迁移 | RoutingPolicy | 保真度取决于 ACG 路由能力 |
| 渠道高级设置 | param\_override、header\_override、status\_code\_mapping、setting 等 | 逐项评估 | 待定 | ACG 不支持的字段显式声明 dropped |
| 用户账号 | users：username、display\_name、email、role、status | 转换迁移 | User | 软删除用户不迁移 |
| 用户密码 | users.password | 重置 | User | 统一重置为 User@123，强制首次登录修改，见第六节 |
| 用户分组 | users.group | 转换迁移 | Group 关联 | 与渠道分组共用 Group 资源 |
| 用户额度 | users.quota、used\_quota | 转换迁移 | QuotaPolicy 或余额 | 需单位换算与总量对账 |
| 令牌 | tokens：key、额度、过期时间、模型限制、IP 限制、分组 | 原值迁移 | ConsumerCredential | 前提是 ACG 支持指定 key 原值写入 |
| 第三方登录绑定 | users：github\_id、oidc\_id 等 | 待确认 | — | 见第十节待确认事项 |
| 用户系统访问令牌 | users.access\_token | 不迁移 | — | 管理用途，迁移后由用户重新生成 |
| 分组倍率、模型倍率与价格 | options 表 | 待确认 | PricingPolicy | 需业务确认计费口径 |
| 历史日志、用量统计 | logs 等 | 不迁移 | — | 本期范围外 |
| 兑换码、充值记录 | redemptions、top\_ups 等 | 不迁移 | — | 本期范围外 |
| 其余系统设置 | options 表 | 不迁移 | — | 在 ACG 中单独配置 |

## 二、总体架构

架构沿用 v1 的主链路，核心原则不变：外部项目的原始结构不进入引擎，引擎只处理标准记录和 acg/v1 资源。

&#91;embedded content: 总体架构 · 源端到 ACG 的 5 层通用链路，4 类配置作用点\]

实线是数据流向，虚线是配置作用点。新增源项目时，优先只新增右侧配置；读取驱动、映射引擎、执行引擎和导入器都不随源项目增加。

| 层 | 职责 | 扩展方式 |
| --- | --- | --- |
| 通用读取层 | 连接数据库或 API，按逻辑数据集契约输出标准记录 | 新驱动写代码；新项目只加契约配置 |
| 映射引擎 | 执行 YAML 规则，生成 acg/v1 资源和字段溯源 | YAML；特殊转换用 Transformer 扩展 |
| 标准资源层 | acg/v1 资源类型与 JSON Schema | 随 ACG 导入契约升级版本 |
| 执行引擎 | 规划、执行、增量收敛、回滚、状态管理 | 框架通用 |
| 统一导入器 | 写入 ACG，始终只有一套 | 随 ACG 接口变化 |

## 三、数据读取层

映射规则只依赖逻辑数据集契约，不依赖具体 SQL 或 API 字段。各读取方式负责把原始数据归一到契约，同一份映射因此可以复用于 MySQL、PostgreSQL、SQLite 和管理 API。

### 3.1 项目级契约与实例级连接分离

项目级配置描述 New API 有哪些数据集、每个数据集的逻辑字段，以及各读取方式如何绑定到这些字段。实例级配置只描述连接哪个具体实例。多个 New API 实例共用一份项目级配置。

```yaml
# configs/sources/new-api.yaml —— 项目级：逻辑数据集契约（示例，字段以实际版本核定）
version: 2
project: new-api

compat:
  supported: ">=0.6.0"              # 首期支持的版本区间，阶段一核定
  probe:
    api: {path: /api/status, version_path: data.version}
    database:                        # 直连时以表结构指纹判定
      require_columns:
        channels: [id, type, key, base_url, models, group, model_mapping, priority, weight, status]
        users: [id, username, role, status, quota, used_quota, group]
        tokens: [id, user_id, key, status, expired_time, remain_quota, unlimited_quota]

datasets:
  channels:
    primary_key: id
    schema:
      id:            {type: int, required: true}
      type:          {type: int, required: true}
      name:          {type: string}
      key:           {type: secret}
      base_url:      {type: string}
      models:        {type: string}
      group:         {type: string, default: "default"}
      model_mapping: {type: json}
      priority:      {type: int, default: 0}
      weight:        {type: int, default: 0}
      status:        {type: int, required: true}
    dropped: [balance, used_quota, response_time, test_time]   # 显式声明不迁移的列
    bindings:
      database: {table: channels}    # 框架按方言生成显式列名 SQL，自动处理 group、key 等保留字
      api:
        path: /api/channel/
        records_path: data.items     # 以实际接口为准
        pagination: {type: page, page_param: p, size_param: page_size, size: 100}
        unavailable: [key]           # 管理 API 通常不返回完整密钥
```

```yaml
# configs/instances/newapi-prod.yaml —— 实例级：连接方式
instance: newapi-prod
project: new-api
access:
  type: database
  driver: postgres                   # mysql | postgres | sqlite
  dsn_env: NEWAPI_PROD_DSN
```

通过管理 API 读取时，渠道密钥通常拿不到。框架把 unavailable 字段标记为缺失，由 missing\_secret 策略决定阻止或跳过。因此渠道密钥迁移以数据库直连为准，API 模式适用于无数据库权限、只迁移非密钥数据的场景。

### 3.2 方言与类型归一

- 生成 SQL：按契约生成显式列名查询，不使用 SELECT \*；按方言引用保留字（MySQL 用反引号，PostgreSQL、SQLite 用双引号）。
- 类型归一：tinyint 与 boolean、以文本存储的 JSON、Unix 秒级时间戳统一转换为契约类型；转换失败即报错。
- 分批读取：按主键游标分页（WHERE id > ? ORDER BY id LIMIT ?），不使用 OFFSET。
- 只读保护：连接层开启只读事务，并建议使用只读账号；read\_only 不再是一个可关闭的配置项。

### 3.3 一致性快照

所有数据集在同一个只读事务中读取，避免渠道、用户、令牌之间出现时间差。PostgreSQL 使用 REPEATABLE READ，MySQL 使用 START TRANSACTION WITH CONSISTENT SNAPSHOT，SQLite 使用单个读事务。API 模式无法保证一致性，报告中会标注。

### 3.4 版本与漂移检测

采集前先执行探测。版本不在 supported 区间内，或契约要求的列缺失，则阻止迁移。源表中出现契约未声明、也未列入 dropped 的列时给出告警，因为这往往意味着新版本增加了需要迁移的字段。

## 四、映射 DSL 规范

DSL 只做声明，所有表达式统一使用 CEL；首期能力以 New API 所需为准，不为 LiteLLM、Higress 预先增加语法。

### 4.1 规则结构

| 关键字 | 作用 | 是否必填 |
| --- | --- | --- |
| name | 规则名，用于报告和字段溯源 | 是 |
| dataset | 主数据集 | 是 |
| join | 关联其他数据集，左连接或内连接 | 否 |
| when | 条件为假时不生成资源 | 否 |
| for\_each | 将一个字段展开为多行，支持嵌套 | 否 |
| group\_by | 跨记录聚合，生成一个资源 | 否 |
| emit.kind | 目标资源类型 | 是 |
| emit.identity | 资源局部标识 | 是 |
| emit.fields | 字段映射 | 是 |
| fidelity | 规则或字段的语义保真度 | 否，默认 exact |

### 4.2 表达式、插值与空值

- 统一 CEL：`expr` 直接写 CEL；identity 和字符串中的 `${...}` 是 CEL 的语法糖，编译期转换为 CEL 字符串拼接，两者错误处理一致。
- 空值区分：字段缺失、值为 null、值为空字符串是三种情况，分别用 `has(row.x)`、`row.x == null`、`row.x == ""` 判断；内置 `blank(x)` 同时判断后两种。
- 类型严格：类型不匹配、枚举未定义、引用无法解析默认阻止迁移，不做隐式转换。
- 编译期检查：配置加载时编译全部表达式，引用了契约中不存在的字段直接报错，而不是运行时得到 null。

### 4.3 资源标识

- 作者只写局部标识，例如 `channel:${row.id}`；框架自动加上 `<instance>/<kind>/` 前缀，作者无需也不能省略来源实例。
- 标识必须由稳定的源字段构成，不能使用数组下标、时间戳或随机值。
- 跨渠道共享的资源（逻辑模型、分组）用业务名称作标识，例如 `model:${model}`、`group:${g}`，这样多个渠道会自然收敛到同一个资源。
- 密钥类资源用 `fingerprint(secret)` 构成标识，即带盐 HMAC-SHA256，密钥顺序变化不会导致资源重建。

### 4.4 join、when、group\_by 示例

```yaml
# 令牌关联所属用户；用户已删除的令牌不迁移
- name: token-to-credential
  dataset: tokens
  join:
    users: {on: "tokens.user_id == users.id", type: inner}
  when: "users.deleted_at == null"
  emit:
    kind: ConsumerCredential
    identity: "token:${row.id}"
    fields:
      user_ref: {ref: "User/user:${users.id}"}

# 所有渠道中的同名模型聚合为一个逻辑模型
- name: models-to-logical-model
  dataset: channels
  for_each: {from: models, transform: split_csv, as: model}
  group_by: "model"
  emit:
    kind: Model
    identity: "model:${key}"
    fields:
      name: {expr: "key"}
```

### 4.5 语义保真度与漏映射告警

保真度分四级，预览报告按资源和字段汇总，让操作者看到哪些行为会改变：

| 级别 | 含义 | 报告处理 |
| --- | --- | --- |
| exact | 语义完全等价 | 只计数 |
| approximate | 主要行为一致，细节不同 | 列出并附 note |
| lossy | 部分行为丢失 | 列出，可配置为需人工确认 |
| dropped | 显式声明不迁移 | 列出字段及原因 |

契约中的每个字段必须被至少一条规则使用，或列入 dropped。两者都不满足时，validate 给出告警，严格模式下报错。这条规则用于防止 group 这类关键字段被静默遗漏。

### 4.6 内置函数与查找表

沿用 v1 的内置函数（类型转换、字符串、拆分合并、parse\_json、lookup、default、filter、map），新增 split\_lines、fingerprint、blank、map\_get（安全读取 JSON 对象的键）。查找表支持 default 项与 `on_missing: fail`，例如渠道类型找不到对应供应商类型时直接报错，不猜测。

### 4.7 扩展点

- Transformer：处理配置无法表达的字段转换，接口与 v1 相同，只做纯函数转换，不写入、不重试。
- Decryptor：独立于 Transformer 的解密扩展，用于源端加密存储的密钥（LiteLLM 接入时需要）。单独注册、单独授权，调用全部记入审计日志。首期 New API 不需要。

## 五、New API 映射设计

一条渠道产出一个 Provider、一到多个 ProviderCredential、多个 ModelDeployment；同名模型跨渠道聚合为一个逻辑 Model；用户和令牌分别映射为 User 和 ConsumerCredential。

### 5.1 资源产出关系

| 源数据 | ACG 资源 | 数量关系 | 标识 |
| --- | --- | --- | --- |
| 渠道 | Provider | 1 : 1 | `channel:<id>` |
| 渠道 key | ProviderCredential | 1 : N（多密钥） | `channel:<id>:key:<指纹>` |
| 渠道 models | ModelDeployment | 1 : N | `channel:<id>:model:<模型名>` |
| 全部渠道的 models | Model | N : 1（按模型名聚合） | `model:<模型名>` |
| 渠道 group、用户 group | Group | N : 1（按分组名聚合） | `group:<分组名>` |
| 用户 | User | 1 : 1 | `user:<id>` |
| 令牌 | ConsumerCredential | 1 : 1 | `token:<id>` |

多条规则可以产出同一标识的资源（例如渠道和用户都产出 Group）。字段一致时合并为一个资源，不一致时视为映射冲突并阻止迁移。

### 5.2 渠道映射

```yaml
# configs/mappings/newapi-to-acg/channels.yaml
version: 2
profile: newapi-to-acg
source: new-api
target_schema: acg/v1

mappings:
  - name: channel-to-provider
    dataset: channels
    emit:
      kind: Provider
      identity: "channel:${row.id}"
      fields:
        name: {from: name}
        type: {from: type, lookup: newapi_channel_types, on_missing: fail}
        endpoint:
          expr: "blank(row.base_url) ? lookup('newapi_default_base_url', row.type) : row.base_url"
        enabled:
          expr: "row.status == 1 || (row.status == 3 && policy.auto_disabled_channel == 'enabled')"

  - name: channel-key-to-credential
    dataset: channels
    when: "!blank(row.key)"
    for_each:
      expr: "lookup('newapi_key_format', row.type) == 'multi_line' ? split_lines(row.key) : [row.key]"
      as: k
    emit:
      kind: ProviderCredential
      identity: "channel:${row.id}:key:${fingerprint(k)}"
      fields:
        provider_ref: {ref: "Provider/channel:${row.id}"}
        secret: {secret_from: k}

  - name: channel-model-to-deployment
    dataset: channels
    for_each: {from: models, transform: split_csv, as: model}
    emit:
      kind: ModelDeployment
      identity: "channel:${row.id}:model:${model}"
      fields:
        model_ref: {ref: "Model/model:${model}"}
        provider_ref: {ref: "Provider/channel:${row.id}"}
        credential_refs: {ref_prefix: "ProviderCredential/channel:${row.id}:key:"}
        upstream_model: {expr: "map_get(row.model_mapping, model, model)"}
        group_refs:
          expr: "split_csv(row.group).map(g, 'Group/group:' + g)"
          as_refs: true
        priority: {from: priority, fidelity: approximate, note: "见 5.3 路由语义"}
        weight: {from: weight, fidelity: approximate}

  - name: channel-group-to-group
    dataset: channels
    for_each: {from: group, transform: split_csv, as: g}
    group_by: "g"
    emit:
      kind: Group
      identity: "group:${key}"
      fields:
        name: {expr: "key"}
```

`ref_prefix` 引用同一前缀下的全部资源，用于部署关联渠道的所有密钥。逻辑 Model 的聚合规则见 4.4 示例。

### 5.3 关键语义决策

- 逻辑模型与部署分离：用户请求的模型名对应逻辑 Model；每个渠道上的该模型是一个 ModelDeployment。多个渠道提供 gpt-4o 时，产出一个 Model 和多个部署，不再命名冲突。
- 模型重定向：`upstream_model` 取 model\_mapping 中该模型的映射值，没有映射时取模型名本身。model\_mapping 解析失败即报错。
- 多密钥：渠道类型在 `newapi_key_format` 查找表中标注 key 格式。multi\_line 按行拆分；json（例如云厂商服务账号凭据）和组合格式整体作为一个密钥，必要时由扩展函数解析。
- 分组权限：渠道的 group 决定哪些用户分组可以使用该渠道，迁移为部署与 Group 的关联。ACG 若没有对应的分组权限概念，迁移在预检阶段直接阻止，不降级处理，因为降级会扩大访问范围。
- 路由语义：New API 在同一分组、同一模型的候选渠道中优先选择最高优先级，同优先级内按权重随机，失败后按重试设置降级到更低优先级。ACG 支持“优先级分层 + 层内加权”时标为 exact，否则标为 approximate 或 lossy 并在报告中说明。
- 渠道状态：

| New API status | 含义 | ACG enabled |
| --- | --- | --- |
| 1 | 启用 | true |
| 2 | 手动禁用 | false |
| 3 | 自动禁用（健康检查失败） | 由策略 `auto_disabled_channel` 决定，默认 false，并在报告中列出 |

- 渠道高级设置：参数覆盖、请求头覆盖、状态码映射、代理等字段，ACG 支持前一律列入 dropped，报告中逐渠道列出被丢弃的非空值，便于人工补配。

### 5.4 用户映射

```yaml
# configs/mappings/newapi-to-acg/users.yaml
  - name: user-to-user
    dataset: users
    when: "row.deleted_at == null"
    emit:
      kind: User
      identity: "user:${row.id}"
      fields:
        username: {from: username}
        display_name: {from: display_name}
        email: {from: email}
        role: {from: role, lookup: newapi_roles, on_missing: fail}
        enabled: {expr: "row.status == 1"}
        group_refs:
          expr: "['Group/group:' + (blank(row.group) ? 'default' : row.group)]"
          as_refs: true
        password: {initial_password: true}      # 不读取源端密码哈希，按第六节处理
        balance: {expr: "convert_quota(row.quota)", fidelity: approximate}
```

- 角色映射：New API 的普通用户（1）、管理员（10）、超级管理员（100）通过 `newapi_roles` 查找表映射到 ACG 角色；未定义的角色值阻止迁移。
- 用户名冲突：目标已存在同名用户时按 conflict 策略处理，默认失败。多个 New API 实例迁入同一 ACG 时，是否合并同名用户需要单独确认。
- 不迁移：软删除用户、系统访问令牌（access\_token）、密码哈希。

### 5.5 令牌映射

```yaml
# configs/mappings/newapi-to-acg/tokens.yaml
  - name: token-to-credential
    dataset: tokens
    join:
      users: {on: "tokens.user_id == users.id", type: inner}
    when: "users.deleted_at == null"
    emit:
      kind: ConsumerCredential
      identity: "token:${row.id}"
      fields:
        user_ref: {ref: "User/user:${users.id}"}
        name: {from: name}
        key: {secret_from: key, prefix: "${target.key_prefix}"}
        enabled: {expr: "row.status != 2"}
        expires_at: {expr: "row.expired_time == -1 ? null : timestamp(row.expired_time)"}
        quota: {expr: "row.unlimited_quota ? null : convert_quota(row.remain_quota)"}
        model_allowlist: {expr: "row.model_limits_enabled ? split_csv(row.model_limits) : []"}
        ip_allowlist: {expr: "split_lines(row.allow_ips)"}
        group_ref:
          expr: "blank(row.group) ? null : 'Group/group:' + row.group"
          as_ref: true
```

- key 前缀：New API 数据库中的令牌一般不含 sk- 前缀，用户实际使用的是前缀加库中值。导入时按目标配置拼接前缀，保证用户手中的 key 原样可用。阶段一用实际数据核对。
- 状态：手动禁用（2）迁移为禁用；已过期、额度耗尽等自动状态不单独迁移，由过期时间和剩余额度在 ACG 侧自然生效。
- 只迁移所属用户仍存在的令牌；孤儿令牌列入报告。

### 5.6 额度换算与对账

```yaml
# configs/jobs/newapi-prod-to-acg.yaml 片段
quota:
  source_units_per_usd: auto      # 读取 New API 的 QuotaPerUnit 设置，默认 500000
  target_units_per_usd: TBD       # 待 ACG 确认
  rounding: floor
  reconcile: true
```

`convert_quota` 按上述比例换算。reconcile 开启时，报告给出换算前后的用户余额总和、令牌剩余额度总和，以及取整造成的差额；差额超过阈值则阻止正式执行。已用额度（used\_quota）属于用量统计，本期不迁移。

## 六、账号与密钥安全

用户密码统一重置为 User@123，令牌和上游密钥原值迁移。统一初始密码意味着知道这个值的人可以登录任何未改密的账号，因此必须配套下列措施，缺一项即不允许正式执行用户迁移。

### 6.1 用户密码统一重置

- 不读取源端密码：源端 bcrypt 哈希不参与迁移，User 资源的 password 字段只声明 `initial_password: true`。
- 初始密码参数化：初始密码从环境变量 `MIGRATOR_INITIAL_PASSWORD` 读取，本项目约定取值 User@123，不写入代码、映射配置、日志、预览报告和状态库。
- 写入方式：优先调用 ACG 的“设置密码”接口传入；若 ACG 支持导入哈希，则由迁移工具按 ACG 的哈希算法计算后写入。
- 强制首次登录修改：每个迁移用户写入 `must_change_password = true`。修改前只允许访问改密页面，不能进入控制台其他功能。
- 初始密码有效期：默认 7 天，可配置。到期仍未修改的账号自动锁定，需管理员重置。
- 管理员账号单独处理：角色为管理员或超级管理员的账号不使用统一初始密码，改为生成一次性随机密码，加密导出后线下交付；或迁移后保持禁用，待人工启用。二选一，见待确认事项。
- 登录保护：ACG 对登录失败做限流和锁定，所有初始密码登录写入审计日志。
- 跟踪与通知：迁移完成后导出用户清单（用户名、邮箱、角色），供业务方通知；report 命令可随时列出仍未改密的账号。

密码重置只影响控制台登录。令牌原值迁移，下游程序调用 API 不受影响。

### 6.2 上游密钥

- 受控容器：被 `secret_from` 标记的值装入 SecretValue 类型。该类型的字符串化、JSON 序列化、日志输出都只得到 `***`，只有导入器在写入时能取出原值。
- 指纹比对：每个密钥计算带盐 HMAC-SHA256 指纹，盐从环境变量读取。指纹用于资源标识、变更检测、多源去重；ACG 的密钥通常只写不读，比对只能依赖指纹。
- 不落盘，断点重读：状态库只存指纹，不存密钥原值。续跑时按资源标识回到源端重新读取密钥，并校验指纹一致；源端不可用时，涉及密钥的操作无法续跑，其余操作可以继续。

### 6.3 令牌原值迁移

New API 的令牌在数据库中以原值存储，可以原值迁移，前提是 ACG 支持指定 key 值写入。目标已存在相同 key 时一律失败，不允许覆盖或跳过后静默共用。

ACG 不支持指定 key 时，降级为重签模式：生成新 key，导出加密的新旧对照文件，由业务方分发，新旧网关并行运行一段时间后再关闭旧 key。重签模式需要在任务配置中显式开启。

### 6.4 数据保护

- 日志、预览报告、状态库全部脱敏；报告中的密钥只显示指纹前 8 位。
- 源库 DSN、ACG 凭据、指纹盐、初始密码全部来自环境变量或密钥管理系统，配置文件中只出现变量名。
- 任务产物中含敏感内容的文件（管理员一次性密码、重签对照表）一律加密输出，并在报告中提示保管和销毁。

## 七、内部标准模型、统一导入器与前置要求

acg/v1 首期定义 9 类资源，字段在阶段一与 ACG 团队共同定稿；导入器优先走 ACG 管理 API，且要求 ACG 具备下文列出的前置能力。

### 7.1 acg/v1 首期资源

| 资源 | 引用 | New API 来源 | 说明 |
| --- | --- | --- | --- |
| Group | — | 渠道 group、用户 group | 访问权限分组 |
| Provider | — | 渠道 | 上游服务地址与类型 |
| ProviderCredential | Provider | 渠道 key | 上游密钥，只写 |
| Model | — | 渠道 models 聚合 | 对外暴露的逻辑模型 |
| ModelDeployment | Model、Provider、ProviderCredential、Group | 渠道 × 模型 | 含 upstream\_model、优先级、权重 |
| RoutingPolicy | Model、Group | 由部署的优先级与权重推导 | ACG 路由内置于部署时可省略 |
| User | Group | 用户 | 含角色、状态、强制改密标记 |
| ConsumerCredential | User、Group | 令牌 | 含额度、过期时间、模型与 IP 白名单 |
| QuotaPolicy | User | 用户额度 | ACG 以余额字段表示时可并入 User |

所有资源携带统一元数据：`source_identity`（带实例前缀的来源标识）、`migration_job`（任务 ID）、`content_hash`（期望内容哈希）。资源结构同时由 Go 类型和 JSON Schema 约束，schema 带版本号。

### 7.2 统一导入器接口

相比 v1，接口增加能力探测、按来源标识查询、批量写入、写前快照，引用改为执行时绑定。

```go
type Importer interface {
    // 探测 ACG 支持的能力，用于预检与降级决策
    Capabilities(ctx context.Context) (Capabilities, error)

    // 按来源标识查询目标资源，用于幂等、超时后确认、回滚
    Lookup(ctx context.Context, kind Kind, sourceIdentity string) (*TargetRef, error)

    // 读取目标现状，生成操作计划；Operation 中的引用尚未解析
    Plan(ctx context.Context, desired []Resource, state StateReader) (*Plan, error)

    // 批量写入；执行前由 RefResolver 把逻辑引用替换为目标 ID
    ApplyBatch(ctx context.Context, ops []Operation, refs RefResolver) ([]ApplyResult, error)

    // 更新或删除前保存目标原值，用于回滚
    Snapshot(ctx context.Context, ref TargetRef) (Resource, error)

    // 回读并校验关键字段与引用关系
    Verify(ctx context.Context, results []ApplyResult) error
}
```

### 7.3 对 ACG 的前置要求

以下能力需在阶段一与 ACG 团队逐项确认，结论写入 targets 配置的 capabilities 部分。

| 能力 | 用途 | 缺失时的影响 |
| --- | --- | --- |
| 资源可写入并按 source\_identity 查询 | 幂等、超时后确认、回滚、prune | 只能按业务唯一键匹配，回滚和 prune 无法精确识别迁移资源 |
| 按 migration\_job 列出资源 | 回滚、对账 | 回滚依赖本地状态库，状态库丢失即无法回滚 |
| 指定 API Key 原值写入，支持前缀规则 | 令牌无感迁移 | 降级为重签模式，所有用户需更换 key |
| 写入幂等键或业务唯一键 | 安全重试 | 超时重试可能产生重复资源 |
| 批量写入或事务 | 性能、原子性 | 逐条写入，失败时按原子单元补偿 |
| 设置密码接口或哈希导入 | 密码统一重置 | 用户迁移无法实施 |
| 强制改密标记、初始密码有效期 | 第六节安全措施 | 用户迁移不允许正式执行，需 ACG 先开发 |
| 分组权限 | 渠道访问控制 | 迁移阻止，不降级 |
| 逻辑模型与部署分离 | 多渠道同名模型 | 无法表达 New API 的多渠道负载 |
| 优先级分层加层内权重 | 路由语义 | 路由降级为 approximate 或 lossy |
| 余额与额度写入、额度单位说明 | 额度迁移 | 额度无法迁移或无法对账 |
| 配置下发限速或批量刷新 | 大批量写入 | 可能引发配置下发风暴 |

ACG 只能直写数据库时，作为受控的离线写入模式单独评审，不作为默认方案。

## 八、迁移执行引擎

引擎分阶段运行，状态以资源级操作记录在 SQLite；同一任务可以反复执行并收敛到源端当前状态，也可以整体回滚。

### 8.1 执行阶段

1. 预检：配置与表达式编译、源版本探测、漂移检测、ACG 能力探测。任何一项失败即停止。
2. 采集：在一致性快照中读取全部数据集。
3. 映射：生成期望资源，同时记录字段溯源（目标字段来自哪个源字段、哪条规则）。
4. 校验：JSON Schema 校验、引用解析、依赖图构建与环检测、保真度汇总、漏映射检查。
5. 规划：读取 ACG 现状，三方对比生成操作计划，计算计划指纹。plan 模式到此输出报告并结束。
6. 执行：按依赖拓扑分批写入，写入前绑定引用，更新或删除前保存快照。
7. 验证：回读关键字段与引用关系；可选功能探测（对每个上游部署发一次低成本请求，默认关闭，会产生费用）。
8. 报告：操作结果、保真度、dropped 字段、额度对账、未改密用户清单。

### 8.2 依赖、延迟绑定与失败传播

- 依赖图由资源引用构建，写入顺序为 Group → Provider → ProviderCredential → Model → ModelDeployment → RoutingPolicy → User → ConsumerCredential → QuotaPolicy。
- Plan 阶段目标 ID 尚不存在，Operation 中保留逻辑引用；执行时由 RefResolver 从 ID 映射表取出真实 ID 填入。
- 原子单元：一个 Provider 及其密钥和部署作为一个单元。单元内任一操作失败，依赖它的资源标记为 blocked，不再尝试写入；单元外的资源照常执行。

### 8.3 幂等与超时处理

- 每个操作的幂等键 = 来源标识 + 期望内容哈希。ACG 支持幂等键时随请求传入。
- 写入超时或连接中断时，不直接重试：先用 Lookup 按来源标识查询，目标已存在且内容哈希一致则视为成功，否则再重试。
- 重试次数有限，超过后记为失败，等待人工处理或下次 resume。

### 8.4 增量收敛：三方对比

状态库记录每个资源上次写入的内容哈希（last\_applied）。重复执行时对比源端期望、上次写入、ACG 当前三者：

| 源端期望 vs 上次写入 | ACG 当前 vs 上次写入 | 判定 | 默认动作 |
| --- | --- | --- | --- |
| 相同 | 相同 | 无变化 | 跳过 |
| 不同 | 相同 | 源端变化 | 更新 |
| 相同 | 不同 | 目标被人工修改（drift） | 保留目标，报告 |
| 不同 | 不同 | 双方都变 | 冲突，按 conflict 策略 |
| 源端已删除 | 任意 | 源端删除 | 按 prune 策略，默认只报告 |
| 首次出现，目标已有同名资源 | — | 与非迁移资源冲突 | 按 conflict 策略，默认失败 |

### 8.5 计划指纹

plan 输出计划文件及其指纹。`apply --plan <文件>` 执行前重新读取相关目标资源，与计划生成时的状态比较；不一致则拒绝执行，要求重新 plan。这样审核过的计划和实际执行的操作保持一致。

### 8.6 回滚

- 写前快照：update 和 delete 操作执行前保存目标原值。
- `rollback --job <ID>` 按逆拓扑顺序执行：删除本任务创建的资源，用快照恢复被更新的资源。
- 回滚前检查用户和令牌是否已在 ACG 上产生使用记录；已使用的资源默认不删除，列入报告由人工决定。

### 8.7 切换流程

1. 在预演环境用生产数据副本完成全量迁移、校验和一次回滚演练。
2. 生产 ACG 全量迁移，导出报告，人工审核保真度与 dropped 字段。
3. 多轮增量执行，追平源端变化，直到单轮变更量稳定在很小范围。
4. 冻结窗口：New API 停止配置变更（渠道、用户、令牌的增删改）。
5. 最终一次增量执行，完成额度对账。
6. 下游流量切换到 ACG。
7. 切换后立即将 New API 置为只读或停止对外服务，避免两侧同时消耗额度造成余额不一致。
8. 观察期结束后，New API 数据保留一段时间用于追溯，再按规定下线。

### 8.8 策略配置

```yaml
policies:
  conflict: fail              # fail | skip | update
  unsupported: fail
  missing_reference: fail
  missing_secret: fail
  drift: report               # report | overwrite
  prune: report               # report | delete
  auto_disabled_channel: disabled
  lossy_requires_ack: true    # 存在 lossy 字段时正式执行需人工确认

execution:
  mode: plan                  # plan | apply
  batch_size: 100
  retries: 3
  verify_after_write: true
  probe: false
```

### 8.9 状态库

首期使用 SQLite，包含任务、操作、ID 映射（来源标识、目标 ID、last\_applied 哈希、密钥指纹）、写前快照四类数据。同一状态库加文件锁，同一时间只允许一个进程执行。状态库不存密钥原值，但仍属于敏感数据，按 6.4 管理。

## 九、配置组织、工程结构与 CLI

配置按“项目契约、实例连接、映射、查找表、目标、任务、测试”七类分开管理；映射配置与代码一样需要通过 golden test 才能合入。

### 9.1 配置目录

```
configs/
├── sources/
│   └── new-api.yaml                 # 项目级：逻辑数据集契约、版本探测
├── instances/
│   └── newapi-prod.yaml             # 实例级：连接方式与凭据变量名
├── mappings/
│   └── newapi-to-acg/
│       ├── channels.yaml
│       ├── users.yaml
│       └── tokens.yaml
├── lookups/
│   ├── newapi-channel-types.yaml    # 渠道类型 → 供应商类型
│   ├── newapi-default-base-url.yaml
│   ├── newapi-key-format.yaml       # 渠道类型 → key 格式
│   └── newapi-roles.yaml
├── targets/
│   └── acg-prod.yaml                # 地址、认证、capabilities 确认结论
├── jobs/
│   └── newapi-prod-to-acg.yaml      # 来源实例、映射、策略、额度换算
└── tests/
    └── newapi-to-acg/
        ├── fixtures/                # 脱敏样例输入
        └── expected/                # 期望输出的资源
```

后续接入 LiteLLM 时，新增 sources/litellm.yaml、mappings/litellm-to-acg/ 及对应 lookups 和 tests。

### 9.2 Go 工程结构

```
gateway-migrator/
├── cmd/migrator/
├── internal/
│   ├── config/          # 配置解析、DSL schema 校验
│   ├── source/
│   │   ├── contract/    # 逻辑数据集契约、版本探测、漂移检测
│   │   ├── database/    # MySQL、PostgreSQL、SQLite，方言与快照
│   │   ├── api/         # HTTP 读取、分页、限流、重试
│   │   ├── kubernetes/  # 预留给 Higress
│   │   └── file/
│   ├── mapping/
│   │   ├── compiler/    # CEL 编译、插值语法糖
│   │   ├── evaluator/   # join、when、for_each、group_by
│   │   ├── identity/    # 标识规则、实例前缀
│   │   ├── fidelity/    # 保真度汇总、漏映射检查
│   │   └── lineage/     # 字段溯源
│   ├── transforms/      # 内置函数、查找表
│   ├── extensions/      # Transformer、Decryptor 注册
│   ├── domain/          # acg/v1 资源类型
│   ├── importer/acg/    # ACG 导入器
│   ├── engine/          # 规划、执行、三方对比、计划指纹、回滚、验证
│   ├── secrets/         # SecretValue、指纹、初始密码
│   ├── state/           # SQLite 状态库、文件锁
│   └── report/          # 预览与结果报告
├── schemas/acg/v1/
├── configs/
└── go.mod
```

### 9.3 CLI

| 命令 | 作用 |
| --- | --- |
| `validate` | 校验配置、编译表达式、检查漏映射，不连接任何系统 |
| `test` | 运行映射 golden test |
| `probe` | 探测源版本、表结构漂移、ACG 能力 |
| `plan` | 采集、映射、校验、规划，输出计划文件与预览报告 |
| `apply --plan` | 校验计划指纹后执行 |
| `resume --job` | 从上次确认完成的操作继续 |
| `verify --job` | 重新回读校验，可选功能探测 |
| `rollback --job` | 撤销任务写入 |
| `report --job` | 输出结果、对账、未改密用户清单 |

每次 apply、resume、rollback 写入审计日志：执行人、时间、计划指纹、操作统计。报告提供 JSON 和 HTML 两种格式。

## 十、实施计划与待确认事项

首期分四个阶段，每个阶段有明确的通过条件；LiteLLM、Higress 在首期完成后再启动。

### 10.1 实施阶段

1. 契约定稿：确认 ACG 前置能力（7.3）、定稿 acg/v1 首期资源字段、定稿 DSL 规范、范围矩阵经业务方确认。
   - 通过条件：7.3 每项能力都有结论，缺失项有降级方案或 ACG 开发排期。
2. 渠道闭环：三种数据库方言读取、渠道到 Provider、ProviderCredential、Model、ModelDeployment、Group 的映射，plan、apply、verify。
   - 通过条件：测试环境全量迁移成功；无未声明字段；同一任务重复执行变更数为零。
3. 用户与令牌：用户、令牌映射，密码统一重置及 6.1 全部配套措施，额度换算与对账，管理 API 读取。
   - 通过条件：额度对账差额在阈值内；抽样令牌经 ACG 调用成功；新用户首次登录被强制改密。
4. 生产化：三方对比增量收敛、计划指纹、回滚、审计、切换演练。
   - 通过条件：在预演环境完成一次包含回滚的完整切换演练。

后续：LiteLLM 接入（Decryptor 解密扩展、virtual key 只存哈希导致的重签流程）；Higress 接入（K8s 读取器、嵌套结构遍历、插件作用域语义）。

### 10.2 待确认事项

- [ ] ACG 的额度单位，以及与 New API quota 的换算比例
- [ ] 分组倍率、模型倍率与价格是否迁移，ACG 计费体系如何对应
- [ ] 第三方登录绑定（GitHub、OIDC 等）是否迁移
- [ ] 管理员账号处理方式：一次性随机密码线下交付，或迁移后禁用待人工启用
- [ ] 初始密码有效期（默认 7 天）是否合适
- [ ] 自动禁用的渠道迁移后保持禁用还是启用
- [ ] 多个 New API 实例迁入同一 ACG 时，同名用户、同名分组是否合并
- [ ] 渠道高级设置（参数覆盖、请求头覆盖、状态码映射、代理）在 ACG 中的支持情况
- [ ] 首期支持的 New API 版本区间
- [ ] ACG 令牌的 key 前缀规则与 New API 是否一致
