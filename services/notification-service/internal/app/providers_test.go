package app

import (
	"context"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

func TestSendersFromConfiguration(t *testing.T) {
	reg, err := Senders(context.Background(), &config.Config{PushProvider: "dev", EmailProvider: "dev", SMSProvider: "dev"})
	if err != nil || len(reg) != 3 || reg[domain.ChannelSMS].Name() != "capture-sms" {
		t.Fatalf("%v %v", reg, err)
	}
	reg, err = Senders(context.Background(), &config.Config{PushProvider: "disabled", EmailProvider: "disabled", SMSProvider: "disabled"})
	if err != nil || len(reg) != 0 {
		t.Fatalf("disabled channels have no sender: %v %v", reg, err)
	}
	reg, err = Senders(context.Background(), &config.Config{PushProvider: "disabled", EmailProvider: "disabled", SMSProvider: "twilio",
		Twilio: &config.TwilioConfig{AccountSID: "AC1", AuthToken: security.NewSecret("t"), From: "+15550000000"}})
	if err != nil || reg[domain.ChannelSMS].Name() != "twilio" || reg[domain.ChannelSMS].Channel() != domain.ChannelSMS {
		t.Fatalf("optional adapter: %v %v", reg, err)
	}
	if _, err := Senders(context.Background(), &config.Config{PushProvider: "disabled", EmailProvider: "disabled", SMSProvider: "acme"}); err == nil {
		t.Fatal("unknown provider")
	}
	names := ProviderNames()
	if len(names[domain.ChannelSMS]) != 2 || len(names[domain.ChannelPush]) != 2 {
		t.Fatalf("%v", names)
	}
}
