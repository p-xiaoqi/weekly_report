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
	HTMLURL string `json:"html_url"`
	Stats   struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
		Total     int `json:"total"`
	} `json:"stats"`
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
			return nil, githubError(resp.StatusCode, body, g.Owner, g.Repo)
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

// FetchCommitStats 获取单个提交的增删行数及网页链接。
// 列表接口不返回 stats，需单独 GET 提交详情接口。
func (g *GitHubSource) FetchCommitStats(ctx context.Context, sha string) (additions, deletions int, htmlURL string, err error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", g.Owner, g.Repo, sha)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return 0, 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, "", err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, 0, "", githubError(resp.StatusCode, body, g.Owner, g.Repo)
	}
	var detail GitHubCommit
	if err := json.Unmarshal(body, &detail); err != nil {
		return 0, 0, "", err
	}
	return detail.Stats.Additions, detail.Stats.Deletions, detail.HTMLURL, nil
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
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return githubError(resp.StatusCode, body, g.Owner, g.Repo)
	}
	return nil
}

// githubError 把 GitHub API 的非 200 状态码翻译成可操作的中文提示，
// 并附带 GitHub 返回体里的 message，便于用户快速定位（尤其是高频的 401/403/404）。
func githubError(status int, body []byte, owner, repo string) error {
	var detail struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &detail)
	apiMsg := strings.TrimSpace(detail.Message)

	var hint string
	switch status {
	case http.StatusUnauthorized: // 401
		hint = "鉴权失败：token 无效/过期/填写有误（检查是否多了空格或引号、是否已过期、是否为正确的 GitHub PAT）"
	case http.StatusForbidden: // 403
		hint = "被拒绝：通常是触发了 API 限流（未带 token 时每小时仅 60 次），或 token 缺少访问该仓库的权限"
	case http.StatusNotFound: // 404
		hint = fmt.Sprintf("仓库不存在或无权访问：请确认 owner/repo（当前 %s/%s）正确；若为私有仓库，token 需具备 repo 读权限", owner, repo)
	case http.StatusUnprocessableEntity: // 422
		hint = "参数有误：常见于 author 过滤值不是有效的 GitHub 用户"
	default:
		hint = "请求未成功"
	}
	if apiMsg != "" {
		return fmt.Errorf("GitHub API %d - %s（GitHub 返回：%s）", status, hint, apiMsg)
	}
	return fmt.Errorf("GitHub API %d - %s", status, hint)
}
