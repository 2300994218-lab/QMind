# -*- coding: utf-8 -*-
"""东财新闻：个股新闻、全球资讯、股吧。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

from common import (
    AKSHARE_AVAILABLE,
    cache_get,
    cache_set,
    em_get,
)
import json
import time

# akshare 是可选依赖，仅用于个股新闻的兜底来源。
try:
    import akshare as ak
except ImportError:  # pragma: no cover - 仅在无 akshare 环境触发
    ak = None


def fetch_news_stock(code):
    """个股新闻 - 东财 search-api-web（V3.2.1 修复）"""
    cache_key = f"news_{code}"
    cached = cache_get(cache_key, 'news')
    if cached:
        return cached

    results = []
    try:
        url = "https://search-api-web.eastmoney.com/search/jsonp"
        params = {
            "cb": "jQuery_callback",
            "param": json.dumps({
                "uid": "",
                "keyword": code,
                "type": ["cmsArticleWebOld"],
                "client": "web",
                "clientType": "web",
                "clientVersion": "curr",
                "param": {"cmsArticleWebOld": {"searchScope": "default", "sort": "default", "pageIndex": 1, "pageSize": 20}}
            }),
        }
        r = em_get(url, params=params, headers={
            "Referer": "https://so.eastmoney.com/",
        })
        text = r.text
        # Strip JSONP wrapper
        if "jQuery_callback(" in text:
            text = text[text.index("(") + 1:text.rindex(")")]
        data = json.loads(text)
        
        # V3.2.1 fix: cmsArticleWebOld is directly the article list
        articles = []
        if data.get("result") and data["result"].get("cmsArticleWebOld"):
            cms_data = data["result"]["cmsArticleWebOld"]
            if isinstance(cms_data, list):
                articles = cms_data
            elif isinstance(cms_data, dict) and cms_data.get("list"):
                articles = cms_data["list"]
        
        for item in articles[:20]:
            results.append({
                "title": item.get("title", ""),
                "content": item.get("content", "")[:200] if item.get("content") else "",
                "time": item.get("date", "") or item.get("showTime", ""),
                "source": item.get("mediaName", "") or "东财",
                "url": item.get("url", ""),
            })
    except Exception as e:
        print(f"[DataService] stock news error for {code}: {e}")

    # Fallback: akshare
    if not results and AKSHARE_AVAILABLE:
        try:
            df = ak.stock_news_em(symbol=code)
            if df is not None and len(df) > 0:
                for _, row in df.iterrows():
                    results.append({
                        'title': str(row.get('新闻标题', '')),
                        'content': str(row.get('新闻内容', ''))[:200],
                        'time': str(row.get('发布时间', '')),
                        'source': str(row.get('文章来源', '')),
                        'url': str(row.get('新闻链接', '')),
                    })
        except:
            pass

    if results:
        cache_set(cache_key, results)
    return results


def fetch_news_global():
    """全球资讯 - 东财 np-weblist（7×24 财经快讯，替代财联社）"""
    cache_key = "news_global"
    cached = cache_get(cache_key, 'news')
    if cached:
        return cached

    results = []
    try:
        url = "https://np-weblist.eastmoney.com/comm/web/getNewsByColumns"
        params = {
            "columns": "102",
            "client": "web",
            "biz": "web_724",
            "req_trace": str(int(time.time() * 1000)),
            "pageSize": "30",
            "page": "0",
        }
        r = em_get(url, params=params, headers={
            "Referer": "https://kuaixun.eastmoney.com/",
        })
        data = r.json()
        if data and data.get("data") and data["data"].get("list"):
            for item in data["data"]["list"]:
                results.append({
                    "title": item.get("title", ""),
                    "content": item.get("digest", "") or item.get("content", "")[:200],
                    "time": item.get("showTime", "") or item.get("displayTime", ""),
                    "source": "东财全球资讯",
                    "url": item.get("url_unique", "") or item.get("url_w", ""),
                })
    except Exception as e:
        print(f"[DataService] global news error: {e}")

    if results:
        cache_set(cache_key, results)
    return results


def fetch_guba(code):
    """股吧评论 - 东方财富股吧"""
    cache_key = f"guba_{code}"
    cached = cache_get(cache_key, 'news')
    if cached:
        return cached

    results = []
    try:
        url = f"http://guba.eastmoney.com/interface/GetData?path=pcevaluation/list&columns=post_id,post_title,post_content_p_short,post_publish_time,post_source_id,nickName,post_click_count,post_comment_count&sort=post_publish_time&order=desc&ps=20&p=1&code={code}"
        r = em_get(url, headers={"Referer": f"http://guba.eastmoney.com/list,{code}.html"})
        data = r.json()
        if data and 'data' in data and 'list' in data['data']:
            for item in data['data']['list']:
                results.append({
                    'id': item.get('post_id', ''),
                    'title': item.get('post_title', ''),
                    'content': item.get('post_content_p_short', '')[:100],
                    'time': item.get('post_publish_time', ''),
                    'author': item.get('nickName', ''),
                    'views': item.get('post_click_count', 0),
                    'comments': item.get('post_comment_count', 0),
                })
    except Exception as e:
        print(f"[DataService] guba error for {code}: {e}")

    if results:
        cache_set(cache_key, results)
    return results
