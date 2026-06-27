package git

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// GitLabCommit GitLab 提交记录
type GitLabCommit struct {
	ID         string `json:"id"`
	Message    string `json:"message"`
	AuthorName string `json:"author_name"`
	CreatedAt  string `json:"created_at"`
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
		apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/commits?since=%s&until=%s&per_page=100&page=%d",
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
			return nil, fmt.Errorf("GitLab API 返回状态码: %d", resp.StatusCode)
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
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitLab API 返回状态码: %d", resp.StatusCode)
	}
	return nil
}
