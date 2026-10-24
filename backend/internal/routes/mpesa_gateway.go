package routes

import (
	"context"

	"kodipms/internal/billing"
	"kodipms/internal/mpesa"
)

// billingGateway is the seam between two modules that are not allowed to import
// each other. mpesa names the operation it needs -- book a receipt the provider
// already confirmed against this lease -- and billing implements it. This file is
// the only place in the backend where the two vocabularies meet, which is what
// keeps the money path single: an M-Pesa receipt is spent by the same code a
// cashier's payment goes through, allocation, credit and journal entries included.
type billingGateway struct {
	billing *billing.Service
}

// NewMpesaPaymentBooker wires billing into the M-Pesa service. Passing nil instead
// leaves the module able to record receipts but not to book them, and it says so
// rather than quietly dropping the money.
func NewMpesaPaymentBooker(svc *billing.Service) mpesa.PaymentBooker {
	if svc == nil {
		return nil
	}
	return &billingGateway{billing: svc}
}

func (g *billingGateway) Book(ctx context.Context, in mpesa.GatewayPayment) (mpesa.GatewayReceipt, error) {
	detail, err := g.billing.RecordGatewayPayment(ctx, billing.GatewayPaymentInput{
		OrganizationID:  in.OrganizationID,
		LeaseID:         in.LeaseID,
		PaymentMethodID: in.PaymentMethodID,
		Amount:          in.Amount,
		Reference:       in.Reference,
		PaidAt:          in.PaidAt,
		Notes:           in.Notes,
	}, billing.ClientMeta{IP: in.Meta.IP, UserAgent: in.Meta.UserAgent})
	if err != nil {
		return mpesa.GatewayReceipt{}, err
	}
	return mpesa.GatewayReceipt{
		PaymentID: detail.ID,
		Amount:    detail.Amount,
		Allocated: detail.Allocated,
		Credit:    detail.Credit,
	}, nil
}
