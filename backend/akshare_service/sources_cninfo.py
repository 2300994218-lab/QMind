# -*- coding: utf-8 -*-
"""巨潮 cninfo 公告（含 orgId 映射缓存）。

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
from datetime import datetime
import json
import threading
import urllib.request


_cninfo_org_map = {}


_cninfo_org_lock = threading.Lock()


def _cninfo_orgid(code):
    """动态查询巨潮 orgId（V3.2.2 修复 #19）"""
    global _cninfo_org_map
    if code in _cninfo_org_map:
        return _cninfo_org_map[code]
    
    with _cninfo_org_lock:
        if not _cninfo_org_map:
            # 尝试加载官方映射表
            try:
                url = "http://www.cninfo.com.cn/new/data/szse_stock.json"
                req = urllib.request.Request(url, headers={'User-Agent': UA})
                resp = urllib.request.urlopen(req, timeout=10)
                data = json.loads(resp.read().decode('utf-8'))
                if data and data.get("stockList"):
                    for item in data["stockList"]:
                        _cninfo_org_map[item.get("code", "")] = item.get("orgId", "")
                    print(f"[DataService] cninfo orgId map loaded: {len(_cninfo_org_map)} stocks")
            except Exception as e:
                print(f"[DataService] cninfo orgId map load failed: {e}")
    
    # Fallback: 硬编码猜测
    if code not in _cninfo_org_map:
        _cninfo_org_map[code] = f"gssx0{code}"
    return _cninfo_org_map.get(code, f"gssx0{code}")


def fetch_announce_stock(code):
    """个股公告 - 巨潮资讯 cninfo.com.cn"""
    cache_key = f"announce_{code}"
    cached = cache_get(cache_key, 'announce')
    if cached:
        return cached

    results = []
    
    try:
        url = "http://www.cninfo.com.cn/new/hisAnnouncement/query"
        data_str = f"pageNum=1&pageSize=15&column=szse&tabName=fulltext&plate=&stock={code}&searchkey=&secid=&category=&trade=&seDate=&sortName=&sortType=&isHLtitle=true"
        
        req = urllib.request.Request(url, data=data_str.encode('utf-8'), headers={
            'User-Agent': UA,
            'Content-Type': 'application/x-www-form-urlencoded',
            'Referer': 'http://www.cninfo.com.cn/new/disclosure',
            'Accept': 'application/json',
        })
        resp = urllib.request.urlopen(req, timeout=10)
        result_data = json.loads(resp.read().decode('utf-8'))
        
        if result_data and 'announcements' in result_data:
            for ann in result_data['announcements'][:15]:
                results.append({
                    'title': ann.get('announcementTitle', ''),
                    'time': datetime.fromtimestamp(ann.get('announcementTime', 0)/1000).strftime('%Y-%m-%d') if ann.get('announcementTime') else '',
                    'type': ann.get('announcementTypeName', ''),
                    'url': f"http://www.cninfo.com.cn/new/disclosure/detail?annoId={ann.get('announcementId', '')}",
                    'source': 'cninfo',
                })
    except Exception as e:
        print(f"[DataService] cninfo announce error for {code}: {e}")

    # Fallback: 东财公告
    if not results:
        try:
            url = f"https://np-anotice-stock.eastmoney.com/api/security/ann?page_index=1&page_size=15&ann_type=A&client_source=web&stock_list={code}"
            r = em_get(url)
            data = r.json()
            if data and 'data' in data and 'list' in data['data']:
                for item in data['data']['list']:
                    results.append({
                        'title': item.get('title', ''),
                        'time': item.get('display_time', ''),
                        'type': ','.join(item.get('columns', [])) if item.get('columns') else '',
                        'url': f"https://data.eastmoney.com/notices/detail/{code}/{item.get('art_code', '')}.html",
                        'source': 'eastmoney',
                    })
        except Exception as e:
            print(f"[DataService] eastmoney announce fallback error: {e}")

    if results:
        cache_set(cache_key, results)
    return results
