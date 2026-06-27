# 周报自动生成系统 — 技术方案文档

**版本**: v1.0  
**日期**: 2026-06-26  
**技术栈**: Go + SQLite + 飞书生态  
**设计目标**: 轻量级、单二进制部署、无中间件依赖

---

## 1. 技术架构

### 1.1 整体架构

```
┌──────────────────────────────────────────────────────────────┐
│                        用户终端层                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────────────┐   │
│  │  Web浏览器 │  │ 飞书 APP  │  │ 飞书群（机器人消息）      │   │
│  └──────────┘  └──────────┘  └──────────────────────────┘   │
└──────────────────────┬───────────────────────────────────────┘
                       │ HTTPS
┌──────────────────────▼───────────────────────────────────────┐
│                      API 网关层（Go/Gin）                     │
│  • 路由分发 / 认证中间件（JWT） / 请求限流 / 日志记录            │
└──────────────────────┬───────────────────────────────────────┘
                       │
┌──────────────────────▼───────────────────────────────────────┐
│                      业务服务层（Go）                          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
│  │ 认证服务  │  │ 数据源服务│  │ 生成服务  │  │ 模板服务    │  │
│  │ (飞书OAuth)│  │ (Git/飞书)│  │ (周报生成)│  │ (模板管理)  │  │
│  └──────────┘  └──────────┘  └──────────┘  └────────────┘  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
│  │ 周报服务  │  │ 导出服务  │  │ 提醒服务  │  │ 同步服务    │  │
│  │ (CRUD)   │  │ (MD/Word/│  │ (定时任务/│  │ (双向同步)  │  │
│  │          │  │  PDF)    │  │  飞书Bot) │  │             │  │
│  └──────────┘  └──────────┘  └──────────┘  └────────────┘  │
└──────────────────────┬───────────────────────────────────────┘
                       │
┌──────────────────────▼───────────────────────────────────────┐
│                      数据存储层                                │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
│  │  SQLite  │  │ 本地文件  │  │ 配置文件  │  │  日志文件   │  │
│  │ 主存储    │  │ 导出缓存  │  │ (YAML)  │  │             │  │
│  └──────────┘  └──────────┘  └──────────┘  └────────────┘  │
└──────────────────────┬───────────────────────────────────────┘
                       │
┌──────────────────────┬──────────────────┬────────────────────┐
│                      │                  │                    │
┌──────────────┐  ┌────▼──────┐  ┌─────────▼─────┐  ┌─────────▼──┐
│ 飞书开放平台  │  │ GitLab   │  │  GitHub      │  │  其他系统   │
│ • 用户身份   │  │ • 提交    │  │  • 提交       │  │  （扩展）   │
│ • 任务/项目  │  │ • 分支    │  │  • 仓库       │  │             │
│ • 日历/会议  │  │ • 仓库    │  │               │  │             │
│ • 群机器人   │  │           │  │               │  │             │
└──────────────┘  └──────────┘  └───────────────┘  └────────────┘
```

### 1.2 模块职责说明

| 模块 | 职责 | 关键技术点 |
|------|------|-----------|
| **认证服务** | 飞书 OAuth 登录、登出、JWT 签发与校验 | 飞书 Open API、JWT、Cookie/Session |
| **数据源服务** | 管理 Git/飞书数据源配置，执行数据同步 | 接口抽象、并发控制、增量同步、错误重试 |
| **生成服务** | 汇聚多源数据，去重关联，生成结构化周报 | 文本聚合算法、相似度计算、模板渲染 |
| **模板服务** | 模板的 CRUD、版本管理、角色/团队绑定 | Go Template、JSON Schema 校验 |
| **周报服务** | 周报的保存、编辑、提交、查询、状态管理 | 事务控制、乐观锁（防并发修改） |
| **导出服务** | Markdown/Word/PDF 生成与下载 | 文件流处理、内存优化、异步生成（大文件） |
| **提醒服务** | 定时任务扫描、飞书机器人消息发送 | Cron 表达式、飞书 Webhook、失败重试 |
| **同步服务** | 周报到飞书任务/文档的反向同步 | 飞书 API、幂等性控制、冲突处理 |

---

## 2. 技术选型

### 2.1 后端技术栈

| 组件 | 选型 | 版本 | 说明 |
|------|------|------|------|
| Web 框架 | **Gin** | v1.9+ | 轻量、高性能、生态成熟 |
| ORM | **GORM** | v2 | 快速开发，支持 SQLite、自动迁移 |
| 数据库 | **SQLite** | 3 | 零配置、单文件、适合 MVP 和中小团队 |
| 数据库驱动 | **modernc.org/sqlite** | - | 纯 Go 实现，无需 CGO |
| 缓存 | **go-cache** | v2 | 内存缓存，单机场景足够 |
| 定时任务 | **robfig/cron/v3** | v3 | 标准 Cron 库，支持秒级 |
| 配置管理 | **Viper** | v1.16+ | 支持 YAML/JSON/环境变量/命令行参数 |
| 日志 | **Zap** | v1.24+ | 高性能结构化日志 |
| 认证 | **golang-jwt/jwt/v5** | v5 | JWT 签发与校验 |
| 模板渲染 | **Go html/template** | 标准库 | 无额外依赖 |
| Markdown 导出 | 原生生成 | - | 无依赖，直接拼接字符串 |
| Word 导出 | **unioffice** | v1 | 或 **docx** 库 |
| PDF 导出 | **wkhtmltopdf** + 调用 | - | 或 **gofpdf** |
| 密码加密 | **bcrypt** | v4 | 用于系统管理员本地账号（备用） |
| 请求 HTTP | **net/http** | 标准库 | 调用飞书/Git API |
| JSON 处理 | **encoding/json** | 标准库 | 无额外依赖 |
| 时间处理 | **time** | 标准库 | Go 标准库时间处理 |
| 命令行 | **cobra** | v1.7+ | 可选，用于 CLI 命令 |

### 2.2 前端技术栈

| 组件 | 选型 | 说明 |
|------|------|------|
| 模板引擎 | **Go html/template** | 后端渲染，无需前端构建 |
| 交互增强 | **HTMX** | 通过 HTML 属性实现 AJAX，无 JS 代码 |
| CSS 框架 | **Tailwind CSS (CDN)** | 原子化 CSS，无需构建 |
| 图表 | **Chart.js (CDN)** | 历史对比、趋势图 |
| 图标 | **Heroicons / FontAwesome** | 轻量图标库 |

### 2.3 部署方案

| 方案 | 说明 | 适用 |
|------|------|------|
| 方案 A | 单二进制文件 + 配置文件 + SQLite 文件 | 本地测试、个人使用 |
| 方案 B | Docker 容器化（单容器） | 标准部署、CI/CD |
| 方案 C | Docker Compose（应用 + Nginx 反向代理） | 生产环境、多实例 |

---

## 3. 数据库设计（SQLite）

### 3.1 E-R 关系图

```
┌──────────┐       ┌──────────────┐       ┌──────────────┐
│  users   │       │   teams      │       │ data_sources │
│  ─────   │       │   ──────     │       │ ──────────── │
│  id (PK) │       │  id (PK)     │       │ id (PK)      │
│  name    │       │  name        │       │ user_id (FK) │
│  email   │       │  leader_id   │◄──────│ type         │
│  role    │       │  default_    │       │ config (JSON)│
│  team_id │──────►│  template_id │       │ enabled      │
│  open_id │       └──────────────┘       │ last_sync_at │
└──────────┘                             └──────────────┘
     │                                          │
     │         ┌──────────────┐                 │
     │         │ work_records │                 │
     │         │ ──────────── │                 │
     │         │ id (PK)      │                 │
     └────────►│ user_id (FK) │                 │
               │ source_id(FK)│◄────────────────┘
               │ source_type  │
               │ external_id  │
               │ week_start   │
               └──────────────┘
     │
     │         ┌──────────────┐
     │         │weekly_reports│
     │         │ ──────────── │
     │         │ id (PK)      │
     └────────►│ user_id (FK) │
               │ week_start   │
               │ content(JSON)│
               │ status       │
               │ template_id  │
               └──────────────┘
               │
               │    ┌─────────────────────┐
               │    │ weekly_report_      │
               │    │ versions            │
               │    │ ──────────────────  │
               └───►│ report_id (FK)      │
                    │ version             │
                    │ content             │
                    └─────────────────────┘
```

### 3.2 表结构定义

```sql
-- 用户表（飞书登录后自动创建）
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    feishu_open_id TEXT NOT NULL UNIQUE,    -- 飞书用户唯一标识
    feishu_union_id TEXT,                      -- 飞书 Union ID（跨应用）
    name TEXT NOT NULL,
    email TEXT,
    avatar TEXT,
    role TEXT NOT NULL DEFAULT 'member',       -- admin / team_lead / member
    team_id INTEGER,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 团队表
CREATE TABLE teams (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    leader_id INTEGER,
    default_template_id INTEGER,               -- 团队默认模板
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 数据源配置表
CREATE TABLE data_sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    type TEXT NOT NULL CHECK(type IN ('gitlab', 'github', 'feishu_task', 'feishu_calendar')),
    name TEXT NOT NULL,                        -- 用户自定义名称，如"公司GitLab"
    config TEXT NOT NULL,                      -- JSON 格式存储配置（Token、URL等）
    enabled BOOLEAN DEFAULT 1,
    last_sync_at DATETIME,
    sync_status TEXT DEFAULT 'pending',        -- pending / success / failed
    sync_error TEXT,                           -- 上次同步错误信息
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 工作记录表（从各数据源同步的原始数据）
CREATE TABLE work_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    source_id INTEGER NOT NULL,                -- 关联 data_sources.id
    source_type TEXT NOT NULL,                 -- git / feishu_task / feishu_calendar / manual
    record_type TEXT NOT NULL,                 -- commit / task / meeting / manual
    title TEXT NOT NULL,
    description TEXT,
    project_name TEXT,
    external_id TEXT,                          -- 外部系统ID（commit_hash / task_id / event_id）
    url TEXT,                                  -- 外部系统链接（可点击跳转）
    metadata TEXT,                             -- JSON 扩展字段（代码变更量、会议时长等）
    occurred_at DATETIME NOT NULL,
    week_start DATE NOT NULL,                  -- 归属周（周一日期），便于快速查询
    is_hidden BOOLEAN DEFAULT 0,               -- 用户是否手动隐藏
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, source_type, external_id)   -- 联合唯一，防止重复
);

-- 模板表
CREATE TABLE templates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    description TEXT,
    content TEXT NOT NULL,                     -- Go Template 模板内容
    applicable_roles TEXT,                     -- JSON 数组，如 ["developer", "tester"]
    applicable_teams TEXT,                     -- JSON 数组
    scope TEXT NOT NULL DEFAULT 'personal',   -- global / team / personal
    owner_id INTEGER,                          -- 创建人（团队模板为 team_leader，个人模板为本人）
    is_default BOOLEAN DEFAULT 0,
    version INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 周报表
CREATE TABLE weekly_reports (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    week_start DATE NOT NULL,
    week_end DATE NOT NULL,
    template_id INTEGER,
    content TEXT NOT NULL,                     -- JSON 格式：{"work":"...","problems":"...","plans":"..."}
    status TEXT NOT NULL DEFAULT 'draft',      -- draft / submitted / approved
    submitted_at DATETIME,
    approved_at DATETIME,
    approved_by INTEGER,
    version INTEGER DEFAULT 1,                 -- 编辑版本号
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, week_start)
);

-- 周报版本快照表（用于审计与回滚）
CREATE TABLE weekly_report_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    report_id INTEGER NOT NULL,
    content TEXT NOT NULL,
    version INTEGER NOT NULL,
    created_by INTEGER,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 飞书机器人配置表
CREATE TABLE feishu_bot_configs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    webhook_url TEXT NOT NULL,
    secret TEXT,                               -- 可选签名密钥
    target_type TEXT NOT NULL DEFAULT 'group', -- group / user
    target_id TEXT,                            -- 群ID或用户ID
    enabled BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 提醒记录表
CREATE TABLE reminders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    week_start DATE NOT NULL,
    sent_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    channel TEXT NOT NULL DEFAULT 'feishu',     -- feishu / email / dingtalk
    status TEXT NOT NULL,                        -- sent / failed
    error_msg TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 操作审计日志表
CREATE TABLE audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    action TEXT NOT NULL,                      -- login / generate / submit / export / sync / config
    target_type TEXT,                          -- weekly_report / data_source / template
    target_id INTEGER,
    details TEXT,                              -- JSON 格式操作详情
    ip_address TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 索引优化
CREATE INDEX idx_work_records_user_week ON work_records(user_id, week_start);
CREATE INDEX idx_work_records_external ON work_records(user_id, source_type, external_id);
CREATE INDEX idx_weekly_reports_user_week ON weekly_reports(user_id, week_start);
CREATE INDEX idx_audit_logs_user ON audit_logs(user_id, created_at);
```

---

## 4. 接口设计（REST API）

### 4.1 接口清单

#### 认证模块
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/auth/feishu` | 跳转飞书授权页 |
| GET | `/auth/feishu/callback` | 飞书 OAuth 回调，设置 Cookie |
| POST | `/auth/logout` | 登出，清除 Cookie |
| GET | `/api/user/me` | 获取当前用户信息 |
| PUT | `/api/user/me` | 更新个人信息 |

#### 数据源模块
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/data-sources` | 获取当前用户的数据源列表 |
| POST | `/api/data-sources` | 添加数据源 |
| GET | `/api/data-sources/:id` | 获取数据源详情 |
| PUT | `/api/data-sources/:id` | 更新数据源 |
| DELETE | `/api/data-sources/:id` | 删除数据源 |
| POST | `/api/data-sources/:id/test` | 测试连接 |
| POST | `/api/data-sources/:id/sync` | 手动触发同步 |
| GET | `/api/work-records` | 获取工作记录（支持按周、按类型筛选） |
| PUT | `/api/work-records/:id` | 编辑工作记录（仅手动记录） |
| PUT | `/api/work-records/:id/hide` | 隐藏/显示工作记录 |

#### 周报模块
| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/weekly/generate` | 生成周报草稿 |
| GET | `/api/weekly/current` | 获取当前周周报（草稿或已提交） |
| GET | `/api/weekly/:id` | 获取周报详情 |
| PUT | `/api/weekly/:id` | 更新周报内容（保存草稿） |
| POST | `/api/weekly/:id/submit` | 提交周报 |
| POST | `/api/weekly/:id/approve` | 审批周报（团队负责人） |
| GET | `/api/weekly/history` | 获取历史周报列表 |
| GET | `/api/weekly/:id/versions` | 获取周报版本历史 |
| POST | `/api/weekly/:id/rollback` | 回滚到指定版本 |
| GET | `/api/weekly/compare` | 对比两周周报（参数：week1, week2） |

#### 导出模块
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/weekly/:id/export?format=markdown` | 导出 Markdown |
| GET | `/api/weekly/:id/export?format=word` | 导出 Word |
| GET | `/api/weekly/:id/export?format=pdf` | 导出 PDF |
| POST | `/api/weekly/batch-export` | 批量导出（团队负责人） |

#### 模板模块
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/templates` | 获取模板列表（按可见范围） |
| GET | `/api/templates/:id` | 获取模板详情 |
| POST | `/api/templates` | 创建模板 |
| PUT | `/api/templates/:id` | 更新模板 |
| DELETE | `/api/templates/:id` | 删除模板 |
| POST | `/api/templates/:id/default` | 设为默认模板 |

#### 团队管理模块
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/team/members` | 获取团队成员列表 |
| GET | `/api/team/submission-status` | 获取本周提交状态统计 |
| POST | `/api/team/remind` | 手动发送提醒 |
| GET | `/api/team/statistics` | 获取团队统计数据（趋势图） |

#### 系统管理模块
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/admin/audit-logs` | 查询操作日志 |
| GET | `/api/admin/system-config` | 获取系统配置 |
| PUT | `/api/admin/system-config` | 更新系统配置 |
| POST | `/api/admin/bot-configs` | 配置飞书机器人 |
| GET | `/api/admin/bot-configs` | 获取机器人配置列表 |

### 4.2 关键接口详细定义

#### 生成周报草稿

```
POST /api/weekly/generate
Header: Authorization: Bearer <jwt>
Content-Type: application/json

Request:
{
    "week_start": "2024-01-15"  // 本周一日期，可选，默认当前周
}

Response:
{
    "code": 0,
    "message": "success",
    "data": {
        "id": 123,
        "week_start": "2024-01-15",
        "week_end": "2024-01-19",
        "template_id": 1,
        "content": {
            "work": "【订单系统】\n- 完成任务 T-001：支付接口联调（飞书任务）\n- 提交代码 5 次：优化支付回调性能（Git: order-service, commit: a1b2c3d）\n- 参与需求评审会议（飞书日历：1/16 14:00-15:00）\n",
            "problems": "",
            "plans": "",
            "learning": ""
        },
        "status": "draft",
        "auto_stats": {
            "commit_count": 5,
            "task_count": 2,
            "meeting_count": 3,
            "manual_count": 0
        },
        "created_at": "2024-01-19T10:00:00Z"
    }
}
```

#### 对比两周周报

```
GET /api/weekly/compare?week1=2024-01-08&week2=2024-01-15
Header: Authorization: Bearer <jwt>

Response:
{
    "code": 0,
    "data": {
        "week1": { "week_start": "2024-01-08", "commit_count": 12, "task_count": 3, "meeting_count": 5 },
        "week2": { "week_start": "2024-01-15", "commit_count": 8, "task_count": 2, "meeting_count": 3 },
        "diff": {
            "commit_count": -4,
            "task_count": -1,
            "meeting_count": -2
        },
        "trend": [  // 近8周趋势
            {"week": "2024-W01", "commits": 10, "tasks": 2},
            {"week": "2024-W02", "commits": 12, "tasks": 3},
            {"week": "2024-W03", "commits": 8, "tasks": 2}
        ]
    }
}
```

---

## 5. 关键算法设计

### 5.1 本周工作摘要生成算法

```go
// 核心逻辑：按项目分组，去重关联，生成结构化文本
func GenerateWorkSummary(userID int, weekStart, weekEnd time.Time) (string, error) {
    // 1. 拉取本周所有工作记录
    records := fetchWorkRecords(userID, weekStart, weekEnd)
    
    // 2. 按项目分组
    groups := make(map[string][]WorkRecord)
    for _, r := range records {
        groups[r.ProjectName] = append(groups[r.ProjectName], r)
    }
    
    // 3. 同项目内去重：任务ID优先，Git提交作为补充
    for project, items := range groups {
        groups[project] = deduplicateAndMerge(items)
    }
    
    // 4. 生成文本
    var sb strings.Builder
    for project, items := range groups {
        sb.WriteString(fmt.Sprintf("【%s】\n", project))
        for _, item := range items {
            switch item.RecordType {
            case "task":
                sb.WriteString(fmt.Sprintf("- 完成任务 %s：%s（飞书任务）\n", item.ExternalID, item.Title))
            case "commit":
                sb.WriteString(fmt.Sprintf("- 提交代码：%s（Git: %s, commit: %s）\n", 
                    item.Title, item.ProjectName, item.ExternalID[:8]))
            case "meeting":
                sb.WriteString(fmt.Sprintf("- 参与会议：%s（飞书日历：%s）\n", 
                    item.Title, item.OccurredAt.Format("1/2 15:04")))
            }
        }
        sb.WriteString("\n")
    }
    
    return sb.String(), nil
}

// 去重规则：基于任务ID和提交消息相似度
func deduplicateAndMerge(items []WorkRecord) []WorkRecord {
    seen := make(map[string]bool)
    var result []WorkRecord
    
    for _, item := range items {
        // 如果已有相同任务ID，跳过
        if item.RecordType == "task" && seen[item.ExternalID] {
            continue
        }
        
        // 如果 Git 提交消息与已有任务标题相似度 > 80%，合并到任务下
        if item.RecordType == "commit" {
            merged := false
            for _, r := range result {
                if r.RecordType == "task" && similarity(r.Title, item.Title) > 0.8 {
                    r.Metadata = "含代码提交"  // 标记
                    merged = true
                    break
                }
            }
            if merged { continue }
        }
        
        seen[item.ExternalID] = true
        result = append(result, item)
    }
    
    return result
}
```

### 5.2 智能去重与关联算法

```go
// 基于编辑距离的相似度计算（简化版）
func similarity(a, b string) float64 {
    // 可替换为 Levenshtein 距离或更高级的文本相似度算法
    // MVP 阶段可用简单包含判断
    if strings.Contains(a, b) || strings.Contains(b, a) {
        return 0.9
    }
    return 0.0
}

// 从 Git 提交消息中提取任务 ID（如 "#T-123" 或 "T-123"）
func extractTaskID(commitMessage string) string {
    re := regexp.MustCompile(`[T#-](\d{3,})`)
    match := re.FindString(commitMessage)
    return match
}
```

---

## 6. 飞书集成技术实现

### 6.1 飞书 OAuth 登录

```go
package feishu

import (
    "encoding/base64"
    "encoding/json"
    "fmt"
    "net/http"
    "strings"
)

type Client struct {
    AppID     string
    AppSecret string
}

// 获取用户授权 URL
func (c *Client) GetAuthURL(redirectURI, state string) string {
    return fmt.Sprintf(
        "https://open.feishu.cn/open-apis/authen/v1/index?app_id=%s&redirect_uri=%s&state=%s",
        c.AppID, redirectURI, state,
    )
}

// 用 code 换 access_token
func (c *Client) GetAccessToken(code string) (string, error) {
    url := "https://open.feishu.cn/open-apis/authen/v1/oidc/access_token"
    body := fmt.Sprintf(`{"grant_type":"authorization_code","code":"%s"}`, code)

    req, _ := http.NewRequest("POST", url, strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", fmt.Sprintf("Basic %s", c.basicAuth()))

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    var result struct {
        Code int `json:"code"`
        Data struct {
            AccessToken string `json:"access_token"`
        } `json:"data"`
    }
    json.NewDecoder(resp.Body).Decode(&result)

    if result.Code != 0 {
        return "", fmt.Errorf("feishu error: %d", result.Code)
    }
    return result.Data.AccessToken, nil
}

// 获取用户信息
func (c *Client) GetUserInfo(accessToken string) (*UserInfo, error) {
    url := "https://open.feishu.cn/open-apis/authen/v1/user_info"
    req, _ := http.NewRequest("GET", url, nil)
    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var result struct {
        Code int `json:"code"`
        Data struct {
            OpenID string `json:"open_id"`
            Name   string `json:"name"`
            Email  string `json:"email"`
            Avatar string `json:"avatar_url"`
        } `json:"data"`
    }
    json.NewDecoder(resp.Body).Decode(&result)

    return &UserInfo{
        OpenID: result.Data.OpenID,
        Name:   result.Data.Name,
        Email:  result.Data.Email,
        Avatar: result.Data.Avatar,
    }, nil
}

func (c *Client) basicAuth() string {
    auth := c.AppID + ":" + c.AppSecret
    return base64.StdEncoding.EncodeToString([]byte(auth))
}
```

### 6.2 飞书群机器人提醒

```go
package feishu

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "net/http"
    "strconv"
    "strings"
    "time"
)

// 发送机器人消息
type BotMessage struct {
    MsgType string `json:"msg_type"`
    Content struct {
        Text string `json:"text"`
    } `json:"content"`
}

func SendBotMessage(webhookURL, secret, text string) error {
    msg := BotMessage{
        MsgType: "text",
    }
    msg.Content.Text = text

    body, _ := json.Marshal(msg)

    req, _ := http.NewRequest("POST", webhookURL, strings.NewReader(string(body)))
    req.Header.Set("Content-Type", "application/json")

    // 如果有签名密钥，添加签名
    if secret != "" {
        timestamp, sign := generateSign(secret)
        req.Header.Set("X-Lark-Request-Timestamp", timestamp)
        req.Header.Set("X-Lark-Request-Nonce", sign)
    }

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    return nil
}

// 生成飞书机器人签名
func generateSign(secret string) (timestamp, sign string) {
    timestamp = strconv.FormatInt(time.Now().Unix(), 10)
    stringToSign := fmt.Sprintf("%v\n%s", timestamp, secret)
    h := hmac.New(sha256.New, []byte(stringToSign))
    h.Write([]byte(""))
    sign = base64.StdEncoding.EncodeToString(h.Sum(nil))
    return
}
```

### 6.3 定时任务（周五提醒）

```go
package main

import (
    "github.com/robfig/cron/v3"
    "time"
)

func setupReminder(service *service.ReminderService) {
    c := cron.New()

    // 每周五 10:00 执行
    c.AddFunc("0 10 * * 5", func() {
        weekStart := getCurrentWeekStart()
        unsentUsers := service.FindUnsentUsers(weekStart)

        for _, user := range unsentUsers {
            service.SendFeishuReminder(user)
        }
    })

    c.Start()
}

func getCurrentWeekStart() time.Time {
    now := time.Now()
    weekday := int(now.Weekday())
    if weekday == 0 { weekday = 7 } // 周日算作7
    return now.AddDate(0, 0, -weekday+1).Truncate(24 * time.Hour)
}
```

---

## 7. Git 接入技术实现

### 7.1 GitLab API 接入

```go
package git

import (
    "encoding/json"
    "fmt"
    "net/http"
    "net/url"
    "time"
)

// GitLab 提交记录
 type GitLabCommit struct {
    ID        string `json:"id"`
    Message   string `json:"message"`
    AuthorName string `json:"author_name"`
    CreatedAt string `json:"created_at"`
}

// GitLab 数据源实现
type GitLabSource struct {
    Token     string
    ServerURL string  // 如 https://gitlab.company.com
    ProjectPath string // 如 "group/project"
}

// 获取本周提交
func (g *GitLabSource) FetchCommits(authorEmail string, weekStart, weekEnd time.Time) ([]GitLabCommit, error) {
    // 1. URL 编码项目路径
    encodedPath := url.PathEscape(g.ProjectPath)

    // 2. 默认使用 gitlab.com
    baseURL := g.ServerURL
    if baseURL == "" { baseURL = "https://gitlab.com" }

    // 3. 构造请求 URL
    apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/commits?since=%s&until=%s&per_page=100",
        baseURL, encodedPath, weekStart.Format(time.RFC3339), weekEnd.Format(time.RFC3339))
    
    if authorEmail != "" {
        apiURL += "&author=" + url.QueryEscape(authorEmail)
    }

    // 4. 发送请求
    req, _ := http.NewRequest("GET", apiURL, nil)
    req.Header.Set("PRIVATE-TOKEN", g.Token)

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var commits []GitLabCommit
    if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
        return nil, err
    }

    return commits, nil
}

// 测试连接
func (g *GitLabSource) TestConnection() error {
    baseURL := g.ServerURL
    if baseURL == "" { baseURL = "https://gitlab.com" }
    
    apiURL := fmt.Sprintf("%s/api/v4/projects/%s", baseURL, url.PathEscape(g.ProjectPath))
    req, _ := http.NewRequest("GET", apiURL, nil)
    req.Header.Set("PRIVATE-TOKEN", g.Token)

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != 200 {
        return fmt.Errorf("GitLab API 返回状态码: %d", resp.StatusCode)
    }
    return nil
}
```

### 7.2 GitHub API 接入

```go
package git

import (
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

// GitHub 提交记录
type GitHubCommit struct {
    SHA    string `json:"sha"`
    Commit struct {
        Message   string `json:"message"`
        Author    struct {
            Name string `json:"name"`
            Date string `json:"date"`
        } `json:"author"`
    } `json:"commit"`
}

// GitHub 数据源实现
type GitHubSource struct {
    Token       string
    Owner       string
    Repo        string
}

func (g *GitHubSource) FetchCommits(author string, weekStart, weekEnd time.Time) ([]GitHubCommit, error) {
    url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?since=%s&until=%s&per_page=100",
        g.Owner, g.Repo, weekStart.Format(time.RFC3339), weekEnd.Format(time.RFC3339))

    req, _ := http.NewRequest("GET", url, nil)
    req.Header.Set("Authorization", "Bearer "+g.Token)
    req.Header.Set("Accept", "application/vnd.github.v3+json")

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var commits []GitHubCommit
    if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
        return nil, err
    }

    return commits, nil
}
```

---

## 8. 项目目录结构

```
weekly-report/
├── main.go                      # 入口：初始化、路由、启动服务
├── go.mod                       # Go 模块定义
├── config.yaml                  # 配置文件（端口、飞书AppID、密钥等）
├── weekly_report.db             # SQLite 数据库文件（运行时生成）
│
├── internal/
│   ├── model/                   # 数据模型（GORM struct）
│   │   ├── user.go
│   │   ├── git_config.go
│   │   ├── work_record.go
│   │   └── weekly_report.go
│   │
│   ├── service/                 # 业务逻辑层
│   │   ├── auth_service.go      # 飞书认证
│   │   ├── git_service.go       # Git 提交拉取、摘要生成
│   │   ├── weekly_service.go    # 周报 CRUD
│   │   └── reminder_service.go  # 定时提醒
│   │
│   ├── handler/                 # HTTP 处理器（Gin Handler）
│   │   ├── auth_handler.go
│   │   ├── weekly_handler.go
│   │   ├── git_handler.go
│   │   └── page_handler.go      # 页面渲染（Go Templates）
│   │
│   ├── middleware/              # 中间件
│   │   ├── auth_middleware.go   # JWT 校验
│   │   └── log_middleware.go    # 请求日志
│   │
│   ├── feishu/                  # 飞书 SDK 封装
│   │   ├── client.go            # HTTP 客户端
│   │   ├── auth.go              # OAuth 流程
│   │   └── bot.go               # 机器人消息
│   │
│   ├── git/                     # Git 平台封装
│   │   ├── gitlab.go            # GitLab API 实现
│   │   ├── github.go            # GitHub API 实现
│   │   └── interface.go         # GitDataSource 接口
│   │
│   ├── database/                # 数据库初始化
│   │   └── sqlite.go            # 连接、迁移
│   │
│   └── config/                  # 配置加载
│       └── config.go
│
├── web/
│   ├── templates/               # Go HTML 模板
│   │   ├── base.html            # 基础布局（导航、页脚）
│   │   ├── login.html           # 登录页
│   │   ├── dashboard.html       # 首页仪表盘
│   │   ├── weekly_edit.html     # 周报编辑页
│   │   ├── history.html         # 历史列表
│   │   └── settings_git.html    # Git 配置页
│   └── static/                  # 静态文件（如有需要）
│
└── scripts/
    └── init.sql                 # 可选：初始数据脚本
```

---

## 9. 配置文件设计

### 9.1 config.yaml

```yaml
server:
  port: 8080
  mode: debug  # debug / release

feishu:
  app_id: "cli_xxxxxxxxxxxx"
  app_secret: "xxxxxxxxxxxxxxxx"
  redirect_url: "http://localhost:8080/auth/feishu/callback"

database:
  path: "weekly_report.db"
  max_open_conns: 10
  max_idle_conns: 5

jwt:
  secret: "your-random-secret-key-change-in-production"
  expire_hours: 168  # 7天

log:
  level: debug  # debug / info / warn / error
  output: stdout  # stdout / file
  file_path: "logs/app.log"

reminder:
  enabled: true
  cron: "0 10 * * 5"  # 每周五 10:00
  bot_webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxx"
  bot_secret: ""  # 可选签名密钥
```

---

## 10. 部署方案

### 10.1 本地开发运行

```bash
# 1. 克隆代码
git clone https://github.com/yourcompany/weekly-report.git
cd weekly-report

# 2. 安装依赖
go mod download

# 3. 配置
vim config.yaml  # 修改飞书 AppID、Secret 等

# 4. 运行
go run main.go

# 5. 访问
open http://localhost:8080
```

### 10.2 Docker 部署

```dockerfile
# Dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o weekly-report main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/weekly-report .
COPY --from=builder /app/config.yaml .
COPY --from=builder /app/web ./web
EXPOSE 8080
CMD ["./weekly-report"]
```

```bash
# 构建镜像
docker build -t weekly-report:latest .

# 运行容器
docker run -d \
  -p 8080:8080 \
  -v $(pwd)/data:/root/data \
  weekly-report:latest
```

### 10.3 Docker Compose 部署（生产推荐）

```yaml
# docker-compose.yml
version: '3.8'

services:
  app:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./data:/root/data
      - ./config.yaml:/root/config.yaml
    restart: unless-stopped

  nginx:
    image: nginx:alpine
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf
      - ./ssl:/etc/nginx/ssl
    depends_on:
      - app
    restart: unless-stopped
```

---

## 11. 性能与安全设计

### 11.1 性能优化

| 策略 | 实现 |
|------|------|
| 数据库连接池 | GORM 配置 `SetMaxOpenConns(10)` |
| 缓存 | 使用 `go-cache` 缓存用户会话、模板内容、本周数据（TTL 5分钟） |
| 静态资源 | Nginx 反向代理缓存静态文件 |
| 导出异步化 | 大文件导出使用后台 goroutine，前端轮询进度 |
| 分页查询 | 历史列表、审计日志使用分页（默认20条/页） |

### 11.2 安全设计

| 层面 | 措施 |
|------|------|
| 认证 | 飞书 OAuth 2.0，JWT 令牌，HttpOnly Cookie |
| 权限 | RBAC（管理员/团队负责人/成员），中间件校验 |
| 数据加密 | Git Token、飞书 Token 使用 AES-256 加密存储 |
| 传输安全 | 生产环境强制 HTTPS |
| 输入校验 | Gin 绑定 + 自定义校验器，防 SQL 注入、XSS |
| 审计日志 | 记录所有敏感操作，保留 90 天 |
| 备份 | SQLite 文件每日自动备份 |

---

## 12. 开发里程碑

### Phase 1：MVP 核心功能（2-3 周）
- [ ] 项目骨架：Go + Gin + GORM + SQLite 搭建
- [ ] 飞书 OAuth 登录、登出、用户信息获取
- [ ] Git 数据源配置（GitLab/GitHub API）、手动同步
- [ ] 工作记录存储与查询
- [ ] 周报自动生成（本周工作摘要）
- [ ] 周报编辑、保存草稿、提交
- [ ] Markdown 导出
- [ ] 历史周报查看
- [ ] 飞书群机器人周五提醒

### Phase 2：功能完善（2 周）
- [ ] 飞书任务/日历数据接入
- [ ] 手动补充记录
- [ ] 智能去重与关联（Git 提交与任务关联）
- [ ] 问题智能引导、下周计划建议
- [ ] 模板管理（基础版：创建、编辑、选择）
- [ ] Word / PDF 导出
- [ ] 周报预览与版本快照
- [ ] 团队提交状态统计

### Phase 3：高级功能（2 周）
- [ ] 按角色/团队模板定制
- [ ] 历史周报对比（本周 vs 上周）
- [ ] 趋势图表（近8周数据可视化）
- [ ] 团队管理面板（提交率、批量导出）
- [ ] 审批流程（提交后需审批）
- [ ] 双向同步（周报到飞书文档/任务）
- [ ] 操作审计日志

### Phase 4：优化与扩展（持续）
- [ ] 性能优化（大数据量查询、缓存策略）
- [ ] 数据源插件化（支持 Jira、禅道、Tapd）
- [ ] 通知渠道扩展（邮件、企业微信、钉钉）
- [ ] 移动端适配
- [ ] 数据库迁移工具（SQLite → PostgreSQL/MySQL）

---

## 13. 测试验证清单

| 功能 | 测试步骤 | 预期结果 |
|------|---------|---------|
| 飞书登录 | 访问首页 → 点击飞书登录 → 扫码 | 跳回首页，显示用户名和头像 |
| Git 配置 | 设置页 → 添加 GitLab Token → 点击测试 | 显示"连接成功"，列出仓库 |
| 生成周报 | 首页 → 点击"生成周报" | 页面显示本周 Git 提交摘要 |
| 编辑保存 | 在问题/计划框填写内容 → 保存草稿 | 提示"保存成功"，刷新后内容还在 |
| 提交周报 | 点击"提交周报" | 状态变为已提交，首页显示"本周已提交" |
| 导出 Markdown | 点击"导出 Markdown" | 浏览器下载 .md 文件，内容正确 |
| 查看历史 | 访问历史页面 | 显示已提交周报列表，可点击查看详情 |
| 飞书提醒 | 手动触发定时任务或等周五 | 飞书群收到未提交人员的提醒消息 |
| 权限控制 | 普通成员访问团队管理页 | 返回 403 无权限 |
| 审计日志 | 管理员查看日志页 | 显示所有登录/提交/导出操作记录 |

---

## 14. 参考资源

- [飞书开放平台](https://open.feishu.cn/)
- [飞书 OAuth 文档](https://open.feishu.cn/document/server-docs/authentication-management/login-state-management/get-user-info)
- [飞书机器人文档](https://open.feishu.cn/document/ukTMukTMukTM/ucTM5YjL3ETO24yNxkjN)
- [GitLab API 文档](https://docs.gitlab.com/ee/api/)
- [GitHub API 文档](https://docs.github.com/en/rest)
- [Gin 框架文档](https://gin-gonic.com/docs/)
- [GORM 文档](https://gorm.io/docs/)
- [SQLite 限制](https://www.sqlite.org/limits.html)

---

## 15. 资深开发 Review 与修正建议

以下为资深开发对本文档的审查意见，按严重程度分类。建议在编码前修正 🔴 严重问题，编码过程中注意 🟡 中等问题，🟢 轻度问题可在后续迭代优化。

### 15.1 🔴 严重问题（必须修正）

#### 1. SQLite 并发写锁瓶颈
**问题**：Gin 默认多线程处理请求，SQLite 的写锁是**文件级**的。20 人团队周五同时提交周报，会出现 `database is locked` 错误。  
**影响**：高并发场景下系统直接不可用。  
**修正方案**：
- 初始化时开启 WAL（Write-Ahead Logging）模式，提升并发读写性能：
  ```go
  db.Exec("PRAGMA journal_mode=WAL;")
  db.Exec("PRAGMA busy_timeout=5000;") // 等待5秒而不是立即报错
  ```
- 限制写并发连接数（WAL 模式下读并发不受影响）：
  ```go
  sqlDB, _ := db.DB()
  sqlDB.SetMaxOpenConns(1) // 写串行化，但 WAL 允许读并发
  ```
- 涉及位置：第 3 章数据库设计、第 11.1 节性能优化。

#### 2. Git API 分页陷阱
**问题**：`FetchCommits` 代码只取了 `per_page=100`，如果用户本周提交超过 100 条（如批量重构），后面的提交直接丢失。  
**影响**：数据遗漏，周报不完整。  
**修正方案**：GitLab/GitHub 都通过 `Link` Header 分页，必须循环拉取：
  ```go
  func (g *GitLabSource) FetchAllCommits(authorEmail string, weekStart, weekEnd time.Time) ([]GitLabCommit, error) {
      var allCommits []GitLabCommit
      for page := 1; ; page++ {
          commits, hasMore, err := g.fetchPage(authorEmail, weekStart, weekEnd, page)
          if err != nil { return nil, err }
          if len(commits) == 0 { break }
          allCommits = append(allCommits, commits...)
          if !hasMore { break }
      }
      return allCommits, nil
  }
  ```
- 涉及位置：第 7.1 节 GitLab API 接入代码。

#### 3. 飞书 Token 过期未处理
**问题**：飞书 OAuth 的 `access_token` 有效期只有 **2 小时**，文档只讲了获取，没讲刷新。用户登录 2 小时后，所有飞书 API 调用都会 401。  
**影响**：任务/日历同步、用户信息获取等飞书相关功能间歇性失效。  
**修正方案**：
- 方案 A（推荐）：飞书 OAuth 仅用于**首次登录**，获取 `open_id` 后，后续操作不依赖飞书 `access_token`（任务/日历同步通过用户独立授权或 Webhook）。
- 方案 B：实现 `refresh_token` 刷新机制，在调用飞书 API 前检查 Token 是否过期，过期则自动刷新。
  ```go
  func (c *Client) CallAPIWithRefresh(req *http.Request) (*http.Response, error) {
      if c.isTokenExpired() {
          if err := c.refreshToken(); err != nil {
              return nil, err
          }
      }
      req.Header.Set("Authorization", "Bearer "+c.accessToken)
      return http.DefaultClient.Do(req)
  }
  ```
- 涉及位置：第 6.1 节飞书 OAuth 登录。

#### 4. Word/PDF 导出库选型风险
**问题**：`unioffice` 是**商业库**（有试用水印，企业使用需付费），`gofpdf` 已**停止维护**（作者归档仓库）。  
**影响**：部署后导出 Word 有水印，或 PDF 库不再更新。  
**修正方案**：
  | 格式 | 原选型 | 修正选型 | 理由 |
  |------|--------|----------|------|
  | Word | unioffice | **github.com/nguyenthenguyen/docx** | MIT 协议，纯 Go，无水印 |
  | PDF | wkhtmltopdf / gofpdf | **chromedp**（无头 Chrome） | 渲染更稳定，支持 CSS；或先放弃 PDF，只支持 Markdown + Word |
- 涉及位置：第 2.1 节后端技术栈表格。

#### 5. 时区处理缺失
**问题**：Git 提交时间可能带时区（如 `+08:00`），服务器可能 UTC，"本周"计算会错乱。用户周一早上 9 点的提交，按 UTC 算可能是周日晚上。  
**影响**：周报数据归属错误周次，用户看到"本周"数据不全或跨周。  
**修正方案**：
- 所有时间存储统一转 UTC
- 按用户所在时区计算"本周"（配置文件加 `timezone: Asia/Shanghai`）
  ```go
  func GetWeekStart(t time.Time, loc *time.Location) time.Time {
      tt := t.In(loc)
      weekday := int(tt.Weekday())
      if weekday == 0 { weekday = 7 }
      return tt.AddDate(0, 0, -weekday+1).Truncate(24*time.Hour)
  }
  ```
- 查询时 `week_start` 和 `week_end` 边界值包含时区偏移
- 涉及位置：第 5 章关键算法设计、第 9 章配置文件设计。

---

### 15.2 🟡 中等问题（建议修正）

#### 6. 前端 CDN 依赖 = 单点故障
**问题**：Tailwind CSS、HTMX、Chart.js 都走 CDN。公司内网环境或 CDN 抽风时，页面直接崩。  
**修正方案**：把关键资源下载到本地 `web/static/`：
  ```html
  <!-- 不要 -->
  <script src="https://cdn.tailwindcss.com"></script>
  
  <!-- 要 -->
  <script src="/static/htmx.min.js"></script>
  <link href="/static/tailwind.min.css" rel="stylesheet">
  ```
- 涉及位置：第 2.2 节前端技术栈、第 8 章项目目录结构。

#### 7. 缺少限流/防刷保护
**问题**：飞书机器人有**QPS 限制**（每秒 10 次），GitHub API 有**每小时 5000 次限制**。用户疯狂点击"刷新数据"或团队大时，会直接触发限流。  
**修正方案**：
- 接口层加 `ratelimit` 中间件（基于 IP 或用户）
- Git API 调用加本地缓存（5 分钟内同一仓库不重复拉取）
- 飞书机器人发消息加队列，控制发送频率
- 涉及位置：第 1.2 节模块职责说明、第 11.2 节安全设计。

#### 8. 飞书日历隐私授权困境
**问题**：读取日历需要用户授权 `calendar:calendar:readonly`，很多用户会拒绝（不想暴露日程）。  
**修正方案**：日历作为**可选**数据源，不强制授权；提供"手动录入会议"替代方案。  
**涉及位置**：第 6 章飞书集成技术实现。

#### 9. 定时任务没有持久化
**问题**：`robfig/cron` 在内存里跑定时任务。如果服务重启，"周五提醒"可能没触发，且没有记录。  
**修正方案**：
- 把定时任务调度写到数据库（如 `cron_jobs` 表），服务启动时扫描未执行的补跑
- 或使用更重的方案（如 `Asynq`、`Gocron` 的持久化支持）
- 涉及位置：第 6.3 节定时任务。

#### 10. GORM AutoMigrate 生产危险
**问题**：`AutoMigrate` 自动改表结构，如果线上误删字段，数据直接丢。  
**修正方案**：
- 开发环境用 `AutoMigrate`，生产环境用**版本化迁移**（如 `golang-migrate` 或手写 SQL 迁移文件）
- 配置里加 `mode: dev/prod`，prod 模式禁用 `AutoMigrate`：
  ```go
  if cfg.Server.Mode == "dev" {
      db.AutoMigrate(&models...)
  }
  ```
- 涉及位置：第 3.2 节表结构定义、第 9 章配置文件设计。

---

### 15.3 🟢 轻度问题（优化项）

#### 11. 配置热更新缺失
**问题**：改 `config.yaml` 里的机器人 Webhook 地址，需要重启服务。  
**修正方案**：用 `Viper` 的 `WatchConfig` 支持热重载，或关键配置走数据库。
  ```go
  viper.WatchConfig()
  viper.OnConfigChange(func(e fsnotify.Event) {
      log.Println("配置已重载:", e.Name)
      reloadConfig()
  })
  ```

#### 12. 没有健康检查和监控
**问题**：系统跑挂了怎么知道？Git 同步失败怎么告警？  
**修正方案**：
- 加 `/health` 接口（检查数据库连接、磁盘空间）
- 飞书同步失败时，发告警到管理员飞书
- 日志接入 `promtail` 或直接用 Zap 写文件

#### 13. 周报内容无 XSS 防护
**问题**：用户提交的周报内容（问题、计划）直接渲染到 HTML，如果用户写 `<script>alert('xss')</script>`，会触发。  
**修正方案**：
- Go Templates 默认会转义 HTML，但如果是富文本编辑器，需要加 `bluemonday` 做 HTML 净化
- 或者只允许 Markdown，使用 `html/template` 安全渲染，不直接输出原始 HTML

#### 14. 导出文件无清理机制
**问题**：`weekly_report_123.md` 生成后存在磁盘，久而久之占满空间。  
**修正方案**：导出文件放到 `tmp/` 目录，设置定时任务（每天凌晨）清理 24 小时前的文件；或使用内存流直接返回，不落地磁盘。

#### 15. 缺少数据库备份策略
**问题**：`weekly_report.db` 是单文件，如果硬盘坏了或误删，数据全丢。  
**修正方案**：每天 3:00 自动 `cp weekly_report.db weekly_report.db.bak.$(date +%Y%m%d)`，保留最近 7 天备份，定期清理旧备份。
  ```go
  // 定时备份任务
  c.AddFunc("0 3 * * *", func() {
      backupPath := fmt.Sprintf("backups/weekly_report.db.bak.%s", time.Now().Format("20060102"))
      os.CopyFile("weekly_report.db", backupPath)
      cleanOldBackups(7) // 保留7天
  })
  ```

---

### 15.5 Review 总结

| 问题等级 | 数量 | 建议处理时机 |
|---------|------|------------|
| 🔴 严重 | 5 | 编码前必须修正，否则系统无法稳定运行 |
| 🟡 中等 | 5 | 编码过程中注意，影响体验但不阻塞 |
| 🟢 轻度 | 5 | 后续迭代优化，提升可维护性 |

---

## 16. 第二轮资深开发 Review（补充发现）

以下为对文档进行二次审查后新发现的问题，补充第一轮未覆盖的边界场景、安全漏洞和工程化缺失。

### 16.1 🔴 严重问题（新发现）

#### 16. 飞书 OAuth 的 CSRF 防护不完整
**问题**：第 6.1 节的代码展示了 `GetAuthURL` 生成授权链接，但没有展示 `state` 的**生成**（随机性）和**校验**（防止 CSRF 攻击）。如果攻击者诱导用户点击伪造的回调链接，可能导致账号被劫持。  
**修正方案**：
  ```go
  func (h *AuthHandler) FeishuLogin(c *gin.Context) {
      state := generateRandomState(32) // 32位随机字符串
      c.SetCookie("feishu_state", state, 600, "/", "", true, true)
      redirectURL := h.feishuClient.GetAuthURL(h.redirectURI, state)
      c.Redirect(302, redirectURL)
  }
  
  func (h *AuthHandler) FeishuCallback(c *gin.Context) {
      state := c.Query("state")
      cookieState, _ := c.Cookie("feishu_state")
      if state == "" || state != cookieState {
          c.JSON(400, gin.H{"error": "invalid state"})
          return
      }
      // ... 继续用 code 换 token
  }
  ```
- 涉及位置：第 6.1 节飞书 OAuth 登录。

#### 17. JWT Secret 硬编码 = 安全后门
**问题**：第 9.1 节 `config.yaml` 示例中 `jwt.secret` 是明文硬编码的字符串 `"your-random-secret-key-change-in-production"`。如果用户直接复制粘贴到生产环境，相当于给所有攻击者留了一把万能钥匙。  
**修正方案**：
- 生产环境 JWT Secret 必须**运行时生成**或从**环境变量**读取，禁止写在配置文件里：
  ```go
  // 生产环境启动时，如果 secret 是默认值，强制退出
  if cfg.JWT.Secret == "your-random-secret-key-change-in-production" {
      log.Fatal("JWT secret 不能为默认值，请通过环境变量 JWT_SECRET 设置")
  }
  ```
- 配置文件示例中应明确标注 `# 生产环境请通过环境变量 JWT_SECRET 覆盖此值`
- 涉及位置：第 9.1 节 config.yaml。

#### 18. 事务控制缺失（数据不一致风险）
**问题**：周报提交时涉及**周报表状态更新** + **版本快照写入** + **审计日志记录**，三个操作应该原子化。如果中间一步失败，数据库可能处于半完成状态（已提交但无版本记录）。  
**修正方案**：
  ```go
  func (s *WeeklyService) SubmitReport(reportID int, userID int) error {
      return s.db.Transaction(func(tx *gorm.DB) error {
          // 1. 更新状态
          if err := tx.Model(&WeeklyReport{}).Where("id = ?", reportID).Update("status", "submitted").Error; err != nil {
              return err
          }
          // 2. 写入版本快照
          if err := tx.Create(&WeeklyReportVersion{ReportID: reportID, ...}).Error; err != nil {
              return err
          }
          // 3. 记录审计日志
          if err := tx.Create(&AuditLog{...}).Error; err != nil {
              return err
          }
          return nil
      })
  }
  ```
- 涉及位置：第 4.1 节接口清单、第 5 章关键算法。

#### 19. 幂等性设计缺失（重复提交/生成）
**问题**：用户快速双击"生成周报"按钮，可能产生两份草稿；网络抖动导致重复提交，可能产生两条已提交记录。  
**修正方案**：
- 前端按钮点击后立即置灰（防止重复点击）
- 后端关键接口（生成、提交、导出）使用**幂等键**（Idempotency Key）：
  ```go
  func (h *WeeklyHandler) Generate(c *gin.Context) {
      idemKey := c.GetHeader("Idempotency-Key")
      if idemKey != "" {
          // 检查缓存中是否已有结果
          if cached, found := idemCache.Get(idemKey); found {
              c.JSON(200, cached)
              return
          }
      }
      // ... 执行生成逻辑
      // 结果缓存 5 分钟
      idemCache.Set(idemKey, result, 5*time.Minute)
  }
  ```
- 涉及位置：第 4.1 节接口清单。

#### 20. Graceful Shutdown 缺失（数据损坏风险）
**问题**：`main.go` 没有展示如何优雅关闭。如果服务正在写入 SQLite 时收到 `SIGTERM`（如 Docker 停止容器），数据库文件可能损坏。  
**修正方案**：
  ```go
  func main() {
      r := gin.Default()
      // ... 路由注册
      
      srv := &http.Server{Addr: ":8080", Handler: r}
      
      go func() {
          if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
              log.Fatalf("listen: %s\n", err)
          }
      }()
      
      // 等待中断信号
      quit := make(chan os.Signal, 1)
      signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
      <-quit
      log.Println("Shutting down server...")
      
      // 优雅关闭，5秒超时
      ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
      defer cancel()
      if err := srv.Shutdown(ctx); err != nil {
          log.Fatal("Server forced to shutdown:", err)
      }
      
      // 关闭数据库连接
      sqlDB, _ := db.DB()
      sqlDB.Close()
      
      log.Println("Server exited")
  }
  ```
- 涉及位置：第 8 章项目目录结构（main.go 入口）。

---

### 16.2 🟡 中等问题（新发现）

#### 21. Git 作者邮箱匹配不准确
**问题**：`FetchCommits` 按 `authorEmail` 过滤，但 Git 提交的作者邮箱（`git config user.email`）可能和飞书邮箱不一致（如用户用个人邮箱提交公司代码）。  
**修正方案**：
- 支持多邮箱配置（用户可添加多个 Git 邮箱）
- 如果邮箱不匹配，退而求其次：拉取全部提交后，按用户名模糊匹配
- 涉及位置：第 7.1 节 GitLab API 接入。

#### 22. Cookie 安全属性缺失
**问题**：飞书 OAuth 回调设置的 Cookie 和 JWT Cookie 没有展示 `Secure`、`HttpOnly`、`SameSite` 属性。如果通过 HTTP 传输或 XSS 攻击，Cookie 可能被窃取。  
**修正方案**：
  ```go
  c.SetCookie("token", jwtToken, 3600*24*7, "/", "", true, true) // secure=true, httponly=true
  ```
- 生产环境必须设置 `SameSite=Strict` 或 `Lax`
- 涉及位置：第 6.1 节飞书 OAuth 回调。

#### 23. 并发同步产生重复数据
**问题**：用户连续点击"同步"按钮，或定时任务和手动同步同时触发，可能产生重复工作记录。虽然数据库有 `UNIQUE` 约束，但冲突时的处理逻辑没有展示。  
**修正方案**：
  ```go
  // 使用 INSERT OR IGNORE（SQLite）或 INSERT ON CONFLICT DO NOTHING（PostgreSQL）
  func (r *WorkRecordRepo) BatchInsert(records []WorkRecord) error {
      return r.db.Clauses(clause.OnConflict{
          Columns:   []clause.Column{{Name: "user_id"}, {Name: "source_type"}, {Name: "external_id"}},
          DoUpdates: clause.AssignmentColumns([]string{"updated_at"}), // 冲突时更新更新时间
      }).Create(&records).Error
  }
  ```
- 涉及位置：第 3.2 节表结构定义、第 7 章 Git 接入。

#### 24. 飞书 App Secret 泄露风险（配置管理）
**问题**：`config.yaml` 包含飞书 `app_secret` 和 Git Token，如果用户误提交到 Git 仓库，敏感信息直接泄露。  
**修正方案**：
- 将 `config.yaml` 加入 `.gitignore`
- 提供 `config.example.yaml`（脱敏模板）用于版本管理
- 敏感字段通过环境变量注入：
  ```yaml
  feishu:
    app_secret: ${FEISHU_APP_SECRET}  # 从环境变量读取
  ```
- 涉及位置：第 9.1 节 config.yaml、第 10 章部署方案。

#### 25. 数据归档策略缺失（长期性能隐患）
**问题**：工作记录表和审计日志表数据量随时间线性增长，3 年后查询会变慢。当前文档没有提及数据归档或分表策略。  
**修正方案**：
- 工作记录保留 2 年，过期数据归档到 `work_records_archive` 表或导出为 CSV
- 审计日志保留 1 年，过期数据压缩存储
- 提供归档任务（每月执行一次）
  ```go
  func ArchiveOldRecords(db *gorm.DB) error {
      cutoff := time.Now().AddDate(-2, 0, 0)
      return db.Where("occurred_at < ?", cutoff).Delete(&WorkRecord{}).Error
  }
  ```
- 涉及位置：第 3.2 节表结构定义、第 11.1 节性能优化。

---

### 16.3 🟢 轻度问题（新发现）

#### 26. 错误处理不完善（工程化缺失）
**问题**：文档中大量代码示例对 `err` 的处理过于简单（如 `return nil, err` 或直接忽略），没有展示错误分类、日志记录、用户友好提示。  
**修正方案**：
- 定义业务错误码（如 `ErrGitAPIFailed`、`ErrTokenExpired`）
- 统一错误中间件，将内部错误映射为用户友好的 HTTP 响应
  ```go
  type AppError struct {
      Code    string `json:"code"`
      Message string `json:"message"`
      Detail  string `json:"detail,omitempty"`
  }
  ```
- 涉及位置：第 5-7 章代码示例。

#### 27. 缺少降级策略（外部依赖不可用）
**问题**：如果飞书 API 挂了、Git 平台维护了，系统应该如何表现？当前文档没有展示降级逻辑。  
**修正方案**：
- 飞书 OAuth 不可用：降级为本地账号密码登录（备用方案）
- Git API 限流/超时：提示用户"Git 数据暂不可用，可手动补充记录"
- 飞书机器人发送失败：记录到队列，稍后重试；超过 3 次失败则告警管理员
- 涉及位置：第 1.2 节模块职责、第 6 章飞书集成。

#### 28. 日志分级使用场景不明确
**问题**：虽然选用了 Zap，但没有定义什么场景用 `debug/info/warn/error`。开发时可能全用 `Info`，导致生产日志爆炸。  
**修正方案**：
  | 级别 | 使用场景 |
  |------|----------|
  | Debug | 开发调试，如 SQL 执行详情、接口入参出参 |
  | Info | 业务流程节点，如"用户登录成功""周报已提交" |
  | Warn | 非致命异常，如"Git API 返回空数据""飞书机器人发送失败（第1次）" |
  | Error | 致命错误，如"数据库连接失败""SQLite 锁超时" |
- 涉及位置：第 2.1 节后端技术栈。

#### 29. 没有定义 SLA/SLO（可观测性缺失）
**问题**：文档提到"系统可用性 > 99.5%"，但没有定义具体的 SLA 指标和监控方式。  
**修正方案**：
  | 指标 | SLO | 监控方式 |
  |------|-----|----------|
  | API 可用性 | > 99.5% | `/health` 接口 + 定时探活 |
  | 周报生成 P99 延迟 | < 3s | Zap 日志记录耗时 + 告警 |
  | 飞书提醒成功率 | > 98% | 对比 `reminders` 表 sent vs failed |
  | 数据同步成功率 | > 99% | `data_sources` 表 sync_status 统计 |
- 涉及位置：第 5.2 节性能需求。

#### 30. 国际化（i18n）未考虑
**问题**：如果团队有外籍员工，系统界面和周报内容需要中英文切换。当前模板和前端文案全是中文硬编码。  
**修正方案**：
- 使用 Go 的 `golang.org/x/text/message` 或简单的 JSON 字典实现 i18n
- 模板中支持 `{{.Lang}}` 变量切换语言
- MVP 阶段可暂不实现，但应在 PRD 中标注"未来支持多语言"
- 涉及位置：PRD 模板管理、Technical-Design 前端技术栈。

---

### 16.4 第二轮 Review 总结

| 问题等级 | 数量 | 与第一轮合计 |
|---------|------|------------|
| 🔴 严重 | 5 | 10 |
| 🟡 中等 | 5 | 10 |
| 🟢 轻度 | 5 | 10 |
| **总计** | **15** | **30** |
