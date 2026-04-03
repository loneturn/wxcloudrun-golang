package service

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// PublishRequest 发版请求结构
type PublishRequest struct {
	Title   string `json:"title"`
	Author  string `json:"author"`
	Content string `json:"content_md"`
	Digest  string `json:"digest,omitempty"`
}

// PublishResponse 发版响应结构
type PublishResponse struct {
	Code     int         `json:"code"`
	ErrorMsg string      `json:"errorMsg,omitempty"`
	Data     interface{} `json:"data"`
}

// PublishHandler 微信公众号草稿发布接口
// POST /api/publish
// Body: {"title":"标题","author":"作者","content_md":"## markdown内容","digest":"摘要"}
func PublishHandler(w http.ResponseWriter, r *http.Request) {
	res := &PublishResponse{}

	if r.Method != http.MethodPost {
		res.Code = -1
		res.ErrorMsg = fmt.Sprintf("只支持 POST 方法，当前: %s", r.Method)
		sendJSON(w, res)
		return
	}

	var req PublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		res.Code = -1
		res.ErrorMsg = fmt.Sprintf("JSON 解析失败: %v", err)
		sendJSON(w, res)
		return
	}
	if req.Title == "" || req.Content == "" {
		res.Code = -1
		res.ErrorMsg = "title 和 content_md 不能为空"
		sendJSON(w, res)
		return
	}
	if req.Author == "" {
		req.Author = "一只家电狗"
	}
	if req.Digest == "" {
		req.Digest = makeDigest(req.Content)
	}

	token, err := getAccessToken()
	if err != nil {
		res.Code = -1
		res.ErrorMsg = fmt.Sprintf("获取access_token失败: %v", err)
		sendJSON(w, res)
		return
	}

	htmlContent := mdToWechatHTML(req.Content)
	htmlContent, _ = processImages(htmlContent, token)

	article := map[string]interface{}{
		"title":            req.Title,
		"author":           req.Author,
		"content":          htmlContent,
		"digest":           req.Digest,
		"need_open_comment": 1,
		"only_fans":        1,
	}

	mediaResp, err := addDraft(token, []interface{}{article})
	if err != nil {
		res.Code = -1
		res.ErrorMsg = fmt.Sprintf("提交草稿失败: %v", err)
		sendJSON(w, res)
		return
	}

	res.Code = 0
	res.Data = map[string]interface{}{
		"msg":       "已提交到草稿箱",
		"media_id":  mediaResp.MediaID,
		"title":     req.Title,
		"author":    req.Author,
		"check_url": "https://mp.weixin.qq.com/cgi-bin/draft?action=list_draft",
	}
	sendJSON(w, res)
}

// ---- 微信 API ----

func getAccessToken() (string, error) {
	appID := "wxccbba9dda3868591"
	appSecret := "35b1afd9feef46123f9170256d4955da"
	url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s", appID, appSecret)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("请求token失败: %v", err)
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var result struct {
		AccessToken string `json:"access_token"`
		ErrCode    int    `json:"errcode"`
		ErrMsg     string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析token响应失败: %v", err)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("access_token为空: code=%d msg=%s", result.ErrCode, result.ErrMsg)
	}
	return result.AccessToken, nil
}

type wxResp struct {
	MediaID string `json:"media_id"`
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func addDraft(token string, articles []interface{}) (wxResp, error) {
	url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/draft/add?access_token=%s", token)
	payload := map[string]interface{}{"articles": articles}
	bodyBytes, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return wxResp{}, fmt.Errorf("请求草稿接口失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := ioutil.ReadAll(resp.Body)
	var result wxResp
	if err := json.Unmarshal(respBody, &result); err != nil {
		return wxResp{}, fmt.Errorf("解析草稿响应失败: %v", err)
	}
	if result.ErrCode != 0 {
		return wxResp{}, fmt.Errorf("微信错误: code=%d msg=%s", result.ErrCode, result.ErrMsg)
	}
	return result, nil
}

func uploadImage(token string, imageData []byte, filename string) (string, error) {
	url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/media/upload?access_token=%s&type=image", token)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("media", filename)
	if err != nil {
		return "", fmt.Errorf("创建表单文件失败: %v", err)
	}
	part.Write(imageData)
	writer.Close()

	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("POST", url, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("上传图片请求失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := ioutil.ReadAll(resp.Body)
	var result struct {
		URL     string `json:"url"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("解析图片上传响应失败: %v", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("上传图片失败: code=%d msg=%s", result.ErrCode, result.ErrMsg)
	}
	return result.URL, nil
}

// ---- Markdown → 微信 HTML ----

func mdToWechatHTML(md string) string {
	html := md

	// 代码块
	codeBlockRe := regexp.MustCompile("(?s)\x60\x60\x60[^\n]*\n(.+?)\x60\x60\x60")
	html = codeBlockRe.ReplaceAllStringFunc(html, func(match string) string {
		m := codeBlockRe.FindStringSubmatch(match)
		if len(m) < 2 {
			return match
		}
		return "<pre><code>" + htmlEncode(m[1]) + "</code></pre>"
	})

	// 行内代码
	inlineCodeRe := regexp.MustCompile("\x60([^\x60]+)\x60")
	html = inlineCodeRe.ReplaceAllStringFunc(html, func(match string) string {
		m := inlineCodeRe.FindStringSubmatch(match)
		if len(m) < 2 {
			return match
		}
		return "<code>" + htmlEncode(m[1]) + "</code>"
	})

	// 标题
	html = regexp.MustCompile("(?m)^###\\s+(.+)$").ReplaceAllString(html, "<h4>$1</h4>")
	html = regexp.MustCompile("(?m)^##\\s+(.+)$").ReplaceAllString(html, "<h3>$1</h3>")
	html = regexp.MustCompile("(?m)^#\\s+(.+)$").ReplaceAllString(html, "<h2>$1</h2>")

	// 加粗和斜体
	html = regexp.MustCompile("\\*\\*\\*(.+?)\\*\\*\\*").ReplaceAllString(html, "<strong><em>$1</em></strong>")
	html = regexp.MustCompile("\\*\\*(.+?)\\*\\*").ReplaceAllString(html, "<strong>$1</strong>")
	html = regexp.MustCompile("\\*(.+?)\\*").ReplaceAllString(html, "<em>$1</em>")

	// 引用
	html = regexp.MustCompile("(?m)^>\\s+(.+)$").ReplaceAllString(html, "<blockquote><p>$1</p></blockquote>")

	// 分割线
	html = regexp.MustCompile("(?m)^---+$").ReplaceAllString(html, "<hr />")

	// 列表
	html = regexp.MustCompile("(?m)^-\\s+(.+)$").ReplaceAllString(html, "<li>$1</li>")
	html = regexp.MustCompile("(?m)^\\d+\\.\\s+(.+)$").ReplaceAllString(html, "<li>$1</li>")

	// 图片
	html = regexp.MustCompile("!\\[([^\\]]*)\\]\\(([^)]+)\\)").ReplaceAllString(html, `<img src="$2" alt="$1" style="max-width:100%;" />`)

	// 链接
	html = regexp.MustCompile("\\[([^\\]]+)\\]\\(([^)]+)\\)").ReplaceAllString(html, `<a href="$2">$1</a>`)

	// 段落
	paragraphs := strings.Split(html, "\n\n")
	var result []string
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "<h") || strings.HasPrefix(p, "<p>") ||
			strings.HasPrefix(p, "<ul") || strings.HasPrefix(p, "<ol") ||
			strings.HasPrefix(p, "<li") || strings.HasPrefix(p, "<pre") ||
			strings.HasPrefix(p, "<blockquote") || strings.HasPrefix(p, "<hr") ||
			strings.HasPrefix(p, "<img") {
			result = append(result, p)
		} else {
			p = strings.ReplaceAll(p, "\n", "<br />")
			result = append(result, "<p>"+p+"</p>")
		}
	}
	return strings.Join(result, "\n")
}

func htmlEncode(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func makeDigest(md string) string {
	// 去掉 markdown 符号
	s := md
	re := regexp.MustCompile("[#*\x60\\[\\]()>-]")
	s = re.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "  ", " ")
	s = strings.TrimSpace(s)
	if len(s) > 54 {
		return s[:54] + "..."
	}
	return s
}

// ---- 图片处理 ----

func processImages(htmlContent, token string) (string, error) {
	imgRe := regexp.MustCompile(`<img[^>]+src=["']([^"']+)["'][^>]*>`)
	matches := imgRe.FindAllStringSubmatch(htmlContent, -1)
	for _, m := range matches {
		src := m[1]
		if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
			continue
		}

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(src)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		ext := ".jpg"
		if strings.Contains(src, ".png") {
			ext = ".png"
		} else if strings.Contains(src, ".gif") {
			ext = ".gif"
		} else if strings.Contains(src, ".webp") {
			ext = ".webp"
		}
		filename := fmt.Sprintf("%x%s", md5.Sum(data), ext)

		imgURL, err := uploadImage(token, data, filename)
		if err != nil {
			continue
		}
		htmlContent = strings.ReplaceAll(htmlContent, src, imgURL)
	}
	return htmlContent, nil
}

// ---- 工具 ----

func sendJSON(w http.ResponseWriter, res *PublishResponse) {
	w.Header().Set("content-type", "application/json")
	msg, _ := json.Marshal(res)
	w.Write(msg)
}
