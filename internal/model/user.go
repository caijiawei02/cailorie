package model

import "time"

// User represents a Telegram user seen in an allowed group.
type User struct {
	ChatID     int64
	UserID     int64
	Username   string
	FirstName  string
	LastSeenAt time.Time // UTC
}