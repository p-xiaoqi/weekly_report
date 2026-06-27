# 周报系统测试报告

> 报告版本：v2.0
> 生成日期：2026-06-27
> 测试范围：核心功能集成测试 + 存储层单元测试
> 格式：Markdown

---

## 1. 执行摘要

本次测试针对**周报系统**进行了全面的自动化测试，覆盖认证、周报采集、导出、数据源管理、模板管理、存储层等核心模块。本轮在修复一批生产缺陷与安全隐患（详见第 5 节"修复记录"）后重新跑批，测试结果表明：

- **25 个测试用例全部通过** ✅
- **鉴权校验真实生效**：受保护路由统一挂载到 `authMiddleware` 鉴权组，未携带合法 JWT 的请求正确返回 401
- **Token 加密升级为 AES-256-GCM**，`SaveToken → GetToken` 往返一致性验证通过
- **代码覆盖率为 27.0%**（`cmd/server` 包，statements）

---

## 2. 测试统计

| 指标 | 数值 |
|------|------|
| 总测试用例 | 25 |
| 通过 | 25 |
| 失败 | 0 |
| 跳过 | 0 |
| 通过率 | **100%** |
| 代码覆盖率 | **27.0%**（cmd/server 包，statements） |
| 执行时间 | ~0.07 秒 |
| 并发模式 | 串行（避免数据库状态污染） |

---

## 3. 核心模块覆盖详情

### 3.1 基础设施

| 测试 | 结果 |
|------|------|
| TC-HEALTH-001 健康检查接口 | ✅ PASS |

### 3.2 认证模块

| 测试 | 结果 | 备注 |
|------|------|------|
| TC-AUTH-001 飞书登录 URL 生成 | ✅ PASS | 返回 URL 含 `open.feishu.cn` |
| TC-AUTH-002 飞书 callback 缺少 code | ✅ PASS | 正确返回 400 |

### 3.3 周报模块

| 测试 | 结果 | 备注 |
|------|------|------|
| TC-REPORT-001 未登录访问周报列表 | ✅ PASS | 经 `authMiddleware` 返回 401 |
| TC-REPORT-002 创建并查询周报 | ✅ PASS | 响应体 `data.report.week_start` 正确 |
| TC-REPORT-003 查询不存在的周报 | ✅ PASS | 返回 404 |

### 3.4 导出模块

| 测试 | 结果 | 备注 |
|------|------|------|
| TC-EXPORT-001 导出 Markdown | ✅ PASS | Content-Type 正确，内容完整 |
| TC-EXPORT-002 导出 Word | ✅ PASS | 生成非空 docx 文件 |
| TC-EXPORT-003 导出 PDF（打印 HTML） | ✅ PASS | HTML 含 doctype、print 按钮、A4 CSS |
| TC-EXPORT-004 导出不支持的格式 | ✅ PASS | 返回 400 |
| TC-EXPORT-005 导出周报不存在 | ✅ PASS | 返回 404 |

### 3.5 浏览器插件采集

| 测试 | 结果 | 备注 |
|------|------|------|
| TC-BROWSER-001 浏览器插件推送数据 | ✅ PASS | 端点已纳入鉴权组，身份取自会话 cookie，记录归属 `user_test_001` |

### 3.6 数据源管理

| 测试 | 结果 | 备注 |
|------|------|------|
| TC-DS-001 创建数据源 | ✅ PASS | GitLab 类型创建成功 |
| TC-DS-002 未登录创建数据源 | ✅ PASS | 经 `authMiddleware` 返回 401 |
| TC-DS-003 列出数据源 | ✅ PASS | 返回数量与创建一致 |
| TC-DS-004 删除数据源 | ✅ PASS | 删除后查询不存在 |

### 3.7 模板管理

| 测试 | 结果 | 备注 |
|------|------|------|
| TC-TMPL-001 列出模板 | ✅ PASS | 包含默认模板 |
| TC-TMPL-002 创建模板 | ✅ PASS | 自定义模板创建成功 |
| TC-TMPL-003 删除模板 | ✅ PASS | 删除后查询不存在 |

### 3.8 HTML 渲染与安全

| 测试 | 结果 | 备注 |
|------|------|------|
| TC-HTML-001 HTML 打印页面包含必要元素 | ✅ PASS | doctype/print 按钮/A4 CSS |
| TC-HTML-002 HTML 转义防止 XSS | ✅ PASS | `<script>` 被转义 |

### 3.9 存储层

| 测试 | 结果 | 备注 |
|------|------|------|
| TC-STORE-001 报告版本快照 | ✅ PASS | Save + Get 正确 |
| TC-STORE-002 Token 加密解密 | ✅ PASS | AES-256-GCM 加解密往返一致 |
| TC-STORE-003 WorkRecord 隐藏/显示 | ✅ PASS | Hide 后查询排除 |
| TC-STORE-004 用户映射 | ✅ PASS | 映射/未映射均正确 |

---

## 4. 代码覆盖率分析

```bash
$ go test -coverprofile=coverage.out ./cmd/server/
ok      weekly-report-system/cmd/server 0.069s  coverage: 27.0% of statements
```

### 关键函数覆盖率（节选）

| 函数 | 覆盖率 | 说明 |
|------|--------|------|
| `exportHandler` | 86.1% | 导出逻辑（PDF/Word/Markdown）主路径覆盖 |
| `generateHTMLPrintPage` | 79.4% | 打印 HTML 生成覆盖 |
| `browserCollectHandler` | 75.0% | 浏览器采集主路径覆盖 |
| `generateWeeklyReport` | 45.0% | 模板/回退分支部分覆盖 |
| `collectHandler` | 0.0% | 依赖真实飞书 token，未在单测覆盖 |
| `compareReportHandler` | 0.0% | 未编写专项测试 |
| `statsHandler` | 0.0% | 未编写专项测试 |
| **总计** | **27.0%** | cmd/server 包语句覆盖率 |

**覆盖率说明：** 本轮覆盖率（27.0%）低于历史报告所声称的数值，主要原因是：将 `collectHandler` 由内联 lark 抓取改为委托 `collector` 服务后，该 handler 因依赖真实飞书 token 无法在单测覆盖；同时新增的 `compareReportHandler`（增强版）、`statsHandler` 暂无专项测试。后续建议补充外部服务 mock 以提升覆盖率。

---

## 5. 修复记录（本轮）

本轮针对一批"已写好但从未接入 `main`"的包，以及若干真实 bug 与一处密钥泄漏进行了修复：

| 编号 | 修复项 | 说明 |
|------|--------|------|
| FIX 1 | 密钥泄漏 + cron 格式 | `configs/config.yaml` 中硬编码的真实飞书 webhook 改为 `${FEISHU_BOT_WEBHOOK}`；`cron` 由 5 段 `0 10 * * 5` 改为 6 段 `0 0 10 * * 5`（`cron.WithSeconds()` 要求 6 段）；`.env.example` 新增 `FEISHU_BOT_WEBHOOK=` |
| FIX 2 | 缺失 FeishuToken 表迁移 | `main()` 的 `AutoMigrate` 列表补充 `&model.FeishuToken{}`，修复生产环境 `SaveToken` 失败导致"token 已过期"的问题 |
| FIX 3 | 接入 database.Init（WAL + 连接池） | `main()` 改用 `database.Init` 打开数据库，启用 WAL、busy_timeout 与写连接限制；移除未使用的 `gorm`/`sqlite` 直接导入 |
| FIX 4 | AES-256-GCM Token 加密 | `internal/store/sqlite.go` 将仅 Base64 的 `encryptToken/decryptToken` 升级为 AES-256-GCM（随机 nonce、密文长度校验），并新增 `SetTokenKey`，由 `main()` 以 JWT Secret 派生密钥 |
| FIX 5 | 接入提醒服务 + 真实 webhook 发送 | `reminder.sendBotMessage` 改为真实 HTTP POST 飞书消息；`main()` 初始化并按配置启动 `reminderSvc`；`testRemindHandler` 改为真实发送 |
| FIX 6 | 接入 collector 采集 | `main()` 构建 `lark.Adapter` 与 `collector`；`collectHandler` 委托 `collector.Collect`，实现 git/lark/日历/文档采集、周过滤、下周计划与统一模板渲染 |
| FIX 7 | 修复 GetWeeklyStats SQL + 增强对比/统计 | 修复 `date('now', '-? days')` 占位符错误（改为安全内联天数、仅绑定 userID）；`compareReportHandler` 返回本周/上周分类计数；新增 `GET /api/v1/stats`（8 周统计） |
| FIX 8 | 加固浏览器采集端点 | `/api/v1/collect/browser` 移入鉴权组，身份改为取自已验证会话 cookie，移除"从请求体信任 user_id"的逻辑 |
| FIX 9 | 测试鉴权对齐生产 | `setupTestServer` 引入 `authMiddleware` 鉴权组并补齐路由；新增 `authCookies` JWT 助手；未授权用例保持无 cookie 以真实返回 401 |

---

## 6. 测试环境信息

| 环境项 | 值 |
|--------|-----|
| 操作系统 | Linux |
| Go 版本 | Go 1.24.x（go1.24.13 linux/amd64） |
| 数据库 | SQLite 3（内存模式） |
| 测试数据库隔离 | 每个测试独立 `file:<test_name>?mode=memory&cache=shared` |
| 测试执行命令 | `go test -v ./cmd/server/` |
| 覆盖率命令 | `go test -coverprofile=coverage.out ./cmd/server/` |

---

## 7. 跑批测试结果集

```
=== RUN   TestHealthCheck
--- PASS: TestHealthCheck (0.00s)
=== RUN   TestLarkLoginURL
--- PASS: TestLarkLoginURL (0.00s)
=== RUN   TestLarkCallbackMissingCode
--- PASS: TestLarkCallbackMissingCode (0.00s)
=== RUN   TestListReportsUnauthorized
--- PASS: TestListReportsUnauthorized (0.00s)
=== RUN   TestCreateAndGetReport
--- PASS: TestCreateAndGetReport (0.00s)
=== RUN   TestGetReportNotFound
--- PASS: TestGetReportNotFound (0.00s)
=== RUN   TestExportMarkdown
--- PASS: TestExportMarkdown (0.00s)
=== RUN   TestExportWord
--- PASS: TestExportWord (0.00s)
=== RUN   TestExportPDF
--- PASS: TestExportPDF (0.00s)
=== RUN   TestExportInvalidFormat
--- PASS: TestExportInvalidFormat (0.00s)
=== RUN   TestExportReportNotFound
--- PASS: TestExportReportNotFound (0.00s)
=== RUN   TestBrowserCollect
--- PASS: TestBrowserCollect (0.00s)
=== RUN   TestCreateDataSource
--- PASS: TestCreateDataSource (0.00s)
=== RUN   TestCreateDataSourceUnauthorized
--- PASS: TestCreateDataSourceUnauthorized (0.00s)
=== RUN   TestListDataSources
--- PASS: TestListDataSources (0.00s)
=== RUN   TestDeleteDataSource
--- PASS: TestDeleteDataSource (0.00s)
=== RUN   TestListTemplates
--- PASS: TestListTemplates (0.00s)
=== RUN   TestCreateTemplate
--- PASS: TestCreateTemplate (0.00s)
=== RUN   TestDeleteTemplate
--- PASS: TestDeleteTemplate (0.00s)
=== RUN   TestGenerateHTMLPrintPage
--- PASS: TestGenerateHTMLPrintPage (0.00s)
=== RUN   TestGenerateHTMLPrintPageEscaping
--- PASS: TestGenerateHTMLPrintPageEscaping (0.00s)
=== RUN   TestStoreSaveReportVersion
--- PASS: TestStoreSaveReportVersion (0.00s)
=== RUN   TestStoreTokenEncryptDecrypt
--- PASS: TestStoreTokenEncryptDecrypt (0.00s)
=== RUN   TestStoreHideWorkRecord
--- PASS: TestStoreHideWorkRecord (0.00s)
=== RUN   TestStoreUserMapping
--- PASS: TestStoreUserMapping (0.00s)
PASS
ok      weekly-report-system/cmd/server 0.067s
```

---

## 8. 结论与建议

### 结论

- **25 个测试用例 100% 通过**，覆盖核心功能路径
- **鉴权对齐生产**，未授权访问真实返回 401
- **Token 加密升级为 AES-256-GCM**，往返一致性验证通过
- **代码覆盖率为 27.0%**（cmd/server 包，statements）

### 后续建议

1. **补充外部服务 mock 测试**：为 `larkClient`、`collector.Collect` 添加 httptest mock，提升 `collectHandler` 覆盖率
2. **新增端点测试**：为 `compareReportHandler`、`statsHandler` 编写专项用例
3. **边界测试**：超长 Markdown、特殊 Unicode 字符、空内容导出
4. **提醒服务测试**：用本地 httptest server mock 飞书 webhook，验证 `sendBotMessage`

---

*本报告基于实际跑批测试结果生成。*
