package utilities

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"

	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// Pure helpers: the parsing, rounding and error mapping the service files share.
// Nothing here touches the database.

func authorize(p *rbac.Principal, permission string) error {
	if p == nil || p.OrgID == 0 {
		return httpx.ErrUnauthorized
	}
	if !p.Can(permission) {
		return httpx.ErrForbidden
	}
	return nil
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

// roundToThree matches the three decimals the reading columns are declared with,
// so a dial read as 12.3456 is stored at the precision the column can hold.
func roundToThree(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
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

func parseRequiredDate(name, value string, fields map[string]string) time.Time {
	parsed, ok := parseDate(value)
	if !ok {
		fields[name] = "must be a date formatted as " + dateLayout
	}
	return parsed
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

// parseOptionalDateString is the same test on a field that cannot be sent as
// JSON null: an empty string means the caller left it out and the module fills it
// in, anything unparseable is a validation error.
func parseOptionalDateString(name, value string, fields map[string]string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	parsed, ok := parseDate(value)
	if !ok {
		fields[name] = "must be a date formatted as " + dateLayout
	}
	return parsed
}

// truncateDate drops the clock so a business date compares cleanly.
func truncateDate(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

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

// dueDateAfter places a generated bill on the tenant's rent due day in the month
// after the reading, so utilities are collected with the rent they follow rather
// than on a date nobody reconciles against.
func dueDateAfter(on time.Time, dueDay int) time.Time {
	if dueDay < minDueDay || dueDay > maxDueDay {
		dueDay = defaultDueDay
	}
	first := time.Date(on.Year(), on.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	return time.Date(first.Year(), first.Month(), dueDay, 0, 0, 0, 0, time.UTC)
}

func nullableID(id sql.NullInt64) any {
	if !id.Valid || id.Int64 <= 0 {
		return nil
	}
	return uint64(id.Int64)
}

func nullText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullIDPtr(value sql.NullInt64) *uint64 {
	if !value.Valid || value.Int64 <= 0 {
		return nil
	}
	out := uint64(value.Int64)
	return &out
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time.UTC()
	return &out
}

func nullFloatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	out := value.Float64
	return &out
}

func isDuplicateEntry(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

// checkRejection turns MySQL 3819, the error a CHECK constraint raises, into a
// 409 carrying the schema's own words. The utility tables keep their value rules
// in the schema, so reaching here means a row was wrong in a way the caller
// should be told about rather than being shown a 500.
func checkRejection(err error, fallback string) error {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 3819 {
		return nil
	}
	message := mysqlErr.Message
	if i := strings.LastIndexByte(message, ']'); i >= 0 {
		message = message[i+1:]
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = fallback
	}
	return httpx.Conflict(message)
}

// cleanLabel trims a short free-text field and records the reason it cannot be
// used, which is how the input builders collect one validation response instead
// of failing on the first problem.
func cleanLabel(value string, max int, name string, fields map[string]string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		fields[name] = "required"
	case utf8.RuneCountInString(value) > max:
		fields[name] = fmt.Sprintf("must be at most %d characters", max)
	case strings.IndexFunc(value, unicode.IsControl) >= 0:
		fields[name] = "cannot contain control characters"
	}
	return value
}
