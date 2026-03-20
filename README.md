# workspace-api

A lightweight, secure HTTP API that lets AI agents (and humans) operate on remote servers as if they were local — no SSH sessions, no shell escaping headaches.

## The Problem

When AI coding agents (Claude, Cursor, Copilot, etc.) need to work on remote servers, they face painful limitations:

- **Shell escaping hell**: Passing code snippets through SSH → bash → commands means quotes, backslashes, `$variables`, and special characters get mangled at every layer
- **No structured operations**: `sed` and `awk` are fragile for code editing — one wrong regex and your file is corrupted
- **Lost context**: SSH sessions are stateless, commands run in isolation, and error handling is primitive
- **Binary-unsafe output**: Command output goes through multiple encoding layers, often corrupting non-ASCII content

## The Solution

**workspace-api** exposes your server's filesystem and shell through a clean JSON-over-HTTPS API. All content travels through `json.dumps()` → HTTPS → `json.loads()` — never through shell parsing.

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

## Features

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
Long-running commands can run in the background, freeing you to do other work while they complete:
```bash
# Add --bg to any command to run it in the background
ws-api go build ./... --bg                    # returns job ID immediately
ws-api exec "make test" --bg                  # explicit exec, also works

# Manage background jobs
ws-api jobs                                   # list all jobs (ID, status, command)
ws-api job <id>                               # get job output (real-time if running)
ws-api job <id> --clear                       # get output & remove job from list
ws-api kill <id>                              # kill a running job
```
- Jobs persist on the server until cleared or the server restarts
- `ws-api job <id>` shows live stdout/stderr even while the job is still running
- Exit codes and timeout status are tracked per job

### File Operations
```bash
ws-api read <path> [-n] [--offset N] [--limit N]
ws-api write <path> < file
ws-api write <path> --content "inline text"
ws-api write <path> --file local_file.txt
```

### File Transfer (Binary-Safe)
Transfer binary files (compiled binaries, images, archives, etc.) between local and remote using base64 encoding:
```bash
# Upload local file to remote server
ws-api upload ./v2board-api /www/wwwroot/xiaovb/v2board-api --mode 0755

# Download remote file to local
ws-api download /root/backup.tar.gz ./backup.tar.gz
```
- Binary-safe: content is base64-encoded in transit, decoded on arrival
- `--mode` sets file permissions on upload (default: 0644)
- Local directories are auto-created on download
- Replaces the need for `scp` over SSH

### Text Editing (Find & Replace)
Multiple input methods to avoid any escaping issues:
```bash
# Inline (simple cases)
ws-api edit <path> --old "old" --new "new" [--all]

# From local files (complex content — zero escaping)
ws-api edit <path> --old-file old.txt --new-file new.txt

# From stdin JSON (programmatic use)
echo '{"old":"match this","new":"replace with"}' | ws-api edit <path>

# Preview changes without writing (dry-run)
ws-api edit <path> --old "old" --new "new" --dry-run
# Shows: match count, line numbers, size delta — no file modification
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
When you need to pass extra options like working directory or timeout:
```bash
ws-api exec "go test ./..." --dir /root/project --timeout 120
ws-api exec "npm run build" --bg                 # background mode
```
- `--dir`: Set working directory for the command
- `--timeout`: Override default timeout (seconds)
- `--bg`: Run in background (see Background Execution above)

### Raw API Access
Bypass the CLI and call any API endpoint directly with custom JSON:
```bash
echo '{"path":"/root/workspace"}' | ws-api raw glob
echo '{"command":"uname -a"}' | ws-api raw exec
```
Useful for accessing endpoint features not yet wrapped by the CLI, or for scripting/automation.

## Architecture

```
┌─────────────┐     HTTPS/JSON      ┌──────────────────┐
│  AI Agent    │ ──────────────────► │  workspace-api   │
│  (sandbox)   │                     │  (your server)   │
│              │ ◄────────────────── │                  │
│  ws-api CLI  │     JSON response   │  Go binary       │
└─────────────┘                      └──────────────────┘

Data flow: content → json.dumps() → HTTPS → json.loads() → operation
                                                              │
Result:   display ← json.loads() ← HTTPS ← json.dumps() ← result
```

Zero shell parsing at any point. Special characters, quotes, backslashes, unicode — all handled automatically by JSON serialization.

## Setup

### Server (the machine you want to control)

```bash
# Clone and build
git clone https://github.com/vruru/workspace-api.git
cd workspace-api
go build -o workspace-api .

# Configure (optional — has sensible defaults)
cat > /root/workspace/.env << 'ENVEOF'
WS_DOMAIN=your-domain.example.com
WS_AUTH_TOKEN=your-secret-token
WS_CERT_DIR=/path/to/cert/cache
ENVEOF

# Run (auto-provisions TLS via Let's Encrypt)
./workspace-api
```

The server automatically:
- Obtains and renews TLS certificates via Let's Encrypt
- Listens on ports 80 (redirect) and 443 (API)
- Authenticates all requests via Bearer token

### Client (the AI agent's environment)

Copy the `ws-api` Python script to your PATH:

```bash
cp ws-api /usr/local/bin/ws-api
chmod +x /usr/local/bin/ws-api
```

Edit the two constants at the top:
```python
API_BASE = "https://your-domain.example.com"
AUTH_TOKEN = "your-secret-token"
```

Requirements: Python 3.6+ (uses only stdlib — no pip install needed).

## API Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/api/exec` | POST | Execute shell commands (separated stdout/stderr) |
| `/api/exec-bg` | POST | Execute command in background, returns job ID |
| `/api/jobs` | POST | List all background jobs |
| `/api/job` | POST | Get job status and output (real-time if running) |
| `/api/job-kill` | POST | Kill a running background job |
| `/api/read` | POST | Read file contents (with optional line numbers, offset, limit) |
| `/api/write` | POST | Write/create files (auto-creates directories) |
| `/api/upload` | POST | Upload binary file (base64 encoded) |
| `/api/download` | POST | Download binary file (base64 encoded) |
| `/api/edit` | POST | Find-and-replace text editing (supports dry-run) |
| `/api/edit-lines` | POST | Line-number-based delete/insert/replace |
| `/api/patch` | POST | Atomic batch find-and-replace |
| `/api/glob` | POST | Find files by pattern |
| `/api/grep` | POST | Search file contents |
| `/api/ping` | POST | Health check |

## Security Notes

- All traffic is encrypted via TLS (auto-provisioned certificates)
- Bearer token authentication on every request
- The server runs commands as the server's user — scope access appropriately
- Consider firewall rules to restrict which IPs can reach the API
- The auth token should be strong and kept secret

## Why Not Just SSH?

| | SSH | workspace-api |
|---|---|---|
| Shell escaping | Nightmare with nested quotes | Zero — JSON handles it |
| Code editing | `sed`/`awk` (fragile) | Structured find-replace + line ops |
| Batch operations | Multiple round-trips | Single atomic request |
| Error handling | Parse exit codes manually | Structured JSON responses |
| Output fidelity | Encoding issues possible | JSON preserves everything |
| AI agent friendly | Requires PTY hacks | Native HTTP/JSON |

## Use Cases

- **AI coding agents** operating on remote dev/build servers
- **CI/CD pipelines** that need to edit config files on remote hosts
- **Remote development** when you want local-feeling file operations
- **Server automation** without the complexity of Ansible/Chef for simple tasks

## License

MIT
