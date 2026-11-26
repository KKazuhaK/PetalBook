# The web app, built once on the build machine's own architecture.
FROM --platform=$BUILDPLATFORM node:26-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# The web tests share the excerpt cases with the Go tests (internal/page/testdata),
# and the build type-checks them, so that one file comes along.
COPY internal/page/testdata/excerpts.json /src/internal/page/testdata/excerpts.json
RUN npm run build

# The server with the web app embedded. Cross-compiled instead of emulated:
# the binary is static (no cgo), so the build machine can make any target.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ENV GOWORK=off CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
# Fill the directory internal/webui embeds (see the Makefile's release target).
RUN find internal/webui/dist -mindepth 1 ! -name placeholder.txt -delete
COPY --from=web /src/web/dist/ internal/webui/dist/
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/petalbook ./cmd/petalbook

# git runs every repository operation and ssh reaches remotes over ssh (the
# server starts them by those names); ca-certificates is for https remotes
# and single sign-on providers.
FROM alpine:3.24
RUN apk add --no-cache git openssh-client ca-certificates \
    && adduser -D -u 10001 petalbook \
    && mkdir /data && chown petalbook:petalbook /data
COPY --from=build /out/petalbook /usr/local/bin/petalbook
USER petalbook
ENV HOME=/home/petalbook
VOLUME /data
EXPOSE 8080
# Accounts mode on every interface. Give --public-url (the address people open)
# and anything else after the image name; that replaces the default command,
# so repeat --data and --listen if you still want them.
ENTRYPOINT ["petalbook"]
CMD ["serve", "--data", "/data", "--listen", ":8080"]
