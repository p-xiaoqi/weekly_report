package git

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitLabCommit GitLab 提交记录
type GitLabCommit struct {
	ID         string `json:"id"`
	Message    string `json:"message"`
	AuthorName string `json:"author_name"`
	CreatedAt  string `json:"created_at"`
	WebURL     string `json:"web_url"`
	Stats      struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	} `json:"stats"`
}

// GitLabSource GitLab 数据源实现
type GitLabSource struct {
	Token       string
	ServerURL   string
	ProjectPath string
}

// FetchCommits 分页拉取本周提交（修复 PRD 分页遗漏风险）
func (g *GitLabSource) FetchCommits(ctx context.Context, authorEmail string, weekStart, weekEnd time.Time) ([]GitLabCommit, error) {
	baseURL := g.ServerURL
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	encodedPath := url.PathEscape(g.ProjectPath)

	var allCommits []GitLabCommit
	page := 1
	for {
		apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/commits?since=%s&until=%s&per_page=100&page=%d&with_stats=true",
			baseURL, encodedPath, weekStart.Format(time.RFC3339), weekEnd.Format(time.RFC3339), page)
		if authorEmail != "" {
			apiURL += "&author_email=" + url.QueryEscape(authorEmail)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", g.Token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, gitlabError(resp.StatusCode, body, g.ProjectPath)
		}

		var commits []GitLabCommit
		if err := json.Unmarshal(body, &commits); err != nil {
			return nil, err
		}
		if len(commits) == 0 {
			break
		}
		allCommits = append(allCommits, commits...)
		if len(commits) < 100 {
			break
		}
		page++
	}
	return allCommits, nil
}

// TestConnection 测试 GitLab 连接
func (g *GitLabSource) TestConnection(ctx context.Context) error {
	baseURL := g.ServerURL
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s", baseURL, url.PathEscape(g.ProjectPath))
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", g.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return gitlabError(resp.StatusCode, body, g.ProjectPath)
	}
	return nil
}

// gitlabError 把 GitLab API 的非 200 状态码翻译成可操作的中文提示，
// 并附带 GitLab 返回体里的 message/error，便于用户快速定位（尤其是高频的 401/403/404）。
func gitlabError(status int, body []byte, projectPath string) error {
	var detail struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &detail)
	apiMsg := strings.TrimSpace(detail.Message)
	if apiMsg == "" {
		apiMsg = strings.TrimSpace(detail.Error)
	}

	var hint string
	switch status {
	case http.StatusUnauthorized: // 401
		hint = "鉴权失败：PRIVATE-TOKEN 无效/过期/填写有误（确认是有效的 GitLab Access Token，且未多带空格或引号）"
	case http.StatusForbidden: // 403
		hint = "被拒绝：token 缺少访问该项目的权限（至少需要 read_api / read_repository scope）"
	case http.StatusNotFound: // 404
		hint = fmt.Sprintf("项目不存在或无权访问：请确认 project_path（当前 %s）与 server_url 正确；私有项目 token 需有读权限", projectPath)
	default:
		hint = "请求未成功"
	}
	if apiMsg != "" {
		return fmt.Errorf("GitLab API %d - %s（GitLab 返回：%s）", status, hint, apiMsg)
	}
	return fmt.Errorf("GitLab API %d - %s", status, hint)
}
