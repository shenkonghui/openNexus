package handlers

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// BrowserHandler 提供内置网页浏览器的内容抓取 API。
// 前端通过它绕过跨域限制，把网页正文提取后加载到任务对话框。
type BrowserHandler struct{}

// NewBrowserHandler 创建浏览器处理器。
func NewBrowserHandler() *BrowserHandler {
	return &BrowserHandler{}
}

const (
	browserMaxBodySize = 2 * 1024 * 1024
	browserTimeout     = 15 * time.Second
)

// Fetch GET /api/v1/browser/fetch?url=...
// 抓取指定 URL 并返回标题、纯文本与原始 HTML。
func (h *BrowserHandler) Fetch(c *gin.Context) {
	rawURL := strings.TrimSpace(c.Query("url"))
	if rawURL == "" {
		Fail(c, http.StatusBadRequest, "BROWSER_URL_EMPTY", "URL 不能为空")
		return
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		Fail(c, http.StatusBadRequest, "BROWSER_URL_INVALID", "仅支持 http/https URL")
		return
	}

	body, finalURL, contentType, err := h.fetchURL(parsed.String())
	if err != nil {
		Fail(c, http.StatusBadGateway, "BROWSER_FETCH_FAILED", err.Error())
		return
	}

	htmlBody := string(body)
	title := ""
	text := ""

	if isHTMLContent(contentType, htmlBody) {
		title = extractTitle(htmlBody)
		text = extractText(htmlBody)
	} else if strings.HasPrefix(contentType, "text/") {
		text = htmlBody
	} else {
		text = htmlBody
	}

	Success(c, http.StatusOK, gin.H{
		"url":   finalURL,
		"title": title,
		"text":  text,
		"html":  htmlBody,
	})
}

func (h *BrowserHandler) fetchURL(target string) ([]byte, string, string, error) {
	client := &http.Client{Timeout: browserTimeout}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 openNexus-Browser/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, "", "", errHTTPStatus{status: resp.Status}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, browserMaxBodySize))
	if err != nil {
		return nil, "", "", err
	}

	finalURL := resp.Request.URL.String()
	if finalURL == "" {
		finalURL = target
	}
	return body, finalURL, resp.Header.Get("Content-Type"), nil
}

type errHTTPStatus struct {
	status string
}

func (e errHTTPStatus) Error() string {
	return "远端返回 " + e.status
}

func isHTMLContent(contentType string, body string) bool {
	if strings.Contains(contentType, "text/html") {
		return true
	}
	if contentType == "" {
		lower := strings.ToLower(body)
		return strings.Contains(lower, "<html") || strings.Contains(lower, "<body")
	}
	return false
}

func extractTitle(s string) string {
	m := titleRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return unescapeAndTrim(m[1])
}

func extractText(s string) string {
	t := scriptRe.ReplaceAllString(s, " ")
	t = styleRe.ReplaceAllString(t, " ")
	t = commentRe.ReplaceAllString(t, " ")
	t = tagRe.ReplaceAllString(t, " ")
	t = html.UnescapeString(t)
	t = spaceRe.ReplaceAllString(strings.TrimSpace(t), " ")
	return t
}

func unescapeAndTrim(s string) string {
	return strings.TrimSpace(html.UnescapeString(s))
}

var (
	scriptRe  = regexp.MustCompile(`(?is)<script\b.*?</script>`)
	styleRe   = regexp.MustCompile(`(?is)<style\b.*?</style>`)
	commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagRe     = regexp.MustCompile(`(?is)<[^>]+>`)
	titleRe   = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	spaceRe   = regexp.MustCompile(`\s+`)
)
