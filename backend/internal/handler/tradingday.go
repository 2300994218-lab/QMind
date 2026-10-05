package handler

// Trading-day resolution driven by market data instead of a guessed holiday table:
// the last daily bar of the SSE Composite says which day the exchange actually
// traded last, so 节假日/调休 need no local calendar.

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	latestTradeDateMutex  sync.Mutex
	latestTradeDate       string
	latestTradeDateAt     time.Time
	latestTradeDateErr    error
	latestTradeDateFailAt time.Time
)

// getLatestTradingDate returns YYYYMMDD of the most recent published index daily bar.
func getLatestTradingDate() (string, error) {
	latestTradeDateMutex.Lock()
	defer latestTradeDateMutex.Unlock()

	if latestTradeDate != "" && time.Since(latestTradeDateAt) < 5*time.Minute {
		return latestTradeDate, nil
	}
	// Both sources failing costs ~20s, so do not retry on every dashboard request.
	if latestTradeDateErr != nil && time.Since(latestTradeDateFailAt) < 2*time.Minute {
		return "", latestTradeDateErr
	}

	date, err := latestTradingDateFromTencent()
	if err != nil {
		log.Printf("[交易日] 腾讯指数日K失败: %v, 改用新浪", err)
		date, err = latestTradingDateFromSina()
	}
	if err != nil {
		latestTradeDateErr = err
		latestTradeDateFailAt = time.Now()
		return "", err
	}
	latestTradeDate = date
	latestTradeDateAt = time.Now()
	latestTradeDateErr = nil
	return latestTradeDate, nil
}

func latestTradingDateFromTencent() (string, error) {
	// data.sh000001 also carries an "qt" quote object, so decode per key instead
	// of assuming every field is a bar array.
	var raw struct {
		Code int                                   `json:"code"`
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	url := "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=sh000001,day,,,12,"
	if err := getJSON(url, "https://gu.qq.com", 10*time.Second, &raw); err != nil {
		return "", err
	}
	dayRaw, ok := raw.Data["sh000001"]["day"]
	if !ok {
		return "", fmt.Errorf("腾讯指数日K为空")
	}
	var bars [][]string
	if err := json.Unmarshal(dayRaw, &bars); err != nil {
		return "", fmt.Errorf("腾讯指数日K解析失败: %v", err)
	}
	if len(bars) == 0 || len(bars[len(bars)-1]) == 0 {
		return "", fmt.Errorf("腾讯指数日K缺少日期字段")
	}
	return normalizeDateDigits(bars[len(bars)-1][0]), nil
}

func latestTradingDateFromSina() (string, error) {
	var rows []map[string]interface{}
	url := "https://quotes.sina.cn/cn/api/json_v2.php/CN_MarketDataService.getKLineData?symbol=sh000001&scale=240&ma=no&datalen=12"
	if err := getJSON(url, "https://finance.sina.com.cn", 10*time.Second, &rows); err != nil {
		return "", err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		d := normalizeDateDigits(safeString(rows[i], "day"))
		if d != "" {
			return d, nil
		}
	}
	return "", fmt.Errorf("新浪指数日K为空")
}

func normalizeDateDigits(s string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(digits) >= 8 {
		return digits[:8]
	}
	return ""
}

// resolveTradeDate maps a requested YYYYMMDD onto a day that actually has market data.
// It returns the date to use plus a human-readable note when they differ.
func resolveTradeDate(requested string) (string, string) {
	if len(requested) != 8 {
		return requested, ""
	}
	latest, err := getLatestTradingDate()
	if err != nil {
		return requested, fmt.Sprintf("无法确认最近交易日(腾讯/新浪指数日K均失败: %v)，按请求日期 %s 运行", err, requested)
	}
	if requested <= latest {
		return requested, ""
	}
	return latest, fmt.Sprintf("%s 休市或尚无行情，已改用最近交易日 %s 的数据", requested, latest)
}

// currentSessionDate returns the YYYY-MM-DD date whose quotes live sources are
// actually mirroring. Outside trading hours the vendors still serve the last
// session, so time.Now() would file that data under a 休市 date.
func currentSessionDate() string {
	if latest, err := getLatestTradingDate(); err == nil && len(latest) == 8 {
		return latest[:4] + "-" + latest[4:6] + "-" + latest[6:]
	}
	return time.Now().Format("2006-01-02")
}

// sessionAnchor rewinds the clock to the most recent session the market actually
// traded. Walk-back loops seeded from time.Now() spend their whole budget on 长假
// dates that hold no data, and then ask upstream APIs for them.
func sessionAnchor() time.Time {
	if latest, err := getLatestTradingDate(); err == nil {
		if parsed, perr := time.ParseInLocation("20060102", latest, time.Local); perr == nil {
			return parsed
		}
	}
	return time.Now()
}

// queryDate resolves a YYYY-MM-DD query parameter onto the session the market
// sources actually hold data for, so 休市 requests do not come back empty.
func queryDate(c *gin.Context, param string) string {
	return formatTradeDateForDisplay(normTradeDate(c.DefaultQuery(param, "")))
}

// isTradingNow reports whether the A-share market is open at the given Shanghai time.
func isTradingNow(now time.Time) bool {
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	hm := now.Hour()*60 + now.Minute()
	return (hm >= 9*60+30 && hm <= 11*60+30) || (hm >= 13*60 && hm <= 15*60)
}

// nextSessionOpen is the earliest moment the live sources can start serving a new
// session. 节假日 make it earlier than reality, which only costs one extra rebuild;
// it is never later, so a snapshot cached until then cannot be stale.
func nextSessionOpen(now time.Time) time.Time {
	open := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, now.Location())
	for !open.After(now) || open.Weekday() == time.Saturday || open.Weekday() == time.Sunday {
		open = open.AddDate(0, 0, 1)
	}
	return open
}

// MarketFreezeWindow sizes the page cache TTL for market snapshots that are pinned
// to a finished session: on a weekend or holiday every request resolves to the last
// traded day, so rebuilding a ~9s dashboard every 2 minutes is pure waste. Evenings
// of a traded day stay unfrozen because settlement revisions still land there.
func MarketFreezeWindow(now time.Time) (time.Duration, bool) {
	if isTradingNow(now) {
		return 0, false
	}
	latest, err := getLatestTradingDate()
	if err != nil || len(latest) != 8 || latest >= now.Format("20060102") {
		return 0, false
	}
	window := nextSessionOpen(now).Sub(now)
	if window < 2*time.Minute {
		return 0, false
	}
	if window > 24*time.Hour {
		window = 24 * time.Hour
	}
	return window, true
}
