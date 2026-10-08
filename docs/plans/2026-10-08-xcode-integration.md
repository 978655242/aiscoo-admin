# XCode / XWork 私有网关实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在本机贯通 New API 原生账号、管理员指定订阅、隔离计费与 XWork 普通 Agent，同时保留独立官方云服务和现有 UI 交互。

**Architecture:** New API 复用 Router → Controller → Service → Model、原生 Session/AuthFlow/Token/Subscription/Logs；XCode 复用 Host/RPC、AccountProviderService、Registry 和本机 worker。唯一指定关系不保存余额；官方连接按 New API 主用户隔离，不从普通模型 Registry 推断。

**Tech Stack:** Go 1.25.1、Gin、GORM、Redis、SQLite/MySQL/PostgreSQL；New API React 19/TypeScript/Bun；XCode TypeScript/Electron/pnpm、现有受限 CEL 编译器。

**Spec:** [New API canonical spec](../specs/xcode-integration.md)；[XCode 客户端 spec](../../../XCode/docs/xcode/specs/new-api-integration.md)。HTTP、错误码、凭据及计费规则只以 canonical 为准。

## Global Constraints

- 本期仅桌面 Local Host 与本机 Agent/worker；不实现 SSH/WSL/Docker 私有远程、转发、凭据 provisioning 或独立 Web 多用户登录。
- 开发 origin：`http://127.0.0.1:3001`；显式配置，不采用 status.server_address 或未经验证的 forwarded host。
- New API：`XCODE_INTEGRATION_ENABLED`、`XCODE_PUBLIC_ORIGIN`；XCode：`NEW_API_BASE_URL`。禁用入口仍保留 purpose/scope 隔离。
- `client_id=xcode-desktop`；精确回调 `zcode://oauth/callback`；只允许 PKCE S256。state 至少 128 bit；授权请求 10 分钟，code/handoff 60 秒。
- control：`aud=new-api-xcode`、`token_use=xcode_access`、固定 `profile:read/wallet:read/usage:read`；15 分钟且不晚于 SID。Session 最长 30 天；Web 凭据最多 15 分钟且不晚于 SID。
- 托管 key 只开放 `POST /v1/messages`、`POST /v1/chat/completions`、`POST /v1/responses`；不增加协议转发层。
- 用户最多一个当前指定授权；普通 key 仅 general/wallet，托管 key 仅指定 xcode 实例，永不 fallback。
- 单周期 `never/daily/weekly/monthly/custom`；`AmountTotal=0` 是 unlimited。按最终结算时周期收费，换套餐仍结算原实例。
- ID/revision/额度/token/记录计数以十进制字符串传递；时间 Unix 毫秒或 null；不创造第二份余额、历史周期余额或统计账本。
- SQLite、MySQL ≥5.7.8、PostgreSQL ≥9.6 同时支持；日志查询还覆盖项目已支持的独立 Log DB/ClickHouse。
- `XCodeConfig` TEXT：64 KiB 上限，单 map 8 KiB；exact-name 元数据，完整能力由管理员提供；Go 不实现 CEL 编译器。
- 保留原生支付、原生注册/安全校验、用户自配 Provider、历史会话和项目/作者归属；不增加 org/project、Start 钱包、额外 reset 机会。
- 保留 UI 布局/导航/交互；名称、额度单位、周期、缺失指标按真实后端适配。未知指标不填 0。
- 根模块 JSON 使用 `common.*`；计费沿用 `.agents/rules/billing.md` 的 checked quota 换算与饱和审计。本方案不要求修改 `relaykit/`。

---

## 1. 当前取证、文档边界与执行纪律

以下为计划编写时的实施前基线，实际实施结果记录在 §6。2026-10-08 读取源码并执行 `git rev-parse HEAD`，两仓 HEAD 与设计基线一致：

| 仓库 | 实际 HEAD | 注意 |
| --- | --- | --- |
| New API | `973cf8ef4600947a4270e95ada7916740fa8264c` | HEAD 一致不证明工作区无未提交改动，也不证明当前二进制一致 |
| XCode | `7740b06bb501e1dce2ab5db867a5f0c3365e4f1a` | 按客户端 spec 的 feature 分支与 `docs/xcode/BRANCHING.md` 实施 |

当前源码风险已经核对：

- `model/token_cache.go` Lua 固定 HSET 字段，读端当前没有明确 schema 校验；`model/user_session.go` 双向 cache 转换也必须加 purpose。
- `service/auth_session.go` 原 refresh bundle 产生 dashboard JWT；仅新增 audience 不能防用途升级。
- `FundingSource.Settle(delta int)` 及 `BillingSession.Settle` 的 delta=0 提前返回不支持跨重置结算；Reserve 对旧总预扣的提前返回同样不成立。
- `RefundSubscriptionPreConsume` 的原记录锁与实例更新不在同一事务，且锁序与目标不一致；七天清理没有专用未完成记录保护。
- XCode Account snapshot 尚无完整 `modelConfigs/catalogIssues`；overlay 只接受已有 Built-in ID；worker 的 received revision 不等于 Registry 已应用。
- 官方 MCP、闲时模型及会话分享复用主账号/普通 Registry；嵌入套餐页有官方长期凭据注入路径。不能通过全局替换 URL 完成隔离。

执行每项任务时：先读所列实现与消费者，再查询 exported symbol references；New API 用可用语言服务器，XCode 同时遵守 `pnpm dep:refs --list-exports <file>` 等架构治理。行号随修改变化，以下路径/符号是稳定定位，不使用过时行号作 patch。

每个任务按“行为回归先失败 → 最小实现 → 聚焦回归 → 实际 smoke → 独立提交”交付；提交前使用明确文件列表，禁止 `git add .` 混入用户改动。不批量新增 forwarding/source-text 测试，不保留 deprecated alias 或原官方网关 fallback。现有 fixture 必须显式初始化 Session/Plan/预扣记录与缓存；串行 SQLite fixture 不能证明并发。

## 2. 阶段顺序与文件所有权

| 阶段 | 任务 | 对应设计切片 | 开放条件 |
| --- | --- | --- | --- |
| P0 资金与数据安全底座 | T1–T3 | N1 | 不签发客户端凭据；先证明原生隔离与跨周期事实 |
| P1 指定授权与管理员 UI | T4 | N2 | 三库授权/迁移/并发通过后才可授予专用实例 |
| P2 专用凭据和 PKCE | T5–T6 | N3 | P0/P1 验收后开放专用登录 |
| P3 只读 Facade/元数据/Web | T7–T9 | N4 | 真实契约可供 Host 消费 |
| P4 Host 主账号与官方连接 | T10–T11 | X1/X4 | 普通 Registry 切换前切断官方云对主账号依赖 |
| P5 Registry/worker/控制 UI | T12–T13 | X2/X3 | 完整数据与请求期 secret 一起贯通 |
| P6 本机联调与发布门槛 | T14 | N5/X5 | 两仓验收矩阵逐项有证据，不以 build 代替 |

共享改动必须有一个集成 owner：`model/subscription.go`/billing 链、auth/cache 链、XCode `node.ts`/shared union/Registry 各自不能由并行任务覆盖。T7 元数据与 T8 日志聚合在接口冻结后可并行；T11 与 T12 只有在 T10 owner 契约固定、`node.ts` 由同一 owner 集成时可并行。无需为了并行拆出通用框架。

新增生产文件按领域组织：New API `model/xcode_authorization.go`、`service/xcode_auth.go`、`service/xcode_facade.go`、`service/xcode_usage.go`、`controller/xcode.go`、`middleware/xcode.go`、`dto/xcode.go`。这些是拟新增文件，不是现有实现；同领域逻辑优先放这些文件，业务不进入 DTO/router。模型能力校验放现有 metadata 领域，日志 SQL 放 `model/log.go`。XCode 新增 `packages/services/src/oauth/providers/newApiProviderAdapter.ts`、`packages/services/src/new-api/newApiClient.ts`、`packages/shared/src/new-api.ts`、`packages/services/src/official-cloud/officialCloudConnection.ts` 与 `officialCloudConnectionService.ts`；复用 OAuth/Provider 框架，不另建通用 backend adapter 层。

## 3. 任务清单

### T1：数据模型、迁移与可信缓存分类

**Files:** 修改 `model/token.go`、`model/token_cache.go`、`model/user_session.go`、`model/subscription.go`、`model/model_meta.go`、`model/main.go`；新增 `model/xcode_authorization.go`。测试优先扩展 `model/token_migration_test.go`、`model/user_session_migration_test.go`、`model/token_auto_groups_cache_test.go`。

**Interfaces:** 产生 canonical §3.1 六类持久化扩展；`XCodeAuthorization.UserId` 普通唯一索引，`SubscriptionId/Revision/UpdatedAt` 不保存额度。旧数据库空 purpose/scope 为原生；缓存字段缺失不等于原生。

- [x] 先补旧行/旧 hash/缺字段/未知 purpose 的回归：数据库既有 key 仍原生，数据库托管 key 即使 cache 损坏仍回源后按托管拒绝；Session 热缓存同样不能丢 purpose。
- [x] 添加字段及迁移注册；purpose/scope 非客户端可写；专用周期 snapshot 创建后不可改，generation 从 1 开始。预扣字段包括 `FundingScope/ResetGeneration/CycleReservedQuota/FinalQuota` 和 settled 状态。
- [x] 同步 Lua ARGV/HSET、cache 双向转换和 schema 校验；旧 namespace/hash 做安全失效清理，复用 mutation fence/tombstone。错误 cache miss 回 DB；DB 失败拒绝，不以零值继续。
- [x] 用真实三库执行新库及最新发布版本代表性数据升级，启动迁移两次；检查旧数据、唯一性/索引及幂等。记录 release tag/SHA、数据库版本与命令，不把设计 HEAD 当成最新 release。

**验收:** 不自动指定既有订阅；无托管 key 降级；无第二账本；无 `default:true`/partial index/数据库 enum 新依赖。T1 不开放 Facade。

### T2：共享 admission、资金选择和全入口拒绝

**Files:** `constant/context_key.go`、`middleware/auth.go`、`middleware/task_artifact_access.go`、`relay/common/relay_info.go`、`service/billing_session.go`、`service/funding_source.go`、`model/subscription.go`；核对 `router/api-router.go`、`router/relay-router.go`、`router/task-router.go`、`router/video-router.go`、`router/plugin-router.go`、`router/task-plugin-protocol-router.go` 的全部 key 分支。

**Interfaces:** 服务端 admission 将 `{funding_scope,subscription_id,authorization_revision}` 放可信 context/RelayInfo；`PreConsumeUserSubscription` 新输入显式携带 scope/目标实例/revision，所有调用者一起迁移。客户端不能指定这些事实。

固定共享输入，放在现有 `model/subscription.go`，避免分别增加不一致的布尔参数。以下是目标签名，T2 必须迁移所有现有调用者：

```go
type SubscriptionFundingTarget struct {
    Scope                 string
    SubscriptionID        int
    AuthorizationRevision int64
}

func PreConsumeUserSubscription(
    requestID string, userID int, modelName string, quotaType int, amount int64,
    target SubscriptionFundingTarget,
) (*SubscriptionPreConsumeResult, error)
```

general 显式传 `Scope="general"`、其余两项为0；xcode 传 admission 的非零实例/revision。零值或未知 scope 拒绝，不能用默认参数保留旧的“查询全部订阅”入口。

- [x] 扩展 `controller/billing_option_test.go` 的真实三库资金策略 fixture：同用户 general+xcode+wallet、所有 billing_preference、专用 overflow=false；断言被选实例和各余额变化，不仅断言 HTTP 成功。
- [x] general 查询同时改 `HasActiveUserSubscription`、`UserActiveSubscriptionsAllowWalletOverflow`、预扣与全部 fallback；未知 scope 不能成为可扣候选。托管 admission 校验同用户、有效 SID/auth_version、当前指定状态/有效期；无授权时免费模型/预扣0/trust也拒绝。
- [x] 把精确 method+route allowlist 应用到 TokenAuth、TokenAuthReadOnly、TokenOrUserAuth、TokenOrTaskArtifactAccessAuth、插件/能力 key 分支；拒绝 models、usage/log token、旧 dashboard、compact、WebSocket GET、图片/视频/task/artifact。独立 route-bound capability 不扩大用途。
- [x] 预扣事务重新校验指定关系与目标；request_id 重放必须匹配 user/scope/原实例，冲突不可复用其他资金记录。加入类型化错误，旧 substring 不再驱动新增资金回退。

**验收:** 托管耗尽时钱包/其他订阅一分不动；普通任务/图片/视频也排除专用资金；禁用入口后隔离仍成立。原普通 readonly 过期/额度语义保持，不能顺手更改。

### T3：跨 reset 的 Reserve/Settle/Refund 权威事实

**Files:** `model/subscription.go`、`service/funding_source.go`、`service/billing_session.go`、`service/billing.go`、`service/quota.go`、`service/text_quota.go`、`service/tiered_settle.go`、`service/image_billing.go`、`service/task_billing.go`、`service/violation_fee.go`、`service/subscription_reset_task.go`。扩展 `service/tiered_settle_test.go`、`service/task_billing_test.go` 和 `model/subscription_reset_test.go`，不在各层复制相同 fixture。

**Interfaces:** 将现有 `FundingSource.Settle(delta int)` clean cutover 为 `Settle(actualQuota, totalReservedQuota int) error`，BillingSession 传完整 actual/总预扣；wallet/general 内部计算差额，xcode 只信任原请求 DB 记录。专用 Reserve/rollback 按请求事实更新，不能调用 legacy delta-only 路径。

专用事务算法如下，所有数值按 checked 整数规则处理：

```text
锁原实例 → 执行仍在有效寿命内应有的懒 reset → 锁该 request 记录
确认 user/scope/instance；已 settled/refunded 按幂等状态处理
currentReserved = record.ResetGeneration == instance.Generation
                  ? record.CycleReservedQuota : 0
Reserve(target): used += target - currentReserved；记录当前代与 target
Settle(actual): used += actual - currentReserved；记录 actual、当前代、settled
Refund: used -= currentReserved；记录当前代、refunded
rollback: 只撤回本操作仍存在于当前代的占用，不撤回跨代消失的占用
提交资金与记录；失败回滚，不换实例、不假报结算成功
```

- [x] 先写确定性用例：预扣100→reset→actual150 当期150；actual100当期100；新周期已有另一请求40，旧预扣退款后仍40；重复 settle/refund 不二次变动。
- [x] 自动/懒/管理员 reset 在同一实例锁事务清 used 并增 generation；general 保持 live-template；xcode 使用 snapshot。never 只有真实管理员 reset 增代；renew 不增代。
- [x] 移除专用 delta=0、target≤旧预扣的错误提前返回；追加 reserve 与 rollback 保存本请求当前代事实，rollback携本操作generation/原占用快照并在锁内核对，已跨代则不减新周期used；专用 Refund 不再二次 `PostConsumeDelta(-extraReserved)`。统一实例→记录锁序及同一 tx，不嵌套另开退款事务。
- [x] 补跨代 reserve/rollback、replace/revoke 后原 A 结算、到期不创建新周期、结算不足与清理保留未完成记录。观察实际原实例 used/记录/status/审计，而非内存 extraReserved。

**验收:** 流式已发送内容不能撤回，实际结算失败必须可见；原 general/wallet、普通异步任务结算不改策略。检查全部 FundingSource/SettleBilling/PostConsumeQuota 调用者，托管不走无 request/generation fallback。

### T4：管理员指定授权事务与原生订阅 UI

**Files:** `model/xcode_authorization.go`、`model/subscription.go`、`controller/xcode.go`、`dto/xcode.go`、`router/api-router.go`、`middleware/access_token_routes.go`；`web/src/features/subscriptions/{api.ts,types.ts}`、`components/dialogs/user-subscriptions-dialog.tsx` 与现有 locale 文件。

**Interfaces:** `GET/POST /api/xcode/v1/admin/users/:id/authorization`，输入 canonical `{action,expected_revision,plan_id?,advance_reset_time?}`，输出 AuthorizationSnapshot。原生 AdminAuth；PAT 额外 billing:read/write，不增加 billing Casbin resource。

- [ ] 扩展现有订阅 reset/auth 测试与 `controller/billing_option_test.go` matrix：并发相同 revision 仅一次提交；同模板 general/xcode reset 只变指定实例；原生管理员非 root 可操作，缺 PAT scope 拒绝。
- [ ] 实现锁序 用户→关系→实例；grant 新建 admin/xcode；active renew 原实例顺延当前模板时长并保留预算/已用/锚点，修复截断 next_reset_time；expired renew 新建；cancelled/revoked 不 renew 复活；replace 取消A新建B无结转；revoke/reset 同事务递增 revision。
- [ ] 校验 compliance、启用模板、`MaxPurchasePerUser=0`；实例 overflow=false。创建快照；续期不复制新 TotalAmount/reset。原 invalidate/cancel/delete 同步维护关系/revision；被关系/预扣/在途任务引用的 xcode 实例拒绝硬删。提交后刷新 cache/group，审计无秘密。
- [ ] 原对话框增加独立“XCode 指定授权”区域，复用模板/实例展示和共享确认弹窗；明确普通绑定不指定、group影响全用户、批量reset与专用reset不同。实际操作 grant→renew→replace→reset→revoke 并观察 DB 与 UI。

**验收:** 自助原生订单仍 general，钱包/订单没有伪付款；模板编辑不改变旧专用周期；并发 409 没有重复顺延或双有效实例。

### T5：Session/JWT purpose 和托管 key 生命周期

**Files:** `service/auth_token.go`、`service/auth_session.go`、`controller/auth_session.go`、`model/user_session.go`、`model/token.go`、`controller/token.go`、`middleware/auth.go`、`middleware/audit.go`、新增 `middleware/xcode.go`。扩展现有 auth_token/auth_session/token/security_account/auth 测试。

**Interfaces:** 复用 authClaims 与签名代码，新增独立 xcode_access/xcode_web signing purpose；dashboard、安全 proof、control、Web 凭据互不接受。受管 key 为 `ClientPurpose=xcode/ClientSessionSID=<同用户SID>/UnlimitedQuota=true`，不改用户 billing_preference/当前组。

- [ ] 先补 root control 调原生支付/key/PAT/账户安全均403、坏内部 JWT 不落 PAT/key、xcode SID+原生 refresh Cookie 不拿 dashboard JWT；覆盖原 refresh 的 race recovery 签发分支。
- [ ] 签发、分类、解析、ValidateLoginSession 与 refresh 全链按 purpose 限定，exp 不晚于 SID。Relay 校验 auth_version/Session状态/到期，不以正常 refresh 的 SessionVersion 变化误撤 key。
- [ ] key 单删/批删/停用撤销关联 SID；模型层受管字段更新限制与 HTTP CRUD一起做，cache失败仍靠 SID 拒绝。刷新返回同 key，不重建用户已删除/停用 key；保留原 Session/key 数量和签发限额。
- [ ] 在真实 Redis 热缓存观察原生设备撤销、revoke-all/revoke-others、密码/安全因子变更后新推理拒绝。XCode退出只撤专用 SID，原浏览器 SID继续有效。

**验收:** 凭据不可升级/互换；不声称超出原认证多节点拓扑保证的即时撤销。安全事件只记录非秘密上下文。

### T6：Authorization Code + PKCE 与专用 SessionBundle

**Files:** 新增 `service/xcode_auth.go`；修改 `model/auth_flow.go`、`model/user_session.go`、`model/token.go`、`controller/xcode.go`、`dto/xcode.go`、`router/api-router.go`、`common/env.go`、`.env.example`；New API 原生授权页新增 `web/src/routes/xcode/authorize.tsx`、`web/src/features/xcode/authorize.tsx`，复用登录与安全流程。

**Interfaces:** canonical §6.2 五条 auth 路由及 SessionBundle；授权 flow/code 各用独立 AuthFlow purpose。`ConsumeAuthFlowWithAction` 内同 tx 消费code、创建专用 SID及key；只为现有创建逻辑增加必要 tx seam，禁止调用各自先提交的方法拼接。

- [ ] 在现有 `controller/auth_flow_test.go`/`controller/auth_session_test.go` 先补 S256错误、plain降级、URI/client错误、过期、code重复消费、来源SID/auth_version失效、key限额导致兑换整体回滚。
- [ ] 校验固定 client/精确URI/state/challenge，flow10分钟/code60秒；complete 必须真实原生浏览器 Bearer，只接受验证过的本源登录返回目标。authorize 页面复用登录/注册和启用的验证，不自建密码表单。
- [ ] 原子兑换并输出同用户 SessionBundle；仅认证响应给Host明文sk-key。专用 refresh复用轮换/replay/recovery，只发专用JWT；logout要求有效control或匹配refresh，不凭裸SID撤销。
- [ ] 对 auth 路由限流、no-store及错误码/真实HTTP状态做实际请求 smoke；检查回调只有 provider/code/state，日志脱敏code/verifier/refresh/key，取消与迟到凭据由T10处理。

**验收:** 不增加 polling 或通用 IdP；开关关闭无新签发，T2/T5拒绝不关闭。

### T7：管理员完整模型元数据与真实目录

**Files:** `model/model_meta.go`、`model/model_metadata_sync.go`、`model/ability.go`、`controller/model_meta.go`、`controller/model_sync.go`、`controller/model.go`、`service/xcode_facade.go`、`controller/xcode.go`、`dto/xcode.go`；`web/src/features/models/{api.ts,types.ts,lib/model-form.ts,components/drawers/model-mutate-drawer.tsx}`。

**Interfaces:** `XCodeConfig={schema_version:1,api_type,model_config}`；`GET /models` 返回 canonical CatalogSnapshot。server做结构/边界校验，Host用现有 `completeModelConfigDataSchema` 做受限 CEL编译。

- [ ] 扩展 `controller/model_management_test.go` 与现有前端 `metadata-editing/metadata-sync` 回归：缺布尔/错误整数/超64KiB/超8KiB/重复reasoning值拒绝；显式false保留；只改其他字段或官方sync不擦配置。
- [ ] 原生 Model.Update Select白名单/编辑缺字段语义纳入XCodeConfig；JsonEditor复用 `web/src/components/json-editor.tsx`，默认不提供猜测GLM配置；版本与原同步并发控制保留。
- [ ] 按用户实际组、启用channel/ability及真实model ID求目录，only exact metadata；api_type/config缺失返回null和disabled reason。候选上游能力一致性由管理员配置与真实请求证明；不推断prefix能力。
- [ ] catalog_revision覆盖组/ability/channel/metadata/授权变化，可对实际有效目录与权限规范化计算稳定摘要，不仅使用Model更新时间。托管请求在shared validator检查实际model、协议与metadata最大输出，保留原maxTokensLimit和错误envelope。

**验收:** 无key/上游URL泄露；OpenAI与Anthropic按原格式返回XCODE_MODEL_UNAVAILABLE等可辨别code；metadata配置不能放宽计费乘数边界。三个协议的真实目录交给T12。

### T8：只读权益/套餐/配置与账号日志聚合

**Files:** `service/xcode_facade.go`、新增 `service/xcode_usage.go`、`model/log.go`、`controller/xcode.go`、`dto/xcode.go`、`router/api-router.go`；可新增一个 `controller/xcode_usage_test.go` 集中保护日志聚合契约，其他回归复用现有文件。

**Interfaces:** `GET /me,/plans,/entitlements,/usage,/client-config`；使用T5专用control+SID，不接受PAT替代。AuthorizationSnapshot按真实锁内DB时间执行到期/懒reset；AccountUsageSnapshot按user_id所有Consume/Refund日志，不按用户名/key/订阅过滤。

- [ ] 先构造同用户多key/多资金、另一用户、历史停用模型、Consume补扣和Refund、超安全整数fixture；精确验证记录数、prompt/completion、charged/refunded/net（含负净额）。关闭日志和真实DB失败必须503，不是零消费。
- [ ] 输出canonical字符串/毫秒/display/无限额度null规则；未指定、active、exhausted、expired、revoked从真实实例判定，查询失败不伪装unassigned；plans保留合规展示与manual_admin。
- [ ] 以IANA自然日构造UTC边界；today小时桶、其余日桶，用参数化GORM对固定边界汇总所有模型，不全量装载30天原始日志、不按模型N+1、不用MySQL日期函数。今天重复小时加offset标签；custom含首尾最多30自然日，非法日期/timezone400。
- [ ] 回归 DST春23小时/秋25小时、月底/闰日、消费与跨期退款边界；实际SQLite/MySQL/PostgreSQL及独立ClickHouse Log DB查询一致。返回source/as_of/unsupported_metrics及保留期口径，不承诺财务完整历史。

**验收:** 账号用量与指定额度范围明确分离；无IP/admin_info/channel secrets；所有控制响应no-store/schema_version/server_time。已有quota_data与SumUsedQuota(username)不能直接代替。

### T9：一次性 Web handoff 和原生只读套餐页面

**Files:** `service/xcode_auth.go`、`service/xcode_facade.go`、`controller/xcode.go`、`middleware/xcode.go`、`middleware/logger.go`、`router/web-router.go`、`router/api-router.go`；新增 `web/src/routes/xcode/plans.tsx`、`web/src/features/xcode/plans.tsx`。

**Interfaces:** `POST /web-handoffs`→同origin `/xcode/plans?handoff=...`；单次消费后303无query；`GET /api/xcode/v1/web/view`仅专用Cookie读plans/权益/display。

- [ ] 先补重复/过期handoff、另一user/SID、SID撤销、control/relay/Web互换和跨origin拒绝；handoff消费不签发dashboard bundle。
- [ ] Cookie独立aud/purpose、HttpOnly、SameSite=Strict、Path=/api/xcode/v1/web、无Domain；TTL≤15分钟且≤SID；loopback HTTP Secure=false例外仅限开发，HTTPS必须Secure。code日志脱敏、no-store、Referrer-Policy=no-referrer。
- [ ] 页面不进入native `_authenticated` guard，不触发 `web/src/lib/api.ts` 原生refresh或写native auth store；使用页面自己的只读请求调用，绝不把control/refresh/key传Renderer/localStorage。
- [ ] 实际浏览器打开handoff：地址去query、模板/额度/手工说明真实；复制Cookie调用原生支付/管理失败，撤销后view拒绝。复用现有布局、状态、金额组件，无第二后台。

**验收:** 不关闭原生购买；这是只读交接，不是长期query token或普通面板登录。

### T10：XCode 主身份、协议解析、安全恢复与generation

**Files（XCode根目录）:** 新增 `packages/shared/src/new-api.ts`、`packages/services/src/new-api/newApiClient.ts`、`packages/services/src/oauth/providers/newApiProviderAdapter.ts`；修改 `packages/shared/src/oauth.ts`、`packages/services/src/oauth/{oauth.ts,oauthService.ts,repo/oauthCredentialRepo.ts}`、`packages/desktop/src/main/desktopOAuthDeepLink.ts`、`packages/desktop/src/preload/oauthCallbackBridge.ts`、`packages/services/src/node.ts` 与已有配置入口。

**Interfaces:** NewApiClient只访问规范化NEW_API_BASE_URL；wire解析复用canonical字段，不拿zcodeJwtToken承载private secret。主Owner冻结 `{origin,userId,sid,authGeneration}`；官方pending另有purpose，不能覆盖主active provider。

- [ ] 先用可控promise屏障做A兑换晚于B、cancel晚回调、两窗口state/provider不匹配、并发refresh单飞、503与撤销不同状态的确定性回归；测试真实owner状态/安全库写入，不是mock返回值复制。
- [ ] Host生成PKCE/state，精确deep-link窗口分发；校验SessionBundle schema/identity/expiry，按 `new-api:<origin-key>:<user-id>:<sid>` 原子保存typed credentials。私有startOAuthWithPolling稳定拒绝，原官方方法保留。
- [ ] control过期最多一次正常refresh重试只读；503保留可恢复凭据，真实撤销清可执行权限/worker secret；取消或过时结果不发布，已获得SID立即尽力服务端撤销并记录真实失败，不能假称已撤销。
- [ ] 实际系统浏览器→本机Native登录注册→desktop回调→重启恢复→logout；Renderer/RPC只拿公开UserInfo/状态，原布局保留，安全库解密失败不降级官方/env key。

**验收:** 主New API身份不因官方登录变化；私有token绝不写全局zcodeJWT、URL、Renderer或用户provider配置。

### T11：独立官方云连接、MCP/闲时任务与分享

**Files:** 新增 `packages/services/src/official-cloud/{officialCloudConnection.ts,officialCloudConnectionService.ts}`；修改 `packages/services/src/node.ts`、`official-mcp/officialMcpCredentials.ts`、`session/offPeakRuntimeModel.ts`、`model-provider/offPeakModelSelectionView.ts`、`conversation-share/conversationShareHttpClient.ts`、`coding-plan-subscription/codingPlanSubscriptionService.ts`、OAuth Repo及对应shared/RPC注册。

**Interfaces:** `IOfficialCloudConnectionService`提供当前owner连接快照、现有官方OAuth登录/注销、真实entitlement/catalog刷新；复用BigModel/Z.ai adapters。owner含Native origin/user_id，官方family/account/credential generation；官方登录purpose=official-cloud。

- [ ] 先补A→B不可见、A重新登录可恢复、旧无owner全局凭据不认领、官方登录不改主UserInfo、迟到official结果被generation丢弃、普通私有model切换不影响off-peak目录。
- [ ] MCP credentials/selected-provider、offPeak credentials/model view、官方feature config全部从独立connection/官方目录取值；保留真实org/project、Plan、scope与二次credential fingerprint，不扫描普通Registry。
- [ ] ConversationShareHttpClient tokenProvider接当前owner官方连接；protected操作无连接authentication_required，public preview/continuation保留optional anonymous但不附Native token；能力/导入结果加owner+official generation栅栏。
- [ ] 用真实官方账号/Plan执行MCP、闲时任务及分享发布/匿名导入；观察官方origin/model/账务和主用户切换。不用mock宣称官方功能可用，缺真实权益列T14阻断项。

**验收:** private reason=off-peak拒绝；官方云登录入口保留，官方凭据从未投递到私有relay，私有key/JWT从未出站到官方。

### T12：三个managed anchors、完整能力快照与本机worker

**Files:** `packages/provider/src/{sources.ts,resolver.ts,registry-service.ts,account-provider-service.ts,account-provider-resolution.ts,account-provider-state.ts}` 与 `config/{provider-data-schema.ts,provider-config.ts,schema.ts,rule-data-schema.ts}`；`packages/provider-node/src/{provider-config-runtime.ts,zcode-builtin-provider-config-materializer.ts,model-selection-facade.ts}`；`packages/shared/src/{model-provider-types.ts,model-provider-family.ts,zcode-protocol/index.ts}`；`packages/services/src/model-provider/{providerRuntime.ts,accountProviderConnectionResolver.ts,accountProviderRequestAuthService.ts,accountRequestAuthService.ts}`、`zcode-agent/zcodeAgentService.ts`；`apps/zcode-cli/packages/bootstrap/src/{app/process-provider-registry-runtime.ts,zcode-protocol/account-provider-config.ts}`。

**Interfaces:** access=`new-api-account`、group=`new-api-family`；anchors固定 `account:new-api-anthropic-messages`、`account:new-api-openai-chat-completions`、`account:new-api-openai-responses`。AccountProviderConfigSnapshot新增同owner/revision的modelConfigs和catalogIssues；RPC/worker严格schema同步，不允许旧解析丢字段继续执行。

- [ ] 先补能力Host→wire→worker完整一致、CDN刷新不删anchors、CEL非法进入issues无可执行ref、personal/template不能放宽能力、A→B/撤销/Builtin mismatch期间旧ref不能执行。
- [ ] 本地维护的Built-in rules始终合并三个空私有anchors；目录按api_type只落一个transport；completeModelConfigDataSchema及受限compiler验证，不填官方GLM能力。metadata缺失项只进catalogIssues，不造model ref。
- [ ] Host与worker在matching revision发布列表/能力/权限；保持basedOnZCodeBuiltinRevision栅栏，授权撤销先fail-closed。请求期resolver再次核owner/SID；不能把receivedRevision当应用成功，观察实际worker请求能力与权限。
- [ ] 迁移所有access/family union消费者与UI只读issues：`useModelSelectionView`、`usePlanIdentitySnapshot`、`accountProviderAccess`、`codingPlanUsageSources`、`modelVisionBadge`、`ModelProviderSection/Detail`；旧个人/企业/Start官方选择即使同model ID也要求重选。保留历史ref显示与custom provider。
- [ ] 实际三个SDK流验证路径、tools、取消与metadata允许的vision/reasoning；私有secret仅走现有本机请求期传输。远程旧provisioning不包含新秘密，明确unsupported而不删原非私有功能。

**验收:** selector可见不等于可执行；实际本机worker运行真model，旧owner/旧选择无fallback。必须单独验证CLI/bootstrap，root typecheck不包含该worker。

### T13：套餐/usage/reset RPC、全部额度面与嵌入升级页

**Files:** `packages/services/src/coding-plan-subscription/{codingPlanSubscription.ts,codingPlanSubscriptionService.ts}`、`usage-stats/{usageStats.ts,usageStatsService.ts}`、`packages/shared/src/{usage-stats.ts,usage-quota.ts,zcode-protocol/index.ts}` 与现有套餐DTO；`packages/ui/src/settings/usage-stats/CodingPlanUsagePanel.tsx`、hooks与quota/reset presentation/coordinator；`settings/{CodingPlanUpgradeDialog.tsx,CodingPlanEmbeddedWebviewDialog.tsx,model-provider-section/codingPlanEmbeddedWebview.ts}`、`packages/desktop/src/preload/codingPlanWebview.ts`。

**Interfaces:** canonical reads投影private-user/manual_admin；六个支付读RPC统一 `SelfServiceReadResult<T>`；reset为 `mode=native-single-period|official-windows`；Native mark-read返回明确not-supported，不保留Promise<void>假成功。

- [ ] 先补有限/无限/无授予/耗尽/过期/撤销/503矩阵，decimal string精度、null unavailable、不反转used比例；账号图表包含多key资金，但额度卡只指定实例。 App Usage继续本机数据库，Native mcpQuota=null。
- [ ] 套餐个人/企业/Start统一只读原生语义；支付/reset写RPC稳定XCODE_SELF_SERVICE_DISABLED且不发HTTP。六个支付读私有not-supported，存活官方路径显式available；当前取证未发现UI订单/card轮询caller，不杜撰，实施引用查询覆盖所有真实调用者。
- [ ] reset hook/dialog/coordinator先判mode；Native不请求机会、mark-read、成功动画或轮询。改消费记录数而非调用次数，时长/工具/健康/解码等保留卡片显示暂不提供；单周期/服务端时区/真实nextResetTime，跨周期used只重读权威快照。
- [ ] 同步所有共享额度面，包括 `CodingPlanUsageRemainingPanel`、`WorkspaceSidebarFooterUsageSummary`、`CodingPlanContextUsage`、`ChatCodingPlanMcpUsageMeter`。按源区分官方MCP真实权益/连接操作，locale/i18n与布局保留。
- [ ] upgrade由Host请求handoff再打开精确同源页面，不注入官方localStorage脚本；WebView partition绑定owner generation，换号/退出关闭并清理，拒跨origin导航。实际检查主用量页、侧栏、输入栏、重置弹窗、升级页、模型设置及官方云入口。

**验收:** 无paid/空订单/免费机会假事实；已提供的真实空桶可零，未知指标不可零；自配Provider与历史会话可用，原生面板付款不受影响。

### T14：真实本机联调、三库升级验证与安全发布

**Files:** 使用两仓现有构建/启动入口；新增验收脚本仅为实际验证需要，不提交throwaway secret/debug产物。实施结果更新本计划执行记录和现有 `docs/authentication.md` 受影响契约，不另增插件文档或重复规范。

- [ ] 在隔离测试库运行本计划§4命令、迁移及账务矩阵；补齐fixture缺少的Plan/Session/预扣模型，MySQL/PostgreSQL DSN未设导致skip即“未验收”。每个数据库记录精确版本；三库升级用最新released schema、代表数据、连续两次启动。
- [ ] 重新取证当前3001容器、挂载、架构、binary SHA/build来源；构建新binary并用匹配架构在隔离数据运行，绑定loopback3001。先确保端口/数据安全，不覆盖用户业务data、不自动执行make reset-setup、compose down -v。已有dev compose为3000+PG15，并非设计3001载体。
- [ ] 启动真实desktop及本机worker；完成canonical §11和客户端§11全部场景，观察用户/SID、实例/资金记录、流/工具、用量、耗尽/撤销、UI与官方出站origin。仅status成功不证明新功能；使用可信构建来源而非image latest或空version。
- [ ] 单独验证入口关闭后托管拒绝/general排除仍存在；测试回滚流程：先撤SID/key、取消全部有效xcode实例，确认无旧binary可误用的专用预算再切旧版本；保留审计。无安全清理不可直接回滚未知scope的binary。
- [ ] 汇总已运行命令、真实数据库版本、每个验收证据和未通过项；清throwaway脚本与秘密。真实官方账户/Plan或三库/ClickHouse未提供时明确未验收，不宣称完整替代。

**验收:** 所有required场景有实际结果；没有编译scaffold、fallback、未迁移caller或仅mock smoke。

## 4. 验证命令与数据库门槛

下列是**未来实施命令**，本次文档编写未运行。新 `TestXCode...` 名称由对应任务创建或加入现有测试文件，并非声称当前已有。

```sh
# New API 根目录：聚焦回归及现有共享回归
GOWORK=off go test ./model ./service ./controller ./middleware ./router
GOWORK=off go test ./controller -run 'TestPreConsumePolicyDatabaseMatrix|TestModelManagementDatabaseMatrix|TestXCode' -count=1 -v
GOWORK=off go test ./service ./controller ./middleware -race -count=1
# 修改Go文件先gofmt；main依赖web/dist，先构建前端再编译binary
make build-web
GOWORK=off go build -o /tmp/new-api-xcode .
# 只有修改relaykit或其public接口时执行，不能用根build替代
# 在relaykit目录：GOWORK=off go build ./...
```

```sh
# New API web目录
bun run typecheck
bun run test
bun run lint
bun run format:check
bun run build:check
bun run i18n:sync
```

```sh
# XCode根目录
node scripts/check-workspace-freshness.mjs
pnpm typecheck
pnpm lint
pnpm fmt:check
pnpm architecture:check --changed
# apps/zcode-cli目录：worker不在root typecheck覆盖范围
pnpm typecheck
pnpm lint
pnpm build
# XCode根目录：已有隔离开发入口，NEW_API_BASE_URL需T10接入
NEW_API_BASE_URL=http://127.0.0.1:3001 mise run dev
```

XCode当前没有统一root/provider测试命令；确定性回归按实际可执行package入口接入，记录命令，禁止杜撰 `pnpm test` 已通过。`pnpm dev:desktop` 默认转prod，不用于本期隔离数据联调。Root desktop开发脚本会准备runtime并构建本机Agent，但仍须独立CLI检查。

| 必须验证的数据库 | 运行方式与fixture | 证据 |
| --- | --- | --- |
| SQLite | 临时文件库，独立连接并发fixture；复用迁移模式 | sqlite_version、新/旧数据升级两次、revision并发与跨代账务 |
| MySQL | `TEST_MYSQL_DSN`真实专用测试库；复用 `newAuditTestDatabase`/matrix fixture | `SELECT VERSION()`、锁行为、迁移索引唯一性、资金与日志聚合 |
| PostgreSQL | `TEST_POSTGRES_DSN`真实专用测试库；复用同fixture | `SELECT VERSION()`、锁行为、升级幂等、资金与日志聚合 |
| 独立 Log DB | `LOG_SQL_DSN`/已有数据库选择配置；包括真实ClickHouse | 主库/日志库分离且user_id聚合、Consume/Refund、失败503；现有DDL测试不算此验收 |

禁止拿 `model/task_cas_test.go` 的单连接TestMain证明并发；禁止让测试DSN指向业务库。如果改动依赖最低支持版本行为，还需MySQL5.7.8/PostgreSQL9.6验证，不用较新版本成功推断最低版本。

## 5. 安全控制与验收关联

已读取 [Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)、[OAuth2](https://cheatsheetseries.owasp.org/cheatsheets/OAuth2_Cheat_Sheet.html)、[CSRF](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html) guidance；[OWASP ASVS](https://owasp.org/www-project-application-security-verification-standard/) 当前稳定基线5.0.0。本计划不声明ASVS合规；实施交付按适用的认证、Session、授权与self-contained token章节记录精确版本/requirement IDs及结果，不凭记忆编编号。

| 控制 | 任务/必须拒绝路径 |
| --- | --- |
| 认证保证/限流/防枚举 | T5/T6/T10；复用已启用的原生注册/MFA/Passkey验证，不因客户端回调绕过。触及具体机制前读相应MFA/恢复 guidance |
| code绑定与重放 | T6/T10；S256、精确client/URI、state/provider/window/origin、来源SID/auth_version、短期code原子单次 |
| 最小权限与JWT验证 | T2/T5；签名算法/issuer/audience/use/exp/SID/scopes，root主体不扩大scoped凭据 |
| refresh/撤销 | T5/T6/T10；专用refresh不升级，轮换/replay、key删除、安全变更、晚回调SID清理 |
| CSRF/浏览器凭据 | T6/T9；complete只认Bearer且禁止Cookie兜底，同源浏览器写请求复用适用防护；handoff GET只消费已绑定短期code，不改变订阅，拒purpose/user/SID混用 |
| 秘密与Cookie | T9/T10/T13；Host安全库，Web只读HttpOnly/SameSite/Path/期限，不在URL/log/Web Storage/Renderer暴露长期秘密 |
| 跨用户与官方出站 | T10–T13；origin/user/SID/authGeneration/officialGeneration栅栏，private secret禁止发官方 |

Loopback HTTP是本期开发约束，不代表生产传输安全合规；不扩成远程例外。官方云真实账号仍按其原安全流程认证，New API套餐不授予官方权益。

## 6. 规范覆盖索引与完成记录

| 规范要求 | 实施位置 |
| --- | --- |
| canonical §1–3 基线/数据/缓存/锁/revision | T1–T4、T14 |
| canonical §4 专用身份/PKCE/刷新/用途不可升级 | T5–T6、T10 |
| canonical §5.1 授予/续期/换套餐/撤销/reset快照 | T3–T4 |
| canonical §5.2 两资金域/全部key入口/在途原实例 | T2–T3、T5 |
| canonical §5.3 最终周期/完整actual/幂等/清理 | T3、T13 |
| canonical §6 所有认证/读/管理/relay协议和错误 | T2、T4–T10、T12 |
| canonical §7 模型完整能力/目录失效 | T7、T12 |
| canonical §8 账号全部日志/退款/DST/不可提供指标 | T8、T13 |
| canonical §9 只读页面/handoff | T9、T13 |
| canonical §10–11 开关/回滚/真实完成门槛 | T14、§4 |
| client §3–4 主owner/凭据恢复/登录generation | T10–T11 |
| client §5 anchors/完整能力/worker/旧选择重选 | T12 |
| client §6–7 支付读写判别/null/单周期/reset/额度UI | T13 |
| client §8 官方MCP/闲时任务/分享独立owner | T11 |
| client §9–11 本机边界/治理/真实桌面验收 | T12–T14、§4 |

执行记录只填真实结果，不将下面的未执行状态改成通过，除非已经取得证据：

| 项目 | 当前状态 | 完成时必须附带 |
| --- | --- | --- |
| 计划与两仓源码路径/脚本取证 | 文档编写阶段核对 | 两spec、源码HEAD、计划覆盖与本地路径检查 |
| T1–T14代码实施 | T1/T2/T3 通过，P0 gate 关闭；T4 开始 | 仅 synthetic 验证凭据，P1 尚未签发真实客户端凭据 |
| 三库迁移/并发/隔离/跨周期 | T1 fresh/release 升级、T2 资金隔离/请求碰撞、T3 跨周期矩阵及真实 HTTP 通过 | SQLite 3.50.4 / MySQL 8.0.37 / PostgreSQL 16.14；不据此推断 desktop/官方云验收 |
| 独立Log DB/ClickHouse统计 | 未执行 | 实际实例版本和相同fixture聚合结果 |
| 真实desktop+worker+三协议 | 未执行 | stream/tool/取消/允许能力、真实路径和资金事实 |
| 真实官方MCP/闲时任务/分享 | 未执行 | 真实账号权益、origin/model/账务和owner隔离 |
| 全UI/秘密/撤销/入口关闭/安全回滚 | 未执行 | 实际交互与拒绝证据 |

首个执行单元是 **P0/T1**，不是先把主登录接上。P0全部通过前，不签发可执行客户端凭据。

### T1 实施记录（2026-10-08）

- 隔离测试容器：`new-api-xcode-test-mysql`、`new-api-xcode-test-pg`、`new-api-xcode-test-redis`；测试库与用户业务数据库分离，没有修改或重启用户服务。
- Go 检查使用已缓存 `golang:1.25.8`、`GOWORK=off`、宿主模块缓存及独立 build cache；没有改动模块 Go 基线或 ORM 版本。
- 聚焦 Token/Session 分类与三库持久化回归已通过；仅此结果不证明 release 升级。
- 实际临时程序调用生产 `model.InitDB()` 和 `model.InitLogDB()` 连续两次，并通过真实 Redis 检查缺 schema/purpose/SID 后身份保持、Session 撤销拒绝、批量预扣额度保持。SQLite/MySQL/PostgreSQL 三次运行均成功。
- 实际发现并修复旧 hash 修复时重复发放额度：DB 为 9、Redis 已预扣 7 尚未落库，删除 schema 后原实现恢复为 9 并错误允许再扣 3。Lua 修复现保留同一 token 的实时额度；同一 smoke 在三库均拒绝第二笔并保持 Redis 余额 2。永久回归扩展现有 `model/token_auto_groups_cache_test.go`。
- 代表性 release fixture 来源：[v1.0.0-rc.42](https://github.com/QuantumNous/new-api/tree/6370b29424168039e94d40d610191e7d2e65dbf4/model)，SHA `6370b29424168039e94d40d610191e7d2e65dbf4`。冻结五类受影响表的实际持久化字段/GORM tag，再执行两次生产启动迁移；不是完整 release 二进制启动。专用外部测试库只接受空 `xcode_upgrade`，不复用其他测试库。
- 独立安全审查确认同 ID 原子额度保留及 mutation fence；未发现额外 T1 缺陷。认证指导来源为本计划 §5 的 OWASP Authentication / Session Management，目的签发/refresh/admission 的完整限制仍由 T2/T5/T6 实现；不声明 ASVS 合规或 P0 完成。
- 完整 `go test ./model ./common -count=1` 已通过（model 18.011s / common 0.523s）；包括 fresh/release 三库矩阵与预扣后 cache 修复、批量落库回归。`service/middleware/router` 完整回归通过；controller 首次因三库 fixture 安全门拒绝 `host.docker.internal`，随后经容器内只绑定 loopback 的临时测试通道运行 `go test -p 1 ./controller -count=1` 通过（82.558s），未放宽安全门。最后 generation=1 的 fresh/release 三库矩阵单跑通过（6.165s），六种 case 无 skip。
- T2 补齐实际 HTTP 消费方证明：删除托管 key 的 schema/purpose/SID 后回源仍拒绝 `/v1/models` 与 `/api/log/token`，冷/热缓存均未降级。T1 第一项已完成；仍不开放客户端签发或宣称 P0 完成。

已执行的 Go 容器使用以下挂载与环境；数据库 DSN 均为独立测试容器：

```sh
docker run --rm \
  -v /Users/timo/ai/new-api:/src \
  -v /Users/timo/go/pkg/mod:/go/pkg/mod \
  -v new-api-xcode-go-build:/root/.cache/go-build -w /src \
  -e GOWORK=off -e GOPROXY=https://goproxy.cn,direct \
  -e 'GOSUMDB=sum.golang.org https://sum.golang.google.cn' \
  -e 'TEST_MYSQL_DSN=root:xcode-test@tcp(host.docker.internal:53306)/xcode_test?charset=utf8mb4&parseTime=True&loc=Local' \
  -e 'TEST_POSTGRES_DSN=postgresql://postgres:xcode-test@host.docker.internal:55432/xcode_test' \
  -e 'TEST_XCODE_UPGRADE_MYSQL_DSN=root:xcode-test@tcp(host.docker.internal:53306)/xcode_upgrade?charset=utf8mb4&parseTime=true' \
  -e 'TEST_XCODE_UPGRADE_POSTGRES_DSN=postgresql://postgres:xcode-test@host.docker.internal:55432/xcode_upgrade?sslmode=disable' \
  golang:1.25.8 go test ./model ./common -count=1
```

### T2 实施记录（2026-10-08）

- 共享 stored-purpose admission 覆盖现有所有 key dispatcher；三种精确 POST 才可进入，普通 readonly 的过期查询语义及独立 artifact capability 保留。托管 SID 校验同用户、active/到期/撤销/auth_version，不使用普通 refresh 的 SessionVersion 作为 key 撤销条件。
- `SubscriptionFundingTarget` clean cutover 完成，所有原调用者显式传资金域。general 含 NULL/空/`general`，排除 xcode/未知；managed 固定 admission 的实例/revision，事务再验证当前关系和实例，无钱包/其他实例回退。请求重放核对 user/scope/原实例。
- 永久回归使用现有 middleware/controller 文件；允许路由直接建立真实 BillingSession 并检查指定实例已用量与钱包未变，不锁定内部用户查询次数。原生四种 preference、overflow、无 general、耗尽、关系冲突及跨用户/域/实例重放通过真实 SQLite/MySQL/PostgreSQL matrix。
- 审查发现并实际复现数据库时钟失败仍进入 handler（200 而非503），现 managed 严格传播查询/时钟失败；native 保留原本机时间 fallback。事务时间查询复用 tx 连接，实际单连接库 paid HTTP 请求不死锁。对应失败前/成功后 regression 已保留。
- 实际 `controller.Relay` 回归发现 Claude 的429丢失 `XCODE_QUOTA_EXHAUSTED` code；host 仅对 `XCODE_` 错误扩展 Claude envelope，保留原生错误形状，未修改 relaykit。三协议实际 relay 拒绝矩阵均保留 canonical code，余额不变。
- 真实临时 Go 程序启动隔离 loopback HTTP server，使用生产 middleware/RelayInfo/BillingSession、真实 Redis DB14 和三种数据库：指定实例从10到70，钱包/general/其他专用/未知 scope 均不变；warm hash 缺三个字段仍拒绝；耗尽不回退；普通 key general 及钱包回退真实扣费；免费无授权请求未进入 handler；warm SID 撤销返回401；actual controller 三协议429保留 code。
- 两用户同 request_id 的同步插入 collision：三库最终仅一笔20，另一用户未扣费且后续重放按所有者拒绝。MySQL 实际1062、PostgreSQL 实际23505后在 rollback 完成后的新事务读原事实，返回 request conflict。SQLite 并发 writer 可返回原生 SQLITE_BUSY；观察到只提交一笔，重放拒绝，不把该结果描述为无冲突并行写保证。
- 完整 `go test -p 1 ./model ./common ./service ./middleware ./router -count=1` 通过（48.739s/2.017s/1.019s/0.908s/0.675s，含T1三库升级）；middleware/controller 聚焦 admission 与资金矩阵通过。新增实际三协议后的 `go test -p 1 ./controller -run 'Test(XCodeFundingPolicyDatabaseMatrix|PreConsumePolicyDatabaseMatrix)' -count=1` 通过（56.248s），外部 DSN 经仅 loopback 的原安全通道，未放宽 fixture 校验。
- 两个独立审查覆盖认证缓存/dispatcher 与资金事务；时钟发现修正后的 scoped review 无新增问题。参考 OWASP Authentication、Session Management 与 ASVS 5.0.0 V8.2.1–V8.2.3、V8.3.1–V8.3.2；不是整体合规认证。P0仍需T3，未签发真实客户端凭据，未做 desktop/upstream Agent 完整联调。
- 完整 controller 首次外部300s deadline 超时；原生 verbose240s诊断显示大量测试通过，全局期限在最后安全测试刚开始（0s）时触发，并非该测试卡死。未列为通过，T3集成后用标准 Go10m/package deadline 跑完整受影响包。


### T3 实施记录（2026-10-08）

- `FundingSource.Settle(actualQuota,totalReservedQuota)` 全调用者迁移完成；wallet/general内部计算原差额，xcode使用原实例请求事实。Reserve使用完整当期target，token总预留保留单调max；不足时仅撤回本操作generation/前后占用。专用Refund不再二次退extraReserved。
- managed自动/懒/管理员reset在原实例锁事务内清used并递增generation，使用immutable period/customSeconds。已cancelled/expired实例仍按最后实际generation完成旧请求，不创建新周期；未完成专用事实不被7天清理删除。
- 原生Refund消除record→instance/nested事务，改同tx实例→record；原生delta-only入口连delta0也拒绝专用/未知scope。无真正managed BillingSession的legacy fallback拒绝，无钱包/另一个实例回退。
- committed baseline `29116e6` 的隔离SQLite consumer实际预扣100→adminreset→Settle(100)：观察used=0、generation=1、record仍consumed、FinalQuota=0，失败断言复现原zero-delta账务缺口。当前永久三库回归同场景used/final=100、status=settled；actual150为150，重复/冲突实际值和退款terminal状态另有断言。
- `go test -p 1 ./model ./service ./controller -run 'Test(XCode|.*Subscription.*Reset|.*Subscription.*Ledger|Managed|PostConsumeQuota|Billing)' -count=1 -timeout=10m` 通过：model4.501s、service0.051s、controller29.997s。包含真实三库current reserve60/token仍预留100、old rollback/refund保留new40、token不足rollback、原A替换后结算、到期不增代、结算不足原子未完成事实、legacyzero拒绝及cleanup。
- 真实 `PostTextConsumeQuota` 流式usage150/预算120不能结算：三库consumer log quota=0、content标失败、admin_info.billing_settlement含actual150/stablecode/funding_applied=false；user/channel paid UsedQuota未增。token单独失败保留已经提交funding事实，不伪造资金退款。managed consume log不再显示跨reset推算的虚假余额。
- disposable Go程序使用生产InitDB和真实HTTP TokenAuth→GenRelayInfo→BillingSession，SQL pool=1；SQLite3.50.4/MySQL8.0.37/PG16.14均PASS：pre100→reset→reserve60→actual100，sameactual幂等、不同actual409；token remain900/used100、wallet900；跨代rollback/refund后new40仍40。只运行独立synthetic库与loopback server，不改用户业务服务。
- 两个独立scoped reviewer分别检查model锁/快照/算术与service/token/audit/nativepolicy，无新增发现；没有把只读review当运行验证。完整 `go test -p 1 ./model ./common ./service ./controller ./middleware ./router ./relay -count=1 -timeout=10m` 通过（64.385s/1.693s/1.223s/328.495s/1.192s/0.765s/0.265s），含三库资金/跨周期与fresh/release升级矩阵。controller真实总耗时328.495s解释原300s外部/240s全局deadline，不需生产修补或删除测试。P0验证完成；P1认证尚未开放。

