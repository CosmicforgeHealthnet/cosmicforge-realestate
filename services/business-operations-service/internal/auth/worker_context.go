package auth

import (
	"context"
	sharedauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// StoredJobContext is only for an owning worker after loading a durable job
// and checking its live account/session. It never accepts a broker delegation,
// creates a token, replaces Gateway authentication or grants permissions.
func StoredJobContext(ctx context.Context, account, org, session, job ids.ID) context.Context {
	ctx = context.WithValue(ctx, principalKey{}, sharedauth.Principal{Domain: platform.DomainBusinessOperations, ActorID: account, OrganizationID: org, SessionID: session})
	ctx = requestcontext.WithActor(ctx, requestcontext.Actor{Domain: string(platform.DomainBusinessOperations), ID: account})
	ctx = requestcontext.WithOrganization(ctx, org)
	return requestcontext.WithRequestID(ctx, "inventory-import:"+job.String())
}
