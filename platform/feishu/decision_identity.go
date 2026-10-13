package feishu

import (
	"context"
	"fmt"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
)

// open_id is scoped to an application. Never compare a manager's open_id to
// the member's callback operator directly, nor authorize by display name.
func (p *Platform) ResolveDecisionUser(ctx context.Context, source core.Platform, user string) (string, error) {
	var from *Platform
	switch v := source.(type) {
	case *Platform:
		from = v
	case *interactivePlatform:
		from = v.Platform
	default:
		return "", fmt.Errorf("decision identity source unavailable")
	}
	if user == "" || from.domain != p.domain {
		return "", fmt.Errorf("invalid decision identity scope")
	}
	if from.appID == p.appID {
		return user, nil
	}
	origin, err := from.decisionContactUser(ctx, user, "open_id")
	if err != nil {
		return "", err
	}
	if origin.UnionId == nil || *origin.UnionId == "" {
		return "", fmt.Errorf("requester has no cross-application identity")
	}
	local, err := p.decisionContactUser(ctx, *origin.UnionId, "union_id")
	if err != nil {
		return "", err
	}
	if local.UnionId == nil || *local.UnionId != *origin.UnionId || local.OpenId == nil || *local.OpenId == "" {
		return "", fmt.Errorf("requester identity could not be verified in destination application")
	}
	return *local.OpenId, nil
}

func (p *Platform) decisionContactUser(ctx context.Context, id, idType string) (*larkcontact.User, error) {
	var user *larkcontact.User
	req := larkcontact.NewGetUserReqBuilder().UserId(id).UserIdType(idType).Build()
	err := p.withFreshTenantAccessTokenRetry(ctx, "resolve decision identity", func(c *lark.Client, opts ...larkcore.RequestOptionFunc) error {
		resp, err := c.Contact.User.Get(ctx, req, opts...)
		if err != nil {
			return err
		}
		if !resp.Success() || resp.Data == nil || resp.Data.User == nil {
			return fmt.Errorf("decision identity lookup failed (code %d)", resp.Code)
		}
		user = resp.Data.User
		return nil
	})
	return user, err
}

var _ core.DecisionUserResolver = (*Platform)(nil)
