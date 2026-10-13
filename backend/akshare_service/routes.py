# -*- coding: utf-8 -*-
"""DataService HTTP 路由表。

原先 ``DataServiceHandler.do_GET`` 是一条近 300 行的 if/elif 分支链，这里
把它改写为「路径 -> handler」的数据表。每个 handler 签名为

    handler(ctx, params, date_str) -> dict | (dict, status)

- 返回 dict 表示 200
- 返回 (dict, status) 表示自定义状态码（400 / 404）

``ctx`` 是 main 模块对象，handler 通过它访问 fetch_* 采集函数与 VERSION，
因此 routes 不需要反向 import main（避免循环导入），且对 main 模块属性做
monkeypatch 的测试依然有效。

本模块是纯结构重构的产物：分支逻辑逐条搬运，对外响应体保持一致。
"""

# ============================== 响应体构造 ==============================

# 需要显式指定 key 顺序无关紧要（dict 比较与顺序无关），但保持与原实现
# 相同的字段组合，便于肉眼核对。

_NOTE_TENCENT = "索引43=振幅%(已校准),46=PB"
_NOTE_BAIDU = "自带MA5/MA10/MA20均线"

_KLINE_FREQ_MAP = {
    "5min": 0,
    "15min": 1,
    "30min": 2,
    "60min": 3,
    "daily": 9,
    "weekly": 5,
    "monthly": 6,
}


def _listed(data, source, with_count=True):
    payload = {"code": 0, "data": data, "source": source}
    if with_count:
        payload["count"] = len(data)
    return payload


def _missing(field):
    return ({"code": -1, "error": "missing %s" % field}, 400)


# ================================ handlers ==============================

def health(ctx, params, date_str):
    return {
        "status": "ok",
        "version": ctx.VERSION,
        "mootdx": ctx.MOOTDX_AVAILABLE,
        "akshare": ctx.AKSHARE_AVAILABLE,
        "requests": ctx.REQUESTS_AVAILABLE,
        "em_min_interval": ctx.EM_MIN_INTERVAL,
        "data_sources": [
            "mootdx(通达信/TCP)", "腾讯财经", "同花顺", "百度股市通",
            "新浪财经", "巨潮cninfo", "同花顺一致预期", "iwencai",
            "东财push2", "东财datacenter", "东财reportapi",
            "东财search-api", "东财np-weblist"
        ],
        "total_sources": 13,
        "architecture": "七层数据架构 · 27+端点 · 13数据源 · 东财防封em_get()",
    }


def quote(ctx, params, date_str):
    codes = params.get("codes", [""])[0].split(",")
    codes = [c.strip() for c in codes if c.strip()]
    if not codes:
        return _missing("codes")
    data = ctx.fetch_quote_tencent(codes)
    return {"code": 0, "data": data, "source": "tencent", "note": _NOTE_TENCENT}


def kline(ctx, params, date_str):
    code = params.get("code", [""])[0]
    freq = _KLINE_FREQ_MAP.get(params.get("freq", ["daily"])[0], 9)
    count = int(params.get("count", ["60"])[0])
    if not code:
        return _missing("code")
    data = ctx.fetch_kline_mootdx(code, freq, count)
    return _listed(data, "mootdx")


def kline_baidu(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    data = ctx.fetch_kline_baidu(code)
    return {"code": 0, "data": data, "source": "baidu", "note": _NOTE_BAIDU}


def minute(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_minute_mootdx(code), "mootdx")


def research(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if code:
        data = ctx.fetch_research_stock(code)
    else:
        data = ctx.fetch_research_market()
    return _listed(data, "eastmoney_reportapi")


def research_market(ctx, params, date_str):
    return _listed(ctx.fetch_research_market(), "eastmoney_reportapi")


def northbound(ctx, params, date_str):
    return _listed(ctx.fetch_northbound(), "ths+eastmoney", with_count=False)


def hot_stocks(ctx, params, date_str):
    return _listed(ctx.fetch_hot_stocks(), "ths")


def concept_blocks(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_concept_blocks(code), "eastmoney_slist")


def fund_flow(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_fund_flow_minute(code), "eastmoney_push2", with_count=False)


def fund_flow_120d(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_fund_flow_120d(code), "eastmoney_push2his", with_count=False)


def dragon_tiger(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if code:
        data = ctx.fetch_dragon_tiger_stock(code)
    else:
        data = ctx.fetch_dragon_tiger_daily(params.get("date", [None])[0])
    return _listed(data, "eastmoney_datacenter")


def dragon_tiger_daily(ctx, params, date_str):
    date = params.get("date", [None])[0]
    return _listed(ctx.fetch_dragon_tiger_daily(date), "eastmoney_datacenter")


def lockup_expiry(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_lockup_expiry(code), "eastmoney_datacenter", with_count=False)


def industry_rank(ctx, params, date_str):
    return _listed(ctx.fetch_industry_rank(), "eastmoney_push2")


def margin(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_margin(code), "eastmoney_datacenter")


def block_trade(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_block_trade(code), "eastmoney_datacenter")


def shareholder_count(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_shareholder_count(code), "eastmoney_datacenter")


def dividend(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_dividend(code), "eastmoney_datacenter")


def news(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if code:
        data = ctx.fetch_news_stock(code)
    else:
        data = ctx.fetch_news_global()
    return _listed(data, "eastmoney")


def news_global(ctx, params, date_str):
    return _listed(ctx.fetch_news_global(), "eastmoney_np-weblist")


def guba(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_guba(code), "eastmoney")


def f10(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_f10(code), "mootdx", with_count=False)


def stock_info(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_stock_info_em(code), "eastmoney_push2", with_count=False)


def financial_statements(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_financial_statements(code), "sina", with_count=False)


def announce(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    return _listed(ctx.fetch_announce_stock(code), "cninfo")


# --------------------------- 原有兼容接口 ---------------------------

def limit_up(ctx, params, date_str):
    data = ctx.fetch_limit_up(date_str)
    return {"code": 0, "data": {"stocks": data, "count": len(data), "trade_date": date_str}}


def limit_down(ctx, params, date_str):
    data = ctx.fetch_limit_down(date_str)
    return {"code": 0, "data": {"stocks": data, "count": len(data), "trade_date": date_str}}


def broken_board(ctx, params, date_str):
    data = ctx.fetch_broken_board(date_str)
    return {"code": 0, "data": {"stocks": data, "count": len(data), "trade_date": date_str}}


def board_ladder(ctx, params, date_str):
    data = ctx.fetch_board_ladder(date_str)
    return {"code": 0, "data": {**data, "trade_date": date_str}}


def market_stats(ctx, params, date_str):
    up = ctx.fetch_limit_up(date_str)
    down = ctx.fetch_limit_down(date_str)
    broken = ctx.fetch_broken_board(date_str)
    highest = max([s.get("limit_times", 0) for s in up], default=0)
    broken_rate = len(broken) / (len(up) + len(broken)) * 100 if (len(up) + len(broken)) > 0 else 0
    return {
        "code": 0,
        "data": {
            "trade_date": date_str,
            "limit_up_count": len(up),
            "limit_down_count": len(down),
            "broken_count": len(broken),
            "highest_board": highest,
            "broken_rate": round(broken_rate, 1),
        },
    }


def concept_heat(ctx, params, date_str):
    data = ctx.fetch_concept_heat()
    return {"code": 0, "data": {"concepts": data, "count": len(data)}}


def industry_heat(ctx, params, date_str):
    data = ctx.fetch_industry_heat()
    return {"code": 0, "data": {"sectors": data, "count": len(data)}}


def market_overview(ctx, params, date_str):
    return {"code": 0, "data": ctx.fetch_market_overview(date_str)}


def all_stocks(ctx, params, date_str):
    data = ctx.fetch_all_stocks()
    return {"code": 0, "data": {"all_stocks": data, "count": len(data)}}


def stock_quote(ctx, params, date_str):
    code = params.get("code", [""])[0]
    if not code:
        return _missing("code")
    data = ctx.fetch_quote_tencent([code])
    if data:
        return {"code": 0, "data": data[0]}
    return ({"code": -1, "error": "not found"}, 404)


# ================================ 路由表 ================================

ROUTES = {
    # health
    "/health": health,
    # Layer 1: 行情层
    "/quote": quote,
    "/kline": kline,
    "/kline/baidu": kline_baidu,
    "/minute": minute,
    # Layer 2: 研报层
    "/research": research,
    "/research/market": research_market,
    # Layer 3: 信号层
    "/northbound": northbound,
    "/hot_stocks": hot_stocks,
    "/concept_blocks": concept_blocks,
    "/fund_flow": fund_flow,
    "/fund_flow_120d": fund_flow_120d,
    "/dragon_tiger": dragon_tiger,
    "/dragon_tiger_daily": dragon_tiger_daily,
    "/lockup_expiry": lockup_expiry,
    "/industry_rank": industry_rank,
    # Layer 4: 资金面/筹码层
    "/margin": margin,
    "/block_trade": block_trade,
    "/shareholder_count": shareholder_count,
    "/dividend": dividend,
    # Layer 5: 新闻层
    "/news": news,
    "/news/market": news_global,
    "/news/global": news_global,
    "/guba": guba,
    # Layer 6: 基础数据层
    "/f10": f10,
    "/finance": f10,
    "/stock_info": stock_info,
    "/financial_statements": financial_statements,
    # Layer 7: 公告层
    "/announce": announce,
    # 原有兼容接口
    "/limit_up": limit_up,
    "/limit_down": limit_down,
    "/broken_board": broken_board,
    "/board_ladder": board_ladder,
    "/market_stats": market_stats,
    "/concept_heat": concept_heat,
    "/industry_heat": industry_heat,
    "/market_overview": market_overview,
    "/all_stocks": all_stocks,
    "/stock_quote": stock_quote,
}

ENDPOINT_INDEX = [
    "/health",
    "# 行情层(不封IP)", "/quote", "/kline", "/kline/baidu", "/minute",
    "# 研报层", "/research", "/research/market",
    "# 信号层", "/northbound", "/hot_stocks", "/concept_blocks",
    "/fund_flow", "/fund_flow_120d", "/dragon_tiger", "/dragon_tiger_daily",
    "/lockup_expiry", "/industry_rank",
    "# 资金面", "/margin", "/block_trade", "/shareholder_count", "/dividend",
    "# 新闻层", "/news", "/news/global", "/guba",
    "# 基础数据", "/f10", "/finance", "/stock_info", "/financial_statements",
    "# 公告层", "/announce",
    "# 兼容接口", "/limit_up", "/limit_down", "/broken_board", "/board_ladder",
    "/market_stats", "/concept_heat", "/industry_heat", "/market_overview",
    "/all_stocks", "/stock_quote",
]
