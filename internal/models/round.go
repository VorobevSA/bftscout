// Package models defines the database models for consensus monitoring.
package models

import "time"

// RoundProposer represents a proposer for a specific round at a given height.
type RoundProposer struct {
	ID              uint   `gorm:"primaryKey"`
	Height          int64  `gorm:"index:ux_height_round,unique;index"`
	Round           int32  `gorm:"index:ux_height_round,unique;index"`
	ProposerAddress string `gorm:"size:128;index"`
	ProposerMoniker string `gorm:"size:128;index"`
	Succeeded       bool   `gorm:"index"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
