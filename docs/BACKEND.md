# Backend — Cailorie Telegram Bot

A Telegram group bot for collaborative calorie tracking. Users send food photos
to a group; the bot estimates calories via Google Gemini and logs per-user daily
totals, posting a daily summary at 23:58 SGT.

## Stack

| Concern        | Library |
|----------------|---------|
| Language        | Go 1.24 (toolchain pinned to local) |
| Telegram       | `gopkg.in/telebot.v3` (long-polling, no inbound port needed) |
| Gemini vision   | `github.com/google/generative-ai-go/genai` (model `gemini-1.5-flash`, free-tier) |
| Storage         | SQLite via `modernc.org/sqlite` v1.45.0 (pure-Go, no CGO — easy on ARM Ampere Oracle VMs) |
| Scheduler        | `github.com/robfig/cron/v3` located in `Asia/Singapore`, seconds-field enabled |
| Config           | `github.com/joho/godotenv` + OS env |

## Module layout

```
cmd/bot/main.go            Entrypoint: load env, open DB, build Gemini client,
                           start telebot (long-polling), register cron, run.
internal/bot/
  handler.go               OnPhoto handler, /chatid helper, user-tracking middleware.
  reply.go                 HTML formatters for the per-photo reply and the daily summary.
  summary.go               SendDailySummary: queries per-user day totals and sends the summary.
internal/gemini/client.go  Client.EstimateCalories(ctx, imageBytes, mimeType) (int, error).
internal/storage/
  db.go                    Open + migrations (meals, users tables + indexes).
  meals.go                 InsertMeal, DayMealCount, DayMeals, DayTotalsForChat.
  users.go                 UpsertUser.
internal/model/
  meal.go                  Meal struct.
  user.go                  User struct.
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

## Flows

### Per-photo reply (quote-reply, HTML)
1. Middleware silently upserts the sender into `users` (for any message type in an allowed chat).
2. `OnPhoto` handler:
   - Downloads the largest photo size via `bot.File(&msg.Photo.File)`.
   - Sends bytes to Gemini with a prompt instructing a single-integer reply, or `NA` if not food.
   - On `NA` / parse failure → error reply, **no DB write**.
   - On a parseable integer → compute meal number = `DayMealCount(...)+1`, `InsertMeal`, then `DayMeals` (full day's meals for that user) and quote-reply:
     ```
     <b>@username</b> on 02 January 2026
     Meal 1: 450 calories
     Meal 2: 120 calories

     Total calories: 570 calories
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
- Prompt: "Identify the food ... reply with ONLY a single integer ... If not food, reply exactly: NA".
- Response parsing tolerates stray text (e.g. "450 calories") by extracting the first integer via regex; bare `NA` or empty → `ErrNotFood`.
- Free-tier limits (~15 RPM / ~1500 req/day) are shared across all groups. On 429 / other API errors the bot replies "Gemini is busy right now" and records nothing.

## Error handling
- Non-allowed chats: silently ignored (no tracking, no reply).
- Download/parse errors: user-facing error reply, no DB write.
- Gemini `ErrNotFood`: user-facing "couldn't identify the meal" reply, no DB write.
- Internal DB errors: user-facing "internal error" reply, logged.

## Deployment (Oracle Cloud Ampere VM, Ubuntu/Oracle Linux)

1. **Build** (on the VM or cross-compile from your machine):
   ```sh
   GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o cailorie ./cmd/bot
   ```
   (`CGO_ENABLED=0` works because `modernc.org/sqlite` is pure-Go.)

2. **Install** binary at `/usr/local/bin/cailorie`, config at `/etc/cailorie/.env`, data dir at `/var/lib/cailorie`.

3. **Service user**:
   ```sh
   sudo useradd -r -d /var/lib/cailorie -s /usr/sbin/nologin cailorie
   sudo mkdir -p /var/lib/cailorie && sudo chown cailorie:cailorie /var/lib/cailorie
   ```

4. **systemd**: copy `systemd/cailorie.service` to `/etc/systemd/system/`, then:
   ```sh
   sudo systemctl daemon-reload
   sudo systemctl enable --now cailorie
   sudo journalctl -u cailorie -f
   ```

5. **Networking**: long-polling makes outbound HTTPS only — no inbound port needs to be opened on the Oracle VCN firewall.

## Bot setup in Telegram
1. Create the bot via @BotFather, copy the token into `TELEGRAM_TOKEN`.
2. Add the bot to your group as a regular member (no admin rights needed).
3. Send `/chatid` in the group (or DM the bot `/chatid`) to get the numeric `chat_id`.
4. Put that id (or a comma-separated list) into `GROUP_CHAT_ID` and restart the service.

## Files changed in this build
- `go.mod`, `go.sum` (new): module `github.com/caijiawei02/cailorie`, deps as above.
- `cmd/bot/main.go` (new): wiring, cron, signal handling.
- `internal/bot/{handler,reply,summary}.go` (new).
- `internal/gemini/client.go` (new).
- `internal/storage/{db,meals,users}.go` (new).
- `internal/model/{meal,user}.go` (new).
- `.env.example`, `.gitignore`, `Dockerfile`, `systemd/cailorie.service` (new).