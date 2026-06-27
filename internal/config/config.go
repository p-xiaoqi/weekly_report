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
		Port string `yaml:"port"`
		Mode string `yaml:"mode"` // debug / release
	} `yaml:"server"`
	Feishu struct {
		AppID       string `yaml:"app_id"`
		AppSecret   string `yaml:"app_secret"`
		RedirectURI string `yaml:"redirect_uri"`
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
	} `yaml:"reminder"`
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
	cfg.JWT.Secret = resolveEnv(cfg.JWT.Secret)
	cfg.Reminder.BotWebhook = resolveEnv(cfg.Reminder.BotWebhook)
	cfg.Reminder.BotSecret = resolveEnv(cfg.Reminder.BotSecret)

	// 默认值
	if cfg.Server.Port == "" {
		cfg.Server.Port = "8080"
	}
	if cfg.Server.Mode == "" {
		cfg.Server.Mode = "debug"
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
