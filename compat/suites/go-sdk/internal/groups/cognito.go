package groups

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ciptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// Cognito returns the Cognito service group.
//
// Groups: cognito-token-validity. cognito-userpools resolves through its
// authored scenario (compat/model/authored/cognito-userpools.json).
func Cognito(c *clients.Clients) ServiceGroup {
	g := &cognitoGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"cognito-token-validity:CreateUserPoolClientWithTokenValidity": g.CreateClientTokenValidity,
			"cognito-token-validity:DescribeUserPoolClientTokenValidity":   g.DescribeClientTokenValidity,
			"cognito-token-validity:UpdateUserPoolClientTokenValidity":     g.UpdateClientTokenValidity,
			"cognito-token-validity:DeleteUserPoolClient":                  g.DeleteUserPoolClient,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"cognito-token-validity": g.setupTokenValidity,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"cognito-token-validity": g.teardownTokenValidity,
		},
	}
}

type cognitoGroup struct{ c *clients.Clients }

func (g *cognitoGroup) cl() *cip.Client { return g.c.Cognito() }

// ── cognito-token-validity ─────────────────────────────────────────────────

func (g *cognitoGroup) setupTokenValidity(_ context.Context, _ *harness.TestContext) error {
	return nil
}

func (g *cognitoGroup) teardownTokenValidity(ctx context.Context, t *harness.TestContext) error {
	poolID := t.GetString("tv_pool_id")
	clientID := t.GetString("tv_client_id")
	if poolID != "" && clientID != "" {
		g.cl().DeleteUserPoolClient(ctx, &cip.DeleteUserPoolClientInput{ //nolint:errcheck
			UserPoolId: aws.String(poolID),
			ClientId:   aws.String(clientID),
		})
	}
	if poolID != "" {
		g.cl().DeleteUserPool(ctx, &cip.DeleteUserPoolInput{UserPoolId: aws.String(poolID)}) //nolint:errcheck
	}
	return nil
}

func (g *cognitoGroup) CreateClientTokenValidity(ctx context.Context, t *harness.TestContext) error {
	poolName := fmt.Sprintf("compat-tv-%s", t.RunID)
	poolResp, err := g.cl().CreateUserPool(ctx, &cip.CreateUserPoolInput{
		PoolName: aws.String(poolName),
	})
	if err != nil {
		return err
	}
	poolID := *poolResp.UserPool.Id
	t.Set("tv_pool_id", poolID)

	resp, err := g.cl().CreateUserPoolClient(ctx, &cip.CreateUserPoolClientInput{
		UserPoolId:           aws.String(poolID),
		ClientName:           aws.String(fmt.Sprintf("compat-client-%s", t.RunID)),
		AccessTokenValidity:  aws.Int32(2),
		IdTokenValidity:      aws.Int32(3),
		RefreshTokenValidity: 7,
		TokenValidityUnits: &ciptypes.TokenValidityUnitsType{
			AccessToken:  ciptypes.TimeUnitsTypeHours,
			IdToken:      ciptypes.TimeUnitsTypeHours,
			RefreshToken: ciptypes.TimeUnitsTypeDays,
		},
	})
	if err != nil {
		return err
	}
	if resp.UserPoolClient == nil || resp.UserPoolClient.ClientId == nil {
		return fmt.Errorf("CreateClientTokenValidity: missing ClientId")
	}
	t.Set("tv_client_id", *resp.UserPoolClient.ClientId)
	return nil
}

func (g *cognitoGroup) DescribeClientTokenValidity(ctx context.Context, t *harness.TestContext) error {
	poolID := t.GetString("tv_pool_id")
	clientID := t.GetString("tv_client_id")
	if poolID == "" || clientID == "" {
		return fmt.Errorf("DescribeClientTokenValidity: missing pool/client from create")
	}
	_, err := g.cl().DescribeUserPoolClient(ctx, &cip.DescribeUserPoolClientInput{
		UserPoolId: aws.String(poolID),
		ClientId:   aws.String(clientID),
	})
	return err
}

func (g *cognitoGroup) UpdateClientTokenValidity(ctx context.Context, t *harness.TestContext) error {
	poolID := t.GetString("tv_pool_id")
	clientID := t.GetString("tv_client_id")
	if poolID == "" || clientID == "" {
		return fmt.Errorf("UpdateClientTokenValidity: missing pool/client from create")
	}
	_, err := g.cl().UpdateUserPoolClient(ctx, &cip.UpdateUserPoolClientInput{
		UserPoolId:          aws.String(poolID),
		ClientId:            aws.String(clientID),
		AccessTokenValidity: aws.Int32(30),
		TokenValidityUnits: &ciptypes.TokenValidityUnitsType{
			AccessToken:  ciptypes.TimeUnitsTypeMinutes,
			IdToken:      ciptypes.TimeUnitsTypeHours,
			RefreshToken: ciptypes.TimeUnitsTypeDays,
		},
	})
	return err
}

func (g *cognitoGroup) DeleteUserPoolClient(ctx context.Context, t *harness.TestContext) error {
	poolID := t.GetString("tv_pool_id")
	clientID := t.GetString("tv_client_id")
	if poolID == "" || clientID == "" {
		return nil
	}
	_, err := g.cl().DeleteUserPoolClient(ctx, &cip.DeleteUserPoolClientInput{
		UserPoolId: aws.String(poolID),
		ClientId:   aws.String(clientID),
	})
	return err
}
