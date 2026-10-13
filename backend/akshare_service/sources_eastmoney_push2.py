# -*- coding: utf-8 -*-
"""东财 push2：概念板块、资金流、行业排名、个股信息。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

from common import (
    cache_get,
    cache_set,
    em_get,
    get_market_id,
)


def fetch_concept_blocks(code):
    """概念板块归属 - 东财 slist（V3.2.2 替换百度PAE）"""
    cache_key = f"concept_blocks_{code}"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    results = []
    try:
        secid = f"{get_market_id(code)}.{code}"
        url = f"https://push2.eastmoney.com/api/qt/slist/get"
        params = {
            "secid": secid,
            "spt": "3",  # 全部板块（行业+概念+地域）
            "fields": "f12,f14,f3,f152,f204",
            "np": "1",
            "fltt": "2",
        }
        r = em_get(url, params=params)
        data = r.json()
        if data and data.get("data") and data["data"].get("diff"):
            for item in data["data"]["diff"]:
                if isinstance(item, dict):
                    results.append({
                        "code": item.get("f12", ""),
                        "name": item.get("f14", ""),
                        "change_pct": item.get("f3", 0),
                        "lead_stock": item.get("f204", ""),
                    })
    except Exception as e:
        print(f"[DataService] concept blocks error for {code}: {e}")

    if results:
        cache_set(cache_key, results)
    return results


def fetch_fund_flow_minute(code):
    """个股资金流向 分钟级 - 东财 push2"""
    cache_key = f"fund_flow_min_{code}"
    cached = cache_get(cache_key, 'fund_flow')
    if cached:
        return cached

    result = {"code": code, "data": []}
    try:
        secid = f"{get_market_id(code)}.{code}"
        url = f"https://push2.eastmoney.com/api/qt/stock/fflow/kline/get"
        params = {
            "secid": secid,
            "fields1": "f1,f2,f3,f7",
            "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63,f64,f65",
            "klt": "1",  # 1分钟
            "lmt": "0",
        }
        r = em_get(url, params=params)
        data = r.json()
        if data and data.get("data") and data["data"].get("klines"):
            for line in data["data"]["klines"]:
                parts = line.split(",")
                if len(parts) >= 6:
                    result["data"].append({
                        "time": parts[0],
                        "main_net": float(parts[1]) if parts[1] != "-" else 0,
                        "super_big": float(parts[2]) if parts[2] != "-" else 0,
                        "big": float(parts[3]) if parts[3] != "-" else 0,
                        "mid": float(parts[4]) if parts[4] != "-" else 0,
                        "small": float(parts[5]) if parts[5] != "-" else 0,
                    })
    except Exception as e:
        print(f"[DataService] fund flow minute error for {code}: {e}")

    if result["data"]:
        cache_set(cache_key, result)
    return result


def fetch_fund_flow_120d(code):
    """个股资金流120日 - 东财 push2his（主力/大单/中单/小单 日级净流入）"""
    cache_key = f"fund_flow_120d_{code}"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    result = {"code": code, "data": []}
    try:
        secid = f"{get_market_id(code)}.{code}"
        url = f"https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get"
        params = {
            "secid": secid,
            "fields1": "f1,f2,f3,f7",
            "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61",
            "lmt": "120",
        }
        r = em_get(url, params=params)
        data = r.json()
        if data and data.get("data") and data["data"].get("klines"):
            for line in data["data"]["klines"]:
                parts = line.split(",")
                if len(parts) >= 6:
                    result["data"].append({
                        "date": parts[0],
                        "main_net": float(parts[1]) if parts[1] != "-" else 0,
                        "super_big": float(parts[2]) if parts[2] != "-" else 0,
                        "big": float(parts[3]) if parts[3] != "-" else 0,
                        "mid": float(parts[4]) if parts[4] != "-" else 0,
                        "small": float(parts[5]) if parts[5] != "-" else 0,
                    })
    except Exception as e:
        print(f"[DataService] fund flow 120d error for {code}: {e}")

    if result["data"]:
        cache_set(cache_key, result)
    return result


def fetch_industry_rank():
    """行业板块排名 - 东财 push2（行业涨跌/上涨下跌家数）"""
    cache_key = "industry_rank"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    results = []
    try:
        # 直接拼接URL避免参数编码问题
        url = "https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=100&po=1&np=1&fltt=2&invt=2&fid=f3&fs=m:90+t:2&fields=f2,f3,f4,f8,f12,f14,f104,f105,f128,f136,f204,f205"
        r = em_get(url)
        data = r.json()
        if data and data.get("data") and data["data"].get("diff"):
            for item in data["data"]["diff"]:
                if isinstance(item, dict):
                    results.append({
                        "code": item.get("f12", ""),
                        "name": item.get("f14", ""),
                        "change_pct": item.get("f3", 0),
                        "turnover_rate": item.get("f8", 0),
                        "up_count": item.get("f104", 0),
                        "down_count": item.get("f105", 0),
                        "lead_stock": item.get("f204", ""),
                        "lead_change": item.get("f205", 0) if item.get("f205") else 0,
                    })
    except Exception as e:
        print(f"[DataService] industry rank error: {e}")

    if results:
        cache_set(cache_key, results)
    return results


def fetch_stock_info_em(code):
    """东财个股信息 - 行业/总股本/流通股/市值/上市日期 (push2)"""
    cache_key = f"stock_info_{code}"
    cached = cache_get(cache_key, 'f10')
    if cached:
        return cached

    result = {}
    try:
        secid = f"{get_market_id(code)}.{code}"
        url = "https://push2.eastmoney.com/api/qt/stock/get"
        params = {
            "secid": secid,
            "fields": "f57,f58,f107,f162,f163,f167,f168,f169,f170,f171,f173,f174,f175,f176,f177,f178,f179,f180,f181,f182,f183,f184,f185,f186,f187,f188,f189,f190,f191,f192,f193",
            "invt": "2",
        }
        r = em_get(url, params=params)
        data = r.json()
        if data and data.get("data"):
            d = data["data"]
            result = {
                "code": d.get("f57", code),
                "name": d.get("f58", ""),
                "industry": d.get("f107", ""),
                "total_shares": d.get("f183", 0),
                "float_shares": d.get("f184", 0),
                "total_mv": d.get("f187", 0),
                "float_mv": d.get("f185", 0),
                "pe_ttm": d.get("f162", 0),
                "pe_static": d.get("f163", 0),
                "pb": d.get("f167", 0),
                "roe": d.get("f173", 0),
                "gross_margin": d.get("f174", 0),
                "net_margin": d.get("f175", 0),
                "listing_date": d.get("f189", ""),
                "source": "eastmoney_push2",
            }
    except Exception as e:
        print(f"[DataService] stock info error for {code}: {e}")

    if result:
        cache_set(cache_key, result)
    return result
