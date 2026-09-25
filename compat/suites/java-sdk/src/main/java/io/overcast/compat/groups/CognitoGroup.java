package io.overcast.compat.groups;

import io.overcast.compat.clients.AwsClients;
import io.overcast.compat.harness.Assertions;
import io.overcast.compat.harness.TestContext;
import io.overcast.compat.harness.TestFn;
import software.amazon.awssdk.services.cognitoidentityprovider.CognitoIdentityProviderClient;
import software.amazon.awssdk.services.cognitoidentityprovider.model.*;

import java.util.Map;

/**
 * Cognito User Pools compatibility test group.
 *
 * <p>Groups: cognito-token-validity. cognito-userpools resolves through its
 * authored scenario (compat/model/authored/cognito-userpools.json).
 */
public final class CognitoGroup implements ServiceGroup {

    private final AwsClients clients;

    public CognitoGroup(AwsClients clients) {
        this.clients = clients;
    }

    private CognitoIdentityProviderClient cognito() { return clients.cognito(); }

    @Override
    public Map<String, TestFn> impls() {
        return Map.ofEntries(
                Map.entry("cognito-token-validity:CreateUserPoolClientWithTokenValidity", this::createClientTokenValidity),
                Map.entry("cognito-token-validity:DescribeUserPoolClientTokenValidity",   this::describeClientTokenValidity),
                Map.entry("cognito-token-validity:UpdateUserPoolClientTokenValidity",     this::updateClientTokenValidity),
                Map.entry("cognito-token-validity:DeleteUserPoolClient",                  this::deleteUserPoolClient)
        );
    }

    @Override
    public Map<String, TestFn> setups() {
        return Map.of(
                "cognito-token-validity", this::setupNoop
        );
    }

    @Override
    public Map<String, TestFn> teardowns() {
        return Map.of(
                "cognito-token-validity", this::teardownTokenValidity
        );
    }

    // ── cognito-token-validity ────────────────────────────────────────────────

    private void setupNoop(TestContext ctx) {}

    private void teardownTokenValidity(TestContext ctx) {
        String poolId = ctx.getString("tvPoolId");
        String clientId = ctx.getString("tvClientId");
        if (poolId != null && clientId != null)
            try { cognito().deleteUserPoolClient(r -> r.userPoolId(poolId).clientId(clientId)); } catch (Exception ignored) {}
        if (poolId != null)
            try { cognito().deleteUserPool(r -> r.userPoolId(poolId)); } catch (Exception ignored) {}
    }

    private void createClientTokenValidity(TestContext ctx) throws Exception {
        String poolName = "compat-tv-" + ctx.runId();
        var poolResp = cognito().createUserPool(r -> r.poolName(poolName));
        String poolId = poolResp.userPool().id();
        Assertions.assertNotBlank(poolId, "CreateClientTokenValidity: missing pool Id");
        ctx.set("tvPoolId", poolId);

        var resp = cognito().createUserPoolClient(r -> r
                .userPoolId(poolId)
                .clientName("compat-client-" + ctx.runId())
                .accessTokenValidity(2)
                .idTokenValidity(3)
                .refreshTokenValidity(7)
                .tokenValidityUnits(u -> u
                        .accessToken("hours")
                        .idToken("hours")
                        .refreshToken("days")));
        Assertions.assertNotBlank(resp.userPoolClient().clientId(),
                "CreateClientTokenValidity: missing ClientId");
        ctx.set("tvClientId", resp.userPoolClient().clientId());
    }

    private void describeClientTokenValidity(TestContext ctx) throws Exception {
        String poolId = ctx.getString("tvPoolId");
        String clientId = ctx.getString("tvClientId");
        Assertions.assertNotBlank(poolId, "DescribeClientTokenValidity: missing poolId");
        Assertions.assertNotBlank(clientId, "DescribeClientTokenValidity: missing clientId");
        var resp = cognito().describeUserPoolClient(r -> r.userPoolId(poolId).clientId(clientId));
        var client = resp.userPoolClient();
        Assertions.assertEquals(2, client.accessTokenValidity(), "DescribeClientTokenValidity: AccessTokenValidity");
        Assertions.assertEquals(3, client.idTokenValidity(), "DescribeClientTokenValidity: IdTokenValidity");
        Assertions.assertEquals(7, client.refreshTokenValidity(), "DescribeClientTokenValidity: RefreshTokenValidity");
        Assertions.assertNotNull(client.tokenValidityUnits(), "DescribeClientTokenValidity: TokenValidityUnits");
        Assertions.assertEquals("hours", client.tokenValidityUnits().accessTokenAsString(),
                "DescribeClientTokenValidity: TokenValidityUnits.AccessToken");
        Assertions.assertEquals("hours", client.tokenValidityUnits().idTokenAsString(),
                "DescribeClientTokenValidity: TokenValidityUnits.IdToken");
        Assertions.assertEquals("days", client.tokenValidityUnits().refreshTokenAsString(),
                "DescribeClientTokenValidity: TokenValidityUnits.RefreshToken");
    }

    private void updateClientTokenValidity(TestContext ctx) throws Exception {
        String poolId = ctx.getString("tvPoolId");
        String clientId = ctx.getString("tvClientId");
        Assertions.assertNotBlank(poolId, "UpdateClientTokenValidity: missing poolId");
        Assertions.assertNotBlank(clientId, "UpdateClientTokenValidity: missing clientId");
        cognito().updateUserPoolClient(r -> r
                .userPoolId(poolId)
                .clientId(clientId)
                .accessTokenValidity(30)
                .tokenValidityUnits(u -> u
                        .accessToken("minutes")
                        .idToken("hours")
                        .refreshToken("days")));
    }

    private void deleteUserPoolClient(TestContext ctx) throws Exception {
        String poolId = ctx.getString("tvPoolId");
        String clientId = ctx.getString("tvClientId");
        if (poolId == null || clientId == null) return;
        cognito().deleteUserPoolClient(r -> r.userPoolId(poolId).clientId(clientId));
    }
}
