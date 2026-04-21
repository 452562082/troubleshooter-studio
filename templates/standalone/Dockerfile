# 由 troubleshooter-factory 生成
FROM python:3.11-slim

WORKDIR /app

# 先装依赖，利用层缓存
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# 拷贝代码、prompt、资产
COPY server.py index.html system-prompt.md ./
COPY skills ./skills
COPY scripts ./scripts

EXPOSE 3000

CMD ["python3", "server.py"]
