# 用户鉴权与登录会话

面板鉴权采用短期 Access Token、HttpOnly Refresh Cookie 与服务端登录会话控制面的组合。面板请求不再依赖 Gin session，也不再要求 `New-Api-User` 请求头。

## 鉴权模型

- Access Token 是有效期 15 分钟的 JWT，只保存在浏览器内存中，通过 `Authorization: Bearer <token>` 发送。
- Refresh Token 是随机不透明值，有效期最长 30 天。浏览器只通过 `HttpOnly`、`SameSite=Strict` Cookie 持有它；服务端仅保存 HMAC 摘要，并在每次刷新时轮换。
- `new_api_has_session` 是 Refresh Cookie 的会话提示，值恒为 `1`，`Path=/`、非 `HttpOnly`，与 Refresh Cookie 同时写入、同时清除、同一过期时间。它只声明"曾签发过 Refresh Cookie"，不含任何凭据，也不参与任何鉴权判定；伪造它唯一的效果是自费一次注定失败的 refresh。它存在的原因是 Refresh Cookie 被 `HttpOnly` 和 `Path=/api/user/auth` 双重限制，`/` 上的页面无法判断自己是否匿名，否则每次冷启动都要发一次注定 401 的 refresh，而该请求还会占用按 IP 计数的 `CriticalRateLimit` 配额。
- `user_sessions` 是登录会话控制面，记录设备、IP、登录方式、最后活跃时间、到期时间和撤销状态。数据库中的 Session 状态是最终权威；撤销传播速度取决于下文所述的 Redis 拓扑。
- 用户的密码、状态、角色或安全因子发生安全相关变化时，`auth_version` 会递增并使旧登录会话失效。订阅带来的分组升降级只刷新授权缓存，不会退出任何登录设备。
- Redis 缓存保存用户鉴权快照和登录会话快照。版本栅栏和撤销 tombstone 防止旧缓存重新授权；Session 快照使用跟随 `SYNC_FREQUENCY` 的短 TTL，缓存未命中或未启用 Redis 时回退到数据库校验。
- Token/Session 缓存必须携带明确的 `ClientPurpose` 与缓存 schema；缺少字段或目的未知时回源数据库，不把缓存零值当作原生凭据。旧数据库行的空目的仍保持原生语义，缓存分别规范为 `general`（API key）和 `dashboard`（面板 Session）。托管 key 的 `ClientSessionSID` 也必须实际存在且与目的匹配；缓存修复不得丢失该绑定。Token 旧 hash 修复在同一次 Lua 操作中保留同一 token 已原子预扣、尚未批量落库的额度，不能从旧数据库余额重新发放额度。
- 托管 `xcode` key 的用途由持久化字段决定，所有 key 分发入口共享精确 allowlist：只允许 `POST /v1/messages`、`POST /v1/chat/completions`、`POST /v1/responses`。models、readonly usage/log、旧 dashboard、compact、WebSocket GET、图片/视频/task/artifact 的 key 分支返回 `403 XCODE_RELAY_SCOPE_DENIED`；独立 route-bound artifact capability 保留原用途。
- 托管推理必须绑定同用户的活跃 `xcode` SID，校验到期、撤销和当前 `auth_version`；普通 refresh 引起的 SessionVersion 轮换不会撤销同 SID key。服务器从指定授权关系读取实例 ID/revision，客户端 header、body、名称及原生 billing preference 不能指定资金；免费模型也先验证有效授权。
- 托管资金只使用本次 admission 的指定实例，并在预扣事务重新验证关系、归属、scope、状态和期限；耗尽返回 `429 XCODE_QUOTA_EXHAUSTED`，不会使用钱包或其他订阅。原生资金查询仅使用空/`general` scope，专用 overflow=false 不影响原生回退。托管数据库查询或时钟失败返回 `503 XCODE_BACKEND_UNAVAILABLE`，不以无套餐或本机时间继续授权。

`SESSION_SECRET` 用于派生面板 Access Token、XCode control/Web JWT、Security Proof、Refresh Token 摘要和 AuthFlow 摘要的不同用途密钥。生产环境及多节点部署必须在所有节点配置相同的高强度随机值；更换该值会使现有登录、临时鉴权流程和 Security Proof 全部失效。

## XCode 指定授权管理

- 原生管理员（不要求 root）使用 `GET/POST /api/xcode/v1/admin/users/:id/authorization`；PAT 另外要求 `billing:read` / `billing:write`，不新增 billing Casbin 权限。控制响应 `no-store`，ID、revision、额度为十进制字符串，时间为 Unix 毫秒；无上限额度的 total/remaining/percentage 为 null。
- 写入携 `action`、`expected_revision`，grant/renew/replace 另携 `plan_id`；reset 必须显式携 `advance_reset_time`。用户→关系→实例按顺序加锁，过期任务及原生失效/删除入口也先锁用户。并发旧 revision 返回真实 HTTP 409；查询或提交后缓存刷新失败返回 503，不能自动重放写入，重新读取授权和原生实例列表。
- 开通要求已有合规声明确认、启用模板、`MaxPurchasePerUser=0`；只创建 `admin/xcode` 实例，不扣钱包、不创建支付订单。普通绑定和原生订单仍为 general，不会自动指定。group 升降级影响整个账号，不能理解为仅对 XCode 生效。
- 有效续期只顺延原实例寿命，保留预算、已用量与周期快照；已到期续期新建，取消后只能重新 grant。replace 取消旧实例并新建，不结转额度；专用 reset 只改指定实例，原生批量 reset 仍作用于同用户/模板的全部有效实例。取消仍有效实例前先持久化已到期的周期重置，关闭后旧请求只使用最后实际 generation 结算。
- 原生失效维护指定关系 revision；有关系、预扣事实或在途任务引用的专用实例不可硬删除，返回 409 并引导取消。提交后的缓存失败不回滚数据库事实，审计记录真实已提交变更；客户端读取后决定下一次操作，不把 503 当作可以重放原 action 的许可。

## XCode 凭据与 Session 生命周期

- 面板 Session 目的为 `dashboard`，数据库旧空值仍按面板处理；专用 Session 必须显式为 `xcode`。原生密码、安全 proof、登录 refresh/logout 和 Session 内的账户安全写入不接受专用或未知目的。
- control JWT 使用独立签名用途 `xcode_access`、单一 audience `new-api-xcode`，固定只读 scopes 为 `profile:read`、`wallet:read`、`usage:read`。Web JWT 使用另一签名用途 `xcode_web` 和 audience `new-api-xcode-web`，不能代替 control 或面板凭据。JWT 到期不晚于关联 SID；实时原生面板 bundle 同样受 SID 期限限制。
- control 仅允许精确的只读控制路由及专用 handoff/logout，不因用户是 root 获得原生支付、key、PAT 或账户安全权限。原生路由拒绝专用 JWT；带专用 audience/use 的坏 JWT（包括错误 issuer）仍走内部 JWT 验证，不回退尝试 PAT 或 relay key。
- 专用 refresh 检查同用户、活跃 SID、目的、`auth_version` 和关联托管 key，复用原有轮换、重放及 race recovery。正常轮换只改变 SessionVersion，返回原 key；缺失、已删除、停用、过期或绑定错误均拒绝，不创建替代 key。
- 受管 key 创建要求同用户有效专用 SID、有限期限且不晚于 SID、启用状态、无限 key 额度和固定路由策略；同一 SID 的已删除历史 key 也阻止重建。`RemainQuota=0` 只约束初次创建，不能把原生无限 key 的正常用量计数误判为失效。名称修改只写名称/状态，不覆盖并发更新的额度计数或路由字段。
- key 更新/删除按数据库中保存的目的和 SID 分类，不信任请求中的改名、伪造 purpose 或绑定。停用、单删、批删先撤销关联 SID；撤销栅栏失败时不修改 key。key 缓存清理失败不能恢复已撤销 SID，重新启用已撤销 key 也不能恢复登录。
- 原生设备撤销、revoke-others/revoke-all、密码及安全因子变更均约束专用 SID。专用退出只撤销该 SID，保留独立原生浏览器登录；revoke-all 则按原有语义退出所有设备。即时传播及失败窗口仍受下文原生 Redis 拓扑保证约束，不承诺额外的全局缓存一致性。

## 多节点 Redis 拓扑

多节点部署必须共用同一主数据库。登录 Session、账户级活跃 Session 上限和签发窗口计数都以数据库为权威，因此这些限制在应用节点间全局生效。Redis 中的 Session Hash（包含 `revoking`/`revoked` tombstone）只是缓存，其 TTL 为 Session 剩余寿命与有效 `SYNC_FREQUENCY` 中的较小值；`SYNC_FREQUENCY` 默认及非法值回退均为 `60` 秒。读取缓存不会续期，过期后会按 SID 回源数据库。延迟完成的 active 缓存回写只能使用其数据库观察窗口尚未消耗的 TTL，不能在撤销 tombstone 到期后重新启动一个完整缓存周期。

| Redis 部署方式 | Session 状态传播 | 限流语义 |
| --- | --- | --- |
| 所有节点共享 Redis | 正常撤销和版本发布通过同一缓存即时传播 | Redis 限流额度在所有节点间共享 |
| 每个节点使用独立 Redis | 最迟在该节点 Session 缓存 TTL 到期后回源收敛，即不超过有效 `SYNC_FREQUENCY`；版本轮换期间，新 Token 在持有旧缓存的节点上可能短暂返回 401 | 每个节点独立计数，集群总额度最坏约为单节点阈值乘以节点数 |
| 不使用 Redis | 每次 Session 校验直接读取数据库 | 使用各节点的内存限流器，额度同样按节点独立 |

`SYNC_FREQUENCY` 越大，独立 Redis 部署的陈旧窗口越长；值越小，每个活跃 SID 在每个节点上回源数据库的频率越高。默认配置下，持续活跃的 Session 每个节点最多约每 60 秒增加一次数据库主键点查。共享 Redis 时，撤销 tombstone 和版本发布仍保持即时传播。

所有节点必须使用相同的 `SESSION_SECRET`。当多个节点连接同一个 Redis 时，还必须使用相同的 `CRYPTO_SECRET`，否则节点生成的缓存键摘要不一致，无法正确共享缓存。上述保证只覆盖登录 Session 鉴权的有界陈旧语义；限流额度及其他 Redis 缓存仍会受到 Redis 拓扑影响，不能据此认为整个控制面与拓扑无关。

## 浏览器接口

登录成功后，密码登录、2FA、Passkey、OAuth、WeChat 和 Telegram 登录均返回统一数据：

```json
{
  "success": true,
  "data": {
    "access_token": "...",
    "token_type": "Bearer",
    "access_expires_at": 1730000000,
    "user": {},
    "session": {
      "sid": "...",
      "current": true,
      "login_method": "password",
      "ip": "...",
      "user_agent": "...",
      "created_at": 1730000000,
      "last_active_at": 1730000000,
      "expires_at": 1732592000
    }
  }
}
```

会话相关接口：

| 接口 | 鉴权 | 用途 |
| --- | --- | --- |
| `POST /api/user/auth/refresh` | Refresh Cookie；Secure 模式附加 Origin 校验 | 轮换 Refresh Token 并签发新的 Access Token |
| `POST /api/user/auth/logout` | Refresh Cookie；Secure 模式附加 Origin 校验，可同时携带 Bearer | 撤销当前登录会话并清除 Cookie |
| `GET /api/user/sessions` | Bearer | 查看当前鉴权版本的有效登录会话，当前会话优先，最多 100 条 |
| `DELETE /api/user/sessions/:sid` | Bearer | 撤销指定登录会话，包括当前会话 |
| `POST /api/user/sessions/revoke-others` | Bearer | 保留当前会话并撤销其他会话 |

客户端内存中已有会话时，应在 refresh/logout 请求中发送 `X-Auth-Session: <sid>`。Refresh Cookie 与该 SID 不一致时，两个端点都返回 `409 AUTH_SESSION_MISMATCH`，且不会轮换、撤销或清除任何会话；客户端先通过 refresh 清除本标签页的旧 SID、恢复 Cookie 当前对应的会话，再重试 logout。冷启动尚无内存会话时可以省略该请求头。

并发使用同一个 Refresh Token 时，服务端通过确定性轮换恢复同一个后继 Token，多个浏览器标签页不会因丢失“胜者”响应而被迫退出。最近一代 Refresh Token 在短暂容错窗口结束后再次出现会撤销对应会话；无法识别的更早代或随机 Token 只会被拒绝，不会允许攻击者凭猜测踢掉会话。

前端使用 Web Locks 串行化同一浏览器配置文件中的刷新，并通过 BroadcastChannel（不支持时回退到 `storage` 事件）仅同步会话标识和登录/退出事件；Access Token 与 Refresh Token 都不会通过跨标签页消息传递或持久化到 Web Storage。

前端将冷启动状态与登录状态分开管理。网络或服务端临时故障允许后续导航重试 refresh；服务端确认 Refresh Cookie 无效时才进入已完成的匿名状态。内存 SID 与 Cookie SID 不一致时，客户端清除旧内存身份并在不携带旧 SID 的情况下重试一次。

公开页面的冷启动会先读 `new_api_has_session`：提示不存在且内存中没有任何身份时跳过 refresh，直接按匿名渲染，且**不**把这次跳过记为已完成的匿名判定——跳过只是延后，不是服务端结论。会依据鉴权结果做跳转的位置（受保护路由与登录页）不看提示，内存为空时一律回源。因此提示缺失但 Refresh Cookie 有效的用户（该 Cookie 上线前建立的会话，或只清理了 `/` 站点数据的浏览器）会在公开页显示为匿名，并在进入上述任一位置时自动恢复登录态，不需要重新输入密码。提示因服务端撤销而过期时，那次 refresh 返回 401 并在同一响应里清除提示，浪费的请求只发生一次。

## Session 签发限额与保留策略

服务端在所有登录方式的统一 Session 签发出口执行两级账户限制：

- `USER_SESSION_ACTIVE_LIMIT`（默认 `50`）：单用户未过期且状态为 active 的 Session 上限。达到上限时新登录返回 `409 AUTH_SESSION_LIMIT`。
- `USER_SESSION_ISSUANCE_LIMIT`（默认 `100`）和 `USER_SESSION_ISSUANCE_WINDOW_SECONDS`（默认 `86400`）：统计窗口内该用户创建的所有 Session，包含已撤销和旧鉴权版本的记录。达到上限时返回 `429 AUTH_SESSION_ISSUANCE_LIMIT`。
- Session/key 签发先锁定所有者，再在同一事务中检查共享限额并插入；原生与 XCode 签发使用同一所有者锁。计数或锁失败会拒绝签发，不会降级放行。

升级时已经超过活跃上限的账户不会被自动下线或挤掉旧会话；限制只作用于后续的新 Session 签发。

`USER_SESSION_REVOKED_RETENTION_DAYS`（默认 `7`）控制 revoked 行的审计保留期。签发计数依赖窗口内的行仍存在，因此签发窗口不得超过 revoked 保留期。如果配置超出，启动时会记录告警并将实际窗口钳制到保留期，避免提前删除 revoked 行导致限流计数被低估。

定时清理即使发现 `expires_at` 已过期，也不会删除 `created_at` 仍落在实际签发窗口内的行；尚未达到 revoked 保留期的撤销记录同样会继续保留。这样在扩大配置窗口时，过期清理不会静默削弱签发计数或审计保留。

活跃数量会计入状态仍为 active 但 `user_auth_version` 已过期的异常残留行，而设备列表只展示当前鉴权版本。因此遇到 `AUTH_SESSION_LIMIT` 时，应优先在仍已登录的设备上执行“撤销其他会话”，该操作会同时清理不可见的旧版本 active 行；没有可用设备时可使用密码重置撤销所有会话。密码重置不会清空签发窗口计数。

仅 master 节点每小时分批删除过期 Session 和超过保留期的 revoked Session。`USER_SESSION_HOURLY_ALERT_THRESHOLD`（默认 `5000`）只在最近一小时全局签发量异常时记录告警，不会形成可被滥用的全站登录拒绝开关。

## Refresh/Logout 的 Origin 校验

refresh/logout 的 Origin 防护与 Refresh Cookie 的 Secure 模式绑定：

- 未配置 `SESSION_COOKIE_SECURE` 或显式设为 `false` 时，Refresh Cookie 可用于本地 HTTP，refresh/logout 的 OriginGuard 关闭，并且不得配置 `SESSION_COOKIE_TRUSTED_URL`。这使 `http://localhost` 上不同端口的 Rsbuild/Vite 开发代理可以正常转发请求。该模式仅用于可信的本地开发环境，不应暴露到公网。
- `SESSION_COOKIE_SECURE=true` 时，Refresh Cookie 仅通过 HTTPS 发送，同时启用严格 OriginGuard。`POST /api/user/auth/refresh` 和 `POST /api/user/auth/logout` 会校验浏览器的 `Origin`；缺少 `Origin` 时只接受合法的单一 `Referer` 作为回退。允许来源包括请求自身的精确 Origin，以及 `SESSION_COOKIE_TRUSTED_URL` 中配置的精确 Origin。

Secure 模式的 Origin 校验不信任客户端直接发送的 `X-Forwarded-Proto`。TLS 在反向代理终止时，应将面板的公开 HTTPS Origin 明确写入 `SESSION_COOKIE_TRUSTED_URL`。

`SESSION_COOKIE_TRUSTED_URL` 现在具有明确的新语义：它是 refresh/logout Cookie 端点的可信 Origin 列表，不是 CORS 白名单。配置规则如下：

- 仅在 `SESSION_COOKIE_SECURE=true` 时配置；多个值用英文逗号分隔。
- 每项必须是精确的 HTTPS Origin，例如 `https://panel.example.com` 或 `https://panel.example.com:8443`。
- 不接受通配符、路径、查询参数、用户信息或域名后缀匹配。
- 不会修改 relay、旧 billing dashboard、`/api/usage/token` 或 `/api/log/token` 的 CORS 行为。浏览器使用 `sk-` key 直连 relay 的场景保持不变。

本地 HTTP 开发示例（OriginGuard 关闭）：

```env
SESSION_SECRET=<local-random-value>
SESSION_COOKIE_SECURE=false
# SESSION_COOKIE_TRUSTED_URL 不得设置
```

生产 HTTPS 示例（OriginGuard 开启）：

```env
SESSION_SECRET=<high-entropy-random-value>
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_TRUSTED_URL=https://panel.example.com,https://admin.example.com
```

该开关只控制面板 Refresh Cookie 和 refresh/logout 的 OriginGuard，不会修改 relay、旧 billing dashboard、`/api/usage/token` 或 `/api/log/token` 的 CORS 行为。

## 可信代理与 IP 限流

Gin 默认会信任所有代理提供的客户端 IP 请求头。本项目改为兼顾常见反代拓扑和公网直连安全的三态配置：

- 未配置、空字符串或纯空白的 `TRUSTED_PROXIES` 默认信任 `127.0.0.0/8`、`::1`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16` 和 `fc00::/7`，并输出启动告警。该默认值覆盖同机 Nginx、Docker Compose 和常见内网反代；公网直连地址不在列表中，其伪造的 `X-Forwarded-For` 会被忽略。
- `TRUSTED_PROXIES=none`（大小写不敏感且必须单独使用）启用严格直连模式，不信任任何代理，`ClientIP()` 只使用 TCP 直连地址。
- 其他非空值按英文逗号解析为代理 IP/CIDR，并完全替代默认列表。应填写反向代理自身的地址而不是客户端网段；非法 CIDR、空列表或将 `none` 与其他值混用都会阻止服务启动。

Gin 只在请求的直连来源属于可信代理时解析客户端 IP 请求头，并从转发链右侧向左寻找首个非可信地址。因此常见 Nginx `$proxy_add_x_forwarded_for` 链中的公网客户端地址会阻止更左侧的伪造前缀生效。默认信任私网的残余风险是：能够从同一私网直接访问应用的其他机器或容器仍可伪造这些请求头；需要消除此风险时应使用 `none` 或配置精确代理地址。

Redis 限流使用原子 Lua 固定窗口，替代旧的近似滑动窗口 List 实现。这是有意的语义变化：窗口边界两侧可分别打满一次，极短时间内通过量最高约为配置值的两倍。例如 `20 次/20 分钟` 在边界可通过约 40 次。帐户级 Session 上限和签发窗口继续控制数据库增长；如未来需要严格抑制边界突发，需单独迁移为 ZSET 滑动窗口。

用户级模型成功请求限流仍使用原有 Redis List 近似滑动窗口，但列表时间戳统一写为 UTC。滚动升级期间，旧节点写入的本地时间字符串和新节点写入的 UTC 字符串无法从格式上区分，可能在一个模型限流窗口内临时误放行或误拒绝。所有节点升级完成并经过一个完整窗口后会自然收敛；本次升级不会切换 Key 或主动删除现有列表。

开放注册仍会受 Critical IP 限流保护，但分布式 IP 多账号攻击不能仅靠 IP 限流阻止。公网开放注册的部署应同时启用 Turnstile 和邮箱验证；更强的设备或多维风控需作为独立安全项目设计。

## PAT 调用契约

面板 PAT（个人访问令牌）通过 `Authorization: Bearer <pat>` 发送，也兼容单值 `Authorization: <pat>`；不接受 URL 参数。`New-Api-User` 不参与鉴权。

- 令牌格式为 `nap_` 加 43 位 base62 随机串（约 256 bit）。明文只在创建时返回一次，数据库只保存 SHA-256 摘要，该摘要同时作为审计记录中的 `token_ref`。
- 每个用户最多 20 个令牌，存放在 `user_access_tokens` 表。创建时必须选择 scope 和过期时间：默认 30 天，最短 1 小时，也允许永不过期（界面会提示风险）。过期令牌立即失效，超过保留期后由 master 节点清理。
- scope 形如 `resource:action`。个人权限（资料、API 令牌、用量、钱包、账户安全）和部分后台权限由令牌目录定义；受 Casbin 保护的后台权限直接复用管理员权限矩阵的 resource 与 action。只能授予自己当前拥有的权限，并且每个请求都会重新读取角色和权限：撤回某项管理员权限后，携带该 scope 的令牌立即失去对应能力。
- 每条面板路由都必须声明所需 scope。未声明的路由一律拒绝（403 `ACCESS_TOKEN_ROUTE_UNDECLARED`），scope 不足返回 403 `ACCESS_TOKEN_SCOPE_DENIED`，令牌过期返回 401 `ACCESS_TOKEN_EXPIRED`。
- PAT 不能创建、改名或撤销 PAT，也不能管理登录会话；这些接口只接受浏览器登录会话。
- PAT 可以完成二次验证，Proof 绑定到该令牌，并且令牌必须持有该操作对应的 scope：`channel.key.read` 需要 `channel:secret_view`，`admin.user.*` 需要 `user:write`，本人的密码、2FA、Passkey、账号绑定和注销需要 `account_security:write`。PAT 不能使用 OAuth 验证方式。
- 通过 PAT 完成的安全变更（如修改密码、2FA、Passkey）会让该用户的所有浏览器登录会话下线，令牌本身保持可用。
- 用户被禁用期间，其令牌全部不可用，重新启用后恢复；注销或删除用户时，其令牌被删除。
- 最后使用时间和 IP 每个令牌每分钟最多更新一次；更新失败不影响请求。

## 临时鉴权流程与二次验证

OAuth state、2FA pending、Passkey ceremony、Telegram bind 等临时状态存放在 `auth_flows`。客户端只持有随机 `flow_token`，数据库仅保存 HMAC 摘要；流程具有用途、provider、intent、用户和登录会话绑定，并且只能原子消费一次。OAuth 注册的 affiliate code 也随登录 AuthFlow 保存。

标准 OAuth 绑定回调由 popup 通过同源 `postMessage` 交给 opener；只有 opener 使用自身内存中的 Bearer 调用后端绑定接口。Telegram 绑定先由已登录前端创建绑定 AuthFlow，再让 widget 回调携带路径中的 `flow_token`，回调时会重新确认原登录会话仍有效。Telegram 的已签名 widget assertion 也会登记为一次性凭据，重复回放会被拒绝。

敏感操作使用有效期 1 分钟的 `X-Security-Proof`：

- `channel.key.read`：查看渠道密钥；
- `passkey.register`：注册 Passkey；
- `passkey.delete`：删除 Passkey。

管理员对其他用户执行的高风险操作同样要求 Proof，且 scope 的 context 绑定被操作用户，签发给某个用户的 Proof 不能改用于另一个用户或另一种动作：

- `admin.user.delete`（`{"user_id"}`）：`DELETE /api/user/:id` 与 `POST /api/user/manage` 的 `delete`；
- `admin.user.manage`（`{"user_id","action"}`，action 为 `disable` / `enable` / `promote` / `demote`）：`POST /api/user/manage`；额度调整 `add_quota` 不要求 Proof；
- `admin.user.update`（`{"user_id"}`）：`PUT /api/user/` 在请求包含新密码或 `admin_permissions` 时要求；仅修改显示名、分组、备注不要求；
- `admin.user.create`（`{"role"}`）：`POST /api/user/` 创建管理员角色时要求；创建普通用户不要求；
- `admin.user.passkey.reset`（`{"user_id"}`）：`DELETE /api/user/:id/reset_passkey`；
- `admin.user.2fa.disable`（`{"user_id"}`）：`DELETE /api/user/:id/2fa`；
- `admin.user.binding.clear`（`{"user_id","binding_type"}` 或 `{"user_id","provider_id"}`）：`DELETE /api/user/:id/bindings/:binding_type` 与 `DELETE /api/user/:id/oauth/bindings/:provider_id`。

这些 `admin.user.*` scope 只对管理员及以上角色签发；已启用 2FA 或 Passkey 的管理员必须使用其中之一，未启用时回退到密码（或已绑定的 OAuth）重新认证；密码登录被关闭时不接受密码验证。通过 PAT 调用上述接口时，令牌需持有 `user:write` 并完成二次验证；旧版令牌不能调用。

Proof 同时绑定用户、登录会话、用户鉴权版本、会话版本和 scope，不能跨用户、跨会话或跨用途复用。对 PAT 而言，Proof 绑定的“登录会话”就是令牌本身。

启用了 2FA 的用户注册 Passkey 时，register begin 与 finish 都必须携带有效的 `passkey.register` Proof；finish 会在消费一次性 AuthFlow 之前重新验证 Proof。未启用 2FA 的首次 Passkey 注册不要求该请求头。

## 微信网站应用登录（wechat-open）

网站微信登录独立于公众号验证码登录：`wechat-open` 使用微信开放平台的网站应用与 `snsapi_login`，旧 `/api/oauth/wechat` 和 `users.wechat_id` 保持原义。网站身份保存在可空、唯一且区分大小写的 `users.wechat_open_id`，值为经服务端校验的 `appid:openid`；未绑定为 SQL NULL，不根据昵称、头像、邮箱、手机号或 UnionID 合并账号。

管理员在认证设置中配置以下字段，入口默认关闭：

| 配置                    | 约定                                                               |
| ----------------------- | ------------------------------------------------------------------ |
| `WeChatOpenEnabled`     | 启用前校验整组配置；关闭时保留已有绑定                             |
| `WeChatOpenAppID`       | 已审核的网站应用 AppID                                             |
| `WeChatOpenAppSecret`   | 仅服务端持有；输入留空保留原值，不明文回显                         |
| `WeChatOpenCallbackURL` | 精确的 HTTPS 回调：公开站点 origin 下的 `/oauth/wechat-open`       |
| `ServerAddress`         | 与上述回调一致的 HTTPS 公开 origin；不得使用用户提交的任意回调替换 |

修改公开域名或整组凭据时，先关闭网站微信入口，更新配置后再启用。Secret、微信 access/refresh token 不返回浏览器或桌面，也不写入日志。`GET /api/status` 只公开 `wechat_open.enabled/app_id/redirect_uri`；配置关闭或无效时 `enabled=false`。微信 token 与 userinfo 请求固定使用官方 HTTPS 端点，不能配置代理上游地址。

浏览器仅在用户同意已启用的协议并主动开始后创建 state、加载官方 `WxLogin`。宽屏采用账号/微信双栏，窄屏可切换；手机保留账号登录并提示到电脑扫码。官方 iframe 保留默认的电脑微信快捷登录能力，是否可用由微信客户端、应用权限和浏览器决定。`onReady` / `onQRcodeReady` 只表示组件就绪；取消、重开或主题切换会调用 SDK 回写到参数对象的 `onCleanup`，释放窗口消息监听。

`POST /api/oauth/state` 使用 `provider="wechat-open"` 与原生 `login/bind/verify` intent，同时必须携带 `X-OAuth-Browser-Proof`：当前标签生成的 32 字节随机值的 base64url 编码。服务端 AuthFlow 仅保存其 SHA-256 与应用配置指纹，期限仍为 10 分钟；兑换前及上游响应后重新校验证明、配置和 intent。`GET /api/oauth/wechat-open` 必须携同一证明，缺失、错误、过期、已消费或旧配置均不签发会话。

登录 attempt 保存在本标签的 sessionStorage，绑定当前用户/SID、期限和安全站内返回目标。账户或 SID 变化、取消、重开和终态结果会清理旧证明；复制 callback 到其他标签不能登录。绑定/验证 popup 只经精确 opener/source/origin 校验转交 code/state，由发起设置页携证明兑换。无 sessionStorage 或 Web Locks 时网站微信明确不可用，账号登录仍可使用。

所有会写认证 Cookie 的原生登录、二次验证、Passkey 登录、OAuth 兑换及退出请求，在统一 Axios adapter 内共用 `new-api:auth-refresh` Web Lock，等完整响应 headers/body 收到后释放，并在入网前复核用户/SID 与单次请求归属。刷新继续使用原有独立客户端和同名锁，避免重复取锁；只读密码公钥请求不占用此锁。只忽略迟到 body 不能阻止 HttpOnly Cookie 写入。

已有绑定登录执行原有禁用、软删除及 2FA/Passkey challenge 规则。新身份仅在 `RegisterEnabled=true` 时事务创建普通用户及唯一绑定；密码注册开关不代替总注册开关。同身份并发创建的失败者回滚并重读获胜者，不留下孤立用户，也不重复发放既有注册权益。登录不直接授予 XWork 套餐。

自服务绑定要求 `account.binding.bind` Proof 与原 SID；不覆盖当前或其他账户已有绑定，添加绑定不额外提升认证版本。自服务解绑为 `DELETE /api/user/oauth/bindings/wechat-open`，携 `account.binding.unbind` Proof 与 `{"provider":"wechat-open"}` context；执行最后可用认证方式保护，提升认证版本并轮换保留的当前会话，前端立即应用响应中的新 Access Token。管理员从用户绑定管理使用既有 `admin.user.binding.clear` 与 `binding_type="wechat-open"`，沿用管理员清除规则及全会话撤销。

XWork 仍使用 `provider=new-api`、`client_id=xcode-desktop`、PKCE S256 和 `zcode://oauth/callback`。微信、注册及必要二次验证往返保留原 `/xcode/authorize?flow_token=...`，不延长原请求，也不自动授权；用户须明确确认当前真实账号。

安全控制依据：[OWASP Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)、[OAuth 2.0](https://cheatsheetseries.owasp.org/cheatsheets/OAuth2_Cheat_Sheet.html) 与 [CSRF Prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)；SDK 生命周期以[官方当前脚本](https://res.wx.qq.com/connect/zh_CN/htmledition/js/wxLogin.js)为依据。受控上游与隔离数据库/浏览器验证不能代替已审核应用、登记 HTTPS 域名下的真实微信扫码与真实 Host 验收。

## XCode Authorization Code + PKCE

新设备授权入口默认关闭。启用时必须同时设置 `XCODE_INTEGRATION_ENABLED=true` 和 `XCODE_PUBLIC_ORIGIN`；本源只接受 HTTPS，开发环境允许 HTTP loopback，不从请求 Host、转发头或面板地址推导。

- 固定客户端为 `xcode-desktop`，精确回调为 `zcode://oauth/callback`，仅接受 `response_type=code` 和 PKCE `S256`。Host 必须生成随机 state；服务端校验其字符与长度，不把长度视为随机性证明。
- `/api/xcode/v1/auth/authorize` 创建 10 分钟请求并跳转本源授权页。内部 `/auth/authorize/request` 仅返回客户端和期限；请求失效时不会引导登录。
- 授权页复用原生登录、注册和已启用的安全验证。`/auth/authorize/complete` 仅接受有效原生浏览器 Bearer，不接受 PAT、托管 key 或专用 control。用户明确确认实际账号、权限及设备期限后，页面校验 JSON `redirect_url`，仅分发含 `provider/code/state` 的精确回调。
- code 有效期 60 秒。`/auth/token` 在同一事务中消费 code、创建独立 XCode SID、创建同 SID 托管 key；共享原生 Session/key 限额，失败整体回滚，不消费 code。原生与专用签发都先锁所有者，避免 MySQL repeatable-read 的过期限额快照。
- 正确 client、URI、verifier 的 code 重放返回 invalid，并撤销该 code 记录的已签发专用 SID/key；并发重放中来源 SID 后续失效也不绕过撤销。错误证明不得触发撤销。
- SessionBundle 只给 Host 窄用户身份、专用 control/refresh 和 `sk-` 托管 key；不替换原生 Cookie。`/auth/refresh` 复用已有 SID/key；`/auth/logout` 要求有效专用 control 或匹配 client/SID 的 refresh，不接受裸 SID。关闭新授权入口后 refresh/logout 与目的隔离继续生效。
- auth API 响应为 no-store/no-referrer；限流、依赖失败及超大匿名请求保留真实 HTTP 状态和稳定 `code/request_id`。授权和原生认证页面禁止 framing；SPA 内嵌路径也不渲染凭据或确认操作。原生尾斜杠 canonical redirect 保留，最终认证文档仍有安全头。

### Desktop 主身份与独立官方连接

- 客户端仅在显式设置 `NEW_API_BASE_URL` 时启用 New API 主登录；未设置时保留原官方主登录，不默认连接 localhost。Host 使用规范化的精确 origin，绑定 `{origin,userId,sid,authGeneration}`；官方云连接另有 owner/purpose，不能覆盖主身份。
- Desktop 私有凭据复用原安全库，由 macOS/Windows 等平台的 `safeStorage` 保护随机加密主密钥；安全库不可用、解密失败或 Linux `basic_text` 时明确要求恢复，不降级到环境变量或旧派生密钥。Renderer 只接收公开身份/状态；长期凭据不写 provider 配置、全局官方 JWT 或远端 provisioning。
- worker 的授权更新必须绕过正在执行模型的普通请求队列及其后续请求启动屏障，及时取消旧 owner/revision 的执行；普通请求仍串行，停止/取消保留原目标请求启动屏障。
- `429 XCODE_RATE_LIMITED` 是真实服务端限流，不等同于凭据格式错误或订阅耗尽。退出本地清理成功不代表远程撤销成功；服务端拒绝或不可达时记录真实撤销失败，不自动重试或假称 SID 已撤销。

### 入口关闭与安全回滚

- 关闭 `XCODE_INTEGRATION_ENABLED` 只停止 authorize/complete/token 新签发；已有有效 SID 的 refresh/me 与有效指定预算推理继续工作，目的隔离和 general 资金排除继续生效。入口开关不是全量停用或安全回滚开关。
- 切换不认识 XCode scope/purpose 的旧版本前，必须先撤销全部专用 SID/key、取消全部有效专用实例，并确认活跃专用 SID/key/实例均为零；保留授权审计和已结算记录。只关闭签发入口不能阻止旧版本误用专用预算。
- 本机 HTTP 验证不证明生产 TLS/HSTS 或整体 OWASP ASVS 合规；窄 Web Cookie Path 与 ASVS L2 `__Host-` 根 Path 要求的差异仍须明确评估。

## 升级注意事项

- 旧 `session` Cookie 不再使用；升级后现有面板登录会失效，用户需要重新登录。
- 数据库迁移会新增 `user_sessions`、`auth_flows`、`external_identity_claims` 和 `users.auth_version`，并为已有用户初始化鉴权版本、回填 Telegram 账号唯一归属；若历史数据中同一 Telegram ID 已绑定多个用户，迁移会拒绝继续启动，需先消除歧义。
- 数据库迁移会为 Session 签发计数和分批清理新增索引；已有 `user_sessions` 很大时应为首次启动预留维护窗口。
- `user_sessions.previous_refresh_hash` 会从定长 `char(64)` 迁移为 `varchar(64)`。应用会兼容读取历史定长字段留下的空格填充；迁移后的目标结构必须保持幂等，连续启动不应反复执行列类型变更。
- 仅 master 节点定时清理过期登录会话、超过配置保留期的 revoked 会话和已过保留期的 AuthFlow。
- 未配置 `TRUSTED_PROXIES` 时会兼容信任回环和常见私网代理；使用公网负载均衡器、`100.64.0.0/10`、链路本地地址或自定义 CNI 网段的部署仍需显式配置。需要严格忽略所有转发头时设置为 `none`。
- Redis 限流从近似滑动窗口改为原子固定窗口，存在明确的边界双倍突发语义。
- 用户级模型成功请求限流的 UTC 时间戳在滚动升级期间存在一个窗口的混合格式过渡，期间可能临时误放行或误拒绝。
- 自建客户端应按新的 AuthBundle、`flow_token` 和 Security Proof 契约升级；PAT 客户端可直接移除 `New-Api-User`。
- 旧版 PAT 保留在 `users.access_token` 中，自新版首次启动起 30 天内照常可用，之后自动停用并返回 401 `ACCESS_TOKEN_LEGACY_RETIRED`。停用日期记录在 `options` 表的 `LegacyAccessTokenRetireAt`，由服务端写入，不能通过设置接口修改。过渡期内不能再生成旧版令牌，但可以撤销。
- 删除了 `/api/user/token*` 接口，改为 `/api/user/access_tokens`。
- 降级到旧版本后，新令牌不可用；未撤销的旧版令牌会重新可用，因为旧版本不认停用日期。
