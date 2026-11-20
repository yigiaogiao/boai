# BOAI 懒猫微服候选包

这份说明对应 `cloud.lazycat.app.boai` 的 `0.1.12` 候选包。它已经在本机完成构建、lint 和打包内容冒烟，**没有**在懒猫微服真机安装，也**没有**上架商店。最新候选包会自动复制到 `/lzcapp/document/`；`0.1.12` 已复制并校验 SHA-256。`0.1.12` 改进 OPML 导入结果：显示全部条目并把失败项排在前面；源站返回 HTML 403 访问页时，明确提示「源站拒绝当前网络访问」，不再误报为 RSS／Atom 解析失败。`0.1.11` 只把仓库和包元数据切换到 MIT 许可证，功能与 `0.1.10` 相同。`0.1.10` 是一次源码自查：删除已移除的书签功能残留（模型、存储方法、旧表定义）、无引用的后端函数、前端 API 封装、图标、样式、CSS 变量和过期文案，不改变用户可见功能。它保留 `0.1.9` 的国内热门推荐排在海外英文之前、中文热门单集和英文科技／商业两个子栏各最多 10 条、桌面单集详情返回按钮，以及单集详情里的节目封面、节目名和面包屑打开播主详情页（未订阅试看节目同样适用）。它同时保留 `0.1.8` 的侧栏「继续收听」计数、fake-IP DNS 放行和瞬时传输重试，`0.1.7` 的统计跳转链（上限 12 次，逐跳校验）、取消关注可撤销和节目详情 120 集上限，0.1.6 的英文主题筛选、单集直达、列表保留和长介绍折叠，0.1.5 的隐藏试看、16 MB 订阅源上限，0.1.4 的选择入口简化、OPML 上移，0.1.3 的内联搜索、继续收听，以及 0.1.2 的右击／长按菜单、本地下载和自动暂停修复。`dist/` 里旧的 `0.1.0` 至 `0.1.11` 仅作对照，不再是最新候选。

## 产物

| 项目 | 值 |
| --- | --- |
| LPK | `lazycat/dist/cloud.lazycat.app.boai-v0.1.12.lpk` |
| 大小 | 15,113,216 字节（约 15 MB） |
| SHA-256 | `88905c858b10c811f8466a4a1e5c831b0650826d799481185f44b7305be16225` |
| 包 ID | `cloud.lazycat.app.boai` |
| 版本 | `0.1.12`（与 `web/package.json`、`lazycat/package.yml`、二进制内 `main.version` 一致） |
| 目标 | linux/amd64，Go 静态链接 |
| 运行时 | `registry.lazycat.cloud/lzc/lzcapp:3.20.3` |

构建环境：Go 1.27.4、Node.js 26.5.1、npm 12.0.2、lzc-cli 2.0.9。Go 不在默认 `PATH` 时通过 `GO` 和 `PATH` 指定：

```bash
PATH=/home/shenhai/sdk/go1.27.4/bin:$PATH \
  GO=/home/shenhai/sdk/go1.27.4/bin/go \
  ./scripts/build-lpk.sh
```

脚本做五件事：构建前端；以 linux/amd64 静态链接编译 Go 服务并校验 `file` 输出；把 `web/dist`、CA 证书和可执行文件放进 `lazycat/content`；运行 `lzc-cli project lint` 后打包；若本机存在可写的 `/lzcapp/document`，自动把新 LPK 复制过去并校验内容。设置 `BOAI_SKIP_NETDISK_COPY=1` 可跳过最后一步。没有 Docker、没有镜像构建、没有镜像推送。

## 包里有什么

`content.tar` 的根就是 `contentdir` 的内容：

```text
boai                     # 14 MB，ELF x86-64，statically linked，stripped
ca-certificates.crt      # HTTPS 订阅源和 AI 供应商使用的 CA
web/index.html
web/assets/*.css
web/assets/*.js
```

权限归一为目录 `755`、普通文件 `644`、`boai` 可执行 `755`。`lzc-cli` 把 `manifest.yml`、`package.yml`、`icon.png` 和 `content.tar` 打进 LPK；解包核对后，`application.injects`、`multi_instance: true`、路由和 healthcheck 都在最终 `manifest.yml` 里。

## 部署设计

- **多实例**：`multi_instance: true`。每个用户有自己的容器和 `/lzcapp/var/boai`，SQLite、续听进度、AI 配置和加密后的 Key 都按用户隔离。没有申请网盘权限，也不读取 `/lzcapp/run/mnt/home` 或 `/lzcapp/media`。
- **身份**：设置 `BOAI_TRUST_USER_HEADER=true` 和 `BOAI_USER_HEADER=X-HC-User-ID`。服务只认这个头；缺失、过长或含控制字符的标识直接返回 `401`，不会退回 `local` 公共用户。可信前提是懒猫网关会清理客户端伪造的同名头并重设，这一点只能在真机确认。
- **数据**：`/lzcapp/var/boai:/data`。重启、升级和卸载重装后保留；首次启动会在 `/data/ai-secret` 生成 0600 的 AI Key 加密密钥，备份数据目录时要一起保留。用户主动下载的音频位于 `/data/downloads/<用户哈希>/`，目录 0700、文件 0600，仍属于这个用户自己的数据目录，不读也不写懒猫网盘的其他文件。
- **网络**：只申请 `net.internet`。manifest 设置 `BOAI_ALLOW_FAKE_IP_DNS=true`，因为懒猫把公网域名改写到 `198.18.0.0/15` 再由平台代理；该开关只接受主机名请求在 80／443 端口使用这一段地址，私网、回环、链路本地、云元数据和直接 IP 链接仍被拒绝。manifest 不设置 `BOAI_ALLOW_PRIVATE_NETWORK`、`BOAI_ALLOW_INSECURE_AI`，订阅源、重定向、DNS 和 AI 供应商地址继续走公开 HTTP(S) 校验。
- **入口缓存**：服务自身对 HTML 返回 `no-cache`，对 `/assets/` 返回 immutable；manifest 再对非 API、非资源路径注入 `Cache-Control: no-cache, must-revalidate`，避免懒猫 WebView 在覆盖升级后继续跑旧 bundle。
- **健康检查**：`wget -qO- http://localhost:8080/healthz`，每 30 秒一次，启动宽限 30 秒。
- **端口**：容器内 `:8080`，经 `routes: - /=http://boai:8080` 暴露。

没有使用 `run_as`、`setup_script` 或镜像内初始化参数，避免本机设备上出现过的 idmapped 目录和用户 UID 问题。

## 本机验证结果（2026-11-19）

- `lzc-cli project lint . -f lzc-build.yml`：`No manifest lint warnings found.`
- `lzc-cli project build . -f lzc-build.yml`：生成上述 LPK。
- `file lazycat/content/boai`：`ELF 64-bit LSB executable, x86-64, statically linked, stripped`。
- 解包 LPK：顶层为 `content.tar`、`icon.png`、`manifest.yml`、`package.yml`；最终 manifest 保留多实例、路由、注入、bind 和环境变量。
- 用解包后的 `content/` 在本机启动二进制（模拟 `BOAI_WEB_DIR`、`BOAI_DATA_DIR`、可信用户头，按 manifest 设置 `BOAI_ALLOW_FAKE_IP_DNS=true`，**未设置** `BOAI_ALLOW_PRIVATE_NETWORK`）：
  - `/healthz` 无身份头返回 `200`。
  - `/api/status` 无身份头返回 `401 {"error":"缺少网关用户标识"}`；带 `X-HC-User-ID` 返回 `200`，版本为 `0.1.12`，`library` 含 `in_progress`。
  - 入口 HTML 引用新的 `web/assets/index-DSUYlrRG.js` 和 `index-BKKJFlx_.css`。
  - `/` 返回 `Cache-Control: no-cache`；`/assets/*.js` 返回 `public, max-age=31536000, immutable`。
  - 数据目录生成 `boai.db` 和权限 `0600` 的 `ai-secret`。
  - 带身份头请求 `GET /api/discover` 返回 `200`，Apple 公开榜单在当前网络下可用。
  - `/api/discover` 返回 12 条中文榜、最多 10 条中文热门最新一期、10 条科技与 AI、10 条金融与商业；英文来源字段写明「榜位，不是收听人数」，科技／商业各返回最多 10 条「各节目最新一期」。
  - 用空数据目录请求 `POST /api/discover/show` 试看「声动早咖啡」：返回 120 集、`subscribed:false`；随后 `/api/status` 仍为 `feeds:0, episodes:0, new_episodes:0, in_progress:0`，`/api/feeds` 为空，单集详情仍可按 ID 读取。订阅同一节目后复用原记录，状态变为 `feeds:1`，节目进入收件箱。
  - 书签路由保持移除：`GET /api/episodes/1/bookmarks` 返回 `404`；单集详情不再返回 `bookmarks` 字段。
  - 最终 `0.1.12` 包（`index-tTNjDjYK.js`＋`index-ZMJb-gQP.css`）解包后浏览器冒烟：带身份头打开订阅播客，板块顺序为「热门播客」「热门节目最新一期」「海外热门 · 英文精选单集」；12 个榜位节目、10 条中文热门单集、英文科技和商业各 10 条可见；点热门单集进入播放详情，桌面端显示返回按钮，点节目封面或节目名进入播主详情页，未订阅试看节目也能打开；桌面和 390×844 手机视口无横向溢出、无脚本错误。`0.1.12` 包内英文单集点播 `readyState=4`、无媒体错误。OPML 导入实测 64 条：38 个新增、26 个失败，失败项排在最前；23 个 `feed.xyzfm.space` 条目显示「订阅源返回 403 访问页（403 · 无法访问），源站拒绝当前网络访问」，2 个 404、1 个证书过期也分别给出原因。
  - `0.1.8` 音频冒烟：在只开 fake-IP 放行、不开私网开关的配置下，真实 Apple 英文推荐的 16 个节目源可完成试看并返回 `audio/mpeg`；逐条请求音频均返回 `206`（Vergecast 曾在一次批量请求中因平台 DNS 瞬时失败返回 422，重试后恢复）。重建后 `0.1.8` 包再测：16 条英文单集 15 条直接返回 `206`，另 1 条因并发查询与榜单刷新之间的标题匹配竞态未命中，按单集 ID 复查返回 `206`。浏览器在 `0.1.8` 包上连续点播 6 条英文单集，全部 `readyState=4`、`currentTime=1.5`、无错误提示、无脚本错误。
  - `0.1.9` 包音频检查：同样只开 fake-IP 放行、不开私网开关；英文科技与商业两个子栏各 10 条、共 20 条单集逐条请求音频全部返回 `206`；浏览器点播其中一条 `readyState=4`、无错误提示。
  - 说明：`0.1.7` 及更早的本地音频冒烟临时启用过 `BOAI_ALLOW_PRIVATE_NETWORK=true`；`0.1.8` 起改用只覆盖 `198.18.0.0/15` 主机名 80／443 的 `BOAI_ALLOW_FAKE_IP_DNS`，发布包不包含私网放行开关。
- `go test -race ./... -count=1`、`go vet ./...` 均通过；前端 `npm run build` 通过（CSS 39.45 kB、JS 315.88 kB，gzip 8.52 kB／94.83 kB）。
- 订阅播客与节目详情浏览器 E2E：侧栏「继续收听」、收件箱／订阅内联搜索、批量分类、12 个榜位节目、中文热门 10 条、英文科技／商业各最多 10 条、目录搜索均通过；桌面和手机详情返回、未订阅试看节目的播主页跳转也通过；我的订阅和公开目录的节目都进入详情页，未订阅详情显示介绍、节目列表和订阅按钮；订阅后状态更新、取消关注后回到发现页；「热门节目最新一期」直接进入播放详情；390px 手机详情页有返回按钮、可打开单集且无横向溢出。外部榜单只验证当前网络，未测 Apple 限流和断网提示。
- 样式与下载批量删除的浏览器 E2E：收件箱搜索输入框计算样式为无边框、透明底，390px 下为 16px 且无横向溢出；选择条与列表同为纸面底色；下载板块两项「全选」后批量删除，下载记录和 `downloads/` 下的两个本地音频文件同时消失。
- 0.1.12 OPML 回归：重定向到 HTML 403 访问页时返回明确错误，不再报「不是可解析的 RSS／Atom」；结果列表显示全部 64 条、失败项置顶、区域可滚动；桌面和 390px 均无脚本错误。包内复测 38 个新增、26 个失败，与失败源逐条对应。
- 0.1.11 许可证元数据：`lazycat/package.yml` 与 `web/package.json` 均为 MIT，包内 `package.yml` 的 `version: 0.1.11`、`license: MIT`；构建和 lint 通过。
- 0.1.10 源码自查回归：清理后的完整 E2E 93 项全部通过；桌面导航 11 项、手机导航 9 项全部通过；`go vet`、`go test`、`deadcode -test`、未使用前端导出／CSS／图标扫描和前端构建均通过；`0.1.10` 包冒烟 13 项全过。
- 0.1.9 交互回归：订阅播客的板块顺序和 10 条上限在桌面与 390px 一致；桌面单集详情显示返回按钮，点节目名或封面能打开播主详情页，未订阅试看节目同样可用；收件箱的桌面阅读区不显示无实际作用的返回按钮，手机端返回仍先回列表。
- 0.1.8 交互回归：侧栏「继续收听」显示的数量与 `/api/status` 的 `in_progress` 一致；进度保存后计数即时变化；fake-IP 放行只覆盖主机名的 80／443，私网、元数据、直接 IP 和其它端口仍被拒绝。
- 0.1.7 交互回归：节目详情最多渲染最近 120 集；从我的订阅取消关注后节目页和三栏布局保持，出现「订阅节目」并可原地重新订阅；媒体代理覆盖 10 次跳转与 12 次上限；单集列表 `limit` 参数校验通过。
- 0.1.6 交互回归：英文单集行浏览器默认按钮边框为 0，节目名与单集标题不再相同，点英文单集直接进入 Reader；我的订阅打开节目后中间列表仍可见，播放单集时列表仍在而 Reader 占据第三栏，手机单列下节目详情会隐藏列表、返回后恢复；英文栏只保留当前榜单中的 AI／科技／互联网／金融商业主题，TED Radio Hour 和 American Alchemy 不再出现；长介绍默认折叠三行并可展开。
- 0.1.5 交互回归：只通过右击／长按「多选」进入选择模式；单集页没有书签和「资料库」面包屑；从单集封面或节目名进入节目详情后可返回原单集；英文栏渲染 8＋8 条最新单集，不再出现纯节目名卡片；热门节目和目录卡片没有直接订阅按钮；桌面 1440px、600px 和手机 390／360px 均无横向溢出。
- 下载相关：本机 E2E 验证了安全抓取、`0600` 文件、Range 播放、单个／批量删除和退订清理；尚未在微服真机上验证下载大文件、离线播放和 `/lzcapp/var/boai/downloads` 的持久化。

`lzc-cli box list` 在本机返回 `No box found`，因此没有执行 `lpk install`／`deploy`，也没有生成任何真机通过结论。

## 侧载步骤（交给用户执行）

1. 在懒猫客户端设置里允许安装非商店应用。
2. `cloud.lazycat.app.boai-v0.1.12.lpk` 已自动复制到 `/lzcapp/document/`。如果网盘里还留着旧的 `0.1.0` 至 `0.1.11` 包，注意不要点错版本。
3. 在懒猫桌面端或手机端的网盘里点开该 LPK，按提示安装。
4. 安装后打开应用。应免密进入空收件箱；订阅一个公开播客源，确认单集进入收件箱并可播放。
5. 涉及权限或数据目录的改动后，重启应用再验证。

新的 BOAI 候选包会自动复制到 `/lzcapp/document/`；不要发布商店、替换网盘里的其他文件或修改相邻项目。

## 真机验收清单（全部待实测）

- [ ] 桌面客户端和手机客户端都能免密打开 `BOAI`，首页不白屏、入口 HTML 升级后能换到新 bundle。
- [ ] 手机 WebView 上收件箱可滚动、单集详情可进入和返回，硬件返回先回列表而不是退出应用。
- [ ] 桌面和手机客户端上，从「订阅播客」点热门单集进入详情后能返回上一级；点节目封面或节目名能进入播主详情页，未订阅的试看节目同样可用。
- [ ] 手机 WebView 上长音频能开始播放、拖动进度、续听，且连续播放超过 15 秒不会自动暂停；锁屏、切换应用和后台恢复行为符合客户端预期。
- [ ] 手机 WebView 上长按列表行能打开操作菜单，菜单不越界、长按不会误触打开单集；收件箱和「我的订阅」的「下载 N ↗」入口可用。
- [ ] 下载单集后，断网仍能从「下载」板块播放本地文件；右击／长按删除后文件确实从 `/lzcapp/var/boai/downloads` 消失；退订节目会清理对应下载。
- [ ] 网关确实传入 `X-HC-User-ID`；直接请求不带该头时 API 是 `401`。
- [ ] 用两个账号分别安装/打开：订阅、收件箱、进度、AI 设置和 Key 互不可见；一边订阅不会出现在另一边。
- [ ] 不配置 AI Key 时，订阅、播放、逐字稿和空态都正常；配置入口能打开。
- [ ] 真实外部 RSS、Apple Podcasts 链接和音频 `Range` 请求在微服网络上可用；HTTPS 证书链没有报错。
- [ ] 「订阅播客」的海外热门英文单集能在真机打开节目详情并播放，fake-IP DNS 环境下不再提示「无法连接音频源」；同时确认内网地址仍被拒绝。
- [ ] 重启应用后订阅、进度和 AI 配置仍在；覆盖安装新版本后 `/lzcapp/var/boai` 数据仍在。
- [ ] 未设置 `BOAI_ALLOW_PRIVATE_NETWORK`／`BOAI_ALLOW_INSECURE_AI` 时，内网地址被拒绝；AI 供应商仍只接受公开地址。
- [ ] 真实 AI 供应商另测；本轮只验收过本地假文本／假转写服务，不能把假结果写成真实供应商通过。

验收记录请写清设备、客户端版本、账号数量和结果；没有设备或账号的项保持未勾选，不要用本机构建或 lint 结果替代。
