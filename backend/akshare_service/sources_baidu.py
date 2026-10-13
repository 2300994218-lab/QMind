# -*- coding: utf-8 -*-
"""百度股市通 K 线（自带均线）。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

from common import (
    UA,
    cache_get,
    cache_set,
)
import json
import urllib.request


def fetch_kline_baidu(code):
    """百度股市通K线 — 独有能力: 返回时自带 ma5/ma10/ma20 均价（不封IP）"""
    cache_key = f"baidu_kline_{code}"
    cached = cache_get(cache_key, 'kline')
    if cached:
        return cached

    url = "https://finance.pae.baidu.com/selfselect/getstockquotation"
    params = {
        "all": "1", "isIndex": "false", "isBk": "false", "isBlock": "false",
        "isFutures": "false", "isStock": "true", "newFormat": "1",
        "group": "quotation_kline_ab", "finClientType": "pc",
        "code": code, "start_time": "", "ktype": "1",
    }
    headers = {
        "User-Agent": UA,
        "Accept": "application/vnd.finance-web.v1+json",
        "Origin": "https://gushitong.baidu.com",
        "Referer": "https://gushitong.baidu.com/",
    }

    try:
        from urllib.parse import urlencode
        req = urllib.request.Request(
            f"{url}?{urlencode(params)}",
            headers=headers
        )
        resp = urllib.request.urlopen(req, timeout=10)
        d = json.loads(resp.read().decode('utf-8'))
        result_data = d.get("Result", {})
        if isinstance(result_data, list) and result_data:
            result_data = result_data[0] if isinstance(result_data[0], dict) else {}
        if not isinstance(result_data, dict):
            result_data = {}
        md = result_data.get("newMarketData", {})
        if isinstance(md, list) and md:
            md = md[0] if isinstance(md[0], dict) else {}
        if not isinstance(md, dict):
            md = {}
        keys = md.get("keys", []) if isinstance(md.get("keys"), list) else []
        rows_str = md.get("marketData", "") if isinstance(md.get("marketData"), str) else ""
        
        if not rows_str:
            return {"keys": [], "data": []}

        rows = rows_str.split(";")
        data = []
        for row in rows:
            if not row.strip():
                continue
            vals = row.split(",")
            item = {}
            for i, k in enumerate(keys):
                if i < len(vals):
                    item[k] = vals[i]
            data.append(item)

        result = {"keys": keys, "data": data}
        if data:
            cache_set(cache_key, result)
        return result
    except Exception as e:
        print(f"[DataService] baidu kline error for {code}: {e}")
        return {"keys": [], "data": []}
