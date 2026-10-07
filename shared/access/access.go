package access

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

var streamPattern = regexp.MustCompile(`^live/[A-Za-z0-9_-]+/[0-9]{4}-[0-9]{2}-[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$`)

type Claims struct {
	Stream  string `json:"stream"`
	Role    string `json:"role"`
	Expires int64  `json:"expires"`
}

func ValidStream(stream string) bool { return streamPattern.MatchString(stream) }

func Sign(secret []byte, claims Claims) (string, error) {
	if !ValidStream(claims.Stream) || claims.Expires <= time.Now().Unix() {
		return "", errors.New("invalid claims")
	}
	data, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func Verify(secret []byte, token, role, stream string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || !ValidStream(stream) {
		return errors.New("invalid token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("invalid signature")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return errors.New("invalid signature")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("invalid payload")
	}
	var claims Claims
	if json.Unmarshal(data, &claims) != nil || claims.Stream != stream || claims.Role != role || claims.Expires <= time.Now().Unix() {
		return errors.New("token expired or scope mismatch")
	}
	return nil
}
