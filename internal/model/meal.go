package model

import "time"

// Meal represents a single calorie entry logged by a user in a group.
type Meal struct {
	ID          int64
	ChatID      int64
	UserID      int64
	Username    string
	PhotoFileID string
	Calories    int
	MealLabel   int       // sequence number per user per SGT day (1-based)
	Caption     string    // optional user-typed description of the meal (may be empty)
	CreatedAt   time.Time // UTC
}