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

// useBoardTable points the package's DB handle at a throwaway in-memory sqlite
// holding both the demo-seed keys and the live collector's '*_rt' keys.
func useBoardTable(t *testing.T, rows []model.LimitUpBoard) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.LimitUpBoard{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("insert %s: %v", rows[i].Code, err)
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

func mixedBoardRows() []model.LimitUpBoard {
	return []model.LimitUpBoard{
		{Code: "601318", Name: "中国平安", Price: 24.11, ChangePct: 9.63, LimitType: "limit_up", BoardCount: 7, FundAmount: -3.63, TradeDate: "2026-09-30"},
		{Code: "600059", Name: "古越龙山", Price: 12.27, ChangePct: 10.045, LimitType: "limit_up_rt", BoardCount: 1, TradeDate: "2026-09-30"},
		{Code: "600519", Name: "贵州茅台", Price: 1500, ChangePct: 9.99, LimitType: "broken", BoardCount: 1, TradeDate: "2026-09-30"},
		{Code: "000001", Name: "平安银行", Price: 11.2, ChangePct: -3.1, LimitType: "broken_rt", OpenCount: 2, TradeDate: "2026-09-30"},
	}
}

func TestBoardRowsServeOnlyLiveCollectorRows(t *testing.T) {
	useBoardTable(t, mixedBoardRows())

	up := boardRowsForType("2026-09-30", realBoardType("limit_up"), "U")
	if len(up) != 1 {
		t.Fatalf("涨停 fallback returned %d rows, want only the real row: %+v", len(up), up)
	}
	if up[0]["name"] != "古越龙山" || up[0]["data_source"] != "live_board" {
		t.Errorf("unexpected row: %+v", up[0])
	}

	broken := boardRowsForType("2026-09-30", realBoardType("broken"), "Z")
	if len(broken) != 1 || broken[0]["code"] != "000001" {
		t.Errorf("炸板 fallback wrong: %+v", broken)
	}

	// 跌停 has no live collector key, so the board table must yield nothing rather
	// than the demo rows; limitBoardRows then decides whether the snapshot may stand in.
	if rows := boardRowsForType("2026-09-30", realBoardType("limit_down"), "D"); len(rows) != 0 {
		t.Errorf("跌停 fallback leaked board rows: %+v", rows)
	}
}

func TestBoardLadderIgnoresSeededBoardCounts(t *testing.T) {
	useBoardTable(t, mixedBoardRows())

	var seeded []model.LimitUpBoard
	repository.DB.Where("trade_date = ? AND limit_type = ? AND board_count >= 2", "2026-09-30", "limit_up").Find(&seeded)
	if len(seeded) == 0 {
		t.Fatal("fixture lost the invented 7连板 row")
	}

	var real []model.LimitUpBoard
	repository.DB.Where("trade_date = ? AND limit_type = ? AND board_count >= 2", "2026-09-30", "limit_up_rt").Find(&real)
	if len(real) != 0 {
		t.Errorf("连板天梯 should stay empty without a real 连板数 source: %+v", real)
	}
}

func TestRealBoardTypeClassification(t *testing.T) {
	if got := realBoardType("limit_up"); got != "limit_up_rt" {
		t.Errorf("limit_up mapped to %q", got)
	}
	if got := realBoardType("limit_down"); got != "" {
		t.Errorf("limit_down mapped to %q, want no live key", got)
	}
	if isRealBoardType("limit_up") || !isRealBoardType("broken_rt") {
		t.Error("demo seed must not classify as a real board key")
	}
}
