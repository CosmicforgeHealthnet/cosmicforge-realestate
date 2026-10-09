package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/channels"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/domain"
)

// Provider registries: configuration name -> adapter factory. To add a
// provider, implement channels.PushProvider/EmailProvider/SMSProvider, add its
// settings to config and register a factory here. Notification logic (use
// cases, dispatch, retry classification) does not change.
var (
	pushProviders = map[string]func(context.Context, *config.Config) (channels.PushProvider, error){
		config.ProviderDev: func(_ context.Context, c *config.Config) (channels.PushProvider, error) {
			return devCapture(domain.ChannelPush, c.DevCaptureFile)
		},
		config.ProviderFCM: func(ctx context.Context, c *config.Config) (channels.PushProvider, error) {
			ts, err := channels.FCMTokenSourceFromJSON(ctx, []byte(c.FCM.CredentialsJSON.Reveal()))
			if err != nil {
				return nil, err
			}
			return channels.NewFCM(channels.FCMConfig{ProjectID: c.FCM.ProjectID, BaseURL: c.FCM.BaseURL, Tokens: ts, Timeout: c.SendTimeout})
		},
	}
	emailProviders = map[string]func(context.Context, *config.Config) (channels.EmailProvider, error){
		config.ProviderDev: func(_ context.Context, c *config.Config) (channels.EmailProvider, error) {
			return devCapture(domain.ChannelEmail, c.DevCaptureFile)
		},
		// Optional adapter; no production email provider has been selected.
		config.ProviderSMTP: func(_ context.Context, c *config.Config) (channels.EmailProvider, error) {
			return channels.NewSMTP(channels.SMTPConfig{Host: c.SMTP.Host, Port: c.SMTP.Port, Username: c.SMTP.Username,
				Password: c.SMTP.Password, From: c.SMTP.From, FromName: c.SMTP.FromName, RequireTLS: c.SMTP.RequireTLS, Timeout: c.SendTimeout})
		},
	}
	smsProviders = map[string]func(context.Context, *config.Config) (channels.SMSProvider, error){
		config.ProviderDev: func(_ context.Context, c *config.Config) (channels.SMSProvider, error) {
			return devCapture(domain.ChannelSMS, c.DevCaptureFile)
		},
		// Optional Twilio-compatible adapter; no production SMS provider has been selected.
		config.ProviderTwilio: func(_ context.Context, c *config.Config) (channels.SMSProvider, error) {
			return channels.NewTwilio(channels.TwilioConfig{BaseURL: c.Twilio.BaseURL, AccountSID: c.Twilio.AccountSID, AuthToken: c.Twilio.AuthToken,
				From: c.Twilio.From, MessagingServiceSID: c.Twilio.MessagingServiceSID, Timeout: c.SendTimeout})
		},
	}
)

// devCapture is the dev provider; its optional capture file is validated
// (created 0600, no symlink, owned by this user) before the service starts.
func devCapture(ch domain.Channel, file string) (*channels.Capture, error) {
	if file != "" {
		if err := channels.PrepareCaptureFile(file); err != nil {
			return nil, err
		}
	}
	return &channels.Capture{ChannelName: ch, File: file}, nil
}

// ProviderNames lists the registered adapter names per channel.
func ProviderNames() map[domain.Channel][]string {
	names := func(keys []string) []string { sort.Strings(keys); return keys }
	var p, e, s []string
	for k := range pushProviders {
		p = append(p, k)
	}
	for k := range emailProviders {
		e = append(e, k)
	}
	for k := range smsProviders {
		s = append(s, k)
	}
	return map[domain.Channel][]string{domain.ChannelPush: names(p), domain.ChannelEmail: names(e), domain.ChannelSMS: names(s)}
}

// Senders builds the configured channel senders. A channel set to
// "disabled" has no sender; requests for it are reported as
// CHANNEL_NOT_CONFIGURED. Credentials come only from configuration.
func Senders(ctx context.Context, cfg *config.Config) (channels.Registry, error) {
	reg := channels.Registry{}
	if cfg.PushProvider != config.ProviderDisabled {
		f, ok := pushProviders[cfg.PushProvider]
		if !ok {
			return nil, fmt.Errorf("unknown push provider %q", cfg.PushProvider)
		}
		p, err := f(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("push provider %s: %w", cfg.PushProvider, err)
		}
		reg[domain.ChannelPush] = channels.Push(p)
	}
	if cfg.EmailProvider != config.ProviderDisabled {
		f, ok := emailProviders[cfg.EmailProvider]
		if !ok {
			return nil, fmt.Errorf("unknown email provider %q", cfg.EmailProvider)
		}
		p, err := f(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("email provider %s: %w", cfg.EmailProvider, err)
		}
		reg[domain.ChannelEmail] = channels.Email(p)
	}
	if cfg.SMSProvider != config.ProviderDisabled {
		f, ok := smsProviders[cfg.SMSProvider]
		if !ok {
			return nil, fmt.Errorf("unknown sms provider %q", cfg.SMSProvider)
		}
		p, err := f(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("sms provider %s: %w", cfg.SMSProvider, err)
		}
		reg[domain.ChannelSMS] = channels.SMS(p)
	}
	return reg, nil
}
