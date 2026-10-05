package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"quantmind/internal/model"
	"quantmind/internal/repository"
	"quantmind/pkg/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== Tushare Client ====================

const (
	tushareAPIURL       = "http://api.tushare.pro"
	defaultTushareToken = "9009687b64bb934feeb28a7d1245fb4537047b280ea9cc400c812f3f"
)

// tushareTokenCache caches the token in memory
var (
	tushareTokenMu    sync.RWMutex
	tushareTokenCache string
	tushareTokenTime  time.Time
)

// getTushareToken retrieves the tushare token from DB (SystemConfig) or uses default
func getTushareToken() string {
	tushareTokenMu.RLock()
	if tushareTokenCache != "" && time.Since(tushareTokenTime) < 5*time.Minute {
		defer tushareTokenMu.RUnlock()
		return tushareTokenCache
	}
	tushareTokenMu.RUnlock()

	tushareTokenMu.Lock()
	defer tushareTokenMu.Unlock()

	// Double-check after acquiring write lock
	if tushareTokenCache != "" && time.Since(tushareTokenTime) < 5*time.Minute {
		return tushareTokenCache
	}

	// Try to read from DB
	var cfg model.SystemConfig
	if err := repository.DB.Where("config_key = ?", "tushare_token").First(&cfg).Error; err == nil && cfg.ConfigValue != "" {
		tushareTokenCache = cfg.ConfigValue
	} else {
		tushareTokenCache = defaultTushareToken
	}
	tushareTokenTime = time.Now()
	return tushareTokenCache
}

// clearTushareTokenCache invalidates the cached token (called on settings update)
func clearTushareTokenCache() {
	tushareTokenMu.Lock()
	defer tushareTokenMu.Unlock()
	tushareTokenCache = ""
	tushareTokenTime = time.Time{}
}

// tushareRequest represents a Tushare API request body
type tushareRequest struct {
	APIName string            `json:"api_name"`
	Token   string            `json:"token"`
	Params  map[string]string `json:"params"`
	Fields  string            `json:"fields"`
}

// tushareResponse represents a Tushare API response
type tushareResponse struct {
	RequestID string `json:"request_id"`
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	Data      struct {
		Fields  []string        `json:"fields"`
		Items   [][]interface{} `json:"items"`
		HasMore bool            `json:"has_more"`
	} `json:"data"`
}

// callTushareAPI sends a POST request to the Tushare API
func callTushareAPI(apiName string, params map[string]string, fields string) (*tushareResponse, error) {
	token := getTushareToken()
	if permissionSilenced(token, apiName) {
		return nil, permissionSkippedError{api: apiName}
	}

	reqBody := tushareRequest{
		APIName: apiName,
		Token:   token,
		Params:  params,
		Fields:  fields,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(tushareAPIURL, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("tushare API request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var result tushareResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if result.Code != 0 {
		if result.Code == 40203 {
			silencePermission(token, apiName)
		}
		return nil, fmt.Errorf("tushare error code %d: %s", result.Code, result.Msg)
	}

	return &result, nil
}

// 40203 means the token lacks the API permission, which no retry can fix. Without a
// memory of it every page view pays a doomed round trip per candidate date. The
// verdict is stored per token so a newly granted token is tried immediately.
const permissionSilenceDuration = 6 * time.Hour

var (
	permissionMu            sync.RWMutex
	permissionToken         string
	permissionSilencedUntil = map[string]time.Time{}
)

func permissionSilenced(token, apiName string) bool {
	permissionMu.Lock()
	defer permissionMu.Unlock()
	if token != permissionToken {
		permissionToken = token
		permissionSilencedUntil = map[string]time.Time{}
		return false
	}
	return time.Now().Before(permissionSilencedUntil[apiName])
}

// permissionSkippedError is returned locally, without a round trip, for an API this
// token is known to lack. Its text keeps "40203" so callers that stop their date
// walk-back on a rejected API still do so; isPermissionSkipped lets them also stay
// quiet, since silencePermission already logged the verdict once.
type permissionSkippedError struct{ api string }

func (e permissionSkippedError) Error() string {
	return fmt.Sprintf("tushare api %s skipped: 40203 permission silenced for this token", e.api)
}

func isPermissionSkipped(err error) bool {
	var target permissionSkippedError
	return errors.As(err, &target)
}

func silencePermission(token, apiName string) {
	permissionMu.Lock()
	defer permissionMu.Unlock()
	if token != permissionToken {
		permissionToken = token
		permissionSilencedUntil = map[string]time.Time{}
	}
	permissionSilencedUntil[apiName] = time.Now().Add(permissionSilenceDuration)
	log.Printf("[Tushare] %s 无权限(40203)，%d 小时内不再请求该接口", apiName, int(permissionSilenceDuration.Hours()))
}

// tushareDataToMap converts tushare response fields/items into a slice of maps
func tushareDataToMap(resp *tushareResponse) []map[string]interface{} {
	if resp == nil || len(resp.Data.Fields) == 0 || len(resp.Data.Items) == 0 {
		return nil
	}

	var results []map[string]interface{}
	for _, item := range resp.Data.Items {
		row := make(map[string]interface{})
		for i, field := range resp.Data.Fields {
			if i < len(item) {
				row[field] = item[i]
			}
		}
		results = append(results, row)
	}
	return results
}

// tsFloat safely extracts a float64 from a tushare data row
func tsFloat(row map[string]interface{}, key string) float64 {
	v, ok := row[key]
	if !ok || v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	}
	return 0
}

// tsString safely extracts a string from a tushare data row
func tsString(row map[string]interface{}, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%.0f", val)
	}
	return fmt.Sprintf("%v", v)
}

// codeToTsCode converts a 6-digit stock code to tushare ts_code format
// e.g., "600519" -> "600519.SH", "000001" -> "000001.SZ"
func codeToTsCode(code string) string {
	code = strings.TrimSpace(code)
	// Already has suffix
	if strings.Contains(code, ".") {
		return code
	}
	// 6xxxxx, 5xxxxx -> .SH (Shanghai)
	if strings.HasPrefix(code, "6") || strings.HasPrefix(code, "5") {
		return code + ".SH"
	}
	// 0xxxxx, 3xxxxx, 1xxxxx, 2xxxxx -> .SZ (Shenzhen)
	if strings.HasPrefix(code, "0") || strings.HasPrefix(code, "3") ||
		strings.HasPrefix(code, "1") || strings.HasPrefix(code, "2") {
		return code + ".SZ"
	}
	// 8xxxxx, 4xxxxx, 9xxxxx -> .BJ (Beijing)
	if strings.HasPrefix(code, "8") || strings.HasPrefix(code, "4") || strings.HasPrefix(code, "9") {
		return code + ".BJ"
	}
	return code + ".SH"
}

// tsCodeToCode converts "600519.SH" -> "600519"
func tsCodeToCode(tsCode string) string {
	parts := strings.Split(tsCode, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return tsCode
}

// ==================== Tushare-based fetchBatchQuotes ====================

// fetchBatchQuotesTushare fetches multiple stock quotes from Tushare daily API
func fetchBatchQuotesTushare(codes []string) map[string]*model.StockQuote {
	result := make(map[string]*model.StockQuote)
	if len(codes) == 0 {
		return result
	}

	// Convert codes to ts_codes
	tsCodes := make([]string, len(codes))
	for i, code := range codes {
		tsCodes[i] = codeToTsCode(code)
	}

	// Fetch daily data for the latest trading day.
	// Anchor on the session the market actually traded last: counting back from
	// time.Now() stops short of a 长假 (e.g. 国庆) and returns nothing.
	tradeDate := ""
	anchor := sessionAnchor()
	var dailyRows []map[string]interface{}
	for attempt := 0; attempt < 6; attempt++ {
		d := anchor.AddDate(0, 0, -attempt)
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		tradeDate = d.Format("20060102")

		resp, err := callTushareAPI("daily", map[string]string{
			"ts_code":    strings.Join(tsCodes, ","),
			"trade_date": tradeDate,
		}, "ts_code,trade_date,open,high,low,close,pre_close,change,pct_chg,vol,amount")

		if err != nil {
			log.Printf("[Tushare] daily API error for date %s: %v", tradeDate, err)
			continue
		}

		dailyRows = tushareDataToMap(resp)
		if len(dailyRows) > 0 {
			break
		}
	}

	// Build a lookup by ts_code
	dailyMap := make(map[string]map[string]interface{})
	for _, row := range dailyRows {
		tsCode := tsString(row, "ts_code")
		dailyMap[tsCode] = row
	}

	// Also try to get stock names from stock_basic if needed
	nameMap := fetchTushareStockNames(tsCodes)

	for _, code := range codes {
		tsCode := codeToTsCode(code)
		row, ok := dailyMap[tsCode]
		if !ok {
			result[code] = &model.StockQuote{Code: code, Name: nameMap[tsCode]}
			continue
		}

		closePrice := tsFloat(row, "close")
		preClose := tsFloat(row, "pre_close")
		change := tsFloat(row, "change")
		pctChg := tsFloat(row, "pct_chg")
		openPrice := tsFloat(row, "open")
		highPrice := tsFloat(row, "high")
		lowPrice := tsFloat(row, "low")
		vol := tsFloat(row, "vol")       // in 手(100 shares)
		amount := tsFloat(row, "amount") // in 千元

		name := nameMap[tsCode]
		if name == "" {
			name = code
		}

		result[code] = &model.StockQuote{
			Code:      code,
			Name:      name,
			Price:     closePrice,
			Close:     closePrice,
			TradeDate: tradeDate,
			Change:    change,
			ChangePct: pctChg,
			High:      highPrice,
			Low:       lowPrice,
			Open:      openPrice,
			PreClose:  preClose,
			Volume:    vol,           // 手, same unit as the Eastmoney ulist path
			Amount:    amount * 1000, // 千元 -> 元
		}
	}

	return result
}

// fetchTushareStockNames fetches stock names from tushare stock_basic
func fetchTushareStockNames(tsCodes []string) map[string]string {
	nameMap := make(map[string]string)
	if len(tsCodes) == 0 {
		return nameMap
	}

	// stock_basic doesn't support multi-code query in one call easily,
	// so we fetch all stocks and filter, but cache the result
	tushareNameCacheMu.RLock()
	if len(tushareNameCacheMap) > 0 && time.Since(tushareNameCacheTime) < 24*time.Hour {
		for _, tsCode := range tsCodes {
			if name, ok := tushareNameCacheMap[tsCode]; ok {
				nameMap[tsCode] = name
			}
		}
		tushareNameCacheMu.RUnlock()
		return nameMap
	}
	tushareNameCacheMu.RUnlock()

	resp, err := callTushareAPI("stock_basic", map[string]string{
		"list_status": "L",
	}, "ts_code,name,industry")
	if err != nil {
		log.Printf("[Tushare] stock_basic error: %v", err)
		return nameMap
	}

	rows := tushareDataToMap(resp)
	newCache := make(map[string]string)
	newIndustryCache := make(map[string]string)
	for _, row := range rows {
		tsCode := tsString(row, "ts_code")
		name := tsString(row, "name")
		industry := tsString(row, "industry")
		newCache[tsCode] = name
		newIndustryCache[tsCode] = industry
	}

	tushareNameCacheMu.Lock()
	tushareNameCacheMap = newCache
	tushareIndustryCacheMap = newIndustryCache
	tushareNameCacheTime = time.Now()
	tushareNameCacheMu.Unlock()

	for _, tsCode := range tsCodes {
		if name, ok := newCache[tsCode]; ok {
			nameMap[tsCode] = name
		}
	}
	return nameMap
}

// Name and industry cache
var (
	tushareNameCacheMu      sync.RWMutex
	tushareNameCacheMap     map[string]string
	tushareIndustryCacheMap map[string]string
	tushareNameCacheTime    time.Time
)

// getTushareStockIndustry looks up a stock's industry from the cache
func getTushareStockIndustry(tsCode string) string {
	tushareNameCacheMu.RLock()
	defer tushareNameCacheMu.RUnlock()
	return tushareIndustryCacheMap[tsCode]
}

// getTushareStockName looks up a stock's name from the cache
func getTushareStockName(tsCode string) string {
	tushareNameCacheMu.RLock()
	defer tushareNameCacheMu.RUnlock()
	return tushareNameCacheMap[tsCode]
}

// ==================== Tushare-based fetch5DayClose ====================

func fetch5DayCloseTushare(code string) []gin.H {
	tsCode := codeToTsCode(code)

	// Fetch last 10 calendar days to get 5 trading days
	endDate := time.Now().Format("20060102")
	startDate := time.Now().AddDate(0, 0, -15).Format("20060102")

	resp, err := callTushareAPI("daily", map[string]string{
		"ts_code":    tsCode,
		"start_date": startDate,
		"end_date":   endDate,
	}, "ts_code,trade_date,close")
	if err != nil {
		log.Printf("[Tushare] 5day close error for %s: %v", code, err)
		return []gin.H{}
	}

	rows := tushareDataToMap(resp)
	if len(rows) == 0 {
		return []gin.H{}
	}

	// Tushare returns newest first, we want newest last (or chronological)
	// Actually we only need the last 5 entries in chronological order
	sort.Slice(rows, func(i, j int) bool {
		return tsString(rows[i], "trade_date") < tsString(rows[j], "trade_date")
	})

	// Take last 5
	start := 0
	if len(rows) > 5 {
		start = len(rows) - 5
	}

	klines := []gin.H{}
	for _, row := range rows[start:] {
		dateStr := tsString(row, "trade_date")
		// Convert YYYYMMDD to YYYY-MM-DD
		if len(dateStr) == 8 {
			dateStr = dateStr[:4] + "-" + dateStr[4:6] + "-" + dateStr[6:]
		}
		klines = append(klines, gin.H{
			"date":  dateStr,
			"close": tsFloat(row, "close"),
		})
	}
	return klines
}

// ==================== Tushare-based K-Line ====================

// fetchKLineTushare fetches K-line data from tushare
func fetchKLineTushare(code string, period string, limit int) gin.H {
	tsCode := codeToTsCode(code)

	apiName := "daily"
	endDate := time.Now().Format("20060102")
	startDate := time.Now().AddDate(0, 0, -limit*2).Format("20060102") // extra buffer

	switch period {
	case "week", "weekly", "102":
		apiName = "weekly"
		startDate = time.Now().AddDate(0, 0, -limit*10).Format("20060102")
	case "month", "monthly", "103":
		apiName = "monthly"
		startDate = time.Now().AddDate(0, -limit*2, 0).Format("20060102")
	}

	resp, err := callTushareAPI(apiName, map[string]string{
		"ts_code":    tsCode,
		"start_date": startDate,
		"end_date":   endDate,
	}, "ts_code,trade_date,open,high,low,close,pre_close,change,pct_chg,vol,amount")
	if err != nil {
		log.Printf("[Tushare] KLine %s error for %s: %v", apiName, code, err)
		return gin.H{"klines": []interface{}{}, "code": code, "period": period}
	}

	rows := tushareDataToMap(resp)
	if len(rows) == 0 {
		return gin.H{"klines": []interface{}{}, "code": code, "period": period}
	}

	// Sort chronologically
	sort.Slice(rows, func(i, j int) bool {
		return tsString(rows[i], "trade_date") < tsString(rows[j], "trade_date")
	})

	// Limit to requested amount
	if len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}

	// Get name
	name := getTushareStockName(tsCode)

	// Pre-close from first item's pre_close
	preClose := 0.0
	if len(rows) > 0 {
		preClose = tsFloat(rows[0], "pre_close")
	}

	klines := []gin.H{}
	for _, row := range rows {
		dateStr := tsString(row, "trade_date")
		if len(dateStr) == 8 {
			dateStr = dateStr[:4] + "-" + dateStr[4:6] + "-" + dateStr[6:]
		}

		vol := tsFloat(row, "vol")       // 手
		amount := tsFloat(row, "amount") // 千元

		klines = append(klines, gin.H{
			"date":       dateStr,
			"open":       tsFloat(row, "open"),
			"close":      tsFloat(row, "close"),
			"high":       tsFloat(row, "high"),
			"low":        tsFloat(row, "low"),
			"volume":     vol * 100,     // convert to 股
			"amount":     amount * 1000, // convert to 元
			"change_pct": tsFloat(row, "pct_chg"),
			"turnover":   0, // will be filled if available
		})
	}

	return gin.H{
		"code":      code,
		"name":      name,
		"period":    period,
		"pre_close": preClose,
		"klines":    klines,
	}
}

// ==================== SystemConfig Model & Settings API ====================

// GetSystemSettings returns system settings (admin only)
func (h *Handler) GetSystemSettings(c *gin.Context) {
	var configs []model.SystemConfig
	if err := repository.DB.Find(&configs).Error; err != nil {
		response.InternalError(c, "读取系统设置失败: "+err.Error())
		return
	}

	settings := make(map[string]string)
	for _, cfg := range configs {
		settings[cfg.ConfigKey] = cfg.ConfigValue
	}

	// Mask sensitive tokens for display. The update handler recognises this
	// form and keeps the original value when the user edits another field.
	settings["tushare_token"] = maskSecret(settings["tushare_token"])
	settings["ai_decision_api_key"] = maskSecret(settings["ai_decision_api_key"])

	response.Success(c, settings)
}

// UpdateSystemSettings updates system settings (admin only)
func (h *Handler) UpdateSystemSettings(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid settings data")
		return
	}

	// Allowed setting keys
	allowedKeys := map[string]bool{
		"tushare_token":        true,
		"data_source":          true,
		"ai_decision_base_url": true,
		"ai_decision_api_key":  true,
		"ai_decision_model":    true,
	}

	if len(req) == 0 {
		response.BadRequest(c, "没有需要保存的设置")
		return
	}

	// Use a transaction so a failed write cannot leave only part of a
	// multi-field AI configuration persisted.
	tx := repository.DB.Begin()
	if tx.Error != nil {
		response.InternalError(c, "打开设置事务失败: "+tx.Error.Error())
		return
	}
	tushareChanged := false
	updatedKeys := 0
	for key, rawValue := range req {
		if !allowedKeys[key] {
			continue
		}
		updatedKeys++
		value := strings.TrimSpace(rawValue)

		var cfg model.SystemConfig
		err := tx.Where("config_key = ?", key).First(&cfg).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			tx.Rollback()
			response.InternalError(c, "读取设置失败: "+err.Error())
			return
		}

		// GET /settings intentionally returns a masked secret. If that value is
		// submitted unchanged while the user edits the URL/model, preserve the
		// raw value already in the database instead of storing the mask itself.
		if (key == "tushare_token" || key == "ai_decision_api_key") && isMaskedSecret(value) {
			if err == nil && strings.TrimSpace(cfg.ConfigValue) != "" {
				continue
			}
			tx.Rollback()
			response.BadRequest(c, "请填写完整的密钥，不能保存脱敏值")
			return
		}
		previousValue := cfg.ConfigValue

		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Create new
			cfg = model.SystemConfig{
				ConfigKey:   key,
				ConfigValue: value,
				Description: getSettingDescription(key),
			}
			if createErr := tx.Create(&cfg).Error; createErr != nil {
				tx.Rollback()
				response.InternalError(c, "保存设置失败: "+createErr.Error())
				return
			}
		} else {
			// Update existing
			if updateErr := tx.Model(&cfg).Update("config_value", value).Error; updateErr != nil {
				tx.Rollback()
				response.InternalError(c, "保存设置失败: "+updateErr.Error())
				return
			}
		}
		if key == "tushare_token" && previousValue != value {
			tushareChanged = true
		}
	}
	if updatedKeys == 0 {
		tx.Rollback()
		response.BadRequest(c, "没有可保存的设置项")
		return
	}
	if err := tx.Commit().Error; err != nil {
		response.InternalError(c, "提交设置失败: "+err.Error())
		return
	}

	// Clear token cache if tushare_token was updated
	if tushareChanged {
		clearTushareTokenCache()
	}

	if userID, ok := c.Get("user_id"); ok {
		if uid, ok := userID.(uint); ok {
			if err := repository.DB.Create(&model.AuditLog{
				UserID: uid, Module: "settings", Action: "update",
				Target: "system_settings", Detail: "更新系统设置",
				IP: c.ClientIP(), Status: "success",
			}).Error; err != nil {
				log.Printf("[Settings] audit log write failed: %v", err)
			}
		}
	}

	response.Success(c, gin.H{"message": "设置已更新"})
}

func getSettingDescription(key string) string {
	switch key {
	case "tushare_token":
		return "Tushare Pro API Token (用于获取股票行情数据)"
	case "data_source":
		return "数据源选择 (tushare/eastmoney)"
	case "ai_decision_base_url":
		return "AI买卖决策模型 Base URL"
	case "ai_decision_api_key":
		return "AI买卖决策模型 API Key"
	case "ai_decision_model":
		return "AI买卖决策模型名称"
	default:
		return key
	}
}

// ==================== Tushare-based fetchBatchQuotesWithMarketCap ====================

// fetchBatchQuotesWithMarketCapTushare fetches batch quotes with market cap from Tushare
// Uses daily + daily_basic APIs to get price data and market cap/PE/PB
func fetchBatchQuotesWithMarketCapTushare(codes []string) map[string]*quoteWithCap {
	result := make(map[string]*quoteWithCap)
	if len(codes) == 0 {
		return result
	}

	// Convert codes to ts_codes
	tsCodes := make([]string, len(codes))
	for i, code := range codes {
		tsCodes[i] = codeToTsCode(code)
	}

	// Find the latest trade date with data
	tradeDate := ""
	var dailyRows []map[string]interface{}
	anchor := sessionAnchor()
	for attempt := 0; attempt < 7; attempt++ {
		d := anchor.AddDate(0, 0, -attempt)
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		tradeDate = d.Format("20060102")

		resp, err := callTushareAPI("daily", map[string]string{
			"ts_code":    strings.Join(tsCodes, ","),
			"trade_date": tradeDate,
		}, "ts_code,trade_date,open,high,low,close,pre_close,change,pct_chg,vol,amount")
		if err != nil {
			log.Printf("[Tushare] daily API error for date %s: %v", tradeDate, err)
			continue
		}

		dailyRows = tushareDataToMap(resp)
		if len(dailyRows) > 0 {
			break
		}
	}

	// Build daily data lookup
	dailyMap := make(map[string]map[string]interface{})
	for _, row := range dailyRows {
		tsCode := tsString(row, "ts_code")
		dailyMap[tsCode] = row
	}

	// Fetch daily_basic for market cap, PE, PB
	var basicRows []map[string]interface{}
	if tradeDate != "" {
		resp, err := callTushareAPI("daily_basic", map[string]string{
			"ts_code":    strings.Join(tsCodes, ","),
			"trade_date": tradeDate,
		}, "ts_code,close,turnover_rate,volume_ratio,pe,pe_ttm,pb,total_mv,circ_mv")
		if err != nil {
			log.Printf("[Tushare] daily_basic API error: %v", err)
		} else {
			basicRows = tushareDataToMap(resp)
		}
	}

	basicMap := make(map[string]map[string]interface{})
	for _, row := range basicRows {
		tsCode := tsString(row, "ts_code")
		basicMap[tsCode] = row
	}

	// Get stock names
	nameMap := fetchTushareStockNames(tsCodes)

	for _, code := range codes {
		tsCode := codeToTsCode(code)
		daily := dailyMap[tsCode]
		basic := basicMap[tsCode]

		name := nameMap[tsCode]
		if name == "" {
			name = code
		}

		price := 0.0
		changePct := 0.0
		if daily != nil {
			price = tsFloat(daily, "close")
			changePct = tsFloat(daily, "pct_chg")
		}

		marketCap := 0.0
		concept := getTushareStockIndustry(tsCode)
		if basic != nil {
			// total_mv is in 万元, convert to 亿元
			totalMv := tsFloat(basic, "total_mv")
			marketCap = totalMv / 10000 // 万 -> 亿
			marketCap = math.Round(marketCap*100) / 100
		}

		result[code] = &quoteWithCap{
			Name:      name,
			Price:     price,
			ChangePct: changePct,
			MarketCap: marketCap,
			Concept:   concept,
		}
	}

	return result
}

// ==================== Tushare-based single stock quote ====================

// fetchSingleStockQuoteTushare fetches a single stock's real-time quote data from Tushare
func fetchSingleStockQuoteTushare(code string) *model.StockQuote {
	tsCode := codeToTsCode(code)

	// Try to get daily data for the latest trade date
	anchor := sessionAnchor()
	for attempt := 0; attempt < 7; attempt++ {
		d := anchor.AddDate(0, 0, -attempt)
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		tradeDate := d.Format("20060102")

		resp, err := callTushareAPI("daily", map[string]string{
			"ts_code":    tsCode,
			"trade_date": tradeDate,
		}, "ts_code,trade_date,open,high,low,close,pre_close,change,pct_chg,vol,amount")
		if err != nil {
			continue
		}

		rows := tushareDataToMap(resp)
		if len(rows) == 0 {
			continue
		}

		row := rows[0]
		name := getTushareStockName(tsCode)
		if name == "" {
			// Trigger name cache load
			fetchTushareStockNames([]string{tsCode})
			name = getTushareStockName(tsCode)
		}

		return &model.StockQuote{
			Code:      code,
			Name:      name,
			Price:     tsFloat(row, "close"),
			Change:    tsFloat(row, "change"),
			ChangePct: tsFloat(row, "pct_chg"),
			High:      tsFloat(row, "high"),
			Low:       tsFloat(row, "low"),
			Open:      tsFloat(row, "open"),
			PreClose:  tsFloat(row, "pre_close"),
			// Match the ulist convention the UI expects: volume in 手, amount in 元.
			Volume: tsFloat(row, "vol"),
			Amount: tsFloat(row, "amount") * 1000, // 千元 -> 元
		}
	}

	return nil
}
