# -*- coding: utf-8 -*-
"""同花顺信号：北向资金、当日强势股。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

from common import (
    UA,
    cache_get,
    cache_set,
    em_get,
)
import json
import urllib.request


def fetch_northbound():
    """北向资金 - 同花顺（零鉴权，不封IP）"""
    cache_key = "northbound"
    cached = cache_get(cache_key, 'northbound')
    if cached:
        return cached

    result = {"hgt": [], "sgt": [], "total_net": 0}
    try:
        # 同花顺北向资金实时接口
        url = "https://data.10jqka.com.cn/financial/hsgtzjl/field/zdf/order/desc/page/1/ajax/1/"
        req = urllib.request.Request(url, headers={
            'User-Agent': UA,
            'Referer': 'https://data.10jqka.com.cn/',
        })
        resp = urllib.request.urlopen(req, timeout=10)
        content = resp.read().decode('utf-8')
        # Parse the HTML or JSON response
        # Fallback to Eastmoney if THS fails
    except Exception as e:
        print(f"[DataService] THS northbound error: {e}")

    # Fallback: Eastmoney northbound
    if not result.get("hgt"):
        try:
            url = "https://push2.eastmoney.com/api/qt/kamt.rtmin/get?fields1=f1,f2,f3,f4&fields2=f51,f52,f53,f54,f55,f56"
            r = em_get(url)
            data = r.json()
            if data and data.get("data"):
                d = data["data"]
                # Parse northbound data
                s2n_data = d.get("s2n", {})  # 北向
                if s2n_data:
                    result["total_net"] = s2n_data.get("f52", 0)
                    # Parse minute data
                    if "s2nMinu" in d:
                        for item in d["s2nMinu"]:
                            parts = item.split(",") if isinstance(item, str) else []
                            if len(parts) >= 4:
                                result["hgt"].append({
                                    "time": parts[0],
                                    "net": float(parts[1]) if parts[1] != "-" else 0,
                                })
        except Exception as e:
            print(f"[DataService] eastmoney northbound error: {e}")

    if result.get("hgt") or result.get("total_net"):
        cache_set(cache_key, result)
    return result


def fetch_hot_stocks():
    """当日强势股 - 同花顺热点（零鉴权）"""
    cache_key = "hot_stocks"
    cached = cache_get(cache_key, 'realtime')
    if cached:
        return cached

    results = []
    try:
        url = "https://eq.10jqka.com.cn/open/api/hot_list/v1/hot_stock/concept/data.txt"
        req = urllib.request.Request(url, headers={
            'User-Agent': UA,
            'Referer': 'https://eq.10jqka.com.cn/',
        })
        resp = urllib.request.urlopen(req, timeout=10)
        data = json.loads(resp.read().decode('utf-8'))
        if data and data.get("data") and data["data"].get("stock_list"):
            for item in data["data"]["stock_list"]:
                results.append({
                    "code": item.get("code", ""),
                    "name": item.get("name", ""),
                    "change_pct": item.get("change", 0),
                    "reason": item.get("reason", ""),
                    "tag": item.get("tag", ""),
                    "order": item.get("order", 0),
                })
    except Exception as e:
        print(f"[DataService] THS hot stocks error: {e}")

    if results:
        cache_set(cache_key, results)
    return results
