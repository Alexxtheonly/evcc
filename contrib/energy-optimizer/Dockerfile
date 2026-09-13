# syntax=docker/dockerfile:1
FROM python:3.13-slim AS source
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN git init && git remote add origin https://github.com/evcc-io/optimizer.git \
    && git fetch --depth 1 origin a50b33da38e5ee46c23ec744cd72fcff8b3b3c3d \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = a50b33da38e5ee46c23ec744cd72fcff8b3b3c3d
COPY upstream.patch /tmp/upstream.patch
RUN git apply --check /tmp/upstream.patch && git apply /tmp/upstream.patch

FROM source AS dependencies
COPY --from=ghcr.io/astral-sh/uv:0.12.13 /uv /uvx /bin/
RUN --mount=type=cache,target=/root/.cache/uv uv sync --locked --no-editable
ADD --checksum=sha256:f551e7b843e25becee466a447118f6f44f219c4e46cfb4670829ecd3cf47e7d8 \
    https://github.com/coin-or/Cbc/releases/download/releases%2F2.10.10/Cbc-releases.2.10.10-x86_64-ubuntu22-gcc1130-static.tar.gz \
    /tmp/cbc.tar.gz
ARG TARGETARCH
RUN if [ "$TARGETARCH" = "amd64" ]; then tar -xzf /tmp/cbc.tar.gz -C /usr/local ./bin/cbc; fi

FROM dependencies AS builder
COPY tests /app/extension-tests
RUN uv run --locked pytest -q tests extension-tests
RUN uv sync --locked --no-editable --no-group dev

FROM python:3.13-slim
RUN groupadd -r app && useradd -r -g app -s /bin/false app
WORKDIR /app
COPY --from=builder --chown=app:app /app/.venv /app/.venv
COPY --from=builder --chown=app:app /app/openapi.yaml /app/openapi.yaml
ARG TARGETARCH
RUN --mount=from=builder,source=/tmp/cbc.tar.gz,target=/tmp/cbc.tar.gz \
    if [ "$TARGETARCH" = "amd64" ]; then tar -xzf /tmp/cbc.tar.gz -C /usr/local ./bin/cbc; fi
ENV PYTHONUNBUFFERED=1 \
    OPTIMIZER_TIME_LIMIT=5 \
    OPTIMIZER_NUM_THREADS=1 \
    GUNICORN_CMD_ARGS="--workers 2 --timeout 20 --max-requests 32 --config python:optimizer.gunicorn_conf --access-logfile -"
USER app
EXPOSE 7050
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
    CMD ["/app/.venv/bin/python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:7050/optimize/capabilities', timeout=2)"]
CMD ["/app/.venv/bin/gunicorn", "--bind", "0.0.0.0:7050", "optimizer.app:app"]
