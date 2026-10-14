package handler

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"quantmind/internal/model"
	"quantmind/internal/repository"
)

// 连板天梯 fallback for the sessions the permission-gated feeds cannot cover.
//
// limit_step, limit_list_ths and limit_list_d all answer 40203 on this token, and
// Eastmoney's 涨停池 only publishes 连板数 (f136) while its session is live, so every
// closed session - 周末、长假、收盘之后 - left the ladder empty. Tushare's market-wide
// daily bars plus stk_limit cover the same facts: stk_limit publishes the exchange's own
// 涨停价/跌停价, so 涨停 means close == 涨停价. No guess at the 10%/20%/5% bands is
// needed, which matters because those bands differ for ST, 创业板, 北交所 and
// 无涨跌幅次新股 - a guessed band silently misfiles exactly those stocks.
// 连板数 is then the run of consecutive sessions ending at the requested date.
//
// Two calls per session, stored per session: a date after the first 推导 reuses its
// predecessors' rows, so it only pays for the new session. Deriving is slow enough
// (~10s cold) that readers trigger it in the background rather than inside a request.
const (
	derivedLimitListSource   = "derived_daily"
	derivedLadderSessions    = 8
	derivedWarmSessions      = 20 // 开机预热回看的场次，让往前翻的历史日期直接有数
	derivedStreakCarryIn     = 8  // 窗口之外再多回溯的场次，给最早的窗口日期留连板起点
	derivedLimitPriceEpsilon = 0.005
	derivedLimitRetryWait    = 30 * time.Minute
	derivedFetchPace         = 300 * time.Millisecond // 两场之间限速，别把 daily 打到分钟上限
)

type derivedQuote struct {
	TsCode    string
	Close     float64
	High      float64
	Pct       float64
	Amount    float64 // 千元 as returned by daily
	UpLimit   float64
	DownLimit float64
}

// derivedLimit classifies the close against the exchange's own limit prices. 炸板 needs
// the intraday high, so it only covers "touched 涨停价 and did not close there".
func (q derivedQuote) derivedLimit() string {
	switch {
	case q.UpLimit > 0 && q.Close >= q.UpLimit-derivedLimitPriceEpsilon:
		return "U"
	case q.DownLimit > 0 && q.Close <= q.DownLimit+derivedLimitPriceEpsilon:
		return "D"
	case q.UpLimit > 0 && q.High >= q.UpLimit-derivedLimitPriceEpsilon:
		return "Z"
	}
	return ""
}

var (
	derivedLimitRuns        sync.Map // tradeDate -> struct{}, one derivation per session
	derivedLimitNonSessions sync.Map // tradeDate -> struct{}, 交易所日历确认非场次
	derivedLimitFailed      time.Time
	derivedLimitFailMu      sync.Mutex
)

// WarmDerivedLimitLists derives the most recent session on boot so the first dashboard
// load after a restart does not stare at an empty ladder for ten seconds.
func WarmDerivedLimitLists() {
	SafeGo("WarmDerivedLimitLists", func() {
		time.Sleep(3 * time.Second) // let migrations and the trading-day probe settle
		date := strings.ReplaceAll(currentSessionDate(), "-", "")
		log.Printf("[LimitDerive] 启动预热 %s 往前 %d 场的涨跌停/连板…", date, derivedWarmSessions)
		n := ensureDerivedLimitListsWindow(date, derivedWarmSessions)
		log.Printf("[LimitDerive] 预热完成: %s 涨停 %d 只", date, n)
	})
}

func derivedLimitBackedOff() bool {
	derivedLimitFailMu.Lock()
	defer derivedLimitFailMu.Unlock()
	return time.Now().Before(derivedLimitFailed)
}

func markDerivedLimitFailure() {
	derivedLimitFailMu.Lock()
	defer derivedLimitFailMu.Unlock()
	derivedLimitFailed = time.Now().Add(derivedLimitRetryWait)
}

// startDerivedLimitLists runs the derivation for a session in the background. Readers
// call it whenever their 涨跌停 query came up empty, so the panel fills on the next load.
func startDerivedLimitLists(tradeDate string) {
	if derivedLimitBackedOff() || derivedNonSession(tradeDate) || !derivedSessionSettled(tradeDate, time.Now()) {
		return
	}
	if _, running := derivedLimitRuns.LoadOrStore(tradeDate, struct{}{}); running {
		return
	}
	SafeGo("startDerivedLimitLists", func() {
		defer derivedLimitRuns.Delete(tradeDate)
		if n := ensureDerivedLimitLists(tradeDate); n > 0 {
			log.Printf("[LimitDerive] %s 按交易所涨停价推导完成: 涨停 %d 只", tradeDate, n)
		}
	})
}

// derivedNonSession remembers dates the exchange calendar rejected, so a 周末或长假
// request does not pay for a trade_cal probe on every page load.
func derivedNonSession(tradeDate string) bool {
	_, seen := derivedLimitNonSessions.Load(tradeDate)
	return seen
}

// derivedSessionSettled rejects the session still trading: its daily bar holds the
// intraday last price, so classifying it would freeze a partial streak and could
// overwrite the 连板数 the exchange feed already published. isTradingNow only knows
// weekday+clock, so 周末和长假还要靠"最近场次就是今天"来区分 - 节假日的最近场次早已收盘。
func derivedSessionSettled(tradeDate string, now time.Time) bool {
	if !isTradingNow(now) {
		return true
	}
	latest, err := getLatestTradingDate()
	if err != nil || latest != now.Format("20060102") {
		return true
	}
	return tradeDate != latest
}

// settledLimitCounts answers a finished session from the 涨跌停 rows it already owns, so
// 封板/跌停/炸板 and the 连板天梯 come from one set of facts. Snapshot counts derived from
// a pct_chg threshold cannot agree with them: 9.5% files every 20cm 创业板 stock as 涨停
// and sees no 炸板 at all.
func settledLimitCounts(tradeDate string, now time.Time) (int64, int64, int64, bool) {
	if !derivedSessionSettled(tradeDate, now) {
		return 0, 0, 0, false
	}
	var up, down, broken int64
	repository.DB.Model(&TsLimitList{}).Where("trade_date = ? AND `limit` = 'U'", tradeDate).Count(&up)
	repository.DB.Model(&TsLimitList{}).Where("trade_date = ? AND `limit` = 'D'", tradeDate).Count(&down)
	repository.DB.Model(&TsLimitList{}).Where("trade_date = ? AND `limit` = 'Z'", tradeDate).Count(&broken)
	if up == 0 && down == 0 {
		return 0, 0, 0, false
	}
	return up, down, broken, true
}

// ensureDerivedLimitLists rebuilds the 涨跌停 rows for the sessions ending at tradeDate
// and returns the 涨停 count of that session.
func ensureDerivedLimitLists(tradeDate string) int {
	return ensureDerivedLimitListsWindow(tradeDate, derivedLadderSessions)
}

// ensureDerivedLimitListsWindow walks `sessions` 场次 ending at tradeDate, oldest first, so
// each session's 连板数 is carried from its own predecessors. The chain reaches
// `derivedStreakCarryIn` 场次 further back and stores those too: a streak is only as
// trustworthy as the number of predecessors behind it, and without the carry-in the
// oldest session of every window would report 1板 for a 5连板.
func ensureDerivedLimitListsWindow(tradeDate string, sessions int) int {
	if !derivedSessionSettled(tradeDate, time.Now()) {
		log.Printf("[LimitDerive] %s 仍在盘中，连板数交给涨停池 f136", tradeDate)
		return 0
	}
	window := derivedSessionChain(tradeDate, sessions+derivedStreakCarryIn)
	if len(window) == 0 || window[len(window)-1] != tradeDate {
		if len(window) > 0 {
			derivedLimitNonSessions.Store(tradeDate, struct{}{})
			log.Printf("[LimitDerive] %s 交易所日历里没有场次（止于 %s），不推导", tradeDate, window[len(window)-1])
		}
		return 0
	}
	names := derivedNameIndex()
	upStreaks, downStreaks := map[string]int{}, map[string]int{}

	for _, session := range window {
		if rows, cached := loadDerivedSessionRows(session); cached {
			upStreaks, downStreaks = recountDerivedStreaks(rows, upStreaks, downStreaks)
			if session == tradeDate {
				applyDerivedBoardCounts(formatTradeDateForDisplay(tradeDate), rows)
			}
			continue
		}
		quotes, err := fetchDerivedSession(session)
		if err != nil {
			log.Printf("[LimitDerive] %s 行情获取失败: %v", session, err)
			markDerivedLimitFailure()
			return 0
		}
		if len(quotes) == 0 {
			continue // 非交易日或当日行情尚未发布: neither a session nor a break in the chain
		}
		time.Sleep(derivedFetchPace)
		rows, nextUp, nextDown := buildDerivedSessionRows(session, quotes, names, upStreaks, downStreaks)
		if len(rows) > 0 && session == tradeDate {
			applyDerivedBoardCounts(formatTradeDateForDisplay(tradeDate), rows)
		}
		if len(rows) > 0 {
			saveDerivedSessionRows(session, rows)
		}
		upStreaks, downStreaks = nextUp, nextDown
	}

	var final []TsLimitList
	repository.DB.Where("trade_date = ? AND data_source = ?", tradeDate, derivedLimitListSource).Find(&final)
	limitUp := 0
	for _, r := range final {
		if r.Limit == "U" {
			limitUp++
		}
	}
	return limitUp
}

// derivedSessionChain lists the n sessions ending at tradeDate, oldest first. trade_cal
// answers in one call; if it does not, weekday candidates are used and the walk itself
// discards the ones that turn out to hold no market data.
func derivedSessionChain(tradeDate string, n int) []string {
	start := tradeDate
	if t, err := time.ParseInLocation("20060102", tradeDate, time.Local); err == nil {
		start = t.AddDate(0, 0, -90).Format("20060102")
	}
	resp, err := callTushareAPI("trade_cal", map[string]string{
		"exchange":   "SSE",
		"start_date": start,
		"end_date":   tradeDate,
		"is_open":    "1",
	}, "cal_date")
	var dates []string
	if err != nil {
		log.Printf("[LimitDerive] trade_cal 不可用(%v)，按工作日回推候选场次", err)
		dates = weekdaySessionCandidates(tradeDate, n)
	} else {
		for _, row := range tushareDataToMap(resp) {
			if d := tsString(row, "cal_date"); len(d) == 8 && d <= tradeDate {
				dates = append(dates, d)
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	}
	if len(dates) > n {
		dates = dates[:n]
	}
	sort.Strings(dates)
	return dates
}

func weekdaySessionCandidates(tradeDate string, n int) []string {
	t, err := time.ParseInLocation("20060102", tradeDate, time.Local)
	if err != nil {
		return nil
	}
	var dates []string
	for i := 0; len(dates) < n && i < 40; i++ {
		d := t.AddDate(0, 0, -i)
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		dates = append(dates, d.Format("20060102"))
	}
	sort.Strings(dates)
	return dates
}

func fetchDerivedSession(session string) ([]derivedQuote, error) {
	dailyResp, err := callTushareAPI("daily", map[string]string{"trade_date": session},
		"ts_code,close,high,pct_chg,amount")
	if err != nil {
		return nil, err
	}
	limitResp, err := callTushareAPI("stk_limit", map[string]string{"trade_date": session},
		"ts_code,up_limit,down_limit")
	if err != nil {
		return nil, err
	}

	byCode := map[string]*derivedQuote{}
	for _, row := range tushareDataToMap(dailyResp) {
		code := tsString(row, "ts_code")
		if code == "" {
			continue
		}
		byCode[code] = &derivedQuote{
			TsCode: code,
			Close:  tsFloat(row, "close"),
			High:   tsFloat(row, "high"),
			Pct:    tsFloat(row, "pct_chg"),
			Amount: tsFloat(row, "amount"),
		}
	}
	for _, row := range tushareDataToMap(limitResp) {
		q, ok := byCode[tsString(row, "ts_code")]
		if !ok {
			continue
		}
		q.UpLimit = tsFloat(row, "up_limit")
		q.DownLimit = tsFloat(row, "down_limit")
	}

	quotes := make([]derivedQuote, 0, len(byCode))
	for _, q := range byCode {
		quotes = append(quotes, *q)
	}
	sort.Slice(quotes, func(i, j int) bool { return quotes[i].TsCode < quotes[j].TsCode })
	return quotes, nil
}

// buildDerivedSessionRows turns one session's bars into 涨跌停 rows, carrying each
// 涨停/跌停 streak forward from the previous session.
func buildDerivedSessionRows(session string, quotes []derivedQuote, names map[string][2]string,
	prevUp, prevDown map[string]int) (rows []TsLimitList, nextUp, nextDown map[string]int) {
	nextUp, nextDown = map[string]int{}, map[string]int{}
	for _, q := range quotes {
		limit := q.derivedLimit()
		if limit == "" {
			continue
		}
		times := 0
		switch limit {
		case "U":
			times = prevUp[q.TsCode] + 1
			nextUp[q.TsCode] = times
		case "D":
			times = prevDown[q.TsCode] + 1
			nextDown[q.TsCode] = times
		}
		name, industry := names[q.TsCode][0], names[q.TsCode][1]
		rows = append(rows, TsLimitList{
			TradeDate:  session,
			TsCode:     q.TsCode,
			Name:       name,
			Industry:   industry,
			Close:      q.Close,
			PctChg:     q.Pct,
			Amount:     q.Amount * 1000, // daily 成交额是千元，榜单与前端按元显示
			Turnover:   q.Amount * 1000,
			LimitTimes: times,
			Limit:      limit,
			DataSource: derivedLimitListSource,
		})
	}
	return rows, nextUp, nextDown
}

// recountDerivedStreaks re-derives the 连板数 of rows already in the DB from the sessions
// this walk actually covered. 缓存的行可能是在一个更浅的窗口里写的 - 窗口最前面那几场
// 没有前序场次可 carry，3 连板会被记成 1板 - 所以这里只将连板数往上调，绝不下调，
// 多跑一遍就收敛到正确值。
func recountDerivedStreaks(rows []TsLimitList, prevUp, prevDown map[string]int) (up, down map[string]int) {
	up, down = map[string]int{}, map[string]int{}
	for i := range rows {
		r := &rows[i]
		var prev int
		switch r.Limit {
		case "U":
			prev = prevUp[r.TsCode]
		case "D":
			prev = prevDown[r.TsCode]
		default:
			continue // 炸板不给下一场留连板
		}
		if times := prev + 1; times > r.LimitTimes {
			r.LimitTimes = times
			if err := repository.DB.Model(&TsLimitList{}).Where("id = ?", r.ID).
				Update("limit_times", times).Error; err != nil {
				log.Printf("[LimitDerive] 连板数重算写回失败 %s: %v", r.TsCode, err)
			}
		}
		if r.Limit == "U" {
			up[r.TsCode] = r.LimitTimes
		} else {
			down[r.TsCode] = r.LimitTimes
		}
	}
	return up, down
}

func loadDerivedSessionRows(session string) ([]TsLimitList, bool) {
	var rows []TsLimitList
	repository.DB.Where("trade_date = ? AND data_source = ?", session, derivedLimitListSource).
		Order("limit asc, limit_times desc").Find(&rows)
	return rows, len(rows) > 0
}

func saveDerivedSessionRows(session string, rows []TsLimitList) {
	tx := repository.DB.Begin()
	if tx.Error != nil {
		log.Printf("[LimitDerive] %s 开启事务失败: %v", session, tx.Error)
		return
	}
	// Replace only what this derivation owns; a later limit_list_d/ths grant wins.
	tx.Where("trade_date = ? AND data_source = ?", session, derivedLimitListSource).Delete(&TsLimitList{})
	if err := tx.CreateInBatches(&rows, 100).Error; err != nil {
		tx.Rollback()
		log.Printf("[LimitDerive] %s 写入失败: %v", session, err)
		return
	}
	if err := tx.Commit().Error; err != nil {
		log.Printf("[LimitDerive] %s 提交失败: %v", session, err)
	}
}

// derivedNameIndex maps ts_code to 名称/行业 from the local stock list.
func derivedNameIndex() map[string][2]string {
	var basics []TsStockBasic
	repository.DB.Find(&basics)
	names := make(map[string][2]string, len(basics))
	for _, b := range basics {
		names[b.TsCode] = [2]string{b.Name, b.Industry}
	}
	return names
}

// applyDerivedBoardCounts aligns the 实时涨停池 (limit_up_boards/limit_up_rt) with the
// derived rows so panels that read only the board table agree with the 涨跌停统计:
// 采集器已写过的行只升连板数（价量留给实时源，且盘中 f136 是更好的数），池子里没有的
// 涨停股按推导行补上（涨停池常漏 20cm/北交所那几只）。补写行带 derived_daily 标记，
// 重算时只删自己写的，绝不动采集器的行。
func applyDerivedBoardCounts(displayDate string, rows []TsLimitList) {
	repository.DB.Where("trade_date = ? AND limit_type = ? AND data_source = ?",
		displayDate, "limit_up_rt", derivedLimitListSource).Delete(&model.LimitUpBoard{})

	var present []model.LimitUpBoard
	repository.DB.Where("trade_date = ? AND limit_type = ?", displayDate, "limit_up_rt").Find(&present)
	have := make(map[string]model.LimitUpBoard, len(present))
	for _, b := range present {
		have[b.Code] = b
	}

	updated, inserted := 0, 0
	for _, r := range rows {
		if r.Limit != "U" {
			continue
		}
		code := tsCodeToCode(r.TsCode)
		existing, ok := have[code]
		if !ok {
			board := model.LimitUpBoard{
				Code: code, Name: r.Name, Price: r.Close, ChangePct: r.PctChg,
				LimitType: "limit_up_rt", BoardCount: r.LimitTimes,
				Concept: r.Industry, TradeDate: displayDate, DataSource: derivedLimitListSource,
			}
			if err := repository.DB.Create(&board).Error; err != nil {
				log.Printf("[LimitDerive] 补写涨停池失败 %s: %v", code, err)
				continue
			}
			have[code] = board
			inserted++
			continue
		}
		if r.LimitTimes < 2 || existing.BoardCount >= r.LimitTimes {
			continue
		}
		res := repository.DB.Model(&model.LimitUpBoard{}).
			Where("trade_date = ? AND limit_type = ? AND code = ? AND board_count < ?",
				displayDate, "limit_up_rt", code, r.LimitTimes).
			Update("board_count", r.LimitTimes)
		if res.Error != nil {
			log.Printf("[LimitDerive] 回写连板数失败 %s: %v", r.TsCode, res.Error)
			continue
		}
		updated += int(res.RowsAffected)
	}
	if updated+inserted > 0 {
		log.Printf("[LimitDerive] %s 实时涨停池对齐: 补 %d 行，连板数上调 %d 行", displayDate, inserted, updated)
	}
}
