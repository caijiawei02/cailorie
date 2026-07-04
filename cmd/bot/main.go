package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/caijiawei02/cailorie/internal/bot"
	"github.com/caijiawei02/cailorie/internal/gemini"
	"github.com/caijiawei02/cailorie/internal/llm"
	"github.com/caijiawei02/cailorie/internal/ollama"
	"github.com/caijiawei02/cailorie/internal/storage"
	"github.com/joho/godotenv"
	"github.com/robfig/cron/v3"
	telebot "gopkg.in/telebot.v3"
)

func main() {
	// Load .env (ignore error when missing — env may come from systemd).
	_ = godotenv.Load()

	tgToken := os.Getenv("TELEGRAM_TOKEN")
	chatIDsEnv := os.Getenv("GROUP_CHAT_ID")
	tzName := os.Getenv("TZ")
	dbPath := os.Getenv("DB_PATH")

	// LLM provider config.
	provider := os.Getenv("LLM_PROVIDER") // "gemini" (default) or "ollama"
	geminiKey := os.Getenv("GEMINI_API_KEY")
	geminiModel := os.Getenv("GEMINI_MODEL")
	ollamaBaseURL := os.Getenv("OLLAMA_BASE_URL") // e.g. https://api.ollama.com (Cloud) or http://localhost:11434
	ollamaModel := os.Getenv("OLLAMA_MODEL")       // e.g. llava:7b, minicpm-v, qwen2.5-vl
	ollamaKey := os.Getenv("OLLAMA_API_KEY")       // Ollama Cloud bearer token (empty for local)

	// Webhook config.
	webhookPublicURL := os.Getenv("WEBHOOK_PUBLIC_URL")
	webhookListen := os.Getenv("WEBHOOK_LISTEN")
	webhookSecret := os.Getenv("WEBHOOK_SECRET_TOKEN")
	healthListen := os.Getenv("HEALTH_LISTEN")

	if tgToken == "" {
		log.Fatal("TELEGRAM_TOKEN is required")
	}
	if provider == "" {
		provider = "gemini"
	}
	if webhookPublicURL == "" || webhookListen == "" {
		log.Fatal("WEBHOOK_PUBLIC_URL and WEBHOOK_LISTEN are required (webhook mode)")
	}
	if webhookSecret == "" {
		log.Fatal("WEBHOOK_SECRET_TOKEN is required (Telegram secret token header)")
	}
	if tzName == "" {
		tzName = "Asia/Singapore"
	}
	if dbPath == "" {
		dbPath = "cailorie.db"
	}
	if healthListen == "" {
		healthListen = ":8081"
	}

	sgt, err := time.LoadLocation(tzName)
	if err != nil {
		log.Fatalf("load timezone %q: %v", tzName, err)
	}
	// Set the process timezone so all log timestamps are local.
	if err := os.Setenv("TZ", tzName); err == nil {
		if loc, e := time.LoadLocation(tzName); e == nil {
			time.Local = loc
		}
	}

	// Parse allowed chat IDs (comma-separated).
	allowedChats := parseChatIDs(chatIDsEnv)
	if len(allowedChats) == 0 {
		log.Fatal("GROUP_CHAT_ID is required (comma-separated list of int64 chat ids)")
	}
	log.Printf("allowed chats: %v", chatIDKeys(allowedChats))

	// Open SQLite.
	db, err := storage.Open(dbPath)
	if err != nil {
		log.Fatalf("open storage: %v", err)
	}
	defer db.Close()

	// Build the LLM client based on the configured provider.
	var lc llm.Client
	switch provider {
	case "ollama":
		oc, err := ollama.New(ollamaBaseURL, ollamaModel, ollamaKey)
		if err != nil {
			log.Fatalf("ollama client: %v", err)
		}
		lc = oc
		log.Printf("LLM provider: ollama (base=%s, model=%s)", ollamaBaseURL, ollamaModel)
	case "gemini":
		fallthrough
	default:
		if geminiKey == "" {
			log.Fatal("GEMINI_API_KEY is required when LLM_PROVIDER=gemini")
		}
		gc, err := gemini.New(geminiKey, geminiModel)
		if err != nil {
			log.Fatalf("gemini client: %v", err)
		}
		lc = gc
		log.Printf("LLM provider: gemini (model=%s)", geminiModel)
	}
	defer lc.Close()

	// Build telebot (webhook). nginx terminates TLS, so the bot listens on
	// plain HTTP. Endpoint.PublicURL tells Telegram where to POST updates.
	pref := telebot.Settings{
		Token:     tgToken,
		ParseMode: telebot.ModeHTML,
		Poller: &telebot.Webhook{
			Listen:      webhookListen,
			SecretToken:  webhookSecret,
			Endpoint:    &telebot.WebhookEndpoint{PublicURL: webhookPublicURL},
			MaxConnections: 40,
			DropUpdates:    true,
		},
	}
	tgBot, err := telebot.NewBot(pref)
	if err != nil {
		log.Fatalf("telegram bot: %v", err)
	}

	// Register handlers.
	h := bot.NewHandler(tgBot, db, lc, sgt, allowedChats)
	h.Register()

	// Schedule the daily summary at 23:58 SGT, every day.
	c := cron.New(cron.WithLocation(sgt), cron.WithSeconds())
	_, err = c.AddFunc("0 58 23 * * *", func() {
		fireTime := time.Now().In(sgt)
		log.Printf("firing daily summary for %d chats at %s", len(allowedChats), fireTime.Format(time.RFC3339))
		for chatID := range allowedChats {
			bot.SendDailySummary(tgBot, db, sgt, chatID, fireTime)
		}
	})
	if err != nil {
		log.Fatalf("schedule daily summary: %v", err)
	}

	// Schedule the weekly summary at 23:58 SGT on Sundays only.
	_, err = c.AddFunc("0 58 23 * * 0", func() {
		fireTime := time.Now().In(sgt)
		log.Printf("firing weekly summary for %d chats at %s", len(allowedChats), fireTime.Format(time.RFC3339))
		for chatID := range allowedChats {
			bot.SendWeeklySummary(tgBot, db, sgt, chatID, fireTime)
		}
	})
	if err != nil {
		log.Fatalf("schedule weekly summary: %v", err)
	}
	c.Start()
	defer c.Stop()

	log.Printf("cailorie bot starting in webhook mode (TZ=%s, listen=%s, public=%s)", tzName, webhookListen, webhookPublicURL)

	// Health endpoint for Docker/nginx healthchecks (separate from the
	// telebot webhook listener).
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "ok")
		})
		if err := http.ListenAndServe(healthListen, mux); err != nil {
			log.Printf("health server on %s: %v", healthListen, err)
		}
	}()

	// Run until SIGINT/SIGTERM.
	go func() {
		tgBot.Start()
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutdown signal received, stopping...")
	tgBot.Stop()
}

func parseChatIDs(env string) map[int64]bool {
	out := map[int64]bool{}
	for _, part := range strings.Split(env, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			log.Printf("ignoring invalid chat id %q: %v", part, err)
			continue
		}
		out[id] = true
	}
	return out
}

func chatIDKeys(m map[int64]bool) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}