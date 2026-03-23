# workspace-api

A lightweight, secure API that lets AI agents (and humans) operate on remote servers as if they were local — no SSH sessions, no shell escaping headaches.

## The Problem

When AI coding agents (Claude, Cursor, Copilot, etc.) need to work on remote servers, they face painful limitations:

- **Shell escaping hell**: Passing code snippets through SSH -> bash -> commands means quotes, backslashes, `$variables`, and special characters get mangled at every layer
- **No structured operations**: `sed` and `awk` are fragile for code editing — one wrong regex and your file is corrupted
- **Lost context**: SSH sessions are stateless, commands run in isolation, and error handling is primitive
- **Binary-unsafe output**: Command output goes through multiple encoding layers, often corrupting non-ASCII content

## The Solution

**workspace-api** exposes your server's filesystem and shell through a clean JSON API over **WSS (WebSocket Secure)** and **HTTPS**. All content travels through `json.dumps()` -> TLS -> `json.loads()` — never through shell parsing.

The included `ws-api` CLI client makes remote operations feel exactly like local commands:

```bash
# Just prefix any command with ws-api — runs on remote server
ws-api ls -la /root/project
ws-api git log --oneline -5
ws-api go build ./...
ws-api docker ps

# Background execution — long tasks run without blocking
ws-api go build ./... --bg                        # returns job ID instantly
ws-api jobs                                       # check progress
ws-api job <id>                                   # get output

# Structured file operations — zero escaping issues
ws-api read server.go -n                          # with line numbers
ws-api edit config.go --old "old text" --new "new text"
ws-api edit config.go --old "x" --new "y" --dry-run  # preview changes
ws-api edit-lines main.go --delete 70-320         # delete by line range
ws-api edit-lines main.go --insert 15 < patch.txt # insert at line
ws-api write deploy.sh < local_script.sh          # from local file

# Binary file transfer — no more scp
ws-api upload ./app /usr/local/bin/app --mode 0755
ws-api download /var/log/app.log ./app.log
```

## Architecture

```
                    WSS (default)
┌─────────────┐  ═══════════════════  ┌──────────────────┐
│  AI Agent    │      or HTTPS         │  workspace-api   │
│  (sandbox)   │ ───────────────────► │  (your server)   │
│              │ ◄─────────────────── │                  │
│  ws-api CLI  │                       │  Go binary :19188│
└─────────────┘                        └──────────────────┘
                                              ▲
                                       ┌──────┴──────┐
                                       │    Caddy     │
                                       │  (TLS proxy) │
                                       │  :443 → :19188│
                                       └──────────────┘
```

**Transport**: WSS is the default (persistent connection, lower overhead). HTTPS is the automatic fallback.
**TLS**: Caddy handles automatic certificate provisioning and TLS termination.
**Data flow**: content -> json.dumps() -> WSS/HTTPS -> json.loads() -> operation

## Features

### Dual Transport: WSS + HTTPS

The client uses **WSS (WebSocket Secure)** by default for lower latency and persistent connections. If WSS is unavailable, it falls back to **HTTPS** automatically.

```bash
# Default: WSS transport
ws-api ping
# {"ok": true, "pong": true, "transport": "wss"}

# Force HTTPS mode
WS_TRANSPORT=http ws-api ping
# {"ok": true, "pong": true, "transport": "https"}
```

### Transparent Command Execution
Any unrecognized command is automatically forwarded to the remote server:
```bash
ws-api npm install           # just works
ws-api python3 train.py      # just works
ws-api kubectl get pods      # just works
```
- Stdout and stderr are separated (just like local execution)
- Exit codes are preserved
- Timeout detection with clear messaging
- Special characters in output are perfectly preserved through JSON transport

### Background Execution
Long-running commands can run in the background:
```bash
ws-api go build ./... --bg                    # returns job ID immediately
ws-api exec "make build" --bg                 # explicit exec, background
ws-api jobs                                   # list all jobs
ws-api job <id>                               # get job output (real-time if running)
ws-api job <id> --clear                       # get output & remove job
ws-api kill <id>                              # kill a running bg job
```

### File Operations
```bash
ws-api read <path> [-n] [--offset N] [--limit N]
ws-api write <path> < file
ws-api write <path> --content "inline text"
ws-api write <path> --file local_file.txt
```

### File Transfer (Binary-Safe)
Transfer binary files between local and remote using base64 encoding:
```bash
ws-api upload ./v2board-api /www/wwwroot/app/v2board-api --mode 0755
ws-api download /root/backup.tar.gz ./backup.tar.gz
```

### Text Editing (Find & Replace)
Multiple input methods to avoid any escaping issues:
```bash
# Inline (simple cases)
ws-api edit <path> --old "old" --new "new" [--all]

# Positional args
ws-api edit <path> "old text" "new text"

# From local files (complex content — zero escaping)
ws-api edit <path> --old-file old.txt --new-file new.txt

# From stdin JSON (programmatic use)
echo '{"old":"match this","new":"replace with"}' | ws-api edit <path>

# Preview changes without writing (dry-run)
ws-api edit <path> --old "old" --new "new" --dry-run
```

### Line-Based Editing
Operate by line numbers — perfect for large-scale changes:
```bash
ws-api edit-lines <path> --delete 10-20           # delete line range
ws-api edit-lines <path> --insert 15 < content    # insert at line
ws-api edit-lines <path> --replace 10-20 < new    # replace line range
```

### Batch Editing (Atomic Multi-Edit)
Apply multiple replacements in a single atomic operation:
```bash
echo '[
  {"old": "oldFunc()", "new": "newFunc()"},
  {"old": "v1.0", "new": "v2.0"}
]' | ws-api patch <path>
```

### Search
```bash
ws-api glob "*.go" [--path /root/project]
ws-api grep "TODO" [--glob "*.py"] [--context 3]
```

### Explicit Exec (with Options)
```bash
ws-api exec "go test ./..." --dir /root/project --timeout 120
ws-api exec "npm run build" --bg
```

### Raw API Access
```bash
echo '{"path":"/root/workspace"}' | ws-api raw glob
echo '{"command":"uname -a"}' | ws-api raw exec
```

## Setup

### Server

The server listens on a single HTTP port (default 19188) and relies on **Caddy** for TLS termination.

```bash
# Clone and build
git clone https://github.com/vruru/workspace-api.git
cd workspace-api
go build -o workspace-api .

# Run
WS_PORT=19188 WS_AUTH_TOKEN=your-secret-token ./workspace-api
```

#### Caddy Configuration

```
your-domain.example.com {
    reverse_proxy localhost:19188
}
```

Caddy automatically provisions TLS certificates and proxies both HTTPS and WSS traffic.

#### Systemd Service

```ini
[Unit]
Description=Workspace API (HTTP :19188, behind Caddy)
After=network.target

[Service]
Type=simple
ExecStart=/path/to/workspace-api
WorkingDirectory=/root/workspace
Restart=always
RestartSec=5
LimitNOFILE=65535
Environment=WS_PORT=19188
Environment=WS_AUTH_TOKEN=your-secret-token

[Install]
WantedBy=multi-user.target
```

### Client

Copy the `ws-api` Python script to your PATH:

```bash
cp ws-api /usr/local/bin/ws-api
chmod +x /usr/local/bin/ws-api
```

Edit the constants at the top:
```python
API_BASE = "https://your-domain.example.com"
WS_URL = "wss://your-domain.example.com/ws"
AUTH_TOKEN = "your-secret-token"
```

Requirements: Python 3.6+ with `websocket-client` (`pip install websocket-client`). Falls back to stdlib HTTPS if websocket-client is not installed.

## API Endpoints

### HTTP Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/api/exec` | POST | Execute shell commands (separated stdout/stderr) |
| `/api/exec-bg` | POST | Execute command in background, returns job ID |
| `/api/jobs` | POST | List all background jobs |
| `/api/job` | POST | Get job status and output |
| `/api/job-kill` | POST | Kill a running background job |
| `/api/read` | POST | Read file contents |
| `/api/write` | POST | Write/create files |
| `/api/upload` | POST | Upload binary file (base64) |
| `/api/download` | POST | Download binary file (base64) |
| `/api/edit` | POST | Find-and-replace text editing |
| `/api/edit-lines` | POST | Line-number-based editing |
| `/api/patch` | POST | Atomic batch find-and-replace |
| `/api/glob` | POST | Find files by pattern |
| `/api/grep` | POST | Search file contents |
| `/api/ping` | POST | Health check |

### WebSocket Endpoint

`/ws?token=your-secret-token`

Message format:
```json
{
  "id": "unique-request-id",
  "action": "exec",
  "data": {"command": "ls -la"}
}
```

Response:
```json
{
  "id": "unique-request-id",
  "ok": true,
  "stdout": "...",
  "exit_code": 0
}
```

Supported actions: all HTTP endpoint names (without `/api/` prefix), plus Bridge-compatible aliases (`terminal/exec`, `files/read`, `files/write`).

## Security Notes

- All traffic is encrypted via TLS (Caddy auto-provisions certificates)
- Bearer token authentication on every HTTP request
- WebSocket authentication via query parameter token
- The server runs commands as the server's user — scope access appropriately
- Consider firewall rules to restrict which IPs can reach the API

## Why Not Just SSH?

| | SSH | workspace-api |
|---|---|---|
| Shell escaping | Nightmare with nested quotes | Zero — JSON handles it |
| Code editing | `sed`/`awk` (fragile) | Structured find-replace + line ops |
| Batch operations | Multiple round-trips | Single atomic request |
| Error handling | Parse exit codes manually | Structured JSON responses |
| Output fidelity | Encoding issues possible | JSON preserves everything |
| AI agent friendly | Requires PTY hacks | Native WSS/HTTPS + JSON |
| Transport | TCP (no auto-TLS) | WSS + HTTPS (auto-TLS via Caddy) |

## License

MIT
