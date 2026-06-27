package lark

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"weekly-report-system/internal/model"
)

type Adapter struct {
	client *Client
}

func NewAdapter(client *Client) *Adapter {
	return &Adapter{client: client}
}

func (a *Adapter) Type() string {
	return "lark"
}

func parseTimestampOrRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// 尝试解析为整数时间戳（毫秒或秒）
	if ts, err := strconv.ParseInt(s, 10, 64); err == nil && ts > 0 {
		if ts > 1e12 {
			return time.UnixMilli(ts)
		}
		return time.Unix(ts, 0)
	}
	// 尝试 RFC3339Nano（兼容带毫秒或不带毫秒）
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	// 兜底 RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func (a *Adapter) Fetch(ctx context.Context, req FetchRequest) ([]model.WorkRecord, []model.WorkRecord, error) {
	token := req.UserAuthToken
	if token == "" {
		var err error
		token, err = a.client.getTenantToken(ctx)
		if err != nil {
			return nil, nil, err
		}
	}

	var records []model.WorkRecord
	var errs []string

	tasks, err := a.client.FetchUserTasks(ctx, token)
	if err != nil {
		errs = append(errs, fmt.Sprintf("任务: %v", err))
		log.Printf("[WARN] 获取飞书任务失败: %v", err)
	} else {
		log.Printf("[DEBUG] 获取到 %d 条任务", len(tasks))
		for _, task := range tasks {
			// 飞书任务状态字段是 status: "done" / "todo"
			if task.Status != "done" {
				continue
			}
			// 优先使用 completed_at（飞书实际返回的字段）
			completedTimeStr := task.CompletedAt
			if completedTimeStr == "" {
				completedTimeStr = task.CompletedTime
			}
			completedTime := parseTimestampOrRFC3339(completedTimeStr)
			if completedTime.IsZero() {
				log.Printf("[WARN] 任务 completed_at 解析失败: %s", completedTimeStr)
				continue
			}
			// 只保留本周内完成的任务
			if completedTime.Before(req.WeekStart) || completedTime.After(req.WeekEnd) {
				continue
			}
			log.Printf("[DEBUG] 任务: %s, completed_at=%s", task.Summary, completedTime.Format("2006-01-02 15:04"))
			records = append(records, model.WorkRecord{
				UserID:      req.UserID,
				SourceType:  "lark",
				ExternalID:  task.GUID,
				RecordType:  model.TypeTask,
				Title:       task.Summary,
				Description: task.Notes,
				ProjectName: task.TopicName,
				OccurredAt:  completedTime,
			})
		}
	}

	events, err := a.client.FetchUserCalendarEvents(ctx, token, req.WeekStart, req.WeekEnd)
	if err != nil {
		errs = append(errs, fmt.Sprintf("日历: %v", err))
		log.Printf("[WARN] 获取飞书日历失败: %v", err)
	} else {
		log.Printf("[DEBUG] 获取到 %d 条日历事件", len(events))
		for _, event := range events {
			// 过滤已取消的日程
			if event.Status == "cancelled" {
				log.Printf("[DEBUG] 跳过已取消日程: %s", event.Summary)
				continue
			}
			occurredAt := parseTimestampOrRFC3339(event.StartTime.Timestamp)
			if occurredAt.IsZero() {
				log.Printf("[WARN] 日历事件时间解析失败: %s", event.StartTime.Timestamp)
			}
			log.Printf("[DEBUG] 日历事件: %s, time=%s", event.Summary, occurredAt.Format("2006-01-02 15:04"))
			records = append(records, model.WorkRecord{
				SourceType:  "lark",
				ExternalID:  event.EventID,
				RecordType:  model.TypeMeeting,
				Title:       event.Summary,
				Description: event.Description,
				ProjectName: event.Location.Name,
				OccurredAt:  occurredAt,
			})
		}
	}

	docs, err := a.client.FetchUserDocs(ctx, token)
	if err != nil {
		errs = append(errs, fmt.Sprintf("文档: %v", err))
		log.Printf("[WARN] 获取飞书文档失败: %v", err)
	} else {
		log.Printf("[DEBUG] 获取到 %d 条文档", len(docs))
		for _, doc := range docs {
			occurredAt := parseTimestampOrRFC3339(doc.ModifiedTime)
			if occurredAt.IsZero() {
				occurredAt = parseTimestampOrRFC3339(doc.CreatedTime)
			}
			log.Printf("[DEBUG] 文档: %s, time=%s", doc.Name, occurredAt.Format("2006-01-02 15:04"))

			// 只保留本周内的文档
			if occurredAt.Before(req.WeekStart) || occurredAt.After(req.WeekEnd) {
				continue
			}

			records = append(records, model.WorkRecord{
				SourceType:  "lark",
				ExternalID:  doc.Token,
				RecordType:  model.TypeDoc,
				Title:       doc.Name,
				Description: "",
				ProjectName: "",
				OccurredAt:  occurredAt,
			})
		}
	}

	// 查询下周日程用于下周计划
	nextWeekStart := req.WeekStart.AddDate(0, 0, 7)
	nextWeekEnd := req.WeekEnd.AddDate(0, 0, 7)
	var nextWeekEvents []model.WorkRecord
	nextEvents, err := a.client.FetchUserCalendarEvents(ctx, token, nextWeekStart, nextWeekEnd)
	if err != nil {
		log.Printf("[WARN] 获取下周日程失败: %v", err)
	} else {
		log.Printf("[DEBUG] 获取到 %d 条下周日程", len(nextEvents))
		for _, event := range nextEvents {
			if event.Status == "cancelled" {
				continue
			}
			occurredAt := parseTimestampOrRFC3339(event.StartTime.Timestamp)
			nextWeekEvents = append(nextWeekEvents, model.WorkRecord{
				SourceType:  "lark",
				ExternalID:  event.EventID,
				RecordType:  model.TypeMeeting,
				Title:       event.Summary,
				Description: event.Description,
				ProjectName: event.Location.Name,
				OccurredAt:  occurredAt,
			})
		}
	}

	if len(records) == 0 && len(errs) > 0 {
		return nil, nil, fmt.Errorf("飞书数据获取失败: %s", strings.Join(errs, "; "))
	}
	return records, nextWeekEvents, nil
}

type FetchRequest struct {
	UserID          string
	WeekStart       time.Time
	WeekEnd         time.Time
	UserAuthToken   string
	EnterpriseToken string
}
