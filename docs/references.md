# References — External Sources

Sources consulted while implementing the Cailorie bot.

## Libraries
- **telebot.v3** (Telegram bot framework, webhook mode) — `gopkg.in/telebot.v3`
  - Repo: https://github.com/telebot-template/telebot (canonical: https://github.com/mukti/telebot) — used `telebot.Webhook{Listen, SecretToken, Endpoint: &WebhookEndpoint{PublicURL}}` as the poller (nginx terminates TLS, bot listens on plain HTTP inside Docker), `OnPhoto`, `bot.File`, `bot.Reply`, middleware `bot.Use`, `Settings.ParseMode = ModeHTML`, `Photo.File.FileID` (telebot auto-selects the largest photo size via `Photo.UnmarshalJSON`). telebot validates the `X-Telegram-Bot-Api-Secret-Token` header against `Webhook.SecretToken` and rejects mismatches.
- **google/generative-ai-go** (official Go SDK for Gemini) — `github.com/google/generative-ai-go/genai`
  - Repo: https://github.com/google/generative-ai-go — used `genai.NewClient(ctx, option.WithAPIKey(...))`, `client.GenerativeModel("gemini-1.5-flash")`, `model.GenerateContent(ctx, genai.ImageData(mime, bytes), genai.Text(prompt))`, response `Candidates[0].Content.Parts` typed as `genai.Text`. Auth option package: `google.golang.org/api/option`.
- **modernc.org/sqlite** (pure-Go SQLite driver) — v1.45.0 (pinned; v1.46+ requires Go 1.25)
  - Repo: https://github.com/dolmen-go/sqlite (canonical: https://gitlab.com/cznic/sqlite) — driver name `"sqlite"`; used `journal_mode(WAL)`, `busy_timeout`, `ON CONFLICT DO UPDATE` upsert.
- **robfig/cron/v3** — `github.com/robfig/cron/v3`
  - Repo: https://github.com/robfig/cron — used `cron.New(cron.WithLocation(sgt), cron.WithSeconds())` and 6-field spec `0 58 23 * * *` (seconds + 23:58 SGT daily).
- **joho/godotenv** — https://github.com/joho/godotenv — `.env` loading for local dev.

## API docs / specs
- **Telegram Bot API — getFile / photo sizes** — https://core.telegram.org/bots/api#getfile
  - Used to confirm photos arrive as an array of sizes and that `file_id` + the file download URL is how image bytes are obtained. telebot wraps this; we call `bot.File(&photo.File)` which returns an `io.ReadCloser`.
- **Telegram Bot API — sendMessage (parse_mode HTML)** — https://core.telegram.org/bots/api#sendmessage
  - Confirmed HTML parse mode supports `<b>` for the bold header and reply-to quoting via `reply_to_message_id` (telebot sets this when using `Reply`).
- **Telegram Bot API — setWebhook** — https://core.telegram.org/bots/api#setwebhook
  - Confirmed webhook params: `url` (public HTTPS), `secret_token` (1–256 chars `[A-Za-z0-9_-]`, sent back in the `X-Telegram-Bot-Api-Secret-Token` header on every update POST), `max_connections`, `drop_pending_updates`. telebot's `Webhook` struct maps these 1:1; `Endpoint.PublicURL` becomes the `url` sent to Telegram.
- **Telegram Bot API — making requests via webhook** — https://core.telegram.org/bots/api#making-requests
  - Confirmed Telegram POSTs the JSON-serialized `Update` object to the webhook URL and expects a 200 OK (or 4xx to signal failure). nginx forwards the body unchanged; telebot decodes it in `Webhook.ServeHTTP`.
- **Gemini API — Free tier limits** — https://ai.google.dev/pricing
  - gemini-1.5-flash free tier: ~15 RPM, ~1500 RPD. Adequate for a single/small number of groups. Referenced for the error-message strategy on 429.
- **Gemini API — Vision / image input** — https://ai.google.dev/gemini-api/docs/vision
  - Confirmed inline `inlineData` (bytes + mimeType) is the way to pass an image; the Go SDK exposes this via `genai.ImageData(format, data)` returning a `Blob`. Photos on Telegram are JPEG, so we pass `image/jpeg`.
- **IANA Time Zone Database — Asia/Singapore** — https://en.wikipedia.org/wiki/List_of_tz_database_time_zones
  - `time.LoadLocation("Asia/Singapore")` is UTC+8, no DST. Used for both the cron schedule and the SGT-day-boundary math in `sgtDayBounds`.

## Deployment
- **Oracle Cloud Always Free — Ampere A1 Compute** — https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm
  - Confirmed ARM Ampere VMs are part of the always-free tier; pure-Go SQLite (`CGO_ENABLED=0`) cross-compiles cleanly to `GOARCH=arm64`. The deploy mirrors the sibling fyp project (same VM, same nginx + Let's Encrypt + Docker Compose pattern — see `fyp/docs/DEPLOY_STEP_BY_STEP.md`).
- **Let's Encrypt / Certbot** — https://certbot.eff.org/
  - `certbot certonly --standalone -d cailorie.mycaregiver.xyz` provisions the cert; nginx loads `fullchain.pem` / `privkey.pem` from `/etc/letsencrypt/live/cailorie.mycaregiver.xyz/`. Auto-renewed via cron with `--post-hook "docker compose restart nginx"`. Same VM and domain family as the sibling fyp project (`api.mycaregiver.xyz`).
- **nginx reverse proxy** — https://nginx.org/en/docs/http/ngx_http_proxy_module.html
  - `proxy_pass http://bot:8080` forwards Telegram webhook POSTs to the bot container; `proxy_set_header` passes `Host`/`X-Forwarded-*` (TLS offloading). `/health` proxies to `bot:8081`.
- **GitHub Actions SSH deploy (appleboy/ssh-action)** — https://github.com/appleboy/ssh-action
  - Same action/version (`v1.0.3`) as the fyp workflow for consistency; runs `docker compose -f docker-compose.prod.yml up -d --build` on the VM over SSH.