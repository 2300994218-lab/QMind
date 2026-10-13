# -*- coding: utf-8 -*-
"""数据采集基础设施：依赖探测、常量、TTL 缓存、东财防封传输、mootdx 客户端与代码工具。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

import json
import os
import random
import time
import threading
import urllib.request


try:
    # 仅用于可用性探测（AKSHARE_AVAILABLE）；真正的数据调用在各 sources_* 模块内
    # 各自完成绑定，本模块不直接消费 ak / pd。
    import akshare as ak  # noqa: F401
    import pandas as pd  # noqa: F401
    AKSHARE_AVAILABLE = True
except ImportError:
    AKSHARE_AVAILABLE = False
    print("[DataService] WARNING: akshare not installed")

try:
    import pandas as pd  # noqa: F401
except ImportError:
    pass

try:
    from mootdx.quotes import Quotes
    MOOTDX_AVAILABLE = True
except ImportError:
    MOOTDX_AVAILABLE = False
    print("[DataService] WARNING: mootdx not installed")

try:
    import requests as req_lib
    REQUESTS_AVAILABLE = True
except ImportError:
    REQUESTS_AVAILABLE = False
    print("[DataService] WARNING: requests not installed, using urllib")


UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"


CACHE_TTL = {
    'realtime': 30,      # 实时行情 30秒
    'kline': 300,        # K线 5分钟
    'news': 600,         # 新闻 10分钟
    'research': 1800,    # 研报 30分钟
    'f10': 3600,         # F10 1小时
    'announce': 1800,    # 公告 30分钟
    'fund_flow': 60,     # 资金流 1分钟
    'northbound': 60,    # 北向资金 1分钟
    'datacenter': 300,   # 数据中心 5分钟
    'default': 300,      # 默认 5分钟
}


_cache = {}


def cache_get(key, category='default'):
    if key in _cache:
        data, ts = _cache[key]
        ttl = CACHE_TTL.get(category, CACHE_TTL['default'])
        if time.time() - ts < ttl:
            return data
    return None


def cache_set(key, data):
    _cache[key] = (data, time.time())


_em_lock = threading.Lock()


_em_last_call = [0.0]


EM_MIN_INTERVAL = float(os.environ.get('EM_MIN_INTERVAL', '1.0'))  # 最小间隔秒数


if REQUESTS_AVAILABLE:
    EM_SESSION = req_lib.Session()
    EM_SESSION.headers.update({
        "User-Agent": UA,
        "Accept": "application/json, text/plain, */*",
        "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
    })
else:
    EM_SESSION = None


def em_get(url, params=None, headers=None, timeout=15, **kwargs):
    """东财统一请求入口：自动节流 + 复用 session + 默认 UA。
    所有 eastmoney.com 接口都应通过它请求，避免高频被封 IP。
    
    防封铁律：
    1. 串行，不并发
    2. 每次间隔 ≥ 1 秒 + 随机抖动
    3. 复用 HTTP 会话（Keep-Alive）
    4. 带正常 UA + Referer
    """
    with _em_lock:
        wait = EM_MIN_INTERVAL - (time.time() - _em_last_call[0])
        if wait > 0:
            time.sleep(wait + random.uniform(0.1, 0.5))
        try:
            if EM_SESSION:
                hdrs = headers or {}
                if 'Referer' not in hdrs:
                    hdrs['Referer'] = 'https://data.eastmoney.com/'
                resp = EM_SESSION.get(url, params=params, headers=hdrs, timeout=timeout, **kwargs)
                return resp
            else:
                # Fallback to urllib
                full_url = url
                if params:
                    from urllib.parse import urlencode
                    full_url = f"{url}?{urlencode(params)}"
                req = urllib.request.Request(full_url, headers={
                    'User-Agent': UA,
                    'Referer': 'https://data.eastmoney.com/',
                    **(headers or {})
                })
                resp = urllib.request.urlopen(req, timeout=timeout)
                class FakeResp:
                    def __init__(self, data, status):
                        self._data = data
                        self.status_code = status
                        self.content = data
                        self.text = data.decode('utf-8')
                    def json(self):
                        return json.loads(self._data)
                return FakeResp(resp.read(), resp.status)
        finally:
            _em_last_call[0] = time.time()


def eastmoney_datacenter(report_name, columns="ALL", filter_str="",
                         page_size=50, sort_columns="", sort_types="-1",
                         page_number=1):
    """东财数据中心统一查询 — 龙虎榜/解禁/融资融券/大宗交易/股东户数/分红 共用（已内置限流）"""
    params = {
        "reportName": report_name,
        "columns": columns,
        "filter": filter_str,
        "pageNumber": str(page_number),
        "pageSize": str(page_size),
        "sortColumns": sort_columns,
        "sortTypes": sort_types,
        "source": "WEB",
        "client": "WEB",
    }
    url = "https://datacenter-web.eastmoney.com/api/data/v1/get"
    r = em_get(url, params=params, timeout=15)
    try:
        d = r.json()
        if d.get("result") and d["result"].get("data"):
            return d["result"]["data"]
    except Exception as e:
        print(f"[DataService] datacenter error for {report_name}: {e}")
    return []


_mootdx_client = None


_mootdx_lock = threading.Lock()


def get_mootdx_client():
    """Get or create mootdx client (thread-safe singleton)"""
    global _mootdx_client
    if not MOOTDX_AVAILABLE:
        return None
    with _mootdx_lock:
        if _mootdx_client is None:
            try:
                _mootdx_client = Quotes.factory(market='std', timeout=10)
                print("[DataService] mootdx client initialized")
            except Exception as e:
                print(f"[DataService] mootdx init error: {e}")
                return None
    return _mootdx_client


def get_prefix(code):
    """6位代码 → 市场前缀"""
    code = code.strip()
    if code.startswith(("6", "9", "5")):
        return "sh"
    elif code.startswith("8"):
        return "bj"
    else:
        return "sz"


def get_market_id(code):
    """6位代码 → 东财 secid 前缀 (0=深圳, 1=上海, 0=北京)"""
    code = code.strip()
    if code.startswith(("6", "9", "5")):
        return "1"
    else:
        return "0"


def normalize_code(code):
    """归一化股票代码为6位纯数字"""
    code = code.strip().upper()
    # Remove prefixes like SH/SZ/BJ
    for prefix in ['SH', 'SZ', 'BJ']:
        if code.startswith(prefix):
            code = code[2:]
            break
    # Remove suffix like .SH/.SZ
    if '.' in code:
        code = code.split('.')[0]
    return code
