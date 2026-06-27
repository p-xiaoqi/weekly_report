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

// GitHubCommit GitHub 提交记录
type GitHubCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

// GitHubSource GitHub 数据源实现
type GitHubSource struct {
	Token string
	Owner string
	Repo  string
}

// FetchCommits 分页拉取本周提交。
// author 为可选的作者过滤条件：仅当非空时才通过 GitHub API 的 author 参数按作者过滤，
// 为空（默认）时返回时间窗口内的全部提交，避免因未配置/错配作者而漏掉提交。
func (g *GitHubSource) FetchCommits(ctx context.Context, author string, weekStart, weekEnd time.Time) ([]GitHubCommit, error) {
	var allCommits []GitHubCommit
	page := 1
	for {
		apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?since=%s&until=%s&per_page=100&page=%d",
			g.Owner, g.Repo, weekStart.Format(time.RFC3339), weekEnd.Format(time.RFC3339), page)
		if author != "" {
			apiURL += "&author=" + url.QueryEscape(author)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+g.Token)
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("GitHub API 返回状态码: %d", resp.StatusCode)
		}

		var commits []GitHubCommit
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

// TestConnection 测试 GitHub 连接
func (g *GitHubSource) TestConnection(ctx context.Context) error {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", g.Owner, g.Repo)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub API 返回状态码: %d", resp.StatusCode)
	}
	return nil
}
