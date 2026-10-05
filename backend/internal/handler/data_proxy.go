package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quantmind/internal/model"
	"quantmind/pkg/response"

	"github.com/gin-gonic/gin"
)

// ==================== 数据服务代理 - Data Service Proxy ====================
// Proxies requests to the unified Python data service (mootdx + tencent + akshare + cninfo + eastmoney)

var dataServiceClient = &http.Client{Timeout: 30 * time.Second}

// proxyToDataService forwards request to the Python data service. It returns
// false when the optional service is unavailable/non-2xx so callers can use a
// native Go/Tushare fallback instead of silently returning an empty page.
func proxyToDataService(c *gin.Context, path string, params map[string]string) bool {
	baseURL := getAkShareServiceURL()
	if baseURL == "" {
		return false
	}
	reqURL := fmt.Sprintf("%s%s", baseURL, path)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		log.Printf("[DataProxy] create request failed: %v", err)
		return false
	}

	q := req.URL.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	req.URL.RawQuery = q.Encode()

	resp, err := dataServiceClient.Do(req)
	if err != nil {
		log.Printf("[DataProxy] Request to %s failed: %v", reqURL, err)
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[DataProxy] read response failed: %v", err)
		return false
	}

	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[DataProxy] parse response failed: %v", err)
		return false
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Printf("[DataProxy] %s returned status %d", reqURL, resp.StatusCode)
		return false
	}
	c.JSON(resp.StatusCode, result)
	return true
}

func dataServiceUnavailable(c *gin.Context) {
	message := "数据服务不可用，请启动 AkShare 数据服务或配置可用数据源"
	c.JSON(http.StatusServiceUnavailable, gin.H{"code": -1, "message": message, "error": message})
}

// dataProxyPageSize reads the optional page_size query param (0 means default).
func dataProxyPageSize(c *gin.Context) int {
	n, _ := strconv.Atoi(c.Query("page_size"))
	return n
}

// GetDataQuote - 获取实时行情 (Tencent + mootdx)
func (h *Handler) GetDataQuote(c *gin.Context) {
	if proxyToDataService(c, "/quote", map[string]string{
		"codes": c.Query("codes"),
	}) {
		return
	}

	// The Python service is optional. Tushare is already configured for the Go
	// server, so keep Xiaofan's quote cards useful when AkShare is offline.
	codes := splitStockCodes(c.Query("codes"))
	if len(codes) == 0 {
		response.BadRequest(c, "请提供股票代码")
		return
	}
	quotesByCode := fetchBatchQuotes(codes)
	quotes := make([]model.StockQuote, 0, len(codes))
	for _, code := range codes {
		if quote, ok := quotesByCode[code]; ok && quote != nil {
			quotes = append(quotes, *quote)
		}
	}
	if len(quotes) == 0 {
		dataServiceUnavailable(c)
		return
	}
	response.Success(c, quotes)
}

// GetDataKline - 获取K线数据 (mootdx)
// Frontend sends period: day/week/month, Python expects freq: daily/weekly/monthly
func (h *Handler) GetDataKline(c *gin.Context) {
	period := c.Query("period")
	count := c.Query("count")

	// Map frontend period names to Python service freq names
	freqMap := map[string]string{
		"day":   "daily",
		"week":  "weekly",
		"month": "monthly",
		"year":  "monthly", // no yearly in mootdx, use monthly with larger count
		// Also support direct freq names for backward compatibility
		"daily":   "daily",
		"weekly":  "weekly",
		"monthly": "monthly",
	}

	freq := freqMap[period]
	if freq == "" {
		freq = "daily" // default
	}

	// For year period, use monthly with larger count to cover ~5 years
	if period == "year" && (count == "" || count == "60") {
		count = "120" // 120 months = 10 years of monthly data
	}

	if proxyToDataService(c, "/kline", map[string]string{
		"code":  c.Query("code"),
		"freq":  freq,
		"count": count,
	}) {
		return
	}

	// Fall back to the shared Tushare implementation when the Python service is
	// not running. This returns the same {code, period, klines} shape consumed by
	// XiaofanSelectPage.
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		response.BadRequest(c, "请提供股票代码")
		return
	}
	limit := 120
	if count != "" {
		if parsed, err := strconv.Atoi(count); err == nil {
			limit = parsed
		}
	}
	if limit < 5 {
		limit = 5
	} else if limit > 500 {
		limit = 500
	}
	tusharePeriod := period
	if tusharePeriod == "year" {
		// The shared Tushare adapter has no yearly endpoint; monthly bars with a
		// larger limit provide the intended multi-year view.
		tusharePeriod = "month"
	}
	result := fetchKLineTushare(code, tusharePeriod, limit)
	if rows, ok := result["klines"].([]gin.H); ok && len(rows) > 0 {
		response.Success(c, result)
		return
	}

	// Last resort: Sina/Tencent daily-and-weekly bars, same row shape as Tushare.
	bars, source, err := fetchPeriodKLineFallback(code, tusharePeriod, limit)
	if err != nil || len(bars) == 0 {
		if err == nil {
			err = fmt.Errorf("无K线数据")
		}
		log.Printf("[DataProxy] /kline %s(%s) 兜底失败: %v", code, tusharePeriod, err)
		dataServiceUnavailable(c)
		return
	}
	log.Printf("[DataProxy] /kline %s(%s) 使用%s兜底 %d 根", code, tusharePeriod, source, len(bars))
	response.Success(c, gin.H{
		"code":        extractPureCode(code),
		"period":      period,
		"klines":      bars,
		"data_source": source,
	})
}

// GetDataMinute - 获取分时数据 (mootdx)
func (h *Handler) GetDataMinute(c *gin.Context) {
	if proxyToDataService(c, "/minute", map[string]string{
		"code": c.Query("code"),
	}) {
		return
	}

	// mootdx-backed service is offline here, but Eastmoney's trends2 endpoint is
	// reachable, so serve the same {price,vol,volume} list from it.
	code := c.Query("code")
	if points := minutePointsFromTrends(code); len(points) > 0 {
		log.Printf("[DataProxy] /minute %s 使用分时趋势兜底 %d 个点", code, len(points))
		response.Success(c, points)
		return
	}
	dataServiceUnavailable(c)
}

func minutePointsFromTrends(code string) []gin.H {
	if code == "" {
		return nil
	}
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/trends2/get?secid=%s&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13&fields2=f51,f52,f53,f54,f55,f56,f57,f58&iscr=0&ndays=1", buildSecID(code))
	data, err := fetchEastmoneyAPIWithRetry(url, 2)
	if err != nil || len(data) == 0 {
		log.Printf("[DataProxy] /minute 分时兜底失败: %v", err)
		return nil
	}
	parsed := parseTrendData(data)
	trends, _ := parsed["trends"].([]gin.H)
	points := make([]gin.H, 0, len(trends))
	for _, t := range trends {
		price, _ := t["price"].(float64)
		if price <= 0 {
			continue
		}
		vol, _ := t["volume"].(float64)
		if vol == 0 {
			vol, _ = t["vol"].(float64)
		}
		amount, _ := t["amount"].(float64)
		avg, _ := t["avg"].(float64)
		points = append(points, gin.H{
			"time":   fmt.Sprintf("%v", t["time"]),
			"price":  price,
			"vol":    int64(vol),
			"volume": int64(vol),
			"amount": amount,
			"avg":    avg,
		})
	}
	return points
}

// GetDataResearch - 获取研报数据 (eastmoney)
func (h *Handler) GetDataResearch(c *gin.Context) {
	if proxyToDataService(c, "/research", map[string]string{
		"code":      c.Query("code"),
		"page_size": c.Query("page_size"),
		"page":      c.Query("page"),
	}) {
		return
	}

	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		response.BadRequest(c, "请提供股票代码")
		return
	}
	items, err := fetchResearchFromEastmoney(code, dataProxyPageSize(c))
	if err != nil {
		dataServiceUnavailable(c)
		return
	}
	response.Success(c, gin.H{"list": items, "total": len(items), "data_source": "eastmoney-reportapi"})
}

// GetDataNews - 获取新闻数据 (akshare)
func (h *Handler) GetDataNews(c *gin.Context) {
	if proxyToDataService(c, "/news", map[string]string{
		"code": c.Query("code"),
	}) {
		return
	}

	// News has a native Eastmoney/LLM fallback in the Go server, so the
	// Xiaofan info modal remains useful without the optional Python service.
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		response.BadRequest(c, "请提供股票代码")
		return
	}
	response.Success(c, fetchStockNewsFromEastmoney(code, ""))
}

// GetDataGuba - 获取股吧讨论 (eastmoney)
func (h *Handler) GetDataGuba(c *gin.Context) {
	if proxyToDataService(c, "/guba", map[string]string{
		"code":      c.Query("code"),
		"page":      c.Query("page"),
		"page_size": c.Query("page_size"),
	}) {
		return
	}
	// The SSR scraper behind /market/guba is verified reachable from here.
	h.GetGubaDiscussion(c)
}

// GetDataF10 - 获取F10基础数据 (mootdx finance)
func (h *Handler) GetDataF10(c *gin.Context) {
	if proxyToDataService(c, "/f10", map[string]string{
		"code": c.Query("code"),
	}) {
		return
	}

	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		response.BadRequest(c, "请提供股票代码")
		return
	}
	if info := fetchF10FromEastmoney(code); info != nil {
		response.Success(c, info)
		return
	}
	dataServiceUnavailable(c)
}

// GetDataAnnounce - 获取公告数据 (cninfo 巨潮资讯)
func (h *Handler) GetDataAnnounce(c *gin.Context) {
	if proxyToDataService(c, "/announce", map[string]string{
		"code":      c.Query("code"),
		"page_size": c.Query("page_size"),
		"page":      c.Query("page"),
	}) {
		return
	}

	// cninfo is not reachable from this network, so read the same disclosures
	// from East Money's announcement list API.
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		response.BadRequest(c, "请提供股票代码")
		return
	}
	if items := fetchAnnounceFromEastmoney(code, dataProxyPageSize(c)); len(items) > 0 {
		response.Success(c, gin.H{"list": items, "total": len(items), "data_source": "eastmoney-ann"})
		return
	}
	dataServiceUnavailable(c)
}

func splitStockCodes(value string) []string {
	parts := strings.Split(value, ",")
	codes := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		code := strings.TrimSpace(part)
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	return codes
}

// ValidateStockCode - 验证股票代码并返回真实名称
// 调用腾讯/通达信接口验证代码有效性
func (h *Handler) ValidateStockCode(c *gin.Context) {
	code := c.Query("code")
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": -1, "error": "股票代码不能为空"})
		return
	}

	log.Printf("[DataProxy] ValidateStockCode: code=%s", code)

	baseURL := getAkShareServiceURL()
	reqURL := fmt.Sprintf("%s/quote?codes=%s", baseURL, code)

	resp, err := dataServiceClient.Get(reqURL)
	if err != nil {
		log.Printf("[DataProxy] ValidateStock request failed: %v", err)
		if respondStockValidity(c, code) {
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": -1, "error": "数据服务不可用", "valid": false})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": -1, "error": "读取响应失败", "valid": false})
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": -1, "error": "解析响应失败", "valid": false})
		return
	}

	// Check if quote data contains valid name
	if data, ok := result["data"]; ok {
		if quotes, ok := data.([]interface{}); ok && len(quotes) > 0 {
			if q, ok := quotes[0].(map[string]interface{}); ok {
				name, _ := q["name"].(string)
				price, _ := q["price"].(float64)
				if name != "" && name != "---" && price > 0 {
					log.Printf("[DataProxy] ValidateStock success: code=%s, name=%s", code, name)
					c.JSON(http.StatusOK, gin.H{"code": 0, "valid": true, "name": name, "price": price})
					return
				}
			}
		}
		// Also handle single quote object
		if q, ok := data.(map[string]interface{}); ok {
			name, _ := q["name"].(string)
			price, _ := q["price"].(float64)
			if name != "" && name != "---" && price > 0 {
				log.Printf("[DataProxy] ValidateStock success: code=%s, name=%s", code, name)
				c.JSON(http.StatusOK, gin.H{"code": 0, "valid": true, "name": name, "price": price})
				return
			}
		}
	}

	log.Printf("[DataProxy] ValidateStock invalid: code=%s", code)
	if respondStockValidity(c, code) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "valid": false, "error": "无效的股票代码"})
}

// respondStockValidity answers from the ulist quote source. It returns false only
// when that source is unreachable, so a code the source simply does not know is
// reported as invalid instead of as a service outage.
func respondStockValidity(c *gin.Context, code string) bool {
	pure := extractPureCode(code)
	rows, err := fetchQuoteViaUlist([]string{pure})
	if err != nil && !errors.Is(err, errNoUlistRows) {
		return false
	}
	name, price := "", 0.0
	if len(rows) > 0 {
		name, _ = rows[0]["name"].(string)
		price, _ = rows[0]["close"].(float64)
	}
	if name == "" || price <= 0 {
		c.JSON(http.StatusOK, gin.H{"code": 0, "valid": false, "error": "无效的股票代码"})
		return true
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "valid": true, "name": name, "price": price})
	return true
}
