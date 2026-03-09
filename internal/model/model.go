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

type UserModemPermission struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UserID      uint      `gorm:"index:idx_user_iccid,unique;not null" json:"user_id"`
	ICCID       string    `gorm:"index:idx_user_iccid,unique;not null" json:"iccid"`
	CanMakeCall bool      `gorm:"default:false" json:"can_make_call"`
	CanViewSMS  bool      `gorm:"default:false" json:"can_view_sms"`
	CanSendSMS  bool      `gorm:"default:false" json:"can_send_sms"`
	CanSendAT   bool      `gorm:"default:false" json:"can_send_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type APIKey struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	UserID      uint       `gorm:"index;not null" json:"user_id"`
	Name        string     `gorm:"size:64" json:"name"`
	KeyPrefix   string     `gorm:"size:24" json:"key_prefix"`
	KeyHash     string     `gorm:"size:128;uniqueIndex;not null" json:"-"`
	CanMakeCall bool       `gorm:"default:true" json:"can_make_call"`
	CanViewSMS  bool       `gorm:"default:true" json:"can_view_sms"`
	CanSendSMS  bool       `gorm:"default:true" json:"can_send_sms"`
	CanSendAT   bool       `gorm:"default:true" json:"can_send_at"`
	IsActive    bool       `gorm:"default:true;index" json:"is_active"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type Modem struct {
	ICCID             string    `gorm:"primaryKey;column:iccid" json:"iccid"`
	Name              string    `gorm:"column:name" json:"name"` // User defined alias
	IMEI              string    `gorm:"column:imei" json:"imei"`
	SIPEnabled        bool      `gorm:"column:sip_enabled" json:"sip_enabled"`
	SIPUsername       string    `gorm:"column:sip_username" json:"sip_username,omitempty"`
	SIPPassword       string    `gorm:"column:sip_password" json:"-"`
	SIPProxy          string    `gorm:"column:sip_proxy" json:"sip_proxy,omitempty"`
	SIPPort           int       `gorm:"column:sip_port" json:"sip_port"`
	SIPDomain         string    `gorm:"column:sip_domain" json:"sip_domain,omitempty"`
	SIPTransport      string    `gorm:"column:sip_transport" json:"sip_transport,omitempty"`
	SIPRegister       bool      `gorm:"column:sip_register" json:"sip_register"`
	SIPTLSSkipVerify  bool      `gorm:"column:sip_tls_skip_verify" json:"sip_tls_skip_verify"`
	SIPListenPort     int       `gorm:"column:sip_listen_port" json:"sip_listen_port"`
	SIPAcceptIncoming bool      `gorm:"column:sip_accept_incoming" json:"sip_accept_incoming"`
	SIPInviteTarget   string    `gorm:"column:sip_invite_target" json:"sip_invite_target,omitempty"`
	SIPHasPassword    bool      `gorm:"-" json:"sip_has_password,omitempty"`
	Operator          string    `gorm:"-" json:"operator"`        // runtime field (not persisted as source of truth)
	SignalStrength    int       `gorm:"-" json:"signal_strength"` // runtime field (CSQ)
	PortName          string    `gorm:"-" json:"port_name"`       // Current COM port, can change
	Status            string    `gorm:"-" json:"status"`          // runtime field: online/offline
	Registration      string    `gorm:"-" json:"registration"`    // runtime field
	LastSeen          time.Time `gorm:"-" json:"last_seen"`       // runtime field
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
