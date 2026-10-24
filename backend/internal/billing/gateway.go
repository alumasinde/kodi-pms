package billing

import (
	"context"
	"time"

	"kodipms/internal/platform/httpx"
	"kodipms/internal/rbac"
)

// GatewayPaymentInput is a receipt a payment channel confirmed outside the app:
// an M-Pesa C2B confirmation, an STK callback, a bank statement line. The caller
// has already established that the money arrived and which lease it belongs to;
// what it does not get to decide is how the receipt is spent.
type GatewayPaymentInput struct {
	OrganizationID  uint64
	LeaseID         uint64
	PaymentMethodID uint64
	Amount          float64
	Reference       string
	PaidAt          time.Time
	Notes           string
}

// RecordGatewayPayment books a channel-confirmed receipt with no person to
// authorize, so that money only ever enters the ledger through one path: the
// same oldest-due-first allocation, the same tenant credit for the remainder and
// the same journal posting and database triggers a cashier goes through.
//
// The channel's own timestamp is used only when the organization's calendar
// accepts it. Refusing money that is already in the till over a clock the
// provider got wrong helps nobody, so an unusable date is logged and this instant
// is booked instead -- the opposite of the client path, where a future or
// ancient paid_at is the caller's mistake to correct.
//
// The reference matters: payments are unique per organization, method and
// reference, so a channel that retries a confirmation it already booked is
// refused rather than paid twice.
func (s *Service) RecordGatewayPayment(ctx context.Context, in GatewayPaymentInput, meta ClientMeta) (*PaymentDetailDTO, error) {
	if in.OrganizationID == 0 {
		return nil, httpx.Validation(map[string]string{"organization_id": "required"})
	}
	row, fields := buildPayment(in.OrganizationID, 0, gatewayRequest(in))
	if len(fields) > 0 {
		return nil, httpx.Validation(fields)
	}
	if !in.PaidAt.IsZero() {
		timezone, err := s.repo.OrganizationTimezone(ctx, in.OrganizationID)
		if err != nil {
			return nil, err
		}
		candidate := in.PaidAt.UTC()
		if problem := paymentDateProblem(candidate, todayIn(orgLocation(timezone))); problem != "" {
			s.log.Warn("gateway receipt date refused, booking today", "org_id", in.OrganizationID,
				"paid_at", candidate.Format(time.RFC3339), "reason", problem)
		} else {
			row.PaidAt = candidate
		}
	}
	return s.bookPayment(ctx, gatewayPrincipal(in.OrganizationID), row, nil, meta)
}

// gatewayPrincipal is the identity a channel books under: the organization's
// whole portfolio, no user, and no permissions, because the authority for this
// receipt is the provider's confirmation rather than a person's grant.
func gatewayPrincipal(orgID uint64) *rbac.Principal {
	return &rbac.Principal{
		OrgID: orgID,
		Scope: rbac.Scope{OrgID: orgID, AllProperties: true},
	}
}

func gatewayRequest(in GatewayPaymentInput) PaymentInput {
	gateway := PaymentInput{LeaseID: in.LeaseID, PaymentMethodID: in.PaymentMethodID, Amount: in.Amount}
	if in.Reference != "" {
		gateway.Reference = &in.Reference
	}
	if in.Notes != "" {
		gateway.Notes = &in.Notes
	}
	return gateway
}
