package storage

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/caijiawei02/cailorie/internal/model"
)

// InsertMeal inserts a meal row and returns the inserted record (with ID).
// mealLabel is the per-user-per-day sequence number.
func InsertMeal(db *sql.DB, m model.Meal) (model.Meal, error) {
	res, err := db.Exec(
		`INSERT INTO meals (chat_id, user_id, username, photo_file_id, calories, meal_label, caption, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ChatID, m.UserID, m.Username, m.PhotoFileID, m.Calories, m.MealLabel, m.Caption, m.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return m, fmt.Errorf("insert meal: %w", err)
	}
	id, _ := res.LastInsertId()
	m.ID = id
	return m, nil
}

// DeleteMeal hard-deletes a meal row by its ID.
func DeleteMeal(db *sql.DB, mealID int64) error {
	_, err := db.Exec(`DELETE FROM meals WHERE id = ?`, mealID)
	return err
}

// LastMealToday returns the user's most recent meal within [dayStart, dayEnd)
// in the given chat, or nil if they have no meals today.
func LastMealToday(db *sql.DB, chatID, userID int64, dayStart, dayEnd time.Time) (*model.Meal, error) {
	row := db.QueryRow(
		`SELECT id, chat_id, user_id, username, photo_file_id, calories, meal_label, caption, created_at
		 FROM meals
		 WHERE chat_id=? AND user_id=? AND created_at >= ? AND created_at < ?
		 ORDER BY created_at DESC
		 LIMIT 1`,
		chatID, userID, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339),
	)
	var m model.Meal
	var createdAtStr string
	var caption sql.NullString
	if err := row.Scan(&m.ID, &m.ChatID, &m.UserID, &m.Username, &m.PhotoFileID, &m.Calories, &m.MealLabel, &caption, &createdAtStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	m.Caption = caption.String
	m.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	return &m, nil
}

// DayMealCount returns the number of meals logged by a user in a chat within
// the half-open window [dayStart, dayEnd) (UTC times).
func DayMealCount(db *sql.DB, chatID, userID int64, dayStart, dayEnd time.Time) (int, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM meals
		 WHERE chat_id=? AND user_id=? AND created_at >= ? AND created_at < ?`,
		chatID, userID, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339),
	).Scan(&n)
	return n, err
}

// DayMeals returns all meals for a user in a chat within [dayStart, dayEnd),
// ordered by creation time ascending.
func DayMeals(db *sql.DB, chatID, userID int64, dayStart, dayEnd time.Time) ([]model.Meal, error) {
	rows, err := db.Query(
		`SELECT id, chat_id, user_id, username, photo_file_id, calories, meal_label, caption, created_at
		 FROM meals
		 WHERE chat_id=? AND user_id=? AND created_at >= ? AND created_at < ?
		 ORDER BY created_at ASC`,
		chatID, userID, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Meal
	for rows.Next() {
		var m model.Meal
		var createdAtStr string
		var caption sql.NullString
		if err := rows.Scan(&m.ID, &m.ChatID, &m.UserID, &m.Username, &m.PhotoFileID, &m.Calories, &m.MealLabel, &caption, &createdAtStr); err != nil {
			return nil, err
		}
		m.Caption = caption.String
		m.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DayMealsForChat returns all meals for every user in a chat within
// [dayStart, dayEnd), ordered by user then creation time ascending.
func DayMealsForChat(db *sql.DB, chatID int64, dayStart, dayEnd time.Time) ([]model.Meal, error) {
	rows, err := db.Query(
		`SELECT id, chat_id, user_id, username, photo_file_id, calories, meal_label, caption, created_at
		 FROM meals
		 WHERE chat_id=? AND created_at >= ? AND created_at < ?
		 ORDER BY username ASC, created_at ASC`,
		chatID, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Meal
	for rows.Next() {
		var m model.Meal
		var createdAtStr string
		var caption sql.NullString
		if err := rows.Scan(&m.ID, &m.ChatID, &m.UserID, &m.Username, &m.PhotoFileID, &m.Calories, &m.MealLabel, &caption, &createdAtStr); err != nil {
			return nil, err
		}
		m.Caption = caption.String
		m.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DayTotalsRow is one user's daily aggregate for the summary message.
type DayTotalsRow struct {
	UserID    int64
	Username  string
	FirstName string
	Total     int
	Meals     int
}

// HighScoreRow is one user's highest-calorie day across all time.
type HighScoreRow struct {
	UserID    int64
	Username  string
	FirstName string
	Day       string // "02 January 2006" formatted
	Total     int
	Meals     int
}

// WeeklyAvgRow is one user's weekly average calories per day.
type WeeklyAvgRow struct {
	UserID    int64
	Username  string
	FirstName string
	AvgCal    int
	Days      int
	Total     int
}

// DayTotalsForChat returns per-user totals for the given chat within the
// half-open window [dayStart, dayEnd) (UTC), including only users active in
// the chat today (last_seen_at >= dayStart). Users with zero meals are
// included (LEFT JOIN). Ordered by total DESC, then username/first_name ASC.
func DayTotalsForChat(db *sql.DB, chatID int64, dayStart, dayEnd time.Time) ([]DayTotalsRow, error) {
	startStr := dayStart.UTC().Format(time.RFC3339)
	endStr := dayEnd.UTC().Format(time.RFC3339)
	rows, err := db.Query(
		`SELECT u.user_id, u.username, u.first_name,
		        COALESCE(SUM(m.calories), 0) AS total,
		        COUNT(m.id) AS meals
		 FROM users u
		 LEFT JOIN meals m
		   ON m.user_id = u.user_id AND m.chat_id = u.chat_id
		  AND m.created_at >= ? AND m.created_at < ?
		 WHERE u.chat_id = ? AND u.last_seen_at >= ?
		 GROUP BY u.user_id, u.username, u.first_name
		 ORDER BY total DESC, u.username ASC, u.first_name ASC`,
		startStr, endStr, chatID, startStr,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayTotalsRow
	for rows.Next() {
		var r DayTotalsRow
		if err := rows.Scan(&r.UserID, &r.Username, &r.FirstName, &r.Total, &r.Meals); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// tzOffset returns a SQLite time-modifier string like "+08:00" or "-05:00"
// representing the offset of the given location from UTC.
func tzOffset(loc *time.Location) string {
	_, offset := time.Date(2024, 1, 1, 12, 0, 0, 0, loc).Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	h := offset / 3600
	m := (offset % 3600) / 60
	return fmt.Sprintf("%s%02d:%02d", sign, h, m)
}

// UserHighScore returns the single highest-calorie day for one user in a chat.
// Returns false (nil row) if the user has no meals.
func UserHighScore(db *sql.DB, chatID, userID int64, loc *time.Location) (*HighScoreRow, error) {
	off := tzOffset(loc)
	row := db.QueryRow(
		`SELECT SUM(calories) AS total, COUNT(id) AS meals,
		        DATE(created_at, ?) AS day
		 FROM meals
		 WHERE chat_id = ? AND user_id = ?
		 GROUP BY day
		 ORDER BY total DESC, created_at DESC
		 LIMIT 1`,
		off, chatID, userID,
	)
	var r HighScoreRow
	var dayStr string
	if err := row.Scan(&r.Total, &r.Meals, &dayStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	r.UserID = userID

	uRow := db.QueryRow(
		`SELECT username, first_name FROM users WHERE chat_id = ? AND user_id = ?`,
		chatID, userID,
	)
	_ = uRow.Scan(&r.Username, &r.FirstName)

	parsed, err := time.Parse("2006-01-02", dayStr)
	if err != nil {
		return nil, err
	}
	r.Day = parsed.In(loc).Format("02 January 2006")
	return &r, nil
}

// ChatHighScores returns each user's highest-calorie day in a chat,
// ordered by total DESC.
func ChatHighScores(db *sql.DB, chatID int64, loc *time.Location) ([]HighScoreRow, error) {
	off := tzOffset(loc)
	rows, err := db.Query(
		`SELECT user_id, username, first_name, total, meals, day FROM (
		     SELECT m.user_id, u.username, u.first_name,
		            SUM(m.calories) AS total, COUNT(m.id) AS meals,
		            DATE(m.created_at, ?) AS day,
		            ROW_NUMBER() OVER (PARTITION BY m.user_id ORDER BY SUM(m.calories) DESC, MAX(m.created_at) DESC) AS rn
		     FROM meals m
		     JOIN users u ON u.user_id = m.user_id AND u.chat_id = m.chat_id
		     WHERE m.chat_id = ?
		     GROUP BY m.user_id, u.username, u.first_name, day
		 ) WHERE rn = 1
		 ORDER BY total DESC, username ASC`,
		off, chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HighScoreRow
	for rows.Next() {
		var r HighScoreRow
		var dayStr string
		if err := rows.Scan(&r.UserID, &r.Username, &r.FirstName, &r.Total, &r.Meals, &dayStr); err != nil {
			return nil, err
		}
		parsed, err := time.Parse("2006-01-02", dayStr)
		if err != nil {
			return nil, err
		}
		r.Day = parsed.In(loc).Format("02 January 2006")
		out = append(out, r)
	}
	return out, rows.Err()
}

// WeeklyAvgForUser returns a single user's weekly average calories per day
// for the given chat within [weekStart, weekEnd). Returns nil if the user has
// no meals in the window.
func WeeklyAvgForUser(db *sql.DB, chatID, userID int64, weekStart, weekEnd time.Time, loc *time.Location) (*WeeklyAvgRow, error) {
	off := tzOffset(loc)
	startStr := weekStart.UTC().Format(time.RFC3339)
	endStr := weekEnd.UTC().Format(time.RFC3339)
	var r WeeklyAvgRow
	err := db.QueryRow(
		`SELECT m_user_id, u_username, u_first_name,
		        day_total, day_count,
		        CAST(day_total / day_count AS INTEGER) AS avg_cal
		 FROM (
		     SELECT m.user_id AS m_user_id,
		            u.username AS u_username,
		            u.first_name AS u_first_name,
		            SUM(m.calories) AS day_total,
		            COUNT(DISTINCT DATE(m.created_at, ?)) AS day_count
		     FROM meals m
		     JOIN users u ON u.user_id = m.user_id AND u.chat_id = m.chat_id
		     WHERE m.chat_id = ? AND m.user_id = ? AND m.created_at >= ? AND m.created_at < ?
		       AND u.last_seen_at >= ?
		     GROUP BY m.user_id, u.username, u.first_name
		 )`,
		off, chatID, userID, startStr, endStr, startStr,
	).Scan(&r.UserID, &r.Username, &r.FirstName, &r.Total, &r.Days, &r.AvgCal)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// WeeklyAvgForChat returns per-user weekly average calories per day for the
// given chat within [weekStart, weekEnd). Only users who logged at least one
// meal during the week are included. Ordered by avgCal DESC, then username ASC.
func WeeklyAvgForChat(db *sql.DB, chatID int64, weekStart, weekEnd time.Time, loc *time.Location) ([]WeeklyAvgRow, error) {
	off := tzOffset(loc)
	startStr := weekStart.UTC().Format(time.RFC3339)
	endStr := weekEnd.UTC().Format(time.RFC3339)
	rows, err := db.Query(
		`SELECT m_user_id, u_username, u_first_name,
		        day_total, day_count,
		        CAST(day_total / day_count AS INTEGER) AS avg_cal
		 FROM (
		     SELECT m.user_id AS m_user_id,
		            u.username AS u_username,
		            u.first_name AS u_first_name,
		            SUM(m.calories) AS day_total,
		            COUNT(DISTINCT DATE(m.created_at, ?)) AS day_count
		     FROM meals m
		     JOIN users u ON u.user_id = m.user_id AND u.chat_id = m.chat_id
		     WHERE m.chat_id = ? AND m.created_at >= ? AND m.created_at < ?
		       AND u.last_seen_at >= ?
		     GROUP BY m.user_id, u.username, u.first_name
		 )
		 ORDER BY avg_cal DESC, u_username ASC, u_first_name ASC`,
		off, chatID, startStr, endStr, startStr,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WeeklyAvgRow
	for rows.Next() {
		var r WeeklyAvgRow
		if err := rows.Scan(&r.UserID, &r.Username, &r.FirstName, &r.Total, &r.Days, &r.AvgCal); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}