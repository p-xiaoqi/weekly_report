package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
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
// query 非空时调用搜索接口（按群名模糊匹配），便于群很多时定位；为空则列全部。
// 传入 user_access_token 时返回该用户加入的群；为空则回退 tenant_access_token（返回机器人所在群）。
func (c *Client) ListUserChats(ctx context.Context, userAccessToken, query string) ([]ChatInfo, error) {
	token := userAccessToken
	if token == "" {
		var err error
		token, err = c.getTenantToken(ctx)
		if err != nil {
			return nil, err
		}
	}

	base := "https://open.feishu.cn/open-apis/im/v1/chats"
	if strings.TrimSpace(query) != "" {
		base = "https://open.feishu.cn/open-apis/im/v1/chats/search?query=" + url.QueryEscape(query)
	}

	var all []ChatInfo
	pageToken := ""
	for {
		u := base
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + "page_size=100"
		if pageToken != "" {
			u += "&page_token=" + pageToken
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
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

// RefreshUserAccessToken 用 refresh_token 续期 user_access_token。

// 失败时返回错误（常见 20007: refresh_token 过期，需要用户重新授权登录）。

func (c *Client) RefreshUserAccessToken(ctx context.Context, refreshToken string) (*UserTokenInfo, error) {

	appToken, err := c.getTenantToken(ctx)

	if err != nil {

		return nil, err

	}

	reqBody := map[string]string{

		"grant_type": "refresh_token",

		"refresh_token": refreshToken,
	}

	data, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST",

		"https://open.feishu.cn/open-apis/authen/v1/refresh_access_token",

		bytes.NewReader(data))

	if err != nil {

		return nil, err

	}

	req.Header.Set("Content-Type", "application/json")

	req.Header.Set("Authorization", "Bearer "+appToken)

	resp, err := c.httpClient.Do(req)

	if err != nil {

		return nil, err

	}

	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Code int `json:"code"`

		Msg string `json:"msg"`

		Data struct {
			AccessToken string `json:"access_token"`

			RefreshToken string `json:"refresh_token"`

			ExpiresIn int `json:"expires_in"`

			OpenID string `json:"open_id"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {

		return nil, fmt.Errorf("刷新 token 响应无法解析(HTTP %d): %s", resp.StatusCode, string(body))

	}

	if result.Code != 0 {

		return nil, fmt.Errorf("refresh user token failed: code=%d, msg=%s", result.Code, result.Msg)

	}

	return &UserTokenInfo{

		AccessToken: result.Data.AccessToken,

		RefreshToken: result.Data.RefreshToken,

		ExpiresIn: result.Data.ExpiresIn,

		OpenID: result.Data.OpenID,
	}, nil

}

// SendCard 用应用身份发送 interactive 卡片消息。card 为飞书卡片 JSON 结构。

func (c *Client) SendCard(ctx context.Context, receiveIDType, receiveID string, card interface{}) error {

	token, err := c.getTenantToken(ctx)

	if err != nil {

		return fmt.Errorf("获取 tenant_access_token 失败: %w", err)

	}

	cardBytes, _ := json.Marshal(card)

	reqBody := map[string]string{

		"receive_id": receiveID,

		"msg_type": "interactive",

		"content": string(cardBytes),
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
		Code int `json:"code"`

		Msg string `json:"msg"`
	}

	if err := json.Unmarshal(body, &result); err != nil {

		return fmt.Errorf("飞书发送卡片响应无法解析(HTTP %d): %s", resp.StatusCode, string(body))

	}

	if result.Code != 0 {

		return fmt.Errorf("飞书发送卡片失败 code=%d msg=%s", result.Code, result.Msg)

	}

	return nil

}

// BuildReminderCard 构造"周报提醒"交互卡片，附带"立即填写"跳转按钮。

func BuildReminderCard(title, content, buttonText, url string) map[string]interface{} {

	elements := []interface{}{

		map[string]interface{}{

			"tag": "div",

			"text": map[string]interface{}{

				"tag": "lark_md",

				"content": content,
			},
		},
	}

	if url != "" {

		if buttonText == "" {

			buttonText = "立即填写"

		}

		elements = append(elements, map[string]interface{}{

			"tag": "action",

			"actions": []interface{}{

				map[string]interface{}{

					"tag": "button",

					"text": map[string]interface{}{"tag": "plain_text", "content": buttonText},

					"type": "primary",

					"url": url,
				},
			},
		})

	}

	return map[string]interface{}{

		"config": map[string]interface{}{"wide_screen_mode": true},

		"header": map[string]interface{}{

			"template": "orange",

			"title": map[string]interface{}{"tag": "plain_text", "content": title},
		},

		"elements": elements,
	}

}

// CreateDocWithText 用应用身份创建一篇飞书云文档，并把 markdown 转成多种 docx 块写入。
// 创建失败返回错误；写入正文失败时仍返回文档链接（best-effort）。
func (c *Client) CreateDocWithText(ctx context.Context, title, markdown string) (docURL string, err error) {
	token, err := c.getTenantToken(ctx)
	if err != nil {
		return "", err
	}

	// 1) 创建空文档
	createBody, _ := json.Marshal(map[string]string{"title": title})
	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://open.feishu.cn/open-apis/docx/v1/documents", bytes.NewReader(createBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var createResult struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Document struct {
				DocumentID string `json:"document_id"`
			} `json:"document"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &createResult); err != nil {
		return "", fmt.Errorf("创建飞书文档响应无法解析(HTTP %d): %s", resp.StatusCode, string(body))
	}
	if createResult.Code != 0 || createResult.Data.Document.DocumentID == "" {
		return "", fmt.Errorf("创建飞书文档失败 code=%d msg=%s", createResult.Code, createResult.Msg)
	}
	documentID := createResult.Data.Document.DocumentID
	docURL = "https://feishu.cn/docx/" + documentID

	// 2) markdown 转 docx 块并分批插入（每批 <=50），失败仅记录日志、仍返回链接
	blocks := markdownToDocxBlocks(markdown)
	childURL := fmt.Sprintf("https://open.feishu.cn/open-apis/docx/v1/documents/%s/blocks/%s/children", documentID, documentID)
	index := 0
	for start := 0; start < len(blocks); start += 50 {
		end := start + 50
		if end > len(blocks) {
			end = len(blocks)
		}
		batch := blocks[start:end]
		childBody, _ := json.Marshal(map[string]interface{}{"index": index, "children": batch})
		creq, cerr := http.NewRequestWithContext(ctx, "POST", childURL, bytes.NewReader(childBody))
		if cerr != nil {
			log.Printf("[WARN] 构造飞书文档块请求失败: %v", cerr)
			break
		}
		creq.Header.Set("Content-Type", "application/json; charset=utf-8")
		creq.Header.Set("Authorization", "Bearer "+token)
		cresp, cerr := c.httpClient.Do(creq)
		if cerr != nil {
			log.Printf("[WARN] 写入飞书文档块失败: %v", cerr)
			break
		}
		cbody, _ := io.ReadAll(cresp.Body)
		cresp.Body.Close()
		var cres struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if jerr := json.Unmarshal(cbody, &cres); jerr != nil || cres.Code != 0 {
			log.Printf("[WARN] 写入飞书文档块失败 code=%d msg=%s body=%s", cres.Code, cres.Msg, string(cbody))
			break
		}
		index += len(batch)
	}
	return docURL, nil
}

// markdownToDocxBlocks 把 markdown 逐行转换为 docx 块（标题/待办/列表/引用/段落）。
func markdownToDocxBlocks(md string) []map[string]interface{} {
	var blocks []map[string]interface{}
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == "" {
			continue // 跳过空行
		}
		switch {
		case strings.HasPrefix(trimmed, "#### "):
			blocks = append(blocks, textBlock(6, "heading4", mdInlineToElements(trimmed[5:]), nil))
		case strings.HasPrefix(trimmed, "### "):
			blocks = append(blocks, textBlock(5, "heading3", mdInlineToElements(trimmed[4:]), nil))
		case strings.HasPrefix(trimmed, "## "):
			blocks = append(blocks, textBlock(4, "heading2", mdInlineToElements(trimmed[3:]), nil))
		case strings.HasPrefix(trimmed, "# "):
			blocks = append(blocks, textBlock(3, "heading1", mdInlineToElements(trimmed[2:]), nil))
		case strings.HasPrefix(trimmed, "- [ ] "):
			blocks = append(blocks, textBlock(17, "todo", mdInlineToElements(trimmed[6:]), map[string]interface{}{"done": false}))
		case strings.HasPrefix(trimmed, "- [x] "), strings.HasPrefix(trimmed, "- [X] "):
			blocks = append(blocks, textBlock(17, "todo", mdInlineToElements(trimmed[6:]), map[string]interface{}{"done": true}))
		case strings.HasPrefix(trimmed, "- "):
			blocks = append(blocks, textBlock(12, "bullet", mdInlineToElements(trimmed[2:]), nil))
		case strings.HasPrefix(trimmed, "* "):
			blocks = append(blocks, textBlock(12, "bullet", mdInlineToElements(trimmed[2:]), nil))
		case strings.HasPrefix(trimmed, "> "):
			blocks = append(blocks, textBlock(2, "text", mdInlineToElements("▎ "+trimmed[2:]), nil))
		default:
			blocks = append(blocks, textBlock(2, "text", mdInlineToElements(trimmed), nil))
		}
	}
	return blocks
}

// textBlock 构造一个 docx 块：block_type + 字段键（heading/todo/bullet/text 等）。
func textBlock(blockType int, key string, elements []map[string]interface{}, extraStyle map[string]interface{}) map[string]interface{} {
	style := map[string]interface{}{}
	for k, v := range extraStyle {
		style[k] = v
	}
	return map[string]interface{}{
		"block_type": blockType,
		key: map[string]interface{}{
			"elements": elements,
			"style":    style,
		},
	}
}

// mdInlineToElements 解析行内 markdown：链接 [label](url) 与加粗 **x**，其余作为纯文本。
func mdInlineToElements(s string) []map[string]interface{} {
	var elements []map[string]interface{}
	for len(s) > 0 {
		// 链接 [label](url)
		if lb := strings.Index(s, "["); lb >= 0 {
			if rb := strings.Index(s[lb:], "]("); rb >= 0 {
				rb += lb
				if rp := strings.Index(s[rb:], ")"); rp >= 0 {
					rp += rb
					if lb > 0 {
						elements = append(elements, plainElements(s[:lb])...)
					}
					label := s[lb+1 : rb]
					rawURL := s[rb+2 : rp]
					elements = append(elements, map[string]interface{}{
						"text_run": map[string]interface{}{
							"content":            label,
							"text_element_style": map[string]interface{}{"link": map[string]interface{}{"url": url.QueryEscape(rawURL)}},
						},
					})
					s = s[rp+1:]
					continue
				}
			}
		}
		// 无更多链接：处理剩余文本（去除加粗标记）
		elements = append(elements, plainElements(s)...)
		break
	}
	if len(elements) == 0 {
		elements = append(elements, map[string]interface{}{"text_run": map[string]interface{}{"content": ""}})
	}
	return elements
}

// plainElements 去掉加粗标记并生成纯文本 text_run 元素。
func plainElements(s string) []map[string]interface{} {
	s = strings.ReplaceAll(s, "**", "")
	if s == "" {
		return nil
	}
	return []map[string]interface{}{{"text_run": map[string]interface{}{"content": s}}}
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
