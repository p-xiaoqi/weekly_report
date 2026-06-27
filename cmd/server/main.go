package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"weekly-report-system/internal/adapter/lark"
	"weekly-report-system/internal/application/collector"
	"weekly-report-system/internal/config"
	"weekly-report-system/internal/database"
	"weekly-report-system/internal/git"
	"weekly-report-system/internal/model"
	"weekly-report-system/internal/reminder"
	"weekly-report-system/internal/response"
	"weekly-report-system/internal/store"

	"github.com/fumiama/go-docx"
	"github.com/signintech/gopdf"
)

// cjkFontData 内嵌的中文 TTF 字体（SimHei），用于 PDF 导出时渲染中文。
// 若构建环境无中文字体可用，应替换为其他 CJK TTF；缺失时 PDF 仍可生成，
// 但中文字形可能无法正确显示。
//
//go:embed assets/fonts/SimHei.ttf
var cjkFontData []byte

var (
	cfg          *config.Config
	storeDB      *store.Store
	larkClient   *lark.Client
	collectorSvc *collector.Collector
	reminderSvc  *reminder.ReminderService

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
			response.Fail(c, http.StatusTooManyRequests, response.CodeTooManyRequests, "请求过于频繁，请稍后重试")
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
			response.FailUnauthorized(c, "未登录")
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
			response.Fail(c, http.StatusUnauthorized, response.CodeTokenExpired, "登录已过期，请重新登录")
			c.Abort()
			return
		}

		// 验证 token 中的 user_id 与 cookie 中的 user_id 是否一致
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			response.FailUnauthorized(c, "无效的登录凭证")
			c.Abort()
			return
		}
		tokenUserID, _ := claims["user_id"].(string)
		cookieUserID, _ := c.Cookie("user_id")
		if subtle.ConstantTimeCompare([]byte(tokenUserID), []byte(cookieUserID)) != 1 {
			response.FailUnauthorized(c, "登录凭证不匹配")
			c.Abort()
			return
		}

		c.Next()
	}
}

// adminMiddleware 在 authMiddleware 之后运行，校验当前登录用户是否为管理员。
// 身份取自已验证的 user_id Cookie（即飞书 OpenID），从数据库加载用户后判断：
//  1. 系统内 User.Role == "admin"，或
//  2. 该用户的 email / open_id 命中配置的管理员白名单（ADMIN_EMAILS / ADMIN_OPEN_IDS）。
//
// 注意：此处的"管理员"是本系统自有的授权概念，与"飞书群管理员"无关。
// 非管理员一律拒绝，返回 HTTP 403。
func adminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, _ := c.Cookie("user_id")
		email, role := "", ""
		if user, err := storeDB.GetUserByFeishuOpenID(userID); err == nil && user != nil {
			email = user.Email
			role = user.Role
		}
		if role == "admin" || cfg.IsAdmin(email, userID) {
			c.Next()
			return
		}
		// 把"当前检测到的身份"回显出来，方便用户比对：通常是配置的 open_id 与
		// 实际登录用户的 open_id 不一致，或 .env 未被加载 / 服务未重启。
		response.FailForbidden(c, fmt.Sprintf(
			"无权限：仅管理员可操作。当前登录 open_id=%q，email=%q；请确认该 open_id 已加入 ADMIN_OPEN_IDS（或 email 加入 ADMIN_EMAILS）且服务已重启。"+
				"已加载的白名单：ADMIN_OPEN_IDS=%q，ADMIN_EMAILS=%q（若此处为空说明 .env 未被读取）。飞书群管理员与本系统管理员无关。",
			userID, email, cfg.Admin.OpenIDs, cfg.Admin.Emails))
		c.Abort()
	}
}

func main() {
	var err error
	cfg, err = config.Load()
	if err != nil {
		panic(err)
	}

	// Token 加密密钥派生自 JWT Secret
	store.SetTokenKey(cfg.JWT.Secret)

	// 初始化数据库（WAL + 连接池）
	db, err := database.Init(cfg.Database.Path)
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
		&model.FeishuToken{},
	); err != nil {
		panic(fmt.Sprintf("migrate db failed: %v", err))
	}

	storeDB = store.New(db)
	storeDB.InitDefaultTemplates()
	larkClient = lark.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret)

	// 采集服务
	larkAdapter := lark.NewAdapter(larkClient)
	collectorSvc = collector.New(larkAdapter, storeDB)

	// 提醒服务
	reminderSvc = reminder.NewReminderService(storeDB)
	// 注入应用身份发送器：启用后用 App ID/Secret 经 im API 发消息（替代 webhook）。
	reminderSvc.SetAppSender(larkClient, cfg.Reminder.UseApp, cfg.Reminder.ChatID)
	if cfg.Reminder.Enabled {
		reminderSvc.Start(cfg.Reminder.Cron, cfg.Reminder.BotWebhook, cfg.Reminder.BotSecret)
	}

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

	// 需要登录的 API
	authorized := r.Group("/")
	authorized.Use(authMiddleware())
	{
		// 周报收集
		authorized.POST("/api/v1/collect", collectHandler)

		// 浏览器插件推送（身份来自已验证的会话）
		authorized.POST("/api/v1/collect/browser", browserCollectHandler)

		// 周报查询
		authorized.GET("/api/v1/reports/:week", getReportHandler)
		authorized.PUT("/api/v1/reports/:week", updateReportHandler)
		authorized.GET("/api/v1/reports", listReportsHandler)
		authorized.POST("/api/v1/reports/:week/submit", submitReportHandler)
		authorized.GET("/api/v1/reports/:week/compare", compareReportHandler)
		authorized.GET("/api/v1/reports/:week/versions", listReportVersionsHandler)

		// 统计
		authorized.GET("/api/v1/stats", statsHandler)

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

		// 飞书：获取当前授权用户所在的群列表（chat_id + 名称），用于选择通知目标群
		authorized.GET("/api/v1/feishu/chats", listChatsHandler)

		// 提醒测试（仅管理员可调用）
		admin := authorized.Group("/api/v1/admin")
		admin.Use(adminMiddleware())
		{
			admin.POST("/remind", testRemindHandler)
		}
	}

	// 前端页面
	r.NoRoute(func(c *gin.Context) {
		// API 路径未匹配，返回 JSON 404
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			response.FailNotFound(c, "接口不存在")
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
		// 与 authMiddleware 一致：断言签名算法为 HMAC，拒绝其它算法（如 none/RSA）。
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
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
			"id":           s.ID,
			"type":         s.Type,
			"name":         s.Name,
			"enabled":      s.Enabled,
			"sync_status":  s.SyncStatus,
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
		response.FailParam(c, "缺少 code 参数")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	userTokenInfo, err := larkClient.GetUserAccessToken(ctx, code)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeLoginFailed, "获取 token 失败: "+err.Error())
		return
	}

	// 保存或更新用户
	user := &model.User{
		FeishuOpenID: userTokenInfo.OpenID,
		Name:         userTokenInfo.Name,
		Email:        userTokenInfo.Email,
	}
	if err := storeDB.CreateOrUpdateUser(user); err != nil {
		response.FailInternal(c, "保存用户失败: "+err.Error())
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
		response.FailInternal(c, "生成 JWT 失败")
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
		WeekStart string `json:"week_start"`

		DataSources []string `json:"data_sources"`

		TemplateID uint `json:"template_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {

		response.FailParam(c, "请求格式错误")

		return

	}

	if req.WeekStart == "" {

		response.FailParam(c, "缺少 week_start 参数")

		return

	}

	weekStartT, err := time.Parse("2006-01-02", req.WeekStart)

	if err != nil {

		response.FailParam(c, "week_start 格式错误，应为 YYYY-MM-DD")

		return

	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	defer cancel()

	report, warnings, err := collectorSvc.Collect(ctx, model.CollectionRequest{

		UserID: userID, WeekStart: weekStartT, DataSources: req.DataSources, TemplateID: req.TemplateID,
	})

	if err != nil {

		response.Fail(c, http.StatusInternalServerError, response.CodeCollectFailed, "生成周报失败: "+err.Error())

		return

	}

	records, _ := storeDB.GetWorkRecords(userID, req.WeekStart)

	storeDB.LogAudit(userID, "generate", "report", report.ID,

		fmt.Sprintf("生成周报，数据源:%v，记录数:%d", req.DataSources, len(records)), c.ClientIP())

	c.JSON(200, gin.H{"code": 0, "data": gin.H{"report": report, "records": records,

		"auto_stats": gin.H{"task_count": len(records)}}, "warnings": warnings})

}

func browserCollectHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
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
		response.FailParam(c, "请求格式错误")
		return
	}
	if req.WeekStart == "" {
		response.FailParam(c, "缺少 week_start 参数")
		return
	}

	var records []model.WorkRecord
	for _, r := range req.Records {
		records = append(records, model.WorkRecord{
			UserID:      userID,
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

	// 复用采集主流程的模板渲染逻辑，保证插件推送与主链路格式一致
	weekEndStr := req.WeekStart
	if t, err := time.Parse("2006-01-02", req.WeekStart); err == nil {
		weekEndStr = t.AddDate(0, 0, 6).Format("2006-01-02")
	}
	report := &model.WeeklyReport{
		UserID:    userID,
		WeekStart: req.WeekStart,
		WeekEnd:   weekEndStr,
		Status:    "draft",
	}
	var templateContent string
	if t, err := storeDB.GetDefaultTemplate(""); err == nil && t != nil {
		templateContent = t.Content
		report.TemplateID = t.ID
	}
	report.Markdown = collector.RenderReport(report, records, nil, templateContent)
	storeDB.SaveReport(report)

	storeDB.LogAudit(userID, "collect", "report", report.ID,
		fmt.Sprintf("浏览器插件推送 %d 条记录", len(records)), c.ClientIP())

	c.JSON(200, gin.H{"code": 0, "data": report})
}

func getReportHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")
	report, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		response.Fail(c, http.StatusNotFound, response.CodeReportNotFound, "周报不存在")
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

// updateReportHandler 编辑周报草稿（owner-scoped）。
// 接受 JSON {markdown?, summary?, problems?, plans?, status?}，以 markdown 作为
// 草稿正文的权威来源；若提供了结构化字段则写入 Content(JSON)。
// 编辑成功后复用既有版本机制创建一次版本快照，并返回更新后的周报。
func updateReportHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")

	report, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		response.Fail(c, http.StatusNotFound, response.CodeReportNotFound, "周报不存在")
		return
	}

	var req struct {
		Markdown string `json:"markdown"`
		Summary  string `json:"summary"`
		Problems string `json:"problems"`
		Plans    string `json:"plans"`
		Status   string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailParam(c, "请求格式错误")
		return
	}

	// markdown 为草稿正文的权威来源
	if req.Markdown != "" {
		report.Markdown = req.Markdown
	}
	// 若提供任一结构化字段，则合并保存到 Content(JSON)
	if req.Summary != "" || req.Problems != "" || req.Plans != "" {
		structured := map[string]string{"work": req.Summary, "problems": req.Problems, "plans": req.Plans}
		if b, err := json.Marshal(structured); err == nil {
			report.Content = string(b)
		}
	}
	if req.Status != "" {
		report.Status = req.Status
	}
	report.UpdatedAt = time.Now()
	storeDB.SaveReport(report)

	// 复用既有版本机制，记录一次版本快照
	report.Version++
	storeDB.SaveReportVersion(&model.WeeklyReportVersion{
		ReportID: report.ID,
		Content:  report.Markdown,
		Version:  report.Version,
	})
	storeDB.SaveReport(report)

	storeDB.LogAudit(userID, "edit", "report", report.ID, "编辑周报草稿", c.ClientIP())

	response.OK(c, gin.H{"report": report})
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
		response.Fail(c, http.StatusNotFound, response.CodeReportNotFound, "周报不存在")
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

		response.Fail(c, http.StatusNotFound, response.CodeReportNotFound, "周报不存在")

		return

	}

	prevWeekStart := getPreviousWeekStart(weekStr)

	previous, _ := storeDB.GetReport(userID, prevWeekStart)

	curRecs, _ := storeDB.GetWorkRecords(userID, weekStr)

	prevRecs, _ := storeDB.GetWorkRecords(userID, prevWeekStart)

	countByType := func(recs []model.WorkRecord) gin.H {

		var task, meeting, doc, commit int

		for _, r := range recs {

			switch r.RecordType {

			case model.TypeMeeting:

				meeting++

			case model.TypeDoc:

				doc++

			case model.TypeCommit:

				commit++

			default:

				task++

			}

		}

		return gin.H{"task_count": task, "meeting_count": meeting, "doc_count": doc, "commit_count": commit, "total": len(recs)}

	}

	c.JSON(200, gin.H{"code": 0, "data": gin.H{

		"current": current, "previous": previous, "has_previous": previous != nil,

		"current_stats": countByType(curRecs), "previous_stats": countByType(prevRecs),
	}})

}

func statsHandler(c *gin.Context) {

	userID, _ := c.Cookie("user_id")

	stats, err := storeDB.GetWeeklyStats(userID, 8)

	if err != nil {

		response.FailInternal(c, err.Error())

		return

	}

	c.JSON(200, gin.H{"code": 0, "data": stats})

}

func listReportVersionsHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	weekStr := c.Param("week")

	report, ok := storeDB.GetReport(userID, weekStr)
	if !ok {
		response.Fail(c, http.StatusNotFound, response.CodeReportNotFound, "周报不存在")
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
		response.Fail(c, http.StatusNotFound, response.CodeReportNotFound, "周报不存在")
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
		response.Fail(c, http.StatusNotFound, response.CodeReportNotFound, "周报不存在")
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailParam(c, "请求格式错误")
		return
	}

	comment := &model.ReportComment{
		ReportID: report.ID,
		UserID:   userID,
		Content:  req.Content,
	}
	// 填充展示用的用户名，避免前端只能显示飞书 open_id
	if u, err := storeDB.GetUserByFeishuOpenID(userID); err == nil && u != nil {
		comment.UserName = u.Name
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
		response.Fail(c, http.StatusNotFound, response.CodeReportNotFound, "周报不存在")
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
			response.Fail(c, http.StatusInternalServerError, response.CodeExportFailed, "生成 Word 文件失败: "+err.Error())
			return
		}
	case "pdf":
		data, err := generatePDF(report.Markdown)
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, response.CodeExportFailed, "生成 PDF 失败: "+err.Error())
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"weekly-report-%s.pdf\"", weekStr))
		c.Data(200, "application/pdf", data)
	case "html":
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"周报_%s_%s.html\"", userID, weekStr))
		generateHTMLPrintPage(report.Markdown, c.Writer)
	default:
		response.FailParam(c, "不支持的导出格式")
	}
}

// generatePDF 使用纯 Go 的 gopdf 库将周报 Markdown 渲染为真实 PDF。
// 内嵌 SimHei TTF 以支持中文；对长行按字符宽度自动换行，并做轻量 Markdown 去标记。
// 注意：若内嵌字体缺失，将无法加载 CJK 字形，中文可能无法显示（见 cjkFontData 注释）。
func generatePDF(markdown string) ([]byte, error) {
	const (
		marginLeft = 40.0
		marginTop  = 40.0
		pageW      = 595.28 // A4 宽（pt）
		pageH      = 841.89 // A4 高（pt）
		lineHeight = 18.0
		fontSize   = 13.0
	)

	pdf := gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
	pdf.AddPage()
	if err := pdf.AddTTFFontData("cjk", cjkFontData); err != nil {
		return nil, err
	}
	if err := pdf.SetFont("cjk", "", fontSize); err != nil {
		return nil, err
	}

	maxWidth := pageW - 2*marginLeft
	y := marginTop
	for _, raw := range strings.Split(markdown, "\n") {
		line := stripMarkdownLine(raw)
		if line == "" {
			y += lineHeight / 2
			continue
		}
		for _, seg := range wrapPDFText(&pdf, line, maxWidth) {
			if y+lineHeight > pageH-marginTop {
				pdf.AddPage()
				y = marginTop
			}
			pdf.SetXY(marginLeft, y)
			if err := pdf.Cell(nil, seg); err != nil {
				return nil, err
			}
			y += lineHeight
		}
	}
	return pdf.GetBytesPdf(), nil
}

// stripMarkdownLine 去除常见 Markdown 行首标记，得到适合纯文本排版的内容。
func stripMarkdownLine(line string) string {
	line = strings.TrimSpace(line)
	for _, p := range []string{"### ", "## ", "# ", "- [x] ", "- [ ] ", "- ", "* "} {
		if strings.HasPrefix(line, p) {
			line = strings.TrimPrefix(line, p)
			break
		}
	}
	return line
}

// wrapPDFText 按测量宽度对文本做逐字符换行，确保不超出页面可用宽度。
func wrapPDFText(pdf *gopdf.GoPdf, text string, maxWidth float64) []string {
	var lines []string
	var cur []rune
	for _, r := range text {
		test := string(append(cur, r))
		if w, err := pdf.MeasureTextWidth(test); err == nil && w > maxWidth && len(cur) > 0 {
			lines = append(lines, string(cur))
			cur = []rune{r}
		} else {
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		lines = append(lines, string(cur))
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
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
	if userID == "" {
		response.FailUnauthorized(c, "未登录")
		return
	}
	dss, err := storeDB.GetDataSources(userID)
	if err != nil {
		response.FailInternal(c, err.Error())
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
	if userID == "" {
		response.FailUnauthorized(c, "未登录")
		return
	}
	var req struct {
		Type   string                 `json:"type"`
		Name   string                 `json:"name"`
		Config map[string]interface{} `json:"config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailParam(c, "请求格式错误")
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
		response.FailInternal(c, err.Error())
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "数据源创建成功"})
}

func getDataSourceHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		response.FailParam(c, "无效的数据源 ID")
		return
	}
	ds, err := storeDB.GetDataSourceByID(id)
	if err != nil || ds == nil || ds.UserID != userID {
		response.Fail(c, http.StatusNotFound, response.CodeDataSourceNotFound, "数据源不存在")
		return
	}
	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"id":      ds.ID,
			"type":    ds.Type,
			"name":    ds.Name,
			"config":  ds.Config,
			"enabled": ds.Enabled,
		},
	})
}

func updateDataSourceHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		response.FailParam(c, "无效的数据源 ID")
		return
	}
	old, err := storeDB.GetDataSourceByID(id)
	if err != nil || old == nil || old.UserID != userID {
		response.Fail(c, http.StatusNotFound, response.CodeDataSourceNotFound, "数据源不存在")
		return
	}
	var req struct {
		Type   string                 `json:"type"`
		Name   string                 `json:"name"`
		Config map[string]interface{} `json:"config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailParam(c, "请求格式错误")
		return
	}
	configJSON, _ := json.Marshal(req.Config)
	old.Type = req.Type
	old.Name = req.Name
	old.Config = string(configJSON)
	if err := storeDB.UpdateDataSource(old); err != nil {
		response.FailInternal(c, err.Error())
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "数据源更新成功"})
}

func deleteDataSourceHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		response.FailParam(c, "无效的数据源 ID")
		return
	}
	if err := storeDB.DeleteDataSource(userID, id); err != nil {
		response.FailInternal(c, err.Error())
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "数据源删除成功"})
}

// persistSyncStatus 在一次连通性测试/采集后更新数据源的同步状态，使前端状态标签反映真实结果。
func persistSyncStatus(ds *model.DataSource, ok bool, errMsg string) {
	now := time.Now()
	if ok {
		ds.SyncStatus = "success"
		ds.SyncError = ""
	} else {
		ds.SyncStatus = "failed"
		ds.SyncError = errMsg
	}
	ds.LastSyncAt = &now
	if err := storeDB.UpdateDataSource(ds); err != nil {
		fmt.Printf("[WARN] persist datasource sync status failed: %v\n", err)
	}
}

// commitPreview 是测试连接时返回给前端展示的单条提交摘要。
type commitPreview struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Repo    string `json:"repo"`
}

// firstLine 截取多行提交信息的首行，避免破坏前端展示。
func firstLine(s string) string {
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}

// gitHubCommitsPreview 把拉取到的 GitHub 提交转成最多 20 条的预览列表，供测试连接展示。
func gitHubCommitsPreview(commits []git.GitHubCommit, owner, repo string) []commitPreview {
	const max = 20
	out := make([]commitPreview, 0, len(commits))
	for i, cm := range commits {
		if i >= max {
			break
		}
		sha := cm.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		out = append(out, commitPreview{
			SHA:     sha,
			Message: firstLine(cm.Commit.Message),
			Author:  cm.Commit.Author.Name,
			Date:    cm.Commit.Author.Date,
			Repo:    fmt.Sprintf("%s/%s", owner, repo),
		})
	}
	return out
}

// gitLabCommitsPreview 把拉取到的 GitLab 提交转成最多 20 条的预览列表，供测试连接展示。
func gitLabCommitsPreview(commits []git.GitLabCommit, projectPath string) []commitPreview {
	const max = 20
	out := make([]commitPreview, 0, len(commits))
	for i, cm := range commits {
		if i >= max {
			break
		}
		sha := cm.ID
		if len(sha) > 7 {
			sha = sha[:7]
		}
		out = append(out, commitPreview{
			SHA:     sha,
			Message: firstLine(cm.Message),
			Author:  cm.AuthorName,
			Date:    cm.CreatedAt,
			Repo:    projectPath,
		})
	}
	return out
}

func testDataSourceHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		response.FailParam(c, "无效的数据源 ID")
		return
	}
	ds, err := storeDB.GetDataSourceByID(id)
	if err != nil || ds == nil || ds.UserID != userID {
		response.Fail(c, http.StatusNotFound, response.CodeDataSourceNotFound, "数据源不存在")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 使用最近 7 天的窗口做一次轻量级真实请求
	weekEnd := time.Now()
	weekStart := weekEnd.AddDate(0, 0, -7)

	switch ds.Type {
	case "github":
		var cfg struct {
			Token  string `json:"token"`
			Owner  string `json:"owner"`
			Repo   string `json:"repo"`
			Author string `json:"author"`
		}
		if err := json.Unmarshal([]byte(ds.Config), &cfg); err != nil {
			persistSyncStatus(ds, false, "配置解析失败: "+err.Error())
			c.JSON(200, gin.H{"code": response.CodeDataSourceTestFail, "success": false, "message": "配置解析失败: " + err.Error()})
			return
		}
		source := &git.GitHubSource{Token: cfg.Token, Owner: cfg.Owner, Repo: cfg.Repo}
		commits, err := source.FetchCommits(ctx, cfg.Author, weekStart, weekEnd)
		if err != nil {
			persistSyncStatus(ds, false, err.Error())
			c.JSON(200, gin.H{"code": response.CodeDataSourceTestFail, "success": false, "message": "GitHub 连接失败: " + err.Error()})
			return
		}
		persistSyncStatus(ds, true, "")
		c.JSON(200, gin.H{"code": response.CodeOK, "success": true,
			"message": fmt.Sprintf("GitHub 连接成功，最近 7 天获取到 %d 条提交", len(commits)),
			"records": gitHubCommitsPreview(commits, cfg.Owner, cfg.Repo)})
	case "gitlab":
		var cfg struct {
			Token       string `json:"token"`
			ServerURL   string `json:"server_url"`
			ProjectPath string `json:"project_path"`
			Email       string `json:"email"`
		}
		if err := json.Unmarshal([]byte(ds.Config), &cfg); err != nil {
			persistSyncStatus(ds, false, "配置解析失败: "+err.Error())
			c.JSON(200, gin.H{"code": response.CodeDataSourceTestFail, "success": false, "message": "配置解析失败: " + err.Error()})
			return
		}
		source := &git.GitLabSource{Token: cfg.Token, ServerURL: cfg.ServerURL, ProjectPath: cfg.ProjectPath}
		commits, err := source.FetchCommits(ctx, cfg.Email, weekStart, weekEnd)
		if err != nil {
			persistSyncStatus(ds, false, err.Error())
			c.JSON(200, gin.H{"code": response.CodeDataSourceTestFail, "success": false, "message": "GitLab 连接失败: " + err.Error()})
			return
		}
		persistSyncStatus(ds, true, "")
		c.JSON(200, gin.H{"code": response.CodeOK, "success": true,
			"message": fmt.Sprintf("GitLab 连接成功，最近 7 天获取到 %d 条提交", len(commits)),
			"records": gitLabCommitsPreview(commits, cfg.ProjectPath)})
	case "lark", "feishu":
		// 飞书数据源依赖用户 OAuth 授权,检查是否已授权
		if _, ok := storeDB.GetToken(userID); !ok {
			persistSyncStatus(ds, false, "飞书未授权")
			c.JSON(200, gin.H{"code": response.CodeDataSourceTestFail, "success": false,
				"message": "飞书未授权，请先完成飞书登录授权"})
			return
		}
		persistSyncStatus(ds, true, "")
		c.JSON(200, gin.H{"code": response.CodeOK, "success": true, "message": "飞书已授权，连接正常"})
	default:
		persistSyncStatus(ds, false, "不支持的类型: "+ds.Type)
		c.JSON(200, gin.H{"code": response.CodeDataSourceTestFail, "success": false,
			"message": "暂不支持测试该类型数据源: " + ds.Type})
	}
}

// ------------------- 模板管理 -------------------

func listTemplatesHandler(c *gin.Context) {
	// 模板管理列表与周报生成下拉框共用本接口，应返回该用户可用的全部模板
	// （global 全局/默认模板 + 该用户自己的 personal 个人模板），不返回他人的个人模板。
	// 不能用用户的“系统角色”（User.Role 取值 admin/member，真实登录默认 member）
	// 去过滤模板的“适用岗位角色”（Template.Role 取值 developer/tester/...），
	// 两者是不同的取值体系：否则用户新建的（岗位角色 developer 等）模板会被
	// 错误过滤掉，导致创建后既不出现在模板管理列表，也不出现在生成下拉框。
	userID, _ := c.Cookie("user_id")
	templates, err := storeDB.GetTemplates("", userID)
	if err != nil {
		response.FailInternal(c, err.Error())
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": templates})
}

func createTemplateHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Content     string `json:"content"`
		Role        string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailParam(c, "请求格式错误")
		return
	}
	template := &model.Template{
		UserID:      userID,
		Name:        req.Name,
		Description: req.Description,
		Content:     req.Content,
		Role:        req.Role,
		Scope:       "personal",
	}
	if err := storeDB.SaveTemplate(template); err != nil {
		response.FailInternal(c, err.Error())
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "模板创建成功"})
}

// templateVisible 判断模板对当前用户是否可见：全局/默认模板对所有人可见，
// 个人模板仅对其归属用户可见。
func templateVisible(t *model.Template, userID string) bool {
	if t.Scope == "global" || t.IsDefault {
		return true
	}
	return t.UserID == userID
}

func getTemplateHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		response.FailParam(c, "无效的模板 ID")
		return
	}
	template, err := storeDB.GetTemplateByID(id)
	if err != nil || !templateVisible(template, userID) {
		response.Fail(c, http.StatusNotFound, response.CodeTemplateNotFound, "模板不存在")
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": template})
}

func updateTemplateHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		response.FailParam(c, "无效的模板 ID")
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Content     string `json:"content"`
		Role        string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailParam(c, "请求格式错误")
		return
	}
	template, err := storeDB.GetTemplateByID(id)
	if err != nil {
		response.Fail(c, http.StatusNotFound, response.CodeTemplateNotFound, "模板不存在")
		return
	}
	// 全局/默认模板不允许修改
	if template.Scope == "global" || template.IsDefault {
		response.FailForbidden(c, "无权限：全局/默认模板不可修改")
		return
	}
	// 仅允许修改自己的个人模板，他人模板视为不存在
	if template.UserID != userID {
		response.Fail(c, http.StatusNotFound, response.CodeTemplateNotFound, "模板不存在")
		return
	}
	template.Name = req.Name
	template.Description = req.Description
	template.Content = req.Content
	template.Role = req.Role
	if err := storeDB.SaveTemplate(template); err != nil {
		response.FailInternal(c, err.Error())
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "模板更新成功"})
}

func deleteTemplateHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	idStr := c.Param("id")
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		response.FailParam(c, "无效的模板 ID")
		return
	}
	template, err := storeDB.GetTemplateByID(id)
	if err != nil {
		response.Fail(c, http.StatusNotFound, response.CodeTemplateNotFound, "模板不存在")
		return
	}
	// 全局/默认模板不允许删除
	if template.Scope == "global" || template.IsDefault {
		response.FailForbidden(c, "无权限：全局/默认模板不可删除")
		return
	}
	// 仅允许删除自己的个人模板，他人模板视为不存在
	if template.UserID != userID {
		response.Fail(c, http.StatusNotFound, response.CodeTemplateNotFound, "模板不存在")
		return
	}
	if err := storeDB.DeleteTemplate(id); err != nil {
		response.FailInternal(c, err.Error())
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "模板删除成功"})
}

// listChatsHandler 返回当前授权用户所在的飞书群列表（chat_id + 名称），
// 便于用户挑选要接收周报提醒的群（可多选，配置到 REMINDER_CHAT_ID，逗号分隔）。
func listChatsHandler(c *gin.Context) {
	userID, _ := c.Cookie("user_id")
	token, ok := storeDB.GetToken(userID)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, response.CodeTokenExpired, "飞书授权已过期，请重新登录后再获取群列表")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	chats, err := larkClient.ListUserChats(ctx, token)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternalError, err.Error())
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": chats})
}

func testRemindHandler(c *gin.Context) {
	if reminderSvc == nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeRemindFailed, "提醒服务未初始化")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 可选请求体：勾选要发送的群 chat_ids。提供时按群逐个发送并返回每个群的结果。
	var body struct {
		ChatIDs []string `json:"chat_ids"`
	}
	_ = c.ShouldBindJSON(&body)

	if len(body.ChatIDs) > 0 {
		results, err := reminderSvc.SendToChats(ctx, body.ChatIDs, "")
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, response.CodeRemindFailed, err.Error())
			return
		}
		ok, fail := 0, 0
		for _, r := range results {
			if r.Success {
				ok++
			} else {
				fail++
			}
		}
		c.JSON(200, gin.H{
			"code":    0,
			"message": fmt.Sprintf("发送完成：成功 %d，失败 %d", ok, fail),
			"data":    results,
		})
		return
	}

	// 未勾选群时：应用身份模式下私信当前登录管理员本人，否则回退 webhook。
	toOpenID, _ := c.Cookie("user_id")
	if err := reminderSvc.SendTest(ctx, toOpenID, cfg.Reminder.BotWebhook, cfg.Reminder.BotSecret, ""); err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeRemindFailed, err.Error())
		return
	}
	c.JSON(200, gin.H{"code": 0, "message": "提醒发送成功"})
}

// ------------------- 辅助函数 -------------------

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
