package http

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"net/http"
)

func financialWalletRoutes() []Route {
	return []Route{
		CommandPre("POST", "/rent-obligations/{obligation_id}/financial-collection", "requestFinancialRentCollection", "Financial Wallet", "Request collection for an accepted rent quote", http.StatusOK,
			func(c *Call, in *usecases.AcceptedCollectionInput) (usecases.AcceptedCollectionEvidence, error) {
				return c.S.PrepareAcceptedRentCollection(c.Ctx, c.Subj, c.ID("obligation_id"), *in)
			},
			func(c *Call, _ *usecases.AcceptedCollectionInput, ev usecases.AcceptedCollectionEvidence) (payment.CollectionIntent, error) {
				return c.S.RecordAcceptedRentCollection(c.Ctx, c.Subj, c.ID("obligation_id"), ev)
			}).Describe("Owned unpaid rent only. Requires a Financial Core locked quote matching the business reference, rent principal and currency. Financial Core revalidates policy, consent and payer context. The signed external call precedes the Owner transaction. Retry uses a stable obligation key. No automatic debit; collected money is not paid rent or available earnings."),
		Get("/rent-obligations/{obligation_id}/financial-collection", "getFinancialRentCollection", "Financial Wallet", "Read Financial Core collection observation", func(c *Call) (payment.CollectionIntent, error) {
			return c.S.FinancialRentCollection(c.Ctx, c.Subj, c.ID("obligation_id"))
		}).Describe("Authorizes the Owner business obligation, validates the canonical local binding, then reads Financial Core directly. Canonical events also update a separate observation projection. Neither readback nor collection events change paid rent. Dependency outages remain unavailable."),
		Get("/financial-wallet", "getFinancialWallet", "Financial Wallet", "Read my authoritative wallet and balance buckets", func(c *Call) (payment.WalletSnapshot, error) { return c.S.OwnFinancialWallet(c.Ctx, c.Subj) }).Describe("Authenticated active Owner only. Lazily ensures the source-bound purpose wallet, then reads Financial Core balances over signed service requests. Pending/held money remains unavailable. No Owner-owned balance table or inferred custody. Dependency outage stays unavailable."),
	}
}
