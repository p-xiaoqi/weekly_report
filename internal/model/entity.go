package model

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// RecordType 工作记录类型
type RecordType string

const (
	TypeTask    RecordType = "task"
	TypeMeeting RecordType = "meeting"
	TypeDoc     RecordType = "doc"
	TypeCommit  RecordType = "commit"
	TypeManual  RecordType = "manual"
)

// TemplateItem 模板中的单条工作记录

type TemplateItem struct {
	Title        string
	Description  string
	ProjectName  string
	URL          string
	OccurredAt   time.Time
	OccurredDate string // 格式化后的日期
}

// ReportTemplateData 传递给模板引擎的数据结构
type ReportTemplateData struct {
	WeekStart      string
	WeekEnd        string
	WeekRange      string
	Tasks          []TemplateItem
	Commits        []TemplateItem
	Meetings       []TemplateItem
	Docs           []TemplateItem
	NextWeekEvents []TemplateItem
	Problems       []string // 自动识别到的问题/阻塞项
	TaskCount      int
	CommitCount    int
	MeetingCount   int
	DocCount       int
	NextWeekCount  int
	HasTasks       bool
	HasCommits     bool
	HasMeetings    bool
	HasDocs        bool
	HasNextWeek    bool
	HasProblems    bool
}

// User 用户表（飞书登录后自动创建）
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	FeishuOpenID string    `gorm:"uniqueIndex;not null" json:"feishu_open_id"`
	Name         string    `gorm:"not null" json:"name"`
	Email        string    `json:"email"`
	Avatar       string    `json:"avatar"`
	Role         string    `gorm:"default:'member'" json:"role"` // admin / member
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DataSource 数据源配置表
type DataSource struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	UserID     string     `gorm:"index;not null" json:"user_id"`
	Type       string     `gorm:"not null" json:"type"` // lark, gitlab, github, manual
	Name       string     `gorm:"not null" json:"name"`
	Config     string     `gorm:"type:text" json:"config"` // JSON 存储 Token/URL 等
	Enabled    bool       `gorm:"default:true" json:"enabled"`
	LastSyncAt *time.Time `json:"last_sync_at"`
	SyncStatus string     `gorm:"default:'pending'" json:"sync_status"` // pending / success / failed
	SyncError  string     `json:"sync_error"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// WorkRecord 工作记录表（从各数据源同步的原始数据）
type WorkRecord struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	UserID      string     `gorm:"index;not null" json:"user_id"`
	ReportID    string     `gorm:"index" json:"report_id"`      // 关联周报
	SourceType  string     `gorm:"not null" json:"source_type"` // lark, gitlab, github, manual, browser_plugin
	RecordType  RecordType `gorm:"not null" json:"record_type"` // task, meeting, doc, commit, manual
	Title       string     `gorm:"not null" json:"title"`
	Description string     `json:"description"`
	ProjectName string     `json:"project_name"`
	ExternalID  string     `json:"external_id"` // 外部系统 ID
	URL         string     `json:"url"`
	Metadata    string     `json:"metadata"` // JSON 扩展字段
	OccurredAt  time.Time  `json:"occurred_at"`
	WeekStart   string     `gorm:"index" json:"week_start"` // 归属周（周一日期），格式 2006-01-02
	IsHidden    bool       `gorm:"default:false" json:"is_hidden"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// WeeklyReport 周报表
type WeeklyReport struct {
	ID          string     `gorm:"primaryKey" json:"id"`
	UserID      string     `gorm:"index;not null" json:"user_id"`
	WeekStart   string     `gorm:"index;not null" json:"week_start"` // 2006-01-02
	WeekEnd     string     `gorm:"not null" json:"week_end"`
	TemplateID  uint       `json:"template_id"`
	Content     string     `gorm:"type:text" json:"content"` // JSON 格式：{"work":"...","problems":"...","plans":"..."}
	Markdown    string     `gorm:"type:text" json:"markdown"`
	Status      string     `gorm:"default:'draft'" json:"status"` // draft / submitted
	SubmittedAt *time.Time `json:"submitted_at"`
	Version     int        `gorm:"default:1" json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// WeeklyReportVersion 周报版本快照表
type WeeklyReportVersion struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ReportID  string    `gorm:"index;not null" json:"report_id"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	Version   int       `gorm:"not null" json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

// Template 模板表
type Template struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UserID      string    `gorm:"index" json:"user_id"` // 个人模板归属用户（飞书 OpenID）；全局/默认模板为空
	Name        string    `gorm:"not null" json:"name"`
	Description string    `json:"description"`
	Content     string    `gorm:"type:text;not null" json:"content"` // Go Template 内容
	Role        string    `json:"role"`                              // developer, tester, product_manager, designer, leader
	Scope       string    `gorm:"default:'personal'" json:"scope"`   // global / personal
	IsDefault   bool      `gorm:"default:false" json:"is_default"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AuditLog 操作审计日志表
type AuditLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     string    `gorm:"index" json:"user_id"`
	Action     string    `gorm:"not null" json:"action"` // login, generate, submit, export, sync, config
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Details    string    `json:"details"` // JSON 格式
	IPAddress  string    `json:"ip_address"`
	CreatedAt  time.Time `json:"created_at"`
}

// CronJob 定时任务状态表（持久化 cron 调度，重启不丢）
type CronJob struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Name      string     `gorm:"uniqueIndex;not null" json:"name"`
	Spec      string     `json:"spec"` // cron 表达式
	LastRunAt *time.Time `json:"last_run_at"`
	NextRunAt *time.Time `json:"next_run_at"`
	Enabled   bool       `gorm:"default:true" json:"enabled"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// FeishuToken 飞书 Token 存储（内存缓存 + 数据库持久化双保险）
type FeishuToken struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    string    `gorm:"uniqueIndex;not null" json:"user_id"`
	Token     string    `gorm:"not null" json:"token"` // Base64 编码存储
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CollectionRequest 采集请求
type CollectionRequest struct {
	UserID      string    `json:"user_id"`
	WeekStart   time.Time `json:"week_start"`
	DataSources []string  `json:"data_sources"` // lark, gitlab, github, manual
	TemplateID  uint      `json:"template_id"`
}

// CollectionResponse 采集响应
type CollectionResponse struct {
	Report    *WeeklyReport `json:"report"`
	AutoStats *AutoStats    `json:"auto_stats"`
}

// AutoStats 自动生成统计
type AutoStats struct {
	CommitCount  int `json:"commit_count"`
	TaskCount    int `json:"task_count"`
	MeetingCount int `json:"meeting_count"`
	ManualCount  int `json:"manual_count"`
}

// AfterCreate 钩子：自动创建 user_id + week_start 联合唯一约束
func (WeeklyReport) TableName() string {
	return "weekly_reports"
}

func (WorkRecord) TableName() string {
	return "work_records"
}

// BeforeCreate 生成 UUID 主键
func (r *WeeklyReport) BeforeCreate(tx *gorm.DB) error {
	if r.ID == "" {
		r.ID = fmt.Sprintf("report_%d", time.Now().UnixNano())
	}
	return nil
}

// ReportComment 周报批注/评论表

type ReportComment struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ReportID  string    `gorm:"index;not null" json:"report_id"`
	UserID    string    `gorm:"index;not null" json:"user_id"`
	UserName  string    `json:"user_name"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	Section   string    `json:"section"` // 批注针对的段落：work / problems / plans / general
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReportCompareResult 周报对比结果

type ReportCompareResult struct {
	WeekStart      string         `json:"week_start"`
	WeekEnd        string         `json:"week_end"`
	PrevWeekStart  string         `json:"prev_week_start"`
	PrevWeekEnd    string         `json:"prev_week_end"`
	CurrentStats   RecordStats    `json:"current_stats"`
	PreviousStats  RecordStats    `json:"previous_stats"`
	TaskChanges    []RecordChange `json:"task_changes"`
	MeetingChanges []RecordChange `json:"meeting_changes"`
	DocChanges     []RecordChange `json:"doc_changes"`
	CommitChanges  []RecordChange `json:"commit_changes"`
}

type RecordStats struct {
	TaskCount    int `json:"task_count"`
	MeetingCount int `json:"meeting_count"`
	DocCount     int `json:"doc_count"`
	CommitCount  int `json:"commit_count"`
}

type RecordChange struct {
	Type       string `json:"type"` // added / removed / changed
	Title      string `json:"title"`
	RecordType string `json:"record_type"`
}

// ReportWithComments 带批注的周报

type ReportWithComments struct {
	Report   WeeklyReport    `json:"report"`
	Comments []ReportComment `json:"comments"`
}
