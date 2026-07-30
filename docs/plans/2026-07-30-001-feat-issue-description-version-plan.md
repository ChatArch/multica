# Issue description 版本历史 —— 最终方案（只新增一张表）

> **状态**：最终方案，等待实现。范围已由 Yushen 收窄为**只新增 `issue_description_version` 一张表**，其余字段的变更历史继续用现有 `activity_log`。
>
> **范围变更说明**：本文 v1–v3 设计的是覆盖所有实体的通用版本管理（`entity_snapshot` / `content_blob` / `change_group` / `change_item`）。经评审判定为对当前需求的过度设计，已撤下 —— 那部分设计保留在本分支的 git 历史里（`c9d3cceea` → `11628bd1a` → `3a283099b`），将来真要给 agent instructions、skill 文件做版本历史时再按实际共性抽象，不为假设需求提前付复杂度。本文 v4 起只讲一件事：**issue description 的版本、diff、恢复**。
>
> 本方案与 [MUL-5470](https://multica-app.copilothub.ai/multica-ai/issues/7ec44e6e-604e-4980-9ce4-57e5fd85a8d7) / PR #6129 是同一件事。#6129 已实现该表但被两轮独立 review 判定不可合并（9 条 correctness blocker），本文的 §2 与 §5 就是针对那 9 条给出的修正版设计。

## 1. 目标

| # | 能力 |
|---|---|
| G1 | 记录每次 description 编辑后的**全文** |
| G2 | 知道**谁**在**什么时间**改的（member / agent，agent 还要能回溯到代表哪个人） |
| G3 | **任意两版**之间计算 diff |
| G4 | 能**恢复**旧版本 |
| G5 | 最关键的那次对比要存在：**人的原始诉求 → agent 的第一次改写** |

不做：其他字段的文档式历史（title 等的 activity 已带 from/to）、分支与合并、逐段评论、手动命名快照、通用实体版本管理。

## 2. 表结构（最终）

以 #6129 的 `235_issue_description_version.up.sql` 为基线，做 6 处修改。

```sql
CREATE TABLE IF NOT EXISTS issue_description_version (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL,
    issue_id          UUID NOT NULL,

    -- 线性版本链。NULL 标记 V0（第一次被追踪的编辑之前的内容）。
    parent_version_id UUID,
    -- 每个 issue 内单调递增。给稳定排序，同时是并发写的结构性闸门（见 §5.2）。
    version_no        INT NOT NULL,

    -- WHO
    actor_type        TEXT CHECK (actor_type IN ('member', 'agent', 'system')),
    actor_id          UUID,
    -- agent 写入时的归责人，复用 server/internal/attribution 的解析结果。
    -- "on behalf of"，不参与任何鉴权判断。
    on_behalf_of_user_id UUID,
    -- agent run 边界。agent 的编辑会话由它切分，不看时钟。
    source_task_id    UUID,

    -- 内容
    content           TEXT NOT NULL,
    -- sha256(content)。用来判定 no-op —— 不做跨 issue / 跨 workspace 去重。
    content_hash      TEXT NOT NULL,
    -- 相对 parent_version_id 的行数增删。仅本行，不累加。
    added_lines       INT NOT NULL DEFAULT 0,
    removed_lines     INT NOT NULL DEFAULT 0,

    -- 编辑会话标识。**只用于读取时折叠自动保存**，不决定是否覆盖历史行。
    edit_session_id   UUID NOT NULL,
    -- 普通编辑为 NULL；恢复时记录来源版本，让 UI 能说"恢复到了 X 月 X 日那版"。
    restored_from_version_id UUID,

    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

与 #6129 现状的差异，以及每一处的理由：

| # | 改动 | 理由 |
|---|---|---|
| 1 | **删掉 `updated_at`** | 历史行**永不 UPDATE**。#6129 靠 `UpdateIssueDescriptionVersionContent` 原地改写"当前会话行"，这正是 review 复现出的 P1：3 次写入 → 2 个版本但 3 条 timeline，三条徽标 `+1/+2/+3` 而 `version_id` 相同，点开早期那条看到的是最终 diff。有 `updated_at` 就说明这张表被当成可变表在用。这一列删掉，`UpdateIssueDescriptionVersionContent` 这个 query 一并删除。 |
| 2 | 新增 `version_no` | 稳定排序不再依赖时间戳；配合唯一约束成为防并发双 root 的闸门。 |
| 3 | 新增 `content_hash` | no-op 判定按**内容值**。`issue.go:2913` 的 `descriptionChanged` 比的是两个 `*string` **指针**（`textToPtr` 返回 `*string`，见 `handler.go:426`），所以任何带 description 的 PUT 都被判为已变更 —— review 实测相同内容 PUT 也会产生假版本。版本写入**不得复用**那个布尔量。 |
| 4 | 新增 `edit_session_id` | 把"会话"从**存储语义**降级为**展示语义**。行按每次写入追加，折叠只发生在读取时，于是既有"一次编辑在时间线上只有一行"，又不需要可变历史，中间态也不丢。 |
| 5 | 新增 `on_behalf_of_user_id` | G2 要求 agent 能回溯到人。`internal/attribution` 已经算好了这个值，直接落。 |
| 6 | 新增 `restored_from_version_id` | 让恢复在 UI 上可自解释，也便于区分"更新"与"恢复"的通知文案。 |

索引与约束：

```sql
-- 保留 #6129 的读取索引（两条读路径都以 issue_id 开头且要 newest-first）
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_description_version_issue
    ON issue_description_version (issue_id, created_at DESC, id DESC);

-- 读取时按会话折叠要用
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_description_version_session
    ON issue_description_version (issue_id, edit_session_id, version_no);

-- 并发防线一：同一 issue 的 version_no 唯一，并发写必有一方撞约束并重试
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_issue_description_version_no
    ON issue_description_version (issue_id, version_no);

-- 并发防线二：禁止版本链分叉（没有分支语义，分叉一律是并发 bug）
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_issue_description_version_parent
    ON issue_description_version (issue_id, parent_version_id)
    WHERE parent_version_id IS NOT NULL;

-- 并发防线三：每个 issue 只能有一个 V0。
-- ⚠️ 这一条不能省，也不能被上一条替代 —— 已实测：上一条的 WHERE 把
-- parent IS NULL 排除在外，而且 Postgres 唯一索引默认视 NULL 互不相等，
-- 第二个 root 会被干净地插进去。MUL-5470 那个"双 root"就是这么来的。
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_issue_description_version_root
    ON issue_description_version (issue_id)
    WHERE parent_version_id IS NULL;
```

沿用仓库约定：**不加外键、不加级联**（删除清理见 §5.4），每条 `CREATE INDEX CONCURRENTLY` 单独成文件。

正文直接存 Postgres，靠 TOAST 压缩，**不引对象存储、不做内容寻址 blob 表**。理由是体积先测再说：描述几 KB 量级，一次编辑会话按停顿数产生 10–50 行，一个被反复编辑的 issue 也就几百 KB。真的涨得离谱再拆，不提前建设。

## 3. 何时产生一个新版本（核心逻辑）

分两个独立的判定，**不要混为一谈** —— 这是 #6129 出问题的根源。

### 3.1 判定一：是否**追加一行**

```
是否写入新的 version 行？
├─ 新内容的 sha256 == 最新行的 content_hash ？
│    └─ 是 → 不写。不产生行、不产生 timeline 条目。（修正"假版本"blocker）
├─ 该 issue 还没有任何 version 行？
│    ├─ issue 刚创建且描述非空 → 写 V0：parent=NULL, version_no=1,
│    │                            actor=创建者, 标记 is_original
│    └─ 存量 issue 首次被编辑 → 先 lazy seed V0（内容 = 编辑前的描述,
│                                actor='system'）, 再走下一条
└─ 其余情况 → 追加一行：parent = 最新行 id, version_no = 最新 + 1
```

三点说明：

- **V0 必须存在**，这是 G5。没有它，整个功能里最有价值的那次对比（人的原始诉求 → agent 的第一次改写）恰好是不存在的那次。
- **新建 issue 就种 V0**，而不是只在首次编辑时 lazy seed。#6129 只做了 lazy seed；补上创建时种子后，"创建时的描述"这个语义就不依赖于"后来有没有人编辑过"。存量 issue 没有历史，仍然需要 lazy seed 兜底，UI 要如实说明"历史从上线日开始"。
- **历史行永不 UPDATE**。任何"改写当前行"的实现都会重新引入徽标与 diff 打架。

### 3.2 判定二：这一行属于**哪个编辑会话**

会话只影响**展示折叠**，不影响是否写行。

```
新行的 edit_session_id = ?
├─ 是 restore 操作 → 全新 session（强制）
├─ actor 与最新行的 actor 不同 → 全新 session
├─ actor 是 agent：
│    ├─ source_task_id 与最新行相同 → 沿用最新行的 session（同一 run 内不论隔多久）
│    └─ 不同 → 全新 session
└─ actor 是 member：
     ├─ 最新行 created_at 距今 < 10 分钟 → 沿用最新行的 session
     └─ ≥ 10 分钟 → 全新 session
```

- **agent 按 run 切**（`source_task_id`）比按时钟准确得多：一次 run 可能跨几十分钟，中间穿插工具调用，但语义上是一次改写。
- **member 按 10 分钟空闲窗口切**，因为 1.5s 防抖自动保存不提供别的信号。窗口取 10 分钟：足够跨过阅读/思考的停顿，又短到"午饭回来接着写"算作新版本。
- **restore 必须强制开新会话。** #6129 写测试时抓到的坑：恢复若按普通编辑参与折叠，会并进当前那个还开着的会话，把**正要恢复走的那个版本覆盖掉** —— 恰好摧毁这个功能存在的理由。现在历史行不可变，后果没那么致命，但"恢复"和"接着打字"在语义上必须是两件事，否则 timeline 会把恢复藏进一次普通编辑里。

### 3.3 timeline 条目：一个会话一条

- **只在新会话开始时**写一条 `description_updated` activity；同一会话内的后续写入**不再新增** activity。这直接修掉"3 次写入 3 条 timeline"。
- activity 的 `details` 存 **`edit_session_id` 与 `from_version_id`**（= 该会话第一行的 parent），**不存** `to_version_id`。因为会话还会继续增长，存了就必须更新，就又回到可变数据。
- 徽标 ±行数在**读取时**解析：`diff(from_version.content, 该 session 当前最后一行.content)`。timeline 查询用一次 `LEFT JOIN LATERAL` 取每个 session 的最后一行即可。这样徽标和点开后的 diff 永远来自同一对版本，**结构上不可能打架**。
- 前端**不要**再对 `description_updated` 做显示层折叠 —— 服务端已经保证一会话一条。#6129 当初把它从前端折叠里排除掉的判断是对的，但前提（服务端已折叠 activity）当时并不成立，现在才成立。

## 4. UI：怎么让用户看到版本之间的改动

三个入口，按价值排序。

### 4.1 时间线内联 diff（主入口）

时间线上那条从

```
Ada 更新了描述
```

变成

```
Ada 改写了描述 · +42 −7 · 查看变更 ▸
```

点开**就地展开**统一 diff（unified、源码、3 行上下文、行内词级高亮），不跳页不开弹窗。

- 这是最高价值的位置：**"agent 刚改完描述"这一刻**就在时间线上，把一个审计功能变成复查功能。
- agent 写的版本额外显示 `来自 Ada 的第 N 次运行` 并链到 transcript。
- diff 过长时截断，底部给"在历史面板中查看完整对比"。

### 4.2 描述块下方的历史入口

描述区下面一行弱化的元信息：

```
最后由 Ada 编辑 · 3 小时前 · 历史
```

- 只有 V0、从未编辑过的 issue **完全不显示这一行**（没历史就不该占位）。
- 这一行同时承担 P1 的"自你上次查看以来有变更"提示：若描述在你上次打开之后被改过，这行变成带强调色的 `Ada 在你上次查看后改写了描述 · 查看变更`。专门服务"你不在的时候 agent 干了活"。

### 4.3 历史面板（对比的主场）

左右两栏：

**左栏 · 版本列表**（newest first）

每条：作者头像（人/agent 可区分）· 相对时间 · `+N −M` 徽标 · agent 版本附 run 链接 · V0 标为 `原始`。

- **默认按会话折叠**：一次编辑会话显示为一条。顶部一个开关 `显示会话内的自动保存`，打开后中间态以缩进子项展开 —— 这就是"行按写入追加"换来的好处：中间态没丢，只是默认不打扰你。
- 列表上限 200 条（读取保护）。

**右栏 · diff**

- 默认显示：**选中版本 vs 它的父版本**。
- **任意两版对比**：列表里选一条作为基准（`设为对比基准`），再点另一条，右栏切换成这两版的 diff，顶部显示 `v3 → v7 · 共 4 次编辑 · 参与者 2 人`。这是全量快照模型真正买到的能力，UI 必须把它露出来，否则等于白存。
- 视图切换：`统一 / 并排`、`源码 / 渲染`（渲染态 diff 放 P1）、`词级高亮`默认开。
- **整篇重写特判**：相似度低于阈值时自动切成左右并排全文对照，而不是画满屏红绿。agent 改写描述基本都会命中这条，属于高频而非边缘情况。
- **空 → 有内容**用文案特判为"首次填写描述"（全绿新增），**不要**判成"整篇重写" —— 没东西被覆盖就不该吓唬人。有内容 → 空同理。

**每条版本上的操作**

`恢复此版本` → 先弹窗展示"即将应用的 diff"再确认。恢复后时间线出现一条 `dev 将描述恢复到 7 月 28 日的版本`（文案与"更新"区分），且**被恢复走的那一版仍在列表里**。权限与编辑描述一致。

### 4.4 移动端

只读：时间线显示 ±行数徽标，点开全屏 diff。语义与 web 对齐、布局可不同。web 的渲染组件因为 `react-dom` 依赖不能复用，但纯计算层可以（见 §5.5）。

## 5. 实现上不能省的七件事

这七条是 #6129 那 9 条 blocker 的根因。**模型（一张表存全文）本身没问题，出问题的是写入路径没有收敛。**

### 5.1 版本写入与描述更新同事务，fail-closed

```
BEGIN
  SELECT ... FROM issue WHERE id = $1 FOR UPDATE     -- 见 5.2
  UPDATE issue SET description = ...
  INSERT INTO issue_description_version ...
  INSERT INTO activity_log ...（仅新会话时，带 edit_session_id / from_version_id）
COMMIT
```

#6129 是在 description 写入**提交之后**才 best-effort 记版本，失败只打日志。理由不成立：一次失败的编辑用户会重试，而一条丢失的版本没人察觉。仓库里已有 fail-closed 的先例 —— `handler/agent_env.go` 的密钥审计写失败会回滚业务并返回 500。

### 5.2 并发

- 事务内 `SELECT ... FOR UPDATE` 锁 issue 行，再读"最新版本"。
- 三条唯一索引（§2）作为最后防线，冲突方**重试**而非忽略。
- **禁止把查询错误当成"没有历史"**。#6129 是 `latest, err := GetLatest...; if err != nil { seed V0 }` —— 一次瞬时 DB 错误就会凭空种下第二个 root。必须区分 `pgx.ErrNoRows` 与真错误，后者向上传播。
- 验收：同一 issue 并发发两次首编辑，重复 20 次，**零**双 root、零 sibling。（#6129 实测 18/20 失败。）

### 5.3 写入路径必须穷举

所有会改 `issue.description` 的路径都走**同一个函数**：单条 update、**batch update**、restore、CLI `issue update --description-file`、GitHub triage / webhook 同步、autopilot。

#6129 的 batch update 接受 `updates.description` 但既不记版本也不发 `description_changed`，实测 live 描述已改而版本数与 activity 数都是 0、原文不可恢复。这类遗漏是静默的，所以要加**结构性测试**：断言"description 变了 ⟹ 必有对应 version 行"，新 handler 绕过就让测试失败。

### 5.4 删除清理，且在事务内

这张表**没有外键**，所以不会随 workspace 级联删除 —— `activity_log` 有 `ON DELETE CASCADE` 才安全，这张表没有。三处都要显式清理，且与主删除同事务：

- `DeleteIssue`（#6129 有，但不在事务内）
- **批量删除 issue**（#6129 没有）
- **`DeleteWorkspace`**（#6129 没有；实测删 workspace 后仍残留版本行）。已有手工清扫无 FK 表的先例：`handler/workspace.go:797` 显式扫 `chat_pinned_agent`，`:785` 先 `LockWorkspaceForDelete`。

### 5.5 diff 计算的分工

- 徽标 ±行数由 **Go** 算（`server/internal/util/linediff.go`），diff 正文由 **TS** 算并渲染（`packages/core/issues/description-diff.ts`，纯函数无 DOM，三端可共用）。两边共用同一套 LCS 语义、测试表逐条对齐 —— 否则会出现"徽标和它自己的 diff 打架"。这套分层 #6129 已经建好，保留。
- **不引入 `@pierre/diffs`**：它 peer 依赖 `react-dom`，进不了 `packages/core` 的计算层（仓库硬约束），也覆盖不了移动端，且 Shadow DOM 与我们 Tailwind token / `next-themes` 的兼容未验证。渲染器是可替换的纯渲染层决定，等验证过再换，不阻塞本期。

### 5.6 前端缓存

query client 是全局 `staleTime: Infinity`（`packages/core/query-client.ts:7`），所以历史相关 query **必须显式 invalidate**：本地 mutation 成功后、收到 `issue:updated` realtime 后、restore 后。否则症状是"新建 issue → 编辑描述 → 历史入口根本不出现，必须整页刷新"（#6129 实测）。

### 5.7 恢复不触发意外的 agent run

恢复会重新走 `issue:updated / description_changed`，从而重新触发 @mention 的订阅与通知。已确认描述里的 mention **只通知 member，不会派发 agent run**（`dispatchIssueRun` 只由 assignee / status 变化触发），所以恢复不会把 agent 拉起来。通知文案要能区分"更新"与"恢复"。

## 6. 接口

沿用 #6129 已注册的三条（`router.go:1133-1135`），语义按本方案调整，并新增两条：

```
GET  /api/issues/{id}/description/versions            # 列表，不含正文；支持 ?fold=session|all
GET  /api/issues/{id}/description/versions/{vid}      # 单版本正文
POST /api/issues/{id}/description/versions/{vid}/restore
GET  /api/issues/{id}/description/versions/{vid}/diff?from={vid2}   # 新增：任意两版
GET  /api/issues/{id}/timeline                        # 已存在，description 条目改为
                                                      # 每会话一条 + 读取时解析徽标
```

列表响应移除 `updated_at`，新增 `version_no` / `edit_session_id` / `restored_from_version_id` / `on_behalf_of_user_id`，保留 `is_original`（= `parent_version_id IS NULL`）。

## 7. 分期

**P0（本期）**
1. 表 + 4 个索引 + 唯一约束；删掉 `updated_at` 与 `UpdateIssueDescriptionVersionContent`
2. §3 的两个判定（追加行 / 会话归属）+ 创建时种 V0 + 存量 lazy seed
3. §5 的七件事（同事务 fail-closed、并发、写路径穷举、删除清理、计算分工、缓存失效、恢复语义）
4. 时间线内联 diff（一会话一条 + 读取时解析徽标）
5. 历史面板：会话折叠列表 + 父版本 diff + **任意两版对比** + 恢复
6. 移动端只读

**P1**
7. `multica issue description versions | diff | restore` CLI —— **让 agent 也能读 diff**。agent 每次运行都要重读整篇描述，若能问"人在我上次运行之后改了什么"，既省 token 又更准。这是最有差异化的一条。
8. "自上次查看以来有变更"提示条 + inbox 通知直达 diff
9. 渲染态 markdown diff
10. 并发编辑检测（保存时带版本 id，不匹配则拒绝并提示"描述已被 Ada 修改，看过差异再保存"）

**明确不做**：保留期裁剪（先测体积）、对象存储、命名版本、通用实体版本管理。

## 8. 与 MUL-5492 的重叠

MUL-5470 拆出的 [MUL-5492](https://multica-app.copilothub.ai/multica-ai/issues/a473423e-02ce-42a6-8542-226931250dc2) 修两件事：timeline 的 `ORDER BY created_at ASC LIMIT 2000` 取的是**最旧**的 2000 条（超限后新动态全部消失且无提示），以及 WS 广播剥离 `prev_description` / `prev_title`（现在每次自动保存都把描述全文推给整个工作区的所有在线连接）。

两件都与本方案相邻但独立。本方案把 description activity 从"每次写入一条"降到"每会话一条"，会**显著降低** 2000 上限的触发概率，但不能替代那个修复。**确认 MUL-5492 的进展再动**，不要两边各修一次。
