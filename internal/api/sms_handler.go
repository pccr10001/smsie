package api

import (
	"encoding/hex"
	"fmt"
	"github.com/pccr10001/smsie/pkg/logger"
	"github.com/warthog618/sms/encoding/tpdu"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/warthog618/sms"

	"github.com/gin-gonic/gin"
	"github.com/pccr10001/smsie/internal/model"
	"gorm.io/gorm"
)

type SMSHandler struct {
	db *gorm.DB
}

func NewSMSHandler(db *gorm.DB) *SMSHandler {
	return &SMSHandler{db: db}
}

func (h *SMSHandler) ListSMS(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	isAdmin := (user.Role == "admin")
	var allowed []string

	if !isAdmin {
		if user.AllowedModems != "" && user.AllowedModems != "*" {
			allowed = strings.Split(user.AllowedModems, ",")
		}
	}

	limitStr := c.DefaultQuery("limit", "20")
	pageStr := c.DefaultQuery("page", "1")

	var limit int
	var page int

	// Helper logic to parse int not included here, assuming basic strconv or just string input if possible?
	// No, better implement strict parsing.
	// Since imports are limited, let's use safe unchecked casts or add strconv.
	// Adding strconv to imports below.

	// Simplified parsing
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
		limit = l
	} else {
		limit = 20
	}
	if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
		page = p
	} else {
		page = 1
	}

	iccid := c.Query("iccid")

	query := h.db.Model(&model.SMS{}) // Start with model to allow counting

	if iccid != "" {
		// If user requests specific ICCID, ensure they have access
		if !isAdmin && !contains(allowed, iccid) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied to this ICCID"})
			return
		}
		query = query.Where("iccid = ?", iccid)
	} else {
		// If no ICCID specified, filter by allowed
		if !isAdmin {
			if len(allowed) == 0 {
				query = query.Where("1 = 0")
			} else {
				query = query.Where("iccid IN ?", allowed)
			}
		}
	}

	var total int64
	query.Count(&total)

	var smsList []model.SMS
	offset := (page - 1) * limit
	if err := query.Order("timestamp desc").Limit(limit).Offset(offset).Find(&smsList).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	for i, s := range smsList {
		if s.Content == "" && s.Phone == "" {
			d, _ := hex.DecodeString(s.RawPDU)
			// SMSC Address Handling
			// The first octet is the length of the SMSC field in octets
			if len(d) > 0 {
				smscLen := int(d[0])
				if len(d) > smscLen+1 {
					// Skip SMSC field (Len byte + Address bytes)
					d = d[smscLen+1:]
				}
			}
			msg, err := sms.Unmarshal(d)
			content := ""
			if err != nil {
				log.Panic(err)
			}
			// Use tpdu.DecodeUserData to correctly handle GSM7/UCS2 encoding
			alphabet, alphaErr := msg.DCS.Alphabet()
			var udContent []byte
			var decErr error

			if alphaErr != nil {
				decErr = alphaErr // Handle alpha error as decode error
			} else {
				udContent, decErr = tpdu.DecodeUserData(msg.UD, msg.UDH, alphabet)
			}

			if decErr == nil {
				content = string(udContent)
			} else {
				logger.Log.Warnf("[] Failed to decode UD: %v. DCS: %02X.", decErr, msg.DCS)
				// Fallback to simpler extraction or raw
				// If 7-bit, simply casting to string is wrong, but better than nothing for ASCII-like?
				// Actually better to show hex if it failed
				content = fmt.Sprintf("Decode Failed (DCS: 0x%02X)", msg.DCS)
			}

			// Final check
			if content == "" && len(msg.UD) > 0 {
				content = fmt.Sprintf("UD Hex: %X", msg.UD)
			}
			log.Println(content)
			smsList[i].Content = content
			smsList[i].Phone = msg.OA.Number()
			h.db.Updates(&smsList[i])
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  smsList,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
