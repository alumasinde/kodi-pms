package billing

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

// cleanNumber validates a document number a caller types instead of accepting
// the generated one: the charset keeps it a plain reference everywhere it lands.
func cleanNumber(value, field string, fields map[string]string) string {
	number := strings.TrimSpace(value)
	if utf8.RuneCountInString(number) > maxNumberLen {
		fields[field] = fmt.Sprintf("must be at most %d characters", maxNumberLen)
		return ""
	}
	for _, r := range number {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' && r != '/' {
			fields[field] = "may contain letters, digits, dot, dash, slash and underscore only"
			return ""
		}
	}
	return number
}

// numberPrefix builds the counter namespace for one property: {prefix}-{code},
// or just the prefix when the property has no code.
func numberPrefix(prefix, propertyCode string) string {
	code := strings.TrimSpace(propertyCode)
	if code == "" {
		return prefix
	}
	return prefix + "-" + code
}

func mergeSettings(rows []settingRow, orgID uint64) BillingSettings {
	settings := BillingSettings{
		InvoicePrefix: defaultInvoicePrefix, CurrencyCode: defaultCurrency,
		RentDueDay: defaultRentDueDay, LateFeeGraceDays: defaultGraceDays,
		LateFeeType: defaultLateFeeType, LateFeeValue: defaultLateFeeValue, VATRate: defaultVATRate,
	}
	apply := func(row settingRow) {
		if !row.Value.Valid {
			return
		}
		value := strings.TrimSpace(row.Value.String)
		if value == "" {
			return
		}
		switch row.Key {
		case "invoice_prefix":
			if clean := sanitizePrefix(value); clean != "" {
				settings.InvoicePrefix = clean
			}
		case "currency_code":
			if len(value) == 3 {
				settings.CurrencyCode = strings.ToUpper(value)
			}
		case "rent_due_day":
			if day, err := strconv.Atoi(value); err == nil && day >= minDayOfMonth && day <= maxDayOfMonth {
				settings.RentDueDay = day
			}
		case "late_fee_grace_days":
			if days, err := strconv.Atoi(value); err == nil && days >= 0 && days <= 365 {
				settings.LateFeeGraceDays = days
			}
		case "late_fee_type":
			if value == "percent" || value == "fixed" {
				settings.LateFeeType = value
			}
		case "late_fee_value":
			if amount, err := strconv.ParseFloat(value, 64); err == nil && amount >= 0 && amount <= maxAmount {
				settings.LateFeeValue = roundToCents(amount)
			}
		case "vat_rate":
			if rate, err := strconv.ParseFloat(value, 64); err == nil && rate >= 0 && rate <= maxTaxRate {
				settings.VATRate = roundToCents(rate)
			}
		}
	}
	for _, row := range rows {
		if !row.OrganizationID.Valid {
			apply(row)
		}
	}
	for _, row := range rows {
		if row.OrganizationID.Valid && row.OrganizationID.Int64 == int64(orgID) {
			apply(row)
		}
	}
	return settings
}

// sanitizePrefix keeps a configured prefix from smuggling operators into an
// invoice number, which is echoed back to clients and used in a LIKE pattern.
func sanitizePrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 20 {
		return ""
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return ""
		}
	}
	return strings.ToUpper(value)
}

func inList(list []string, value string) bool {
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

func roundQuantity(value float64) float64 {
	return math.Round(value*1000) / 1000
}

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

func parseOptionalDate(name string, value *string, fields map[string]string) time.Time {
	if value == nil {
		return time.Time{}
	}
	parsed, ok := parseDate(*value)
	if !ok {
		fields[name] = "must be a date formatted as " + dateLayout
	}
	return parsed
}

func parseTimestampField(name string, value *string, fields map[string]string) (time.Time, bool) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return time.Time{}, false
	}
	parsed, ok := parseDate(*value)
	if !ok {
		fields[name] = "must be a date or an RFC3339 timestamp"
	}
	return parsed, ok
}

func truncateDate(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

// todayIn answers "what date is it for this organization", because due dates,
// grace periods and invoice periods are business dates in the organization's
// own timezone, not UTC.
func todayIn(loc *time.Location) time.Time {
	return truncateDate(time.Now().In(loc))
}

func orgLocation(timezone string) *time.Location {
	// Africa/Nairobi is the operating timezone of every seeded organization and
	// the fallback when a timezone string cannot be loaded.
	if zone := strings.TrimSpace(timezone); zone != "" {
		if loc, err := time.LoadLocation(zone); err == nil {
			return loc
		}
	}
	loc, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		return time.UTC
	}
	return loc
}

func nullableID(id uint64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func truncate(value string, limit int) any {
	if value == "" {
		return nil
	}
	return truncateString(value, limit)
}

func truncateString(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func nullText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// isJSONNull tells an explicit JSON null apart from an absent field: one clears
// a value, the other leaves it alone.
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func nullTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func nullIDValue(value sql.NullInt64) uint64 {
	if !value.Valid {
		return 0
	}
	return uint64(value.Int64)
}

func isDuplicateEntry(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func isForeignKeyRestrict(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1451
}

// isTriggerRejection recognises SIGNAL SQLSTATE '45000', which every billing
// trigger in 000006 raises when a write would break a money rule.
func isTriggerRejection(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1644
}
