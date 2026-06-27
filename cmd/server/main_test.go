package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"weekly-report-system/internal/adapter/lark"
	"weekly-report-system/internal/application/collector"
	"weekly-report-system/internal/config"
	"weekly-report-system/internal/model"
	"weekly-report-system/internal/store"
)

// authCookies 生成与生产环境一致的 JWT + user_id cookie，用于通过 authMiddleware
func authCookies(userID string) []*http.Cookie {
	claims := jwt.MapClaims{"user_id": userID, "exp": time.Now().Add(time.Hour).Unix()}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := tok.SignedString([]byte(cfg.JWT.Secret))
	return []*http.Cookie{{Name: "token", Value: s}, {Name: "user_id", Value: userID}}
}

func setupTestServer(t *testing.T) (*gin.Engine, *store.Store) {
	gin.SetMode(gin.TestMode)

	// 每个测试使用独立的内存数据库，避免数据污染
	dbName := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db failed: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.DataSource{},
		&model.WorkRecord{},
		&model.WeeklyReport{},
		&model.WeeklyReportVersion{},
		&model.Template{},
		&model.AuditLog{},
		&model.CronJob{},
		&model.ReportComment{},
		&model.FeishuToken{},
	); err != nil {
		t.Fatalf("migrate test db failed: %v", err)
	}

	s := store.New(db)
	s.InitDefaultTemplates()

	// 临时设置全局变量（测试串行执行，无需恢复）
	cfg = &config.Config{}
	cfg.Server.Port = "8080"
	cfg.Server.Mode = "test"
	cfg.JWT.Secret = "test-secret-key-for-jwt-signing-only"
	cfg.JWT.ExpireHours = 168
	cfg.Feishu.AppID = "cli_test"
	cfg.Feishu.AppSecret = "test_secret"
	cfg.Feishu.RedirectURI = "http://localhost:8080/callback"
	storeDB = s
	store.SetTokenKey(cfg.JWT.Secret)
	larkClient = lark.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret)
	collectorSvc = collector.New(lark.NewAdapter(larkClient), s)

	r := gin.New()
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	r.NoRoute(gin.WrapH(http.FileServer(http.Dir("./web"))))
	// 公开路由
	r.GET("/api/v1/auth/lark/login", larkLoginHandler)
	r.GET("/api/v1/auth/lark/callback", larkCallbackHandler)

	// 需要登录的路由（与生产环境一致）
	authorized := r.Group("/")
	authorized.Use(authMiddleware())
	{
		authorized.POST("/api/v1/collect", collectHandler)
		authorized.POST("/api/v1/collect/browser", browserCollectHandler)
		authorized.GET("/api/v1/reports/:week", getReportHandler)
		authorized.GET("/api/v1/reports", listReportsHandler)
		authorized.GET("/api/v1/export/:week", exportHandler)
		authorized.GET("/api/v1/datasources", listDataSourcesHandler)
		authorized.POST("/api/v1/datasources", createDataSourceHandler)
		authorized.GET("/api/v1/datasources/:id", getDataSourceHandler)
		authorized.PUT("/api/v1/datasources/:id", updateDataSourceHandler)
		authorized.DELETE("/api/v1/datasources/:id", deleteDataSourceHandler)
		authorized.GET("/api/v1/templates", listTemplatesHandler)
		authorized.POST("/api/v1/templates", createTemplateHandler)
		authorized.GET("/api/v1/templates/:id", getTemplateHandler)
		authorized.PUT("/api/v1/templates/:id", updateTemplateHandler)
		authorized.DELETE("/api/v1/templates/:id", deleteTemplateHandler)
	}

	return r, s
}

// ------------------- 测试用例 -------------------

// TC-HEALTH-001: 健康检查接口
func TestHealthCheck(t *testing.T) {
	r, _ := setupTestServer(t)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Errorf("expected body to contain 'ok', got %s", w.Body.String())
	}
}

// TC-AUTH-001: 飞书登录 URL 生成
func TestLarkLoginURL(t *testing.T) {
	r, _ := setupTestServer(t)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/auth/lark/login", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	data := resp["data"].(map[string]interface{})
	authURL := data["auth_url"].(string)
	if !strings.Contains(authURL, "open.feishu.cn") {
		t.Errorf("expected auth_url to contain feishu domain, got %s", authURL)
	}
}

// TC-AUTH-002: 飞书 callback 缺少 code 参数
func TestLarkCallbackMissingCode(t *testing.T) {
	r, _ := setupTestServer(t)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/auth/lark/callback", nil)
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// TC-REPORT-001: 未登录访问周报列表返回 401
func TestListReportsUnauthorized(t *testing.T) {
	r, _ := setupTestServer(t)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/reports", nil)
	r.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

// TC-REPORT-002: 创建并查询周报
func TestCreateAndGetReport(t *testing.T) {
	r, s := setupTestServer(t)

	// 创建一个周报
	report := &model.WeeklyReport{
		UserID:    "user_test_001",
		WeekStart: "2026-06-23",
		WeekEnd:   "2026-06-27",
		Markdown:  "## 周报\n\n- 任务1\n",
		Status:    "draft",
	}
	s.SaveReport(report)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/reports/2026-06-23", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	data := resp["data"].(map[string]interface{})
	reportData := data["report"].(map[string]interface{})
	if reportData["week_start"] != "2026-06-23" {
		t.Errorf("expected week_start 2026-06-23, got %v", reportData["week_start"])
	}
}

// TC-REPORT-003: 查询不存在的周报返回 404
func TestGetReportNotFound(t *testing.T) {
	r, _ := setupTestServer(t)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/reports/2099-01-01", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// TC-EXPORT-001: 导出 Markdown
func TestExportMarkdown(t *testing.T) {
	r, s := setupTestServer(t)

	report := &model.WeeklyReport{
		UserID:    "user_test_001",
		WeekStart: "2026-06-23",
		Markdown:  "## 周报\n\n- 任务1\n",
		Status:    "draft",
	}
	s.SaveReport(report)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/export/2026-06-23?format=markdown", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/markdown") {
		t.Errorf("expected Content-Type text/markdown, got %s", w.Header().Get("Content-Type"))
	}
	if w.Body.String() != "## 周报\n\n- 任务1\n" {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

// TC-EXPORT-002: 导出 Word
func TestExportWord(t *testing.T) {
	r, s := setupTestServer(t)

	report := &model.WeeklyReport{
		UserID:    "user_test_001",
		WeekStart: "2026-06-23",
		Markdown:  "## 周报\n\n- 任务1\n",
		Status:    "draft",
	}
	s.SaveReport(report)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/export/2026-06-23?format=word", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "wordprocessingml") {
		t.Errorf("expected Content-Type wordprocessingml, got %s", w.Header().Get("Content-Type"))
	}
	if w.Body.Len() == 0 {
		t.Errorf("expected non-empty body for word export")
	}
}

// TC-EXPORT-003: 导出 PDF（返回真实 PDF 文件）
func TestExportPDF(t *testing.T) {
	r, s := setupTestServer(t)

	report := &model.WeeklyReport{
		UserID:    "user_test_001",
		WeekStart: "2026-06-23",
		Markdown:  "## 周报\n\n### 本周工作\n\n- 任务1\n\n### 下周计划\n\n- 待补充\n",
		Status:    "draft",
	}
	s.SaveReport(report)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/export/2026-06-23?format=pdf", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/pdf") {
		t.Errorf("expected Content-Type application/pdf, got %s", w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "weekly-report-2026-06-23.pdf") {
		t.Errorf("expected pdf attachment filename, got %s", w.Header().Get("Content-Disposition"))
	}
	body := w.Body.Bytes()
	if len(body) < 4 || string(body[:4]) != "%PDF" {
		t.Errorf("expected body to start with %%PDF magic header, got % x", body[:min(4, len(body))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TC-EXPORT-004: 导出不支持的格式返回 400
func TestExportInvalidFormat(t *testing.T) {
	r, s := setupTestServer(t)

	report := &model.WeeklyReport{
		UserID:    "user_test_001",
		WeekStart: "2026-06-23",
		Markdown:  "## 周报\n",
		Status:    "draft",
	}
	s.SaveReport(report)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/export/2026-06-23?format=excel", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

// TC-EXPORT-005: 导出周报不存在返回 404
func TestExportReportNotFound(t *testing.T) {
	r, _ := setupTestServer(t)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/export/2099-01-01?format=pdf", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// TC-BROWSER-001: 浏览器插件推送数据
func TestBrowserCollect(t *testing.T) {
	r, s := setupTestServer(t)

	payload := map[string]interface{}{
		"user_id":    "browser_user_001",
		"week_start": "2026-06-23",
		"records": []map[string]string{
			{"title": "JIRA-123 修复bug", "description": "修复登录问题", "source": "jira", "url": "https://jira.example.com/JIRA-123"},
			{"title": "PR #456", "description": "代码审查", "source": "github", "url": "https://github.com/example/pr/456"},
		},
	}
	body, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/collect/browser", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// 验证周报已生成
	report, ok := s.GetReport("user_test_001", "2026-06-23")
	if !ok {
		t.Errorf("expected report to be created after browser collect")
	}
	if !strings.Contains(report.Markdown, "JIRA-123") {
		t.Errorf("expected report to contain JIRA-123 task")
	}
}

// TC-DS-001: 创建数据源
func TestCreateDataSource(t *testing.T) {
	r, _ := setupTestServer(t)

	payload := map[string]interface{}{
		"type":   "gitlab",
		"name":   "公司GitLab",
		"config": map[string]string{"url": "https://gitlab.example.com", "token": "glpat-xxx"},
	}
	body, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/datasources", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TC-DS-002: 未登录创建数据源返回 401
func TestCreateDataSourceUnauthorized(t *testing.T) {
	r, _ := setupTestServer(t)

	payload := map[string]interface{}{
		"type":   "gitlab",
		"name":   "公司GitLab",
		"config": map[string]string{"url": "https://gitlab.example.com"},
	}
	body, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/datasources", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

// TC-DS-003: 列出数据源
func TestListDataSources(t *testing.T) {
	r, s := setupTestServer(t)

	// 先创建数据源
	ds := &model.DataSource{
		UserID: "user_test_001",
		Type:   "github",
		Name:   "公司GitHub",
		Config: `{"url":"https://github.com/example"}`,
	}
	s.CreateDataSource(ds)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/datasources", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].([]interface{})
	if len(data) != 1 {
		t.Errorf("expected 1 data source, got %d", len(data))
	}
}

// TC-DS-004: 删除数据源
func TestDeleteDataSource(t *testing.T) {
	r, s := setupTestServer(t)

	ds := &model.DataSource{
		UserID: "user_test_001",
		Type:   "github",
		Name:   "公司GitHub",
		Config: `{"url":"https://github.com/example"}`,
	}
	s.CreateDataSource(ds)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("/api/v1/datasources/%d", ds.ID), nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// 验证已删除
	_, err := s.GetDataSourceByID(ds.ID)
	if err == nil {
		t.Errorf("expected data source to be deleted")
	}
}

// TC-TMPL-001: 列出模板
func TestListTemplates(t *testing.T) {
	r, s := setupTestServer(t)

	// 创建用户
	s.CreateOrUpdateUser(&model.User{FeishuOpenID: "user_test_001", Name: "Tester", Role: "developer"})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/templates", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].([]interface{})
	if len(data) == 0 {
		t.Errorf("expected at least default templates")
	}
}

// TC-TMPL-002: 创建模板
func TestCreateTemplate(t *testing.T) {
	r, _ := setupTestServer(t)

	payload := map[string]interface{}{
		"name":    "开发周报模板",
		"content": "## 周报\n\n### 本周工作\n{{range .Tasks}}\n- {{.Title}}\n{{end}}",
		"role":    "developer",
	}
	body, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

// TC-TMPL-004: 创建后立即出现在列表（回归：真实登录用户系统角色为 member，
// 新建模板的岗位角色为 developer，列表不应因系统角色过滤掉新模板）
func TestCreateThenListTemplate(t *testing.T) {
	r, s := setupTestServer(t)

	// 模拟真实飞书登录：仅设置 Name，Role 走默认值 "member"
	s.CreateOrUpdateUser(&model.User{FeishuOpenID: "user_test_001", Name: "Tester"})

	// 1) 创建一个岗位角色为 developer 的个人模板
	payload := map[string]interface{}{
		"name":        "我的开发周报模板",
		"description": "round-trip",
		"content":     "## 周报\n{{range .Tasks}}- {{.Title}}\n{{end}}",
		"role":        "developer",
	}
	body, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/templates", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("create expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// 2) 列表应包含刚创建的模板
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/templates", nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("list expected status 200, got %d", w.Code)
	}
	var resp struct {
		Code int              `json:"code"`
		Data []model.Template `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal list resp: %v", err)
	}
	found := false
	for _, tmpl := range resp.Data {
		if tmpl.Name == "我的开发周报模板" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("newly created template should appear in list, got %d templates: %s", len(resp.Data), w.Body.String())
	}
	_ = s
}

// TC-TMPL-003: 删除模板
func TestDeleteTemplate(t *testing.T) {
	r, s := setupTestServer(t)

	tmpl := &model.Template{
		UserID:  "user_test_001",
		Name:    "测试模板",
		Content: "## 测试\n",
		Role:    "developer",
		Scope:   "personal",
	}
	s.SaveTemplate(tmpl)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("/api/v1/templates/%d", tmpl.ID), nil)
	for _, ck := range authCookies("user_test_001") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	_, err := s.GetTemplateByID(tmpl.ID)
	if err == nil {
		t.Errorf("expected template to be deleted")
	}
}

// TC-TMPL-005: 用户 B 不能删除用户 A 的个人模板（应被拒绝：403 或 404）
func TestDeleteOtherUsersTemplateForbidden(t *testing.T) {
	r, s := setupTestServer(t)

	// 用户 A 拥有的个人模板
	tmpl := &model.Template{
		UserID:  "user_A",
		Name:    "A 的私有模板",
		Content: "## A\n",
		Role:    "developer",
		Scope:   "personal",
	}
	s.SaveTemplate(tmpl)

	// 用户 B 尝试删除
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("/api/v1/templates/%d", tmpl.ID), nil)
	for _, ck := range authCookies("user_B") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)

	if w.Code != 403 && w.Code != 404 {
		t.Errorf("expected status 403 or 404, got %d", w.Code)
	}

	// 模板仍应存在
	if _, err := s.GetTemplateByID(tmpl.ID); err != nil {
		t.Errorf("expected A's template to still exist, got err: %v", err)
	}

	// 用户 B 的模板列表中不应出现 A 的个人模板
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/templates", nil)
	for _, ck := range authCookies("user_B") {
		req.AddCookie(ck)
	}
	r.ServeHTTP(w, req)
	var resp struct {
		Code int              `json:"code"`
		Data []model.Template `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	for _, tm := range resp.Data {
		if tm.Name == "A 的私有模板" {
			t.Errorf("user B should not see user A's personal template in list")
		}
	}
}

// TC-HTML-001: 验证 HTML 打印页面包含必要元素
func TestGenerateHTMLPrintPage(t *testing.T) {
	rec := httptest.NewRecorder()
	markdown := "## 周报\n\n### 本周工作\n\n- 任务1\n- 任务2\n\n普通段落\n"
	generateHTMLPrintPage(markdown, rec)

	body := rec.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("expected HTML5 doctype")
	}
	if !strings.Contains(body, "window.print()") {
		t.Errorf("expected print button")
	}
	if !strings.Contains(body, "@page { size: A4") {
		t.Errorf("expected A4 page CSS")
	}
	if !strings.Contains(body, "<h2>周报</h2>") {
		t.Errorf("expected h2 rendered")
	}
	if !strings.Contains(body, "<h3>本周工作</h3>") {
		t.Errorf("expected h3 rendered")
	}
	if !strings.Contains(body, "<li>任务1</li>") {
		t.Errorf("expected list item rendered")
	}
	if !strings.Contains(body, "<p>普通段落</p>") {
		t.Errorf("expected paragraph rendered")
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("unexpected script tag")
	}
}

// TC-HTML-002: 验证 HTML 转义防止 XSS
func TestGenerateHTMLPrintPageEscaping(t *testing.T) {
	rec := httptest.NewRecorder()
	markdown := "## <script>alert(1)</script>\n"
	generateHTMLPrintPage(markdown, rec)

	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("expected XSS payload to be escaped")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("expected escaped script tag")
	}
}

// TC-STORE-001: 存储层报告版本快照
func TestStoreSaveReportVersion(t *testing.T) {
	_, s := setupTestServer(t)

	report := &model.WeeklyReport{
		UserID:    "user_test_001",
		WeekStart: "2026-06-23",
		Markdown:  "v1",
		Status:    "draft",
	}
	s.SaveReport(report)

	version := &model.WeeklyReportVersion{
		ReportID: report.ID,
		Content:  "v1 content",
		Version:  1,
	}
	if err := s.SaveReportVersion(version); err != nil {
		t.Errorf("save report version failed: %v", err)
	}

	versions, err := s.GetReportVersions(report.ID)
	if err != nil {
		t.Errorf("get report versions failed: %v", err)
	}
	if len(versions) != 1 {
		t.Errorf("expected 1 version, got %d", len(versions))
	}
}

// TC-STORE-002: 存储层 Token 加密解密
func TestStoreTokenEncryptDecrypt(t *testing.T) {
	_, s := setupTestServer(t)

	s.SaveToken("user_test_001", "secret_token_123", time.Hour)
	token, ok := s.GetToken("user_test_001")
	if !ok {
		t.Errorf("expected token to be found")
	}
	if token != "secret_token_123" {
		t.Errorf("expected token to match, got %s", token)
	}
}

// TC-STORE-003: 存储层 WorkRecord 隐藏/显示
func TestStoreHideWorkRecord(t *testing.T) {
	_, s := setupTestServer(t)

	records := []model.WorkRecord{
		{UserID: "user_test_001", SourceType: "lark", RecordType: model.TypeTask, Title: "任务1", WeekStart: "2026-06-23"},
	}
	s.SaveWorkRecords(records)

	// 获取记录 ID
	all, _ := s.GetWorkRecords("user_test_001", "2026-06-23")
	if len(all) == 0 {
		t.Fatalf("expected records to exist")
	}
	id := all[0].ID

	// 隐藏
	if err := s.HideWorkRecord("user_test_001", id, true); err != nil {
		t.Errorf("hide work record failed: %v", err)
	}

	hidden, _ := s.GetWorkRecords("user_test_001", "2026-06-23")
	if len(hidden) != 0 {
		t.Errorf("expected hidden records to be excluded, got %d", len(hidden))
	}
}

// TC-STORE-004: 用户映射
func TestStoreUserMapping(t *testing.T) {
	_, s := setupTestServer(t)

	s.SetUserMapping("browser_123", "real_user_456")
	realID := s.GetRealUserID("browser_123")
	if realID != "real_user_456" {
		t.Errorf("expected real_user_456, got %s", realID)
	}

	// 未映射的返回自身
	selfID := s.GetRealUserID("unknown_browser")
	if selfID != "unknown_browser" {
		t.Errorf("expected unknown_browser, got %s", selfID)
	}
}

// TC-COLLECT-COMMIT-001: git commit 记录应被渲染进周报正文（回归 Problem 1）
func TestCommitRecordsRendered(t *testing.T) {
	report := &model.WeeklyReport{WeekStart: "2026-06-22", WeekEnd: "2026-06-28"}
	records := []model.WorkRecord{
		{SourceType: "github", RecordType: model.TypeCommit, Title: "feat: 实现登录功能", ProjectName: "org/repo", OccurredAt: time.Now()},
		{SourceType: "gitlab", RecordType: model.TypeCommit, Title: "fix: 修复采集 bug", ProjectName: "grp/proj", OccurredAt: time.Now()},
	}
	// 默认模板（空 templateContent 走 fallback）与具名模板都应包含提交标题
	md := collector.RenderReport(report, records, nil, "")
	if !strings.Contains(md, "feat: 实现登录功能") || !strings.Contains(md, "fix: 修复采集 bug") {
		t.Errorf("expected commit titles to appear in rendered markdown, got:\n%s", md)
	}
}

// 测试主入口（使 go test 可以运行）
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
