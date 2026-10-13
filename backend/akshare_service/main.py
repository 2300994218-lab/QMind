"""
QuantMind Unified Data Service (v4.0)
======================================
整合十三层数据源为统一微服务，基于 a-stock-data V3.2.2 架构升级：

数据源优先级（按封 IP 风险排序）：
  1（首选）mootdx（通达信）   TCP 7709    不封 IP    K线/五档/逐笔/财务快照/F10
  2（首选）腾讯财经           HTTP       不封 IP    实时价/PE/PB/市值/换手率/涨跌停/指数/ETF
  3        同花顺热点/北向     HTTP       极低       强势股/题材归因/北向资金
  4        百度股市通          HTTP       极低       K线（带 MA5/10/20）
  5        新浪财经            HTTP       低         财报三表
  6        巨潮 cninfo         HTTP       低         公告全文
  7        同花顺一致预期      HTTP       低         EPS 一致预期
  8        iwencai             OpenAPI    低         NL 语义搜索
  9        东财 push2          HTTP       中         行情/资金流/板块（限流）
  10       东财 datacenter     HTTP       中         龙虎榜/解禁/两融/大宗/股东户数/分红
  11       东财 reportapi      HTTP       中         研报列表/PDF
  12       东财 search-api     HTTP       中         个股新闻
  13       东财 np-weblist     HTTP       中         全球资讯

核心升级：
  - 东财防封机制：em_get() 串行限流（间隔≥1s+随机抖动）+ 会话复用
  - 数据源优先级：mootdx/腾讯优先（不封IP），东财仅用于独有数据
  - 腾讯字段索引校准：修正43号=振幅（非PB），46号=PB
  - 新增端点：龙虎榜席位、融资融券、大宗交易、股东户数、解禁日历、
              分红送转、北向资金、行业板块排名、资金流120日、全球资讯、
              概念板块归属、百度K线（带均线）

Endpoints:
  GET /health                        - 健康检查 + 数据源状态
  
  # === 行情层 (mootdx + 腾讯 + 百度K线) ===
  GET /quote?codes=000001,600519     - 实时行情 (腾讯，不封IP)
  GET /kline?code=000001&freq=daily&count=60 - K线数据 (mootdx，不封IP)
  GET /kline/baidu?code=600519       - K线+MA均线 (百度股市通)
  GET /minute?code=000001            - 分时数据 (mootdx)
  GET /index?code=000001             - 指数行情 (腾讯)
  
  # === 研报层 (东财 reportapi) ===
  GET /research?code=000001          - 个股研报
  GET /research/market               - 市场研报
  
  # === 信号层 (同花顺 + 东财) ===
  GET /northbound                    - 北向资金 (同花顺)
  GET /hot_stocks                    - 当日强势股 (同花顺)
  GET /concept_blocks?code=000001    - 概念板块归属 (东财 slist)
  GET /fund_flow?code=000001         - 个股资金流向 分钟级 (东财 push2)
  GET /fund_flow_120d?code=000001    - 个股资金流120日 (东财 push2his)
  GET /dragon_tiger?code=000001      - 龙虎榜席位 (东财 datacenter)
  GET /dragon_tiger_daily            - 全市场龙虎榜 (东财 datacenter)
  GET /lockup_expiry?code=000001     - 限售解禁日历 (东财 datacenter)
  GET /industry_rank                 - 行业板块排名 (东财 push2)
  
  # === 资金面/筹码层 (东财 datacenter) ===
  GET /margin?code=000001            - 融资融券明细
  GET /block_trade?code=000001       - 大宗交易
  GET /shareholder_count?code=000001 - 股东户数变化
  GET /dividend?code=000001          - 分红送转历史
  
  # === 新闻层 (东财直连HTTP) ===
  GET /news?code=000001              - 个股新闻 (东财 search-api)
  GET /news/global                   - 全球资讯 (东财 np-weblist)
  GET /guba?code=000001              - 股吧评论
  
  # === 基础数据层 (mootdx + 东财 + 新浪) ===
  GET /f10?code=000001               - F10基础数据 (mootdx)
  GET /finance?code=000001           - 财务快照 (mootdx)
  GET /stock_info?code=000001        - 个股信息 (东财 push2)
  GET /financial_statements?code=000001 - 财报三表 (新浪)
  
  # === 公告层 (巨潮 cninfo) ===
  GET /announce?code=000001          - 个股公告
  
  # === 原有兼容接口 ===
  GET /limit_up                      - 涨停板
  GET /limit_down                    - 跌停板
  GET /broken_board                  - 炸板
  GET /board_ladder                  - 连板天梯
  GET /market_stats                  - 市场统计
  GET /concept_heat                  - 概念热力
  GET /industry_heat                 - 行业热力
  GET /market_overview               - 涨跌概览
  GET /all_stocks                    - 全市场行情
"""

import json
import os
import sys
import traceback
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse, parse_qs

# --- 数据采集区已按数据源拆分为 common + sources_*，这里集中再导出 ---
# 对外接口不变：`from main import fetch_x`、`patch.object(main, 'fetch_x')` 仍然生效，
# routes.py 的 handler 也通过 ctx 访问这些名字。__all__ 声明即为再导出契约。
from common import (
    AKSHARE_AVAILABLE,
    CACHE_TTL,
    EM_MIN_INTERVAL,
    EM_SESSION,
    MOOTDX_AVAILABLE,
    REQUESTS_AVAILABLE,
    UA,
    cache_get,
    cache_set,
    eastmoney_datacenter,
    em_get,
    get_market_id,
    get_mootdx_client,
    get_prefix,
    normalize_code,
)
# 以下划线开头的内部状态同样再导出：监控脚本与测试会直接读写这些缓存/单例
# （如 main._cache.clear()）。导入的是同一对象引用，语义与拆分前一致。
from common import (  # noqa: F401
    _cache,
    _em_last_call,
    _em_lock,
    _mootdx_client,
    _mootdx_lock,
)
from sources_cninfo import _cninfo_org_map, _cninfo_orgid  # noqa: F401
from sources_baidu import fetch_kline_baidu
from sources_cninfo import fetch_announce_stock
from sources_eastmoney_datacenter import (
    fetch_block_trade,
    fetch_dividend,
    fetch_dragon_tiger_daily,
    fetch_dragon_tiger_stock,
    fetch_lockup_expiry,
    fetch_margin,
    fetch_shareholder_count,
)
from sources_eastmoney_push2 import (
    fetch_concept_blocks,
    fetch_fund_flow_120d,
    fetch_fund_flow_minute,
    fetch_industry_rank,
    fetch_stock_info_em,
)
from sources_eastmoney_report import fetch_research_market, fetch_research_stock
from sources_market import (
    fetch_all_stocks,
    fetch_board_ladder,
    fetch_broken_board,
    fetch_concept_heat,
    fetch_industry_heat,
    fetch_limit_down,
    fetch_limit_up,
    fetch_market_overview,
    get_trade_date,
)
from sources_mootdx import fetch_f10, fetch_kline_mootdx, fetch_minute_mootdx
from sources_news import fetch_guba, fetch_news_global, fetch_news_stock
from sources_sina import fetch_financial_statements
from sources_tencent import fetch_quote_tencent
from sources_ths import fetch_hot_stocks, fetch_northbound

__all__ = [
    "AKSHARE_AVAILABLE", "CACHE_TTL", "EM_MIN_INTERVAL", "EM_SESSION",
    "MOOTDX_AVAILABLE", "REQUESTS_AVAILABLE", "UA", "VERSION",
    "cache_get", "cache_set", "eastmoney_datacenter", "em_get",
    "get_market_id", "get_mootdx_client", "get_prefix", "get_trade_date",
    "normalize_code",
    "fetch_all_stocks", "fetch_announce_stock", "fetch_block_trade",
    "fetch_board_ladder", "fetch_broken_board", "fetch_concept_blocks",
    "fetch_concept_heat", "fetch_dividend", "fetch_dragon_tiger_daily",
    "fetch_dragon_tiger_stock", "fetch_f10", "fetch_financial_statements",
    "fetch_fund_flow_120d", "fetch_fund_flow_minute", "fetch_guba",
    "fetch_hot_stocks", "fetch_industry_heat", "fetch_industry_rank",
    "fetch_kline_baidu", "fetch_kline_mootdx", "fetch_limit_down",
    "fetch_limit_up", "fetch_lockup_expiry", "fetch_margin",
    "fetch_market_overview", "fetch_minute_mootdx", "fetch_news_global",
    "fetch_news_stock", "fetch_northbound", "fetch_quote_tencent",
    "fetch_research_market", "fetch_research_stock", "fetch_shareholder_count",
    "fetch_stock_info_em",
    "DataServiceHandler", "main",
]

# --- 路由表（do_GET 的路径分发已抽取到 routes 模块） ---
# 服务以 `python main.py` 启动，脚本目录在 sys.path 中；通过测试或 Go 侧
# 以其他方式加载时路径可能缺失，这里兜底补一次。
try:
    import routes as _routes
except ImportError:  # pragma: no cover - 仅非标准加载路径会走到
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    import routes as _routes

_ROUTES = _routes.ROUTES
_ENDPOINT_INDEX = _routes.ENDPOINT_INDEX
# handler 通过 ctx 访问本模块的 fetch_* 与 VERSION，避免 routes 反向 import
_CTX = sys.modules[__name__]

# ===================== Constants =====================
# 依赖探测（akshare / mootdx / requests）与 TTL 缓存、东财防封 em_get()、
# mootdx 客户端、代码工具均已迁至 common.py；各 fetch_* 按数据源落在
# sources_*.py。布局见各模块 docstring。
VERSION = "4.0"

# ===================== HTTP Server =====================

class DataServiceHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        sys.stdout.write(f"[DataService] {format%args}\n")
        sys.stdout.flush()

    def send_json(self, data, status=200):
        self.send_response(status)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Access-Control-Allow-Origin', '*')
        self.send_header('Access-Control-Allow-Methods', 'GET, OPTIONS')
        self.send_header('Access-Control-Allow-Headers', 'Content-Type')
        self.end_headers()
        self.wfile.write(json.dumps(data, ensure_ascii=False, default=str).encode('utf-8'))

    def do_OPTIONS(self):
        self.send_response(200)
        self.send_header('Access-Control-Allow-Origin', '*')
        self.send_header('Access-Control-Allow-Methods', 'GET, OPTIONS')
        self.send_header('Access-Control-Allow-Headers', 'Content-Type')
        self.end_headers()

    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path
        params = parse_qs(parsed.query)
        date_str = get_trade_date(params.get('trade_date', [None])[0] or params.get('date', [None])[0])
        handler = _ROUTES.get(path)

        try:
            if handler is None:
                self.send_json({
                    "error": "Not found", "version": VERSION,
                    "endpoints": _ENDPOINT_INDEX,
                }, 404)
                return
            result = handler(_CTX, params, date_str)
            if isinstance(result, tuple):
                self.send_json(*result)
            else:
                self.send_json(result)
        except Exception as e:
            print(f"[DataService] Handler error: {e}")
            traceback.print_exc()
            self.send_json({"code": -1, "error": str(e)}, 500)


def main():
    port = int(os.environ.get('AKSHARE_PORT', os.environ.get('DATA_SERVICE_PORT', '9090')))
    server = HTTPServer(('0.0.0.0', port), DataServiceHandler)
    print(f"╔══════════════════════════════════════════════════════════════╗")
    print(f"║  QuantMind Data Service v{VERSION}                              ║")
    print(f"║  七层架构 · 27+ 端点 · 13 数据源 · 东财防封 em_get()       ║")
    print(f"╠══════════════════════════════════════════════════════════════╣")
    print(f"║  Port: {port}                                                 ║")
    print(f"║  mootdx: {'✓ available ' if MOOTDX_AVAILABLE else '✗ unavailable'}                                  ║")
    print(f"║  akshare: {'✓ available ' if AKSHARE_AVAILABLE else '✗ unavailable'}                                 ║")
    print(f"║  requests: {'✓ available' if REQUESTS_AVAILABLE else '✗ unavailable'}                                 ║")
    print(f"║  em_get interval: {EM_MIN_INTERVAL}s                                    ║")
    print(f"╠══════════════════════════════════════════════════════════════╣")
    print(f"║  数据源优先级:                                              ║")
    print(f"║  1. mootdx(TCP) + 腾讯(HTTP) — 不封IP，优先用              ║")
    print(f"║  2. 同花顺 + 百度 + 新浪 + 巨潮 — 低风险                   ║")
    print(f"║  3. 东财(push2/datacenter/reportapi/search/np) — 限流防封   ║")
    print(f"╚══════════════════════════════════════════════════════════════╝")
    sys.stdout.flush()
    server.serve_forever()


if __name__ == '__main__':
    main()
