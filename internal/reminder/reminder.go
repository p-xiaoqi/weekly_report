package reminder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/robfig/cron/v3"

	"weekly-report-system/internal/store"
)

// ReminderService 定时提醒服务
type ReminderService struct {
	cron    *cron.Cron
	store   *store.Store
	enabled bool
}

// NewReminderService 创建提醒服务
func NewReminderService(s *store.Store) *ReminderService {
	return &ReminderService{
		store: s,
	}
}

// Start 启动定时任务
func (r *ReminderService) Start(spec string, botWebhook string) {
	if r.enabled {
		return
	}
	r.cron = cron.New(cron.WithSeconds())

	// 添加定时任务：每周五上午10点提醒
	if spec == "" {
		spec = "0 0 10 * * 5" // 每周五 10:00
	}
	_, err := r.cron.AddFunc(spec, func() {
		log.Println("[Reminder] 定时提醒任务触发")
		// 获取当前周的开始日期（周一），使用本地时区零点计算，
		// 避免 Truncate(24h) 以 UTC 对齐造成的跨时区偏差。
		now := time.Now()
		offset := (int(now.Weekday()) + 6) % 7 // 周一=0, 周日=6
		monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -offset)
		weekStart := monday.Format("2006-01-02")

		// 检测未提交人员
		unsubmittedUsers, err := r.findUnsubmittedUsers(weekStart)
		if err != nil {
			log.Printf("[Reminder] 查询未提交人员失败: %v", err)
			return
		}

		if len(unsubmittedUsers) == 0 {
			log.Println("[Reminder] 本周所有用户均已提交周报")
			return
		}

		// 构建提醒消息
		mentionList := ""
		for _, u := range unsubmittedUsers {
			if mentionList != "" {
				mentionList += "、"
			}
			mentionList += u
		}

		content := fmt.Sprintf("⏰ 周报提醒：%d 位成员尚未提交本周周报（%s），请及时提交！", len(unsubmittedUsers), mentionList)

		if botWebhook != "" {
			if err := r.sendBotMessage(botWebhook, content); err != nil {
				log.Printf("[Reminder] 发送提醒失败: %v", err)
			}
		} else {
			log.Printf("[Reminder] %s", content)
		}
	})
	if err != nil {
		log.Printf("[Reminder] 添加定时任务失败: %v", err)
		return
	}

	r.cron.Start()
	r.enabled = true
	log.Printf("[Reminder] 定时提醒服务已启动，规则: %s", spec)
}

// findUnsubmittedUsers 返回本周未提交周报的用户列表
func (r *ReminderService) findUnsubmittedUsers(weekStart string) ([]string, error) {
	// 获取所有用户
	users, err := r.store.ListAllUsers()
	if err != nil {
		return nil, err
	}

	// 获取已提交用户
	submittedUsers, err := r.store.ListSubmittedUsers(weekStart)
	if err != nil {
		return nil, err
	}

	submittedMap := make(map[string]bool)
	for _, uid := range submittedUsers {
		submittedMap[uid] = true
	}

	// 找出未提交用户
	var unsubmitted []string
	for _, u := range users {
		if !submittedMap[u.FeishuOpenID] {
			name := u.Name
			if name == "" {
				name = u.FeishuOpenID
			}
			unsubmitted = append(unsubmitted, name)
		}
	}
	return unsubmitted, nil
}

// Stop 停止定时任务
func (r *ReminderService) Stop() {
	if r.cron != nil {
		r.cron.Stop()
		r.enabled = false
		log.Println("[Reminder] 定时提醒服务已停止")
	}
}

// SendTestMessage 发送测试消息到飞书群机器人
func (r *ReminderService) SendTestMessage(webhook, secret, content string) error {
	if webhook == "" {
		return fmt.Errorf("webhook URL 不能为空")
	}
	if content == "" {
		content = "🧪 这是周报系统的测试消息"
	}
	return r.sendBotMessage(webhook, content)
}

// sendBotMessage 发送飞书群机器人消息
func (r *ReminderService) sendBotMessage(webhook, content string) error {
	payload := map[string]interface{}{"msg_type": "text", "content": map[string]string{"text": content}}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
