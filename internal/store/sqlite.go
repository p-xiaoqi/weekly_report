package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"gorm.io/gorm"

	"weekly-report-system/internal/model"
)

// DataSourceAuth 保留兼容现有接口
type DataSourceAuth struct {
	Source   string    `json:"source"`
	BoundAt  time.Time `json:"bound_at"`
	UserID   string    `json:"user_id"`
	UserName string    `json:"user_name"`
}

// Store 基于 SQLite 的存储实现，保留原 memory.go 的对外接口
type Store struct {
	db       *gorm.DB
	memCache *cache.Cache

	muUserMap sync.RWMutex
	userMap   map[string]string // browser_user_id -> real_user_id
}

func New(db *gorm.DB) *Store {
	s := &Store{
		db:       db,
		memCache: cache.New(5*time.Minute, 10*time.Minute),
		userMap:  make(map[string]string),
	}
	return s
}

func (s *Store) Stop() {
	// SQLite 无需特殊清理
}

// --- 周报相关 ---

func (s *Store) SaveReport(report *model.WeeklyReport) {
	if err := s.db.Save(report).Error; err != nil {
		log.Printf("[ERROR] SaveReport failed: %v", err)
	}
}

func (s *Store) UpdateReport(userID, weekStart string, updater func(*model.WeeklyReport)) bool {
	var report model.WeeklyReport
	if err := s.db.Where("user_id = ? AND week_start = ?", userID, weekStart).First(&report).Error; err != nil {
		return false
	}
	updater(&report)
	if err := s.db.Save(&report).Error; err != nil {
		log.Printf("[ERROR] UpdateReport failed: %v", err)
		return false
	}
	return true
}

func (s *Store) GetReport(userID, weekStart string) (*model.WeeklyReport, bool) {
	var report model.WeeklyReport
	if err := s.db.Where("user_id = ? AND week_start = ?", userID, weekStart).First(&report).Error; err != nil {
		return nil, false
	}
	return &report, true
}

// UpdateReportMarkdown 更新指定用户某周周报的草稿正文及结构化字段，
// 仅更新传入的非空字段，并返回更新后的周报。报告不存在时返回 (nil,false)。
func (s *Store) UpdateReportMarkdown(userID, weekStart, markdown, content, status string) (*model.WeeklyReport, bool) {
	var report model.WeeklyReport
	if err := s.db.Where("user_id = ? AND week_start = ?", userID, weekStart).First(&report).Error; err != nil {
		return nil, false
	}
	if markdown != "" {
		report.Markdown = markdown
	}
	if content != "" {
		report.Content = content
	}
	if status != "" {
		report.Status = status
	}
	if err := s.db.Save(&report).Error; err != nil {
		log.Printf("[ERROR] UpdateReportMarkdown failed: %v", err)
		return nil, false
	}
	return &report, true
}

func (s *Store) ListReports(userID string) []*model.WeeklyReport {
	var reports []model.WeeklyReport
	s.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&reports)
	result := make([]*model.WeeklyReport, len(reports))
	for i := range reports {
		result[i] = &reports[i]
	}
	return result
}

// --- Token 相关 ---

// tokenKey 为 AES-256-GCM 加解密密钥，必须通过 SetTokenKey 由配置（JWT Secret）派生后才可使用。
// 不再提供任何硬编码默认值，未设置时加解密直接返回错误。
var (
	tokenKey    [32]byte
	tokenKeySet bool
)

// SetTokenKey 设置 Token 加密密钥（应在启动时通过 JWT Secret 派生）。
// 入参经 sha256 归一化为固定 32 字节，因此任意长度的 Secret 均可使用。
func SetTokenKey(s string) {
	tokenKey = sha256.Sum256([]byte(s))
	tokenKeySet = true
}

func encryptToken(plain string) (string, error) {
	if !tokenKeySet {
		return "", fmt.Errorf("token encryption key not set: call store.SetTokenKey first")
	}
	block, err := aes.NewCipher(tokenKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decryptToken(enc string) (string, error) {
	if !tokenKeySet {
		return "", fmt.Errorf("token encryption key not set: call store.SetTokenKey first")
	}
	data, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(tokenKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Store) SaveToken(userID, token string, expiresIn time.Duration) {
	encrypted, err := encryptToken(token)
	if err != nil {
		log.Printf("[ERROR] SaveToken encrypt failed: %v", err)
		return
	}
	expiresAt := time.Now().Add(expiresIn)

	var ft model.FeishuToken
	if err := s.db.Where("user_id = ?", userID).First(&ft).Error; err == nil {
		ft.Token = encrypted
		ft.ExpiresAt = expiresAt
		s.db.Save(&ft)
	} else {
		s.db.Create(&model.FeishuToken{
			UserID:    userID,
			Token:     encrypted,
			ExpiresAt: expiresAt,
		})
	}
}

func (s *Store) GetToken(userID string) (string, bool) {
	var ft model.FeishuToken
	if err := s.db.Where("user_id = ?", userID).First(&ft).Error; err != nil {
		return "", false
	}
	if time.Now().After(ft.ExpiresAt) {
		return "", false
	}
	token, err := decryptToken(ft.Token)
	if err != nil {
		return "", false
	}
	return token, true
}

// --- 用户映射（浏览器插件兼容） ---

func (s *Store) SetUserMapping(browserUserID, realUserID string) {
	s.muUserMap.Lock()
	defer s.muUserMap.Unlock()
	s.userMap[browserUserID] = realUserID
}

func (s *Store) GetRealUserID(browserUserID string) string {
	s.muUserMap.RLock()
	defer s.muUserMap.RUnlock()
	if realID, ok := s.userMap[browserUserID]; ok {
		return realID
	}
	return browserUserID
}

// --- 内存缓存（go-cache） ---

func (s *Store) SetCache(key string, value interface{}, ttl time.Duration) {
	s.memCache.Set(key, value, ttl)
}

func (s *Store) GetCache(key string) (interface{}, bool) {
	return s.memCache.Get(key)
}

func (s *Store) DeleteCache(key string) {
	s.memCache.Delete(key)
}

// --- 数据源授权（兼容现有接口） ---

func (s *Store) BindDataSource(userID string, auth DataSourceAuth) bool {
	var count int64
	s.db.Model(&model.DataSource{}).Where("user_id = ? AND type = ?", userID, auth.Source).Count(&count)
	if count > 0 {
		return false
	}

	config, _ := json.Marshal(map[string]string{
		"user_id":   auth.UserID,
		"user_name": auth.UserName,
	})

	if err := s.db.Create(&model.DataSource{
		UserID: auth.UserID,
		Type:   auth.Source,
		Name:   auth.Source,
		Config: string(config),
	}).Error; err != nil {
		log.Printf("[ERROR] BindDataSource failed: %v", err)
		return false
	}
	return true
}

func (s *Store) UnbindDataSource(userID, source string) bool {
	result := s.db.Where("user_id = ? AND type = ?", userID, source).Delete(&model.DataSource{})
	if result.Error != nil {
		log.Printf("[ERROR] UnbindDataSource failed: %v", result.Error)
		return false
	}
	return result.RowsAffected > 0
}

func (s *Store) GetDataSourceAuths(userID string) []DataSourceAuth {
	var sources []model.DataSource
	s.db.Where("user_id = ?", userID).Find(&sources)

	auths := make([]DataSourceAuth, 0, len(sources))
	for _, src := range sources {
		var cfg map[string]string
		json.Unmarshal([]byte(src.Config), &cfg)
		auths = append(auths, DataSourceAuth{
			Source:   src.Type,
			BoundAt:  src.CreatedAt,
			UserID:   cfg["user_id"],
			UserName: cfg["user_name"],
		})
	}
	return auths
}

func (s *Store) HasDataSource(userID, source string) bool {
	var count int64
	s.db.Model(&model.DataSource{}).Where("user_id = ? AND type = ?", userID, source).Count(&count)
	return count > 0
}

// --- 新增：用户管理 ---

func (s *Store) CreateOrUpdateUser(user *model.User) error {
	var existing model.User
	if err := s.db.Where("feishu_open_id = ?", user.FeishuOpenID).First(&existing).Error; err == nil {
		existing.Name = user.Name
		existing.Email = user.Email
		existing.Avatar = user.Avatar
		return s.db.Save(&existing).Error
	}
	return s.db.Create(user).Error
}

func (s *Store) GetUserByFeishuOpenID(openID string) (*model.User, error) {
	var user model.User
	if err := s.db.Where("feishu_open_id = ?", openID).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Store) GetUserByID(id uint) (*model.User, error) {
	var user model.User
	if err := s.db.First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// CountAdmins 统计系统内 Role=admin 的用户数量（用于"首位用户引导为管理员"）。
func (s *Store) CountAdmins() (int64, error) {
	var n int64
	err := s.db.Model(&model.User{}).Where("role = ?", "admin").Count(&n).Error
	return n, err
}

// SetUserRole 设置指定飞书 open_id 用户的角色。
func (s *Store) SetUserRole(openID, role string) error {
	return s.db.Model(&model.User{}).Where("feishu_open_id = ?", openID).Update("role", role).Error
}

// --- 新增：数据源配置管理 ---

func (s *Store) CreateDataSource(ds *model.DataSource) error {
	return s.db.Create(ds).Error
}

func (s *Store) GetDataSources(userID string) ([]model.DataSource, error) {
	var sources []model.DataSource
	if err := s.db.Where("user_id = ?", userID).Find(&sources).Error; err != nil {
		return nil, err
	}
	return sources, nil
}

func (s *Store) GetDataSourceByID(id uint) (*model.DataSource, error) {
	var ds model.DataSource
	if err := s.db.First(&ds, id).Error; err != nil {
		return nil, err
	}
	return &ds, nil
}

func (s *Store) UpdateDataSource(ds *model.DataSource) error {
	return s.db.Save(ds).Error
}

func (s *Store) DeleteDataSource(userID string, id uint) error {
	return s.db.Where("user_id = ? AND id = ?", userID, id).Delete(&model.DataSource{}).Error
}

// --- 新增：工作记录 ---

func (s *Store) SaveWorkRecords(records []model.WorkRecord) error {
	if len(records) == 0 {
		return nil
	}
	for i := range records {
		// 仅当存在稳定的 ExternalID 时才做去重 upsert（lark 任务/日历/文档、git 提交等）。
		// 手动录入 / 浏览器插件推送的记录没有 ExternalID，若按空 external_id 去重，
		// 同一 (user, source) 下的多条记录会互相覆盖，导致只剩最后一条（数据丢失）。
		if records[i].ExternalID == "" {
			s.db.Create(&records[i])
			continue
		}
		var existing model.WorkRecord
		err := s.db.Where("user_id = ? AND source_type = ? AND external_id = ?", records[i].UserID, records[i].SourceType, records[i].ExternalID).First(&existing).Error
		if err == nil {
			records[i].ID = existing.ID
			s.db.Save(&records[i])
		} else {
			s.db.Create(&records[i])
		}
	}
	return nil
}

func (s *Store) GetWorkRecords(userID, weekStart string) ([]model.WorkRecord, error) {
	var records []model.WorkRecord
	if err := s.db.Where("user_id = ? AND week_start = ? AND is_hidden = ?", userID, weekStart, false).Order("occurred_at DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) HideWorkRecord(userID string, id uint, hidden bool) error {
	return s.db.Model(&model.WorkRecord{}).Where("user_id = ? AND id = ?", userID, id).Update("is_hidden", hidden).Error
}

// --- 新增：周报版本快照 ---

func (s *Store) SaveReportVersion(rv *model.WeeklyReportVersion) error {
	return s.db.Create(rv).Error
}

func (s *Store) GetReportVersions(reportID string) ([]model.WeeklyReportVersion, error) {
	var versions []model.WeeklyReportVersion
	if err := s.db.Where("report_id = ?", reportID).Order("version DESC").Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// --- 新增：模板 ---

// GetTemplates 返回当前用户可见的模板：全局/默认模板（scope=global 或 is_default）
// 加上该用户自己的个人模板（user_id = userID）。不返回其他用户的个人模板。
func (s *Store) GetTemplates(role, userID string) ([]model.Template, error) {
	var templates []model.Template
	q := s.db.Where("scope = ? OR is_default = ? OR user_id = ?", "global", true, userID)
	if role != "" {
		q = q.Where("role = ? OR role = ?", role, "")
	}
	if err := q.Find(&templates).Error; err != nil {
		return nil, err
	}
	return templates, nil
}

func (s *Store) GetTemplateByID(id uint) (*model.Template, error) {
	var t model.Template
	if err := s.db.First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) GetDefaultTemplate(role string) (*model.Template, error) {
	var t model.Template
	err := s.db.Where("(role = ? OR role = ?) AND is_default = ?", role, "", true).First(&t).Error
	if err != nil {
		// 兜底：返回第一个通用模板
		err = s.db.Where("role = ?", "").First(&t).Error
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) SaveTemplate(t *model.Template) error {
	return s.db.Save(t).Error
}

func (s *Store) DeleteTemplate(id uint) error {
	return s.db.Delete(&model.Template{}, id).Error
}

// --- 新增：审计日志 ---

func (s *Store) LogAudit(userID, action, targetType, targetID, details, ip string) {
	if err := s.db.Create(&model.AuditLog{
		UserID:     userID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Details:    details,
		IPAddress:  ip,
	}).Error; err != nil {
		log.Printf("[ERROR] LogAudit failed: %v", err)
	}
}

// --- 新增：CronJob 状态 ---

func (s *Store) GetCronJob(name string) (*model.CronJob, error) {
	var job model.CronJob
	if err := s.db.Where("name = ?", name).First(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Store) SaveCronJob(job *model.CronJob) error {
	var existing model.CronJob
	if err := s.db.Where("name = ?", job.Name).First(&existing).Error; err == nil {
		job.ID = existing.ID
		return s.db.Save(job).Error
	}
	return s.db.Create(job).Error
}

// --- 新增：统计查询 ---

func (s *Store) GetWeeklyStats(userID string, weeks int) ([]map[string]interface{}, error) {
	var results []map[string]interface{}
	// manual 记录与 task 合并统计，与 compareReportHandler 的归类规则保持一致（manual 计入任务桶）。
	sql := fmt.Sprintf(`SELECT week_start,
		SUM(CASE WHEN record_type = 'commit' THEN 1 ELSE 0 END) as commit_count,
		SUM(CASE WHEN record_type = 'task' OR record_type = 'manual' THEN 1 ELSE 0 END) as task_count,
		SUM(CASE WHEN record_type = 'meeting' THEN 1 ELSE 0 END) as meeting_count
		FROM work_records
		WHERE user_id = ? AND is_hidden = 0
		GROUP BY week_start
		ORDER BY week_start DESC
		LIMIT %d`, weeks)
	rows, err := s.db.Raw(sql, userID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var weekStart string
		var commitCount, taskCount, meetingCount int
		rows.Scan(&weekStart, &commitCount, &taskCount, &meetingCount)
		results = append(results, map[string]interface{}{
			"week":          weekStart,
			"commit_count":  commitCount,
			"task_count":    taskCount,
			"meeting_count": meetingCount,
		})
	}
	return results, nil
}

// --- 新增：批注/评论 ---

func (s *Store) CreateComment(c *model.ReportComment) error {
	return s.db.Create(c).Error
}

func (s *Store) GetComments(reportID string) ([]model.ReportComment, error) {
	var comments []model.ReportComment
	if err := s.db.Where("report_id = ?", reportID).Order("created_at ASC").Find(&comments).Error; err != nil {
		return nil, err
	}
	return comments, nil
}

func (s *Store) DeleteComment(userID string, id uint) error {
	return s.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.ReportComment{}).Error
}

// --- 新增：周报提交相关 ---

// ListSubmittedUsers 返回某周已提交周报的用户ID列表
func (s *Store) ListSubmittedUsers(weekStart string) ([]string, error) {
	var userIDs []string
	if err := s.db.Model(&model.WeeklyReport{}).
		Where("week_start = ? AND status = ?", weekStart, "submitted").
		Pluck("user_id", &userIDs).Error; err != nil {
		return nil, err
	}
	return userIDs, nil
}

// ListAllUsers 返回所有已注册用户信息
func (s *Store) ListAllUsers() ([]model.User, error) {
	var users []model.User
	if err := s.db.Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// --- 辅助：WeekStart 格式化 ---

func FormatWeekStart(t time.Time) string {
	return t.Format("2006-01-02")
}

// --- 事务支持 ---

func (s *Store) Transaction(fn func(tx *gorm.DB) error) error {
	return s.db.Transaction(fn)
}

// --- 初始化默认模板 ---

func (s *Store) InitDefaultTemplates() error {
	defaults := []model.Template{
		{
			Name:        "研发工程师默认模板",
			Description: "适用于研发工程师的周报模板",
			Role:        "developer",
			Scope:       "global",
			IsDefault:   true,
			Content: `## 本周完成工作 ({{.WeekRange}})

{{if .HasTasks}}### 任务进展

{{range .Tasks}}- [x] {{.Title}}
{{if .Description}}  - {{.Description}}
{{end}}{{end}}
{{end}}
{{if .HasCommits}}### 代码提交

{{range .Commits}}- 💻 {{.Title}}{{if .ProjectName}} @{{.ProjectName}}{{end}}{{if .OccurredDate}}（{{.OccurredDate}}）{{end}}
{{end}}
{{end}}
{{if .HasMeetings}}### 会议/日程

{{range .Meetings}}- 🗓️ {{.Title}} ({{.OccurredDate}})
{{if .Description}}  - {{.Description}}
{{end}}{{if .ProjectName}}  - 📍 地点：{{.ProjectName}}
{{end}}{{end}}
{{end}}
{{if .HasDocs}}### 文档编辑

{{range .Docs}}- 📝 {{.Title}} ({{.OccurredDate}})
{{end}}
{{end}}
{{if and (not .HasTasks) (not .HasCommits) (not .HasMeetings) (not .HasDocs)}}- 本周暂无记录
{{end}}
## 遇到的问题

{{if .HasProblems}}{{range .Problems}}- {{.}}
{{end}}{{else}}- （本周暂无自动识别到的问题，请补充）
{{end}}
## 下周计划

{{if .HasNextWeek}}{{range .NextWeekEvents}}- [ ] {{.Title}} ({{.OccurredDate}})
{{if .ProjectName}}  - 📍 地点：{{.ProjectName}}
{{end}}{{end}}{{else}}- 待补充
{{end}}`,
		},
		{
			Name:        "通用默认模板",
			Description: "适用于所有角色的通用周报模板",
			Role:        "",
			Scope:       "global",
			IsDefault:   true,
			Content: `## 本周完成工作 ({{.WeekRange}})

{{if .HasTasks}}### 任务进展

{{range .Tasks}}- [x] {{.Title}}
{{if .Description}}  - {{.Description}}
{{end}}{{end}}
{{end}}
{{if .HasCommits}}### 代码提交

{{range .Commits}}- 💻 {{.Title}}{{if .ProjectName}} @{{.ProjectName}}{{end}}{{if .OccurredDate}}（{{.OccurredDate}}）{{end}}
{{end}}
{{end}}
{{if .HasMeetings}}### 会议/日程

{{range .Meetings}}- {{.Title}} ({{.OccurredDate}})
{{end}}
{{end}}
{{if .HasDocs}}### 文档编辑

{{range .Docs}}- {{.Title}} ({{.OccurredDate}})
{{end}}
{{end}}
{{if and (not .HasTasks) (not .HasCommits) (not .HasMeetings) (not .HasDocs)}}- 本周暂无记录
{{end}}
## 遇到的问题

{{if .HasProblems}}{{range .Problems}}- {{.}}
{{end}}{{else}}- （本周暂无自动识别到的问题，请补充）
{{end}}
## 下周计划

- 待补充`,
		},
	}

	// 按名称 upsert 全局默认模板：不存在则创建，已存在则刷新内容/描述，
	// 这样升级（如新增“代码提交”分类）能落到已存在的数据库，而不会跳过初始化；
	// 全局默认模板不归属任何用户、用户也不可编辑，刷新是安全的。
	for i := range defaults {
		d := defaults[i]
		var existing model.Template
		err := s.db.Where("name = ? AND scope = ?", d.Name, "global").First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			if cerr := s.db.Create(&d).Error; cerr != nil {
				log.Printf("[ERROR] InitDefaultTemplates create %q failed: %v", d.Name, cerr)
			}
			continue
		}
		if err != nil {
			log.Printf("[ERROR] InitDefaultTemplates lookup %q failed: %v", d.Name, err)
			continue
		}
		existing.Description = d.Description
		existing.Role = d.Role
		existing.IsDefault = d.IsDefault
		existing.Content = d.Content
		if uerr := s.db.Save(&existing).Error; uerr != nil {
			log.Printf("[ERROR] InitDefaultTemplates update %q failed: %v", d.Name, uerr)
		}
	}
	return nil
}

// --- 兼容：旧接口 GetReport 的 key 格式转换 ---

func ParseWeekKey(key string) (userID, weekStart string) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return key, ""
}

func (s *Store) GetReportByKey(key string) (*model.WeeklyReport, bool) {
	userID, weekStart := ParseWeekKey(key)
	if weekStart == "" {
		return nil, false
	}
	return s.GetReport(userID, weekStart)
}

func (s *Store) UpdateReportByKey(key string, updater func(*model.WeeklyReport)) bool {
	userID, weekStart := ParseWeekKey(key)
	if weekStart == "" {
		return false
	}
	return s.UpdateReport(userID, weekStart, updater)
}
