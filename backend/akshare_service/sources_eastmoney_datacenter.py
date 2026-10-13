# -*- coding: utf-8 -*-
"""东财 datacenter：龙虎榜、解禁、两融、大宗、股东户数、分红。

本模块从 ``main.py`` 拆出（QuantMind 数据采集区按数据源分域）。
``main.py`` 原样再导出这些名字，调用方与测试不受影响。
"""

from __future__ import annotations

from common import (
    cache_get,
    cache_set,
    eastmoney_datacenter,
)
from datetime import datetime
import time


def fetch_dragon_tiger_stock(code):
    """龙虎榜席位 - 东财 datacenter（上榜记录 + 买卖席位TOP5 + 机构动向）"""
    cache_key = f"dragon_tiger_{code}"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    results = []
    try:
        # 查询个股龙虎榜记录
        filter_str = f'(SECURITY_CODE="{code}")'
        data = eastmoney_datacenter(
            "RPT_DAILYBILLBOARD_DETAILSNEW",
            columns="ALL",
            filter_str=filter_str,
            page_size=20,
            sort_columns="TRADE_DATE",
            sort_types="-1"
        )
        for item in data:
            record = {
                "trade_date": item.get("TRADE_DATE", "")[:10] if item.get("TRADE_DATE") else "",
                "code": item.get("SECURITY_CODE", ""),
                "name": item.get("SECURITY_NAME_ABBR", ""),
                "reason": item.get("EXPLANATION", ""),
                "close_price": item.get("CLOSE_PRICE", 0),
                "change_pct": item.get("CHANGE_RATE", 0),
                "buy_total": item.get("BUY_TOTAL_AMT", 0),
                "sell_total": item.get("SELL_TOTAL_AMT", 0),
                "net_amount": (item.get("BUY_TOTAL_AMT", 0) or 0) - (item.get("SELL_TOTAL_AMT", 0) or 0),
                "turnover_rate": item.get("TURNOVERRATE", 0),
            }
            results.append(record)

        # 获取买卖席位明细
        if results:
            time.sleep(0.3)  # 额外间隔
            buy_filter = f'(SECURITY_CODE="{code}")(OPERATEDIRECTION="BUY")'
            buy_seats = eastmoney_datacenter(
                "RPT_BILLBOARD_DAILYDETAILSBUY",
                filter_str=buy_filter,
                page_size=20,
                sort_columns="TRADE_DATE,BUY_AMOUNT",
                sort_types="-1,-1"
            )
            sell_filter = f'(SECURITY_CODE="{code}")(OPERATEDIRECTION="SELL")'
            sell_seats = eastmoney_datacenter(
                "RPT_BILLBOARD_DAILYDETAILSSELL",
                filter_str=sell_filter,
                page_size=20,
                sort_columns="TRADE_DATE,SELL_AMOUNT",
                sort_types="-1,-1"
            )
            # Attach seats to results
            for r in results:
                r["buy_seats"] = [
                    {"name": s.get("BUYER_NAME", ""), "amount": s.get("BUY_AMOUNT", 0)}
                    for s in buy_seats if s.get("TRADE_DATE", "")[:10] == r["trade_date"]
                ][:5]
                r["sell_seats"] = [
                    {"name": s.get("SELLER_NAME", ""), "amount": s.get("SELL_AMOUNT", 0)}
                    for s in sell_seats if s.get("TRADE_DATE", "")[:10] == r["trade_date"]
                ][:5]

    except Exception as e:
        print(f"[DataService] dragon tiger error for {code}: {e}")

    if results:
        cache_set(cache_key, results)
    return results


def fetch_dragon_tiger_daily(date=None):
    """全市场龙虎榜 - 每日全市场上榜股票 + 净买额排名"""
    if not date:
        date = datetime.now().strftime("%Y-%m-%d")
    cache_key = f"dragon_tiger_daily_{date}"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    results = []
    try:
        filter_str = f"(TRADE_DATE='{date}')"
        data = eastmoney_datacenter(
            "RPT_DAILYBILLBOARD_DETAILSNEW",
            columns="ALL",
            filter_str=filter_str,
            page_size=50,
            sort_columns="BUY_TOTAL_AMT",
            sort_types="-1"
        )
        for item in data:
            results.append({
                "code": item.get("SECURITY_CODE", ""),
                "name": item.get("SECURITY_NAME_ABBR", ""),
                "reason": item.get("EXPLANATION", ""),
                "close_price": item.get("CLOSE_PRICE", 0),
                "change_pct": item.get("CHANGE_RATE", 0),
                "buy_total": item.get("BUY_TOTAL_AMT", 0),
                "sell_total": item.get("SELL_TOTAL_AMT", 0),
                "net_amount": (item.get("BUY_TOTAL_AMT", 0) or 0) - (item.get("SELL_TOTAL_AMT", 0) or 0),
                "turnover_rate": item.get("TURNOVERRATE", 0),
                "trade_date": date,
            })
    except Exception as e:
        print(f"[DataService] dragon tiger daily error: {e}")

    if results:
        cache_set(cache_key, results)
    return results


def fetch_lockup_expiry(code):
    """限售解禁日历 - 历史解禁 + 未来90天待解禁"""
    cache_key = f"lockup_{code}"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    results = {"history": [], "upcoming": []}
    try:
        # 历史解禁
        filter_str = f'(SECURITY_CODE="{code}")'
        data = eastmoney_datacenter(
            "RPT_CUSTOM_LIFT_BANDETAILS",
            filter_str=filter_str,
            page_size=30,
            sort_columns="FREE_DATE",
            sort_types="-1"
        )
        now = datetime.now()
        for item in data:
            free_date = item.get("FREE_DATE", "")[:10] if item.get("FREE_DATE") else ""
            record = {
                "free_date": free_date,
                "free_shares": item.get("FREE_SHARES_TOTAL", 0),
                "free_market_value": item.get("FREE_MARKET_VALUE", 0),
                "free_ratio": item.get("FREE_RATIO", 0),
                "holder_name": item.get("HOLDER_NAME", ""),
            }
            if free_date:
                try:
                    fd = datetime.strptime(free_date, "%Y-%m-%d")
                    if fd >= now:
                        results["upcoming"].append(record)
                    else:
                        results["history"].append(record)
                except:
                    results["history"].append(record)
            else:
                results["history"].append(record)
    except Exception as e:
        print(f"[DataService] lockup expiry error for {code}: {e}")

    if results["history"] or results["upcoming"]:
        cache_set(cache_key, results)
    return results


def fetch_margin(code):
    """融资融券明细 - 日级融资余额/买入/偿还 + 融券"""
    cache_key = f"margin_{code}"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    results = []
    try:
        filter_str = f'(SECURITY_CODE="{code}")'
        data = eastmoney_datacenter(
            "RPTA_WEB_RZRQ_GGMX",
            filter_str=filter_str,
            page_size=30,
            sort_columns="DIM_DATE",
            sort_types="-1"
        )
        for item in data:
            results.append({
                "date": item.get("DIM_DATE", "")[:10] if item.get("DIM_DATE") else "",
                "rz_balance": item.get("RZYE", 0),         # 融资余额
                "rz_buy": item.get("RZMRE", 0),            # 融资买入额
                "rz_repay": item.get("RZCHE", 0),          # 融资偿还额
                "rq_balance": item.get("RQYE", 0),         # 融券余额
                "rq_sell": item.get("RQMCL", 0),           # 融券卖出量
                "rq_repay": item.get("RQCHL", 0),          # 融券偿还量
                "rz_rq_balance": item.get("RZRQYE", 0),    # 融资融券余额
            })
    except Exception as e:
        print(f"[DataService] margin error for {code}: {e}")

    if results:
        cache_set(cache_key, results)
    return results


def fetch_block_trade(code):
    """大宗交易 - 成交价/量 + 买卖方营业部 + 溢价率"""
    cache_key = f"block_trade_{code}"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    results = []
    try:
        filter_str = f'(SECURITY_CODE="{code}")'
        data = eastmoney_datacenter(
            "RPT_BLOCKTRADE_DETAILSNEW",
            filter_str=filter_str,
            page_size=30,
            sort_columns="TRADE_DATE",
            sort_types="-1"
        )
        for item in data:
            close_price = item.get("CLOSE_PRICE", 0) or 0
            deal_price = item.get("DEAL_PRICE", 0) or 0
            premium_rate = 0
            if close_price > 0 and deal_price > 0:
                premium_rate = round((deal_price - close_price) / close_price * 100, 2)
            results.append({
                "trade_date": item.get("TRADE_DATE", "")[:10] if item.get("TRADE_DATE") else "",
                "deal_price": deal_price,
                "deal_volume": item.get("DEAL_VOLUME", 0),
                "deal_amount": item.get("DEAL_AMOUNT", 0),
                "close_price": close_price,
                "premium_rate": premium_rate,
                "buyer": item.get("BUYER_NAME", ""),
                "seller": item.get("SELLER_NAME", ""),
                "change_pct": item.get("D1_CLOSE_ADJCHRATE", 0),
            })
    except Exception as e:
        print(f"[DataService] block trade error for {code}: {e}")

    if results:
        cache_set(cache_key, results)
    return results


def fetch_shareholder_count(code):
    """股东户数变化 - 季度股东户数 + 环比变化 + 户均持股"""
    cache_key = f"shareholder_{code}"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    results = []
    try:
        filter_str = f'(SECURITY_CODE="{code}")'
        data = eastmoney_datacenter(
            "RPT_HOLDERNUM_DIAG",
            filter_str=filter_str,
            page_size=20,
            sort_columns="END_DATE",
            sort_types="-1"
        )
        for item in data:
            results.append({
                "end_date": item.get("END_DATE", "")[:10] if item.get("END_DATE") else "",
                "holder_num": item.get("HOLDER_NUM", 0),
                "holder_num_change": item.get("HOLDER_NUM_CHANGE", 0),
                "holder_num_change_rate": item.get("HOLDER_NUM_CHANGE_RATE", 0),
                "avg_shares": item.get("AVG_FREE_SHARES", 0),
                "avg_market_value": item.get("AVG_MARKET_CAP", 0),
                "total_market_cap": item.get("TOTAL_MARKET_CAP", 0),
            })
    except Exception as e:
        print(f"[DataService] shareholder count error for {code}: {e}")

    if results:
        cache_set(cache_key, results)
    return results


def fetch_dividend(code):
    """分红送转历史 - 每股派息/送股/转增 + 进度状态"""
    cache_key = f"dividend_{code}"
    cached = cache_get(cache_key, 'datacenter')
    if cached:
        return cached

    results = []
    try:
        filter_str = f'(SECURITY_CODE="{code}")'
        data = eastmoney_datacenter(
            "RPT_SHAREBONUS_DET",
            filter_str=filter_str,
            page_size=30,
            sort_columns="EX_DIVIDEND_DATE",
            sort_types="-1"
        )
        for item in data:
            results.append({
                "report_date": item.get("REPORT_DATE", "")[:10] if item.get("REPORT_DATE") else "",
                "plan_content": item.get("ASSIGN_DETAIL", ""),
                "bonus_per_share": item.get("PRETAX_BONUS_RMB", 0),  # 每股派息
                "transfer_per_share": item.get("TRANSFER_RATIO", 0),  # 每股转增
                "bonus_ratio": item.get("BONUS_RATIO", 0),  # 每股送股
                "ex_date": item.get("EX_DIVIDEND_DATE", "")[:10] if item.get("EX_DIVIDEND_DATE") else "",
                "record_date": item.get("EQUITY_RECORD_DATE", "")[:10] if item.get("EQUITY_RECORD_DATE") else "",
                "progress": item.get("IMPL_STATUS", ""),
            })
    except Exception as e:
        print(f"[DataService] dividend error for {code}: {e}")

    if results:
        cache_set(cache_key, results)
    return results
