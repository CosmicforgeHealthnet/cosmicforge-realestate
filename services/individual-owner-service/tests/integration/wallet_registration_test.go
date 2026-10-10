package integration

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"sync"
	"testing"
)

type walletDeliveryFixture struct {
	mu       sync.Mutex
	fail     bool
	calls    int
	accounts map[ids.ID]string
}

func (p *walletDeliveryFixture) Ensure(_ context.Context, id ids.ID, ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail {
		return errors.New("payment unavailable")
	}
	if p.accounts == nil {
		p.accounts = map[ids.ID]string{}
	}
	p.accounts[id] = ref
	return nil
}
func TestAutomaticWalletRegistrationDurableDelivery(t *testing.T) {
	h := newHarness(t)
	o := h.register(false)
	client := &walletDeliveryFixture{fail: true}
	h.app.Service.SetWalletRegistration(client)
	if h.count(`SELECT count(*) FROM wallet_registration_delivery WHERE account_id=$1`, o.id) != 1 {
		t.Fatal("registration not queued atomically")
	}
	if e := h.app.Service.DeliverWalletRegistrations(context.Background()); e != nil {
		t.Fatal(e)
	}
	if h.count(`SELECT count(*) FROM wallet_registration_delivery WHERE account_id=$1 AND delivered_at IS NULL AND attempts=1 AND last_error_code='PAYMENT_WALLET_UNAVAILABLE'`, o.id) != 1 {
		t.Fatal("failure was lost")
	}
	client.fail = false
	if _, e := h.db.Pool.Exec(context.Background(), `UPDATE wallet_registration_delivery SET lease_token=$2,lease_until=now()-interval '1 second',next_attempt_at=now()-interval '1 second' WHERE account_id=$1`, o.id, ids.New()); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := h.app.Service.DeliverWalletRegistrations(context.Background()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if client.calls != 2 || len(client.accounts) != 1 || client.accounts[o.id] == "" {
		t.Fatal("duplicate delivery", client.calls, client.accounts)
	}
	if h.count(`SELECT count(*) FROM wallet_registration_delivery WHERE account_id=$1 AND delivered_at IS NOT NULL AND attempts=2`, o.id) != 1 {
		t.Fatal("not acknowledged")
	}
	if e := h.app.Service.DeliverWalletRegistrations(context.Background()); e != nil {
		t.Fatal(e)
	}
	if client.calls != 2 {
		t.Fatal("redelivered acknowledged registration")
	}
}
