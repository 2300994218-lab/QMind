FROM python:3.11-slim

WORKDIR /app

# Install system dependencies for mootdx
# Use the Alibaba Debian mirror to avoid slow/unreachable deb.debian.org connections in mainland China.
RUN sed -i 's#deb.debian.org#mirrors.aliyun.com#g' /etc/apt/sources.list.d/debian.sources 2>/dev/null || true
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    && rm -rf /var/lib/apt/lists/*

# Install dependencies first for better caching
COPY requirements.txt .
RUN pip install --no-cache-dir -i https://pypi.tuna.tsinghua.edu.cn/simple \
    akshare>=1.12.0 \
    pandas>=1.5.0 \
    mootdx>=0.11.0

# Copy application code
# 数据采集区已按数据源拆分：main.py 依赖同目录的 routes.py / common.py /
# sources_*.py（以 `python main.py` 启动，脚本目录在 sys.path 中）。
# 用通配复制，后续新增模块无需再改 Dockerfile。
COPY *.py .

# Expose port
EXPOSE 9090

# Health check
HEALTHCHECK --interval=10s --timeout=5s --retries=5 --start-period=30s \
    CMD python3 -c "import urllib.request; urllib.request.urlopen('http://localhost:9090/health')" || exit 1

# Run the service
CMD ["python3", "main.py"]
