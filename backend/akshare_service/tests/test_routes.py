# -*- coding: utf-8 -*-
"""DataServiceHandler.do_GET 路由契约测试。

这些测试锁定每个端点的对外行为（响应体与状态码），用于保护 do_GET 的
内部结构重构（路由表化）：只要测试全绿，端点行为即未改变。

所有 fetch_* / get_trade_date 都被 mock，测试完全离线，不依赖
requests / akshare / mootdx。
"""
import importlib.util
import io
import json
import os
import sys
from types import SimpleNamespace
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


# ---------------------------------------------------------------- helpers

class _Handler:
    """捕获 send_json 输出的假 handler。"""

    def __init__(self, path):
        self.path = path
        self.payload = None
        self.status = None

    def send_json(self, data, status=200):
        self.payload = data
        self.status = status


def call(path):
    h = _Handler(path)
    main.DataServiceHandler.do_GET(h)
    return h


# --------------------------------------------------------- baseline data

_LIST = [{"a": 1}]
_DICT = {"k": "v"}
CODE_KW = {"code": "000001"}
TODAY = "20260101"


def _patch(name, value):
    return patch.object(main, name, return_value=value)


# --------------------------------------------------------------- health

def test_health_returns_service_metadata():
    h = call("/health")
    assert h.status == 200
    assert h.payload["status"] == "ok"
    assert h.payload["version"] == main.VERSION
    assert h.payload["mootdx"] is main.MOOTDX_AVAILABLE
    assert h.payload["akshare"] is main.AKSHARE_AVAILABLE
    assert h.payload["requests"] is main.REQUESTS_AVAILABLE
    assert h.payload["em_min_interval"] == main.EM_MIN_INTERVAL
    assert h.payload["total_sources"] == 13
    assert len(h.payload["data_sources"]) == 13


# ------------------------------------------------- generic list endpoints
# (path, fetch name, expected source tag)
LIST_ENDPOINTS = [
    ("/research/market", "fetch_research_market", "eastmoney_reportapi"),
    ("/hot_stocks", "fetch_hot_stocks", "ths"),
    ("/industry_rank", "fetch_industry_rank", "eastmoney_push2"),
]


@pytest.mark.parametrize("path,func,source", LIST_ENDPOINTS)
def test_no_argument_endpoints(path, func, source):
    with _patch(func, _LIST):
        h = call(path)
    assert h.status == 200
    assert h.payload == {"code": 0, "data": _LIST, "count": len(_LIST), "source": source}


def test_northbound_omits_count_field():
    """北向资金端点历史响应体不含 count 字段。"""
    with _patch("fetch_northbound", _LIST):
        h = call("/northbound")
    assert h.status == 200
    assert h.payload == {"code": 0, "data": _LIST, "source": "ths+eastmoney"}


# --------------------------------------------- code-required endpoints
CODE_ENDPOINTS = [
    ("/kline/baidu", "fetch_kline_baidu", "baidu"),
    ("/minute", "fetch_minute_mootdx", "mootdx"),
    ("/concept_blocks", "fetch_concept_blocks", "eastmoney_slist"),
    ("/fund_flow", "fetch_fund_flow_minute", "eastmoney_push2"),
    ("/fund_flow_120d", "fetch_fund_flow_120d", "eastmoney_push2his"),
    ("/lockup_expiry", "fetch_lockup_expiry", "eastmoney_datacenter"),
    ("/margin", "fetch_margin", "eastmoney_datacenter"),
    ("/block_trade", "fetch_block_trade", "eastmoney_datacenter"),
    ("/shareholder_count", "fetch_shareholder_count", "eastmoney_datacenter"),
    ("/dividend", "fetch_dividend", "eastmoney_datacenter"),
    ("/guba", "fetch_guba", "eastmoney"),
    ("/f10", "fetch_f10", "mootdx"),
    ("/finance", "fetch_f10", "mootdx"),
    ("/stock_info", "fetch_stock_info_em", "eastmoney_push2"),
    ("/financial_statements", "fetch_financial_statements", "sina"),
    ("/announce", "fetch_announce_stock", "cninfo"),
]


@pytest.mark.parametrize("path,func,source", CODE_ENDPOINTS)
def test_code_endpoints_missing_code_returns_400(path, func, source):
    h = call(path)
    assert h.status == 400
    assert h.payload == {"code": -1, "error": "missing code"}


@pytest.mark.parametrize("path,func,source", CODE_ENDPOINTS)
def test_code_endpoints_success(path, func, source):
    with _patch(func, _LIST):
        h = call("%s?code=000001" % path)
    assert h.status == 200
    assert h.payload["code"] == 0
    assert h.payload["data"] == _LIST
    assert h.payload["source"] == source


# ------------------------------------------------------- special quote

def test_quote_missing_codes_returns_400():
    h = call("/quote")
    assert h.status == 400
    assert h.payload == {"code": -1, "error": "missing codes"}


def test_quote_splits_and_trims_codes():
    with patch.object(main, "fetch_quote_tencent") as fq:
        fq.return_value = _LIST
        h = call("/quote?codes=000001,+600519,")
    fq.assert_called_once_with(["000001", "600519"])
    assert h.status == 200
    assert h.payload["source"] == "tencent"
    assert h.payload["note"] == "索引43=振幅%(已校准),46=PB"


# ---------------------------------------------------------- kline specifics

@pytest.mark.parametrize(
    "freq,freq_value",
    [("5min", 0), ("15min", 1), ("30min", 2), ("60min", 3), ("daily", 9), ("weekly", 5), ("monthly", 6)],
)
def test_kline_maps_frequency(freq, freq_value):
    with patch.object(main, "fetch_kline_mootdx", return_value=_LIST) as fk:
        h = call("/kline?code=000001&freq=%s" % freq)
    assert fk.call_args[0][1] == freq_value
    assert h.payload["count"] == len(_LIST)


def test_kline_unknown_frequency_defaults_to_daily():
    with patch.object(main, "fetch_kline_mootdx", return_value=[]) as fk:
        call("/kline?code=000001&freq=unknown")
    assert fk.call_args[0][1] == 9


def test_kline_accepts_count():
    with patch.object(main, "fetch_kline_mootdx", return_value=[]) as fk:
        call("/kline?code=000001&count=120")
    assert fk.call_args[0][2] == 120


def test_kline_missing_code_returns_400():
    h = call("/kline")
    assert h.status == 400
    assert h.payload == {"code": -1, "error": "missing code"}


# -------------------------------------------- dual-mode code endpoints

def test_research_with_code_uses_stock_research():
    with patch.object(main, "fetch_research_stock", return_value=_LIST) as fs, \
         patch.object(main, "fetch_research_market", return_value=[]) as fm:
        h = call("/research?code=000001")
    fs.assert_called_once_with("000001")
    fm.assert_not_called()
    assert h.payload["source"] == "eastmoney_reportapi"


def test_research_without_code_uses_market_research():
    with patch.object(main, "fetch_research_stock", return_value=[]) as fs, \
         patch.object(main, "fetch_research_market", return_value=_LIST) as fm:
        h = call("/research")
    fm.assert_called_once_with()
    fs.assert_not_called()


def test_dragon_tiger_with_code():
    with patch.object(main, "fetch_dragon_tiger_stock", return_value=_LIST) as fs:
        h = call("/dragon_tiger?code=000001")
    fs.assert_called_once_with("000001")
    assert h.payload["source"] == "eastmoney_datacenter"


def test_dragon_tiger_daily_uses_date_param():
    with patch.object(main, "fetch_dragon_tiger_daily", return_value=_LIST) as fd:
        h = call("/dragon_tiger_daily?date=20260618")
    fd.assert_called_once_with("20260618")
    assert h.payload["source"] == "eastmoney_datacenter"


def test_dragon_tiger_without_code_falls_back_to_daily():
    with patch.object(main, "fetch_dragon_tiger_daily", return_value=_LIST) as fd:
        call("/dragon_tiger?date=20260618")
    fd.assert_called_once_with("20260618")


def test_news_with_code():
    with patch.object(main, "fetch_news_stock", return_value=_LIST) as fs:
        h = call("/news?code=000001")
    fs.assert_called_once_with("000001")
    assert h.payload["source"] == "eastmoney"


def test_news_without_code():
    with patch.object(main, "fetch_news_global", return_value=_LIST) as fg:
        h = call("/news")
    fg.assert_called_once_with()
    assert h.payload["source"] == "eastmoney"


@pytest.mark.parametrize("path", ["/news/market", "/news/global"])
def test_market_news_aliases(path):
    with patch.object(main, "fetch_news_global", return_value=_LIST):
        h = call(path)
    assert h.payload["source"] == "eastmoney_np-weblist"


# ------------------------------------------- legacy date-driven endpoints

def _patch_legacy(**values):
    return [
        patch.object(main, name, return_value=value) for name, value in values.items()
    ]


def test_limit_up_uses_trade_date():
    cms = [
        patch.object(main, "get_trade_date", return_value=TODAY),
        patch.object(main, "fetch_limit_up", return_value=_LIST),
    ]
    cms = [patch.object(main, "get_trade_date", return_value=TODAY),
           patch.object(main, "fetch_limit_up", return_value=_LIST)]
    with cms[0], cms[1]:
        h = call("/limit_up")
    assert h.payload == {
        "code": 0,
        "data": {"stocks": _LIST, "count": len(_LIST), "trade_date": TODAY},
    }


def test_limit_down_uses_trade_date():
    with patch.object(main, "get_trade_date", return_value=TODAY), \
         patch.object(main, "fetch_limit_down", return_value=[]):
        h = call("/limit_down")
    assert h.payload["data"]["trade_date"] == TODAY


def test_broken_board_uses_trade_date():
    with patch.object(main, "get_trade_date", return_value=TODAY), \
         patch.object(main, "fetch_broken_board", return_value=[]):
        h = call("/broken_board")
    assert h.payload["data"]["trade_date"] == TODAY


def test_board_ladder_merges_trade_date():
    with patch.object(main, "get_trade_date", return_value=TODAY), \
         patch.object(main, "fetch_board_ladder", return_value=_DICT):
        h = call("/board_ladder")
    assert h.payload == {"code": 0, "data": {**_DICT, "trade_date": TODAY}}


def test_market_stats_aggregates_board_metrics():
    up = [{"limit_times": 3}, {"limit_times": 5}]
    down = [{"x": 1}]
    broken = [{"y": 1}]
    with patch.object(main, "get_trade_date", return_value=TODAY), \
         patch.object(main, "fetch_limit_up", return_value=up), \
         patch.object(main, "fetch_limit_down", return_value=down), \
         patch.object(main, "fetch_broken_board", return_value=broken):
        h = call("/market_stats")
    assert h.payload == {
        "code": 0,
        "data": {
            "trade_date": TODAY,
            "limit_up_count": 2,
            "limit_down_count": 1,
            "broken_count": 1,
            "highest_board": 5,
            "broken_rate": 33.3,
        },
    }


def test_market_stats_without_any_board_data():
    with patch.object(main, "get_trade_date", return_value=TODAY), \
         patch.object(main, "fetch_limit_up", return_value=[]), \
         patch.object(main, "fetch_limit_down", return_value=[]), \
         patch.object(main, "fetch_broken_board", return_value=[]):
        h = call("/market_stats")
    assert h.payload["data"]["highest_board"] == 0
    assert h.payload["data"]["broken_rate"] == 0


@pytest.mark.parametrize(
    "path,func,key",
    [
        ("/concept_heat", "fetch_concept_heat", "concepts"),
        ("/industry_heat", "fetch_industry_heat", "sectors"),
        ("/all_stocks", "fetch_all_stocks", "all_stocks"),
    ],
)
def test_wrapped_list_endpoints(path, func, key):
    with _patch(func, _LIST):
        h = call(path)
    assert h.payload == {"code": 0, "data": {key: _LIST, "count": len(_LIST)}}


def test_market_overview_uses_trade_date():
    with patch.object(main, "get_trade_date", return_value=TODAY), \
         patch.object(main, "fetch_market_overview", return_value=_DICT):
        h = call("/market_overview")
    assert h.payload == {"code": 0, "data": _DICT}


def test_stock_quote_returns_first_item():
    with patch.object(main, "fetch_quote_tencent", return_value=[{"name": "x"}]):
        h = call("/stock_quote?code=000001")
    assert h.payload == {"code": 0, "data": {"name": "x"}}


def test_stock_quote_missing_code_returns_400():
    h = call("/stock_quote")
    assert h.status == 400
    assert h.payload == {"code": -1, "error": "missing code"}


def test_stock_quote_not_found_returns_404():
    with patch.object(main, "fetch_quote_tencent", return_value=[]):
        h = call("/stock_quote?code=000001")
    assert h.status == 404
    assert h.payload == {"code": -1, "error": "not found"}


# ------------------------------------------------------------ error paths

def test_unknown_path_returns_404_with_endpoint_index():
    h = call("/does_not_exist")
    assert h.status == 404
    assert h.payload["error"] == "Not found"
    assert h.payload["version"] == main.VERSION
    assert "/health" in h.payload["endpoints"]
    assert "/quote" in h.payload["endpoints"]
    assert "/stock_quote" in h.payload["endpoints"]


def test_exception_is_caught_and_reported_as_500():
    with patch.object(main, "fetch_hot_stocks", side_effect=RuntimeError("boom")):
        h = call("/hot_stocks")
    assert h.status == 500
    assert h.payload == {"code": -1, "error": "boom"}


def test_404_index_paths_are_all_registered():
    """404 索引里列出的每个端点都必须在路由表中注册。"""
    h = call("/nope")
    listed = [e for e in h.payload["endpoints"] if not e.startswith("#")]
    registered = set(main._ROUTES)
    for path in listed:
        assert path in registered, "404 index advertises unregistered endpoint: %s" % path


def test_route_table_matches_documented_endpoint_count():
    """路由表不应随重构丢失或增添端点（当前 39 个路径）。"""
    assert len(main._ROUTES) == 39
