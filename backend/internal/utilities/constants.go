package utilities

import (
	"strings"
)

// The vocabulary the module writes: the utility types and bill statuses the
// schema constrains, the meter status it leaves to the application, and the
// limits that keep a value inside its column.
const (
	defaultMeterSort   = "p.name ASC, m.meter_number ASC"
	defaultReadingSort = "ur.reading_date DESC"
	defaultBillSort    = "ub.date_due ASC"

	utilityWater       = "water"
	utilityElectricity = "electricity"
	utilityGas         = "gas"
	utilityGarbage     = "garbage"
	utilityInternet    = "internet"
	utilityOther       = "other"

	meterActive   = "active"
	meterInactive = "inactive"
	meterFaulty   = "faulty"

	// unitInactive mirrors the units table's own status vocabulary: a door taken
	// out of circulation should not be accruing charges.
	unitInactive = "inactive"

	billPending = "pending"
	billBilled  = "billed"
	billPaid    = "paid"
	billVoid    = "void"

	// leaseActive is the one lease status a bill may be raised against, because
	// a draft lease is not yet a tenancy that owes anything.
	leaseActive = "active"

	maxMeterNumber   = 50
	maxUnitOfMeasure = 10
	maxSearch        = 100
	maxNotes         = 255
	maxRate          = 999999999.9999
	maxReadingValue  = 9999999999.999
	maxAmount        = 9999999999.99
	maxGenerated     = 200
	maxLookupUnits   = 500
	maxDetailRows    = 50

	// A generated bill is due on the tenant's rent due day, so the day has to sit
	// inside the 1-28 range the leases table constrains due_day to.
	minDueDay     = 1
	maxDueDay     = 28
	defaultDueDay = 5

	dateLayout = "2006-01-02"

	notPendingBill = "Only a pending bill can be changed"
	readingLocked  = "This reading has already been billed or the next one has; void that bill first"
)

var (
	utilityTypes  = []string{utilityWater, utilityElectricity, utilityGas, utilityGarbage, utilityInternet, utilityOther}
	meterStatuses = []string{meterActive, meterInactive, meterFaulty}
	billStatuses  = []string{billPending, billBilled, billPaid, billVoid}

	utilityTypeMessage = "must be one of " + strings.Join(utilityTypes, ", ")
	meterStatusMessage = "must be one of " + strings.Join(meterStatuses, ", ")
	billStatusMessage  = "must be one of " + strings.Join(billStatuses, ", ")

	meterSortColumns = map[string]string{
		"number":       "m.meter_number",
		"type":         "m.utility_type",
		"unit":         "mu.number",
		"property":     "p.name",
		"rate":         "m.rate_per_unit",
		"status":       "m.status",
		"last_reading": "last_reading_date",
		"created_at":   "m.created_at",
		"updated_at":   "m.updated_at",
	}
	readingSortColumns = map[string]string{
		"date":        "ur.reading_date",
		"consumption": "ur.consumption",
		"current":     "ur.current_reading",
		"created_at":  "ur.created_at",
	}
	billSortColumns = map[string]string{
		"due":        "ub.date_due",
		"amount":     "ub.amount",
		"type":       "ub.type",
		"status":     "ub.status",
		"period":     "ub.period_start",
		"unit":       "u.number",
		"property":   "p.name",
		"created_at": "ub.created_at",
		"updated_at": "ub.updated_at",
	}
)
