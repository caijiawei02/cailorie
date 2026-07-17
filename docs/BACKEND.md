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
  handler.go               OnPhoto handler, /chatid, /today, /yesterday, /highscore, /leaderboard, /week, /deletelast, /help, user-tracking middleware.
  reply.go                 HTML formatters for per-photo reply, all-users daily meals, daily/weekly summary, yesterday summary, high scores, leaderboard scores, and help text.
  summary.go               SendDailySummary: queries per-user day totals and sends the summary. SendWeeklySummary: queries weekly averages and sends on Sundays.
internal/gemini/client.go  Client.EstimateCalories(ctx, imageBytes, mimeType, userText) (int, error).
internal/storage/
  db.go                    Open + migrations (meals, users tables + indexes).
  meals.go                 InsertMeal, DeleteMeal, LastMealToday, DayMealCount, DayMeals, DayMealsForChat, DayTotalsForChat, ChatHighScores, LeaderboardScoresForChat, WeeklyAvgForChat.
  users.go                 UpsertUser.
internal/model/
  meal.go                  Meal struct.
  user.go                  User struct.
nginx.conf                 (lives in ~/server-stuff/nginx.conf — cailorie server blocks committed there; NOT in this repo)
docker-compose.prod.yml    Production compose (bot only — joins the `shared` Docker network so the edge nginx in ~/server-stuff can reach it).
docker-compose.yml         Dev compose (bot only, ports exposed for ngrok testing).
.github/workflows/deploy.yml  GitHub Actions: SSH to Oracle VM, docker compose up, reload ~/server-stuff's nginx.
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
      <b>@username — 02 January 2026</b>

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
   <b>Daily Calorie Summary — 02 January 2026</b>

   @user1 — 650 calories (3 meals)
   @user2 — 200 calories (1 meal)
   @user3 — 0 calories (0 meals)
   ```
- If no users were active today at all → `No meals were logged today.`

### Weekly summary (`0 59 23 * * 0` in SGT — Sundays only)
- Fires at **23:59:00 SGT on Sundays**, for **each** allowed chat, as a **separate message** after the daily summary.
- Snapshot window is `[Monday 00:00 SGT of the current week, fireTime)`. Uses `sgtWeekStart` to compute the Monday.
- Query (`storage.WeeklyAvgForChat`): `meals` JOIN `users` filtered to the window, grouped by user. Only users who logged at least 1 meal during the week appear (no 0-day rows). Average = `total / days_logged` (integer division). Ordered by avg DESC, then username ASC.
- Message:
  ```
  <b>Weekly Average — 30 June 2026</b>

  @user1 — 1332 calories/day (6 days)
  @user2 — 428 calories/day (3 days)
  ```
- Header date = Monday that started the week.
- If no meals were logged that week → `No meals were logged this week.`

### Multi-group support
- `GROUP_CHAT_ID` is a comma-separated list; parsed into `map[int64]bool` in `main.go`.
- Both handlers and the summary loop iterate per-`chat_id`; data is isolated by the `chat_id` column in every query.
- One bot process serves all configured groups (efficient on a single Oracle VM).

### `/today` — everyone's meals today
- Replies with every user's meals logged so far today (SGT day) in the current chat, with per-user subtotals and a grand total.
- Uses `DayMealsForChat` and `formatAllMealsReply`.

### `/yesterday` — everyone's yesterday summary
- Replies with every user's calorie totals from yesterday (SGT day), using `DayTotalsForChat` with yesterday's window. Same format as the daily summary but for yesterday.
- Replies "No meals were logged on <date>" if none.

### `/highscore` — all users' all-time high scores
- Replies with every user's highest-calorie day in the chat, using `ChatHighScores` which uses `ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY total DESC)` to pick each user's best day. Ordered by total DESC.
- Format:
  ```
  <b>High Scores — All Time</b>

  @user1 — 1250 calories (4 meals on 02 January 2026)
  @user2 — 980 calories (3 meals on 28 December 2025)
  ```
- Replies "No meals have been logged yet." if the chat has no meals at all.

### `/leaderboard` — all-time leaderboard points
- Replies with each user's total points: number of days they tied for the highest calorie total, excluding days with only one participant.
- Uses `LeaderboardScoresForChat` with a SQL query using `RANK() OVER (PARTITION BY DATE(...) ORDER BY SUM(calories) DESC)` and `COUNT(DISTINCT user_id) OVER (PARTITION BY DATE(...))` to detect ties and solo days.
- Only days with 2+ participants count. Ties award +1 to all tied users.
- Ordered by score DESC, then username ASC.
- Format:
  ```
  <b>Leaderboard — All Time</b>

  <b>Scores</b>
  @alice — 2 points
  @bob — 1 point
  ```
- Replies "No leaderboard data yet. At least two participants need to log meals on the same day." if no qualifying days exist.

### `/week` — everyone's weekly average calories/day
- Replies with every user's average calories per day for the current week (Monday–now, SGT) in the current chat.
- Uses `WeeklyAvgForChat` — the same query and format as the cron-based weekly summary, but on-demand.
- Only users who logged at least 1 meal during the week appear (no 0-day rows).
- Average = total calories / number of distinct days with at least 1 meal (integer division).
- If no meals were logged this week: replies `No meals were logged this week.`

### `/chatid` helper
- Replies with the current `chat.ID`. Works in DM or any group; used during setup to discover the id to put in `GROUP_CHAT_ID`.

### `/help` — list available commands
- Replies with an HTML message listing all commands and a brief "How to Log Meals" section.

### `/deletelast` — delete sender's last meal today
- Hard-deletes the caller's most recent meal today (SGT day) and confirms deletion.
- Format: `{displayName} — deleted Meal {label} ({calories} calories).`
- If the user has no meals today: replies `{displayName} — no meals to delete today.`
- Uses `LastMealToday` (ordered by `created_at DESC LIMIT 1`) and `DeleteMeal` for the hard delete.

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
Telegram ──HTTPS POST──▶  │  edge nginx:443 (~/server-stuff, Let's Encrypt TLS)          │
                           │   ├─ Host: api.mycaregiver.xyz   → http://caregiver-api:8000  │
                           │   └─ Host: cailorie.mycaregiver.xyz                          │
                           │         ├─ /tg/   → http://cailorie-bot:8080  (this bot)     │
                           │         └─ /health→ http://cailorie-bot:8081                  │
                           │                  │                                             │
                           │                  ▼                                             │
                           │           cailorie bot (HTTP, plain) ──▶ Gemini (outbound)   │
                           │                                                              │
                           └──────────────────────────────────────────────────────────────┘
```

- **Shared nginx, separate subdomain:** cailorie reuses the *same* nginx process (and the *same* `nginx.conf` file) as the sibling fyp and telegram-order-bot projects. The cailorie `server` blocks for `cailorie.mycaregiver.xyz` live in **`~/server-stuff/nginx.conf`** (the shared edge-proxy repo) — no second nginx, no directory of separate files, no port 80/443 conflict. Routing is by `Host` header: fyp keeps `api.mycaregiver.xyz`, cailorie gets `cailorie.mycaregiver.xyz`. Both are A records to the same VM IP.
- **Why separate subdomain:** keeps cailorie decoupled from the FYP lifecycle. No path-namespace sharing.
- **Shared Docker network:** both composes attach containers to an external `shared` network so the edge nginx can resolve `cailorie-bot`. Created once on the VM: `docker network create shared`; nginx joins it via `~/server-stuff/docker-compose.yml`, cailorie joins it via `~/cailorie/docker-compose.prod.yml`.
- **TLS:** the edge nginx terminates TLS with a separate Let's Encrypt cert for `cailorie.mycaregiver.xyz` (provisioned via certbot; separate from fyp's `api.mycaregiver.xyz` cert, both on the same VM). The bot container listens on plain HTTP inside the Docker network — never exposed directly.
- **Secret token:** `WEBHOOK_SECRET_TOKEN` is sent by Telegram in the `X-Telegram-Bot-Api-Secret-Token` header. telebot validates it (`Webhook.SecretToken`) and rejects requests without it. nginx passes the header through unchanged.
- **Webhook path:** a random secret path segment in `WEBHOOK_PUBLIC_URL` (e.g. `/tg/a1b2c3d4e5/`) provides defense-in-depth on top of the secret-token header.
- **Source of truth for the cailorie nginx block:** `~/server-stuff/nginx.conf` (committed in the server-stuff repo). The cailorie repo itself does **not** carry an `nginx.conf`.

## Deployment (Oracle Cloud Ampere VM, mirroring the fyp project)

### One-time VM setup (matches `fyp/docs/DEPLOY_STEP_BY_STEP.md`)
1. **OCI Console → Networking → VCN → Security Lists → Add Ingress Rules:** `0.0.0.0/0` → ports `22`, `80`, `443`. (Already done for fyp — skip if fyp is already deployed on this VM.)
2. **VM:** install Docker, Git, add 2G swap (A1.Flex needs it). (Already done for fyp — skip.)
3. **DNS:** add A record `cailorie.mycaregiver.xyz` → VM public IP (same IP as `api.mycaregiver.xyz`).
4. **TLS cert for `cailorie.mycaregiver.xyz`:**
   ```sh
   # The edge nginx (~/server-stuff) must be up to serve the ACME challenge on
   # port 80. Using webroot mode means we don't need to stop nginx:
   cd ~/server-stuff && docker compose up -d
   sudo certbot certonly --webroot -w /var/www/certbot -d cailorie.mycaregiver.xyz
   # certs land in /etc/letsencrypt/live/cailorie.mycaregiver.xyz/
   docker compose -f ~/server-stuff/docker-compose.yml exec -T nginx nginx -s reload
   ```
5. **Shared Docker network** so the edge nginx can resolve `cailorie-bot`:
   ```sh
   docker network create shared
   ```
   (nginx joins `shared` via `~/server-stuff/docker-compose.yml`; cailorie-bot
   joins it via `~/cailorie/docker-compose.prod.yml`. Both deploy workflows
   ensure `shared` exists before starting. The cailorie `server` blocks are
   committed in `~/server-stuff/nginx.conf` — no file changes needed on the VM.)
6. **Auto-renew cron** (sudo crontab -e) — renew all certs and reload the edge nginx:
   ```
   0 0,12 * * * certbot renew --quiet --post-hook "docker compose -f $HOME/server-stuff/docker-compose.yml restart nginx"
   ```

### CI/CD via GitHub Actions
- Secrets (repo → Settings → Secrets → Actions): `OCI_VM_IP`, `OCI_SSH_PRIVATE_KEY`. (Username is hardcoded to `ubuntu`, matching the fyp workflow.)
- On push to `main`, `.github/workflows/deploy.yml` SSHes into the VM, pulls the latest code, runs `docker compose -f docker-compose.prod.yml up -d --build`, then reloads the edge nginx in `~/server-stuff` (the cailorie `server` block is already committed in `~/server-stuff/nginx.conf`, deployed via the server-stuff repo).
- First deploy: the workflow copies `.env.example` → `.env` if missing. **SSH in and fill in the real secrets** (`TELEGRAM_TOKEN`, `GEMINI_API_KEY`, `GROUP_CHAT_ID`, `WEBHOOK_*`) before the bot will start.

### Manual deploy (without GitHub Actions)
```sh
# On the VM:
cd ~/cailorie
cp .env.example .env && nano .env   # fill in secrets
docker network create shared 2>/dev/null || true
docker compose -f docker-compose.prod.yml up -d --build
docker compose -f docker-compose.prod.yml logs -f bot

# Reload the edge nginx (cailorie server block is in ~/server-stuff/nginx.conf,
# committed via the server-stuff repo):
docker compose -f ~/server-stuff/docker-compose.yml exec -T nginx nginx -s reload
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
- (In the sibling `server-stuff` repo) `nginx.conf`: the `cailorie.mycaregiver.xyz` server blocks live here (under a comment header) — this is the source of truth for the cailorie reverse-proxy config.