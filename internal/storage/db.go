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
	caption      TEXT NOT NULL DEFAULT '',
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
	if err != nil {
		return err
	}

	// Additive: add the optional caption column to meals for existing DBs.
	// Fresh DBs already get it via CREATE TABLE above. Guarded so re-running
	// migrate() is idempotent.
	if err := addColumnIfMissing(db, "meals", "caption", "TEXT"); err != nil {
		return fmt.Errorf("alter meals add caption: %w", err)
	}
	return nil
}

// addColumnIfMissing adds column `col` (of SQLite type `colType`) to `table`
// only if it is not already present. This makes ALTER TABLE statements safe to
// run repeatedly against pre-existing databases.
func addColumnIfMissing(db *sql.DB, table, col, colType string) error {
	var found bool
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, colType))
	return err
}