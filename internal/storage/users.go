package storage

import (
	"database/sql"
	"time"
)

// UpsertUser records that a user was seen in a chat at the given time (UTC).
// On conflict, username/first_name/last_seen_at are updated.
func UpsertUser(db *sql.DB, chatID, userID int64, username, firstName string, seenAt time.Time) error {
	_, err := db.Exec(
		`INSERT INTO users (chat_id, user_id, username, first_name, last_seen_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(chat_id, user_id) DO UPDATE SET
		   username = excluded.username,
		   first_name = excluded.first_name,
		   last_seen_at = excluded.last_seen_at`,
		chatID, userID, username, firstName, seenAt.UTC().Format(time.RFC3339),
	)
	return err
}