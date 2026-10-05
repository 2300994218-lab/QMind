package handler

// Reachable fallbacks for the Eastmoney endpoints this machine cannot open:
// api/qt/clist/get (full-market lists), api/qt/stock/get (single quote) and
// push2his kline/get (history) all reset the TCP connection, while ulist.np,
// trends2 and fflow still answer.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

const sinaUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func sinaSymbol(code string) string {
	pure := extractPureCode(code)
	if strings.HasPrefix(pure, "6") {
		return "sh" + pure
	}
	return "sz" + pure
}

func getJSON(url, referer string, timeout time.Duration, out interface{}) error {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", sinaUA)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 || len(body) == 0 || isHTMLResponse(body) {
		return fmt.Errorf("HTTP %d, %d 字节异常响应", resp.StatusCode, len(body))
	}
	return json.Unmarshal(body, out)
}

// fetchSinaDailyKLine returns daily bars oldest-first. volume is in 股.
func fetchSinaDailyKLine(code string, limit int) ([]gin.H, error) {
	url := fmt.Sprintf("https://quotes.sina.cn/cn/api/json_v2.php/CN_MarketDataService.getKLineData?symbol=%s&scale=240&ma=no&datalen=%d",
		sinaSymbol(code), limit)
	var rows []map[string]interface{}
	if err := getJSON(url, "https://finance.sina.com.cn", 15*time.Second, &rows); err != nil {
		return nil, fmt.Errorf("新浪K线: %v", err)
	}
	return barsFromSinaRows(rows), nil
}

func barsFromSinaRows(rows []map[string]interface{}) []gin.H {
	bars := make([]gin.H, 0, len(rows))
	prevClose := 0.0
	for _, r := range rows {
		closePrice := safeFloat(r, "close")
		if closePrice <= 0 {
			continue
		}
		date := safeString(r, "day")
		chg := 0.0
		if prevClose > 0 {
			chg = (closePrice - prevClose) / prevClose * 100
		}
		bars = append(bars, gin.H{
			"date":       date,
			"open":       safeFloat(r, "open"),
			"close":      closePrice,
			"high":       safeFloat(r, "high"),
			"low":        safeFloat(r, "low"),
			"volume":     safeFloat(r, "volume"),
			"amount":     0.0, // 新浪日K不提供成交额，宁缺勿造
			"change_pct": math.Round(chg*100) / 100,
			"turnover":   0.0,
		})
		prevClose = closePrice
	}
	return bars
}

// fetchTencentKLine pulls qfq bars of cat (day|week|month), oldest-first.
func fetchTencentKLine(code, cat string, limit int) ([]gin.H, error) {
	sym := sinaSymbol(code)
	url := fmt.Sprintf("https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=%s,%s,,,%d,qfq", sym, cat, limit)
	var raw struct {
		Code int                                   `json:"code"`
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	if err := getJSON(url, "https://gu.qq.com", 15*time.Second, &raw); err != nil {
		return nil, fmt.Errorf("腾讯K线: %v", err)
	}
	node, ok := raw.Data[sym]
	if !ok {
		return nil, fmt.Errorf("腾讯K线: 无 %s 数据", sym)
	}
	arr := node[cat+"qfqday"]
	if len(arr) == 0 {
		arr = node["qfq"+cat+"day"]
	}
	if len(arr) == 0 {
		arr = node[cat+"day"]
	}
	var rows [][]string
	if err := json.Unmarshal(arr, &rows); err != nil {
		return nil, fmt.Errorf("腾讯K线解析失败: %v", err)
	}
	prevClose := 0.0
	bars := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		open, _ := strconv.ParseFloat(r[1], 64)
		closePrice, _ := strconv.ParseFloat(r[2], 64)
		high, _ := strconv.ParseFloat(r[3], 64)
		low, _ := strconv.ParseFloat(r[4], 64)
		volume, _ := strconv.ParseFloat(r[5], 64)
		if closePrice <= 0 {
			continue
		}
		chg := 0.0
		if prevClose > 0 {
			chg = (closePrice - prevClose) / prevClose * 100
		} else if len(r) >= 9 {
			chg, _ = strconv.ParseFloat(r[8], 64)
		}
		bars = append(bars, gin.H{
			"date":       r[0],
			"open":       open,
			"close":      closePrice,
			"high":       high,
			"low":        low,
			"volume":     volume * 100, // 手 -> 股
			"amount":     0.0,          // 腾讯日K字段无成交额
			"change_pct": math.Round(chg*100) / 100,
			"turnover":   0.0,
		})
		prevClose = closePrice
	}
	if len(bars) == 0 {
		return nil, fmt.Errorf("腾讯K线: %s 无数据", sym)
	}
	return bars, nil
}

// fetchPeriodKLineFallback serves period bars from Tencent/Sina when the blocked
// Eastmoney history API returns nothing. Minute periods are not covered here.
func fetchPeriodKLineFallback(code, period string, limit int) ([]gin.H, string, error) {
	if limit <= 0 {
		limit = 120
	}
	switch period {
	case "day", "daily", "101", "":
		if bars, err := fetchTencentKLine(code, "day", limit); err == nil {
			return bars, "腾讯K线", nil
		}
		bars, err := fetchSinaDailyKLine(code, limit)
		if err != nil {
			return nil, "", err
		}
		return bars, "新浪K线", nil
	case "week", "weekly", "102":
		bars, err := fetchTencentKLine(code, "week", limit)
		return bars, "腾讯K线(周)", err
	case "month", "monthly", "103":
		bars, err := fetchTencentKLine(code, "month", limit)
		return bars, "腾讯K线(月)", err
	default:
		return nil, "", fmt.Errorf("分钟级K线暂无可用兜底数据源")
	}
}

// ==================== Single-stock quote ====================

// errNoUlistRows means the quote source replied but knows none of the codes.
var errNoUlistRows = errors.New("ulist 无匹配行情")

// fetchQuoteViaUlist replaces the blocked api/qt/stock/get for one or more codes;
// ulist.np/get is served by the same host but is not blocked.
func fetchQuoteViaUlist(codes []string) ([]gin.H, error) {
	secids := make([]string, 0, len(codes))
	for _, c := range codes {
		pure := extractPureCode(c)
		if len(pure) != 6 {
			continue
		}
		market := "0"
		if strings.HasPrefix(pure, "6") || strings.HasPrefix(pure, "5") || strings.HasPrefix(pure, "9") {
			market = "1"
		}
		secids = append(secids, fmt.Sprintf("%s.%s", market, pure))
	}
	if len(secids) == 0 {
		return nil, fmt.Errorf("无有效股票代码")
	}
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/ulist.np/get?secids=%s&fltt=2&invt=2&fields=f2,f3,f4,f5,f6,f7,f8,f12,f14,f15,f16,f17,f18,f20,f21,f100,f60",
		strings.Join(secids, ","))
	var raw map[string]interface{}
	if err := getJSON(url, "", 10*time.Second, &raw); err != nil {
		return nil, err
	}
	data, _ := raw["data"].(map[string]interface{})
	if data == nil {
		// The source answered; it simply has no quote for these codes.
		return nil, errNoUlistRows
	}
	diff, _ := data["diff"].([]interface{})
	out := make([]gin.H, 0, len(diff))
	for _, it := range diff {
		d, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		out = append(out, gin.H{
			"code":          safeString(d, "f12"),
			"name":          safeString(d, "f14"),
			"close":         safeFloat(d, "f2"),
			"change_pct":    safeFloat(d, "f3"),
			"change":        safeFloat(d, "f4"),
			"volume":        safeFloat(d, "f5"),
			"amount":        safeFloat(d, "f6"),
			"amplitude":     safeFloat(d, "f7"),
			"turnover_rate": safeFloat(d, "f8"),
			"high":          safeFloat(d, "f15"),
			"low":           safeFloat(d, "f16"),
			"open":          safeFloat(d, "f17"),
			"pre_close":     safeFloat(d, "f18"),
			"total_mv":      safeFloat(d, "f20"),
			"circ_mv":       safeFloat(d, "f21"),
			"industry":      safeString(d, "f100"),
		})
	}
	if len(out) == 0 {
		return nil, errNoUlistRows
	}
	return out, nil
}

// ==================== Cached full-market snapshot ====================

var (
	snapshotMutex  sync.Mutex
	snapshotAt     time.Time
	snapshotStocks []dailyStock
	snapshotErr    error
)

// sinaMarketSnapshot caches the ~5200-row Sina A-share list so dashboard widgets
// that only need change/turnover/mktcap share one slow fetch.
func sinaMarketSnapshot(ttl time.Duration) ([]dailyStock, error) {
	snapshotMutex.Lock()
	defer snapshotMutex.Unlock()
	if len(snapshotStocks) > 0 && time.Since(snapshotAt) < ttl {
		return snapshotStocks, nil
	}
	stocks, err := fetchFromSinaDaily()
	if err != nil {
		snapshotErr = err
		return nil, err
	}
	snapshotStocks = stocks
	snapshotAt = time.Now()
	snapshotErr = nil
	return snapshotStocks, nil
}

// distributionBuckets maps change percentages onto the 9 buckets the dashboard chart expects.
func distributionBuckets(pcts []float64) []gin.H {
	var limitUp, up5, up1to5, up0to1, flat, down0to1, down1to5, down5, limitDown int
	for _, pct := range pcts {
		switch {
		case pct >= 9.5:
			limitUp++
		case pct >= 5:
			up5++
		case pct >= 1:
			up1to5++
		case pct > 0:
			up0to1++
		case pct == 0:
			flat++
		case pct > -1:
			down0to1++
		case pct > -5:
			down1to5++
		case pct > -9.5:
			down5++
		default:
			limitDown++
		}
	}
	total := limitUp + up5 + up1to5 + up0to1 + flat + down0to1 + down1to5 + down5 + limitDown
	if total == 0 {
		return nil
	}
	return []gin.H{
		{"label": "涨幅≥9.5%", "value": limitUp, "color": "#DC2626"},
		{"label": "9.5%~5%", "value": up5, "color": "#EF4444"},
		{"label": "5%~1%", "value": up1to5, "color": "#F87171"},
		{"label": "1%~0%", "value": up0to1, "color": "#FCA5A5"},
		{"label": "平盘", "value": flat, "color": "#D1D5DB"},
		{"label": "0%~-1%", "value": down0to1, "color": "#86EFAC"},
		{"label": "-1%~-5%", "value": down1to5, "color": "#4ADE80"},
		{"label": "-5%~-9.5%", "value": down5, "color": "#22C55E"},
		{"label": "跌幅≤-9.5%", "value": limitDown, "color": "#16A34A"},
	}
}

func fetchDistributionFromSina() []gin.H {
	stocks, err := sinaMarketSnapshot(3 * time.Minute)
	if err != nil {
		log.Printf("[Distribution] 新浪快照失败: %v", err)
		return nil
	}
	pcts := make([]float64, 0, len(stocks))
	for _, s := range stocks {
		pcts = append(pcts, s.PctChg)
	}
	d := distributionBuckets(pcts)
	log.Printf("[Distribution] 新浪快照分桶: %d 只", len(pcts))
	return d
}

// stocksToRows renders dailyStock rows in the shape the limit-stock lists return.
func stocksToRows(stocks []dailyStock) []gin.H {
	rows := make([]gin.H, 0, len(stocks))
	for _, s := range stocks {
		rows = append(rows, gin.H{
			"code":          extractPureCode(s.Code),
			"name":          s.Name,
			"close":         s.Close,
			"change_pct":    s.PctChg,
			"turnover_rate": s.TurnoverRate,
			"volume_ratio":  s.VolumeRatio,
			"amount":        s.Amount,
			"total_mv":      s.TotalMV,
			"circ_mv":       s.CircMV,
			"industry":      s.Industry,
		})
	}
	return rows
}

// fetchLimitUpDownFromSina classifies 涨停/跌停 from the Sina snapshot, using the
// exchange rounding rule instead of a fixed 9.9% cut-off.
func fetchLimitUpDownFromSina() (gin.H, gin.H, error) {
	stocks, err := sinaMarketSnapshot(3 * time.Minute)
	if err != nil {
		return nil, nil, err
	}
	var ups, downs []dailyStock
	for _, s := range stocks {
		if s.PreClose <= 0 {
			continue
		}
		limitUp := roundTo2(s.PreClose*1.1) - 0.001
		limitDown := roundTo2(s.PreClose*0.9) + 0.001
		if s.Close >= limitUp {
			ups = append(ups, s)
		} else if s.Close <= limitDown {
			downs = append(downs, s)
		}
	}
	return gin.H{"total": len(ups), "stocks": stocksToRows(ups)},
		gin.H{"total": len(downs), "stocks": stocksToRows(downs)}, nil
}

func roundTo2(v float64) float64 {
	return math.Round(v*100) / 100
}

// ==================== Real limit-up history ====================

// limitUpCodesFromKLines checks each candidate's recent daily bars for a true
// 主板 10% limit-up close. Used by the overnight screener's Step 3, whose Eastmoney
// history source is blocked.
func limitUpCodesFromKLines(codes []string, lookbackDays int) map[string]bool {
	result := make(map[string]bool)
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup

	for _, code := range codes {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			bars, _, err := fetchPeriodKLineFallback(c, "day", lookbackDays+1)
			if err != nil || len(bars) < 2 {
				return
			}
			for i := 1; i < len(bars); i++ {
				prevClose, _ := bars[i-1]["close"].(float64)
				closePrice, _ := bars[i]["close"].(float64)
				if prevClose <= 0 || closePrice <= 0 {
					continue
				}
				if closePrice >= roundTo2(prevClose*1.1)-0.001 {
					mu.Lock()
					result[extractPureCode(c)] = true
					mu.Unlock()
					return
				}
			}
		}(code)
	}
	wg.Wait()
	return result
}

// ==================== Industry / concept boards ====================

// Sina board lists are the only reachable 板块 source here (Eastmoney's m:90 clist
// is blocked). Bodies are GBK, hence the explicit decode.
var sinaBoardURLs = map[string]string{
	"sector":  "https://vip.stock.finance.sina.com.cn/q/view/newSinaHy.php",
	"concept": "https://vip.stock.finance.sina.com.cn/q/view/newFLJK.php?param=class",
}

func decodeGBK(body []byte) (string, error) {
	decoded, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), body)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// fetchSinaBoards returns rows shaped like fetchSectorOrConceptFromEastmoney.
// Sina publishes no fund flow, so net_flow/flow_in/flow_out/net_pct stay zero.
func fetchSinaBoards(category string) []gin.H {
	url, ok := sinaBoardURLs[category]
	if !ok {
		return nil
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", sinaUA)
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[板块] 新浪 %s 请求失败: %v", category, err)
		return nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || len(raw) == 0 {
		return nil
	}
	text, err := decodeGBK(raw)
	if err != nil {
		log.Printf("[板块] 新浪 %s GBK 解码失败: %v", category, err)
		return nil
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		log.Printf("[板块] 新浪 %s 响应格式异常", category)
		return nil
	}
	var entries map[string]string
	if err := json.Unmarshal([]byte(text[start:end+1]), &entries); err != nil {
		log.Printf("[板块] 新浪 %s 解析失败: %v", category, err)
		return nil
	}

	items := make([]gin.H, 0, len(entries))
	for _, v := range entries {
		f := strings.Split(v, ",")
		if len(f) < 13 {
			continue
		}
		pct, _ := strconv.ParseFloat(f[5], 64)
		amount, _ := strconv.ParseFloat(f[7], 64)
		avgPrice, _ := strconv.ParseFloat(f[3], 64)
		leadPct, _ := strconv.ParseFloat(f[9], 64)
		items = append(items, gin.H{
			"name":        f[1],
			"code":        f[0],
			"change_pct":  pct,
			"net_flow":    0.0,
			"flow_in":     0.0,
			"flow_out":    0.0,
			"net_pct":     0.0,
			"lead_stock":  f[12],
			"lead_pct":    leadPct,
			"stock_count": f[2],
			"price":       avgPrice,
			"amount":      amount / 100000000, // 元 -> 亿
			"source":      "新浪板块",
		})
	}
	sort.Slice(items, func(i, j int) bool {
		a, _ := items[i]["change_pct"].(float64)
		b, _ := items[j]["change_pct"].(float64)
		return a > b
	})
	log.Printf("[板块] 新浪 %s 板块 %d 个", category, len(items))
	return items
}

// ==================== 资讯 / 公告 / 研报 兜底 ====================

// eastJSON fetches a URL whose payload may be wrapped in a JSONP callback.
func eastJSON(url, referer string, timeout time.Duration, out interface{}) error {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", sinaUA)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	payload := string(body)
	if i := strings.IndexByte(payload, '{'); i > 0 {
		payload = payload[i:]
	}
	if j := strings.LastIndexByte(payload, '}'); j >= 0 && j < len(payload)-1 {
		payload = payload[:j+1]
	}
	if resp.StatusCode != 200 || isHTMLResponse([]byte(payload)) {
		return fmt.Errorf("HTTP %d, %d 字节异常响应", resp.StatusCode, len(body))
	}
	return json.Unmarshal([]byte(payload), out)
}

var emHighlightTags = []string{"<em>", "</em>"}

func stripEmTags(s string) string {
	for _, tag := range emHighlightTags {
		s = strings.ReplaceAll(s, tag, "")
	}
	return s
}

func truncateRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "..."
}

// fetchNewsFromEastmoneySearch returns news items shaped like the old
// searchapi response: {title, summary, source, date, link}.
func fetchNewsFromEastmoneySearch(keyword string, limit int) []gin.H {
	if limit <= 0 || limit > 30 {
		limit = 8
	}
	param := fmt.Sprintf(`{"uid":"","keyword":"%s","type":["cmsArticleWebOld"],"client":"web","clientType":"web","clientVersion":"curr","param":{"cmsArticleWebOld":{"searchScope":"default","sort":"default","pageIndex":1,"pageSize":%d}}}`, keyword, limit)
	reqURL := "https://search-api-web.eastmoney.com/search/jsonp?cb=jQuery&param=" + url.QueryEscape(param)

	var out struct {
		Result struct {
			Articles []map[string]interface{} `json:"cmsArticleWebOld"`
		} `json:"result"`
	}
	if err := eastJSON(reqURL, "https://so.eastmoney.com/", 12*time.Second, &out); err != nil {
		log.Printf("[兜底] 东财新闻搜索失败: %v", err)
		return nil
	}

	news := make([]gin.H, 0, len(out.Result.Articles))
	for _, m := range out.Result.Articles {
		title := strings.TrimSpace(stripEmTags(safeString(m, "title")))
		if title == "" {
			continue
		}
		news = append(news, gin.H{
			"title":   title,
			"summary": truncateRunes(stripEmTags(safeString(m, "content")), 80),
			"source":  safeString(m, "mediaName"),
			"date":    safeString(m, "date"),
			"link":    safeString(m, "url"),
		})
	}
	if len(news) > 0 {
		log.Printf("[兜底] 东财新闻搜索 %q 命中 %d 条", keyword, len(news))
	}
	return news
}

// fetchAnnounceFromEastmoney returns announcements for a pure stock code.
func fetchAnnounceFromEastmoney(code string, limit int) []gin.H {
	pure := extractPureCode(code)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	reqURL := fmt.Sprintf("https://np-anotice-stock.eastmoney.com/api/security/ann?sr=-1&page_size=%d&page_index=1&ann_type=A&client_source=web&stock_list=%s", limit, pure)

	var out struct {
		Data struct {
			List []map[string]interface{} `json:"list"`
		} `json:"data"`
	}
	if err := eastJSON(reqURL, "https://data.eastmoney.com/notices/", 12*time.Second, &out); err != nil {
		log.Printf("[兜底] 东财公告失败 %s: %v", pure, err)
		return nil
	}

	items := make([]gin.H, 0, len(out.Data.List))
	for _, m := range out.Data.List {
		title := strings.TrimSpace(safeString(m, "title"))
		date := safeString(m, "notice_date")
		if title == "" {
			continue
		}
		artCode := safeString(m, "art_code")
		items = append(items, gin.H{
			"title":             title,
			"announcementTitle": title,
			"date":              date,
			"announcementTime":  date,
			"link":              fmt.Sprintf("https://data.eastmoney.com/notices/detail/%s/%s.html", pure, artCode),
		})
	}
	if len(items) > 0 {
		log.Printf("[兜底] 东财公告 %s 命中 %d 条", pure, len(items))
	}
	return items
}

// fetchResearchFromEastmoney returns sell-side reports for a pure stock code.
// A nil error with no items means the source answered and simply has no report,
// which is not the same as the source being unreachable.
func fetchResearchFromEastmoney(code string, limit int) ([]gin.H, error) {
	pure := extractPureCode(code)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	end := time.Now().Format("2006-01-02")
	begin := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	reqURL := fmt.Sprintf("https://reportapi.eastmoney.com/report/list?industryCode=*&pageSize=%d&industry=*&rating=&ratingChange=&beginTime=%s&endTime=%s&pageNo=1&qType=0&code=%s", limit, begin, end, pure)

	var out struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := eastJSON(reqURL, "https://data.eastmoney.com/report/", 12*time.Second, &out); err != nil {
		log.Printf("[兜底] 东财研报失败 %s: %v", pure, err)
		return nil, err
	}

	items := make([]gin.H, 0, len(out.Data))
	for _, m := range out.Data {
		title := strings.TrimSpace(safeString(m, "title"))
		if title == "" {
			continue
		}
		infoCode := safeString(m, "infoCode")
		items = append(items, gin.H{
			"title":        title,
			"report_title": title,
			"stock_name":   safeString(m, "stockName"),
			"author":       safeString(m, "orgSName"),
			"researcher":   safeString(m, "researcher"),
			"rating":       safeString(m, "emRatingName"),
			"date":         safeString(m, "publishDate"),
			"publish_time": safeString(m, "publishDate"),
			"link":         fmt.Sprintf("https://data.eastmoney.com/report/zw_stock.jshtml?infocode=%s", infoCode),
		})
	}
	if len(items) > 0 {
		log.Printf("[兜底] 东财研报 %s 命中 %d 篇", pure, len(items))
	}
	return items, nil
}

func f10MarketCode(code string) string {
	pure := extractPureCode(code)
	switch {
	case strings.HasPrefix(pure, "6"):
		return "SH" + pure
	case strings.HasPrefix(pure, "8"), strings.HasPrefix(pure, "4"):
		return "BJ" + pure
	default:
		return "SZ" + pure
	}
}

// fetchF10FromEastmoney returns the company profile behind the F10 panel.
func fetchF10FromEastmoney(code string) gin.H {
	reqURL := fmt.Sprintf("https://emweb.securities.eastmoney.com/PC_HSF10/CompanySurvey/PageAjax?code=%s", f10MarketCode(code))
	var out struct {
		Profile   []map[string]interface{} `json:"jbzl"`
		IssueInfo []map[string]interface{} `json:"fxxg"`
	}
	if err := eastJSON(reqURL, "https://emweb.securities.eastmoney.com/", 12*time.Second, &out); err != nil {
		log.Printf("[兜底] 东财F10失败 %s: %v", code, err)
		return nil
	}
	if len(out.Profile) == 0 {
		return nil
	}
	b := out.Profile[0]
	info := gin.H{
		"code":         safeString(b, "SECURITY_CODE"),
		"name":         safeString(b, "SECURITY_NAME_ABBR"),
		"full_name":    safeString(b, "ORG_NAME"),
		"en_name":      safeString(b, "ORG_NAME_EN"),
		"market":       safeString(b, "TRADE_MARKET"),
		"industry":     safeString(b, "INDUSTRYCSRC1"),
		"province":     safeString(b, "PROVINCE"),
		"chairman":     safeString(b, "CHAIRMAN"),
		"president":    safeString(b, "PRESIDENT"),
		"secretary":    safeString(b, "SECRETARY"),
		"legal_person": safeString(b, "LEGAL_PERSON"),
		"reg_capital":  safeString(b, "REG_CAPITAL"),
		"employees":    safeString(b, "EMP_NUM"),
		"address":      safeString(b, "ADDRESS"),
		"website":      safeString(b, "ORG_WEB"),
		"tel":          safeString(b, "ORG_TEL"),
		"email":        safeString(b, "ORG_EMAIL"),
		"profile":      safeString(b, "ORG_PROFILE"),
		"business":     safeString(b, "BUSINESS_SCOPE"),
		"data_source":  "eastmoney-f10",
	}
	if len(out.IssueInfo) > 0 {
		f := out.IssueInfo[0]
		info["found_date"] = safeString(f, "FOUND_DATE")
		info["listing_date"] = safeString(f, "LISTING_DATE")
		info["issue_price"] = safeFloat(f, "ISSUE_PRICE")
		info["total_funds"] = safeFloat(f, "TOTAL_FUNDS")
	}
	log.Printf("[兜底] 东财F10 %s -> %s", info["code"], info["name"])
	return info
}
