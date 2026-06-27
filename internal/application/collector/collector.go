package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"text/template"
	"time"

	"weekly-report-system/internal/adapter/lark"
	"weekly-report-system/internal/git"
	"weekly-report-system/internal/model"
	"weekly-report-system/internal/store"
)

// problemKeywords 用于从工作记录中轻量启发式识别"问题/阻塞"候选项。
var problemKeywords = []string{
	"阻塞", "blocked", "block", "bug", "失败", "fail", "error",
	"问题", "risk", "风险", "delay", "延期", "卡住", "异常",
}

// extractProblems 扫描工作记录（提交信息、任务标题/描述），命中关键词的条目
// 作为候选问题返回，并按标题去重。
func extractProblems(records []model.WorkRecord) []string {
	var problems []string
	seen := make(map[string]bool)
	for _, rec := range records {
		if rec.IsHidden {
			continue
		}
		lower := strings.ToLower(rec.Title + " " + rec.Description)
		for _, kw := range problemKeywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				item := strings.TrimSpace(rec.Title)
				if item == "" {
					item = strings.TrimSpace(rec.Description)
				}
				if item != "" && !seen[item] {
					seen[item] = true
					problems = append(problems, item)
				}
				break
			}
		}
	}
	return problems
}

// mergeRecords 将 extra 中尚未出现在 base 的记录追加进来，避免重复。
// 去重键优先使用 SourceType+ExternalID，缺失时回退到 SourceType+RecordType+Title。
func mergeRecords(base, extra []model.WorkRecord) []model.WorkRecord {
	key := func(r model.WorkRecord) string {
		if r.ExternalID != "" {
			return r.SourceType + "|" + r.ExternalID
		}
		return r.SourceType + "|" + string(r.RecordType) + "|" + r.Title
	}
	seen := make(map[string]bool, len(base))
	for _, r := range base {
		seen[key(r)] = true
	}
	for _, r := range extra {
		k := key(r)
		if !seen[k] {
			seen[k] = true
			base = append(base, r)
		}
	}
	return base
}

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

func (c *Collector) Collect(ctx context.Context, req model.CollectionRequest) (*model.WeeklyReport, []string, error) {
	weekEnd := req.WeekStart.AddDate(0, 0, 6).Add(23*time.Hour + 59*time.Minute + 59*time.Second)
	weekStartStr := req.WeekStart.Format("2006-01-02")
	weekEndStr := weekEnd.Format("2006-01-02")

	var allRecords []model.WorkRecord
	var nextWeekEvents []model.WorkRecord
	var warnings []string

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
				return nil, warnings, fmt.Errorf("user not authorized with Lark, please visit /api/v1/auth/lark/login first")
			}
			fetchReq := lark.FetchRequest{
				UserID:        req.UserID,
				WeekStart:     req.WeekStart,
				WeekEnd:       weekEnd,
				UserAuthToken: token,
			}
			records, nwe, err := c.larkAdapter.Fetch(ctx, fetchReq)
			if err != nil {
				return nil, warnings, fmt.Errorf("fetch from lark failed: %w", err)
			}
			allRecords = append(allRecords, records...)
			nextWeekEvents = append(nextWeekEvents, nwe...)
		case "gitlab":
			records, ws := c.fetchGitLab(ctx, req.UserID, req.WeekStart, weekEnd)
			warnings = append(warnings, ws...)
			allRecords = append(allRecords, records...)
		case "github":
			records, ws := c.fetchGitHub(ctx, req.UserID, req.WeekStart, weekEnd)
			warnings = append(warnings, ws...)
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
		return nil, warnings, fmt.Errorf("save work records failed: %w", err)
	}
	if err := c.store.SaveWorkRecords(nextWeekEvents); err != nil {
		return nil, warnings, fmt.Errorf("save next week events failed: %w", err)
	}

	// 合并数据库中已存储的手动录入 / 浏览器插件推送记录，使其同样出现在生成的草稿中。
	// GetWorkRecords 会返回当周全部非隐藏记录（含刚保存的 allRecords），由 mergeRecords 去重。
	if stored, err := c.store.GetWorkRecords(req.UserID, weekStartStr); err == nil {
		allRecords = mergeRecords(allRecords, stored)
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

	return report, warnings, nil

}

// markSync 在一次数据源拉取后更新其同步状态（SyncStatus/LastSyncAt/SyncError），
// 使前端的数据源状态标签能够反映真实的连接结果。owner-scoped：ds 来自当前用户。
func (c *Collector) markSync(ds *model.DataSource, ok bool, errMsg string) {
	now := time.Now()
	if ok {
		ds.SyncStatus = "success"
		ds.SyncError = ""
	} else {
		ds.SyncStatus = "failed"
		ds.SyncError = errMsg
	}
	ds.LastSyncAt = &now
	if err := c.store.UpdateDataSource(ds); err != nil {
		log.Printf("[WARN] update datasource %q sync status failed: %v", ds.Name, err)
	}
}

func (c *Collector) fetchGitLab(ctx context.Context, userID string, weekStart, weekEnd time.Time) ([]model.WorkRecord, []string) {
	dss, err := c.store.GetDataSources(userID)
	if err != nil {
		return nil, []string{fmt.Sprintf("gitlab: 读取数据源配置失败: %v", err)}
	}

	var records []model.WorkRecord
	var warnings []string

	for i := range dss {
		ds := &dss[i]
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
			warnings = append(warnings, fmt.Sprintf("gitlab 数据源 %q 配置解析失败: %v", ds.Name, err))
			c.markSync(ds, false, "配置解析失败: "+err.Error())
			continue
		}

		source := &git.GitLabSource{
			Token:       cfg.Token,
			ServerURL:   cfg.ServerURL,
			ProjectPath: cfg.ProjectPath,
		}

		// 作者过滤是可选的：仅当显式配置了 email 时才按作者过滤，否则返回窗口内全部提交。
		commits, err := source.FetchCommits(ctx, cfg.Email, weekStart, weekEnd)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("gitlab 数据源 %q 拉取失败（请检查网络连通性 / Token / 项目路径配置）: %v", ds.Name, err))
			c.markSync(ds, false, err.Error())
			continue
		}

		if len(commits) == 0 {
			hint := ""
			if cfg.Email != "" {
				hint = fmt.Sprintf("（已按作者 email=%s 过滤，请确认与 git 提交作者一致）", cfg.Email)
			}
			warnings = append(warnings, fmt.Sprintf("gitlab 数据源 %q 连接成功，但本周（%s ~ %s）没有匹配到任何提交%s",
				ds.Name, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"), hint))
		}

		for _, commit := range commits {
			records = append(records, model.WorkRecord{
				SourceType:  "gitlab",
				ExternalID:  commit.ID,
				RecordType:  model.TypeCommit,
				Title:       commit.Message,
				ProjectName: cfg.ProjectPath,
				OccurredAt:  parseGitTime(commit.CreatedAt),
			})
		}
		c.markSync(ds, true, "")
	}

	return records, warnings
}

func (c *Collector) fetchGitHub(ctx context.Context, userID string, weekStart, weekEnd time.Time) ([]model.WorkRecord, []string) {
	dss, err := c.store.GetDataSources(userID)
	if err != nil {
		return nil, []string{fmt.Sprintf("github: 读取数据源配置失败: %v", err)}
	}

	var records []model.WorkRecord
	var warnings []string

	for i := range dss {
		ds := &dss[i]
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
			warnings = append(warnings, fmt.Sprintf("github 数据源 %q 配置解析失败: %v", ds.Name, err))
			c.markSync(ds, false, "配置解析失败: "+err.Error())
			continue
		}

		source := &git.GitHubSource{
			Token: cfg.Token,
			Owner: cfg.Owner,
			Repo:  cfg.Repo,
		}

		// 作者过滤是可选的：仅当显式配置了 author 时才按作者过滤，否则返回窗口内全部提交。
		commits, err := source.FetchCommits(ctx, cfg.Author, weekStart, weekEnd)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("github 数据源 %q 拉取失败（请检查网络连通性 / Token / owner、repo 配置）: %v", ds.Name, err))
			c.markSync(ds, false, err.Error())
			continue
		}

		if len(commits) == 0 {
			hint := ""
			if cfg.Author != "" {
				hint = fmt.Sprintf("（已按作者 author=%s 过滤，请确认与 git 提交作者一致）", cfg.Author)
			}
			warnings = append(warnings, fmt.Sprintf("github 数据源 %q 连接成功，但本周（%s ~ %s）没有匹配到任何提交%s",
				ds.Name, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"), hint))
		}

		for _, commit := range commits {
			records = append(records, model.WorkRecord{
				SourceType:  "github",
				ExternalID:  commit.SHA,
				RecordType:  model.TypeCommit,
				Title:       commit.Commit.Message,
				ProjectName: fmt.Sprintf("%s/%s", cfg.Owner, cfg.Repo),
				OccurredAt:  parseGitTime(commit.Commit.Author.Date),
			})
		}
		c.markSync(ds, true, "")
	}

	return records, warnings
}

func parseGitTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Now()
}

func (c *Collector) RenderMarkdown(r *model.WeeklyReport, records []model.WorkRecord, nextWeekEvents []model.WorkRecord, templateContent string) string {
	return RenderReport(r, records, nextWeekEvents, templateContent)
}

// RenderReport 是周报 Markdown 渲染的统一入口，供采集主流程（Collect）
// 和浏览器插件推送路径共同复用，确保两条链路产出格式完全一致。
func RenderReport(r *model.WeeklyReport, records []model.WorkRecord, nextWeekEvents []model.WorkRecord, templateContent string) string {
	if templateContent == "" {
		return renderMarkdownFallback(r, records, nextWeekEvents)
	}

	data := buildTemplateData(r, records, nextWeekEvents)

	tmpl, err := template.New("report").Parse(templateContent)
	if err != nil {
		log.Printf("[WARN] template parse failed: %v, falling back to default", err)
		return renderMarkdownFallback(r, records, nextWeekEvents)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("[WARN] template execute failed: %v, falling back to default", err)
		return renderMarkdownFallback(r, records, nextWeekEvents)
	}

	return buf.String()
}

func renderMarkdownFallback(r *model.WeeklyReport, records []model.WorkRecord, nextWeekEvents []model.WorkRecord) string {
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

	md += "## 遇到的问题\n\n"
	if problems := extractProblems(records); len(problems) > 0 {
		for _, p := range problems {
			md += fmt.Sprintf("- %s\n", p)
		}
	} else {
		md += "- （本周暂无自动识别到的问题，请补充）\n"
	}

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

	problems := extractProblems(records)

	return model.ReportTemplateData{
		WeekStart:      report.WeekStart,
		WeekEnd:        report.WeekEnd,
		WeekRange:      report.WeekStart + " ~ " + report.WeekEnd,
		Tasks:          tasks,
		Meetings:       meetings,
		Docs:           docs,
		NextWeekEvents: nextItems,
		Problems:       problems,
		TaskCount:      len(tasks),
		MeetingCount:   len(meetings),
		DocCount:       len(docs),
		NextWeekCount:  len(nextItems),
		HasTasks:       len(tasks) > 0,
		HasMeetings:    len(meetings) > 0,
		HasDocs:        len(docs) > 0,
		HasNextWeek:    len(nextItems) > 0,
		HasProblems:    len(problems) > 0,
	}
}
