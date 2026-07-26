FROM mcr.microsoft.com/playwright:v1.61.1-noble

RUN apt-get update \
    && apt-get install -y --no-install-recommends iproute2 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /work/web
COPY web/package.json web/package-lock.json ./
COPY web/vendor ./vendor
RUN npm ci

COPY web/ ./
RUN chmod +x ./scripts/run-media-suite.sh

ENTRYPOINT ["./scripts/run-media-suite.sh"]
