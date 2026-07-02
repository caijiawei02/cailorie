# Backend — Cailorie Telegram Bot

A Telegram group bot for collaborative calorie tracking. Users send food photos
to a group; the bot estimates calories via Google Gemini and logs per-user daily
totals, posting a daily summary at 23:58 SGT.

## Stack

| Concern        | Library |
|----------------|---------|
| Language        | Go 1.24 (toolchain pinned to local) |
| Telegram       | `gopkg.in/telebot.v3` (**webhook** mode; nginx terminates TLS) |
| Gemini vision   | `github.com/google/generative-ai-go/genai` (model `gemini-1.5-flash`, free-tier) |
| Storage         | SQLite via `modernc.org/sqlite` v1.45.0 (pure-Go, no CGO — easy on ARM Ampere Oracle VMs) |
| Scheduler        | `github.com/robfig/cron/v3` located in `Asia/Singapore`, seconds-field enabled |
| Config           | `github.com/joho/godotenv` + OS env |
| Reverse proxy    | nginx (SSL termination via Let's Encrypt) |
| Deploy           | Docker Compose on Oracle Cloud Free Tier VM, via GitHub Actions SSH |

## Module layout

```
cmd/bot/main.go            Entrypoint: load env, open DB, build Gemini client,
                           start telebot (webhook), register cron, run,
                           serve /health on a separate port.
internal/bot/
  handler.go               OnPhoto handler, /chatid helper, user-tracking middleware.
  reply.go                 HTML formatters for the per-photo reply and the daily summary.
  summary.go               SendDailySummary: queries per-user day totals and sends the summary.
internal/gemini/client.go  Client.EstimateCalories(ctx, imageBytes, mimeType, userText) (int, error).
internal/storage/
  db.go                    Open + migrations (meals, users tables + indexes).
  meals.go                 InsertMeal, DayMealCount, DayMeals, DayTotalsForChat.
  users.go                 UpsertUser.
internal/model/
  meal.go                  Meal struct.
  user.go                  User struct.
nginx.conf                 (lives in ~/fyp/backend/nginx.conf — cailorie server blocks appended there; NOT in this repo)
docker-compose.prod.yml    Production compose (bot only — joins the `shared` Docker network so fyp's nginx can reach it).
docker-compose.yml         Dev compose (bot only, ports exposed for ngrok testing).
.github/workflows/deploy.yml  GitHub Actions: SSH to Oracle VM, docker compose up, connect fyp's nginx to `shared`, reload.
```

## Data model

All timestamps stored as RFC3339 strings in UTC.

### `meals`
| column | type | notes |
|---|---|---|
| id | INTEGER PK AUTOINCREMENT | |
| chat_id | INTEGER | group chat id (multi-group isolation) |
| user_id | INTEGER | Telegram user id |
| username | TEXT | cached @username |
| photo_file_id | TEXT | Telegram file_id of the photo |
| calories | INTEGER | Gemini estimate |
| meal_label | INTEGER | per-user-per-SGT-day sequence (1-based) |
| caption | TEXT NOT NULL DEFAULT '' | optional user-typed description of the meal (Telegram photo caption; empty when none) |
| created_at | TEXT (RFC3339, UTC) | used for day-window queries |

Index `idx_meals_day(chat_id, user_id, created_at)`.

### `users`
| column | type | notes |
|---|---|---|
| id | INTEGER PK | |
| chat_id | INTEGER | |
| user_id | INTEGER | |
| username | TEXT | latest known |
| first_name | TEXT | latest known |
| last_seen_at | TEXT (RFC3339, UTC) | updated on every message the bot sees |

`UNIQUE(chat_id, user_id)`. Index `idx_users_chat_seen(chat_id, last_seen_at)`.

## Configuration (env)

| Var | Required | Default | Meaning |
|---|---|---|---|
| `TELEGRAM_TOKEN` | yes | — | BotFather token |
| `GEMINI_API_KEY` | yes | — | Google AI Studio key |
| `GROUP_CHAT_ID` | yes | — | comma-separated int64 group chat ids |
| `TZ` | no | `Asia/Singapore` | IANA timezone for day boundaries + cron |
| `DB_PATH` | no | `cailorie.db` | SQLite file path |
| `WEBHOOK_PUBLIC_URL` | yes | — | public HTTPS URL Telegram POSTs to, e.g. `https://cailorie.mycaregiver.xyz/tg/<secret>/` |
| `WEBHOOK_LISTEN` | yes | — | bot's webhook HTTP listener addr (inside container), e.g. `:8080` |
| `WEBHOOK_SECRET_TOKEN` | yes | — | random string for the `X-Telegram-Bot-Api-Secret-Token` header (1–256 chars `[A-Za-z0-9_-]`) |
| `HEALTH_LISTEN` | no | `:8081` | addr of the `/health` endpoint for Docker/nginx healthchecks |

## Flows

### Per-photo reply (quote-reply, HTML)
1. Middleware silently upserts the sender into `users` (for any message type in an allowed chat).
2. `OnPhoto` handler:
   - Downloads the largest photo size via `bot.File(&msg.Photo.File)`.
   - Reads the optional Telegram photo caption from `msg.Caption` (empty when the user sent a photo with no text).
   - Sends bytes + prompt to the vision LLM. The prompt is built by `llm.CaloriePromptFor(userText)`: when a caption is present it is appended as `The user described this meal as: "<caption>". Use this description to guide your estimate.`; when absent the bare base prompt is used (preserving prior behaviour). The base instruction asks for a single-integer reply, or `NA` if not food.
   - On `NA` / parse failure → error reply, **no DB write**.
   - On a parseable integer → compute meal number = `DayMealCount(...)+1`, `InsertMeal`, then `DayMeals` (full day's meals for that user) and quote-reply:
     ```
     <b>@username</b> on 02 January 2026
     Meal 1: 450 calories
     Meal 2: 120 calories

     Total: 570 calories
     ```
3. Display name: `@username` when present, else first name (HTML-escaped).

### Daily summary (`0 58 23 * * *` in SGT)
- Fires at **23:58:00 SGT** every day, for **each** allowed chat.
- Snapshot window is `[00:00 SGT today, fireTime)` (upper bound = now), so any meal logged during the 23:58–00:00 tail still counts toward today and is not lost.
- Query (`storage.DayTotalsForChat`): `users` LEFT JOIN `meals` filtered to the window, **only users with `last_seen_at >= dayStart`** (i.e. active in the group today). Zero-meal users appear as `0 calories (0 meals)`. Ordered by total DESC, then username/first_name ASC.
- Message:
  ```
  📊 Daily Calorie Summary — 02 January 2026

  @user1 — 650 calories (3 meals)
  @user2 — 200 calories (1 meal)
  @user3 — 0 calories (0 meals)
  ```
- If no users were active today at all → `No meals were logged today.`

### Multi-group support
- `GROUP_CHAT_ID` is a comma-separated list; parsed into `map[int64]bool` in `main.go`.
- Both handlers and the summary loop iterate per-`chat_id`; data is isolated by the `chat_id` column in every query.
- One bot process serves all configured groups (efficient on a single Oracle VM).

### `/chatid` helper
- Replies with the current `chat.ID`. Works in DM or any group; used during setup to discover the id to put in `GROUP_CHAT_ID`.

## Gemini usage notes
- Model: `gemini-1.5-flash` (free-tier friendly).
- Prompt: built by `llm.CaloriePromptFor(userText)` in `internal/llm/llm.go`. Base instruction: "Identify the food ... reply with ONLY a single integer ... If not food, reply exactly: NA". When the user attaches a caption to their photo (e.g. "Spicy Ramen with chicken cutlet"), it is appended so the model can use the description to guide its estimate. The Ollama provider (`internal/ollama/client.go`) builds the same prompt into its chat message.
- Response parsing tolerates stray text (e.g. "450 calories") by extracting the first integer via regex; bare `NA` or empty → `ErrNotFood`.
- Free-tier limits (~15 RPM / ~1500 req/day) are shared across all groups. On 429 / other API errors the bot replies "Gemini is busy right now" and records nothing.

## Error handling
- Non-allowed chats: silently ignored (no tracking, no reply).
- Download/parse errors: user-facing error reply, no DB write.
- Gemini `ErrNotFood`: user-facing "couldn't identify the meal" reply, no DB write.
- Internal DB errors: user-facing "internal error" reply, logged.

## Webhook architecture

```
                          ┌─────────────────── Oracle VM (same as fyp) ──────────────────┐
                          │                                                              │
Telegram ──HTTPS POST──▶  │  fyp nginx:443 (shared, Let's Encrypt TLS)                   │
                          │   ├─ Host: api.mycaregiver.xyz   → http://api:8000  (fyp)     │
                          │   └─ Host: cailorie.mycaregiver.xyz                          │
                          │         ├─ /tg/   → http://cailorie-bot:8080  (this bot)     │
                          │         └─ /health→ http://cailorie-bot:8081                  │
                          │                  │                                             │
                          │                  ▼                                             │
                          │           cailorie bot (HTTP, plain) ──▶ Gemini (outbound)   │
                          │                                                              │
                          └──────────────────────────────────────────────────────────────┘
```

- **Shared nginx, separate subdomain:** cailorie reuses the *same* nginx process (and the *same* `nginx.conf` file) as the sibling fyp project. The cailorie `server` blocks for `cailorie.mycaregiver.xyz` are **appended directly into `~/fyp/backend/nginx.conf`** (under a clearly-marked comment header) — no second nginx, no directory of separate files, no port 80/443 conflict. Routing is by `Host` header: fyp keeps `api.mycaregiver.xyz`, cailorie gets `cailorie.mycaregiver.xyz`. Both are A records to the same VM IP.
- **Why separate subdomain:** keeps cailorie decoupled from the FYP lifecycle (when FYP is retired, move the cailorie server block into a standalone nginx and repoint DNS). No path-namespace sharing.
- **Shared Docker network:** both composes attach containers to an external `shared` network so fyp's `caregiver-nginx` can resolve `cailorie-bot`. Created once on the VM: `docker network create shared`, then `docker network connect shared caregiver-nginx`.
- **TLS:** the shared fyp nginx terminates TLS with a separate Let's Encrypt cert for `cailorie.mycaregiver.xyz` (provisioned via certbot; separate from fyp's `api.mycaregiver.xyz` cert, both on the same VM). The bot container listens on plain HTTP inside the Docker network — never exposed directly.
- **Secret token:** `WEBHOOK_SECRET_TOKEN` is sent by Telegram in the `X-Telegram-Bot-Api-Secret-Token` header. telebot validates it (`Webhook.SecretToken`) and rejects requests without it. nginx passes the header through unchanged.
- **Webhook path:** a random secret path segment in `WEBHOOK_PUBLIC_URL` (e.g. `/tg/a1b2c3d4e5/`) provides defense-in-depth on top of the secret-token header.
- **Source of truth for the cailorie nginx block:** `~/fyp/backend/nginx.conf` (committed in the fyp repo). The cailorie repo itself does **not** carry an `nginx.conf`.

## Deployment (Oracle Cloud Ampere VM, mirroring the fyp project)

### One-time VM setup (matches `fyp/docs/DEPLOY_STEP_BY_STEP.md`)
1. **OCI Console → Networking → VCN → Security Lists → Add Ingress Rules:** `0.0.0.0/0` → ports `22`, `80`, `443`. (Already done for fyp — skip if fyp is already deployed on this VM.)
2. **VM:** install Docker, Git, add 2G swap (A1.Flex needs it). (Already done for fyp — skip.)
3. **DNS:** add A record `cailorie.mycaregiver.xyz` → VM public IP (same IP as `api.mycaregiver.xyz`).
4. **TLS cert for `cailorie.mycaregiver.xyz`:**
   ```sh
   # Stop fyp's nginx so certbot can bind port 80 for the challenge:
   docker compose -f ~/fyp/backend/docker-compose.prod.yml stop nginx
   sudo certbot certonly --standalone -d cailorie.mycaregiver.xyz
   # certs land in /etc/letsencrypt/live/cailorie.mycaregiver.xyz/
   docker compose -f ~/fyp/backend/docker-compose.prod.yml start nginx
   ```
5. **Connect fyp's nginx to the shared Docker network** so it can resolve `cailorie-bot`:
   ```sh
   docker network create shared
   ```
   (fyp's `docker-compose.prod.yml` now declares `networks: shared: external: true` for the `nginx` service, so `caregiver-nginx` joins `shared` automatically on every `docker compose up`. fyp's deploy workflow also ensures `shared` exists before starting. The cailorie `server` blocks for `cailorie.mycaregiver.xyz` are already committed in `~/fyp/backend/nginx.conf` — no file changes needed on the VM.)
6. **Auto-renew cron** (sudo crontab -e) — renew both certs and reload the shared nginx:
   ```
   0 0,12 * * * certbot renew --quiet --post-hook "docker compose -f $HOME/fyp/backend/docker-compose.prod.yml restart nginx"
   ```

### CI/CD via GitHub Actions
- Secrets (repo → Settings → Secrets → Actions): `OCI_VM_IP`, `OCI_SSH_PRIVATE_KEY`. (Username is hardcoded to `ubuntu`, matching the fyp workflow.)
- On push to `main`, `.github/workflows/deploy.yml` SSHes into the VM, pulls the latest code, runs `docker compose -f docker-compose.prod.yml up -d --build`, then connects fyp's `caregiver-nginx` to the `shared` network and reloads it (the cailorie `server` block is already in `~/fyp/backend/nginx.conf`, committed via the fyp repo).
- First deploy: the workflow copies `.env.example` → `.env` if missing. **SSH in and fill in the real secrets** (`TELEGRAM_TOKEN`, `GEMINI_API_KEY`, `GROUP_CHAT_ID`, `WEBHOOK_*`) before the bot will start.

### Manual deploy (without GitHub Actions)
```sh
# On the VM:
cd ~/cailorie
cp .env.example .env && nano .env   # fill in secrets
docker network create shared 2>/dev/null || true
docker compose -f docker-compose.prod.yml up -d --build
docker compose -f docker-compose.prod.yml logs -f bot

# Connect fyp's nginx to the shared network and reload (cailorie server block
# is already in ~/fyp/backend/nginx.conf, committed via the fyp repo):
docker network connect shared caregiver-nginx 2>/dev/null || true
docker compose -f ~/fyp/backend/docker-compose.prod.yml exec nginx nginx -s reload
```

### Local dev with webhook (ngrok)
```sh
cp .env.example .env && nano .env
# Set WEBHOOK_PUBLIC_URL to your ngrok URL, e.g. https://xxxx.ngrok-free.app/tg/secret/
docker compose -f docker-compose.yml up -d --build   # bot on :8080/:8081, no nginx
# In another terminal:
ngrok http 8080
# Update WEBHOOK_PUBLIC_URL to the ngrok URL + /tg/secret/ and restart the bot.
```

## Build (cross-compile for ARM64)
```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o cailorie ./cmd/bot
```
(`CGO_ENABLED=0` works because `modernc.org/sqlite` is pure-Go.) The Dockerfile does this inside a `golang:1.24-alpine` build stage.

## Bot setup in Telegram
1. Create the bot via @BotFather, copy the token into `TELEGRAM_TOKEN`.
2. Add the bot to your group as a regular member (no admin rights needed).
3. Send `/chatid` in the group (or DM the bot `/chatid`) to get the numeric `chat_id`.
4. Put that id (or a comma-separated list) into `GROUP_CHAT_ID` and restart the service.

## Files changed in this build
- `go.mod`, `go.sum` (new): module `github.com/caijiawei02/cailorie`, deps as above.
- `cmd/bot/main.go` (new): wiring, webhook poller, cron, signal handling, /health server.
- `internal/bot/{handler,reply,summary}.go` (new).
- `internal/gemini/client.go` (new).
- `internal/storage/{db,meals,users}.go` (new).
- `internal/model/{meal,user}.go` (new).
- `Dockerfile`, `docker-compose.prod.yml`, `docker-compose.yml` (new).
- `.github/workflows/deploy.yml` (new): GitHub Actions SSH deploy to Oracle VM.
- `.env.example`, `.gitignore` (new).
- (In the sibling fyp repo) `backend/nginx.conf`: appended the `cailorie.mycaregiver.xyz` server blocks under a comment header — this is the source of truth for the cailorie reverse-proxy config.