package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

type Client struct {
	AppID      string
	AppSecret  string
	httpClient *http.Client

	tenantToken string
	tokenExpire time.Time
	mu          sync.RWMutex
}

func NewClient(appID, appSecret string) *Client {
	return &Client{
		AppID:      appID,
		AppSecret:  appSecret,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SendMessage 使用应用身份（tenant_access_token）通过 im/v1/messages 发送文本消息。
// receiveIDType 取值：open_id / user_id / union_id / email / chat_id。
// 这是 webhook 自定义机器人之外的另一种通知方式，靠 App ID + App Secret 鉴权，
// 无需"加签"，且可定向发给指定用户或群。
func (c *Client) SendMessage(ctx context.Context, receiveIDType, receiveID, text string) error {
	token, err := c.getTenantToken(ctx)
	if err != nil {
		return fmt.Errorf("获取 tenant_access_token 失败（请检查 FEISHU_APP_ID/FEISHU_APP_SECRET）: %w", err)
	}

	// content 字段要求是 JSON 字符串
	contentBytes, _ := json.Marshal(map[string]string{"text": text})
	reqBody := map[string]string{
		"receive_id": receiveID,
		"msg_type":   "text",
		"content":    string(contentBytes),
	}
	data, _ := json.Marshal(reqBody)

	url := "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=" + receiveIDType
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("飞书发送消息响应无法解析(HTTP %d): %s", resp.StatusCode, string(body))
	}
	if result.Code != 0 {
		hint := ""
		switch result.Code {
		case 99991663, 99991661, 99991664:
			hint = "（tenant_access_token 无效/过期，请检查 App ID/Secret）"
		case 230002, 230013:
			hint = "（接收方 open_id 与本应用不匹配，open_id 按应用隔离，请用本应用 /api/v1/auth/status 返回的 user_id）"
		case 9499:
			hint = "（缺少权限，请在开放平台为应用开通 im:message 发送消息权限并发布版本）"
		case 230001:
			hint = "（接收方未与机器人建立会话或机器人不可用，请确认应用已发布且接收人可用该应用）"
		}
		return fmt.Errorf("飞书发送消息失败 code=%d msg=%s%s", result.Code, result.Msg, hint)
	}
	return nil
}

// ChatInfo 群聊信息（用于让用户选择通知目标群）。
type ChatInfo struct {
	ChatID      string `json:"chat_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Avatar      string `json:"avatar"`
}

// ListUserChats 拉取授权用户所在的群列表（chat_id + 名称）。
// 传入 user_access_token 时返回该用户加入的群；为空则回退 tenant_access_token（返回机器人所在群）。
func (c *Client) ListUserChats(ctx context.Context, userAccessToken string) ([]ChatInfo, error) {
	token := userAccessToken
	if token == "" {
		var err error
		token, err = c.getTenantToken(ctx)
		if err != nil {
			return nil, err
		}
	}

	var all []ChatInfo
	pageToken := ""
	for {
		url := "https://open.feishu.cn/open-apis/im/v1/chats?page_size=100"
		if pageToken != "" {
			url += "&page_token=" + pageToken
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var result struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Items     []ChatInfo `json:"items"`
				PageToken string     `json:"page_token"`
				HasMore   bool       `json:"has_more"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("群列表响应无法解析(HTTP %d): %s", resp.StatusCode, string(body))
		}
		if result.Code != 0 {
			hint := ""
			switch result.Code {
			case 99991663, 99991661, 99991664:
				hint = "（飞书登录态已过期，请重新登录后重试）"
			case 9499:
				hint = "（应用缺少 im:chat 权限，请在开放平台开通并发布版本）"
			}
			return nil, fmt.Errorf("获取群列表失败 code=%d msg=%s%s", result.Code, result.Msg, hint)
		}
		all = append(all, result.Data.Items...)
		if !result.Data.HasMore {
			break
		}
		pageToken = result.Data.PageToken
	}
	return all, nil
}

func (c *Client) getTenantToken(ctx context.Context) (string, error) {

	c.mu.RLock()
	if c.tenantToken != "" && time.Now().Before(c.tokenExpire.Add(-5*time.Minute)) {
		t := c.tenantToken
		c.mu.RUnlock()
		return t, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.tenantToken != "" && time.Now().Before(c.tokenExpire.Add(-5*time.Minute)) {
		return c.tenantToken, nil
	}

	reqBody := map[string]string{
		"app_id":     c.AppID,
		"app_secret": c.AppSecret,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal",
		bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	if result.Code != 0 {
		return "", fmt.Errorf("get tenant token failed: %s", result.Msg)
	}

	c.tenantToken = result.TenantAccessToken
	c.tokenExpire = time.Now().Add(time.Duration(result.Expire) * time.Second)

	return c.tenantToken, nil
}

func (c *Client) GetUserAccessToken(ctx context.Context, code string) (*UserTokenInfo, error) {
	reqBody := map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
		"app_id":     c.AppID,
		"app_secret": c.AppSecret,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://open.feishu.cn/open-apis/authen/v1/access_token",
		bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken     string `json:"access_token"`
			RefreshToken    string `json:"refresh_token"`
			ExpiresIn       int    `json:"expires_in"`
			UserID          string `json:"user_id"`
			OpenID          string `json:"open_id"`
			Name            string `json:"name"`
			Email           string `json:"email"`
			EnterpriseEmail string `json:"enterprise_email"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("get user token failed: code=%d, msg=%s", result.Code, result.Msg)
	}

	email := result.Data.EnterpriseEmail
	if email == "" {
		email = result.Data.Email
	}

	return &UserTokenInfo{
		AccessToken:  result.Data.AccessToken,
		RefreshToken: result.Data.RefreshToken,
		ExpiresIn:    result.Data.ExpiresIn,
		UserID:       result.Data.UserID,
		OpenID:       result.Data.OpenID,
		Name:         result.Data.Name,
		Email:        email,
	}, nil
}

func (c *Client) FetchUserTasks(ctx context.Context, userAccessToken string) ([]Task, error) {
	token := userAccessToken
	if token == "" {
		var err error
		token, err = c.getTenantToken(ctx)
		if err != nil {
			return nil, err
		}
	}

	var allTasks []Task
	pageToken := ""

	for {
		url := "https://open.feishu.cn/open-apis/task/v2/tasks?page_size=100"
		if pageToken != "" {
			url += "&page_token=" + pageToken
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		log.Printf("[DEBUG] FetchUserTasks raw response: %s", string(body))

		var result struct {
			Code int `json:"code"`
			Data struct {
				Items     []Task `json:"items"`
				PageToken string `json:"page_token"`
				HasMore   bool   `json:"has_more"`
			} `json:"data"`
		}

		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}

		if result.Code != 0 {
			return nil, fmt.Errorf("fetch tasks failed: code=%d", result.Code)
		}

		log.Printf("[DEBUG] FetchUserTasks parsed %d items, has_more=%v", len(result.Data.Items), result.Data.HasMore)
		allTasks = append(allTasks, result.Data.Items...)

		if !result.Data.HasMore {
			break
		}
		pageToken = result.Data.PageToken
	}

	return allTasks, nil
}

func (c *Client) FetchUserCalendarEvents(ctx context.Context, userAccessToken string, startTime, endTime time.Time) ([]CalendarEvent, error) {
	token := userAccessToken
	if token == "" {
		var err error
		token, err = c.getTenantToken(ctx)
		if err != nil {
			return nil, err
		}
	}

	var allEvents []CalendarEvent
	pageToken := ""

	startTs := startTime.Unix()
	endTs := endTime.Unix()

	for {
		url := fmt.Sprintf("https://open.feishu.cn/open-apis/calendar/v4/calendars/primary/events?page_size=100&start_time=%d&end_time=%d", startTs, endTs)
		if pageToken != "" {
			url += "&page_token=" + pageToken
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		log.Printf("[DEBUG] FetchUserCalendarEvents raw response: %s", string(body))

		var result struct {
			Code int `json:"code"`
			Data struct {
				Items     []CalendarEvent `json:"items"`
				PageToken string          `json:"page_token"`
				HasMore   bool            `json:"has_more"`
			} `json:"data"`
		}

		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}

		if result.Code != 0 {
			return nil, fmt.Errorf("fetch calendar events failed: code=%d", result.Code)
		}

		log.Printf("[DEBUG] FetchUserCalendarEvents parsed %d items, has_more=%v", len(result.Data.Items), result.Data.HasMore)
		allEvents = append(allEvents, result.Data.Items...)

		if !result.Data.HasMore {
			break
		}
		pageToken = result.Data.PageToken
	}

	return allEvents, nil
}

type UserTokenInfo struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	UserID       string
	OpenID       string
	Name         string
	Email        string
}

type Task struct {
	GUID          string `json:"guid"`
	Summary       string `json:"summary"`
	Notes         string `json:"notes"`
	Completed     bool   `json:"completed"`
	CompletedTime string `json:"completed_time"`
	CompletedAt   string `json:"completed_at"`
	Status        string `json:"status"`
	CreateTime    string `json:"create_time"`
	UpdateTime    string `json:"update_time"`
	DueTime       string `json:"due_time"`
	Priority      int    `json:"priority"`
	TopicGUID     string `json:"topic_guid"`
	TopicName     string `json:"topic_name"`
}

type CalendarEvent struct {
	EventID     string `json:"event_id"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	StartTime   struct {
		Timestamp string `json:"timestamp"`
	} `json:"start_time"`
	EndTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"end_time"`
	Location struct {
		Name string `json:"name"`
	} `json:"location"`
	Organizer struct {
		UserID string `json:"user_id"`
	} `json:"organizer"`
	Status string `json:"status"`
}

type Doc struct {
	Token        string
	Name         string
	Type         string
	CreateTime   string
	CreatedTime  string
	UpdateTime   string
	ModifiedTime string
	OwnerID      string
	URL          string
}

func (c *Client) FetchUserDocs(ctx context.Context, userAccessToken string) ([]Doc, error) {
	token := userAccessToken
	if token == "" {
		var err error
		token, err = c.getTenantToken(ctx)
		if err != nil {
			return nil, err
		}
	}

	var allDocs []Doc
	visited := make(map[string]bool)
	// 递归获取 root 及其子文件夹，最多3层
	err := c.fetchDocsRecursively(ctx, token, "root", 0, 3, visited, &allDocs)
	if err != nil {
		return nil, err
	}
	return allDocs, nil
}

func (c *Client) fetchDocsRecursively(ctx context.Context, token, folderToken string, depth, maxDepth int, visited map[string]bool, allDocs *[]Doc) error {
	if depth > maxDepth {
		return nil
	}
	if visited[folderToken] {
		return nil
	}
	visited[folderToken] = true

	pageToken := ""
	for {
		urlStr := "https://open.feishu.cn/open-apis/drive/v1/files?page_size=100"
		if folderToken != "" && folderToken != "root" {
			urlStr += "&folder_token=" + folderToken
		}
		if pageToken != "" {
			urlStr += "&page_token=" + pageToken
		}

		req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		log.Printf("[DEBUG] FetchUserDocs folder_token=%s depth=%d raw response: %s", folderToken, depth, string(body))

		var result struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Files []struct {
					Token        string `json:"token"`
					Name         string `json:"name"`
					Type         string `json:"type"`
					CreatedTime  string `json:"created_time"`
					ModifiedTime string `json:"modified_time"`
					OwnerID      string `json:"owner_id"`
					URL          string `json:"url"`
				} `json:"files"`
				Items []struct {
					Token        string `json:"token"`
					Name         string `json:"name"`
					Type         string `json:"type"`
					CreatedTime  string `json:"created_time"`
					ModifiedTime string `json:"modified_time"`
					OwnerID      string `json:"owner_id"`
					URL          string `json:"url"`
				} `json:"items"`
				PageToken string `json:"page_token"`
				HasMore   bool   `json:"has_more"`
			} `json:"data"`
		}

		if err := json.Unmarshal(body, &result); err != nil {
			return err
		}

		if result.Code != 0 {
			return fmt.Errorf("fetch docs failed: code=%d, msg=%s", result.Code, result.Msg)
		}

		files := result.Data.Files
		if len(files) == 0 {
			files = make([]struct {
				Token        string `json:"token"`
				Name         string `json:"name"`
				Type         string `json:"type"`
				CreatedTime  string `json:"created_time"`
				ModifiedTime string `json:"modified_time"`
				OwnerID      string `json:"owner_id"`
				URL          string `json:"url"`
			}, 0, len(result.Data.Items))
			for _, f := range result.Data.Items {
				files = append(files, struct {
					Token        string `json:"token"`
					Name         string `json:"name"`
					Type         string `json:"type"`
					CreatedTime  string `json:"created_time"`
					ModifiedTime string `json:"modified_time"`
					OwnerID      string `json:"owner_id"`
					URL          string `json:"url"`
				}{
					Token:        f.Token,
					Name:         f.Name,
					Type:         f.Type,
					CreatedTime:  f.CreatedTime,
					ModifiedTime: f.ModifiedTime,
					OwnerID:      f.OwnerID,
					URL:          f.URL,
				})
			}
		}

		for _, f := range files {
			if f.Type == "folder" {
				// 递归获取子文件夹
				err := c.fetchDocsRecursively(ctx, token, f.Token, depth+1, maxDepth, visited, allDocs)
				if err != nil {
					log.Printf("[WARN] 获取子文件夹 %s 失败: %v", f.Name, err)
				}
			} else {
				*allDocs = append(*allDocs, Doc{
					Token:        f.Token,
					Name:         f.Name,
					Type:         f.Type,
					CreatedTime:  f.CreatedTime,
					ModifiedTime: f.ModifiedTime,
					OwnerID:      f.OwnerID,
					URL:          f.URL,
				})
			}
		}

		if !result.Data.HasMore {
			break
		}
		pageToken = result.Data.PageToken
	}

	return nil
}
