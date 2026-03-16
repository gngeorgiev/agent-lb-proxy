FROM node:22-bookworm

ARG CODEX_NPM_VERSION=latest

ENV DEBIAN_FRONTEND=noninteractive
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

RUN apt-get update \
    && apt-get install -y --no-install-recommends chromium ca-certificates fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

RUN npm install -g "@openai/codex@${CODEX_NPM_VERSION}"

WORKDIR /opt/codexlb-login
COPY package.json ./
RUN npm install --omit=dev

COPY login-container.mjs ./

ENTRYPOINT ["node", "/opt/codexlb-login/login-container.mjs"]
