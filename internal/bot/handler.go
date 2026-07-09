package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/caijiawei02/cailorie/internal/llm"
	"github.com/caijiawei02/cailorie/internal/model"
	"github.com/caijiawei02/cailorie/internal/storage"
	telebot "gopkg.in/telebot.v3"
)

// Handler bundles the bot with its dependencies for registering handlers.
type Handler struct {
	bot          *telebot.Bot
	db           *sql.DB
	llm          llm.Client
	sgt          *time.Location
	allowedChats map[int64]bool
}

// NewHandler constructs a Handler.
func NewHandler(b *telebot.Bot, db *sql.DB, lc llm.Client, sgt *time.Location, allowed map[int64]bool) *Handler {
	return &Handler{bot: b, db: db, llm: lc, sgt: sgt, allowedChats: allowed}
}

// Register attaches all handlers/middleware to the bot.
func (h *Handler) Register() {
	h.bot.Use(h.trackUserMiddleware())
	h.bot.Handle(telebot.OnPhoto, h.onPhoto)
	h.bot.Handle("/chatid", h.onChatID)
	h.bot.Handle("/today", h.onMeals)
	h.bot.Handle("/alltoday", h.onAllMeals)
	h.bot.Handle("/yesterday", h.onYesterday)
	h.bot.Handle("/allyesterday", h.onAllYesterday)
	h.bot.Handle("/highscore", h.onHighScore)
	h.bot.Handle("/allhighscore", h.onAllHighScore)
	h.bot.Handle("/week", h.onWeek)
	h.bot.Handle("/allweek", h.onAllWeek)
	h.bot.Handle("/deletelast", h.onDeleteLast)
	h.bot.Handle("/help", h.onHelp)
}

// trackUserMiddleware silently upserts every message sender in allowed groups
// into the users table. Non-allowed chats are skipped (no tracking, no reply).
func (h *Handler) trackUserMiddleware() telebot.MiddlewareFunc {
	return func(next telebot.HandlerFunc) telebot.HandlerFunc {
		return func(c telebot.Context) error {
			m := c.Message()
			if m == nil || m.Sender == nil || m.Chat == nil {
				return next(c)
			}
			if !h.chatAllowed(m.Chat.ID) {
				return next(c)
			}
			u := m.Sender
			if err := storage.UpsertUser(h.db, m.Chat.ID, u.ID, u.Username, u.FirstName, time.Now().UTC()); err != nil {
				log.Printf("upsert user %d in chat %d: %v", u.ID, m.Chat.ID, err)
			}
			return next(c)
		}
	}
}

func (h *Handler) chatAllowed(chatID int64) bool {
	if len(h.allowedChats) == 0 {
		return false
	}
	return h.allowedChats[chatID]
}

// onPhoto handles an incoming photo in an allowed group.
func (h *Handler) onPhoto(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil || m.Photo == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}
	sender := m.Sender
	if sender == nil {
		return nil
	}

	// 1. Download the photo bytes from Telegram.
	reader, err := h.bot.File(&m.Photo.File)
	if err != nil {
		log.Printf("download photo (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(fmt.Sprintf("%s — couldn't download your photo. Try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	defer reader.Close()
	imageBytes, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("read photo bytes (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(fmt.Sprintf("%s — couldn't process your photo. Try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}

	// 2. Estimate calories via Gemini. Photos are JPEG on Telegram.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	calories, err := h.llm.EstimateCalories(ctx, imageBytes, "image/jpeg", m.Caption)
	if err != nil {
		log.Printf("llm estimate (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(formatLLMError(err, sender.Username, sender.FirstName), telebot.ModeHTML)
	}

	// 3. Determine meal number (count existing meals today + 1).
	dayStart, dayEnd := sgtDayBounds(time.Now(), h.sgt)
	count, err := storage.DayMealCount(h.db, chatID, sender.ID, dayStart, dayEnd)
	if err != nil {
		log.Printf("day meal count (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(fmt.Sprintf("%s — internal error, please try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	mealLabel := count + 1

	// 4. Insert the meal row.
	meal := model.Meal{
		ChatID:      chatID,
		UserID:      sender.ID,
		Username:    sender.Username,
		PhotoFileID: m.Photo.FileID,
		Calories:    calories,
		MealLabel:   mealLabel,
		Caption:     m.Caption,
		CreatedAt:   time.Now().UTC(),
	}
	if _, err := storage.InsertMeal(h.db, meal); err != nil {
		log.Printf("insert meal (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(fmt.Sprintf("%s — internal error, please try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}

	// 5. Query the day's meals for this user (to render the full list).
	meals, err := storage.DayMeals(h.db, chatID, sender.ID, dayStart, dayEnd)
	if err != nil {
		log.Printf("day meals query (chat %d, user %d): %v", chatID, sender.ID, err)
		return nil
	}

	// 6. Reply with the full meal list + total (quote-reply, HTML).
	reply := formatMealsReply(meals, sender.Username, sender.FirstName, time.Now(), h.sgt)
	return c.Reply(reply, telebot.ModeHTML)
}

// onChatID replies with the current chat id. Useful during setup; works in any
// chat (DM or group) — it's a debugging helper, not a calorie command.
func (h *Handler) onChatID(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil {
		return nil
	}
	return c.Reply(fmt.Sprintf("chat_id: %d", m.Chat.ID))
}

// onMeals replies with the caller's meals logged so far today (SGT day),
// including any captions they attached. Works in any allowed group.
func (h *Handler) onMeals(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil || m.Sender == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}
	sender := m.Sender

	dayStart, dayEnd := sgtDayBounds(time.Now(), h.sgt)
	meals, err := storage.DayMeals(h.db, chatID, sender.ID, dayStart, dayEnd)
	if err != nil {
		log.Printf("meals day query (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(fmt.Sprintf("%s — internal error, please try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	if len(meals) == 0 {
		return c.Reply(fmt.Sprintf("%s — no meals logged yet today.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	reply := formatMealsReply(meals, sender.Username, sender.FirstName, time.Now(), h.sgt)
	return c.Reply(reply, telebot.ModeHTML)
}

// onAllMeals replies with every user's meals logged so far today (SGT day)
// in the current chat, with per-user subtotals and a grand total.
func (h *Handler) onAllMeals(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}

	dayStart, dayEnd := sgtDayBounds(time.Now(), h.sgt)
	meals, err := storage.DayMealsForChat(h.db, chatID, dayStart, dayEnd)
	if err != nil {
		log.Printf("allmeals day query (chat %d): %v", chatID, err)
		return c.Reply("Internal error, please try again.", telebot.ModeHTML)
	}
	if len(meals) == 0 {
		return c.Reply("No meals logged yet today.", telebot.ModeHTML)
	}
	reply := formatAllMealsReply(meals, time.Now(), h.sgt)
	return c.Reply(reply, telebot.ModeHTML)
}

// onYesterday replies with the caller's calorie summary from yesterday (SGT).
func (h *Handler) onYesterday(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil || m.Sender == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}
	sender := m.Sender

	yStart, yEnd := sgtYesterdayBounds(time.Now(), h.sgt)
	meals, err := storage.DayMeals(h.db, chatID, sender.ID, yStart, yEnd)
	if err != nil {
		log.Printf("yesterday meals query (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(fmt.Sprintf("%s — internal error, please try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	if len(meals) == 0 {
		yesterday := time.Now().In(h.sgt).AddDate(0, 0, -1)
		return c.Reply(fmt.Sprintf("%s — no meals logged on %s.", displayName(sender.Username, sender.FirstName), yesterday.Format("02 January 2006")), telebot.ModeHTML)
	}
	yesterday := time.Now().In(h.sgt).AddDate(0, 0, -1)
	reply := formatMealsReply(meals, sender.Username, sender.FirstName, yesterday, h.sgt)
	return c.Reply(reply, telebot.ModeHTML)
}

// onAllYesterday replies with every user's calorie summary from yesterday (SGT)
// in the current chat.
func (h *Handler) onAllYesterday(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}

	yStart, yEnd := sgtYesterdayBounds(time.Now(), h.sgt)
	rows, err := storage.DayTotalsForChat(h.db, chatID, yStart, yEnd)
	if err != nil {
		log.Printf("allyesterday summary query (chat %d): %v", chatID, err)
		return c.Reply("Internal error, please try again.", telebot.ModeHTML)
	}
	yesterday := time.Now().In(h.sgt).AddDate(0, 0, -1)
	if len(rows) == 0 {
		return c.Reply(fmt.Sprintf("No meals were logged on %s.", yesterday.Format("02 January 2006")), telebot.ModeHTML)
	}
	reply := formatSummary(rows, yesterday, h.sgt)
	return c.Reply(reply, telebot.ModeHTML)
}

// onWeek replies with the caller's weekly average calories/day so far this week
// (Monday–now, SGT). Only days with at least one meal are counted.
func (h *Handler) onWeek(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil || m.Sender == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}
	sender := m.Sender

	now := time.Now().In(h.sgt)
	weekStart := sgtWeekStart(now, h.sgt)
	row, err := storage.WeeklyAvgForUser(h.db, chatID, sender.ID, weekStart, now.UTC(), h.sgt)
	if err != nil {
		log.Printf("week query (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(fmt.Sprintf("%s — internal error, please try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	if row == nil {
		return c.Reply(fmt.Sprintf("%s — no meals logged yet this week.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	reply := formatWeeklyUserReply(row, weekStart, h.sgt)
	return c.Reply(reply, telebot.ModeHTML)
}

// onAllWeek replies with every user's weekly average calories/day so far this
// week (Monday–now, SGT) in the current chat.
func (h *Handler) onAllWeek(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}

	now := time.Now().In(h.sgt)
	weekStart := sgtWeekStart(now, h.sgt)
	rows, err := storage.WeeklyAvgForChat(h.db, chatID, weekStart, now.UTC(), h.sgt)
	if err != nil {
		log.Printf("allweek query (chat %d): %v", chatID, err)
		return c.Reply("Internal error, please try again.", telebot.ModeHTML)
	}
	reply := formatWeeklySummary(rows, weekStart, h.sgt)
	return c.Reply(reply, telebot.ModeHTML)
}

// onHelp replies with a list of all available commands.
func (h *Handler) onHelp(c telebot.Context) error {
	return c.Reply(formatHelpReply(), telebot.ModeHTML)
}

// onHighScore replies with the caller's highest-calorie day of all time.
func (h *Handler) onHighScore(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil || m.Sender == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}
	sender := m.Sender

	row, err := storage.UserHighScore(h.db, chatID, sender.ID, h.sgt)
	if err != nil {
		log.Printf("highscore query (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(fmt.Sprintf("%s — internal error, please try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	if row == nil {
		return c.Reply(fmt.Sprintf("%s — no meals logged yet.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	reply := formatHighScoreReply(row, sender.Username, sender.FirstName)
	return c.Reply(reply, telebot.ModeHTML)
}

// onAllHighScore replies with every user's highest-calorie day of all time
// in the current chat.
func (h *Handler) onAllHighScore(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}

	rows, err := storage.ChatHighScores(h.db, chatID, h.sgt)
	if err != nil {
		log.Printf("allhighscore query (chat %d): %v", chatID, err)
		return c.Reply("Internal error, please try again.", telebot.ModeHTML)
	}
	if len(rows) == 0 {
		return c.Reply("No meals have been logged yet.", telebot.ModeHTML)
	}
	reply := formatAllHighScoresReply(rows)
	return c.Reply(reply, telebot.ModeHTML)
}

// onDeleteLast deletes the caller's last meal today and confirms deletion.
func (h *Handler) onDeleteLast(c telebot.Context) error {
	m := c.Message()
	if m == nil || m.Chat == nil || m.Sender == nil {
		return nil
	}
	chatID := m.Chat.ID
	if !h.chatAllowed(chatID) {
		return nil
	}
	sender := m.Sender

	dayStart, dayEnd := sgtDayBounds(time.Now(), h.sgt)
	meal, err := storage.LastMealToday(h.db, chatID, sender.ID, dayStart, dayEnd)
	if err != nil {
		log.Printf("deletelast query (chat %d, user %d): %v", chatID, sender.ID, err)
		return c.Reply(fmt.Sprintf("%s — internal error, please try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}
	if meal == nil {
		return c.Reply(fmt.Sprintf("%s — no meals to delete today.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}

	if err := storage.DeleteMeal(h.db, meal.ID); err != nil {
		log.Printf("deletelast delete (chat %d, user %d, meal %d): %v", chatID, sender.ID, meal.ID, err)
		return c.Reply(fmt.Sprintf("%s — internal error, please try again.", displayName(sender.Username, sender.FirstName)), telebot.ModeHTML)
	}

	return c.Reply(fmt.Sprintf("%s — deleted Meal %d (%d calories).", displayName(sender.Username, sender.FirstName), meal.MealLabel, meal.Calories), telebot.ModeHTML)
}

// formatLLMError maps an LLM error to a user-facing HTML reply that
// explains what went wrong in plain language.
func formatLLMError(err error, username, firstName string) string {
	who := displayName(username, firstName)
	switch {
	case errors.Is(err, llm.ErrNotFood):
		return fmt.Sprintf("%s — couldn't identify the meal. Please send a clearer photo.", who)
	case errors.Is(err, llm.ErrQuotaExceeded):
		return fmt.Sprintf("%s — the LLM quota for today has been used up. Please try again later.", who)
	case errors.Is(err, llm.ErrInvalidKey):
		return fmt.Sprintf("%s — LLM API key is invalid or unauthorized. The bot admin needs to fix the key.", who)
	case errors.Is(err, llm.ErrModelNotFound):
		return fmt.Sprintf("%s — the LLM model is unavailable. The bot admin needs to update the model name.", who)
	case errors.Is(err, llm.ErrSafetyBlocked):
		return fmt.Sprintf("%s — the image was blocked for safety reasons. Please send a different photo.", who)
	case errors.Is(err, llm.ErrProviderDown):
		return fmt.Sprintf("%s — the LLM provider is unreachable right now. Please try again in a moment.", who)
	default:
		return fmt.Sprintf("%s — couldn't process the image. Please try again in a moment.", who)
	}
}
