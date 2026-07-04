package bot

import (
	"database/sql"
	"log"
	"time"

	"github.com/caijiawei02/cailorie/internal/storage"
	telebot "gopkg.in/telebot.v3"
)

// SendDailySummary computes and sends the daily calorie summary to one chat.
// The snapshot window is [00:00 SGT today, fireTime) so any meal logged
// during the 23:58–00:00 tail still counts toward today.
func SendDailySummary(b *telebot.Bot, db *sql.DB, sgt *time.Location, chatID int64, fireTime time.Time) {
	dayStart, _ := sgtDayBounds(fireTime, sgt) // full-day start
	// window end is fireTime (snapshot stops now)
	rows, err := storage.DayTotalsForChat(db, chatID, dayStart, fireTime.UTC())
	if err != nil {
		log.Printf("daily summary query (chat %d): %v", chatID, err)
		return
	}
	msg := formatSummary(rows, fireTime, sgt)
	if _, err := b.Send(telebot.ChatID(chatID), msg, telebot.ModeHTML); err != nil {
		log.Printf("daily summary send (chat %d): %v", chatID, err)
	}
}

// SendWeeklySummary computes and sends the weekly average calories/day summary
// to one chat. The window is [Monday 00:00 SGT, fireTime). Only sent on Sundays.
func SendWeeklySummary(b *telebot.Bot, db *sql.DB, sgt *time.Location, chatID int64, fireTime time.Time) {
	weekStart := sgtWeekStart(fireTime, sgt)
	rows, err := storage.WeeklyAvgForChat(db, chatID, weekStart, fireTime.UTC(), sgt)
	if err != nil {
		log.Printf("weekly summary query (chat %d): %v", chatID, err)
		return
	}
	msg := formatWeeklySummary(rows, weekStart, sgt)
	if _, err := b.Send(telebot.ChatID(chatID), msg, telebot.ModeHTML); err != nil {
		log.Printf("weekly summary send (chat %d): %v", chatID, err)
	}
}