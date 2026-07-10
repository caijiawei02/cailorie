package bot

import (
	"fmt"
	"strings"
	"time"

	"github.com/caijiawei02/cailorie/internal/model"
	"github.com/caijiawei02/cailorie/internal/storage"
)

// sgtDayBounds returns the full SGT-day UTC window [00:00 SGT, 00:00 next SGT).
func sgtDayBounds(t time.Time, sgt *time.Location) (time.Time, time.Time) {
	local := t.In(sgt)
	dayStartLocal := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, sgt)
	return dayStartLocal.UTC(), dayStartLocal.Add(24 * time.Hour).UTC()
}

// sgtYesterdayBounds returns the SGT-day UTC window for yesterday relative to t.
func sgtYesterdayBounds(t time.Time, sgt *time.Location) (time.Time, time.Time) {
	local := t.In(sgt)
	yesterday := local.AddDate(0, 0, -1)
	dayStartLocal := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, sgt)
	return dayStartLocal.UTC(), dayStartLocal.Add(24 * time.Hour).UTC()
}

// sgtWeekStart returns the Monday 00:00 SGT of the week containing t, as UTC.
func sgtWeekStart(t time.Time, sgt *time.Location) time.Time {
	local := t.In(sgt)
	wd := int(local.Weekday())
	if wd == 0 {
		wd = 7
	}
	daysBack := wd - 1
	monday := local.AddDate(0, 0, -daysBack)
	return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, sgt).UTC()
}

// formatHelpReply returns an HTML message listing all available commands.
func formatHelpReply() string {
	return `<b>Available Commands</b>

/today — Show everyone's meals logged today
/yesterday — Show everyone's calorie summary from yesterday
/week — Show everyone's weekly average calories/day
/highscore — Show everyone's highest calorie day of all time
/deletelast — Delete your last meal logged today

<b>How to Log Meals</b>
Send a photo of your food with an optional caption. The bot will estimate the calories automatically.`
}

// formatMealsReply builds the per-user daily meal list reply (HTML).
// Used for the photo reply (single-user context after logging a meal).
func formatMealsReply(meals []model.Meal, username, firstName string, t time.Time, sgt *time.Location) string {
	var b strings.Builder
	b.WriteString("<b>")
	b.WriteString(displayName(username, firstName))
	b.WriteString(" — ")
	b.WriteString(t.In(sgt).Format("02 January 2006"))
	b.WriteString("</b>\n\n")

	total := 0
	for _, m := range meals {
		fmt.Fprintf(&b, "Meal %d: %d calories\n", m.MealLabel, m.Calories)
		total += m.Calories
	}
	fmt.Fprintf(&b, "\nTotal: %d calories", total)
	return b.String()
}

// formatAllMealsReply builds the all-users daily meal list reply (HTML).
// Groups meals by user, showing each user's meals and per-user total.
func formatAllMealsReply(meals []model.Meal, t time.Time, sgt *time.Location) string {
	var b strings.Builder
	b.WriteString("<b>All meals — ")
	b.WriteString(t.In(sgt).Format("02 January 2006"))
	b.WriteString("</b>\n\n")

	if len(meals) == 0 {
		b.WriteString("No meals logged yet today.")
		return b.String()
	}

	currentUser := ""
	userTotal := 0
	userCount := 0

	flushUser := func() {
		if userCount > 0 {
			fmt.Fprintf(&b, "Total: %d calories\n\n", userTotal)
		}
	}

	for _, m := range meals {
		name := displayName(m.Username, "")
		if name == "" || name == "user" {
			name = fmt.Sprintf("user %d", m.UserID)
		}
		if name != currentUser {
			flushUser()
			currentUser = name
			userTotal = 0
			userCount = 0
			b.WriteString(name)
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "Meal %d: %d calories\n", m.MealLabel, m.Calories)
		userTotal += m.Calories
		userCount++
	}
	flushUser()

	return strings.TrimRight(b.String(), "\n")
}

// formatSummary builds the daily summary for one chat.
// Includes only users active today (last_seen_at >= dayStart). Zero-meal users
// appear with "0 calories (0 meals)". Ordered by total DESC.
func formatSummary(rows []storage.DayTotalsRow, t time.Time, sgt *time.Location) string {
	var b strings.Builder
	b.WriteString("<b>Daily Calorie Summary — ")
	b.WriteString(t.In(sgt).Format("02 January 2006"))
	b.WriteString("</b>\n\n")

	if len(rows) == 0 {
		b.WriteString("No meals were logged today.")
		return b.String()
	}

	for _, r := range rows {
		fmt.Fprintf(&b, "%s — %d calories (%d meals)\n",
			displayName(r.Username, r.FirstName), r.Total, r.Meals)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatAllHighScoresReply formats all users' highest-calorie days.
func formatAllHighScoresReply(rows []storage.HighScoreRow) string {
	var b strings.Builder
	b.WriteString("<b>High Scores — All Time</b>\n\n")

	if len(rows) == 0 {
		b.WriteString("No meals have been logged yet.")
		return b.String()
	}

	for _, r := range rows {
		fmt.Fprintf(&b, "%s — %d calories (%d meals on %s)\n",
			displayName(r.Username, r.FirstName), r.Total, r.Meals, r.Day)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatLeaderboardScoresReply formats the leaderboard scores.
func formatLeaderboardScoresReply(rows []storage.LeaderboardScoreRow) string {
	var b strings.Builder
	b.WriteString("<b>Leaderboard — All Time</b>\n\n")
	b.WriteString("<b>Scores</b>\n")

	for _, r := range rows {
		fmt.Fprintf(&b, "%s — %d point", displayName(r.Username, r.FirstName), r.Score)
		if r.Score != 1 {
			b.WriteByte('s')
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatWeeklySummary builds the weekly average summary for one chat.
// Header uses the Monday date of the week.
func formatWeeklySummary(rows []storage.WeeklyAvgRow, weekStart time.Time, sgt *time.Location) string {
	var b strings.Builder
	b.WriteString("<b>Weekly Average — ")
	b.WriteString(weekStart.In(sgt).Format("02 January 2006"))
	b.WriteString("</b>\n\n")

	if len(rows) == 0 {
		b.WriteString("No meals were logged this week.")
		return b.String()
	}

	for _, r := range rows {
		fmt.Fprintf(&b, "%s — %d calories/day (%d days)\n",
			displayName(r.Username, r.FirstName), r.AvgCal, r.Days)
	}
	return strings.TrimRight(b.String(), "\n")
}

// displayName returns "@username" when a username is present, else the
// first name, HTML-escaped.
func displayName(username, firstName string) string {
	name := firstName
	if username != "" {
		name = "@" + username
	}
	if name == "" {
		name = "user"
	}
	return htmlEscape(name)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}