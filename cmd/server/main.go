package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"weekly-report-system/internal/adapter/lark"
	"weekly-report-system/internal/config"
	"weekly-report-system/internal/model"
	"weekly-report-system/internal/store"

	"github.com/fumiama/go-docx"
)

var (
	cfg       *config.Config
	storeDB   *store.Store
	larkClient *lark.Client

	// 简单内存限流器
	rateLimiter = NewRateLimiter(10, time.Second)
)

// ------------------- 中间件 -------------------

// RateLimiter 基于 IP 的滑动窗口限流器
type RateLimiter struct {
	limit  int
	window time.Duration
	mu     sync.RWMutex
	visits map[string][]time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:  limit,
		window: window,
		visits: make(map[string][]time.Time),
	}
}

func (rl *RateLimiter) Allow(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-rl.window)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// 清理过期记录
	visits := rl.visits[ip]
	var fresh []time.Time
	for _, t := range visits {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= rl.limit {
		rl.visits[ip] = fresh
		return false
	}
	rl.visits[ip] = append(fresh, now)
	return true
}

func rateLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if !rateLimiter.Allow(ip) {
			c.JSON(429, gin.H{"error": "请求过于频繁，请稍后重试"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// authMiddleware 验证 JWT token
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString, err := c.Cookie("token")
		if err != nil || tokenString == "" {
			c.JSON(401, gin.H{"error": "未登录"})
			c.Abort()
			return
		}

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(cfg.JWT.Secret), nil
		})
		if err != nil || !token.Valid {
			c.JSON(401, gin.H{"error": "登录已过期，请重新登录"})
			c.Abort()
			return
		}

		// 验证 token 中的 user_id 与 cookie 中的 user_id 是否一致
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(401, gin.H{"error": "无效的登录凭证"})
			c.Abort()
			return
		}
		tokenUserID, _ := claims["user_id"].(string)
		cookieUserID, _ := c.Cookie("user_id")
		if subtle.ConstantTimeCompare([]byte(tokenUserID), []byte(cookieUserID)) != 1 {
			c.JSON(401, gin.H{"error": "登录凭证不匹配"})
			c.Abort()
			return
		}

		c.Next()
	}
}

func main() {
	var err error
	cfg, err = config.Load()
	if err != nil {
		panic(err)
	}

	// 初始化数据库
	db, err := gorm.Open(sqlite.Open(cfg.Database.Path), &gorm.Config{})
	if err != nil {
		panic(fmt.Sprintf("open db failed: %v", err))
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
	); err != nil {
		panic(fmt.Sprintf("migrate db failed: %v", err))
	}

	storeDB = store.New(db)
	storeDB.InitDefaultTemplates()
	larkClient = lark.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret)

	gin.SetMode(gin.ReleaseMode)
	if cfg.Server.Mode == "debug" {
		gin.SetMode(gin.DebugMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())
	// 全局日志中间件
	r.Use(func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery
		c.Next()
		latency := time.Since(start)
		clientIP := c.ClientIP()
		method := c.Request.Method
		statusCode := c.Writer.Status()
		fmt.Printf("[GIN] %v | %3d | %13v | %15s | %-7s %s\n",
			start.Format("2006/01/02 - 15:04:05"),
			statusCode,
			latency,
			clientIP,
			method,
			path,
		)
		if raw != "" {
			fmt.Printf("?%s", raw)
		}
		fmt.Println()
	})
	// 跨域
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowOrigins = []string{"http://localhost:8080", "http://localhost:3000"}
	corsConfig.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type", "Authorization", "Cookie"}
	corsConfig.AllowCredentials = true
	// 允许 chrome-extension://* 来源（浏览器插件）
	corsConfig.AllowOriginFunc = func(origin string) bool {
		return origin == "http://localhost:8080" || origin == "http://localhost:3000" || strings.HasPrefix(origin, "chrome-extension://")
	}
	r.Use(cors.New(corsConfig))
	// 限流
	r.Use(rateLimitMiddleware())

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// 飞书 OAuth（不需要登录）
	r.GET("/api/v1/auth/status", authStatusHandler)
	r.GET("/api/v1/auth/lark/login", larkLoginHandler)
	r.GET("/api/v1/auth/lark/callback", larkCallbackHandler)
	r.POST("/api/v1/auth/logout", logoutHandler)

	// 浏览器插件推送（不需要 JWT，但有自己的用户映射）
	r.POST("/api/v1/collect/browser", browserCollectHandler)

	// 需要登录的 API
	authorized := r.Group("/")
	authorized.Use(authMiddleware())
	{
		// 周报收集
		authorized.POST("/api/v1/collect", collectHandler)

		// 周报查询
		authorized.GET("/api/v1/reports/:week", getReportHandler)
		authorized.GET("/api/v1/reports", listReportsHandler)
		authorized.POST("/api/v1/reports/:week/submit", submitReportHandler)
		authorized.GET("/api/v1/reports/:week/compare", compareReportHandler)
		authorized.GET("/api/v1/reports/:week/versions", listReportVersionsHandler)

		// 批注
		authorized.GET("/api/v1/reports/:week/comments", listCommentsHandler)
		authorized.POST("/api/v1/reports/:week/comments", addCommentHandler)
		authorized.DELETE("/api/v1/reports/:week/comments/:id", deleteCommentHandler)

		// 导出
		authorized.GET("/api/v1/export/:week", exportHandler)

		// 数据源管理
		authorized.GET("/api/v1/datasources", listDataSourcesHandler)
		authorized.POST("/api/v1/datasources", createDataSourceHandler)
		authorized.GET("/api/v1/datasources/:id", getDataSourceHandler)
		authorized.PUT("/api/v1/datasources/:id", updateDataSourceHandler)
		authorized.DELETE("/api/v1/datasources/:id", deleteDataSourceHandler)
		authorized.POST("/api/v1/datasources/:id/test", testDataSourceHandler)

		// 模板管理
		authorized.GET("/api/v1/templates", listTemplatesHandler)
		authorized.POST("/api/v1/templates", createTemplateHandler)
		authorized.GET("/api/v1/templates/:id", getTemplateHandler)
		authorized.PUT("/api/v1/templates/:id", updateTemplateHandler)
		authorized.DELETE("/api/v1/templates/:id", deleteTemplateHandler)

		// 提醒测试
		authorized.POST("/api/v1/admin/remind", testRemindHandler)
	}

	// 前端页面
	r.NoRoute(func(c *gin.Context) {
		// API 路径未匹配，返回 JSON 404
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(404, gin.H{"error": "接口不存在"})
			return
		}
		// 根路径重定向到 test.html
		if c.Request.URL.Path == "/" {
			c.Redirect(302, "/test.html")
			return
		}
		// 其他路径尝试静态文件
		http.FileServer(http.Dir("./web")).ServeHTTP(c.Writer, c.Request)
	})

	fmt.Println("Server running on http://localhost:" + cfg.Server.Port)
	r.Run(":" + cfg.Server.Port)
}

// ------------------- Handler 实现 -------------------

func authStatusHandler(c *gin.Context) {
	userID, err := c.Cookie("user_id")
	if err != nil || userID == "" {
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"logged_in": false}})
		return
	}

	// 验证 JWT 是否有效
	tokenString, err := c.Cookie("token")
	if err != nil || tokenString == "" {
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"logged_in": false}})
		return
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return []byte(cfg.JWT.Secret), nil
	})
	if err != nil || !token.Valid {
		c.JSON(200, gin.H{"code": 0, "data": gin.H{"logged_in": false}})
		return
	}

	// 查询用户
	user, err := storeDB.GetUserByFeishuOpenID(userID)
	userName := ""
	if err == nil && user != nil {
		userName = user.Name
	}

	// 查询数据源（不返回敏感 Config）
	sources, _ := storeDB.GetDataSources(userID)
	var sourceList []gin.H
	for _, s := range sources {
		sourceList = append(sourceList, gin.H{
			"id":       s.ID,
			"source":   s.Type,
			"name":     s.Name,
			"enabled":  s.Enabled,
			"sync_status": s.SyncStatus,
			"last_sync_at": s.LastSyncAt,
		})
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"logged_in":    true,
			"user_id":      userID,
			"user_name":    userName,
			"data_sources": sourceList,
		},
	})
}

func larkLoginHandler(c *gin.Context) {
	redirectURI := cfg.Feishu.RedirectURI
	if redirectURI == "" {
		redirectURI = "http://localhost:8080/api/v1/auth/lark/callback"
	}
	url := fmt.Sprintf("https://open.feishu.cn/open-apis/authen/v1/index?app_id=%s&redirect_uri=%s",
		cfg.Feishu.AppID, url.QueryEscape(redirectURI))
	c.JSON(200, gin.H{"code": 0, "data": gin.H{"auth_url": url}})
}

func larkCallbackHandler(c *gin.Context) {
	code := c.Query("code")
	if code == "" {
		c.JSON(400, gin.H{"error": "缺少 code 参数"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userTokenInfo, err := larkClient.GetUserAccessToken(ctx, code)
	if err != nil {
		c.JSON(500, gin.H{"error": "获取 token 失败: " + err.Error()})
		return
	}

	// 保存或更新用户
	user := &model.User{
		FeishuOpenID: userTokenInfo.OpenID,
		Name:         userTokenInfo.Name,
	}
	if err := storeDB.CreateOrUpdateUser(user); err != nil {
		c.JSON(500, gin.H{"error": "保存用户失败: " + err.Error()})
		return
	}

	// JWT
	claims := jwt.MapClaims{
		"user_id": userTokenInfo.OpenID,
		"exp":     time.Now().Add(time.Hour * time.Duration(cfg.JWT.ExpireHours)).Unix(),
	}
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := jwtToken.SignedString([]byte(cfg.JWT.Secret))
	if err != nil {
		c.JSON(500, gin.H{"error": "生成 JWT 失败"})
		return
	}

	storeDB.SaveToken(userTokenInfo.OpenID, userTokenInfo.AccessToken, time.Duration(userTokenInfo.ExpiresIn)*time.Second)

	// 设置 cookie，SameSite=Lax 支持 ngrok -> localhost 回调
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("token", tokenString, 7200, "/", "", false, true)
	c.SetCookie("user_id", userTokenInfo.OpenID, 7200, "/", "", false, false)

	// 回调后重定向到前端测试页面
	c.Redirect(http.StatusFound, "/test.html")
}

func logoutHandler(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("user_id", "", -1, "/", "", false, false)
	c.SetCookie("token", "", -1, "/", "", false, true)
	c.JSON(200, gin.H{"code": 0, "message": "已退出登录"})
}

func collectHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")

	var req struct {
		WeekStart   string   `json:"week_start"`
		DataSources []string `json:"data_sources"`
		TemplateID  uint     `json:"template_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "请求格式错误"})
		return
	}
	if req.WeekStart == "" {
		c.JSON(400, gin.H{"error": "缺少 week_start 参数"})
		return
	}

	var allRecords []model.WorkRecord
	var sourceTypes []string

	// 如果没有指定数据源，默认使用 lark
	if len(req.DataSources) == 0 {
		req.DataSources = []string{"lark"}
	}

	for _, source := range req.DataSources {
		switch source {
		case "lark":
			accessToken, ok := storeDB.GetToken(userID)
			if !ok {
				c.JSON(500, gin.H{"error": "飞书 token 已过期，请重新登录"})
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			tasks, err := larkClient.FetchUserTasks(ctx, accessToken)
			if err != nil {
				c.JSON(500, gin.H{"error": "获取飞书任务失败: " + err.Error()})
				return
			}
			for _, t := range tasks {
				allRecords = append(allRecords, model.WorkRecord{
					UserID:      userID,
					SourceType:  "lark",
					RecordType:  model.TypeTask,
					Title:       t.Summary,
					Description: t.Notes,
					ExternalID:  t.GUID,
					WeekStart:   req.WeekStart,
					OccurredAt:  parseTimeOrNow(t.CompletedTime),
				})
			}
			sourceTypes = append(sourceTypes, "lark")
		default:
			// 其他数据源暂不支持实时采集
			sourceTypes = append(sourceTypes, source)
		}
	}

	storeDB.SaveWorkRecords(allRecords)

	// 获取模板
	var template *model.Template
	if req.TemplateID > 0 {
		t, _ := storeDB.GetTemplateByID(req.TemplateID)
		if t != nil {
			template = t
		}
	}
	if template == nil {
		user, _ := storeDB.GetUserByFeishuOpenID(userID)
		role := "member"
		if user != nil && user.Role != "" {
			role = user.Role
		}
		template, _ = storeDB.GetDefaultTemplate(role)
	}

	// 生成周报
	report := generateWeeklyReport(userID, req.WeekStart, allRecords, template)
	storeDB.SaveReport(report)

	// 记录审计日志
	storeDB.LogAudit(userID, "generate", "report", report.ID,
		fmt.Sprintf("生成了周报，数据源: %v，记录数: %d", sourceTypes, len(allRecords)), c.ClientIP())

	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"report":      report,
			"records":     allRecords,
			"source_types": sourceTypes,
			"auto_stats": gin.H{
				"task_count": len(allRecords),
				"sources":    sourceTypes,
			},
		},
	})
}

func browserCollectHandler(c *gin.Context) {
	var req struct {
		UserID    string `json:"user_id"`
		WeekStart string `json:"week_start"`
		Records   []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Source      string `json:"source"`
			URL         string `json:"url"`
		} `json:"records"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "请求格式错误"})
		return
	}
	if req.WeekStart == "" {
		c.JSON(400, gin.H{"error": "缺少 week_start 参数"})
		return
	}

	realUserID := storeDB.GetRealUserID(req.UserID)
	if realUserID == req.UserID {
		// 首次推送，尝试从 cookie 获取映射
		cookieUserID, _ := c.Cookie("user_id")
		if cookieUserID != "" {
			storeDB.SetUserMapping(req.UserID, cookieUserID)
			realUserID = cookieUserID
		}
	}

	var records []model.WorkRecord
	for _, r := range req.Records {
		records = append(records, model.WorkRecord{
			UserID:      realUserID,
			SourceType:  r.Source,
			RecordType:  model.TypeManual,
			Title:       r.Title,
			Description: r.Description,
			URL:         r.URL,
			WeekStart:   req.WeekStart,
			OccurredAt:  time.Now(),
		})
	}
	storeDB.SaveWorkRecords(records)

	report := generateWeeklyReport(realUserID, req.WeekStart, records, nil)
	storeDB.SaveReport(report)

	storeDB.LogAudit(realUserID, "collect", "report", report.ID,
		fmt.Sprintf("浏览器插件推送 %d 条记录", len(records)), c.ClientIP())

	c.JSON(200, gin.H{"code": 0, "data": report})
}

func getReportHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")
	report, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		c.JSON(404, gin.H{"error": "周报不存在"})
		return
	}

	records, _ := storeDB.GetWorkRecords(userID, weekStr)
	versions, _ := storeDB.GetReportVersions(report.ID)

	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"report":   report,
			"records":  records,
			"versions": versions,
		},
	})
}

func listReportsHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	reports := storeDB.ListReports(userID)
	c.JSON(200, gin.H{"code": 0, "data": reports})
}

func submitReportHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")

	report, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		c.JSON(404, gin.H{"error": "周报不存在"})
		return
	}

	now := time.Now()
	report.Status = "submitted"
	report.SubmittedAt = &now
	storeDB.SaveReport(report)

	// 保存版本快照
	version := &model.WeeklyReportVersion{
		ReportID: report.ID,
		Content:  report.Markdown,
		Version:  report.Version + 1,
	}
	storeDB.SaveReportVersion(version)
	report.Version++
	storeDB.SaveReport(report)

	storeDB.LogAudit(userID, "submit", "report", report.ID, "提交周报", c.ClientIP())

	c.JSON(200, gin.H{"code": 0, "message": "周报提交成功", "data": report})
}

func compareReportHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")

	current, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		c.JSON(404, gin.H{"error": "周报不存在"})
		return
	}

	// 查找上一周
	prevWeekStart := getPreviousWeekStart(weekStr)
	previous, _ := storeDB.GetReport(userID, prevWeekStart)

	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"current":  current,
			"previous": previous,
			"has_previous": previous != nil,
		},
	})
}

func listReportVersionsHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")

	report, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		c.JSON(404, gin.H{"error": "周报不存在"})
		return
	}

	versions, _ := storeDB.GetReportVersions(report.ID)
	c.JSON(200, gin.H{"code": 0, "data": versions})
}

func listCommentsHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")

	report, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		c.JSON(404, gin.H{"error": "周报不存在"})
		return
	}

	comments, _ := storeDB.GetComments(report.ID)
	c.JSON(200, gin.H{"code": 0, "data": comments})
}

func addCommentHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")

	report, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		c.JSON(404, gin.H{"error": "周报不存在"})
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "请求格式错误"})
		return
	}

	comment := &model.ReportComment{
		ReportID: report.ID,
		UserID:   userID,
		Content:  req.Content,
	}
	storeDB.CreateComment(comment)
	c.JSON(200, gin.H{"code": 0, "message": "批注添加成功"})
}

func deleteCommentHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	fmt.Sscanf(idStr, "%d", &id)
	storeDB.DeleteComment(userID, id)
	c.JSON(200, gin.H{"code": 0, "message": "批注删除成功"})
}

func exportHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")
	report, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		c.JSON(404, gin.H{"error": "周报不存在"})
		return
	}
	format := c.Query("format")
	if format == "" {
		format = "markdown"
	}
	switch format {
	case "markdown":
		c.Header("Content-Type", "text/markdown; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"周报_%s_%s.md\"", userID, weekStr))
		c.String(200, report.Markdown)
	case "word":
		c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"周报_%s_%s.docx\"", userID, weekStr))
		file := docx.New()
		lines := strings.Split(report.Markdown, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			para := file.AddParagraph()
			if strings.HasPrefix(line, "## ") {
				run := para.AddText(strings.TrimPrefix(line, "## "))
				run.Bold()
			} else if strings.HasPrefix(line, "### ") {
				run := para.AddText(strings.TrimPrefix(line, "### "))
				run.Bold()
			} else {
				para.AddText(line)
			}
		}
		if _, err := file.WriteTo(c.Writer); err != nil {
			c.JSON(500, gin.H{"error": "生成 Word 文件失败: " + err.Error()})
			return
		}
	case "pdf":
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"周报_%s_%s.html\"", userID, weekStr))
		generateHTMLPrintPage(report.Markdown, c.Writer)
	default:
		c.JSON(400, gin.H{"error": "不支持的导出格式"})
	}
}

func generateHTMLPrintPage(markdown string, w http.ResponseWriter) {
	lines := strings.Split(markdown, "\n")
	var body strings.Builder
	inList := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if inList {
				body.WriteString("</ul>\n")
				inList = false
			}
			body.WriteString("<br>\n")
			continue
		}
		if strings.HasPrefix(line, "## ") {
			if inList {
				body.WriteString("</ul>\n")
				inList = false
			}
			body.WriteString(fmt.Sprintf("<h2>%s</h2>\n", html.EscapeString(strings.TrimPrefix(line, "## "))))
		} else if strings.HasPrefix(line, "### ") {
			if inList {
				body.WriteString("</ul>\n")
				inList = false
			}
			body.WriteString(fmt.Sprintf("<h3>%s</h3>\n", html.EscapeString(strings.TrimPrefix(line, "### "))))
		} else if strings.HasPrefix(line, "- ") {
			if !inList {
				body.WriteString("<ul>\n")
				inList = true
			}
			body.WriteString(fmt.Sprintf("<li>%s</li>\n", html.EscapeString(strings.TrimPrefix(line, "- "))))
		} else {
			if inList {
				body.WriteString("</ul>\n")
				inList = false
			}
			body.WriteString(fmt.Sprintf("<p>%s</p>\n", html.EscapeString(line)))
		}
	}
	if inList {
		body.WriteString("</ul>\n")
	}

	htmlPage := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>周报导出</title>
<style>
@page { size: A4; margin: 2cm; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif; line-height: 1.6; color: #333; max-width: 800px; margin: 0 auto; padding: 40px; }
h2 { font-size: 20px; border-bottom: 2px solid #409eff; padding-bottom: 8px; margin-top: 24px; color: #1a1a1a; }
h3 { font-size: 16px; color: #333; margin-top: 16px; }
p, li { font-size: 14px; margin: 8px 0; }
li { list-style: disc; margin-left: 20px; }
ul { margin: 8px 0; padding-left: 20px; }
.print-btn { position: fixed; top: 20px; right: 20px; background: #409eff; color: white; border: none; padding: 10px 20px; border-radius: 6px; cursor: pointer; font-size: 14px; }
@media print { .print-btn { display: none; } body { padding: 0; } }
</style>
</head>
<body>
<button class="print-btn" onclick="window.print()">🖨️ 打印为 PDF</button>
%s
</body>
</html>`, body.String())
	w.Write([]byte(htmlPage))
}

// ------------------- 数据源管理 -------------------

func listDataSourcesHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	dss, err := storeDB.GetDataSources(userID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 不返回敏感 Config
	var result []gin.H
	for _, ds := range dss {
		result = append(result, gin.H{
			"id":           ds.ID,
			"type":         ds.Type,
			"name":         ds.Name,
			"enabled":      ds.Enabled,
			"sync_status":  ds.SyncStatus,
			"last_sync_at": ds.LastSyncAt,
			"created_at":   ds.CreatedAt,
		})
	}
	c.JSON(200, gin.H{"code": 0, "data": result})
}

func createDataSourceHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	var req struct {
		Type   string                 `json:"type"`
		Name   string                 `json:"name"`
		Config map[string]interface{} `json:"config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "请求格式错误"})
		return
	}
	configJSON, _ := json.Marshal(req.Config)
	ds := &model.DataSource{
		UserID: userID,
		Type:   req.Type,
		Name:   req.Name,
		Config: string(configJSON),
	}
	if err := storeDB.CreateDataSource(ds); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "数据源创建成功"})
}

func getDataSourceHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(400, gin.H{"error": "无效的数据源 ID"})
		return
	}
	ds, err := storeDB.GetDataSourceByID(id)
	if err != nil || ds == nil || ds.UserID != userID {
		c.JSON(404, gin.H{"error": "数据源不存在"})
		return
	}
	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"id":     ds.ID,
			"type":   ds.Type,
			"name":   ds.Name,
			"config": ds.Config,
			"enabled": ds.Enabled,
		},
	})
}

func updateDataSourceHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(400, gin.H{"error": "无效的数据源 ID"})
		return
	}
	old, err := storeDB.GetDataSourceByID(id)
	if err != nil || old == nil || old.UserID != userID {
		c.JSON(404, gin.H{"error": "数据源不存在"})
		return
	}
	var req struct {
		Type   string                 `json:"type"`
		Name   string                 `json:"name"`
		Config map[string]interface{} `json:"config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "请求格式错误"})
		return
	}
	configJSON, _ := json.Marshal(req.Config)
	old.Type = req.Type
	old.Name = req.Name
	old.Config = string(configJSON)
	if err := storeDB.UpdateDataSource(old); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "数据源更新成功"})
}

func deleteDataSourceHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(400, gin.H{"error": "无效的数据源 ID"})
		return
	}
	if err := storeDB.DeleteDataSource(userID, id); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "数据源删除成功"})
}

func testDataSourceHandler(c *gin.Context) {
	// 模拟连接测试
	c.JSON(200, gin.H{"code": 0, "message": "连接测试通过（模拟）"})
}

// ------------------- 模板管理 -------------------

func listTemplatesHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	role := "member"
	if u, err := storeDB.GetUserByFeishuOpenID(userID); err == nil && u != nil && u.Role != "" {
		role = u.Role
	}
	templates, err := storeDB.GetTemplates(role)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": templates})
}

func createTemplateHandler(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Content     string `json:"content"`
		Role        string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "请求格式错误"})
		return
	}
	template := &model.Template{
		Name:        req.Name,
		Description: req.Description,
		Content:     req.Content,
		Role:        req.Role,
		Scope:       "personal",
	}
	if err := storeDB.SaveTemplate(template); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "模板创建成功"})
}

func getTemplateHandler(c *gin.Context) {
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(400, gin.H{"error": "无效的模板 ID"})
		return
	}
	template, err := storeDB.GetTemplateByID(id)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": template})
}

func updateTemplateHandler(c *gin.Context) {
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(400, gin.H{"error": "无效的模板 ID"})
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Content     string `json:"content"`
		Role        string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "请求格式错误"})
		return
	}
	template, err := storeDB.GetTemplateByID(id)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	template.Name = req.Name
	template.Description = req.Description
	template.Content = req.Content
	template.Role = req.Role
	if err := storeDB.SaveTemplate(template); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "模板更新成功"})
}

func deleteTemplateHandler(c *gin.Context) {
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(400, gin.H{"error": "无效的模板 ID"})
		return
	}
	if err := storeDB.DeleteTemplate(id); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "模板删除成功"})
}

func testRemindHandler(c *gin.Context) {
	// 模拟发送提醒
	c.JSON(200, gin.H{"code": 0, "message": "提醒发送成功（模拟）"})
}

// ------------------- 辅助函数 -------------------

func generateWeeklyReport(userID, weekStart string, records []model.WorkRecord, tmpl *model.Template) *model.WeeklyReport {
	var content strings.Builder

	if tmpl != nil && tmpl.Content != "" {
		// 使用模板引擎（简化版）
		data := buildTemplateData(userID, weekStart, records)
		t := template.New("report")
		t, err := t.Parse(tmpl.Content)
		if err == nil {
			var buf bytes.Buffer
			t.Execute(&buf, data)
			content.WriteString(buf.String())
		} else {
			// 模板解析失败，回退到默认格式
			content.WriteString(fmt.Sprintf("## 周报 (%s ~ %s)\n\n", weekStart, weekStart))
			content.WriteString("### 本周工作\n\n")
			for _, r := range records {
				content.WriteString(fmt.Sprintf("- %s (%s)\n", r.Title, r.SourceType))
			}
		}
	} else {
		content.WriteString(fmt.Sprintf("## 周报 (%s ~ %s)\n\n", weekStart, weekStart))
		content.WriteString("### 本周工作\n\n")
		for _, r := range records {
			content.WriteString(fmt.Sprintf("- %s (%s)\n", r.Title, r.SourceType))
		}
		content.WriteString("\n### 下周计划\n\n")
		content.WriteString("- 待补充\n")
	}

	return &model.WeeklyReport{
		UserID:    userID,
		WeekStart: weekStart,
		WeekEnd:   weekStart,
		Markdown:  content.String(),
		Status:    "draft",
	}
}

func buildTemplateData(userID, weekStart string, records []model.WorkRecord) map[string]interface{} {
	var tasks, meetings, docs []map[string]string
	for _, r := range records {
		item := map[string]string{
			"Title":       r.Title,
			"Description": r.Description,
			"Source":      r.SourceType,
			"URL":         r.URL,
		}
		switch r.RecordType {
		case model.TypeMeeting:
			meetings = append(meetings, item)
		case model.TypeDoc:
			docs = append(docs, item)
		default:
			tasks = append(tasks, item)
		}
	}
	return map[string]interface{}{
		"WeekStart": weekStart,
		"WeekEnd":   weekStart,
		"Tasks":     tasks,
		"Meetings":  meetings,
		"Docs":      docs,
		"TaskCount": len(tasks),
	}
}

func parseTimeOrNow(timeStr string) time.Time {
	if timeStr == "" {
		return time.Now()
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05+08:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return t
		}
	}
	return time.Now()
}

func getPreviousWeekStart(weekStart string) string {
	t, err := time.Parse("2006-01-02", weekStart)
	if err != nil {
		return ""
	}
	prev := t.AddDate(0, 0, -7)
	return prev.Format("2006-01-02")
}
