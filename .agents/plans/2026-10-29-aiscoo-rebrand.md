# aiscoo 品牌统一实施计划

> 历史实施记录（2026-10-29）。当前品牌素材以 `web/public/brand/` 和 `electron/README.md` 为准：胶囊眼、填充笑眼与 10 秒整头弹跳。旧内联 `web/src/assets/logo.tsx` 已移除，README 八张合成示意图已刷新为现行字标；下文保留实施当时的路径、周期与验收事实。

> **For agentic workers:** 按本计划逐任务实施并检查；本次实施采用 `executing-plans`，完成情况及验收证据见末尾。品牌保护禁令已按用户明确指令删除。

**Goal:** 将本仓库产品层面的名称、Logo、网页元数据、邮件显示名、桌面包装和发行展示统一为小写 `aiscoo`，复用已经上线到客户端的同一套品牌素材。

**Architecture:** 沿用既有 `SystemName / Logo / Footer / About / HomePageContent` 设置，以及 `/api/status → status-query → system-config-store → useSystemConfig → 品牌组件` 链路。补齐静态首屏、默认回退、原生内嵌页、缓存刷新和发行资源中的硬编码；通过兼容升级处理已有配置，保留现有账号、会话和持久化数据的可用性。

**Tech Stack:** Go / Gin、React 19 / TypeScript、Bun / Rsbuild、现有共享 UI 组件、Electron。

**Spec:** 本次用户指令：桌面客户端已采用 `aiscoo`，本仓库名称和 Logo 全部统一，先梳理范围并制定计划；随后明确要求先删除 `AGENTS.md` 中禁止替换品牌的段落再继续。

**Baseline:** 本仓库 `release`，提交 `1b46246a2`。当前客户端品牌来源 `/Users/timo/ai/XCode`，品牌提交 `0afea8f5`（2026-10-29）。

## Global Constraints

- 展示品牌统一用用户指定的小写 `aiscoo`。客户端配置当前 `productName=Aiscoo`、字标 `wordmark=aiscoo`，本计划以用户指令为准；不同时修改客户端仓库。
- 复用现有品牌组件、设置表单、状态管理和错误页，不另建一套平行配置或替代 UI；实施前已读取根及 `web/AGENTS.md` 和项目 `shadcn-ui` 技能。
- `NOTICE:11–25` 独立于已删除的开发规则，要求保留可见来源署名 `Frontend design and development by New API contributors.` 和原项目链接 `https://github.com/QuantumNous/new-api`。统一放在显著的法律/关于/页脚区域，并保留分发中的 LICENSE、NOTICE 和第三方许可证。品牌替换不把这些真实来源声明算作漏改。
- 不将 `new-api / New API / NewAPI / one-api / XCode / XWork / QuantumNous` 一律做字符串替换；逐项区分当前产品品牌、真实上游链接、第三方名称和协议/持久化标识。保留合并上游的 Git 路径与来源事实。
- 从 `release` 建立 `feat/aiscoo-brand` 实施；保护已有的 `docs/plans/2026-10-08-xcode-integration.md` 改动、`.v2c/`、`.video_agent/`。本轮不改业务、不提交或推送品牌实现，不添加 `docs/` 或插件目录文档。

## Review Focus

| 输入或场景 | 必须呈现的行为 | 负责验收的任务 |
|---|---|---|
| 空缓存、请求失败、JavaScript 尚未启动、直接访问错误页或原生内嵌页 | 标题和默认 Logo 已是 aiscoo，不闪现旧品牌 | 任务 1、3 |
| 管理员保存名称/Logo/About/HomePageContent，或浏览器已持久化旧 status | 当前页面、随后导航和再次打开页面均使用新值 | 任务 2、3 |
| 从旧版本升级，options 中仍有默认 New API、旧 Logo URL 或自定义品牌 | 默认旧品牌可升级，自定义内容先备份并按本次替换清单处理，不覆盖无关配置 | 任务 2 |
| 深浅主题、窄屏、折叠菜单、图标加载失败 | 使用正确版本的图形/字标，无黑底黑图、白底白图、裁切或拉伸 | 任务 1、3 |
| 已有账号、Passkey、TOTP、JWT、会话及桌面安装升级 | 展示名更新，身份标识/密钥/数据路径兼容，已有数据和登录方式继续可用 | 任务 2、4、5 |

## 盘点结论与替换边界

| 范围 | 已找到的入口或资源 | 实施要求 |
|---|---|---|
| 服务端默认品牌与站点设置 | `common/constants.go:15`、`model/option.go`、`controller/misc.go` | 默认名称、Logo、页脚、关于和首页配置一致；覆盖旧 options 优先于新默认的情况 |
| 网站品牌组件 | `SystemBrand`、`PublicHeader → HeaderLogo`、`AuthLayout`、`SetupWizard`、`Footer` | 沿用组件及 props，统一主导航、侧栏、登录注册、初始化、页脚等展示 |
| 静态浏览器品牌 | `web/index.html:11–12`、`web/src/main.tsx:75`、`web/public/logo.png`、`web/public/favicon.ico` | 静态 title/meta/favicon 与运行时一致；尤其补 `/xcode/plans` 跳过动态标题的分支 |
| 设置保存及缓存 | `web/src/features/system-settings/hooks/use-update-option.ts:30` | `SystemName / Logo / Footer / general_setting.docs_link` 保存后刷新 `/api/status` 及 store；About query 与首页本地缓存分别处理 |
| 七语言品牌文案 | `web/src/i18n/locales/{en,zh,zh-TW,fr,ru,ja,vi}.json` | 逐项审查普通品牌、欢迎语、帮助和旧客户端词；法律署名、协议和无关第三方名称分开处理 |
| 服务端邮件与身份显示名 | 邮件发件显示名/主题与 TOTP issuer 读取 `SystemName`；`setting/system_setting/passkey.go:138` | 邮件使用新名称；Passkey 显示名初始化副本不能漏改；不改变 RP ID、origin 或已发出的 TOTP secret |
| 桌面包装与图标 | 本仓库 `electron/` 的窗口、菜单、托盘、安装包、元数据及构建资源 | 与当前 aiscoo 客户端使用同源图标；升级安装必须保留已有应用数据路径 |
| 发行、部署和说明 | README、Docker、Compose、CI、发行产物、镜像/服务名称和更新链接 | 自有产品展示改 aiscoo，镜像/仓库链接必须真实存在；不把上游实际仓库 URL 改成不存在的 aiscoo 地址 |
| 技术兼容标识 | JWT issuer=`new-api`、API error type、cookie/storage/Redis/DB 名称、Go module/import 路径 | 作为兼容性例外记录，不做机械替换；如确需改名，须有旧新兼容和迁移回归后再切换 |
| 来源声明 | `NOTICE`、`LICENSE`、第三方许可证及显著法律区域 | 保留真实署名和链接，注明本发行版的 aiscoo 品牌变更 |

### 精确文件入口补充

| 类别 | 主要文件与位置 | 需要覆盖的内容 |
|---|---|---|
| 默认资源和构建 | `web/src/lib/constants.ts:24`、`web/src/assets/logo.tsx:23`、`web/rsbuild.config.ts:65` | 默认名称和 Logo；历史 SVG 未检出运行时引用，确认后同步调整或移除未使用旧素材；构建 favicon 引用同步 |
| 公共品牌 | `web/src/components/layout/components/{system-brand,public-header,header-logo,footer}.tsx` | 统一产品显示，复用 Footer 的 `ProjectAttribution` 保留来源 |
| 登录与初始化 | `web/src/features/auth/auth-layout.tsx:39`、`web/src/features/setup/setup-wizard.tsx:83` | 壳层品牌、图形、alt；不重写认证行为 |
| 主页与关于 | `web/src/features/home/components/sections/hero.tsx:183`、`web/src/features/about/index.tsx:31` | 内置介绍、空配置回退和实际项目链接 |
| 设置与缓存 | `web/src/features/system-settings/general/system-info-section.tsx:98`、`web/src/lib/status-query.ts:94`、`web/src/stores/system-config-store.ts:72`、`web/src/hooks/use-system-config.ts:60`、`web/src/features/home/hooks/use-home-page-content.ts:28` | 配置字段、状态刷新、持久化旧数据、首页缓存 |
| 客户端及管理页面 | `web/src/features/xcode/{authorize,plans}.tsx`、`web/src/features/{auth/sign-in,auth/sign-up}/index.tsx`、`web/src/features/models/components/drawers/model-mutate-drawer.tsx:542`、`web/src/features/subscriptions/components/{subscriptions-mutate-drawer.tsx,dialogs/xcode-authorization-section.tsx}` | 面向客户端与管理者的 XWork/XCode 展示名和提示 |
| 其他前端文字 | `web/src/features/errors/general-error.tsx:26`、`web/src/features/chat/lib/chat-links.ts:195`、`web/src/features/system-settings/integrations/waffo-pancake-settings-section.tsx:65` | 反馈入口、自有客户端导入名称、新建外部资源的显示默认值；保留外部平台协议参数 |
| 后端启动与帮助 | `main.go:66`、`common/init.go:25`、`common/log_text.zh-CN.json:134`、`middleware/recover.go:20` | 启动日志、CLI 帮助、命令展示、支持地址；真实维护者来源保留 |
| 邮件与通知 | `common/email.go:90`、`controller/misc.go:239`、`service/{email_binding,account_security,user_notify}.go`、`i18n/locales/{en,zh-CN,zh-TW}.yaml` | 发件显示名、主题、Bark/Gotify 等通知的自有客户端标识；已读邮件模板未发现硬编码 Logo，无需凭空增加邮件图像 |
| 对外自有产品标头 | `relay/channel/openai/adaptor.go:244`、`service/codex_models.go:65` | OpenRouter 的归属标题/来源网址、User-Agent；只调整品牌元数据，保留提供方 API 语义 |
| 支付产品展示 | `controller/topup_waffo.go:66` | 支付中的 `New API` 产品名回退；不改金额/计费算法，如扩大到账务字段先完整读取 billing 规则 |
| 内容与外部源 | `setting/operation_setting/general_setting.go:27`、`setting/task_plugin.go:15`、`controller/model_sync.go:26` | 文档/插件市场的真实源地址和回退；`/api/newapi/...` 是现有服务路径，保留兼容 |

前端七份语言资源各有 66 条键名匹配旧品牌关键词，包含法律、协议和闲置键，不能直接当作 462 个必须替换的文案。同步核查 `web/src/i18n/static-keys.ts:82` 和 `web/scripts/sync-i18n.mjs:27`。本轮没有形成最终受影响文件总数，实施后据真实 diff 汇总。

## 素材来源与复制清单

品牌真源是 `/Users/timo/ai/XCode/docs/brand/aiscoo`，运行时 SVG 在 `/Users/timo/ai/XCode/packages/ui/src/xwork/brand/assets/`。实施时将资源复制到本仓库并随仓库分发，不引用开发机器的绝对路径。

| 用途 | 可复用源文件 | 尺寸及规则 |
|---|---|---|
| 导航/登录静态字标 | `packages/ui/src/xwork/brand/assets/wordmark-{light,dark}.svg` | 581.15 × 200，透明背景，按原比例显示 |
| 侧栏/窄屏独立图形 | 同目录 `mark-{light,dark}.svg` | 172 × 172 |
| 应用方形图标 | 同目录 `app-{light,dark}.svg` | 1024 × 1024 |
| 浏览器/手机图标 | `packages/web/public/favicon.ico`、`apple-touch-icon.png`、`icon-192.png`、`icon-512.png` | ICO 16–256；PNG 180² / 192² / 512² |
| 桌面发行图标 | `packages/desktop/build/icon.png`、`icon_windows.png`、`icon.ico`、`icon.icns` | PNG 1024²；ICO 七档；ICNS 含 Retina；当前桌面为深底、图形放大 20% |

`light` 是浅背景黑图，`dark` 是深背景白图。品牌前景/背景分别为 `#121317 / #FFFFFF` 和 `#FFFFFF / #1C1C1E`。优先用静态字标，不为此次更名新增动画或重做全站配色。`/Users/timo/ai/aiscoo/aiscoo/AiscooUi/resources` 是另一套圆形机器人脸旧方案，不混用。客户端当前公开 homepage 配置为 `https://zcode.z.ai`；未找到新的 aiscoo 官网配置，不编造官网、支持邮箱或下载地址。

## Task 1: 统一品牌资源和静态首屏

**Files:** `web/public/` 品牌资源、`web/index.html`、现有品牌组件及其现有测试；实施时依据调用点确定新增资源的最终名字，优先保持现有公共 URL 兼容。

**Interfaces:** 提供随仓库打包的 aiscoo 图形、字标、favicon/移动图标和默认标题；消费既有主题状态与 `Logo` 自定义配置。用户配置 Logo 时继续按现有 API 工作。

- [ ] 将上表 SVG/PNG/ICO/ICNS 从已核对的客户端源复制到本仓库；记录素材来源提交，校验类型、尺寸、viewBox 和透明度。
- [ ] 静态 HTML title/meta/图标改为 aiscoo，避免等待 `/api/status` 才出现正确名称；复用现有组件处理深浅版本、alt 和默认 Logo。
- [ ] 清查实际启用的 manifest、移动收藏图标和打包资源引用；没有启用的 PWA 能力不为更名额外引入。
- [ ] 用现有品牌组件测试覆盖浅深主题、单字标/图形模式和图片失败回退；检查浏览器 favicon 在无缓存与有缓存时更新。
- [ ] 执行受影响组件测试、类型检查及文件 lint；打开静态首屏确认不闪现旧名称。

## Task 2: 站点配置、旧数据与服务端显示名

**Files:** `common/constants.go`、`model/option.go`、`controller/misc.go`、`setting/system_setting/passkey.go`，以及盘点出的邮件、通知和 TOTP 显示名入口；优先扩展现有对应测试，不跨层拆散小功能测试。

**Interfaces:** 对外继续提供现有 `SystemName / Logo / Footer / About / HomePageContent` 字段和 `/api/status` 契约；默认/本次部署目标名称为 `aiscoo`，使用任务 1 的本地品牌资源。

- [ ] 在开发/验证数据库读取非敏感品牌设置清单并备份；分别列出缺省值、旧默认值、已经自定义的文本/HTML/URL。生产配置更新在实施阶段使用明确的部署目标和备份执行。
- [ ] 修改新安装的默认品牌；为旧默认设置制定可重复执行的升级路径，确保已存 `SystemName=New API` 不继续覆盖新默认。已有自定义首页/页脚/About 逐条调整其中产品文字和自有 Logo，保留其他内容。
- [ ] 邮件发件显示名、主题、通知标题和新建 TOTP 的 issuer 使用 aiscoo；既有 TOTP secret 不变。Passkey 的 RP 显示名同步更新，RP ID/origin/credential 不变。
- [ ] 如修改认证相关显示名入口，按根规则先读对应 OWASP 指南并仅验证本次影响的契约；认证流程和计费逻辑不在品牌任务中重写。
- [ ] 如果实现触及数据库行为或迁移，在真实 SQLite、MySQL、PostgreSQL 验证新建与旧库升级、重复启动幂等和既有设置保留，并记录具体版本/命令/结果；缺失任一矩阵结果不可宣称该迁移完成。

## Task 3: 所有网页和缓存更新

**Files:** 复用品牌组件及调用点；`web/src/main.tsx`、`web/src/features/system-settings/hooks/use-update-option.ts`、About/HomePageContent 的现有读取/缓存模块、`web/src/i18n/locales/*.json`，以及相关既有 `__tests__/`。

**Interfaces:** 沿用 `/api/status → store`；站点设置更新成功后即时刷新状态。About 使用 `['about-content']` 查询；HomePageContent 使用 effect 和 `localStorage('home_page_content')`，分别更新，不把二者混为同一查询。

- [ ] 沿页面清单替换用户品牌：主页/营销区、公共头部、侧栏与折叠态、登录/注册/找回/验证的壳层、初始化、关于/帮助、页脚、路由错误/空状态、原生 `/xcode/plans` 与客户端授权页。
- [ ] 审查 `XCode / XWork` 等普通客户端名称，展示处采用 aiscoo；保留协议路径、客户端注册 ID 和识别合法来源所需的技术值。品牌替换不扩散到供应商模型 Logo、支付和第三方 OAuth Logo。
- [ ] 在设置保存成功路径补 `SystemName / Logo / Footer / general_setting.docs_link` 的 status 失效与 store 更新；补 About 查询失效和首页本地缓存更新。处理旧持久化配置，使 document.title/meta/favicon 响应新状态和后续保存。
- [ ] 覆盖七语言所有活跃品牌文案；保留来源署名及可见链接，自定义 Footer 也不能吞掉法律信息。更新导航/页脚/关于/帮助中属于本产品的文案和链接标签，真实外链目的地址逐项核实。
- [ ] 回归初次访问、旧缓存升级、设置保存后无须刷新、请求失败、直达错误路由、主题切换、窄屏和原生内嵌页面；断言最终品牌可见，协议行为和法定署名仍有效。

## Task 4: 桌面包装、发行与仓库展示

**Files:** `electron/{package.json,package-lock.json,main.js,build.sh,create-tray-icon.js}`、`electron/{icon.png,tray-iconTemplate.png,tray-iconTemplate@2x.png,tray-icon-windows.png}`；六份根 README、Electron README 和发布示例；`Dockerfile`、`Dockerfile.dev`、`docker-compose.yml`、`docker-compose.dev.yml`、`makefile`、`new-api.service`；`.github/workflows/{docker-build,docker-image-branch,release,electron-build,sync-release-to-gitcode}.yml`。不修改 LICENSE/NOTICE 的真实版权来源。

**Interfaces:** 桌面窗口、托盘、菜单、安装器、文件说明和产物展示为 aiscoo；保持既有安装升级、数据目录、更新和服务接口兼容。本仓库 Electron 当前 `productName=New-API-App`、`appId=com.newapi.desktop`、`publish=null`；其 `userData/data` 和 `new-api.db` 定位不能因显示名变化而丢失。

- [ ] 替换本仓库 Electron 的 productName/description/窗口标题/菜单/托盘提示/安装器文字，复用客户端当前深底桌面图标，另生成符合系统要求的 macOS 模板托盘资源。
- [ ] 列出 appId、系统协议、应用目录、userData、更新通道和签名身份的当前值。显示名可直接更新；身份字段以现有安装兼容为准，任何更换都先给出迁移与回归，不能因改 productName 让 Electron 自动改用新空目录。
- [ ] README 标题/自有产品介绍/截图和发布描述统一品牌，明确基于上游的修改；保留真实作者、历史记录、上游帮助链接以及许可证声明。只更新已获授权的自有品牌内容，不给第三方名词换品牌。
- [ ] 自有二进制/压缩包/镜像与 Compose 显示标签使用 aiscoo。registry/repository 必须已存在且有权限，更新服务和下载链接必须可验证；未确定目标前保留可用 URL，不发布虚构路径。
- [ ] 保留 Go module/import、JWT issuer、API error type、cookie/Redis/DB 等兼容性标识，并建立验收例外表；这些字符串不能通过搜索零命中作为完成标准。如后续要求连内部标识改名，另做有旧新兼容的迁移，不夹带到视觉更名。

### 技术兼容性例外清单

| 位置 | 现有契约与保留理由 |
|---|---|
| `service/auth_token.go:26`、`:61` | issuer、audience、签名派生字串；直接更名会影响已有 JWT 验证 |
| `service/auth_session.go:17`、`service/xcode_web.go:19` | 会话 Cookie 名；浏览器和客户端需持续识别 |
| `common/constants.go:198`、`middleware/cors.go:20`、`controller/relay.go:788` | `X-Oneapi-Request-Id`、`X-New-Api-Version`、`X-New-Api-Other-Ratios` 和既有错误 type；属于对外 API |
| `service/xcode_auth.go:180`、`service/xcode_usage.go:200`、`web/src/features/xcode/authorize.tsx:52` | provider/client ID、账户类型、`/xcode/*` 路由；与已发布客户端协作 |
| 根及子模块 `go.mod`、imports、构建 `-X` | 真实模块来源及独立构建边界；不是产品展示名 |
| `common/database.go:64`、`logger/logger.go:55`、`controller/performance.go:201` | 原 SQLite 文件与日志前缀及查询配套；不得因更名读到新空库或查不到旧日志 |
| `service/channel_affinity.go:30`、`model/subscription.go:43`、`pkg/wsmanager/wsmanager.go:20`、`model/option_primary_key_migration.go:15` | Redis、锁和 PubSub 名称；混合版本节点必须继续协作 |
| `web/src/lib/theme-storage.ts:20`、`web/src/features/chat/lib/chat-links.ts:163` | 已持久化主题键和外部客户端 protocol platform 参数；改展示标签不改既有解析值 |
| `constant/channel.go:201`、`web/src/features/channels/lib/channel-plugin-extensions.ts:76`、`pkg/jsplugin/registry.go:118` | 真实上游 New API 渠道及插件协议键；不是给本站产品起名 |

## Task 5: 验证、残留审查和交付

**Files:** 受影响的既有前后端测试文件、必要的同模块 `__tests__/`；本计划的最终结果记录。

**Interfaces:** 每个用户入口的品牌为 aiscoo、Logo 源一致；允许残留仅为明确记录的来源声明、第三方名称、真实上游链接和兼容性技术标识。

- [ ] 对活跃源码/模板/资源引用搜索 `new-api|New API|NewAPI|one-api|XWork|XCode|QuantumNous`，逐项归入已更改或有理由保留的列表；排除依赖、缓存、构建输出与 Git 历史。逐页确认没有非例外的旧产品品牌。
- [ ] 在 `web/` 先执行已有入口：`bun run test src/lib/__tests__/status-query.test.tsx src/features/xcode/__tests__/plans.test.tsx src/features/xcode/__tests__/display-context.test.ts src/features/errors/__tests__/general-error.test.tsx`，加本次扩展的保存刷新/品牌回退测试；再执行 `bun run typecheck`、`bun run lint`、`bun run format:check`、`bun run build:check`、`bun run copyright:check`。需要新增翻译时运行 `bun run i18n:sync` 并审查七语言 diff。这些是待执行命令，本轮未宣称通过。
- [ ] 根模块执行受影响包测试、`go vet ./...` 和 `go build ./...`（先准备 `web/dist` 嵌入产物）；按修改范围加入 `go test ./common -run '^TestSendEmail'`、`go test ./controller -run 'Test(GetStatus|GetTaskPluginMarketplaceSources|UpdateTaskPluginMarketplaceSources|Passkey)'`。部署/桌面脚本执行 `docker compose -f docker-compose.yml config --quiet`、`docker compose -f docker-compose.dev.yml config --quiet`、`node --check electron/main.js`、`node --check electron/preload.js`。只有触及 `relaykit/` 或 `tokenkit/` API 才在对应模块执行 `GOWORK=off go build ./...`；数据库/认证验证按任务 2 实际影响执行。
- [ ] 浏览器检查桌面/移动端、浅深主题、登录前后、初始化、错误页、原生 plans、设置保存、旧缓存；检查邮件展示。对桌面包检查窗口/托盘/Dock/安装器及从旧版安装升级后数据和登录仍可用。
- [ ] 交付变更文件、界面证据、命令和实际结果、保留例外表。按仓库流程验证后合入 release；提交/推送动作遵循当时用户授权，不把这份计划当作发布授权。

## 当前执行状态

五项实施工作及完整动效字标已完成。用户随后明确要求提交代码并合入 `release`，本次收尾按该授权执行；前文“不提交/推送”是实施阶段的历史边界。用户原有 `docs/plans/2026-10-08-xcode-integration.md`、`.v2c/`、`.video_agent/` 及另行新增的 `legal/aiscoo/` 不纳入品牌提交。未操作生产数据库、域名、镜像仓库或线上部署。

### 实际交付

- 复用 XCode 当前 aiscoo 源素材，更新深浅 Logo、字标、favicon、手机图标、桌面 PNG/ICO/ICNS 及可复现的 22/44px 托盘图；八张 README 模拟截图中的产品品牌同步更新，并说明是模拟数据示意图。
- 网页默认名称、HTML 标题、导航、登录注册、初始化、关于、页脚、客户端授权与套餐、管理配置及七语言文案统一；七语言均包含 53 个品牌键。NOTICE 精确英文来源署名和原项目链接保留。
- 已知旧默认 SystemName/Logo 在内存读取及前端旧缓存中归一化，自定义名称/Logo 保留。邮件、通知、TOTP 新建显示名、Passkey 显示名、CLI/日志和自有请求归属标题同步。RP ID、origin、TOTP secret、Cookie/JWT、provider/client IDs、数据库文件及 Redis 键均保留。
- 修复设置保存后状态/元数据不刷新、首页缓存不更新、旧请求覆盖新配置、损坏自定义 Logo 回退、相对默认 Logo 阻断表单提交。相关回归均观察到修复前失败、修复后通过。
- Electron 名称、窗口/托盘、图标和后端文件、Docker 本地构建、发行文件名/校验和匹配更新。桌面优先发现已有 `aiscoo / New-API-App / new-api-electron / New API` 数据目录中的原数据库，兼容旧安装。

### 实际验证与局限

| 验证 | 实际结果 |
|---|---|
| 前端品牌、站点表单、取消/去重竞态和既有用户绑定测试 | 3 文件 38/38 通过 |
| 前端完整 `bun run test` | 211 文件；2603/2604 通过。唯一失败为未修改的 Markdown 原型污染 trust 测试，在归档的原始 HEAD、相同依赖下同样重现 |
| `bun run typecheck`、`bun run build:check` | 通过，生产产物在 `web/dist` |
| 改动文件 lint | 43 文件，0 errors；6 warnings 为既有相关实现提示 |
| 完整 lint / copyright / format | 全库仍有 155 个未改文件 lint errors；23 个未改文件版权头问题；2 个未改测试文件格式问题。未扩散修改这些存量内容，不能宣称全库检查通过 |
| Go 测试 | common/controller/model/service/middleware/setting/system_setting/relay/channel/openai 受影响包通过；Passkey 服务由 controller 回归覆盖 |
| Go 构建与 vet | `go build` 和 `go vet ./...` 通过 |
| Electron 行为与脚本 | 四种数据目录情形 4/4 通过；主进程/preload/图标生成脚本语法通过 |
| 本机 macOS arm64 应用包 | `CSC_IDENTITY_AUTO_DISCOVERY=false npm run build:mac -- --dir --config.electronDist=.../node_modules/electron/dist` 通过；产物 `electron/dist/mac-arm64/aiscoo.app`；包名/可执行名为 aiscoo，原 appId 保留，图标、后端、LICENSE/NOTICE 齐全，包内后端哈希等于最终构建 |
| Compose | 正式/开发两份 `docker compose ... config --quiet` 通过，正式配置保留原 `version` 废弃提示 |
| 浏览器 | 首页、登录、390×844 窄屏、深色关于字标、无会话 `/xcode/plans` 错误页验证通过；未使用真实管理员账号或有效原生会话，相关行为由组件/集成测试覆盖 |

独立审查确认四项实际问题（macOS 校验通配符、相对 Logo 验证、status 双层去重竞态、长翻译键局部替换），本次均修复。未追加第二次审查。

Windows/Linux 安装器、签名、公证、实际旧版安装升级和生产邮件发送未在本机验证；macOS 产物是本地未签名 `.app`。Electron 上游下载两次失败后使用镜像完成下载，未改依赖版本。远端镜像/签名目的地和真实上游文档来源未替换成不存在的 aiscoo 地址；本地 Compose 默认构建 `aiscoo:local`，没有发布镜像。

详细执行日志和逐项判定在 `.superpowers/sdd/2026-10-29-aiscoo-rebrand/`，本目录不进入 Git。

### 用户追加：完整动效字标

已按用户后续要求将首页公共页头和登录/注册共用认证页头由单 `ai` 图形＋普通文字改为完整 `aiscoo` 动效字标。复用客户端 `wordmark-animated-{light,dark}.svg`（8 秒循环），沿用 HeaderLogo 组件，以 124×40 容器保持比例；隐藏重复视觉文字并保留可访问名称/标题。管理员自定义名称或 Logo 继续显示原配置。

SVG 自带 `prefers-reduced-motion` 停止动画；picture 的 reduce source 进一步选择静态完整字标。五个新增回归覆盖首页/认证页完整资源、重复文字隐藏、减少动态效果和自定义品牌。与站点设置及授权流程一起验证 4 文件 86/86 通过，typecheck、涉及文件 lint（0 errors/0 warnings）及 build:check 通过；浅深色手机页面实测资源加载成功。新增动效资源已进入 `web/dist/brand`。动效追加与品牌实施一并纳入本次授权的提交和合并；既有 macOS 包为此前静态版构建，若发布桌面版本需重新执行打包。

### 提交与合并前复核（2026-10-29）

- `go test ./...`、`go vet ./...`、`bun run build:check`、Electron 4/4 回归、两份 Compose 配置与脚本语法检查通过。Go 测试需在前端构建结束后运行，避免 Rsbuild 清空嵌入目录造成临时构建失败。
- 前端全量 `bun run test`：212 文件，2615/2616 通过；唯一失败仍为未修改的 Markdown 原型污染 trust 回归。本次再次在原始 HEAD 归档、相同依赖下执行该文件，观察到相同失败，未扩大品牌范围修改它。
- 涉及前端文件 lint 为 0 errors / 6 warnings；全量格式检查仅有两份未改测试文件，版权检查仅有 23 份未改文件头问题。`gofmt -l` 与 `git diff --check` 通过。
- 原有无关计划和工具状态、独立法律文稿均保留在工作区。本次只向 `origin/release` 同步品牌代码，不部署网站、重启服务或发布安装包。
