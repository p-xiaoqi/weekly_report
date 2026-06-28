package config

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port    string `yaml:"port"`
		Mode    string `yaml:"mode"`     // debug / release
		BaseURL string `yaml:"base_url"` // 系统对外访问基础地址，用于卡片按钮跳转
	} `yaml:"server"`
	Feishu struct {
		AppID       string `yaml:"app_id"`
		AppSecret   string `yaml:"app_secret"`
		RedirectURI string `yaml:"redirect_uri"`
		// Scopes 为飞书 OAuth 授权时申请的权限范围（空格分隔）。
		// 不申请 scope 时，user_access_token 只带最小默认权限，
		// 会导致任务/日历/文档接口因缺权限而拉不到数据。
		Scopes string `yaml:"scopes"`
	} `yaml:"feishu"`
	Database struct {
		Path         string `yaml:"path"`
		MaxOpenConns int    `yaml:"max_open_conns"`
		MaxIdleConns int    `yaml:"max_idle_conns"`
	} `yaml:"database"`
	JWT struct {
		Secret      string `yaml:"secret"`
		ExpireHours int    `yaml:"expire_hours"`
	} `yaml:"jwt"`
	Log struct {
		Level    string `yaml:"level"`
		Output   string `yaml:"output"` // stdout / file
		FilePath string `yaml:"file_path"`
	} `yaml:"log"`
	Reminder struct {
		Enabled    bool   `yaml:"enabled"`
		Cron       string `yaml:"cron"`
		BotWebhook string `yaml:"bot_webhook"`
		BotSecret  string `yaml:"bot_secret"`
		// UseApp=true 时改用应用身份(App ID/Secret)通过 im API 发消息，而非 webhook。
		UseApp bool `yaml:"use_app"`
		// ChatID 为定时提醒要发送到的群（chat_id）；为空时定时提醒发送到各未提交用户本人。
		ChatID string `yaml:"chat_id"`
	} `yaml:"reminder"`
	// Admin 管理员白名单（用于 /api/v1/admin/* 接口的鉴权）。
	// 与"飞书群管理员"无关：此处指本系统自己的管理员授权名单。
	// Emails / OpenIDs 均为逗号分隔字符串，可由 yaml 或环境变量
	// ADMIN_EMAILS / ADMIN_OPEN_IDS 提供。
	Admin struct {
		Emails  string `yaml:"emails"`
		OpenIDs string `yaml:"open_ids"`
	} `yaml:"admin"`
}

// IsAdmin 判断给定 email 或飞书 open_id 是否在管理员白名单中。
// email 比较大小写不敏感；open_id 精确匹配。
func (c *Config) IsAdmin(email, openID string) bool {
	if email != "" && csvContains(c.Admin.Emails, email, true) {
		return true
	}
	if openID != "" && csvContains(c.Admin.OpenIDs, openID, false) {
		return true
	}
	return false
}

// csvContains 判断逗号分隔列表 list 是否包含 target；ignoreCase 控制是否忽略大小写。
func csvContains(list, target string, ignoreCase bool) bool {
	if list == "" || target == "" {
		return false
	}
	if ignoreCase {
		target = strings.ToLower(target)
	}
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if ignoreCase {
			item = strings.ToLower(item)
		}
		if item == target {
			return true
		}
	}
	return false
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	data, err := os.ReadFile("configs/config.yaml")
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// 环境变量覆盖
	cfg.Feishu.AppID = resolveEnv(cfg.Feishu.AppID)
	cfg.Feishu.AppSecret = resolveEnv(cfg.Feishu.AppSecret)
	cfg.Feishu.RedirectURI = resolveEnv(cfg.Feishu.RedirectURI)
	cfg.Feishu.Scopes = resolveEnv(cfg.Feishu.Scopes)
	if v := os.Getenv("FEISHU_SCOPES"); v != "" {
		cfg.Feishu.Scopes = v
	}
	cfg.JWT.Secret = resolveEnv(cfg.JWT.Secret)
	cfg.Reminder.BotWebhook = resolveEnv(cfg.Reminder.BotWebhook)
	cfg.Reminder.BotSecret = resolveEnv(cfg.Reminder.BotSecret)
	cfg.Reminder.ChatID = resolveEnv(cfg.Reminder.ChatID)
	cfg.Admin.Emails = resolveEnv(cfg.Admin.Emails)
	cfg.Admin.OpenIDs = resolveEnv(cfg.Admin.OpenIDs)
	// 环境变量直接提供时优先覆盖（兼容未在 yaml 中使用 ${...} 占位的情况）
	if v := os.Getenv("ADMIN_EMAILS"); v != "" {
		cfg.Admin.Emails = v
	}
	if v := os.Getenv("ADMIN_OPEN_IDS"); v != "" {
		cfg.Admin.OpenIDs = v
	}
	// 机器人 webhook / secret 的环境变量兜底（兼容 FEISHU_BOT_WEBHOOK 与 REMINDER_BOT_WEBHOOK 两种命名）
	if cfg.Reminder.BotWebhook == "" {
		if v := os.Getenv("FEISHU_BOT_WEBHOOK"); v != "" {
			cfg.Reminder.BotWebhook = v
		} else if v := os.Getenv("REMINDER_BOT_WEBHOOK"); v != "" {
			cfg.Reminder.BotWebhook = v
		}
	}
	if cfg.Reminder.BotSecret == "" {
		if v := os.Getenv("REMINDER_BOT_SECRET"); v != "" {
			cfg.Reminder.BotSecret = v
		} else if v := os.Getenv("FEISHU_BOT_SECRET"); v != "" {
			cfg.Reminder.BotSecret = v
		}
	}
	// 应用身份发消息模式的环境变量兜底
	if v := os.Getenv("REMINDER_USE_APP"); v == "1" || strings.EqualFold(v, "true") {
		cfg.Reminder.UseApp = true
	}
	if cfg.Reminder.ChatID == "" {
		if v := os.Getenv("REMINDER_CHAT_ID"); v != "" {
			cfg.Reminder.ChatID = v
		}
	}

	// 默认值
	if cfg.Server.Port == "" {
		cfg.Server.Port = "8080"
	}
	if cfg.Server.Mode == "" {
		cfg.Server.Mode = "debug"
	}
	// 系统对外基础地址：默认 localhost+端口，可由 SYSTEM_BASE_URL 覆盖
	if cfg.Server.BaseURL == "" {
		cfg.Server.BaseURL = "http://localhost:" + cfg.Server.Port
	}
	if v := os.Getenv("SYSTEM_BASE_URL"); v != "" {
		cfg.Server.BaseURL = v
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "weekly_report.db"
	}
	if cfg.JWT.Secret == "" {
		cfg.JWT.Secret = os.Getenv("JWT_SECRET")
	}
	if cfg.JWT.ExpireHours == 0 {
		cfg.JWT.ExpireHours = 168 // 7天
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Output == "" {
		cfg.Log.Output = "stdout"
	}

	// 🔴 安全校验：JWT Secret 不能为默认值或空
	if cfg.JWT.Secret == "" || cfg.JWT.Secret == "your-random-secret-key-change-in-production" {
		// 个人测试环境允许自动生成，但打印警告
		fmt.Println("⚠️  WARNING: JWT Secret 使用自动生成的随机值，生产环境请通过环境变量 JWT_SECRET 设置")
		secret, err := generateRandomSecret(32)
		if err != nil {
			return nil, fmt.Errorf("generate random secret failed: %w", err)
		}
		cfg.JWT.Secret = secret
	}

	// 飞书 OAuth 申请权限：默认覆盖任务/日历/文档读取与文档创建，
	// 不申请 scope 会导致 user_access_token 缺权限、任务等数据拉不到。
	if cfg.Feishu.Scopes == "" {
		cfg.Feishu.Scopes = "contact:user.base:readonly task:task:readonly calendar:calendar:readonly drive:drive docx:document"
	}

	// 安全校验：飞书密钥不能为空
	if cfg.Feishu.AppID == "" || cfg.Feishu.AppID == "your-app-id" {
		return nil, fmt.Errorf("feishu app_id is required, set via FEISHU_APP_ID env or configs/config.yaml")
	}
	if cfg.Feishu.AppSecret == "" || cfg.Feishu.AppSecret == "your-app-secret" {
		return nil, fmt.Errorf("feishu app_secret is required, set via FEISHU_APP_SECRET env or configs/config.yaml")
	}

	return &cfg, nil
}

func resolveEnv(s string) string {
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		if v := os.Getenv(s[2 : len(s)-1]); v != "" {
			return v
		}
		// 环境变量未设置，返回空字符串以便后续校验捕获
		return ""
	} else if strings.HasPrefix(s, "$") {
		if v := os.Getenv(s[1:]); v != "" {
			return v
		}
		return ""
	}
	return s
}

func generateRandomSecret(n int) (string, error) {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	b := make([]byte, n)
	for i := range b {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return "", err
		}
		b[i] = letters[num.Int64()]
	}
	return string(b), nil
}
