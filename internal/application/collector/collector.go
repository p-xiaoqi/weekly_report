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

// cleanCommitSubject 从可能多行的 commit message 中提取"标题行"，
// 过滤掉 Co-Authored-By / Change-Id / Signed-off-by 等 trailer 噪声，让展示更专业。
func cleanCommitSubject(msg string) string {
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "co-authored-by:") ||
			strings.HasPrefix(low, "change-id:") ||
			strings.HasPrefix(low, "signed-off-by:") ||
			strings.HasPrefix(low, "reviewed-by:") {
			continue
		}
		return line
	}
	return strings.TrimSpace(msg)
}

// extractProblems 扫描工作记录（任务标题/描述），命中关键词的条目
// 作为候选问题返回，并按标题去重。代码提交不计入问题（提交信息常含"修复/fix"等词，
// 属于正常产出而非阻塞问题）。
func extractProblems(records []model.WorkRecord) []string {
	var problems []string
	seen := make(map[string]bool)
	for _, rec := range records {
		if rec.IsHidden || rec.RecordType == model.TypeCommit {
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
				// access_token 过期/缺失时，尝试用 refresh_token 自动续期，避免被迫重新登录
				if rt, hasRT := c.store.GetRefreshToken(req.UserID); hasRT {
					if info, err := c.larkAdapter.RefreshToken(ctx, rt); err == nil && info.AccessToken != "" {
						c.store.SaveTokenFull(req.UserID, info.AccessToken, info.RefreshToken, time.Duration(info.ExpiresIn)*time.Second)
						token, ok = info.AccessToken, true
					}
				}
			}
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
			meta, _ := json.Marshal(map[string]int{"additions": commit.Stats.Additions, "deletions": commit.Stats.Deletions})
			records = append(records, model.WorkRecord{
				SourceType:  "gitlab",
				ExternalID:  commit.ID,
				RecordType:  model.TypeCommit,
				Title:       cleanCommitSubject(commit.Message),
				ProjectName: cfg.ProjectPath,
				URL:         commit.WebURL,
				Metadata:    string(meta),
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
			// 列表接口不含 stats，单独拉取增删行数与网页链接（失败时降级为 0 与拼接的提交链接）
			add, del, htmlURL, serr := source.FetchCommitStats(ctx, commit.SHA)
			if serr != nil || htmlURL == "" {
				htmlURL = fmt.Sprintf("https://github.com/%s/%s/commit/%s", cfg.Owner, cfg.Repo, commit.SHA)
			}
			meta, _ := json.Marshal(map[string]int{"additions": add, "deletions": del})
			records = append(records, model.WorkRecord{
				SourceType:  "github",
				ExternalID:  commit.SHA,
				RecordType:  model.TypeCommit,
				Title:       cleanCommitSubject(commit.Commit.Message),
				ProjectName: fmt.Sprintf("%s/%s", cfg.Owner, cfg.Repo),
				URL:         htmlURL,
				Metadata:    string(meta),
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

// mdLink 当 url 非空时把标题渲染为 markdown 链接。
func mdLink(title, url string) string {
	if url == "" {
		return title
	}
	return "[" + title + "](" + url + ")"
}

// commitStats 从 WorkRecord.Metadata(JSON) 解析提交增删行数。
func commitStats(metadata string) (additions, deletions int) {
	if metadata == "" {
		return 0, 0
	}
	var m struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	}
	_ = json.Unmarshal([]byte(metadata), &m)
	return m.Additions, m.Deletions
}

func renderMarkdownFallback(r *model.WeeklyReport, records []model.WorkRecord, nextWeekEvents []model.WorkRecord) string {

	md := fmt.Sprintf("## 本周完成工作 (%s ~ %s)\n\n", r.WeekStart, r.WeekEnd)

	var tasks, commits, meetings, docs []model.WorkRecord
	for _, rec := range records {
		if rec.IsHidden {
			continue
		}
		switch rec.RecordType {
		case model.TypeCommit:
			commits = append(commits, rec)
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
			md += fmt.Sprintf("- [x] %s\n", mdLink(rec.Title, rec.URL))
			if rec.Description != "" {
				md += fmt.Sprintf("  - %s\n", rec.Description)
			}
		}
		md += "\n"
	}

	if len(commits) > 0 {
		// 先累计本周提交增删行数，用于分类标题下的汇总行
		totalAdd, totalDel := 0, 0
		for _, rec := range commits {
			a, d := commitStats(rec.Metadata)
			totalAdd += a
			totalDel += d
		}
		md += "### 代码提交\n\n"
		if totalAdd > 0 || totalDel > 0 {
			md += fmt.Sprintf("> 本周共 %d 次提交，+%d / -%d 行\n", len(commits), totalAdd, totalDel)
		}
		for _, rec := range commits {
			// 标题已在采集时清洗为 commit 首行，这里防御性再取一次首行。
			title := rec.Title
			if idx := strings.IndexAny(title, "\r\n"); idx >= 0 {
				title = title[:idx]
			}
			line := fmt.Sprintf("- 💻 %s", mdLink(title, rec.URL))
			if rec.ProjectName != "" {
				line += fmt.Sprintf(" @%s", rec.ProjectName)
			}
			if !rec.OccurredAt.IsZero() {
				line += fmt.Sprintf("（%s）", rec.OccurredAt.Format("01-02"))
			}
			if a, d := commitStats(rec.Metadata); a > 0 || d > 0 {
				line += fmt.Sprintf(" (+%d/-%d)", a, d)
			}
			md += line + "\n"
		}
		md += "\n"
	}

	if len(meetings) > 0 {
		md += "### 会议/日程\n\n"
		for _, rec := range meetings {
			md += fmt.Sprintf("- 🗓️ %s (%s)\n", mdLink(rec.Title, rec.URL), rec.OccurredAt.Format("01-02 15:04"))
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
			md += fmt.Sprintf("- 📝 %s (%s)\n", mdLink(rec.Title, rec.URL), rec.OccurredAt.Format("01-02"))
		}
		md += "\n"
	}

	if len(tasks) == 0 && len(commits) == 0 && len(meetings) == 0 && len(docs) == 0 {
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
		// 按类型拆分：任务 / 日程，分别列出
		var nextTasks, nextMeetings []model.WorkRecord
		for _, rec := range nextWeekEvents {
			if rec.RecordType == model.TypeMeeting {
				nextMeetings = append(nextMeetings, rec)
			} else {
				nextTasks = append(nextTasks, rec)
			}
		}
		if len(nextTasks) > 0 {
			md += "### 待办任务\n\n"
			for _, rec := range nextTasks {
				line := fmt.Sprintf("- [ ] 📋 %s", mdLink(rec.Title, rec.URL))
				if !rec.OccurredAt.IsZero() {
					line += fmt.Sprintf("（截止 %s）", rec.OccurredAt.Format("01-02 15:04"))
				}
				md += line + "\n"
			}
			md += "\n"
		}
		if len(nextMeetings) > 0 {
			md += "### 日程安排\n\n"
			for _, rec := range nextMeetings {
				line := fmt.Sprintf("- [ ] 📅 %s", mdLink(rec.Title, rec.URL))
				if !rec.OccurredAt.IsZero() {
					line += fmt.Sprintf("（%s）", rec.OccurredAt.Format("01-02 15:04"))
				}
				md += line + "\n"
				if rec.ProjectName != "" {
					md += fmt.Sprintf("  - 📍 地点：%s\n", rec.ProjectName)
				}
			}
			md += "\n"
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
	var tasks, commits, meetings, docs []model.TemplateItem
	totalAdd, totalDel := 0, 0 // 本周代码增删行数合计
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
		case model.TypeCommit:
			// 从 Metadata(JSON) 解析增删行数，并累计到本周合计
			a, d := commitStats(rec.Metadata)
			item.Additions = a
			item.Deletions = d
			totalAdd += a
			totalDel += d
			commits = append(commits, item)
		case model.TypeMeeting:
			meetings = append(meetings, item)
		case model.TypeDoc:
			docs = append(docs, item)
		default:
			tasks = append(tasks, item)
		}
	}

	var nextItems []model.TemplateItem
	var nextTasks []model.TemplateItem
	var nextMeetings []model.TemplateItem
	for _, rec := range nextWeek {
		item := model.TemplateItem{
			Title:       rec.Title,
			Description: rec.Description,
			ProjectName: rec.ProjectName,
			URL:         rec.URL,
			OccurredAt:  rec.OccurredAt,
		}
		if !rec.OccurredAt.IsZero() {
			item.OccurredDate = rec.OccurredAt.Format("01-02 15:04")
		}
		nextItems = append(nextItems, item)
		// 按记录类型拆分，便于模板分别列出"任务"与"日程"
		switch rec.RecordType {
		case model.TypeMeeting:
			nextMeetings = append(nextMeetings, item)
		default:
			nextTasks = append(nextTasks, item)
		}
	}

	problems := extractProblems(records)

	return model.ReportTemplateData{
		WeekStart:           report.WeekStart,
		WeekEnd:             report.WeekEnd,
		WeekRange:           report.WeekStart + " ~ " + report.WeekEnd,
		Tasks:               tasks,
		Commits:             commits,
		Meetings:            meetings,
		Docs:                docs,
		NextWeekEvents:      nextItems,
		NextWeekTasks:       nextTasks,
		NextWeekMeetings:    nextMeetings,
		Problems:            problems,
		TaskCount:           len(tasks),
		CommitCount:         len(commits),
		MeetingCount:        len(meetings),
		DocCount:            len(docs),
		NextWeekCount:       len(nextItems),
		CommitAdditions:     totalAdd,
		CommitDeletions:     totalDel,
		HasTasks:            len(tasks) > 0,
		HasCommits:          len(commits) > 0,
		HasMeetings:         len(meetings) > 0,
		HasDocs:             len(docs) > 0,
		HasNextWeek:         len(nextItems) > 0,
		HasNextWeekTasks:    len(nextTasks) > 0,
		HasNextWeekMeetings: len(nextMeetings) > 0,
		HasProblems:         len(problems) > 0,
	}
}
