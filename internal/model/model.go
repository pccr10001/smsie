package model

import (
	"time"

	"gorm.io/gorm"
)

type User struct {
	ID            uint           `gorm:"primaryKey" json:"id"`
	Username      string         `gorm:"uniqueIndex;not null" json:"username"`
	PasswordHash  string         `gorm:"not null" json:"-"`
	Role          string         `gorm:"default:'user'" json:"role"` // admin, user
	AllowedModems string         `json:"allowed_modems"`             // Comma separated ICCIDs, or "*"
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

type Modem struct {
	ICCID          string    `gorm:"primaryKey;column:iccid" json:"iccid"`
	Name           string    `json:"name"` // User defined alias
	IMEI           string    `json:"imei"`
	Operator       string    `json:"operator"`
	SignalStrength int       `json:"signal_strength"` // CSQ
	PortName       string    `json:"port_name"`       // Current COM port, can change
	Status         string    `json:"status"`          // online, offline
	Registration   string    `json:"registration"`    // Home, Roaming, Denied, etc.
	LastSeen       time.Time `json:"last_seen"`
}

type SMS struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ICCID     string    `gorm:"index;not null;column:iccid" json:"iccid"`
	Phone     string    `gorm:"index;not null" json:"phone"`
	Content   string    `json:"content"`
	Timestamp time.Time `gorm:"index" json:"timestamp"`
	Type      string    `gorm:"index" json:"type"` // sent, received
	IsRead    bool      `gorm:"default:false" json:"is_read"`
	RawPDU    string    `json:"raw_pdu,omitempty"` // For debugging
	CreatedAt time.Time `json:"created_at"`
}

type Webhook struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ICCID     string    `gorm:"index;not null;column:iccid" json:"iccid"`
	URL       string    `gorm:"not null" json:"url"`
	Platform  string    `json:"platform"`   // telegram, slack, generic
	ChannelID string    `json:"channel_id"` // For Telegram
	Template  string    `json:"template"`   // "Msg from {{.Phone}}: {{.Content}}"
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}
