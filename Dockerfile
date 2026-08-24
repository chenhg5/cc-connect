# syntax=docker.io/docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

ARG NODE_IMAGE=m.daocloud.io/docker.io/library/node:20-alpine@sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293
ARG GO_IMAGE=m.daocloud.io/docker.io/library/golang:1.25.1-alpine@sha256:b6ed3fd0452c0e9bcdef5597f29cc1418f61672e9d3a2f55bf02e7222c014abd
ARG ALPINE_IMAGE=m.daocloud.io/docker.io/library/alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

FROM ${NODE_IMAGE} AS web-build
ARG NPM_REGISTRY=https://registry.npmmirror.com
ENV COREPACK_NPM_REGISTRY=${NPM_REGISTRY} \
    NPM_CONFIG_REGISTRY=${NPM_REGISTRY}
WORKDIR /src
RUN corepack enable && corepack prepare pnpm@10.32.1 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/package.json web/package.json
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
WORKDIR /src/web
COPY web .
RUN pnpm build

FROM ${GO_IMAGE} AS go-build
ARG GOPROXY=https://goproxy.cn
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
COPY --from=web-build /src/web/dist ./web/dist
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
      -o /out/cc-connect-control ./cmd/cc-connect-control && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
      -o /out/cc-connect-server ./cmd/cc-connect

FROM ${ALPINE_IMAGE} AS runtime
ARG ALPINE_MIRROR=https://mirrors.aliyun.com/alpine
RUN sed -i "s#https://dl-cdn.alpinelinux.org/alpine#${ALPINE_MIRROR}#g" /etc/apk/repositories && \
    apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 cc-connect && \
    adduser -S -D -H -u 10001 -G cc-connect cc-connect && \
    install -d -o cc-connect -g cc-connect -m 0700 /var/lib/cc-connect/control && \
    install -d -o cc-connect -g cc-connect -m 0750 /var/lib/cc-connect/app /run/cc-connect
COPY --from=go-build --chmod=0755 /out/cc-connect-control /usr/local/bin/cc-connect-control
COPY --from=go-build --chmod=0755 /out/cc-connect-server /usr/local/bin/cc-connect-server
COPY --chmod=0755 deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
ENV HOME=/var/lib/cc-connect
EXPOSE 9820
USER cc-connect:cc-connect
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["--listen", "0.0.0.0:9820", "--control-dir", "/var/lib/cc-connect/control", "--app-dir", "/var/lib/cc-connect/app", "--run-dir", "/run/cc-connect", "--server-binary", "/usr/local/bin/cc-connect-server", "--setup-token-file", "/var/lib/cc-connect/control/setup-token", "--deployment-owner", "container", "--container-host-socket", "/run/cc-connect-deploy/host.sock"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:9820/api/v1/auth/setup || exit 1
