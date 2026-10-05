package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"quantmind/internal/middleware"
	"quantmind/internal/model"
	"quantmind/internal/repository"
	"quantmind/pkg/response"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type Handler struct{}

func New() *Handler { return &Handler{} }

// ==================== Auth ====================

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	var user model.User
	if err := repository.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		response.Unauthorized(c, "用户不存在")
		return
	}

	if !user.IsActive {
		response.Unauthorized(c, "账户已禁用")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		response.Unauthorized(c, "密码错误")
		return
	}

	token, err := middleware.GenerateToken(user.ID, user.Username, user.Role)
	if err != nil {
		response.InternalError(c, "生成令牌失败")
		return
	}

	// Audit login
	repository.DB.Create(&model.AuditLog{
		UserID: user.ID, Username: user.Username,
		Module: "login", Action: "login", Target: "auth",
		Detail: "用户登录成功", IP: c.ClientIP(), Status: "success",
	})

	response.Success(c, gin.H{
		"token": token,
		"user": gin.H{
			"id": user.ID, "username": user.Username,
			"display_name": user.DisplayName, "role": user.Role,
			"email": user.Email, "avatar": user.Avatar,
		},
	})
}

func (h *Handler) GetProfile(c *gin.Context) {
	userID, _ := c.Get("user_id")
	var user model.User
	repository.DB.First(&user, userID)
	response.Success(c, user)
}

// ==================== Dashboard ====================

func (h *Handler) GetDashboard(c *gin.Context) {
	var agentCount, userCount, conversationCount, quoteCount int64
	var auditCount, signalCount int64
	repository.DB.Model(&model.Agent{}).Count(&agentCount)
	repository.DB.Model(&model.User{}).Count(&userCount)
	repository.DB.Model(&model.Conversation{}).Count(&conversationCount)
	repository.DB.Model(&model.StockQuote{}).Count(&quoteCount)
	repository.DB.Model(&model.AuditLog{}).Count(&auditCount)
	repository.DB.Model(&model.StrategySignal{}).Count(&signalCount)

	// Get today's market overview
	today := currentSessionDate()
	var sentiment model.MarketSentiment
	repository.DB.Where("trade_date = ?", today).First(&sentiment)

	response.Success(c, gin.H{
		"stats": gin.H{
			"agents":           agentCount,
			"users":            userCount,
			"conversations":    conversationCount,
			"stock_quotes":     quoteCount,
			"audit_logs":       auditCount,
			"strategy_signals": signalCount,
		},
		"market_sentiment": sentiment,
	})
}

// ==================== Market Sentiment & Data ====================

func (h *Handler) GetMarketSentiment(c *gin.Context) {
	date := queryDate(c, "date")
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))

	var sentiments []model.MarketSentiment
	repository.DB.Where("trade_date <= ?", date).Order("trade_date desc").Limit(days).Find(&sentiments)

	response.Success(c, sentiments)
}

func (h *Handler) GetSectorHeat(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "30"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 30
	}

	today := currentSessionDate()

	// Try fetching real-time data first
	allSectors := fetchAllSectorOrConcept("sector")

	if len(allSectors) > 0 {
		// Persist to DB
		go persistSectorHeatData(allSectors, "sector", today)
	} else {
		// Fallback: read from DB
		var dbRecords []model.SectorHeat
		repository.DB.Where("trade_date = ? AND category = ?", today, "sector").Order("change_pct desc").Find(&dbRecords)
		for _, r := range dbRecords {
			allSectors = append(allSectors, gin.H{
				"name": r.Name, "code": r.Code, "change_pct": r.ChangePct,
				"net_flow": r.NetFlow, "flow_in": r.FlowIn, "flow_out": r.FlowOut,
				"lead_stock": r.LeadStock, "price": r.Amount, "net_pct": r.NetPct,
			})
		}
	}

	// Paginate
	total := len(allSectors)
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	paged := allSectors[start:end]

	response.Success(c, gin.H{
		"items":       paged,
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": (total + pageSize - 1) / pageSize,
	})
}

func (h *Handler) GetLimitUpBoard(c *gin.Context) {
	date := queryDate(c, "date")
	// Only exchange-feed rows are served: the legacy limit_type keys hold the optional
	// demo seed, whose prices and 连板数 are invented.
	types := realBoardTypes
	if requested := c.DefaultQuery("type", ""); requested != "" {
		rt := realBoardType(requested)
		if isRealBoardType(rt) {
			types = []string{rt}
		} else if isRealBoardType(requested) {
			types = []string{requested}
		} else {
			response.Success(c, []model.LimitUpBoard{})
			return
		}
	}
	var boards []model.LimitUpBoard
	repository.DB.Where("trade_date = ? AND limit_type IN ?", date, types).
		Order("board_count desc, fund_amount desc").Find(&boards)
	response.Success(c, boards)
}

func (h *Handler) GetDragonTiger(c *gin.Context) {
	date := queryDate(c, "date")
	var data []model.DragonTiger
	repository.DB.Where("trade_date = ?", date).Order("net_amount desc").Find(&data)
	response.Success(c, data)
}

func (h *Handler) GetBoardLadder(c *gin.Context) {
	date := queryDate(c, "date")
	var boards []model.LimitUpBoard
	repository.DB.Where("trade_date = ? AND limit_type = ?", date, "limit_up_rt").
		Order("board_count desc").Find(&boards)

	// Build ladder structure
	ladder := make(map[int][]model.LimitUpBoard)
	maxBoard := 0
	for _, b := range boards {
		ladder[b.BoardCount] = append(ladder[b.BoardCount], b)
		if b.BoardCount > maxBoard {
			maxBoard = b.BoardCount
		}
	}
	response.Success(c, gin.H{"ladder": ladder, "max_board": maxBoard})
}

// ==================== Real-time Market Data ====================

// resolveStockQuoteCode accepts a six-digit code (with or without an exchange
// suffix) as well as a stock name, symbol, Tushare code, or pinyin shortcut.
// The UI historically stripped non-digits before calling this endpoint, which
// turned a name such as "国泰海通" into an empty code. Keep the resolution in
// the backend too so API clients are not dependent on that UI behaviour.
func resolveStockQuoteCode(input string) (string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", fmt.Errorf("请提供股票代码或股票名称")
	}

	// Normalize common Tushare forms such as 601211.SH while retaining the
	// six-digit code expected by all quote adapters.
	candidate := strings.ToUpper(raw)
	for _, suffix := range []string{".SH", ".SZ", ".BJ"} {
		candidate = strings.TrimSuffix(candidate, suffix)
	}
	if len(candidate) == 6 {
		allDigits := true
		for _, r := range candidate {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return candidate, nil
		}
	}

	// Stock basics are populated by the Tushare sync and are also used by the
	// broadcast search endpoint. Prefer exact matches, then allow a partial
	// name/pinyin match for convenient queries.
	upper := strings.ToUpper(raw)
	lower := strings.ToLower(raw)
	var stock TsStockBasic
	exact := repository.DB.Where(
		"name = ? OR symbol = ? OR ts_code = ? OR cn_spell = ?",
		raw, raw, upper, lower,
	).First(&stock)
	if exact.Error != nil {
		partial := repository.DB.Where(
			"name LIKE ? OR symbol LIKE ? OR ts_code LIKE ? OR cn_spell LIKE ?",
			"%"+raw+"%", "%"+raw+"%", "%"+upper+"%", "%"+lower+"%",
		).Order("name asc").First(&stock)
		if partial.Error != nil {
			return "", fmt.Errorf("未找到股票：%s，请输入股票代码或已收录的股票名称", raw)
		}
	}

	code := tsCodeToCode(stock.TsCode)
	if len(code) != 6 {
		return "", fmt.Errorf("股票 %s 的代码格式无效", raw)
	}
	return code, nil
}

func (h *Handler) GetStockQuote(c *gin.Context) {
	input := c.Query("code")
	source := c.DefaultQuery("source", "tushare")
	code, resolveErr := resolveStockQuoteCode(input)
	if resolveErr != nil {
		response.BadRequest(c, resolveErr.Error())
		return
	}

	// Try to get from database first (cached)
	var quote model.StockQuote
	today := currentSessionDate()
	err := repository.DB.Where("code = ? AND trade_date = ?", code, today).
		Order("created_at desc").First(&quote).Error

	if err == nil {
		response.Success(c, quote)
		return
	}

	// Try Tushare first
	tushareQuote := fetchSingleStockQuoteTushare(code)
	if tushareQuote != nil && tushareQuote.Price > 0 {
		tushareQuote.Source = "tushare"
		tushareQuote.TradeDate = today
		repository.DB.Create(tushareQuote)
		response.Success(c, tushareQuote)
		return
	}

	// Fallback to external source
	quoteData, fetchErr := fetchQuoteFromSource(code, source)
	effectiveSource := source
	// fetchQuoteFromSource already swapped in Eastmoney's reachable ulist endpoint
	// when stock/get was blocked; keep that recorded instead of the requested name.
	if quoteData != nil && strings.Contains(quoteData.Source, "ulist") {
		effectiveSource = quoteData.Source
	}
	if fetchErr != nil || quoteData == nil || quoteData.Price <= 0 {
		// Eastmoney/Sina can be temporarily blocked or return an empty payload
		// from mainland cloud IPs. The local data service uses Tencent's quote
		// endpoint and is intentionally the reliable fallback for this route.
		if fallback, fallbackErr := fetchQuoteFromAkShareService(code); fallbackErr == nil && fallback != nil && fallback.Price > 0 {
			quoteData = fallback
			fetchErr = nil
			effectiveSource = "tencent"
		}
	}
	if fetchErr != nil || quoteData == nil || quoteData.Price <= 0 {
		if fetchErr == nil {
			fetchErr = fmt.Errorf("数据源未返回有效行情")
		}
		response.InternalError(c, "获取行情数据失败: "+fetchErr.Error())
		return
	}

	// Save to database
	quoteData.Source = effectiveSource
	quoteData.TradeDate = today
	repository.DB.Create(&quoteData)

	response.Success(c, quoteData)
}

// fetchQuoteFromAkShareService reads the Tencent-backed quote exposed by the
// optional Python data service. It is used as a fallback when a direct quote
// provider is unavailable, and works both locally and inside Docker.
func fetchQuoteFromAkShareService(code string) (*model.StockQuote, error) {
	baseURL := strings.TrimRight(getAkShareServiceURL(), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("AkShare 数据服务地址未配置")
	}

	reqURL := fmt.Sprintf("%s/stock_quote?code=%s", baseURL, url.QueryEscape(code))
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Get(reqURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("AkShare 返回 HTTP %d", resp.StatusCode)
	}

	var envelope struct {
		Code  int                    `json:"code"`
		Data  map[string]interface{} `json:"data"`
		Error string                 `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if envelope.Code != 0 || envelope.Data == nil {
		if envelope.Error != "" {
			return nil, errors.New(envelope.Error)
		}
		return nil, fmt.Errorf("AkShare 未返回行情")
	}

	quote := &model.StockQuote{
		Code:      jsonString(envelope.Data, "code", code),
		Name:      jsonString(envelope.Data, "name", code),
		Price:     jsonNumber(envelope.Data, "price"),
		PreClose:  jsonNumber(envelope.Data, "pre_close"),
		Open:      jsonNumber(envelope.Data, "open"),
		High:      jsonNumber(envelope.Data, "high"),
		Low:       jsonNumber(envelope.Data, "low"),
		Volume:    jsonNumber(envelope.Data, "volume"),
		Amount:    jsonNumber(envelope.Data, "amount"),
		Turnover:  jsonNumber(envelope.Data, "turnover"),
		MarketCap: jsonNumber(envelope.Data, "market_cap"),
		PE:        jsonNumber(envelope.Data, "pe"),
		PB:        jsonNumber(envelope.Data, "pb"),
		Source:    "tencent",
	}
	if quote.PreClose > 0 {
		quote.Change = quote.Price - quote.PreClose
		quote.ChangePct = quote.Change / quote.PreClose * 100
	}
	if quote.Price <= 0 {
		return nil, fmt.Errorf("AkShare 返回空行情")
	}
	return quote, nil
}

func jsonNumber(values map[string]interface{}, key string) float64 {
	value, ok := values[key]
	if !ok {
		return 0
	}
	switch number := value.(type) {
	case float64:
		return number
	case float32:
		return float64(number)
	case int:
		return float64(number)
	case json.Number:
		parsed, _ := number.Float64()
		return parsed
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(number), 64)
		return parsed
	default:
		return 0
	}
}

func jsonString(values map[string]interface{}, key, fallback string) string {
	if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func (h *Handler) GetKLine(c *gin.Context) {
	code := c.Query("code")
	period := c.DefaultQuery("period", "day")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "120"))

	if code == "" {
		response.BadRequest(c, "请提供股票代码")
		return
	}

	var klines []model.KLineData
	repository.DB.Where("code = ? AND period = ?", code, period).
		Order("trade_date desc").Limit(limit).Find(&klines)

	if len(klines) == 0 {
		// The k_line_data table is only filled by collectors whose upstream APIs
		// are unreachable here, so serve real bars straight from Tencent/Sina.
		bars, src, err := fetchPeriodKLineFallback(code, period, limit)
		if err == nil && len(bars) > 0 {
			for _, b := range bars {
				num := func(k string) float64 { v, _ := b[k].(float64); return v }
				klines = append(klines, model.KLineData{
					Code:      code,
					Period:    period,
					TradeDate: fmt.Sprintf("%v", b["date"]),
					Open:      num("open"),
					Close:     num("close"),
					High:      num("high"),
					Low:       num("low"),
					Volume:    num("volume"),
					Amount:    num("amount"),
				})
			}
			log.Printf("[KLine] %s(%s) 库内无历史，改用%s %d 根", code, period, src, len(klines))
		} else if err != nil {
			log.Printf("[KLine] %s(%s) 兜底K线失败: %v", code, period, err)
		}
	}

	response.Success(c, klines)
}

func (h *Handler) GetSectorList(c *gin.Context) {
	date := queryDate(c, "date")
	var sectors []model.SectorHeat
	repository.DB.Where("trade_date = ?", date).Order("net_flow desc").Find(&sectors)
	response.Success(c, sectors)
}

// fetchQuoteFromSource fetches real-time data from sina/tencent/eastmoney
func fetchQuoteFromSource(code, source string) (*model.StockQuote, error) {
	var url string
	switch source {
	case "sina":
		prefix := "sh"
		if strings.HasPrefix(code, "0") || strings.HasPrefix(code, "3") {
			prefix = "sz"
		}
		url = fmt.Sprintf("https://hq.sinajs.cn/list=%s%s", prefix, code)
	case "tencent":
		prefix := "sh"
		if strings.HasPrefix(code, "0") || strings.HasPrefix(code, "3") {
			prefix = "sz"
		}
		url = fmt.Sprintf("https://qt.gtimg.cn/q=%s%s", prefix, code)
	case "eastmoney":
		secid := "1." + code
		if strings.HasPrefix(code, "0") || strings.HasPrefix(code, "3") {
			secid = "0." + code
		}
		url = fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/get?secid=%s&fields=f43,f44,f45,f46,f47,f48,f50,f51,f52,f55,f57,f58,f60,f116,f117", secid)
	default:
		url = fmt.Sprintf("https://hq.sinajs.cn/list=sh%s", code)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Quote] %s 行情请求失败 code=%s: %v", source, code, err)
		return hqQuoteWithUlistFallback(code, source, &model.StockQuote{Code: code, Name: "数据获取中"}), nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	quote := parseQuoteResponse(code, source, string(body))
	if quote != nil && quote.Price > 0 {
		log.Printf("[Quote] code=%s 数据源=%s", code, source)
		return quote, nil
	}
	return hqQuoteWithUlistFallback(code, source, quote), nil
}

// hqQuoteWithUlistFallback keeps the quote route alive when api/qt/stock/get is
// unreachable: ulist.np/get runs on the same host but is not blocked. parsed is
// returned unchanged when even the fallback has nothing to offer.
func hqQuoteWithUlistFallback(code, source string, parsed *model.StockQuote) *model.StockQuote {
	fallback, err := hqQuoteFromUlist(code)
	if err == nil {
		log.Printf("[Quote] code=%s 数据源=eastmoney-ulist(兜底, 原数据源 %s 无有效行情)", code, source)
		return fallback
	}
	log.Printf("[Quote] code=%s ulist 兜底失败: %v", code, err)
	if parsed != nil {
		return parsed
	}
	return &model.StockQuote{Code: code, Name: "数据获取中"}
}

// hqQuoteFromUlist maps a ulist.np/get row onto the same StockQuote shape the
// direct sources produce (volume 手 / amount 元, as Eastmoney reports them).
func hqQuoteFromUlist(code string) (*model.StockQuote, error) {
	rows, err := fetchQuoteViaUlist([]string{code})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errNoUlistRows
	}
	row := rows[0]
	price := safeFloat(row, "close")
	if price <= 0 {
		return nil, fmt.Errorf("ulist 返回空行情")
	}
	name := safeString(row, "name")
	if name == "" {
		name = code
	}
	pure := safeString(row, "code")
	if pure == "" {
		pure = extractPureCode(code)
	}
	quote := &model.StockQuote{
		Code:      pure,
		Name:      name,
		Price:     price,
		Close:     price,
		High:      safeFloat(row, "high"),
		Low:       safeFloat(row, "low"),
		Open:      safeFloat(row, "open"),
		Volume:    safeFloat(row, "volume"),
		Amount:    safeFloat(row, "amount"),
		PreClose:  safeFloat(row, "pre_close"),
		Change:    safeFloat(row, "change"),
		ChangePct: safeFloat(row, "change_pct"),
		Turnover:  safeFloat(row, "turnover_rate"),
		MarketCap: safeFloat(row, "total_mv"),
		Source:    "eastmoney-ulist",
	}
	if quote.ChangePct == 0 && quote.PreClose > 0 {
		quote.Change = price - quote.PreClose
		quote.ChangePct = quote.Change / quote.PreClose * 100
	}
	return quote, nil
}

func parseQuoteResponse(code, source, body string) *model.StockQuote {
	quote := &model.StockQuote{Code: code}

	switch source {
	case "sina":
		// Parse sina format: var hq_str_sh600519="贵州茅台,1800.00,..."
		parts := strings.Split(body, "\"")
		if len(parts) >= 2 {
			fields := strings.Split(parts[1], ",")
			if len(fields) >= 32 {
				quote.Name = fields[0]
				quote.Open, _ = strconv.ParseFloat(fields[1], 64)
				quote.PreClose, _ = strconv.ParseFloat(fields[2], 64)
				quote.Price, _ = strconv.ParseFloat(fields[3], 64)
				quote.High, _ = strconv.ParseFloat(fields[4], 64)
				quote.Low, _ = strconv.ParseFloat(fields[5], 64)
				// Sina reports 成交量 in 股; StockQuote stores 手 like the other sources.
				quote.Volume, _ = strconv.ParseFloat(fields[8], 64)
				quote.Volume = quote.Volume / 100
				quote.Amount, _ = strconv.ParseFloat(fields[9], 64)
				if quote.PreClose > 0 {
					quote.Change = quote.Price - quote.PreClose
					quote.ChangePct = (quote.Change / quote.PreClose) * 100
				}
			}
		}
	case "eastmoney":
		var result map[string]interface{}
		if json.Unmarshal([]byte(body), &result) == nil {
			if data, ok := result["data"].(map[string]interface{}); ok {
				quote.Name, _ = data["f58"].(string)
				quote.Price, _ = data["f43"].(float64)
				quote.High, _ = data["f44"].(float64)
				quote.Low, _ = data["f45"].(float64)
				quote.Open, _ = data["f46"].(float64)
				quote.Volume, _ = data["f47"].(float64)
				quote.Amount, _ = data["f48"].(float64)
				quote.PreClose, _ = data["f60"].(float64)
				// Note: Eastmoney returns prices in yuan directly, no scaling needed
				if quote.PreClose > 0 {
					quote.Change = quote.Price - quote.PreClose
					quote.ChangePct = (quote.Change / quote.PreClose) * 100
				}
			}
		}
	case "tencent":
		// Tencent format: v_sh600519="1~name~code~price~pre_close~open~...
		// ~time~change~change_pct~high~low~...~amount~turnover~pe~...~pb~..."
		parts := strings.Split(strings.TrimSpace(body), "~")
		if len(parts) >= 35 {
			quote.Name = strings.Trim(strings.TrimSpace(parts[1]), "\"")
			quote.Price, _ = strconv.ParseFloat(parts[3], 64)
			quote.PreClose, _ = strconv.ParseFloat(parts[4], 64)
			quote.Open, _ = strconv.ParseFloat(parts[5], 64)
			quote.Volume, _ = strconv.ParseFloat(parts[6], 64)
			quote.Change, _ = strconv.ParseFloat(parts[31], 64)
			quote.ChangePct, _ = strconv.ParseFloat(parts[32], 64)
			quote.High, _ = strconv.ParseFloat(parts[33], 64)
			quote.Low, _ = strconv.ParseFloat(parts[34], 64)
			if len(parts) > 37 {
				// Tencent reports amount in 万元; StockQuote stores yuan.
				amount, _ := strconv.ParseFloat(parts[37], 64)
				quote.Amount = amount * 10000
			}
			if len(parts) > 38 {
				quote.Turnover, _ = strconv.ParseFloat(parts[38], 64)
			}
			if len(parts) > 39 {
				quote.PE, _ = strconv.ParseFloat(parts[39], 64)
			}
			if len(parts) > 44 {
				marketCap, _ := strconv.ParseFloat(parts[44], 64)
				quote.MarketCap = marketCap * 100000000
			}
			if len(parts) > 46 {
				quote.PB, _ = strconv.ParseFloat(parts[46], 64)
			}
		}
	default:
		quote.Name = "数据解析中"
	}

	return quote
}

// ==================== AI Agents ====================

func (h *Handler) ListAgents(c *gin.Context) {
	var agents []model.Agent
	repository.DB.Where("is_active = ?", true).Find(&agents)
	response.Success(c, agents)
}

func (h *Handler) GetAgent(c *gin.Context) {
	id := c.Param("id")
	var agent model.Agent
	if err := repository.DB.First(&agent, id).Error; err != nil {
		response.BadRequest(c, "智能体不存在")
		return
	}
	// Get skills
	var skills []model.Skill
	repository.DB.Joins("JOIN agent_skills ON agent_skills.skill_id = skills.id").
		Where("agent_skills.agent_id = ?", id).Find(&skills)

	response.Success(c, gin.H{"agent": agent, "skills": skills})
}

func (h *Handler) CreateAgent(c *gin.Context) {
	var agent model.Agent
	if err := c.ShouldBindJSON(&agent); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	userID, _ := c.Get("user_id")
	agent.CreatedBy = userID.(uint)
	repository.DB.Create(&agent)
	response.Success(c, agent)
}

func (h *Handler) UpdateAgent(c *gin.Context) {
	id := c.Param("id")
	var agent model.Agent
	if err := repository.DB.First(&agent, id).Error; err != nil {
		response.BadRequest(c, "智能体不存在")
		return
	}
	if err := c.ShouldBindJSON(&agent); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	repository.DB.Save(&agent)
	response.Success(c, agent)
}

func (h *Handler) DeleteAgent(c *gin.Context) {
	id := c.Param("id")
	repository.DB.Delete(&model.Agent{}, id)
	response.Success(c, nil)
}

// ==================== Skills ====================

func (h *Handler) ListSkills(c *gin.Context) {
	var skills []model.Skill
	repository.DB.Where("is_active = ?", true).Find(&skills)
	response.Success(c, skills)
}

func (h *Handler) GetAgentSkills(c *gin.Context) {
	id := c.Param("id")
	var skills []model.Skill
	repository.DB.Joins("JOIN agent_skills ON agent_skills.skill_id = skills.id").
		Where("agent_skills.agent_id = ?", id).Find(&skills)
	response.Success(c, skills)
}

func (h *Handler) BindAgentSkill(c *gin.Context) {
	var req struct {
		AgentID uint `json:"agent_id"`
		SkillID uint `json:"skill_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	repository.DB.Create(&model.AgentSkill{AgentID: req.AgentID, SkillID: req.SkillID})
	response.Success(c, nil)
}

// ==================== Conversations & Chat ====================

func (h *Handler) ListConversations(c *gin.Context) {
	userID, _ := c.Get("user_id")
	var convs []model.Conversation
	repository.DB.Where("user_id = ?", userID).Order("updated_at desc").Find(&convs)
	response.Success(c, convs)
}

func (h *Handler) CreateConversation(c *gin.Context) {
	var conv model.Conversation
	if err := c.ShouldBindJSON(&conv); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	userID, _ := c.Get("user_id")
	conv.UserID = userID.(uint)
	repository.DB.Create(&conv)
	response.Success(c, conv)
}

func (h *Handler) DeleteConversation(c *gin.Context) {
	id := c.Param("id")
	repository.DB.Where("conversation_id = ?", id).Delete(&model.Message{})
	repository.DB.Delete(&model.Conversation{}, id)
	response.Success(c, nil)
}

func (h *Handler) GetMessages(c *gin.Context) {
	convID := c.Param("id")
	var messages []model.Message
	repository.DB.Where("conversation_id = ?", convID).Order("created_at asc").Find(&messages)
	response.Success(c, messages)
}

func (h *Handler) SendMessage(c *gin.Context) {
	convID := c.Param("id")
	var req struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	cid, _ := strconv.ParseUint(convID, 10, 32)

	// Save user message
	userMsg := model.Message{
		ConversationID: uint(cid),
		Role:           "user",
		Content:        req.Content,
	}
	repository.DB.Create(&userMsg)

	// Get conversation and agent
	var conv model.Conversation
	repository.DB.First(&conv, cid)

	var agent model.Agent
	repository.DB.First(&agent, conv.AgentID)

	// Get agent skills for context
	var skills []model.Skill
	repository.DB.Joins("JOIN agent_skills ON agent_skills.skill_id = skills.id").
		Where("agent_skills.agent_id = ?", agent.ID).Find(&skills)

	// Get conversation history
	var history []model.Message
	repository.DB.Where("conversation_id = ?", cid).Order("created_at asc").Limit(20).Find(&history)

	// Call AI with Hermes agent pattern
	aiResponse := callAIAgent(agent, skills, history, req.Content)

	// Save AI response
	aiMsg := model.Message{
		ConversationID: uint(cid),
		Role:           "assistant",
		Content:        aiResponse,
	}
	repository.DB.Create(&aiMsg)

	response.Success(c, gin.H{
		"user_message": userMsg,
		"ai_message":   aiMsg,
	})
}

// callAIAgent uses Hermes Agent pattern to call AI
func callAIAgent(agent model.Agent, skills []model.Skill, history []model.Message, userMessage string) string {
	// Get default or agent-specific provider
	var provider model.AIProvider
	if err := repository.DB.Where("is_default = ? AND is_enabled = ?", true, true).First(&provider).Error; err != nil {
		return "未配置AI模型提供商，请在「AI模型管理」页面配置API密钥"
	}

	if provider.APIKey == "" {
		return "AI模型API密钥未配置，请在「AI模型管理」页面填写API密钥后使用"
	}

	// Build Hermes agent messages
	messages := []map[string]string{
		{"role": "system", "content": buildSystemPrompt(agent, skills)},
	}

	// Add conversation history
	for _, msg := range history {
		messages = append(messages, map[string]string{
			"role": msg.Role, "content": msg.Content,
		})
	}
	messages = append(messages, map[string]string{
		"role": "user", "content": userMessage,
	})

	// Call LLM API
	reqBody := map[string]interface{}{
		"model":       provider.Model,
		"messages":    messages,
		"temperature": agent.Temperature,
		"max_tokens":  agent.MaxTokens,
	}

	jsonBody, _ := json.Marshal(reqBody)
	url := strings.TrimSuffix(provider.BaseURL, "/") + "/chat/completions"

	client := &http.Client{Timeout: 60 * time.Second}
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(jsonBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("AI调用失败: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "AI响应解析失败"
	}

	// Extract response content
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := message["content"].(string); ok {
					return content
				}
			}
		}
	}

	return "AI响应为空，请检查模型配置"
}

func buildSystemPrompt(agent model.Agent, skills []model.Skill) string {
	prompt := agent.SystemPrompt + "\n\n"
	if len(skills) > 0 {
		prompt += "【已装备技能/工具】\n"
		for _, skill := range skills {
			prompt += fmt.Sprintf("- %s: %s\n", skill.Name, skill.Description)
			if skill.ToolDefs != "" {
				prompt += fmt.Sprintf("  工具定义: %s\n", skill.ToolDefs)
			}
		}
	}
	prompt += "\n【Hermes Agent协议】\n"
	prompt += "1. 你是一个Hermes Agent，具有自我改进能力和持久记忆\n"
	prompt += "2. 每次交互后，总结学到的经验用于改进后续回答\n"
	prompt += "3. 使用<tool_call>标签调用工具获取实时数据\n"
	prompt += "4. 所有数据必须来自外部数据源（Baostock/东财/新浪等），禁止编造\n"
	prompt += "5. 回答必须引用具体数据和分析系统\n"
	return prompt
}

// ==================== AI Providers ====================

func (h *Handler) ListAIProviders(c *gin.Context) {
	var providers []model.AIProvider
	if err := repository.DB.Find(&providers).Error; err != nil {
		response.InternalError(c, "读取AI提供商失败: "+err.Error())
		return
	}
	for i := range providers {
		providers[i].APIKey = maskSecret(providers[i].APIKey)
	}
	response.Success(c, providers)
}

type aiProviderUpdateRequest struct {
	APIKey    *string `json:"api_key"`
	BaseURL   *string `json:"base_url"`
	Model     *string `json:"model"`
	IsDefault *bool   `json:"is_default"`
	IsEnabled *bool   `json:"is_enabled"`
	Label     *string `json:"label"`
}

func (h *Handler) UpdateAIProvider(c *gin.Context) {
	id := c.Param("id")
	var provider model.AIProvider
	if err := repository.DB.First(&provider, id).Error; err != nil {
		response.BadRequest(c, "提供商不存在")
		return
	}

	var req aiProviderUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	updates := make(map[string]interface{})
	if req.APIKey != nil {
		apiKey := strings.TrimSpace(*req.APIKey)
		// The list endpoint masks keys. Keep the stored key when the mask is
		// submitted back while another provider field is being edited.
		if !isMaskedSecret(apiKey) {
			updates["api_key"] = apiKey
		} else {
			updates["api_key"] = provider.APIKey
		}
	}
	if req.BaseURL != nil {
		updates["base_url"] = strings.TrimSpace(*req.BaseURL)
	}
	if req.Model != nil {
		updates["model"] = strings.TrimSpace(*req.Model)
	}
	if req.IsDefault != nil {
		updates["is_default"] = *req.IsDefault
	}
	if req.IsEnabled != nil {
		updates["is_enabled"] = *req.IsEnabled
	}
	if req.Label != nil {
		updates["label"] = strings.TrimSpace(*req.Label)
	}
	if len(updates) == 0 {
		response.BadRequest(c, "没有需要更新的配置")
		return
	}

	if err := repository.DB.Model(&provider).Updates(updates).Error; err != nil {
		response.InternalError(c, "保存AI提供商失败: "+err.Error())
		return
	}

	// If setting as default, unset others
	if req.IsDefault != nil && *req.IsDefault {
		if err := repository.DB.Model(&model.AIProvider{}).Where("id != ?", id).Update("is_default", false).Error; err != nil {
			response.InternalError(c, "更新默认AI提供商失败: "+err.Error())
			return
		}
	}

	if err := repository.DB.First(&provider, id).Error; err != nil {
		response.InternalError(c, "读取已保存的AI提供商失败: "+err.Error())
		return
	}
	provider.APIKey = maskSecret(provider.APIKey)
	response.Success(c, provider)
}

func (h *Handler) TestAIProvider(c *gin.Context) {
	id := c.Param("id")
	var provider model.AIProvider
	if err := repository.DB.First(&provider, id).Error; err != nil {
		response.BadRequest(c, "提供商不存在")
		return
	}

	if provider.APIKey == "" {
		response.BadRequest(c, "请先配置API密钥")
		return
	}

	// Test connection
	reqBody := map[string]interface{}{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "回复OK"},
		},
		"max_tokens": 10,
	}
	jsonBody, _ := json.Marshal(reqBody)
	url := strings.TrimSuffix(provider.BaseURL, "/") + "/chat/completions"

	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(jsonBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		response.InternalError(c, "连接失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		response.Success(c, gin.H{"status": "connected", "message": "连接成功"})
	} else {
		body, _ := io.ReadAll(resp.Body)
		response.InternalError(c, fmt.Sprintf("连接失败(HTTP %d): %s", resp.StatusCode, string(body)))
	}
}

func (h *Handler) CreateAIProvider(c *gin.Context) {
	var provider model.AIProvider
	if err := c.ShouldBindJSON(&provider); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := repository.DB.Create(&provider).Error; err != nil {
		response.InternalError(c, "创建AI提供商失败: "+err.Error())
		return
	}
	provider.APIKey = maskSecret(provider.APIKey)
	response.Success(c, provider)
}

// ==================== Strategy Signals ====================

func (h *Handler) GetStrategySignals(c *gin.Context) {
	strategy := c.DefaultQuery("strategy", "")
	date := queryDate(c, "date")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	query := repository.DB.Where("trade_date = ?", date)
	if strategy != "" {
		query = query.Where("strategy_name = ?", strategy)
	}

	var signals []model.StrategySignal
	query.Order("score desc").Limit(limit).Find(&signals)
	response.Success(c, signals)
}

func (h *Handler) GetStrategyList(c *gin.Context) {
	strategies := []gin.H{
		{"name": "dragon_board", "label": "龙头打板", "description": "Dragon Score 11因子/130分涨停板量化评分", "icon": "Crown"},
		{"name": "strong_pullback", "label": "强势回踩", "description": "11因子/180分+龙虎榜游资确认", "icon": "ArrowDownUp"},
		{"name": "trend_core", "label": "趋势核心", "description": "趋势池融合buypoint信号+板块核心股票", "icon": "TrendingUp"},
		{"name": "event_burst", "label": "事件爆发", "description": "七步闭环+产业链4层分级选股", "icon": "Zap"},
		{"name": "concept_core", "label": "概念核心", "description": "概念生命周期跟踪+板块趋势共振", "icon": "Lightbulb"},
		{"name": "auction_pick", "label": "竞价选股", "description": "黄金公式高开3-5%+竞价占比8-12%", "icon": "Timer"},
		{"name": "group_hug", "label": "反复抱团", "description": "全市场5步漏斗+8因子画像+反包监控", "icon": "Users"},
		{"name": "pre_market", "label": "盘前精选", "description": "AI策略精选+收盘自动结算+命中率统计", "icon": "Star"},
		{"name": "micro_overnight", "label": "微盘隔夜", "description": "两级过滤+情绪门控+弹性立场WR 72%", "icon": "Moon"},
	}
	response.Success(c, strategies)
}

// ==================== Users ====================

// UserRequest is used for Create/Update user requests.
// The User model has Password json:"-" which prevents JSON binding,
// so we need a dedicated request struct that CAN bind the password field.
type UserRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	IsActive    *bool  `json:"is_active"` // pointer to distinguish false from absent
}

func (h *Handler) ListUsers(c *gin.Context) {
	var users []model.User
	repository.DB.Find(&users)
	response.Success(c, users)
}

func (h *Handler) CreateUser(c *gin.Context) {
	var req UserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	// Validate required fields
	if req.Username == "" {
		response.BadRequest(c, "用户名不能为空")
		return
	}
	if req.Password == "" {
		response.BadRequest(c, "密码不能为空")
		return
	}

	// Check for duplicate username
	var existing model.User
	if err := repository.DB.Where("username = ?", req.Username).First(&existing).Error; err == nil {
		response.BadRequest(c, "用户名已存在")
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		response.InternalError(c, "密码加密失败")
		return
	}

	// Default is_active to true if not specified
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	user := model.User{
		Username:    req.Username,
		Password:    string(hashedPassword),
		DisplayName: req.DisplayName,
		Email:       req.Email,
		Role:        req.Role,
		IsActive:    isActive,
	}
	if user.Role == "" {
		user.Role = "user"
	}

	if err := repository.DB.Create(&user).Error; err != nil {
		response.InternalError(c, "创建用户失败: "+err.Error())
		return
	}
	response.Success(c, user)
}

func (h *Handler) UpdateUser(c *gin.Context) {
	id := c.Param("id")
	var user model.User
	if err := repository.DB.First(&user, id).Error; err != nil {
		response.BadRequest(c, "用户不存在")
		return
	}
	var req UserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	updates := map[string]interface{}{
		"display_name": req.DisplayName,
		"email":        req.Email,
		"role":         req.Role,
	}
	// Only update is_active if explicitly provided
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if req.Password != "" {
		hp, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			response.InternalError(c, "密码加密失败")
			return
		}
		updates["password"] = string(hp)
	}
	repository.DB.Model(&user).Updates(updates)
	// Reload user to return fresh data
	repository.DB.First(&user, id)
	response.Success(c, user)
}

func (h *Handler) DeleteUser(c *gin.Context) {
	id := c.Param("id")
	repository.DB.Delete(&model.User{}, id)
	response.Success(c, nil)
}

// ==================== Audit Logs ====================

func (h *Handler) GetAuditLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	module := c.DefaultQuery("module", "")
	action := c.DefaultQuery("action", "")
	username := c.DefaultQuery("username", "")

	query := repository.DB.Model(&model.AuditLog{})
	if module != "" {
		query = query.Where("module = ?", module)
	}
	if action != "" {
		query = query.Where("action = ?", action)
	}
	if username != "" {
		query = query.Where("username LIKE ?", "%"+username+"%")
	}

	var total int64
	query.Count(&total)

	var logs []model.AuditLog
	query.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs)

	response.Success(c, gin.H{
		"total": total,
		"page":  page,
		"items": logs,
	})
}

// ==================== Utility: unused import fix ====================
var _ = gorm.ErrRecordNotFound

// ==================== Stock Picks (今日推荐) ====================

// CreateStockPick - Admin creates a stock recommendation
func (h *Handler) CreateStockPick(c *gin.Context) {
	var req struct {
		Code          string  `json:"code" binding:"required"`
		Name          string  `json:"name"`
		AttentionLow  float64 `json:"attention_low"`
		AttentionHigh float64 `json:"attention_high"`
		TargetLow     float64 `json:"target_low"`
		TargetHigh    float64 `json:"target_high"`
		Reason        string  `json:"reason"`
		PickDate      string  `json:"pick_date"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误: 请提供股票代码")
		return
	}

	userID, _ := c.Get("user_id")

	if req.PickDate == "" {
		req.PickDate = time.Now().Format("2006-01-02")
	}

	// Auto-fetch stock name from Eastmoney if not provided
	if req.Name == "" {
		req.Name = req.Code
	}

	pick := model.StockPick{
		Code:          req.Code,
		Name:          req.Name,
		AttentionLow:  req.AttentionLow,
		AttentionHigh: req.AttentionHigh,
		TargetLow:     req.TargetLow,
		TargetHigh:    req.TargetHigh,
		Reason:        req.Reason,
		PickDate:      req.PickDate,
		CreatedBy:     userID.(uint),
		IsActive:      true,
	}

	if err := repository.DB.Create(&pick).Error; err != nil {
		response.InternalError(c, "创建推荐失败")
		return
	}

	response.Success(c, pick)
}

// UpdateStockPick - Admin updates a stock recommendation
func (h *Handler) UpdateStockPick(c *gin.Context) {
	id := c.Param("id")
	var pick model.StockPick
	if err := repository.DB.First(&pick, id).Error; err != nil {
		response.BadRequest(c, "推荐记录不存在")
		return
	}

	var req struct {
		Code          string  `json:"code"`
		Name          string  `json:"name"`
		AttentionLow  float64 `json:"attention_low"`
		AttentionHigh float64 `json:"attention_high"`
		TargetLow     float64 `json:"target_low"`
		TargetHigh    float64 `json:"target_high"`
		Reason        string  `json:"reason"`
		PickDate      string  `json:"pick_date"`
		IsActive      *bool   `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	updates := map[string]interface{}{}
	if req.Code != "" {
		updates["code"] = req.Code
	}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.AttentionLow > 0 {
		updates["attention_low"] = req.AttentionLow
	}
	if req.AttentionHigh > 0 {
		updates["attention_high"] = req.AttentionHigh
	}
	if req.TargetLow > 0 {
		updates["target_low"] = req.TargetLow
	}
	if req.TargetHigh > 0 {
		updates["target_high"] = req.TargetHigh
	}
	if req.Reason != "" {
		updates["reason"] = req.Reason
	}
	if req.PickDate != "" {
		updates["pick_date"] = req.PickDate
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}

	repository.DB.Model(&pick).Updates(updates)
	repository.DB.First(&pick, id)
	response.Success(c, pick)
}

// DeleteStockPick - Admin deletes a stock recommendation
func (h *Handler) DeleteStockPick(c *gin.Context) {
	id := c.Param("id")
	if err := repository.DB.Delete(&model.StockPick{}, id).Error; err != nil {
		response.InternalError(c, "删除失败")
		return
	}
	response.Success(c, nil)
}

// ListStockPicks - Admin lists all stock picks with filters
func (h *Handler) ListStockPicks(c *gin.Context) {
	date := c.DefaultQuery("date", "")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))

	query := repository.DB.Model(&model.StockPick{})
	if date != "" {
		query = query.Where("pick_date = ?", date)
	}

	var total int64
	query.Count(&total)

	var picks []model.StockPick
	query.Order("pick_date DESC, created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&picks)

	response.Success(c, gin.H{
		"total": total,
		"page":  page,
		"items": picks,
	})
}

// GetTodayPicks - All authenticated users: get today's active recommendations
func (h *Handler) GetTodayPicks(c *gin.Context) {
	today := time.Now().Format("2006-01-02")
	date := c.DefaultQuery("date", today)

	var picks []model.StockPick
	repository.DB.Where("pick_date = ? AND is_active = ?", date, true).
		Order("created_at DESC").Find(&picks)

	response.Success(c, gin.H{
		"date":  date,
		"items": picks,
		"count": len(picks),
	})
}
