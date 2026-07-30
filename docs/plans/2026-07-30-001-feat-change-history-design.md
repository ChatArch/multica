# 变更历史 / 版本管理设计方案（MUL-5520）

> 状态：设计稿，**不含代码实现**。目标是先把"记录哪些人在什么时间做了什么样的改动"这件事的模型、边界和落地阶段定下来。
>
> **v2（2026-07-30）**：采纳 Yushen 的评审意见 —— 改为 **git 风格的全量快照**（每次变更快照实体完整形态，diff 一律读时计算）。主要改动集中在 §4、§5.3、§7.2、§8、§10；§5.1 的 `change_group` / `change_item` 保留，但 `change_item` 从真相源降级为**可重建的派生索引**。

## 1. 目标与非目标

### 目标

| # | 能力 | 说明 |
|---|---|---|
| G1 | **Who** | 每条变更都能定位到唯一 actor（member / agent / system），且 agent 变更必须能回溯到"代表哪个人"。 |
| G2 | **When** | 精确到毫秒的可排序时间戳；同一次请求内的多字段改动能被识别为**一次**变更，而不是散落的 N 条。 |
| G3 | **What** | 字段级 before/after；长文本（description、instructions、skill 文件）能看到**内容差异**，不只是"某人改了描述"。 |
| G4 | **Where** | 不止 issue。agent 配置、skill、autopilot 规则、project、squad、workspace 设置都要有历史。 |
| G5 | **可查询** | 按实体、按 actor、按时间窗、按变更类型检索；支持工作区级审计视图与导出。 |
| G6 | **可恢复** | 关键内容（description / agent instructions / skill 文件）支持查看旧版本并回滚。 |

### 非目标（本期明确不做）

- 不做 Git 级别的逐字符 CRDT/OT 历史（Figma/Notion 那套实时协作 op-log）。Multica 的编辑并发度不需要。
- 不做"变更审批流"（改动前需 review 才生效）。历史是**记录**，不是**门禁**。
- 不做合规认证级别的不可篡改存证（WORM/哈希链）。设计上留扩展点，本期不实现。
- 不替换 `task_message` / 会话 transcript：那是 agent 执行过程记录，与实体变更历史是两套东西。

## 2. 现状盘点（代码事实）

现有能力集中在 `activity_log` 一张表，由事件总线监听器写入。

**表结构** — `server/migrations/001_init.up.sql:156`

```sql
CREATE TABLE activity_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID REFERENCES issue(id) ON DELETE CASCADE,
    actor_type TEXT CHECK (actor_type IN ('member', 'agent', 'system')),
    actor_id UUID,
    action TEXT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**已覆盖的 action**：

- issue 字段（`server/cmd/server/activity_listeners.go`，事件监听器写入）：`created`、`status_changed`、`priority_changed`、`assignee_changed`、`title_changed`、`description_updated`、`due_date_changed`、`start_date_changed`。
- squad（`server/internal/handler/squad.go:963`）：`squad_leader_evaluated`。
- agent 密钥（`server/internal/handler/agent_env.go:32`）：`agent_env_revealed`、`agent_env_updated` —— 这两条 `IssueID` 为空。

**读取路径**：`GET .../timeline`（`server/cmd/server/router.go:1132`），把 activity 与 comment 合并成一条 `TimelineEntry`（`packages/core/types/activity.ts`），前端会把连续同类项 coalesce（`coalesced_count`）。

**已有的版本化先例**：`autopilot_rule_version`（`server/migrations/186_autopilot_rule_version.up.sql:25`）——append-only 快照表，记录 `published_by_type/id` + `config_summary` JSONB，无 FK 无 cascade。本方案沿用这个风格。

**已有的归责能力**：`server/internal/attribution` 实现了 agent run 的"accountable human"瀑布解析（direct_human / delegation / comment 链 / autopilot rule owner / 降级 fallback），并且明确声明"attribution 是 on behalf of，不是 blame，不用于鉴权"。这是 G1 的现成基础设施。

**已有的"同事务 + fail-closed"先例**：`handler/agent_env.go` 的密钥读写审计是全仓最严格的一处 —— `CreateActivity` 用 `qtx` 在业务事务内写，写失败则**回滚业务变更并返回 500**（"audit log write failed; refusing to serve env without a recorded reveal" / "env update rolled back"）。本方案 §5.2 提出的写入语义不是新发明，而是把这个已被验证的模式提升为**全局默认**。

### 差距（本方案要解决的）

| # | 差距 | 证据 |
|---|---|---|
| D1 | **实体不可寻址**。表上只有 `issue_id` 一列。非 issue 的审计事件只能把 `issue_id` 留空、把实体 id 埋在 `details` JSONB 里（`agent_env_*` 就是这样），结果是这些行**查不出来** —— 无法回答"agent X 的变更历史"。agent / skill / autopilot / project / squad / workspace 设置的常规改动则完全没有记录。 | schema 同上；`agent_env.go:157,244`（`IssueID: pgtype.UUID{}`） |
| D2 | **description 改动没有内容**。`Details: []byte("{}")`，只知道"有人改了描述"，看不到改了什么、也无法回滚。 | `activity_listeners.go:225-233` |
| D3 | **同一次请求的多字段改动被拆散**，无 change-group 概念。一次同时改 status+assignee 会产生 2 条独立记录，无法呈现为一次变更。 | `activity_listeners.go`（每个 `if xxxChanged` 各自 `CreateActivity`） |
| D4 | **issue 路径的写入是 best-effort，非事务**（与 `agent_env.go` 的 fail-closed 形成鲜明对比）。事件总线是进程内**同步**总线（`events/bus.go:27`），但 publish 发生在业务事务提交**之后**，`CreateActivity` 失败只 `slog.Error`、不回滚、不重试；进程在 commit 与 publish 之间崩溃即静默丢失。**同一套审计表存在两种截然不同的可靠性等级**，这是最需要统一的一点。 | `events/bus.go`、`activity_listeners.go` 各 `if err != nil` 分支 vs `agent_env.go:249-254` |
| D5 | **没有工作区级审计查询**。只有 per-issue timeline，且 `LIMIT` 无游标分页；无"某人最近 30 天做了什么"的查询能力。 | `server/pkg/db/queries/activity.sql`（仅 `ListActivitiesForIssue`） |
| D6 | **无保留/归档策略**。表随 workspace 无限增长，只有 `idx_activity_log_issue` 一个索引 + 后来补的 timeline keyset 索引。 | `001_init.up.sql:175`、`068_timeline_keyset_index` |
| D7 | **缺少请求上下文**。无 `request_id`、无来源渠道（web/CLI/API/webhook/Lark）、无 IP/UA，排查"这条改动是谁从哪触发的"要靠猜。 | schema 同上 |
| D8 | **comment 编辑无历史**。`comment` 有 `updated_at` 但无旧版本。 | `001_init.up.sql:97` |

## 3. 业界调研

### 3.1 Jira —— 字段级历史的标准答案：changegroup + changeitem

Jira 用**两级模型**：`changegroup`（一次变更，带 author + created）+ `changeitem`（该次变更里每个字段的 mutation，含 `OLDVALUE/NEWVALUE` 的 ID 形式与 `OLDSTRING/NEWSTRING` 的展示形式）。

官方文档对双写 ID + 字符串的解释很关键：*"OLDVALUE 记录被改实体的 ID，OLDSTRING 记录其名称，这样即使该实体后来被从系统中删除，issue 的变更历史仍然可以正确显示。"*（[developer.atlassian.com](https://developer.atlassian.com/server/jira/platform/database-change-history/)）

**可借鉴**：
- change-group 分组 → 直接解决 D3。
- **历史必须自解释**：快照展示名，不要只存外键。agent 被归档、member 离职、skill 被删除之后，历史不能变成一堆孤立 UUID。
- Jira 把"issue 字段历史"和"管理审计日志（audit log API）"做成两套独立系统 —— 前者面向协作者，后者面向管理员/合规。这个**双系统切分**值得照抄。

### 3.2 Linear —— 协作历史与安全审计分层，审计日志 90 天

Linear 的 audit log 只覆盖账号访问、订阅、设置变更，记录 actor 的 IP 与国家，**保留 90 天**，UI 支持按事件类型过滤（[linear.app/docs/audit-log](https://linear.app/docs/audit-log)）；而 issue 的字段历史走另一条路径，在 issue 详情里内联呈现并支持回看旧版本。

**可借鉴**：
- issue 历史内联在实体页（低摩擦、给协作者看）；审计日志在 Settings/Administration 下（给 admin 看，付费/plan 门槛）。Multica 现在的 timeline 已经是前者，缺的是后者。
- 审计日志记录 IP/来源 → 印证 D7。
- 数据导出动作本身也要记进审计日志（"谁导出了工作区数据"是高价值审计事件）。

### 3.3 GitHub —— 保留期 + 流式外送，把长期留存交给客户

GitHub 的 enterprise audit log 保留期可配置（Git 事件仅 7 天，其余最长 180 天/可配置至无限），数据通过 UI、API、**streaming** 三种方式消费；流被暂停时有 7 天缓冲，超过则从"当前时间前一周"恢复；audit log API 有独立 rate limit（1750 q/h）。GitHub 明确把"更长期留存"定位为**导出到客户自己的系统**（[docs.github.com](https://docs.github.com/admin/monitoring-activity-in-your-enterprise/reviewing-audit-logs-for-your-enterprise/using-the-audit-log-api-for-your-enterprise)、[github.blog](https://github.blog/2021-09-16-audit-log-streaming-public-beta/)）。

**可借鉴**：
- **分层保留**：高频低价值事件（如 session 创建）短保留，配置/权限类事件长保留。
- 热存储在 OLTP，长期留存靠外送/归档，别让 Postgres 承担无限增长（D6）。
- 审计查询要有独立限流，避免大范围扫描打爆主库。

### 3.4 Notion / Figma / Google Docs —— 内容型改动用"快照 + 命名版本"

Figma 每约 30 分钟自动打一个 checkpoint，并允许用户**命名和标注**版本、按 page 维度查看，避免整文件历史噪音过大（[help.figma.com](https://help.figma.com/hc/en-us/articles/360038006754-View-a-file-s-version-history)、[figma blog](https://blog.figma.com/now-you-can-name-and-annotate-your-figma-version-history-250aa1b5caf5)）。Google Docs / Notion 同样是"自动快照 + 手动命名版本 + 差异高亮 + 恢复"。

**可借鉴**：
- 长文本不要每次 keystroke 存一版：**按时间窗聚合**（同一 actor 在 N 分钟内的连续编辑合并为一个 revision）。
- 快照 + 按需算 diff，比存 diff 更稳（diff 算法可换、可重算）。
- "命名版本"是低成本高价值功能，但**属于后续阶段**。

### 3.5 Salesforce —— 字段级追踪必须有配额和保留策略

标准 field history tracking：每对象最多 20 个字段，UI 保留 18 个月；升级 Field Audit Trail 后可追踪至 50 字段并通过 `HistoryRetentionPolicy` 定义长期归档（`FieldHistoryArchive`）（[Trailhead](https://trailhead.salesforce.com/content/learn/modules/field-audit-trail-basics/configure-field-audit-trail)、[Field Audit Trail Implementation Guide](https://resources.docs.salesforce.com/latest/latest/en-us/sfdc/pdf/field_history_retention.pdf)）。

**可借鉴**：
- 明确"**哪些字段被追踪**"是一份显式白名单，不是"全字段自动 diff"。全字段自动 diff 会把内部字段（position、rollup、缓存列）写进历史，产生噪音与隐私风险。
- 保留策略要是**可声明的配置**，而不是硬编码。

### 3.6 数据库层方案（评估后不采用为主路径）

- **系统版本化时态表 / temporal tables**（SQL Server、MariaDB、Postgres 触发器方案）：自动记录行的历史版本。优点是零漏记；缺点是拿不到业务语义的 actor（DB 只知道连接用户）、拿不到 change-group、且把审计逻辑埋进 DDL/触发器，难以测试和演进。
- **完整 Event Sourcing**：所有状态由事件重放得出。改造成本远超收益，且与现有 `issue` 表为真相源的架构冲突。
- **Transactional outbox**：只借用它的**一个点** —— 变更记录与业务写入同事务落库，异步 fanout 只负责推送。这是解决 D4 的正确姿势，见 §5.2。

### 3.7 调研结论（对 Multica 的直接推论）

1. **两级模型（change group + change item）是共识做法**，Multica 缺这一层。
2. **协作历史与管理审计要分开呈现**，但可以共用一套底层存储 + 不同投影/权限。
3. **长文本走快照表，不塞进 details JSONB**。
4. **追踪字段是显式白名单**，配保留策略。
5. **历史必须自解释**（快照展示名），否则实体删除后历史即腐坏。
6. **Multica 独有的一条**：actor 有很大比例是 agent。所有历史条目都必须能回答"这是哪个 agent 改的、它代表哪个人"—— 这是 `attribution` 包已经解决的问题，历史系统直接复用，不要重新发明。

## 4. 设计总览：三层

```
┌──────────────────────────────────────────────────────────────────┐
│ L2 真相源（git 对象模型）                                          │
│   entity_snapshot  每次变更后实体的完整形态（tree，含 blob 指针）    │
│   content_blob     内容寻址、workspace 内去重（blob）              │
│   append-only，与业务写入同事务                                    │
└───────────────┬──────────────────────────────────────────────────┘
                │ 相邻快照 diff（可整段重算）
┌───────────────▼──────────────────┐  ┌────────────────────────────┐
│ L1 变更事实 + 查询索引             │  │ L3 投影层                   │
│  change_group  who/when/origin/归责│  │  a) 实体 timeline（协作者）  │
│  change_item   字段级 before→after │→ │  b) 工作区审计（admin）      │
│                （派生，可重建）      │  │  c) 导出 / 外送归档          │
└──────────────────────────────────┘  └────────────────────────────┘
```

L2 是**真相**：任意时刻的实体完整形态、任意两点之间的 diff，都从这里得出。L1 回答"谁在何时因何改了哪个字段"——`change_group` 承载快照本身不含的语义（actor、来源、归责），`change_item` 是为了查询而存在的派生索引。L3 是同一份数据的两种读法加一条出口。

> 编号沿用初稿（L1 先于 L2 提出），v2 修订后 L2 在依赖上更靠底层。

## 5. 数据模型

> 遵循仓库现有约定：workspace 相关新表**不加 FK、不加 cascade**（见 `152_chat_pinned_agent`、`186_autopilot_rule_version` 的注释），完整性在应用层保证。

### 5.1 L1：change_group / change_item

```sql
-- 一次变更 = 一个 change_group（一次 API 请求 / 一次 agent 动作）
CREATE TABLE change_group (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL,

    -- WHERE：通用实体寻址（解决 D1）
    entity_type   TEXT NOT NULL,   -- issue | comment | agent | skill | autopilot
                                   -- | project | squad | member | workspace | runtime_profile
    entity_id     UUID NOT NULL,
    -- 冗余的 issue 归属，让 issue timeline 查询不用 join（issue 下的 comment 也归到 issue）
    issue_id      UUID NULL,

    -- WHO
    actor_type    TEXT NOT NULL,   -- member | agent | system
    actor_id      UUID NULL,
    actor_label   TEXT NOT NULL,   -- 当时的展示名快照（Jira OLDSTRING 思路）
    -- agent 变更的归责人，复用 attribution 包的结论
    on_behalf_of_user_id UUID NULL,
    attribution_source   TEXT NULL, -- direct_human | delegation | comment_chain | autopilot_rule | fallback

    -- WHEN
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 上下文（解决 D7）
    origin        TEXT NOT NULL DEFAULT 'unknown', -- web | desktop | mobile | cli | api | daemon | webhook | channel | system
    request_id    TEXT NULL,       -- 贯穿一次请求的 trace id
    task_id       UUID NULL,       -- agent run，可跳转 transcript
    summary_kind  TEXT NOT NULL,   -- created | updated | deleted | restored | published | executed
    ip_inet       INET NULL,       -- 仅 member + 敏感事件记录，见 §9
    user_agent    TEXT NULL
);

CREATE INDEX idx_change_group_entity  ON change_group(entity_type, entity_id, created_at DESC, id DESC);
CREATE INDEX idx_change_group_issue   ON change_group(issue_id, created_at DESC, id DESC) WHERE issue_id IS NOT NULL;
CREATE INDEX idx_change_group_actor   ON change_group(workspace_id, actor_type, actor_id, created_at DESC);
CREATE INDEX idx_change_group_ws_time ON change_group(workspace_id, created_at DESC, id DESC);

-- 该次变更里逐字段的 mutation
CREATE TABLE change_item (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id        UUID NOT NULL,
    workspace_id    UUID NOT NULL,   -- 冗余，便于分区/清理不 join
    field           TEXT NOT NULL,   -- status | priority | assignee | title | description | labels | parent | stage | ...
    -- 机器可读（ID / 枚举值 / 数字），实体删除后仍可用于统计
    from_value      JSONB NULL,
    to_value        JSONB NULL,
    -- 人可读快照（Jira OLDSTRING/NEWSTRING），实体删除后历史依然可读
    from_label      TEXT NULL,
    to_label        TEXT NULL,
    -- 长文本不入行内，指向 L2 的 content_blob（内容寻址）
    from_blob_hash  TEXT NULL,
    to_blob_hash    TEXT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_change_item_group ON change_item(group_id);
CREATE INDEX idx_change_item_field ON change_item(workspace_id, field, created_at DESC);
```

设计要点：

- **一次请求一个 group**：解决 D3。前端可以渲染成"Eve 12:16 将状态改为 in_review 并把负责人改为 Alice"。
- **entity_type + entity_id 而非 issue_id**：解决 D1，agent/skill/autopilot 改动共用同一张表，不再一类实体开一张历史表。
- **双写 value + label**：解决"实体被删/被归档后历史腐坏"。
- **`field` 白名单驱动**（Salesforce 教训）：见 §6。
- **append-only**：无 UPDATE / DELETE 路径，只有归档任务能搬走旧行。

**分区带来的一个硬约束（已实测）**：§8 要求 `change_group` 按 `created_at` 月度分区，而 Postgres 要求分区表上的唯一约束必须包含所有分区列：

```
ERROR:  unique constraint on partitioned table must include all partitioning columns
DETAIL:  PRIMARY KEY constraint on table "cg_bad" lacks column "created_at"
         which is part of the partition key.
```

因此最终 DDL 的主键必须是 **`PRIMARY KEY (id, created_at)`** 而非 `id` 单列，且 `change_item.group_id` 无法建 FK 指向它（正好与仓库"新表不加 FK"的约定一致）。同理 `entity_snapshot` 也分区时，`uq_entity_snapshot_rev` 需要并入分区列（`content_blob` 不按时间分区，它是内容寻址的，按 `workspace_id` 哈希分区更合适）。这个约束决定表结构，**必须在 Phase 1 建表时定下**，事后改动等于重建表。

上面 §5.1 的 DDL 已在 PostgreSQL 16 上实际执行通过（非分区形式），作为字段与索引定义的基线；§5.3 的 v2 快照表 DDL 同样已实测建表通过。

### 5.2 写入语义：同事务 + 异步 fanout（解决 D4）

现状是"业务事务提交 → publish 事件 → 监听器另开事务写 activity_log，失败仅记日志"。改为：

```
BEGIN
  UPDATE issue SET ...              -- 业务写入
  INSERT INTO change_group ...      -- 变更事实，同一个 tx
  INSERT INTO change_item ...
COMMIT
  → publish change:recorded 事件（携带已落库的 group id）
      → realtime 广播 / inbox / 通知（这些仍可 best-effort）
```

- 业务改动成功 ⟺ 历史记录存在。不再有"改了但没记"的窗口。
- **fail-closed 是默认**：历史 INSERT 在同一事务内，失败即整体回滚、请求返回 5xx。这与 `agent_env.go` 现有行为一致（"env update rolled back"）。理由：一次失败的更新用户会重试，而一条丢失的历史无人察觉。若后续发现某类高频路径因此可用性下降，再针对该路径单独降级，而不是一开始就全局 best-effort。
- 事件总线退回它擅长的角色：**推送**，不是**持久化**。realtime 推送失败只影响即时刷新，重新拉 timeline 即可恢复一致。
- 落地上收敛到一个 `changelog.Recorder` 抽象（在 service 层，接收 `pgx.Tx`），而不是继续在 `cmd/server/activity_listeners.go` 里逐字段复制粘贴 —— 现状那个文件 317 行里同一段 CreateActivity+errorlog 模板重复了 8 次，新增一个字段就要再抄一遍，这是 D3/D4 的根因。
- 幂等：`(entity_type, entity_id, request_id, field)` 作为去重依据，重放同一请求不产生第二条历史。

### 5.3 L2：全量快照对象层（git 风格）

> **本节在 v2 修订**（Yushen 在 MUL-5520 的评审意见）：从"只给长文本存 revision"改为"**每次变更全量快照整个实体**，diff 一律读时计算"。理由见 §5.3.1。

#### 5.3.1 为什么值得这么做，以及 git 凭什么敢这么做

git 的逻辑模型确实是**全量快照**（每个 commit 指向一棵完整的 tree），但它敢这么做依赖三个机制，缺一个就不成立：

1. **内容寻址去重** —— blob 以内容哈希命名，两个 commit 里内容相同的文件在对象库里**只有一份**。改一个字符的文件，只有那个 blob 是新的。
2. **打包时的 delta 压缩** —— packfile 在存储层对相似对象做 delta + zlib，逻辑上全量、物理上增量。
3. **GC / 可达性回收** —— 不可达对象会被清掉。

如果我们只抄"每次存一份完整 JSON"而不抄这三条，得到的不是 git，而是一张按写入次数线性膨胀、且同一份描述被抄了 200 遍的表。所以下面的设计是**按 git 的对象模型来拆**，而不是按"加一列 snapshot JSONB"来做。

采纳它的收益不只是 review 方便：

- **任意两点对比**，而不只是相邻两次变更。存 diff 只能顺着链条累加，存快照可以直接 `diff(snapshot_a, snapshot_b)`，"这个 issue 从上周一到今天被改成了什么样"是一次查询。
- **时间旅行**：任意时刻的实体完整形态可直接取出，不需要重放。
- **diff 算法可换**：行级换词级、换语义 diff、修 diff 的 bug，都不需要迁移历史数据。存 diff 则算法一变，旧历史就不可解释。
- **索引可重建**：`change_item` 从"真相源"降级为"派生索引"。这顺带解决了 §5.4 里那个隐患 —— 现在有业务逻辑（squad leader 去重、assignee 频次）依赖审计表，一旦审计表漏写就是静默错误；快照成为真相源之后，索引可以整段重算校验。

#### 5.3.2 三个对象

```sql
-- git blob：内容寻址、workspace 内去重。长文本 / 文件内容都进这里。
CREATE TABLE content_blob (
    workspace_id  UUID NOT NULL,
    content_hash  TEXT NOT NULL,      -- sha256(内容)，与 workspace_id 共同构成主键
    content       TEXT NULL,          -- 小于阈值时内联
    storage_key   TEXT NULL,          -- 超阈值时落对象存储（复用 internal/storage）
    byte_size     INT NOT NULL,
    ref_count     BIGINT NOT NULL DEFAULT 0,  -- 供归档 GC 判断可达性
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, content_hash)
);

-- git tree + commit：一次变更后该实体的完整形态
CREATE TABLE entity_snapshot (
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL,
    entity_type        TEXT NOT NULL,
    entity_id          UUID NOT NULL,
    revision_no        BIGINT NOT NULL,   -- 每 (entity_type, entity_id) 单调递增
    parent_snapshot_id UUID NULL,         -- 形成链，可检测"中间是否被别人插入过改动"
    group_id           UUID NULL,         -- 对应的 change_group（谁/何时/从哪来）
    -- 全量追踪字段的规范化序列化结果。长文本字段在这里只存 blob 哈希指针，
    -- 不内联正文 —— 这是控制体积的关键（见 §5.3.4）。
    tree               JSONB NOT NULL,
    snapshot_hash      TEXT NOT NULL,     -- sha256(canonical(tree))，未变则不产生新快照
    label              TEXT NULL,         -- 命名版本（Figma 思路，后续阶段）
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)          -- 分区约束，见 §5.1
);

CREATE UNIQUE INDEX uq_entity_snapshot_rev
    ON entity_snapshot(entity_type, entity_id, revision_no);
CREATE INDEX idx_entity_snapshot_entity
    ON entity_snapshot(entity_type, entity_id, created_at DESC);
CREATE INDEX idx_entity_snapshot_hash
    ON entity_snapshot(workspace_id, snapshot_hash);
```

`change_group` / `change_item`（§5.1）保留不变，但**定位改变**：

| | v1 | v2（本节修订后） |
|---|---|---|
| `entity_snapshot` + `content_blob` | 不存在 | **真相源**，append-only |
| `change_group` | 真相源 | 仍是真相源（who / when / origin / 归责，快照本身不含这些语义） |
| `change_item` | 真相源 | **派生索引**，由相邻两个快照 diff 得出，可整段重算 |

保留 `change_item` 不是冗余 —— git 也一样：对象库存快照，但 `git log --follow` / `git blame` 之所以慢正是因为要反复算 diff，git 为此加了 commit-graph、pack bitmap 等缓存。我们的查询场景（"谁改过 status"、"最近 30 天 assignee 变更频次"、审计按字段过滤）如果只有快照，就等于每次都全表扫 + 逐对 diff。**快照做真相，索引做查询**，两者职责分明。

#### 5.3.3 "全量"必须有边界（三条硬约束）

Yushen 的原话是"全量的把所有内容都记录下来"。方向采纳，但**"全量"指的是全量的追踪字段，不是全量的表列**，原因不是洁癖而是三个具体的坑：

1. **密钥会被抄进一张永不删行的表。** `agent.custom_env` 是 `agent` 表上的**明文 JSONB**（`migrations/040_agent_custom_env.up.sql:5`，`handler/agent_env.go` 里没有任何加密调用，reveal 接口直接返回 plaintext）。对 `agent` 做全行快照，等于把 `ANTHROPIC_API_KEY`、AWS 凭证按每次改动复制若干份进历史。**这是本次修订里唯一不可妥协的一条**：白名单不但不能取消，还必须升级成 Recorder 层的结构性拦截 —— 敏感字段在进入 `tree` 之前替换为 `{"__redacted": true, "keys": [...]}`。
2. **高频无意义列会制造快照风暴。** `issue.position FLOAT`（`001_init.up.sql:68`，拖拽排序，由 `internal/issueposition` 维护）和 `updated_at` 每次都变。若纳入 `tree`，每一次拖拽都会产生一个新快照且 `snapshot_hash` 必然不同，去重完全失效。这两类列必须排除在 `tree` 之外。
3. **`position FLOAT` 参与哈希本身就不安全** —— 浮点的文本化没有跨语言/跨版本的稳定保证，同一个值可能序列化出不同字符串，导致 `snapshot_hash` 抖动。凡是要进哈希的数值列都得先定死格式化规则，或者干脆排除。

#### 5.3.4 canonical serialization：这是本方案最容易做错的地方

git 能靠哈希做去重，前提是对象有**严格且唯一**的字节表示。我们的 `tree` 是 JSONB，而"同一个实体状态"可以有多种 JSON 写法，因此必须先把序列化规则定死，否则哈希去重会静默失效（表现为：什么都没改却每次都产生新快照）。

下面四条在 PostgreSQL 16 上实测过，结论直接决定实现方式：

| 检验 | 结果 | 对设计的影响 |
|---|---|---|
| `{"a":1,"b":2}` 与 `{"b":2,"a":1}` 的 jsonb 哈希 | **相同** | key 排序由 jsonb 自身归一化，**不需要**应用层再排序 |
| 值改走再改回（todo → done → todo） | 第 1、3 版哈希**相同**，第 2 版不同 | 哈希去重按预期工作，"改回原值不产生新快照"成立 |
| `{"a":1,"b":null}` 与 `{"a":1}` | 哈希**不同** | `null` 与"字段缺失"确实是两个不同状态，必须在装配层二选一并强制统一，否则同一状态会产生两种哈希 |
| `0.1` 字面量 vs `0.3::float8 - 0.2::float8` | `{"p": 0.1}` vs **`{"p": 0.09999999999999998}`** | 浮点参与哈希会直接产生假变更。`issue.position FLOAT` 绝不能进 `tree`；任何数值列进哈希前必须定点化 |

据此确定的规则：

- key 顺序：**交给 jsonb**，不自己排（实测已归一化）；
- `null` vs 缺失：**统一用"缺失"表示空值**，装配时剔除空字段；
- 时间戳：统一 UTC + 固定毫秒精度；
- 数值：定点字符串，**禁止浮点**；
- **集合类字段（labels、squad members、skills）必须定义稳定排序**，否则同一组 label 换个顺序就算变更（jsonb 只归一化对象 key，**不会**归一化数组顺序）；
- 长文本字段在 `tree` 里只出现 blob 哈希，形如 `{"description": {"blob": "sha256:..."}}`。

另外，"一个 issue 的全量内容"**不是一行数据**：labels 在 `issue_to_label`，还有 `issue_property`、`issue_pull_request` 等关联。所以快照必须是**装配出来的 tree**（正如 git 的 tree 由多个 blob 组成），需要显式定义每种 entity_type 的装配范围与顺序 —— 这是 Phase 1 的主要设计工作量所在。

#### 5.3.5 体积

真正会炸的从来不是"存全量结构化字段"，而是**把长文本内联进每一份快照**。一个 20 KiB 描述的 issue 被编辑 100 次，内联 = 2 MiB，指针化 + 哈希去重 = 100 个指针 + 若干个实际不同的 blob。

去掉长文本之后，一份 issue 快照的结构化字段规范化后约 400–800 B，JSONB 落 TOAST 还会再压。相比 v1 的"一次单字段变更写 1 group + 1 item"，全量快照在**单字段变更**时略贵（约 2×），在**多字段变更**时反而更省（只多一份快照，而不是多 N 条 item）。数量级可接受 —— 但这仍需 §11 那份真实增量数据才能定稿。

保留其余控量手段：

- **哈希去重**：`snapshot_hash` 与上一版相同则不写新行（改了空格又改回来、幂等重放的 PUT 都会被吃掉）。
- **编辑窗口聚合**（Figma 30 分钟 checkpoint 思路，我们取 5 分钟）：同一 actor 对同一实体在窗口内的连续编辑**覆盖**同一快照，只留窗口末态。注意这是**有损**的，代价是窗口内的中间态不可回溯 —— 这是一个需要明确接受的取舍，若要求"每次保存都可回溯"就必须关掉聚合并接受体积。
- **大文本外置**：blob 超过阈值（建议 64 KiB）落对象存储。

#### 5.3.6 两个安全边界

- **去重只能在 workspace 内做。** 跨 workspace 共享 blob 看起来很省，但会变成一个存在性预言机（用内容哈希探测"别的团队有没有这份文档"），并让一次去重逻辑的 bug 直接升级为跨租户数据泄漏。因此 `content_blob` 主键是 `(workspace_id, content_hash)`，**不做**全局去重。
- **可擦除性。** 正文集中在 `content_blob`，GDPR 类删除请求可以只清 `content` / `storage_key` 而保留快照骨架与变更事实，历史链不断裂。

#### 5.3.7 回滚

回滚 = 读取目标快照 → 走**正常的业务更新路径**写回 → 自然产生一个新的 `change_group`（`summary_kind = restored`）和一个新快照。历史永不被改写，这一点与 git 的 revert（新 commit，而非改写历史）一致。


### 5.4 与 activity_log 的关系（兼容策略）

`activity_log` 不删、不改结构、不做数据迁移改写。

- **新写入**全部走 `change_group/change_item`。
- 读取侧提供一个 **union 投影**：timeline 查询同时读新表与 `activity_log`（老数据），按 `created_at` 归并。`activity_log` 里的 `action='status_changed'` + `details.from/to` 可无损映射为 `field='status'` 的 change_item，映射在读取层做，不落库。
- `activity_log` 上现存的两个特殊消费者必须保持工作：`HasSquadLeaderNoActionEvaluationForTask`（squad leader 评估去重）和 `CountAssigneeChangesByActor`（assignee 频次推荐）。这两个是**业务逻辑依赖审计表**的反模式，迁移时需要同时在新表提供等价查询，并在新旧交叠期读 union，否则会出现"squad leader 重复评估"和"推荐列表突然变空"这类静默回归。
- `agent_env_revealed` / `agent_env_updated` 老行的 `issue_id` 为空、agent id 埋在 `details` 里，映射到新表时 `entity_type='agent'`、`entity_id` 从 `details->>'agent_id'` 取。这类行数量少、价值高（密钥审计），是唯一值得考虑做一次性回填的对象。
- 老数据在保留期内自然过期，不做一次性回填（回填一张审计表收益低、风险高）。

## 6. 追踪字段白名单（= 快照 tree 的组成范围）

显式声明，代码内为常量表，而非"对结构体做反射 diff"。

> **v2 说明**：改为全量快照后，这份白名单**不会消失**，只是角色变了 —— 它从"记录哪些字段的变更"变成"**哪些列参与组装快照 tree**"。理由见 §5.3.3：密钥列必须排除、高频派生列必须排除、参与哈希的列必须格式稳定。

| 实体 | 追踪字段 | 备注 |
|---|---|---|
| issue | status, priority, assignee, title, description, due_date, start_date, parent, stage, project, labels | description 走 L2 |
| comment | body, resolved | 解决 D8，body 走 L2 |
| agent | name, model, instructions, skills, runtime_profile, access_scope, archived | instructions 走 L2；**custom_env 只记 key 名，永不记 value** |
| skill | name, description, files, labels | 文件内容走 L2 |
| autopilot | enabled, trigger, target, instructions, schedule | 与 `autopilot_rule_version` 对齐，见下 |
| project | name, description, resources | |
| squad | name, leader, members, roles | |
| workspace | settings, integrations, retention_policy | 审计高价值 |
| member | role, status | 权限变更，审计高价值 |

**显式不追踪**：`position`（拖拽排序，高频无意义）、各 `*_usage_*` 汇总列、`updated_at`、缓存/派生列、`task_usage_*` rollup 状态。这些若进历史将淹没信号。

**与 autopilot_rule_version 的关系**：该表已经在 dispatch 路径上被当作"取最新 rule owner"的**功能性**数据源使用（不只是审计），因此**不合并、不废弃**。change_group 记录 autopilot 的变更事实，`autopilot_rule_version` 继续作为 publish 快照的真相源；两者通过 `entity_id` 关联，`change_item.to_value` 里带上 version id。

## 7. 读取路径与 API

### 7.1 实体 timeline（协作者视角，扩展现有能力）

```
GET /api/issues/{id}/timeline?cursor=&limit=      # 已存在，改为 union 新旧表 + 游标分页
GET /api/{entity_type}/{id}/history?cursor=&limit=  # 新增：agent/skill/autopilot/project/squad
```

响应沿用并扩展 `TimelineEntry`（`packages/core/types/activity.ts`），新增 group 语义：

```ts
export interface ChangeGroupEntry {
  type: "change";
  id: string;
  actor_type: "member" | "agent" | "system";
  actor_id: string | null;
  actor_label: string;
  on_behalf_of_user_id?: string | null;
  attribution_source?: string | null;
  origin: string;
  task_id?: string | null;
  created_at: string;
  // 该次变更后的实体完整形态，用于"对比/时间旅行/回滚"入口
  snapshot_id: string;
  parent_snapshot_id?: string | null;
  items: Array<{
    field: string;
    from_value?: unknown; to_value?: unknown;
    from_label?: string | null; to_label?: string | null;
    // 长文本字段返回 blob 指针，正文与 diff 按需二次请求
    from_blob?: string | null; to_blob?: string | null;
  }>;
}
```

前端现有的 `coalesced_count` 合并逻辑（`packages/views/issues/surface/activity.ts`）在有了真正的 group 之后可以简化：**服务端分组优于客户端猜测**。

### 7.2 快照、diff 与回滚

```
GET  /api/snapshots/{entity_type}/{id}?cursor=&limit=      # 快照列表（元数据，不含正文）
GET  /api/snapshots/{snapshot_id}                          # 某一时刻实体的完整形态
GET  /api/snapshots/{entity_type}/{id}/at?t=<RFC3339>      # 时间旅行：≤ t 的最近快照
GET  /api/snapshots/diff?from={snap}&to={snap}&mode=line|word
                                                           # 任意两点对比，服务端计算
POST /api/snapshots/{snapshot_id}/restore                  # 回滚，产生新快照 + 新 change_group
```

`from` / `to` 可以是**任意两个**快照，不限于相邻 —— 这是快照模型相对"存 diff"的核心能力差，也是 review 场景（"这个 issue 这一周被改成了什么样"）真正需要的形态。diff 一律读时计算、不落库，因此 diff 算法可以随时替换而不影响历史数据。

### 7.3 工作区审计（admin 视角，新增）

```
GET /api/workspace/audit?actor_type=&actor_id=&entity_type=&field=&from=&to=&origin=&cursor=
GET /api/workspace/audit/export?format=csv|jsonl          # 导出动作本身写入审计（Linear 做法）
```

- **仅 workspace admin 可访问**；agent **不可**通过普通 task token 访问审计接口（否则 agent 可以自查/自证，审计价值归零）。
- 独立限流（GitHub 的 1750 q/h 是个合理量级参考），避免宽时间窗查询打爆主库。
- 强制 `from/to` 时间窗上限（如单次查询 ≤ 31 天），配合 `idx_change_group_ws_time` 走索引扫描。

## 8. 保留与归档

分层保留（GitHub + Salesforce 的组合）：

| 类别 | 示例 | 热存储（Postgres） | 归档 |
|---|---|---|---|
| 高频协作变更 | issue status/priority/assignee | 12 个月 | 冷归档 |
| 内容版本 | description / instructions / skill 文件 | 最近 N=50 版 + 12 个月 | 冷归档，命名版本永不过期 |
| 安全/权限/配置 | member role、workspace settings、integration、token | 24 个月 | 冷归档，保留更久 |
| 噪音类 | session 创建等 | 90 天 | 不归档 |

- 保留期是 **workspace 级可声明配置**（`HistoryRetentionPolicy` 思路），不是硬编码常量。
- 归档任务复用现有 cron 基础设施（`sys_cron_executions` + `internal/scheduler`），按月批量导出 JSONL 到对象存储后删除热数据。
- `change_group` 按 `created_at` **月度分区**（Postgres declarative partitioning），归档 = detach partition，避免大批量 DELETE 造成膨胀与 autovacuum 压力。这个决定要**在建表时就做**，事后改分区代价高得多。
- 容量预估需要真实数据支撑：上线前先用 `activity_log` 现有行的日增速 × 预期字段覆盖倍数（约 3–5×，因为覆盖实体从 1 类扩到 10 类）做一次估算，作为分区粒度与保留期的输入。**这是本设计尚未闭环的一项，见 §11。**

**全量快照与保留策略的冲突（v2 新增）**：快照模型的核心卖点是"任意两点可对比"，而稀释旧快照会**直接破坏**这个能力 —— 一旦删掉中间快照，被删区间内就只能给出近似而非精确的 diff。git 面对同样的问题，答案是 GC + shallow clone（明确承认"浅历史里就是拿不到完整 diff"）。我们必须显式选一条，不能含糊：

| 选项 | 代价 |
|---|---|
| A. 热窗口内全留，出窗后整段归档到对象存储 | 老历史仍完整但查询慢（需从冷存储拉取重建），实现最复杂 |
| B. 出窗后稀释为 checkpoint（每月首尾 + 命名版本 + 每 N 版留一版） | 便宜，但被稀释区间的精确 diff 永久丢失 |
| C. 结构化字段全留（体积小），只稀释 `content_blob` 正文 | 折中：字段级历史永久精确，长文本老版本按需失效 |

倾向 **C**：体积主要来自正文而不是字段，`change_item` 索引本身就很小且可永久保留，用户对"三年前的描述长什么样"的需求远低于"三年前谁把状态改了"。但这是产品取舍，需要 Yushen 拍。已列入 §11。

## 9. 权限、隐私与安全

- **租户隔离**：所有查询强制 `workspace_id` 谓词；`entity_type + entity_id` 是通用寻址，若漏了 workspace 过滤就会成为跨租户读取漏洞。这是本设计**最高风险点**，需在 repository 层强制（函数签名必带 workspace_id，禁止裸 entity 查询）+ 加针对性回归测试。
- **可见性继承**：历史条目的可见性 = 其实体的可见性。私有 agent、access-scope 受限 agent（`handler/agent_access.go`）、私有 project 的历史不得因为审计接口而泄漏给无权成员。
- **密钥零留存**：`custom_env`、token、PAT、webhook secret 的**值**永不写入历史，只记 key 名与"已变更"。这条要在 Recorder 层做**结构性拦截**（字段白名单 + 敏感字段 redact 列表），不能依赖调用方自觉。**全量快照模型下这一条从"应该做"升级为"必须做"**：`agent.custom_env` 是明文 JSONB（`migrations/040_agent_custom_env.up.sql:5`，`handler/agent_env.go` 无加密调用），若快照照抄整行，API key 会被复制进一张 append-only、长保留、且设计上不允许删行的表 —— 泄漏面比现状严重得多。redact 必须发生在**进入 `tree` 之前**，且需要一条"任何快照的 tree 里不得出现敏感 key 的 value"的回归测试。
- **PII 最小化**：`ip_inet` / `user_agent` 仅对 member actor 的安全类事件记录（登录、权限、集成、导出），普通 issue 字段变更不记。
- **删除语义**：实体被删除时历史**保留**（否则"删掉就没痕迹"），但正文内容需可按 GDPR 类请求擦除 —— 因此正文集中在 `content_blob`（可单独擦除 content/storage_key，保留快照骨架与变更事实）。
- **去重不跨 workspace**：见 §5.3.6。跨租户共享 blob 会形成存在性预言机。
- **不可篡改性**：本期只做 append-only（无 UPDATE/DELETE 代码路径）。哈希链/WORM 留作扩展点，不实现。
- **归责 ≠ 追责**：沿用 `attribution` 包的既定立场（"attribution is on behalf of, never blame and never authorization"）。历史里的 `on_behalf_of_user_id` 不参与任何鉴权判断。

## 10. 分阶段落地

每阶段独立可上线、可回滚，不做 big-bang。

**Phase 1 — 快照骨架与统一写入**
- 建 `content_blob` / `entity_snapshot` / `change_group` / `change_item`（含月度分区与 `PRIMARY KEY (id, created_at)`）。
- **定义 issue 的 canonical tree**：装配范围（issue 行 + `issue_to_label` + `issue_property`）、字段排除清单（`position` / `updated_at` / 派生列）、序列化规则（§5.3.4）。这是本阶段最容易被低估的工作量。
- 抽出 `changelog.Recorder`，在 issue 写路径接入，快照与 `change_group` 与业务写入同事务；`change_item` 由相邻快照 diff 得出。
- timeline 读取改为 union 新表 + `activity_log`，加游标分页。
- `activity_log` 保持双写**一个版本周期**，并为 `HasSquadLeaderNoActionEvaluationForTask` / `CountAssigneeChangesByActor` 提供新表等价查询后再停写。
- 验收：一次多字段更新产生 1 快照 + 1 group + N item；**改一个字段再改回原值，`snapshot_hash` 与两版前一致**（证明规范化序列化正确，这是最能暴露序列化 bug 的一条断言）；把 `change_item` 整表清空后能从快照重算出完全一致的结果；注入 DB 错误时业务更新与历史一起回滚。

**Phase 2 — 内容 blob、diff 与时间旅行**
- 长文本入 `content_blob`（关闭 D2）：issue description、agent instructions、skill 文件。
- 编辑窗口聚合 + 哈希去重 + 大文本外置对象存储。
- 任意两点 diff API + 时间旅行 API + 前端"查看改动 / 对比 / 恢复"。
- 验收：任选两个非相邻快照能出行级 diff；连续编辑 5 分钟内只产生 1 个快照；两个 issue 用同一段描述只存 1 个 blob；回滚产生新快照而非改写历史。

**Phase 3 — 实体扩展与审计视图**
- 扩展至 agent / skill / autopilot / project / squad / member / workspace settings。
- workspace 审计查询 + 导出 + 独立限流 + admin 权限门禁。
- 敏感字段 redact 拦截与回归测试。
- 验收：改 agent 模型/权限范围能在审计里查到；`custom_env` value 在任何历史查询里都不可见；非 admin 与 agent token 访问审计接口返回 403。

**Phase 4 — 保留、归档与增强（可选）**
- 保留策略配置 + 月度归档任务 + partition detach。
- 命名版本、审计外送（streaming/webhook）。

## 11. 开放问题（需决策，不在本稿内定）

1. **容量**：`activity_log` 当前日增行数与表体积尚未实测；分区粒度（月/周）与默认保留期需要这个数字才能定稿。
2. **`activity_log` 停写时机**：双写期多长？两个业务查询（squad leader 去重、assignee 频次）切换到新表是否需要一次性回填？
3. **审计接口的商业化门槛**：Linear 把 audit log 放在付费 plan。Multica 是否也做 plan 门槛，影响 API 与 UI 的 gating 位置。
4. **命名版本的范围**：只给 skill 文件/agent instructions，还是也给 issue description？
5. **agent 高频写入**：agent 在一次 run 内多次更新同一 issue（常见）是否需要在 group 层再做一次 run 级聚合，避免 timeline 被单个 agent run 刷屏。
6. **是否需要 `deleted` 实体的历史入口**：实体删除后，历史从哪个 UI 入口可达（仅审计视图，还是提供"已删除实体"列表）。
7. **（v2）快照稀释策略选 A / B / C**：见 §8 的表。直接决定"三年前的描述能不能精确 diff"。
8. **（v2）编辑窗口聚合是否可接受有损**：5 分钟窗口内的中间态不可回溯。若产品要求"每次保存都能回退"，需关闭聚合并接受体积上升。
9. **（v2）comment 是否也纳入全量快照**：comment 数量远大于 issue，全量快照的边际成本主要来自这里。可以只对"被编辑过的 comment"建快照。
