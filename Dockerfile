ARG BINARY_SOURCE=build

FROM --platform=$BUILDPLATFORM oven/bun:1.4.4 AS frontend
WORKDIR /src
COPY frontend/ ./frontend/
RUN if [ ! -f frontend/exported/index.html ]; then \
      bun --cwd=$PWD/frontend/ install --frozen-lockfile && \
      bun --cwd=$PWD/frontend/ generate; \
    fi

FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/exported ./frontend/exported
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}

RUN go build -tags embed -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /dcompose ./cmd/dcompose \
    && go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /dagent ./cmd/dagent

FROM scratch AS prebuilt
COPY binaries/dcompose binaries/dagent /

FROM ${BINARY_SOURCE} AS binaries

FROM docker:28-cli
RUN apk add --no-cache podman-remote \
    && ln -s /usr/bin/podman-remote /usr/local/bin/podman
COPY --from=binaries /dcompose /dagent /usr/local/bin/
ENV DCOMPOSE_CONFIG_DIR=/config
ENV XDG_CACHE_HOME=/cache
EXPOSE 8080 9077
ENTRYPOINT []
CMD ["dcompose"]
