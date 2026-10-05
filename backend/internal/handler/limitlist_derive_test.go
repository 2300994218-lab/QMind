package handler

import (
	"fmt"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"quantmind/internal/model"
	"quantmind/internal/repository"
)

func TestDerivedLimitUsesExchangeLimitPrices(t *testing.T) {
	cases := []struct {
		desc string
		q    derivedQuote
		want string
	}{
		{"主板涨停", derivedQuote{Close: 12.27, High: 12.27, UpLimit: 12.27, DownLimit: 10.04}, "U"},
		// ST 主板 stocks trade at a 5% band some sessions and a 10% band in others; the
		// exchange's own 涨停价 is what decides, not a guessed band.
		{"ST 收盘即涨停价", derivedQuote{Close: 2.45, High: 2.45, UpLimit: 2.45, DownLimit: 2.0}, "U"},
		{"创业板涨停", derivedQuote{Close: 15.13, High: 15.13, UpLimit: 15.13, DownLimit: 12.39}, "U"},
		{"触及涨停未封住", derivedQuote{Close: 14.2, High: 15.13, UpLimit: 15.13, DownLimit: 12.39}, "Z"},
		{"跌停", derivedQuote{Close: 10.04, High: 11.0, UpLimit: 12.24, DownLimit: 10.04}, "D"},
		// 次新股没有涨跌幅限制，+206% 不算涨停。
		{"无涨跌幅次新股", derivedQuote{Close: 65.09, High: 65.09, Pct: 206.59}, ""},
		{"普通上涨", derivedQuote{Close: 11.0, High: 11.0, UpLimit: 12.27, DownLimit: 10.04}, ""},
	}
	for _, c := range cases {
		if got := c.q.derivedLimit(); got != c.want {
			t.Errorf("%s: derivedLimit()=%q, want %q", c.desc, got, c.want)
		}
	}
}

func TestDerivedStreakCountsConsecutiveSessions(t *testing.T) {
	names := map[string][2]string{"600059.SH": {"古越龙山", "白酒"}, "000668.SZ": {"荣丰控股", "房产"}}
	quotes1 := []derivedQuote{
		{TsCode: "600059.SH", Close: 11.15, High: 11.15, UpLimit: 11.15},
		{TsCode: "000668.SZ", Close: 9.9, High: 10.4, UpLimit: 10.4}, // 炸板, breaks the run
	}
	quotes2 := []derivedQuote{
		{TsCode: "600059.SH", Close: 12.27, High: 12.27, UpLimit: 12.27},
		{TsCode: "000668.SZ", Close: 10.89, High: 10.89, UpLimit: 10.89},
	}

	rows1, up1, _ := buildDerivedSessionRows("20260929", quotes1, names, map[string]int{"600059.SH": 2}, nil)
	if len(rows1) != 2 {
		t.Fatalf("session one produced %d rows: %+v", len(rows1), rows1)
	}
	if rows1[0].LimitTimes != 3 || rows1[0].Name != "古越龙山" || rows1[0].Industry != "白酒" {
		t.Errorf("carry-forward wrong: %+v", rows1[0])
	}
	if up1["000668.SZ"] != 0 {
		t.Errorf("炸板 must not extend the 连板 count: %v", up1)
	}

	rows2, up2, _ := buildDerivedSessionRows("20260930", quotes2, names, up1, nil)
	if len(rows2) != 2 {
		t.Fatalf("session two produced %d rows: %+v", len(rows2), rows2)
	}
	if rows2[0].Limit != "U" || rows2[0].LimitTimes != 4 {
		t.Errorf("600059 should be 4连板, got %+v", rows2[0])
	}
	// 荣丰控股 was 炸板 the day before, so its run restarts at 1.
	if rows2[1].LimitTimes != 1 || up2["000668.SZ"] != 1 {
		t.Errorf("炸板后的涨停应重新从1起算: %+v", rows2[1])
	}
}

func TestApplyDerivedBoardCountsAlignsLivePool(t *testing.T) {
	useBoardTable(t, mixedBoardRows())

	rows := []TsLimitList{
		{TsCode: "600059.SH", Limit: "U", LimitTimes: 5, Close: 99, PctChg: 33}, // 采集器行: 只升连板数
		{TsCode: "600519.SH", Limit: "U", LimitTimes: 3, Name: "贵州茅台", Close: 1500, PctChg: 9.99},
		{TsCode: "000001.SZ", Limit: "Z", LimitTimes: 2},
	}
	applyDerivedBoardCounts("2026-09-30", rows)

	var live []model.LimitUpBoard
	repository.DB.Where("trade_date = ? AND limit_type = ? AND code = ?", "2026-09-30", "limit_up_rt", "600059").Find(&live)
	if len(live) != 1 || live[0].BoardCount != 5 || live[0].Price != 12.27 || live[0].DataSource != "" {
		t.Fatalf("采集器行应只升连板数、保留自己的价量: %+v", live)
	}

	// 涨停池没收到的那几只（20cm/北交所等）补进来，榜单才能和"涨停 N 家"对上。
	var added []model.LimitUpBoard
	repository.DB.Where("trade_date = ? AND limit_type = ? AND code = ?", "2026-09-30", "limit_up_rt", "600519").Find(&added)
	if len(added) != 1 || added[0].BoardCount != 3 || added[0].Price != 1500 || added[0].DataSource != derivedLimitListSource {
		t.Fatalf("缺失的涨停应按推导行补写: %+v", added)
	}

	var seeded []model.LimitUpBoard
	repository.DB.Where("code = ? AND limit_type = ?", "600519", "broken").Find(&seeded)
	if len(seeded) != 1 || seeded[0].BoardCount != 1 {
		t.Errorf("其他 key 的行不应被改写: %+v", seeded)
	}

	// 重算只删自己补写的行
	applyDerivedBoardCounts("2026-09-30", rows[:1])
	var after []model.LimitUpBoard
	repository.DB.Where("trade_date = ? AND limit_type = ?", "2026-09-30", "limit_up_rt").Find(&after)
	if len(after) != 1 || after[0].Code != "600059" || after[0].BoardCount != 5 {
		t.Fatalf("重算应只清掉补写行: %+v", after)
	}
}

func TestWeekdaySessionCandidatesSkipWeekends(t *testing.T) {
	got := weekdaySessionCandidates("20260930", 4)
	want := []string{"20260925", "20260928", "20260929", "20260930"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (ascending, 周末剔除, 末尾是请求场次)", got, want)
		}
	}
}

func useLimitListTable(t *testing.T, rows []TsLimitList) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&TsLimitList{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("insert %s: %v", rows[i].TsCode, err)
		}
	}
	previous := repository.DB
	repository.DB = db
	t.Cleanup(func() {
		repository.DB = previous
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
}

// 窗口最前面那几场没有前序场次可 carry，落库时把长连板记成了 1板；重算只上调不下调。
func TestRecountDerivedStreaksRaisesTruncatedRunsOnly(t *testing.T) {
	rows := []TsLimitList{
		{TradeDate: "20260930", TsCode: "600825.SH", Limit: "U", LimitTimes: 1, DataSource: derivedLimitListSource},
		{TradeDate: "20260930", TsCode: "600030.SH", Limit: "U", LimitTimes: 5, DataSource: derivedLimitListSource},
		{TradeDate: "20260930", TsCode: "000001.SZ", Limit: "Z", LimitTimes: 3, DataSource: derivedLimitListSource},
	}
	useLimitListTable(t, rows)

	up, down := recountDerivedStreaks(rows, map[string]int{"600825.SH": 6}, map[string]int{})
	if up["600825.SH"] != 7 {
		t.Errorf("6连板之后应升到7: %+v", up)
	}
	if up["600030.SH"] != 5 {
		t.Errorf("没有前序场次时不能把 5连板 下调: %d", up["600030.SH"])
	}
	if _, ok := up["000001.SZ"]; ok || len(down) != 0 {
		t.Errorf("炸板不应给下一场留连板: up=%+v down=%+v", up, down)
	}

	var stored []TsLimitList
	repository.DB.Where("ts_code = ?", "600825.SH").Find(&stored)
	if len(stored) != 1 || stored[0].LimitTimes != 7 {
		t.Fatalf("重算要写回库: %+v", stored)
	}
}
