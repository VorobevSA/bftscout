package models

import "time"

// RoundVote stores validator votes (prevotes and precommits) for each consensus round
// All prevotes in a round are recorded (not unique constraint)
type RoundVote struct {
	ID               uint      `gorm:"primaryKey"`
	Height           int64     `gorm:"index"`
	Round            int32     `gorm:"index"`
	ValidatorAddress string    `gorm:"size:128;index"`
	ValidatorMoniker string    `gorm:"size:128"`
	ProposerAddress  string    `gorm:"size:128;index"`
	VoteType         string    `gorm:"size:16;index"` // "prevote" or "precommit"
	BlockHash        string    `gorm:"size:128"` // can be empty for failed rounds
	Timestamp        time.Time `gorm:"index"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

