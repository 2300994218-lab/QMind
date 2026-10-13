# -*- coding: utf-8 -*-
"""通达信 mootdx（TCP，不封 IP）：K 线、分时、F10。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

from common import (
    cache_get,
    cache_set,
    get_mootdx_client,
)


def fetch_kline_mootdx(code, frequency=9, count=60):
    """K线数据 - mootdx通达信协议（不封IP）
    frequency: 0=5min, 1=15min, 2=30min, 3=60min, 4=daily, 5=weekly, 6=monthly, 9=daily
    """
    cache_key = f"kline_{code}_{frequency}_{count}"
    cached = cache_get(cache_key, 'kline')
    if cached:
        return cached

    client = get_mootdx_client()
    if not client:
        return []

    try:
        bars = client.bars(symbol=code, frequency=frequency, offset=count)
        if bars is None or len(bars) == 0:
            return []

        result = []
        for idx, row in bars.iterrows():
            result.append({
                'date': str(idx) if isinstance(idx, str) else idx.strftime('%Y-%m-%d') if hasattr(idx, 'strftime') else str(row.get('datetime', '')),
                'open': float(row.get('open', 0)),
                'close': float(row.get('close', 0)),
                'high': float(row.get('high', 0)),
                'low': float(row.get('low', 0)),
                'volume': float(row.get('volume', 0)),
                'amount': float(row.get('amount', 0)) if 'amount' in row else 0,
            })

        if result:
            cache_set(cache_key, result)
        return result
    except Exception as e:
        print(f"[DataService] mootdx kline error for {code}: {e}")
        return []


def fetch_minute_mootdx(code):
    """分时数据 - mootdx（不封IP）"""
    cache_key = f"minute_{code}"
    cached = cache_get(cache_key, 'realtime')
    if cached:
        return cached

    client = get_mootdx_client()
    if not client:
        return []

    try:
        data = client.minute(symbol=code)
        if data is None or len(data) == 0:
            return []
        result = []
        for _, row in data.iterrows():
            result.append({
                'price': float(row.get('price', 0)),
                'vol': int(row.get('vol', 0)),
                'volume': int(row.get('volume', 0)),
            })
        if result:
            cache_set(cache_key, result)
        return result
    except Exception as e:
        print(f"[DataService] mootdx minute error: {e}")
        return []


def fetch_f10(code):
    """F10基础数据 - mootdx finance"""
    cache_key = f"f10_{code}"
    cached = cache_get(cache_key, 'f10')
    if cached:
        return cached

    client = get_mootdx_client()
    if not client:
        return {}

    result = {}
    try:
        finance = client.finance(symbol=code)
        if finance is not None and len(finance) > 0:
            row = finance.iloc[0]
            result = {
                'code': code,
                'total_shares': float(row.get('zongguben', 0)),
                'circ_shares': float(row.get('liutongguben', 0)),
                'total_assets': float(row.get('zongzichan', 0)),
                'net_assets': float(row.get('jingzichan', 0)),
                'revenue': float(row.get('zhuyingshouru', 0)),
                'profit': float(row.get('jinglirun', 0)),
                'eps': float(row.get('meigujingzichan', 0)),
                'ipo_date': str(int(row.get('ipo_date', 0))),
                'shareholders': int(row.get('gudongrenshu', 0)),
                'undistributed': float(row.get('weifenpeilirun', 0)),
                'operating_profit': float(row.get('yingyelirun', 0)),
                'cash_flow': float(row.get('jingyingxianjinliu', 0)),
                'province': int(row.get('province', 0)),
                'industry': int(row.get('industry', 0)),
                'updated_date': str(int(row.get('updated_date', 0))),
                'source': 'mootdx',
            }
    except Exception as e:
        print(f"[DataService] mootdx F10 error for {code}: {e}")

    if result:
        cache_set(cache_key, result)
    return result
