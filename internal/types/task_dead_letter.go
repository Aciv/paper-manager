package types

import "time"

// TaskDeadLetter stores an asynq task after its retry budget is exhausted.
// Identifiers are intentionally nullable because not every task is document-scoped.
type TaskDeadLetter struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Scope     string    `gorm:"type:varchar(32);index;not null;default:'unknown'" json:"scope"`
	ScopeID   string    `gorm:"type:varchar(128);index" json:"scope_id,omitempty"`
	RelatedID string    `gorm:"type:varchar(128);index" json:"related_id,omitempty"`
	TaskType  string    `gorm:"type:varchar(128);index;not null" json:"task_type"`
	Payload   JSON      `gorm:"type:jsonb;not null" json:"payload"`
	LastError string    `gorm:"type:text;not null" json:"last_error"`
	FailCount int       `gorm:"not null;default:1" json:"fail_count"`
	FailedAt  time.Time `gorm:"index;not null" json:"failed_at"`
}

func (TaskDeadLetter) TableName() string { return "task_dead_letters" }
