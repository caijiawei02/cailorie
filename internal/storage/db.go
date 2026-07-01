package storage

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open opens the SQLite database at dbPath and runs migrations.
func Open(dbPath string) (*sql.DB, error) {
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS meals (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id      INTEGER NOT NULL,
	user_id      INTEGER NOT NULL,
	username     TEXT NOT NULL DEFAULT '',
	photo_file_id TEXT NOT NULL DEFAULT '',
	calories     INTEGER NOT NULL,
	meal_label   INTEGER NOT NULL,
	created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_meals_day ON meals(chat_id, user_id, created_at);

CREATE TABLE IF NOT EXISTS users (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id      INTEGER NOT NULL,
	user_id      INTEGER NOT NULL,
	username     TEXT NOT NULL DEFAULT '',
	first_name   TEXT NOT NULL DEFAULT '',
	last_seen_at TEXT NOT NULL,
	UNIQUE(chat_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_users_chat_seen ON users(chat_id, last_seen_at);
`)
	return err
}