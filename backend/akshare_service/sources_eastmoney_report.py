# -*- coding: utf-8 -*-
"""东财 reportapi 研报（个股 / 市场）。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

from common import (
    cache_get,
    cache_set,
    em_get,
)


def fetch_research_stock(code):
    """个股研报 - 东方财富 reportapi（走 em_get 限流）"""
    cache_key = f"research_{code}"
    cached = cache_get(cache_key, 'research')
    if cached:
        return cached

    results = []
    try:
        url = "https://reportapi.eastmoney.com/report/list"
        params = {
            "industryCode": "*", "pageSize": "20", "industry": "*",
            "rating": "*", "ratingChange": "*",
            "beginTime": "2000-01-01", "endTime": "2030-01-01",
            "pageNo": "1", "fields": "", "qType": "0",
            "orgCode": "", "code": code, "rcode": "",
        }
        r = em_get(url, params=params, headers={"Referer": "https://data.eastmoney.com/"})
        data = r.json()
        if data and 'data' in data and data['data']:
            for item in data['data'][:20]:
                results.append({
                    'title': item.get('title', ''),
                    'org_name': item.get('orgSName', '') or item.get('orgName', ''),
                    'author': item.get('researcher', ''),
                    'rating': item.get('emRatingName', ''),
                    'date': item.get('publishDate', '')[:10] if item.get('publishDate') else '',
                    'industry': item.get('industryName', '') or item.get('indvInduName', ''),
                    'eps_this_year': item.get('predictThisYearEps', ''),
                    'eps_next_year': item.get('predictNextYearEps', ''),
                    'eps_next2_year': item.get('predictNextTwoYearEps', ''),
                    'info_code': item.get('infoCode', ''),
                    'source': 'eastmoney_reportapi',
                })
    except Exception as e:
        print(f"[DataService] eastmoney research error: {e}")

    if results:
        cache_set(cache_key, results)
    return results


def fetch_research_market():
    """市场最新研报"""
    cache_key = "research_market"
    cached = cache_get(cache_key, 'research')
    if cached:
        return cached

    results = []
    try:
        url = "https://reportapi.eastmoney.com/report/list"
        params = {
            "industryCode": "*", "pageSize": "30", "industry": "*",
            "rating": "*", "ratingChange": "*",
            "beginTime": "", "endTime": "",
            "pageNo": "1", "fields": "", "qType": "0",
            "orgCode": "", "code": "", "rcode": "",
        }
        r = em_get(url, params=params, headers={"Referer": "https://data.eastmoney.com/"})
        data = r.json()
        if data and 'data' in data and data['data']:
            for item in data['data']:
                results.append({
                    'title': item.get('title', ''),
                    'stock_code': item.get('stockCode', ''),
                    'stock_name': item.get('stockName', ''),
                    'org_name': item.get('orgSName', '') or item.get('orgName', ''),
                    'author': item.get('researcher', ''),
                    'rating': item.get('emRatingName', ''),
                    'date': item.get('publishDate', '')[:10] if item.get('publishDate') else '',
                    'industry': item.get('industryName', ''),
                    'source': 'eastmoney_reportapi',
                })
    except Exception as e:
        print(f"[DataService] market research error: {e}")

    if results:
        cache_set(cache_key, results)
    return results
