package models

import "time"

type Block struct {
    ID               uint      `gorm:"primaryKey"`
    Height           int64     `gorm:"uniqueIndex;not null;index"`
    Hash             string    `gorm:"size:128;index"`
    Time             time.Time `gorm:"index"`
    ProposerAddress  string    `gorm:"size:128;index"`
    ProposerMoniker  string    `gorm:"size:128;index"`
    CommitSucceeded  bool      `gorm:"index"`
    CreatedAt        time.Time
    UpdatedAt        time.Time
}


