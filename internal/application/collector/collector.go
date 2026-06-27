package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"text/template"
	"time"

	"weekly-report-system/internal/adapter/lark"
	"weekly-report-system/internal/git"
	"weekly-report-system/internal/model"
	"weekly-report-system/internal/store"
)

type Collector struct {
	larkAdapter *lark.Adapter
	store       *store.Store
}

func New(larkAdapter *lark.Adapter, store *store.Store) *Collector {
	return &Collector{
		larkAdapter: larkAdapter,
		store:       store,
	}
}

func (c *Collector) Collect(ctx context.Context, req model.CollectionRequest) (*model.WeeklyReport, error) {
	weekEnd := req.WeekStart.AddDate(0, 0, 6).Add(23*time.Hour + 59*time.Minute + 59*time.Second)
	weekStartStr := req.WeekStart.Format("2006-01-02")
	weekEndStr := weekEnd.Format("2006-01-02")

	var allRecords []model.WorkRecord
	var nextWeekEvents []model.WorkRecord

	// 根据数据源列表采集
	sources := req.DataSources
	if len(sources) == 0 {
		sources = []string{"lark"}
	}

	for _, source := range sources {
		switch source {
		case "lark":
			token, ok := c.store.GetToken(req.UserID)
			if !ok {
				return nil, fmt.Errorf("user not authorized with Lark, please visit /api/v1/auth/lark/login first")
			}
			fetchReq := lark.FetchRequest{
				UserID:        req.UserID,
				WeekStart:     req.WeekStart,
				WeekEnd:       weekEnd,
				UserAuthToken: token,
			}
			records, nwe, err := c.larkAdapter.Fetch(ctx, fetchReq)
			if err != nil {
				return nil, fmt.Errorf("fetch from lark failed: %w", err)
			}
			allRecords = append(allRecords, records...)
			nextWeekEvents = append(nextWeekEvents, nwe...)
		case "gitlab":
			records, err := c.fetchGitLab(ctx, req.UserID, req.WeekStart, weekEnd)
			if err != nil {
				return nil, fmt.Errorf("fetch from gitlab failed: %w", err)
			}
			allRecords = append(allRecords, records...)
		case "github":
			records, err := c.fetchGitHub(ctx, req.UserID, req.WeekStart, weekEnd)
			if err != nil {
				return nil, fmt.Errorf("fetch from github failed: %w", err)
			}
			allRecords = append(allRecords, records...)
		}
	}

	// 设置工作记录的 UserID 和 WeekStart
	for i := range allRecords {
		allRecords[i].UserID = req.UserID
		allRecords[i].WeekStart = weekStartStr
		allRecords[i].ReportID = ""
	}
	for i := range nextWeekEvents {
		nextWeekEvents[i].UserID = req.UserID
		nextWeekEvents[i].WeekStart = weekEndStr
	}

	// 保存工作记录到数据库
	if err := c.store.SaveWorkRecords(allRecords); err != nil {
		return nil, fmt.Errorf("save work records failed: %w", err)
	}
	if err := c.store.SaveWorkRecords(nextWeekEvents); err != nil {
		return nil, fmt.Errorf("save next week events failed: %w", err)
	}

	report := &model.WeeklyReport{
		ID:        generateID(),
		UserID:    req.UserID,
		WeekStart: weekStartStr,
		WeekEnd:   weekEndStr,
		Status:    "draft",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// 确定使用的模板
	var templateContent string
	var templateID uint
	if req.TemplateID > 0 {
		if t, err := c.store.GetTemplateByID(req.TemplateID); err == nil && t != nil {
			templateContent = t.Content
			templateID = t.ID
		}
	}
	if templateContent == "" {
		if t, err := c.store.GetDefaultTemplate(""); err == nil && t != nil {
			templateContent = t.Content
			templateID = t.ID
		}
	}
	report.TemplateID = templateID

	report.Markdown = c.RenderMarkdown(report, allRecords, nextWeekEvents, templateContent)
	c.store.SaveReport(report)

	return report, nil
}

func (c *Collector) fetchGitLab(ctx context.Context, userID string, weekStart, weekEnd time.Time) ([]model.WorkRecord, error) {
	dss, err := c.store.GetDataSources(userID)
	if err != nil {
		return nil, err
	}
	var records []model.WorkRecord
	for _, ds := range dss {
		if ds.Type != "gitlab" || !ds.Enabled {
			continue
		}
		var cfg struct {
			Token       string `json:"token"`
			ServerURL   string `json:"server_url"`
			ProjectPath string `json:"project_path"`
			Email       string `json:"email"`
		}
		if err := json.Unmarshal([]byte(ds.Config), &cfg); err != nil {
			continue
		}
		source := &git.GitLabSource{
			Token:       cfg.Token,
			ServerURL:   cfg.ServerURL,
			ProjectPath: cfg.ProjectPath,
		}
		commits, err := source.FetchCommits(ctx, cfg.Email, weekStart, weekEnd)
		if err != nil {
			continue
		}
		for _, commit := range commits {
			records = append(records, model.WorkRecord{
				SourceType: "gitlab",
				ExternalID:  commit.ID,
				RecordType:  model.TypeCommit,
				Title:       commit.Message,
				ProjectName: cfg.ProjectPath,
				OccurredAt:  parseGitTime(commit.CreatedAt),
			})
		}
	}
	return records, nil
}

func (c *Collector) fetchGitHub(ctx context.Context, userID string, weekStart, weekEnd time.Time) ([]model.WorkRecord, error) {
	dss, err := c.store.GetDataSources(userID)
	if err != nil {
		return nil, err
	}
	var records []model.WorkRecord
	for _, ds := range dss {
		if ds.Type != "github" || !ds.Enabled {
			continue
		}
		var cfg struct {
			Token  string `json:"token"`
			Owner  string `json:"owner"`
			Repo   string `json:"repo"`
			Author string `json:"author"`
		}
		if err := json.Unmarshal([]byte(ds.Config), &cfg); err != nil {
			continue
		}
		source := &git.GitHubSource{
			Token: cfg.Token,
			Owner: cfg.Owner,
			Repo:  cfg.Repo,
		}
		commits, err := source.FetchCommits(ctx, cfg.Author, weekStart, weekEnd)
		if err != nil {
			continue
		}
		for _, commit := range commits {
			records = append(records, model.WorkRecord{
				SourceType: "github",
				ExternalID:  commit.SHA,
				RecordType:  model.TypeCommit,
				Title:       commit.Commit.Message,
				ProjectName: fmt.Sprintf("%s/%s", cfg.Owner, cfg.Repo),
				OccurredAt:  parseGitTime(commit.Commit.Author.Date),
			})
		}
	}
	return records, nil
}

func parseGitTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Now()
}

func (c *Collector) RenderMarkdown(r *model.WeeklyReport, records []model.WorkRecord, nextWeekEvents []model.WorkRecord, templateContent string) string {
	if templateContent == "" {
		return c.renderMarkdownFallback(r, records, nextWeekEvents)
	}

	data := buildTemplateData(r, records, nextWeekEvents)

	tmpl, err := template.New("report").Parse(templateContent)
	if err != nil {
		log.Printf("[WARN] template parse failed: %v, falling back to default", err)
		return c.renderMarkdownFallback(r, records, nextWeekEvents)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("[WARN] template execute failed: %v, falling back to default", err)
		return c.renderMarkdownFallback(r, records, nextWeekEvents)
	}

	return buf.String()
}

func (c *Collector) renderMarkdownFallback(r *model.WeeklyReport, records []model.WorkRecord, nextWeekEvents []model.WorkRecord) string {
	md := fmt.Sprintf("## 本周完成工作 (%s ~ %s)\n\n", r.WeekStart, r.WeekEnd)

	var tasks, meetings, docs []model.WorkRecord
	for _, rec := range records {
		if rec.IsHidden {
			continue
		}
		switch rec.RecordType {
		case model.TypeMeeting:
			meetings = append(meetings, rec)
		case model.TypeDoc:
			docs = append(docs, rec)
		default:
			tasks = append(tasks, rec)
		}
	}

	if len(tasks) > 0 {
		md += "### 任务进展\n\n"
		for _, rec := range tasks {
			md += fmt.Sprintf("- [x] %s\n", rec.Title)
			if rec.Description != "" {
				md += fmt.Sprintf("  - %s\n", rec.Description)
			}
		}
		md += "\n"
	}

	if len(meetings) > 0 {
		md += "### 会议/日程\n\n"
		for _, rec := range meetings {
			md += fmt.Sprintf("- 🗓️ %s (%s)\n", rec.Title, rec.OccurredAt.Format("01-02 15:04"))
			if rec.Description != "" {
				md += fmt.Sprintf("  - %s\n", rec.Description)
			}
			if rec.ProjectName != "" {
				md += fmt.Sprintf("  - 📍 地点：%s\n", rec.ProjectName)
			}
		}
		md += "\n"
	}

	if len(docs) > 0 {
		md += "### 文档编辑\n\n"
		for _, rec := range docs {
			md += fmt.Sprintf("- 📝 %s (%s)\n", rec.Title, rec.OccurredAt.Format("01-02"))
		}
		md += "\n"
	}

	if len(tasks) == 0 && len(meetings) == 0 && len(docs) == 0 {
		md += "- 本周暂无记录\n\n"
	}

	md += "## 遇到的问题\n\n- 待补充\n"

	md += "\n## 下周计划\n\n"
	if len(nextWeekEvents) > 0 {
		for _, rec := range nextWeekEvents {
			md += fmt.Sprintf("- [ ] %s (%s)\n", rec.Title, rec.OccurredAt.Format("01-02 15:04"))
			if rec.ProjectName != "" {
				md += fmt.Sprintf("  - 📍 地点：%s\n", rec.ProjectName)
			}
		}
	} else {
		md += "- 待补充\n"
	}

	return md
}

func generateID() string {
	return fmt.Sprintf("report_%d", time.Now().UnixNano())
}

// buildTemplateData 将工作记录转换为模板可用数据结构
func buildTemplateData(report *model.WeeklyReport, records, nextWeek []model.WorkRecord) model.ReportTemplateData {
	var tasks, meetings, docs []model.TemplateItem
	for _, rec := range records {
		if rec.IsHidden {
			continue
		}
		item := model.TemplateItem{
			Title:        rec.Title,
			Description:  rec.Description,
			ProjectName:  rec.ProjectName,
			URL:          rec.URL,
			OccurredAt:   rec.OccurredAt,
			OccurredDate: rec.OccurredAt.Format("01-02"),
		}
		switch rec.RecordType {
		case model.TypeMeeting:
			meetings = append(meetings, item)
		case model.TypeDoc:
			docs = append(docs, item)
		default:
			tasks = append(tasks, item)
		}
	}

	var nextItems []model.TemplateItem
	for _, rec := range nextWeek {
		nextItems = append(nextItems, model.TemplateItem{
			Title:        rec.Title,
			Description:  rec.Description,
			ProjectName:  rec.ProjectName,
			URL:          rec.URL,
			OccurredAt:   rec.OccurredAt,
			OccurredDate: rec.OccurredAt.Format("01-02 15:04"),
		})
	}

	return model.ReportTemplateData{
		WeekStart:      report.WeekStart,
		WeekEnd:        report.WeekEnd,
		WeekRange:      report.WeekStart + " ~ " + report.WeekEnd,
		Tasks:          tasks,
		Meetings:       meetings,
		Docs:           docs,
		NextWeekEvents: nextItems,
		TaskCount:      len(tasks),
		MeetingCount:   len(meetings),
		DocCount:       len(docs),
		NextWeekCount:  len(nextItems),
		HasTasks:       len(tasks) > 0,
		HasMeetings:    len(meetings) > 0,
		HasDocs:        len(docs) > 0,
		HasNextWeek:    len(nextItems) > 0,
	}
}
