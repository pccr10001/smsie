package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func normalizeAuthBearer(raw string) string {
	parts := strings.SplitN(strings.TrimSpace(raw), " ", 2)
	if len(parts) != 2 {
		return ""
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func isSMSIEAPIKey(token string) bool {
	return strings.HasPrefix(token, "smsie_")
}

func hashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func makeAPIKeyPrefix(raw string) string {
	if len(raw) <= 16 {
		return raw
	}
	return raw[:16]
}

func randomAPIKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "smsie_" + hex.EncodeToString(buf), nil
}

func hashPasswordStrict(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func checkPasswordStrict(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
