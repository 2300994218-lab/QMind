# -*- coding: utf-8 -*-
"""akshare_service 纯逻辑函数单元测试。

覆盖代码归一化、市场前缀、TTL 缓存与交易日推算等无需网络的逻辑，
为后续按数据源拆分 main.py 采集区提供护栏。
"""
import importlib.util
import os
import sys
import time
from datetime import datetime
from unittest.mock import patch

import pytest

_HERE = os.path.dirname(os.path.abspath(__file__))
_MAIN = os.path.join(os.path.dirname(_HERE), "main.py")


def _load_main():
    spec = importlib.util.spec_from_file_location("qmind_main", _MAIN)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["qmind_main"] = mod
    spec.loader.exec_module(mod)
    return mod


main = _load_main()

# 数据采集区已拆分到 common / sources_*：monkeypatch 必须打在**调用点所在模块**
# 上（unittest.mock 按名字查找），否则打在 main 上的替身不会被实现代码看到。
import common as _common_mod  # noqa: E402
import sources_market as _market_mod  # noqa: E402


# ------------------------------------------------------------ get_prefix

@pytest.mark.parametrize(
    "code,expected",
    [
        ("600519", "sh"),   # 沪市主板
        ("688001", "sh"),   # 科创板
        ("900001", "sh"),   # B股(沪)
        ("510300", "sh"),   # 沪市ETF
        ("000001", "sz"),   # 深市主板
        ("300750", "sz"),   # 创业板
        ("159915", "sz"),   # 深市ETF
        ("830799", "bj"),   # 北交所
        ("  600519  ", "sh"),  # 容忍空白
    ],
)
def test_get_prefix(code, expected):
    assert main.get_prefix(code) == expected


# --------------------------------------------------------- get_market_id

@pytest.mark.parametrize(
    "code,expected",
    [
        ("600519", "1"),   # 沪 → 1
        ("510300", "1"),
        ("000001", "0"),   # 深 → 0
        ("300750", "0"),
        ("830799", "0"),   # 北交所也按 0
    ],
)
def test_get_market_id(code, expected):
    assert main.get_market_id(code) == expected


# -------------------------------------------------------- normalize_code

@pytest.mark.parametrize(
    "raw,expected",
    [
        ("600519", "600519"),
        ("sh600519", "600519"),
        ("SH600519", "600519"),
        ("sz000001", "000001"),
        ("bj830799", "830799"),
        ("600519.SH", "600519"),
        ("000001.SZ", "000001"),
        ("SH600519.SH", "600519"),
        ("  600519 ", "600519"),
    ],
)
def test_normalize_code(raw, expected):
    assert main.normalize_code(raw) == expected


def test_normalize_code_only_strips_first_matching_prefix():
    # 前缀只在开头匹配一次，不做递归剥离
    assert main.normalize_code("SHSH600519") == "SH600519"


# ------------------------------------------------------------- 缓存 TTL

@pytest.fixture(autouse=True)
def _clean_cache():
    main._cache.clear()
    yield
    main._cache.clear()


def test_cache_miss_returns_none():
    assert main.cache_get("nope") is None


def test_cache_roundtrip_within_ttl():
    main.cache_set("k", {"v": 1})
    assert main.cache_get("k") == {"v": 1}


def test_cache_expires_after_ttl():
    main.cache_set("k", {"v": 1})
    with patch.object(_common_mod.time, "time", return_value=time.time() + main.CACHE_TTL["realtime"] + 1):
        assert main.cache_get("k", "realtime") is None


def test_cache_unknown_category_falls_back_to_default_ttl():
    main.cache_set("k", {"v": 1})
    ttl = main.CACHE_TTL["default"]
    with patch.object(_common_mod.time, "time", return_value=time.time() + ttl - 1):
        assert main.cache_get("k", "unknown-category") == {"v": 1}
    with patch.object(_common_mod.time, "time", return_value=time.time() + ttl + 1):
        assert main.cache_get("k", "unknown-category") is None


def test_cache_ttl_table_is_positive():
    assert all(v > 0 for v in main.CACHE_TTL.values())
    assert "default" in main.CACHE_TTL


# --------------------------------------------------------- get_trade_date

def test_trade_date_passthrough_when_already_compact():
    assert main.get_trade_date("20260618") == "20260618"


def test_trade_date_normalizes_dashed_input():
    assert main.get_trade_date("2026-06-18") == "20260618"


def test_trade_date_falls_back_to_recent_weekday():
    with patch.object(_market_mod, "datetime") as dt:
        # 2026-06-21 是周日 → 应回退到 2026-06-19（周五）
        dt.now.return_value = datetime(2026, 6, 21)
        result = main.get_trade_date()
    assert result == "20260619"


def test_trade_date_treats_any_10_char_input_as_dashed_date():
    """已知边界：长度为 10 的输入一律按 'YYYY-MM-DD' 处理，只剥离 '-'。

    形如 '2026/06/18' 的斜杠日期不会被归一化，会原样返回。这里锁定当前
    行为，避免在拆分采集区时无意改变它；若将来要修正应同步改本用例。
    """
    assert main.get_trade_date("2026/06/18") == "2026/06/18"


# ---------------------------------------------------------- 数据源常量

def test_service_metadata_constants():
    assert main.VERSION == "4.0"
    assert isinstance(main.EM_MIN_INTERVAL, float)
    assert main.EM_MIN_INTERVAL >= 0
    assert isinstance(main.UA, str) and main.UA.startswith("Mozilla/")
