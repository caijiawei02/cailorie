package main

import (
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/caijiawei02/cailorie/internal/bot"
	"github.com/caijiawei02/cailorie/internal/gemini"
	"github.com/caijiawei02/cailorie/internal/storage"
	"github.com/joho/godotenv"
	telebot "gopkg.in/telebot.v3"
	"github.com/robfig/cron/v3"
)

func main() {
	// Load .env (ignore error when missing — env may come from systemd).
	_ = godotenv.Load()

	tgToken := os.Getenv("TELEGRAM_TOKEN")
	geminiKey := os.Getenv("GEMINI_API_KEY")
	chatIDsEnv := os.Getenv("GROUP_CHAT_ID")
	tzName := os.Getenv("TZ")
	dbPath := os.Getenv("DB_PATH")

	if tgToken == "" || geminiKey == "" {
		log.Fatal("TELEGRAM_TOKEN and GEMINI_API_KEY are required")
	}
	if tzName == "" {
		tzName = "Asia/Singapore"
	}
	if dbPath == "" {
		dbPath = "cailorie.db"
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

	// Build Gemini client.
	gem, err := gemini.New(geminiKey)
	if err != nil {
		log.Fatalf("gemini client: %v", err)
	}
	defer gem.Close()

	// Build telebot (long-polling).
	pref := telebot.Settings{
		Token:   tgToken,
		Poller:  &telebot.LongPoller{Timeout: 10 * time.Second},
		Updates: 0,
		ParseMode: telebot.ModeHTML,
	}
	tgBot, err := telebot.NewBot(pref)
	if err != nil {
		log.Fatalf("telegram bot: %v", err)
	}

	// Register handlers.
	h := bot.NewHandler(tgBot, db, gem, sgt, allowedChats)
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
	c.Start()
	defer c.Stop()

	log.Printf("cailorie bot started, polling for updates (TZ=%s)", tzName)

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