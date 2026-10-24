package mpesa

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// payloadJSON hands a stored callback back as the document it is rather than as an
// escaped string. A payload MySQL could not have stored as JSON -- a row written
// before the column existed, say -- is left out instead of guessed at.
func payloadJSON(value sql.NullString) json.RawMessage {
	trimmed := strings.TrimSpace(value.String)
	if !value.Valid || !json.Valid([]byte(trimmed)) {
		return nil
	}
	return json.RawMessage(trimmed)
}

func inList(value string, list []string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func roundToCents(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*100) / 100
}

func optText(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func optID(id uint64) sql.NullInt64 {
	if id == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(id), Valid: true}
}

func nullText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullID(value sql.NullInt64) uint64 {
	if !value.Valid || value.Int64 <= 0 {
		return 0
	}
	return uint64(value.Int64)
}

func flag(value int8) bool { return value != 0 }

func intPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	n := int(value.Int64)
	return &n
}

// parseDate accepts the API's date layout and, for a client that sends a full
// timestamp, RFC 3339. Range filters are dates, so a time is cut down to its day.
func parseDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(dateLayout, value); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), true
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// channelTime reads the shapes a Safaricom timestamp actually arrives in: the
// yyyyMMddHHmmss of a C2B validation, the epoch milliseconds of a confirmation or
// STK callback, and the ISO date an operator types when recording a receipt from
// an SMS. Anything else is not a date, and the caller falls back to this instant
// rather than booking money against year 1.
func channelTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if t, err := time.ParseInLocation(darajaTimeLayout, value, time.UTC); err == nil {
		return t, true
	}
	if millis, err := strconv.ParseInt(value, 10, 64); err == nil && millis > 0 {
		if millis > 1_000_000_000_000 {
			return time.UnixMilli(millis).UTC(), true
		}
		return time.Unix(millis, 0).UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), true
	}
	if t, ok := parseDate(value); ok {
		return t, true
	}
	return time.Time{}, false
}

// normalizeMsisdn turns whatever a tenant typed or Safaricom sent into the
// 2547XXXXXXXX Daraja expects. Local 07/01/09 numbers, +254, 254 and a bare
// 9-digit tail all mean the same line.
func normalizeMsisdn(value string) (string, error) {
	digits := make([]rune, 0, 15)
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= '0' && r <= '9':
			digits = append(digits, r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return "", fmt.Errorf("%q is not a phone number", value)
		}
	}
	plain := string(digits)
	switch {
	case strings.HasPrefix(plain, "254") && len(plain) == 12:
		return plain, nil
	case len(plain) == 10 && plain[0] == '0':
		return "254" + plain[1:], nil
	case len(plain) == 9:
		switch plain[0] {
		case '7', '1', '9':
			return "254" + plain, nil
		}
	}
	return "", fmt.Errorf("%q is not a Kenyan mobile number", value)
}

// DisplayPhone puts a normalized MSISDN back into the +254 form the API shows and
// the stk table stores, so a prompt written from a callback stays readable.
func DisplayPhone(msisdn string) string {
	if strings.HasPrefix(msisdn, "254") && len(msisdn) == 12 {
		return "+" + msisdn
	}
	return msisdn
}

// randomHex is the source of both the webhook token and a merchant reference.
// Neither needs to be secret-quality for a human to remember, so hex is enough
// and keeps the token inside the 64 characters a URL can carry comfortably.
func randomHex(bytesN int) (string, error) {
	buf := make([]byte, bytesN)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// truncate clips a value the provider sent longer than its column, because a
// callback that cannot be stored is worse than one missing a note.
func truncate(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}
