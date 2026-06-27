# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A weekly report auto-generation system that collects work records from multiple data sources (Feishu/Lark tasks, browser plugins) and generates Markdown reports. Built as a Go monolith with in-memory storage (no MySQL/Redis required for local testing).

## Architecture

```
cmd/server/main.go              # Entry point, HTTP routes, handlers
internal/adapter/lark/          # Feishu API client + adapter
internal/application/collector/ # Collection orchestration + Markdown generation
internal/config/                # YAML config loader (Viper)
internal/model/                 # Domain entities (WorkRecord, WeeklyReport)
internal/store/                 # In-memory store with TTL cache
web/                            # Test page + Chrome Extension
```

### Key Design Decisions

- **Monolith first**: Single Go binary, no microservices. Easy to run locally.
- **In-memory storage**: `sync.RWMutex` + maps. Data is lost on restart. Swap to MySQL/Redis later if needed.
- **Adapter pattern**: `SourceAdapter` interface for data sources. Currently implements Feishu API + browser plugin push endpoint.
- **CORS enabled**: `gin-contrib/cors` allows `chrome-extension://*` origins for browser plugin integration.
- **Memory limits**: Store caps at 100 reports and 500 cache items (LRU eviction).

## Common Commands

```bash
# Run server (default port 8080)
go run cmd/server/main.go

# Build binary
go build -o weekly-report cmd/server/main.go

# Clean dependencies and rebuild (after go.mod changes)
go mod tidy && go run cmd/server/main.go

# Kill stuck process on port 8080
lsof -i :8080 | grep LISTEN | awk '{print $2}' | xargs kill -9
```

## Configuration

Edit `configs/config.yaml` or use environment variables:

```yaml
feishu:
  app_id: "${FEISHU_APP_ID}"
  app_secret: "${FEISHU_APP_SECRET}"
  redirect_uri: "${FEISHU_REDIRECT_URI}"
```

Create `.env` file (never commit to Git):

```bash
FEISHU_APP_ID=cli_xxx
FEISHU_APP_SECRET=xxx
FEISHU_REDIRECT_URI=https://your-ngrok-domain.ngrok-free.dev/api/v1/auth/lark/callback
JWT_SECRET=your-strong-random-secret
```

**Note**: `redirect_uri` must match exactly what's configured in Feishu developer console (Security Settings → Redirect URL). Ngrok URL changes on restart.

## Data Flow

### Feishu OAuth Flow
1. `GET /api/v1/auth/lark/login` → returns auth URL
2. User scans QR code on Feishu
3. Feishu redirects to `redirect_uri` with `?code=xxx`
4. `GET /api/v1/auth/lark/callback` exchanges code for user token, stores in cookie, redirects to `http://localhost:8080`
5. `POST /api/v1/collect` uses stored token to fetch tasks and generate report

### Browser Plugin Flow
1. Chrome extension injects `content.js` into any webpage
2. Popup scans DOM for task/issue items (supports Jira, GitHub, Feishu, Zentao, Teambition, generic pages)
3. `POST /api/v1/collect/browser` pushes records directly
4. Both flows end in same `WeeklyReport` struct with Markdown rendering

## Important Implementation Details

- **Cookie settings**: `SameSite=Lax` + `HttpOnly` (required for ngrok → localhost cross-origin callback)
- **Rate limiting**: 10 requests/second per IP (sliding window in middleware)
- **Token storage**: Base64-encoded in memory (not encrypted)
- **Date parsing**: `parseWeekDate()` accepts `YYYY-MM-DD` or ISO format for report queries
- **Plugin user ID**: First time sends prompt to user; stored in `chrome.storage.local` as `weekly_report_user_id`

## Known Limitations

- No persistent database (restarts lose all data)
- Feishu token expires in 2 hours (no refresh token implementation yet)
- Browser plugin has no icons (manifest.json omits icon fields)
- Single-tenant (no multi-user isolation beyond userID string)

## Files to Read for Context

- `configs/config.yaml` — current environment config
- `cmd/server/main.go` — all HTTP handlers and middleware setup
- `internal/adapter/lark/client.go` — Feishu API calls and token management
- `internal/store/memory.go` — in-memory storage with capacity limits
- `web/extension/content.js` — browser plugin DOM extraction logic
- `web/test.html` — frontend test page
