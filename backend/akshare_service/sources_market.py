# -*- coding: utf-8 -*-
"""交易日推算与市场统计：涨跌停、炸板、连板梯队的兼容接口。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

from common import (
    AKSHARE_AVAILABLE,
    cache_get,
    cache_set,
)
from datetime import datetime, timedelta

# akshare / pandas 是可选依赖：缺失时 AKSHARE_AVAILABLE 为 False，
# 各接口在守卫处直接降级返回，不会触达 ak / pd。
try:
    import akshare as ak
    import pandas as pd
except ImportError:  # pragma: no cover - 仅在无 akshare 环境触发
    ak = None
    pd = None


def get_trade_date(date_str=None):
    if date_str and len(date_str) == 8:
        return date_str
    if date_str and len(date_str) == 10:
        return date_str.replace('-', '')
    now = datetime.now()
    for i in range(10):
        d = now - timedelta(days=i)
        if d.weekday() < 5:
            return d.strftime('%Y%m%d')
    return now.strftime('%Y%m%d')


def fetch_limit_up(date_str):
    """涨停板"""
    cache_key = f"limit_up_{date_str}"
    cached = cache_get(cache_key)
    if cached:
        return cached
    if not AKSHARE_AVAILABLE:
        return []
    try:
        df = ak.stock_zt_pool_em(date=date_str)
        if df is None or len(df) == 0:
            return []
        stocks = []
        for _, row in df.iterrows():
            stocks.append({
                "code": str(row.get('代码', '')),
                "name": str(row.get('名称', '')),
                "pct_chg": float(row.get('涨跌幅', 0)),
                "close": float(row.get('最新价', 0)),
                "amount": float(row.get('成交额', 0)),
                "turnover_ratio": float(row.get('换手率', 0)),
                "limit_times": int(row.get('连板数', 1)),
                "first_time": str(row.get('首次封板时间', '')),
                "last_time": str(row.get('最后封板时间', '')),
                "open_times": int(row.get('炸板次数', 0)),
                "industry": str(row.get('所属行业', '')),
                "tag": str(row.get('所属行业', '')),
                "status": f"{int(row.get('连板数', 1))}连板" if int(row.get('连板数', 1)) > 1 else "",
            })
        cache_set(cache_key, stocks)
        return stocks
    except Exception as e:
        print(f"[DataService] limit_up error: {e}")
        return []


def fetch_limit_down(date_str):
    """跌停板"""
    cache_key = f"limit_down_{date_str}"
    cached = cache_get(cache_key)
    if cached:
        return cached
    if not AKSHARE_AVAILABLE:
        return []
    try:
        df = ak.stock_zt_pool_dtgc_em(date=date_str)
        if df is None or len(df) == 0:
            return []
        stocks = []
        for _, row in df.iterrows():
            stocks.append({
                "code": str(row.get('代码', '')),
                "name": str(row.get('名称', '')),
                "pct_chg": float(row.get('涨跌幅', 0)),
                "close": float(row.get('最新价', 0)),
                "amount": float(row.get('成交额', 0)),
                "industry": str(row.get('所属行业', '')),
            })
        cache_set(cache_key, stocks)
        return stocks
    except Exception as e:
        print(f"[DataService] limit_down error: {e}")
        return []


def fetch_broken_board(date_str):
    """炸板"""
    cache_key = f"broken_{date_str}"
    cached = cache_get(cache_key)
    if cached:
        return cached
    if not AKSHARE_AVAILABLE:
        return []
    try:
        df = ak.stock_zt_pool_zbgc_em(date=date_str)
        if df is None or len(df) == 0:
            return []
        stocks = []
        for _, row in df.iterrows():
            stocks.append({
                "code": str(row.get('代码', '')),
                "name": str(row.get('名称', '')),
                "pct_chg": float(row.get('涨跌幅', 0)),
                "close": float(row.get('最新价', 0)),
                "amount": float(row.get('成交额', 0)),
                "open_times": int(row.get('炸板次数', 1)),
                "industry": str(row.get('所属行业', '')),
                "tag": "炸板",
            })
        cache_set(cache_key, stocks)
        return stocks
    except Exception as e:
        print(f"[DataService] broken_board error: {e}")
        return []


def fetch_board_ladder(date_str):
    """连板天梯"""
    cache_key = f"ladder_{date_str}"
    cached = cache_get(cache_key)
    if cached:
        return cached
    if not AKSHARE_AVAILABLE:
        return {"ladder": [], "highest_board": 0}
    try:
        df = ak.stock_zt_pool_em(date=date_str)
        if df is None or len(df) == 0:
            return {"ladder": [], "highest_board": 0}
        max_board = int(df['连板数'].max()) if '连板数' in df.columns else 0
        ladder = []
        multi = df[df['连板数'] >= 2] if '连板数' in df.columns else pd.DataFrame()
        if len(multi) > 0:
            for level in sorted(multi['连板数'].unique(), reverse=True):
                level_stocks = multi[multi['连板数'] == level]
                stocks = []
                for _, row in level_stocks.iterrows():
                    stocks.append({
                        "code": str(row.get('代码', '')),
                        "name": str(row.get('名称', '')),
                        "close": float(row.get('最新价', 0) or 0),
                        "pct_chg": float(row.get('涨跌幅', 0) or 0),
                        "amount": float(row.get('成交额', 0) or 0),
                        "tag": str(row.get('所属行业', '')),
                        "status": f"{int(level)}连板",
                    })
                ladder.append({"level": int(level), "count": len(stocks), "stocks": stocks})
        result = {"ladder": ladder, "highest_board": max_board}
        cache_set(cache_key, result)
        return result
    except Exception as e:
        print(f"[DataService] board_ladder error: {e}")
        return {"ladder": [], "highest_board": 0}


def fetch_concept_heat():
    """概念热力"""
    cache_key = "concept_heat"
    cached = cache_get(cache_key)
    if cached:
        return cached
    if not AKSHARE_AVAILABLE:
        return []
    try:
        df = ak.stock_board_concept_name_em()
        if df is None or len(df) == 0:
            return []
        concepts = []
        for _, row in df.head(80).iterrows():
            concepts.append({
                "name": str(row.get('板块名称', '')),
                "code": str(row.get('板块代码', '')),
                "change_pct": float(row.get('涨跌幅', 0)),
                "lead_stock": str(row.get('领涨股票', '')),
                "amount": float(row.get('总成交额', 0)),
            })
        concepts.sort(key=lambda x: abs(x['change_pct']), reverse=True)
        cache_set(cache_key, concepts)
        return concepts
    except Exception as e:
        print(f"[DataService] concept_heat error: {e}")
        return []


def fetch_industry_heat():
    """行业热力"""
    cache_key = "industry_heat"
    cached = cache_get(cache_key)
    if cached:
        return cached
    if not AKSHARE_AVAILABLE:
        return []
    try:
        df = ak.stock_board_industry_name_em()
        if df is None or len(df) == 0:
            return []
        sectors = []
        for _, row in df.head(80).iterrows():
            sectors.append({
                "name": str(row.get('板块名称', '')),
                "code": str(row.get('板块代码', '')),
                "change_pct": float(row.get('涨跌幅', 0)),
                "lead_stock": str(row.get('领涨股票', '')),
                "amount": float(row.get('总成交额', 0)),
            })
        sectors.sort(key=lambda x: abs(x['change_pct']), reverse=True)
        cache_set(cache_key, sectors)
        return sectors
    except Exception as e:
        print(f"[DataService] industry_heat error: {e}")
        return []


def fetch_market_overview(date_str):
    """涨跌概览"""
    cache_key = f"overview_{date_str}"
    cached = cache_get(cache_key)
    if cached:
        return cached
    if not AKSHARE_AVAILABLE:
        return {"up_count": 0, "down_count": 0, "flat_count": 0, "total": 0}
    try:
        df = ak.stock_zh_a_spot_em()
        if df is None or len(df) == 0:
            return {"up_count": 0, "down_count": 0, "flat_count": 0, "total": 0}
        pct_col = '涨跌幅' if '涨跌幅' in df.columns else None
        if not pct_col:
            return {"up_count": 0, "down_count": 0, "flat_count": 0, "total": 0}
        df[pct_col] = pd.to_numeric(df[pct_col], errors='coerce')
        df = df.dropna(subset=[pct_col])
        result = {
            "up_count": int((df[pct_col] > 0).sum()),
            "down_count": int((df[pct_col] < 0).sum()),
            "flat_count": int((df[pct_col] == 0).sum()),
            "total": len(df),
            "trade_date": date_str,
        }
        cache_set(cache_key, result)
        return result
    except Exception as e:
        print(f"[DataService] market_overview error: {e}")
        return {"up_count": 0, "down_count": 0, "flat_count": 0, "total": 0}


def fetch_all_stocks():
    """全市场行情"""
    cache_key = "all_stocks"
    cached = cache_get(cache_key)
    if cached:
        return cached
    if not AKSHARE_AVAILABLE:
        return []
    try:
        df = ak.stock_zh_a_spot_em()
        if df is None or len(df) == 0:
            return []
        col_map = {'代码': 'code', '名称': 'name', '最新价': 'close', '涨跌幅': 'pct_chg',
                   '成交量': 'volume', '成交额': 'amount', '换手率': 'turnover_rate',
                   '量比': 'volume_ratio', '总市值': 'total_mv', '流通市值': 'circ_mv'}
        result = []
        for _, row in df.iterrows():
            stock = {}
            for cn, en in col_map.items():
                if cn in df.columns:
                    val = row[cn]
                    stock[en] = '' if pd.isna(val) and en in ('code', 'name') else (0 if pd.isna(val) else val)
                else:
                    stock[en] = '' if en in ('code', 'name') else 0
            for k in ['close', 'pct_chg', 'volume', 'amount', 'turnover_rate', 'volume_ratio', 'total_mv', 'circ_mv']:
                try:
                    stock[k] = float(stock.get(k, 0) or 0)
                except:
                    stock[k] = 0.0
            if stock.get('total_mv', 0) > 100000:
                stock['total_mv'] = stock['total_mv'] / 1e8
            if stock.get('circ_mv', 0) > 100000:
                stock['circ_mv'] = stock['circ_mv'] / 1e8
            if stock.get('code') and stock.get('close', 0) > 0:
                result.append(stock)
        if result:
            cache_set(cache_key, result)
        return result
    except Exception as e:
        print(f"[DataService] all_stocks error: {e}")
        return []
