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
		if err := rows.Scan(&m.ID, &m.ChatID, &m.UserID, &m.Username, &m.PhotoFileID, &m.Calories, &m.MealLabel, &m.Caption, &createdAtStr); err != nil {
			return nil, err
		}
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