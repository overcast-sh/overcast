package groups

import (
	"context"
	"fmt"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// Cognito returns the Cognito service group.
//
// Groups: cognito-token-validity. cognito-userpools resolves through its
// authored scenario (compat/model/authored/cognito-userpools.json).
func Cognito() ServiceGroup {
	g := &cognitoCliGroup{}
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

type cognitoCliGroup struct{}

// ── cognito-token-validity ─────────────────────────────────────────────────

func (g *cognitoCliGroup) setupTokenValidity(_ context.Context, _ *harness.TestContext) error {
	return nil
}

func (g *cognitoCliGroup) teardownTokenValidity(_ context.Context, t *harness.TestContext) error {
	poolID := t.GetString("tv_pool_id")
	clientID := t.GetString("tv_client_id")
	if poolID != "" && clientID != "" {
		awscli.Run(t.Endpoint, t.Region, "cognito-idp", "delete-user-pool-client", "--user-pool-id", poolID, "--client-id", clientID) //nolint:errcheck
	}
	if poolID != "" {
		awscli.Run(t.Endpoint, t.Region, "cognito-idp", "delete-user-pool", "--user-pool-id", poolID) //nolint:errcheck
	}
	return nil
}

func (g *cognitoCliGroup) CreateClientTokenValidity(_ context.Context, t *harness.TestContext) error {
	poolName := fmt.Sprintf("compat-tv-%s", t.RunID)
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "cognito-idp", "create-user-pool", "--pool-name", poolName)
	if err != nil {
		return err
	}
	pool, _ := out["UserPool"].(map[string]interface{})
	poolID, _ := pool["Id"].(string)
	if poolID == "" {
		return fmt.Errorf("CreateClientTokenValidity: missing pool Id")
	}
	t.Set("tv_pool_id", poolID)

	out, err = awscli.RunOutput(t.Endpoint, t.Region, "cognito-idp", "create-user-pool-client",
		"--user-pool-id", poolID,
		"--client-name", fmt.Sprintf("compat-client-%s", t.RunID),
		"--access-token-validity", "2",
		"--id-token-validity", "3",
		"--refresh-token-validity", "7",
		"--token-validity-units", `{"AccessToken":"hours","IdToken":"hours","RefreshToken":"days"}`,
	)
	if err != nil {
		return err
	}
	client, _ := out["UserPoolClient"].(map[string]interface{})
	clientID, _ := client["ClientId"].(string)
	if clientID == "" {
		return fmt.Errorf("CreateClientTokenValidity: missing ClientId")
	}
	t.Set("tv_client_id", clientID)
	return nil
}

func (g *cognitoCliGroup) DescribeClientTokenValidity(_ context.Context, t *harness.TestContext) error {
	poolID := t.GetString("tv_pool_id")
	clientID := t.GetString("tv_client_id")
	if poolID == "" || clientID == "" {
		return fmt.Errorf("DescribeClientTokenValidity: missing pool/client from create")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "cognito-idp", "describe-user-pool-client",
		"--user-pool-id", poolID,
		"--client-id", clientID,
	)
	if err != nil {
		return err
	}
	client, _ := out["UserPoolClient"].(map[string]interface{})
	if client == nil {
		return fmt.Errorf("DescribeClientTokenValidity: missing UserPoolClient")
	}
	return nil
}

func (g *cognitoCliGroup) UpdateClientTokenValidity(_ context.Context, t *harness.TestContext) error {
	poolID := t.GetString("tv_pool_id")
	clientID := t.GetString("tv_client_id")
	if poolID == "" || clientID == "" {
		return fmt.Errorf("UpdateClientTokenValidity: missing pool/client from create")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "cognito-idp", "update-user-pool-client",
		"--user-pool-id", poolID,
		"--client-id", clientID,
		"--access-token-validity", "30",
		"--token-validity-units", `{"AccessToken":"minutes","IdToken":"hours","RefreshToken":"days"}`,
	)
	if err != nil {
		return err
	}
	client, _ := out["UserPoolClient"].(map[string]interface{})
	if client == nil {
		return fmt.Errorf("UpdateClientTokenValidity: missing UserPoolClient")
	}
	return nil
}

func (g *cognitoCliGroup) DeleteUserPoolClient(_ context.Context, t *harness.TestContext) error {
	poolID := t.GetString("tv_pool_id")
	clientID := t.GetString("tv_client_id")
	if poolID == "" || clientID == "" {
		return nil
	}
	return awscli.Run(t.Endpoint, t.Region, "cognito-idp", "delete-user-pool-client",
		"--user-pool-id", poolID,
		"--client-id", clientID,
	)
}
