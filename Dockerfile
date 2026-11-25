# Two images from one file:
#   docker build --target server -t inaba-server .   the web UI, API and relay
#   docker build --target host   -t inaba .          a host that runs agents and terminals

# The web UI is plain files, so it is built once on the build machine whatever the target.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
RUN npm install -g pnpm@12
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
# vite writes into ../internal/webui/dist, which the server embeds.
RUN mkdir -p ../internal/webui && pnpm exec vite build

# Go cross-compiles, so building on the build machine avoids emulating the target.
# The SQLite driver is pure Go, so neither binary needs libc.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/
COPY gen/ gen/
COPY --from=web /src/internal/webui/dist/ internal/webui/dist/
ARG TARGETOS TARGETARCH
# The build has no .git to read the version from, so it is passed in.
ARG VERSION=
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/Alex4386/inaba/internal/version.version=$VERSION" -o /out/ ./cmd/inaba-server ./cmd/inaba

FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /out/inaba-server /usr/local/bin/inaba-server
COPY LICENSE THIRD_PARTY_NOTICES.txt /usr/share/doc/inaba/
# Accounts, hosts and sign-ins live here; mount a volume to keep them.
VOLUME /data
WORKDIR /data
# 8080: web UI and API. 3478/udp: the built-in STUN server, if INABA_STUN_ENABLE is on.
EXPOSE 8080 3478/udp
USER nonroot:nonroot
# In the environment rather than a flag, so `docker exec … inaba-server user list` finds the same database and socket.
ENV INABA_DATA=/data/inaba-server.db
ENTRYPOINT ["/usr/local/bin/inaba-server"]
CMD ["serve", "--listen", ":8080"]

# A host runs coding agents, so it carries what they need: node for agents started with
# npx, git and ssh for repositories, and a shell for terminals.
FROM node:24-bookworm-slim AS host
RUN apt-get update \
 && apt-get install -y --no-install-recommends git openssh-client ca-certificates bash curl less procps tini \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --shell /bin/bash inaba \
 && mkdir -p /workspace && chown inaba:inaba /workspace
COPY --from=build /out/inaba /usr/local/bin/inaba
COPY LICENSE THIRD_PARTY_NOTICES.txt /usr/share/doc/inaba/
USER inaba
ENV HOME=/home/inaba SHELL=/bin/bash
# The home keeps the host's database and sign-in, and the agents' own logins;
# /workspace is where projects live.
VOLUME /home/inaba /workspace
WORKDIR /workspace
# Agents start child processes; tini reaps them and passes signals on.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/inaba"]
CMD ["connect"]
