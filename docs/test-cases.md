# 周报系统测试用例文档

> 文档版本：v1.0  
> 生成日期：2026-06-27  
> 对应代码版本：PDF 导出修复后  
> 格式：Markdown

---

## 1. 测试范围

本测试用例覆盖**周报系统**的核心功能，包括：

- 系统健康检查
- 飞书 OAuth 认证
- 周报收集（飞书任务 + 浏览器插件）
- 周报查询与管理
- 导出功能（Markdown / Word / PDF）
- 数据源管理（CRUD）
- 模板管理（CRUD）
- 存储层（Token 加密、WorkRecord、版本快照）
- HTML 打印页面生成与 XSS 防护

---

## 2. 测试用例总览

| 编号 | 名称 | 类别 | 优先级 | 类型 |
|------|------|------|--------|------|
| TC-HEALTH-001 | 健康检查接口 | 基础设施 | P0 | 正向 |
| TC-AUTH-001 | 飞书登录 URL 生成 | 认证 | P0 | 正向 |
| TC-AUTH-002 | 飞书 callback 缺少 code | 认证 | P0 | 负向 |
| TC-REPORT-001 | 未登录访问周报列表 | 周报 | P0 | 权限 |
| TC-REPORT-002 | 创建并查询周报 | 周报 | P0 | 正向 |
| TC-REPORT-003 | 查询不存在的周报 | 周报 | P1 | 负向 |
| TC-EXPORT-001 | 导出 Markdown | 导出 | P0 | 正向 |
| TC-EXPORT-002 | 导出 Word | 导出 | P0 | 正向 |
| TC-EXPORT-003 | 导出 PDF（打印 HTML） | 导出 | P0 | 正向 |
| TC-EXPORT-004 | 导出不支持的格式 | 导出 | P1 | 负向 |
| TC-EXPORT-005 | 导出周报不存在 | 导出 | P1 | 负向 |
| TC-BROWSER-001 | 浏览器插件推送数据 | 采集 | P0 | 正向 |
| TC-DS-001 | 创建数据源 | 数据源 | P1 | 正向 |
| TC-DS-002 | 未登录创建数据源 | 数据源 | P1 | 权限 |
| TC-DS-003 | 列出数据源 | 数据源 | P1 | 正向 |
| TC-DS-004 | 删除数据源 | 数据源 | P1 | 正向 |
| TC-TMPL-001 | 列出模板 | 模板 | P1 | 正向 |
| TC-TMPL-002 | 创建模板 | 模板 | P1 | 正向 |
| TC-TMPL-003 | 删除模板 | 模板 | P1 | 正向 |
| TC-HTML-001 | HTML 打印页面包含必要元素 | 渲染 | P0 | 正向 |
| TC-HTML-002 | HTML 转义防止 XSS | 安全 | P0 | 安全 |
| TC-STORE-001 | 存储层报告版本快照 | 存储 | P1 | 正向 |
| TC-STORE-002 | Token 加密解密 | 存储 | P1 | 正向 |
| TC-STORE-003 | WorkRecord 隐藏/显示 | 存储 | P1 | 正向 |
| TC-STORE-004 | 用户映射 | 存储 | P1 | 正向 |

---

## 3. 详细测试用例

### 3.1 基础设施

#### TC-HEALTH-001：健康检查接口

| 项 | 内容 |
|----|------|
| **目的** | 验证系统健康检查端点正常响应 |
| **前置条件** | 服务器已启动 |
| **输入** | `GET /health` |
| **预期输出** | HTTP 200，响应体包含 `"status": "ok"` |
| **测试文件** | `cmd/server/main_test.go:TestHealthCheck` |

---

### 3.2 认证模块

#### TC-AUTH-001：飞书登录 URL 生成

| 项 | 内容 |
|----|------|
| **目的** | 验证飞书 OAuth 登录 URL 正确生成 |
| **前置条件** | 配置文件中已配置 `feishu.app_id` 和 `feishu.redirect_uri` |
| **输入** | `GET /api/v1/auth/lark/login` |
| **预期输出** | HTTP 200，返回的 `auth_url` 包含 `open.feishu.cn` 域名 |
| **测试文件** | `cmd/server/main_test.go:TestLarkLoginURL` |

#### TC-AUTH-002：飞书 callback 缺少 code 参数

| 项 | 内容 |
|----|------|
| **目的** | 验证 callback 接口在缺少必要参数时返回 400 |
| **前置条件** | 无 |
| **输入** | `GET /api/v1/auth/lark/callback`（无 `code` 参数） |
| **预期输出** | HTTP 400，错误信息为 `缺少 code 参数` |
| **测试文件** | `cmd/server/main_test.go:TestLarkCallbackMissingCode` |

---

### 3.3 周报模块

#### TC-REPORT-001：未登录访问周报列表

| 项 | 内容 |
|----|------|
| **目的** | 验证未登录用户无法访问周报数据 |
| **前置条件** | 无 cookie |
| **输入** | `GET /api/v1/reports` |
| **预期输出** | HTTP 401，错误信息为 `未登录` |
| **测试文件** | `cmd/server/main_test.go:TestListReportsUnauthorized` |

#### TC-REPORT-002：创建并查询周报

| 项 | 内容 |
|----|------|
| **目的** | 验证周报创建和查询流程 |
| **前置条件** | 用户已登录（cookie 中有 `user_id`） |
| **输入** | `GET /api/v1/reports/2026-06-23` |
| **预期输出** | HTTP 200，返回的周报 `week_start` 为 `2026-06-23` |
| **测试文件** | `cmd/server/main_test.go:TestCreateAndGetReport` |

#### TC-REPORT-003：查询不存在的周报

| 项 | 内容 |
|----|------|
| **目的** | 验证查询不存在周报时返回 404 |
| **前置条件** | 用户已登录 |
| **输入** | `GET /api/v1/reports/2099-01-01` |
| **预期输出** | HTTP 404，错误信息为 `周报不存在` |
| **测试文件** | `cmd/server/main_test.go:TestGetReportNotFound` |

---

### 3.4 导出模块（核心修复区域）

#### TC-EXPORT-001：导出 Markdown

| 项 | 内容 |
|----|------|
| **目的** | 验证 Markdown 格式导出正常 |
| **前置条件** | 周报已存在，用户已登录 |
| **输入** | `GET /api/v1/export/2026-06-23?format=markdown` |
| **预期输出** | HTTP 200，Content-Type 为 `text/markdown`，响应体为原始 Markdown 文本 |
| **测试文件** | `cmd/server/main_test.go:TestExportMarkdown` |

#### TC-EXPORT-002：导出 Word

| 项 | 内容 |
|----|------|
| **目的** | 验证 Word 格式导出正常（.docx） |
| **前置条件** | 周报已存在，用户已登录 |
| **输入** | `GET /api/v1/export/2026-06-23?format=word` |
| **预期输出** | HTTP 200，Content-Type 为 `application/vnd.openxmlformats-officedocument.wordprocessingml.document`，响应体非空 |
| **测试文件** | `cmd/server/main_test.go:TestExportWord` |

#### TC-EXPORT-003：导出 PDF（打印 HTML）

| 项 | 内容 |
|----|------|
| **目的** | 验证 PDF 导出返回可打印的 HTML 页面（修复核心） |
| **前置条件** | 周报已存在，用户已登录 |
| **输入** | `GET /api/v1/export/2026-06-23?format=pdf` |
| **预期输出** | HTTP 200，Content-Type 为 `text/html`，响应体包含：`<!DOCTYPE html>`、`window.print()` 按钮、A4 打印 CSS、渲染后的 Markdown 内容 |
| **测试文件** | `cmd/server/main_test.go:TestExportPDF` |

#### TC-EXPORT-004：导出不支持的格式

| 项 | 内容 |
|----|------|
| **目的** | 验证不支持的格式返回 400 |
| **前置条件** | 周报已存在，用户已登录 |
| **输入** | `GET /api/v1/export/2026-06-23?format=excel` |
| **预期输出** | HTTP 400，错误信息为 `不支持的导出格式` |
| **测试文件** | `cmd/server/main_test.go:TestExportInvalidFormat` |

#### TC-EXPORT-005：导出周报不存在

| 项 | 内容 |
|----|------|
| **目的** | 验证导出不存在周报时返回 404 |
| **前置条件** | 用户已登录 |
| **输入** | `GET /api/v1/export/2099-01-01?format=pdf` |
| **预期输出** | HTTP 404，错误信息为 `周报不存在` |
| **测试文件** | `cmd/server/main_test.go:TestExportReportNotFound` |

---

### 3.5 浏览器插件采集

#### TC-BROWSER-001：浏览器插件推送数据

| 项 | 内容 |
|----|------|
| **目的** | 验证浏览器插件可以推送多条记录并自动生成周报 |
| **前置条件** | 无 |
| **输入** | `POST /api/v1/collect/browser`，请求体包含 `user_id`、`week_start` 和多条记录 |
| **预期输出** | HTTP 200，数据库中生成对应周报，包含所有推送记录 |
| **测试文件** | `cmd/server/main_test.go:TestBrowserCollect` |

---

### 3.6 数据源管理

#### TC-DS-001：创建数据源

| 项 | 内容 |
|----|------|
| **目的** | 验证创建数据源成功 |
| **前置条件** | 用户已登录 |
| **输入** | `POST /api/v1/datasources`，请求体包含 `type`、`name`、`config` |
| **预期输出** | HTTP 200，数据源创建成功 |
| **测试文件** | `cmd/server/main_test.go:TestCreateDataSource` |

#### TC-DS-002：未登录创建数据源

| 项 | 内容 |
|----|------|
| **目的** | 验证未登录无法创建数据源 |
| **前置条件** | 无 cookie |
| **输入** | `POST /api/v1/datasources` |
| **预期输出** | HTTP 401，错误信息为 `未登录` |
| **测试文件** | `cmd/server/main_test.go:TestCreateDataSourceUnauthorized` |

#### TC-DS-003：列出数据源

| 项 | 内容 |
|----|------|
| **目的** | 验证列出用户所有数据源 |
| **前置条件** | 用户已登录，已创建至少一个数据源 |
| **输入** | `GET /api/v1/datasources` |
| **预期输出** | HTTP 200，返回的数据源列表长度与创建数量一致 |
| **测试文件** | `cmd/server/main_test.go:TestListDataSources` |

#### TC-DS-004：删除数据源

| 项 | 内容 |
|----|------|
| **目的** | 验证删除数据源后数据库中不存在 |
| **前置条件** | 用户已登录，数据源已存在 |
| **输入** | `DELETE /api/v1/datasources/:id` |
| **预期输出** | HTTP 200，再次查询返回不存在 |
| **测试文件** | `cmd/server/main_test.go:TestDeleteDataSource` |

---

### 3.7 模板管理

#### TC-TMPL-001：列出模板

| 项 | 内容 |
|----|------|
| **目的** | 验证列出模板（含默认模板） |
| **前置条件** | 用户已登录，系统已初始化默认模板 |
| **输入** | `GET /api/v1/templates` |
| **预期输出** | HTTP 200，返回至少包含默认模板 |
| **测试文件** | `cmd/server/main_test.go:TestListTemplates` |

#### TC-TMPL-002：创建模板

| 项 | 内容 |
|----|------|
| **目的** | 验证创建自定义模板 |
| **前置条件** | 用户已登录 |
| **输入** | `POST /api/v1/templates`，请求体包含 `name`、`content`、`role` |
| **预期输出** | HTTP 200，模板创建成功 |
| **测试文件** | `cmd/server/main_test.go:TestCreateTemplate` |

#### TC-TMPL-003：删除模板

| 项 | 内容 |
|----|------|
| **目的** | 验证删除模板后数据库中不存在 |
| **前置条件** | 用户已登录，模板已存在 |
| **输入** | `DELETE /api/v1/templates/:id` |
| **预期输出** | HTTP 200，再次查询返回不存在 |
| **测试文件** | `cmd/server/main_test.go:TestDeleteTemplate` |

---

### 3.8 HTML 渲染与安全

#### TC-HTML-001：HTML 打印页面包含必要元素

| 项 | 内容 |
|----|------|
| **目的** | 验证 `generateHTMLPrintPage` 生成的 HTML 完整且可打印 |
| **前置条件** | 无 |
| **输入** | Markdown 文本：含 `##`、`###`、`-`、普通段落 |
| **预期输出** | HTML 包含：`<!DOCTYPE html>`、`<html lang="zh-CN">`、A4 `@page` CSS、打印按钮、`<h2>`、`<h3>`、`<li>`、`<p>` 标签 |
| **测试文件** | `cmd/server/main_test.go:TestGenerateHTMLPrintPage` |

#### TC-HTML-002：HTML 转义防止 XSS

| 项 | 内容 |
|----|------|
| **目的** | 验证 Markdown 中的恶意脚本被正确转义（安全修复） |
| **前置条件** | 无 |
| **输入** | Markdown 文本包含 `<script>alert(1)</script>` |
| **预期输出** | HTML 输出中不包含原始 `<script>` 标签，而是转义为 `&lt;script&gt;` |
| **测试文件** | `cmd/server/main_test.go:TestGenerateHTMLPrintPageEscaping` |

---

### 3.9 存储层

#### TC-STORE-001：存储层报告版本快照

| 项 | 内容 |
|----|------|
| **目的** | 验证 `SaveReportVersion` 和 `GetReportVersions` 正常工作 |
| **前置条件** | 无 |
| **输入** | 创建周报，保存版本快照 |
| **预期输出** | 版本快照保存成功，查询返回正确数量 |
| **测试文件** | `cmd/server/main_test.go:TestStoreSaveReportVersion` |

#### TC-STORE-002：Token 加密解密

| 项 | 内容 |
|----|------|
| **目的** | 验证 Token 存储时加密、读取时解密 |
| **前置条件** | 无 |
| **输入** | `SaveToken("user_test_001", "secret_token_123", time.Hour)` |
| **预期输出** | `GetToken` 返回原始 token，`ok` 为 true |
| **测试文件** | `cmd/server/main_test.go:TestStoreTokenEncryptDecrypt` |

#### TC-STORE-003：WorkRecord 隐藏/显示

| 项 | 内容 |
|----|------|
| **目的** | 验证 `HideWorkRecord` 可正确隐藏记录 |
| **前置条件** | 无 |
| **输入** | 创建 WorkRecord，调用 `HideWorkRecord(id, true)` |
| **预期输出** | `GetWorkRecords` 不再返回该记录 |
| **测试文件** | `cmd/server/main_test.go:TestStoreHideWorkRecord` |

#### TC-STORE-004：用户映射

| 项 | 内容 |
|----|------|
| **目的** | 验证浏览器插件用户 ID 映射到真实用户 ID |
| **前置条件** | 无 |
| **输入** | `SetUserMapping("browser_123", "real_user_456")` |
| **预期输出** | `GetRealUserID("browser_123")` 返回 `"real_user_456"`，未映射的返回自身 |
| **测试文件** | `cmd/server/main_test.go:TestStoreUserMapping` |

---

## 4. 测试环境

| 环境项 | 值 |
|--------|-----|
| 数据库 | SQLite 内存模式（`:memory:`），每个测试独立 |
| HTTP 框架 | Gin 测试模式（`gin.TestMode`） |
| 测试工具 | Go 标准库 `testing` + `httptest` |
| 并发策略 | 串行执行（避免共享数据库状态） |
| 覆盖率目标 | ≥ 35%（当前 38.1%） |

---

## 5. 测试文件位置

```
cmd/server/main_test.go          # 25 个集成测试用例
```

---

## 6. 执行命令

```bash
# 运行所有测试
go test -v ./cmd/server/...

# 运行特定测试
go test -v ./cmd/server/... -run TestExportPDF

# 生成覆盖率报告
go test -coverprofile=coverage.out ./cmd/server/...
go tool cover -func=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

---

*本文件由 Claude Code 自动生成。*
