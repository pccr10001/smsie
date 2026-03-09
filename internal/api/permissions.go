package api

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/pccr10001/smsie/internal/model"
	"gorm.io/gorm"
)

const (
	PermMakeCall = "make_call"
	PermViewSMS  = "view_sms"
	PermSendSMS  = "send_sms"
	PermSendAT   = "send_at"
)

type authActor struct {
	User   *model.User
	APIKey *model.APIKey
}

func getActor(c *gin.Context) (*authActor, bool) {
	userObj, exists := c.Get("user")
	if !exists {
		return nil, false
	}
	user, ok := userObj.(*model.User)
	if !ok || user == nil {
		return nil, false
	}

	var key *model.APIKey
	if keyObj, ok := c.Get("api_key"); ok {
		if cast, ok := keyObj.(*model.APIKey); ok {
			key = cast
		}
	}

	return &authActor{User: user, APIKey: key}, true
}

func splitAndTrimAllowed(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{}
	}
	if s == "*" {
		return []string{"*"}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func userCanAccessICCID(user *model.User, iccid string) bool {
	if user == nil {
		return false
	}
	if user.Role == "admin" {
		return true
	}
	allowed := splitAndTrimAllowed(user.AllowedModems)
	if len(allowed) == 0 {
		return false
	}
	for _, item := range allowed {
		if item == "*" || item == iccid {
			return true
		}
	}
	return false
}

func userAllowedSet(user *model.User) map[string]struct{} {
	out := map[string]struct{}{}
	if user == nil {
		return out
	}
	for _, item := range splitAndTrimAllowed(user.AllowedModems) {
		out[item] = struct{}{}
	}
	return out
}

func anyPermissionTrue(rule model.UserModemPermission) bool {
	return rule.CanMakeCall || rule.CanViewSMS || rule.CanSendSMS || rule.CanSendAT
}

func anyAPIKeyPermissionTrue(key *model.APIKey) bool {
	if key == nil {
		return true
	}
	return key.CanMakeCall || key.CanViewSMS || key.CanSendSMS || key.CanSendAT
}

func allowedICCIDsForPermission(db *gorm.DB, user *model.User, perm string) ([]string, error) {
	if user == nil {
		return []string{}, nil
	}
	if user.Role == "admin" {
		return nil, nil
	}

	allowedSet := userAllowedSet(user)
	if len(allowedSet) == 0 {
		return []string{}, nil
	}

	var rules []model.UserModemPermission
	if err := db.Where("user_id = ?", user.ID).Find(&rules).Error; err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		if _, wildcard := allowedSet["*"]; wildcard {
			return []string{"*"}, nil
		}
		out := make([]string, 0, len(allowedSet))
		for iccid := range allowedSet {
			if iccid == "*" {
				continue
			}
			out = append(out, iccid)
		}
		return out, nil
	}

	outSet := map[string]struct{}{}
	for _, rule := range rules {
		if _, wildcard := allowedSet["*"]; !wildcard {
			if _, ok := allowedSet[rule.ICCID]; !ok {
				continue
			}
		}

		if perm == "" {
			if anyPermissionTrue(rule) {
				outSet[rule.ICCID] = struct{}{}
			}
			continue
		}

		if permissionFlagFromRule(&rule, perm) {
			outSet[rule.ICCID] = struct{}{}
		}
	}

	out := make([]string, 0, len(outSet))
	for iccid := range outSet {
		out = append(out, iccid)
	}
	return out, nil
}

func userHasAnyPermission(db *gorm.DB, user *model.User, perm string) bool {
	if user == nil {
		return false
	}
	if user.Role == "admin" {
		return true
	}
	list, err := allowedICCIDsForPermission(db, user, perm)
	if err != nil {
		return false
	}
	return len(list) > 0
}

func permissionFlagFromKey(key *model.APIKey, perm string) bool {
	if key == nil {
		return true
	}
	switch perm {
	case "":
		return anyAPIKeyPermissionTrue(key)
	case PermMakeCall:
		return key.CanMakeCall
	case PermViewSMS:
		return key.CanViewSMS
	case PermSendSMS:
		return key.CanSendSMS
	case PermSendAT:
		return key.CanSendAT
	default:
		return false
	}
}

func permissionFlagFromRule(rule *model.UserModemPermission, perm string) bool {
	if rule == nil {
		return false
	}
	switch perm {
	case "":
		return anyPermissionTrue(*rule)
	case PermMakeCall:
		return rule.CanMakeCall
	case PermViewSMS:
		return rule.CanViewSMS
	case PermSendSMS:
		return rule.CanSendSMS
	case PermSendAT:
		return rule.CanSendAT
	default:
		return false
	}
}

func hasWildcardICCID(list []string) bool {
	for _, item := range list {
		if item == "*" {
			return true
		}
	}
	return false
}

func actorCanAccessICCIDPermission(db *gorm.DB, actor *authActor, iccid, perm string) (bool, int, string) {
	if actor == nil || actor.User == nil {
		return false, 401, "Unauthorized"
	}

	if !userCanAccessICCID(actor.User, iccid) {
		return false, 403, "Access denied for this modem"
	}

	if !permissionFlagFromKey(actor.APIKey, perm) {
		return false, 403, "API key permission denied"
	}

	if actor.User.Role == "admin" {
		return true, 0, ""
	}

	var rulesCount int64
	if err := db.Model(&model.UserModemPermission{}).Where("user_id = ?", actor.User.ID).Count(&rulesCount).Error; err != nil {
		return false, 500, "Permission check failed"
	}
	if rulesCount == 0 {
		return true, 0, ""
	}

	var rule model.UserModemPermission
	err := db.Where("user_id = ? AND iccid = ?", actor.User.ID, iccid).First(&rule).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, 403, "Permission denied for this modem"
		}
		return false, 500, "Permission check failed"
	}

	if !permissionFlagFromRule(&rule, perm) {
		return false, 403, "Permission denied for this action"
	}

	return true, 0, ""
}

func enforceICCIDPermission(c *gin.Context, db *gorm.DB, iccid, perm string) bool {
	actor, ok := getActor(c)
	if !ok {
		c.JSON(401, gin.H{"error": "Unauthorized"})
		return false
	}

	allowed, status, message := actorCanAccessICCIDPermission(db, actor, iccid, perm)
	if allowed {
		return true
	}

	c.JSON(status, gin.H{"error": message})
	return false
}
