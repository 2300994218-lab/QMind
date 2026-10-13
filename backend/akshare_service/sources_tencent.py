# -*- coding: utf-8 -*-
"""腾讯财经行情（不封 IP，实时快照首选）。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

from common import (
    UA,
    cache_get,
    cache_set,
    get_prefix,
    normalize_code,
)
import urllib.request


def fetch_quote_tencent(codes):
    """实时行情 - 腾讯财经 qt.gtimg.cn
    不封IP，字段索引实测校准（2026-05）。
    修正：43号=振幅%（非PB），46号=PB（市净率）
    """
    cache_key = f"tencent_quote_{','.join(sorted(codes))}"
    cached = cache_get(cache_key, 'realtime')
    if cached:
        return cached

    # Convert codes to tencent format
    tc_codes = []
    for code in codes:
        code = normalize_code(code)
        tc_codes.append(f"{get_prefix(code)}{code}")

    url = f"https://qt.gtimg.cn/q={','.join(tc_codes)}"
    try:
        req = urllib.request.Request(url, headers={
            'User-Agent': UA,
            'Referer': 'http://finance.qq.com'
        })
        resp = urllib.request.urlopen(req, timeout=8)
        content = resp.read().decode('gbk')

        results = []
        for line in content.strip().split('\n'):
            if '~' not in line:
                continue
            parts = line.split('~')
            if len(parts) < 53:
                continue
            try:
                stock = {
                    'code': parts[2],
                    'name': parts[1],
                    'price': float(parts[3]) if parts[3] else 0,
                    'pre_close': float(parts[4]) if parts[4] else 0,
                    'open': float(parts[5]) if parts[5] else 0,
                    'volume': int(float(parts[6])) if parts[6] else 0,
                    'buy_volume': int(float(parts[7])) if parts[7] else 0,
                    'sell_volume': int(float(parts[8])) if parts[8] else 0,
                    'bid1_price': float(parts[9]) if parts[9] else 0,
                    'bid1_vol': int(float(parts[10])) if parts[10] else 0,
                    'ask1_price': float(parts[19]) if parts[19] else 0,
                    'ask1_vol': int(float(parts[20])) if parts[20] else 0,
                    'time': parts[30] if len(parts) > 30 else '',
                    'change_amt': float(parts[31]) if parts[31] else 0,
                    'change_pct': float(parts[32]) if parts[32] else 0,
                    'high': float(parts[33]) if parts[33] else 0,
                    'low': float(parts[34]) if parts[34] else 0,
                    'amount': float(parts[37]) if len(parts) > 37 and parts[37] else 0,  # 万元
                    'turnover_rate': float(parts[38]) if len(parts) > 38 and parts[38] else 0,
                    'pe_ttm': float(parts[39]) if len(parts) > 39 and parts[39] else 0,
                    # ★ 校准：索引43=振幅%（非PB！网上教程写错），索引46=PB
                    'amplitude_pct': float(parts[43]) if len(parts) > 43 and parts[43] else 0,
                    'mcap_yi': float(parts[44]) if len(parts) > 44 and parts[44] else 0,  # 总市值(亿)
                    'float_mcap_yi': float(parts[45]) if len(parts) > 45 and parts[45] else 0,  # 流通市值(亿)
                    'pb': float(parts[46]) if len(parts) > 46 and parts[46] else 0,  # ★ PB在46号
                    'limit_up': float(parts[47]) if len(parts) > 47 and parts[47] else 0,
                    'limit_down': float(parts[48]) if len(parts) > 48 and parts[48] else 0,
                    'volume_ratio': float(parts[49]) if len(parts) > 49 and parts[49] else 0,
                    'pe_static': float(parts[52]) if len(parts) > 52 and parts[52] else 0,
                    'source': 'tencent',
                }
                # Compute change from price/pre_close for backward compat
                if stock['price'] > 0 and stock.get('pre_close', 0) > 0:
                    stock['change'] = round(stock['price'] - stock['pre_close'], 3)
                else:
                    stock['change'] = stock.get('change_amt', 0)
                # Also map pe field for backward compat
                stock['pe'] = stock['pe_ttm']
                stock['circ_mv'] = stock['float_mcap_yi']
                stock['total_mv'] = stock['mcap_yi']
                if stock['price'] > 0:
                    results.append(stock)
            except (ValueError, IndexError):
                continue

        if results:
            cache_set(cache_key, results)
        return results
    except Exception as e:
        print(f"[DataService] tencent quote error: {e}")
        return []
