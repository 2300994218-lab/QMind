# -*- coding: utf-8 -*-
"""新浪财经财报三表。

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


def fetch_financial_statements(code):
    """新浪财报三表 - 资产负债表/利润表/现金流量表（V3.2.1 修复）"""
    cache_key = f"fin_stmt_{code}"
    cached = cache_get(cache_key, 'f10')
    if cached:
        return cached

    result = {"balance_sheet": [], "income_statement": [], "cash_flow": []}
    
    # 新浪接口
    statements = {
        "balance_sheet": "balancesheet",
        "income_statement": "income", 
        "cash_flow": "cashflow",
    }
    
    for key, report_type in statements.items():
        try:
            url = f"https://quotes.sina.cn/cn/api/openapi.php/CompanyFinanceService.get{report_type.capitalize()}?paperCode={code}&source=gjzb&type=0&reportType=0"
            req = urllib.request.Request(url, headers={
                'User-Agent': UA,
                'Referer': 'https://finance.sina.com.cn',
            })
            resp = urllib.request.urlopen(req, timeout=10)
            data = json.loads(resp.read().decode('utf-8'))
            
            # V3.2.1: 新浪结构是 result.data.report_list
            if data and data.get("result") and data["result"].get("data"):
                report_list = data["result"]["data"].get("report_list", {})
                if isinstance(report_list, dict):
                    for period, period_data in list(report_list.items())[:4]:
                        items = period_data.get("data", []) if isinstance(period_data, dict) else []
                        entry = {"period": period, "items": []}
                        for item in items:
                            if isinstance(item, dict):
                                entry["items"].append({
                                    "name": item.get("item_title", ""),
                                    "value": item.get("item_value", ""),
                                })
                        result[key].append(entry)
        except Exception as e:
            print(f"[DataService] sina {key} error for {code}: {e}")

    if any(result[k] for k in result):
        cache_set(cache_key, result)
    return result
