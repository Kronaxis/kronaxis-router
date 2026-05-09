# syntax=docker/dockerfile:1.7

# ---------- builder ----------
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Cache deps separately from sources so dep changes don't bust the build cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a static binary so we can drop into distroless without libc.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /agent-gateway .

# Sanity-check it runs (catches embed.FS / build-tag misconfig before ship).
RUN /agent-gateway --version >/dev/null

# ---------- runtime ----------
# distroless static keeps the image at ~25 MB. No shell, no package manager,
# no SSL CAs by default — but we add ca-certificates from the builder so the
# gateway can dial Anthropic / OpenAI / etc directly when an account uses an
# api-key-via-env injection mode.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /agent-gateway /usr/local/bin/agent-gateway

# The gateway needs git installed for worktree-ephemeral profiles
# (claude-cli / codex-cli / aider). Distroless static doesn't ship git, so
# users with worktree profiles should base off our `agent-gateway:full` tag
# (Dockerfile.full, not yet shipped) which uses debian:12-slim + git. The
# `:static` image is fine for dir-ephemeral / stateless profiles only.

# Default config path; mount your own config.yaml at /etc/agent-gateway/.
ENV AGENT_GATEWAY_PORT=8055
EXPOSE 8055

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/agent-gateway"]
CMD ["--config", "/etc/agent-gateway/config.yaml"]
