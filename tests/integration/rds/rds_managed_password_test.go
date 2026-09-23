// rds_managed_password_test.go — CreateDBInstance/CreateDBCluster's
// ManageMasterUserPassword option, end to end through a real Secrets Manager
// GetSecretValue rather than the stored record: the whole point of the
// option is that a caller never sees MasterUserPassword and instead reads the
// generated value out of the secret RDS created for them.
package rds_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// smCallForRDS performs a Secrets Manager X-Amz-Target dispatch request
// against the same test server RDS is running on — mirrors
// tests/integration/secretsmanager's smCall, duplicated locally so this
// package does not have to import a _test package.
func smCallForRDS(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager."+operation)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

type rdsMasterUserSecretXML struct {
	SecretArn    string `xml:"SecretArn"`
	SecretStatus string `xml:"SecretStatus"`
	KmsKeyId     string `xml:"KmsKeyId"`
}

// CreateDBInstance with ManageMasterUserPassword=true must never be asked for
// a MasterUserPassword, must generate one valid for the engine, must create a
// Secrets Manager secret holding it, and must echo that secret's ARN on both
// the create response and DescribeDBInstances — and the secret Secrets
// Manager actually holds has to be the same password the container was
// started with.
func TestCreateDBInstance_managedMasterPassword_endToEnd(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const instanceID = "managed-password-e2e"

	cr := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier":     []string{instanceID},
		"Engine":                   []string{"mysql"},
		"MasterUsername":           []string{"admin"},
		"ManageMasterUserPassword": []string{"true"},
	})
	defer cr.Body.Close()
	assert.Equal(t, http.StatusOK, cr.StatusCode)
	createBody, err := io.ReadAll(cr.Body)
	require.NoError(t, err)

	var createOut struct {
		Instance struct {
			MasterUserSecret rdsMasterUserSecretXML `xml:"MasterUserSecret"`
		} `xml:"CreateDBInstanceResult>DBInstance"`
	}
	require.NoError(t, xml.Unmarshal(createBody, &createOut), "body: %s", createBody)
	secretArn := createOut.Instance.MasterUserSecret.SecretArn
	if secretArn == "" {
		t.Fatalf("CreateDBInstance response carries no MasterUserSecret.SecretArn; body: %s", createBody)
	}
	if createOut.Instance.MasterUserSecret.SecretStatus != "active" {
		t.Errorf("MasterUserSecret.SecretStatus = %q, want %q",
			createOut.Instance.MasterUserSecret.SecretStatus, "active")
	}

	// DescribeDBInstances has to agree — this is what a caller who did not
	// keep the create response around actually uses.
	dr := rdsQuery(t, srv, "DescribeDBInstances", url.Values{"DBInstanceIdentifier": []string{instanceID}})
	defer dr.Body.Close()
	assert.Equal(t, http.StatusOK, dr.StatusCode)
	describeBody, err := io.ReadAll(dr.Body)
	require.NoError(t, err)

	var describeOut struct {
		Instances []struct {
			MasterUserSecret rdsMasterUserSecretXML `xml:"MasterUserSecret"`
		} `xml:"DescribeDBInstancesResult>DBInstances>DBInstance"`
	}
	require.NoError(t, xml.Unmarshal(describeBody, &describeOut), "body: %s", describeBody)
	require.Len(t, describeOut.Instances, 1)
	if describeOut.Instances[0].MasterUserSecret.SecretArn != secretArn {
		t.Errorf("DescribeDBInstances MasterUserSecret.SecretArn = %q, want the create response's %q",
			describeOut.Instances[0].MasterUserSecret.SecretArn, secretArn)
	}

	// The secret itself: Secrets Manager, not the RDS record, is where a real
	// caller reads the generated password from.
	gr := smCallForRDS(t, srv, "GetSecretValue", map[string]any{"SecretId": secretArn})
	defer gr.Body.Close()
	assert.Equal(t, http.StatusOK, gr.StatusCode)
	var secretOut struct {
		SecretString string `json:"SecretString"`
	}
	require.NoError(t, json.NewDecoder(gr.Body).Decode(&secretOut))

	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	require.NoError(t, json.Unmarshal([]byte(secretOut.SecretString), &payload), "SecretString: %s", secretOut.SecretString)
	assert.Equal(t, "admin", payload.Username)
	if len(payload.Password) < 8 {
		t.Errorf("generated password %q is shorter than RDS's own minimum", payload.Password)
	}
	// RDS forbids these four characters in any master password — the
	// generated one has to obey its own rule.
	if strings.ContainsAny(payload.Password, `/"@ `) {
		t.Errorf("generated password %q contains a character RDS forbids", payload.Password)
	}

	// And it is the password the record — and so the container — actually
	// has: rds_test.go's rdsStoredMasterPassword-style check, inline.
	if got := rdsStoredPassword(t, srv, instanceID); got != payload.Password {
		t.Errorf("stored MasterUserPassword = %q, want it to match the secret's password %q", got, payload.Password)
	}
}

// Supplying MasterUserPassword and ManageMasterUserPassword=true together is
// a contradiction AWS refuses outright.
func TestCreateDBInstance_managedMasterPassword_combinationError(t *testing.T) {
	srv := helpers.NewTestServer(t)

	cr := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier":     []string{"managed-password-conflict"},
		"Engine":                   []string{"mysql"},
		"MasterUsername":           []string{"admin"},
		"MasterUserPassword":       []string{"explicit-password-1"},
		"ManageMasterUserPassword": []string{"true"},
	})
	defer cr.Body.Close()
	assert.Equal(t, http.StatusBadRequest, cr.StatusCode)
	body, err := io.ReadAll(cr.Body)
	require.NoError(t, err)
	if !strings.Contains(string(body), "InvalidParameterCombination") {
		t.Errorf("expected InvalidParameterCombination, got: %s", body)
	}
}

// The cluster path mints its own "rds!cluster-…" secret rather than reusing
// the instance naming scheme, and a member instance created under the
// cluster must report the cluster's secret rather than minting a second one.
func TestCreateDBCluster_managedMasterPassword_endToEnd(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const clusterID = "managed-password-cluster"

	cr := rdsQuery(t, srv, "CreateDBCluster", url.Values{
		"DBClusterIdentifier":      []string{clusterID},
		"Engine":                   []string{"aurora-mysql"},
		"MasterUsername":           []string{"admin"},
		"ManageMasterUserPassword": []string{"true"},
	})
	defer cr.Body.Close()
	assert.Equal(t, http.StatusOK, cr.StatusCode)
	body, err := io.ReadAll(cr.Body)
	require.NoError(t, err)

	var out struct {
		Cluster struct {
			MasterUserSecret rdsMasterUserSecretXML `xml:"MasterUserSecret"`
		} `xml:"CreateDBClusterResult>DBCluster"`
	}
	require.NoError(t, xml.Unmarshal(body, &out), "body: %s", body)
	secretArn := out.Cluster.MasterUserSecret.SecretArn
	if secretArn == "" {
		t.Fatalf("CreateDBCluster response carries no MasterUserSecret.SecretArn; body: %s", body)
	}
	if !strings.Contains(secretArn, ":secret:rds!cluster-") {
		t.Errorf("secret ARN %q does not follow AWS's rds!cluster-<uuid> naming", secretArn)
	}

	gr := smCallForRDS(t, srv, "GetSecretValue", map[string]any{"SecretId": secretArn})
	defer gr.Body.Close()
	assert.Equal(t, http.StatusOK, gr.StatusCode)
}

// rdsStoredPassword reads MasterUserPassword straight off the stored
// DBInstance record — DescribeDBInstances never returns it, managed or not.
func rdsStoredPassword(t *testing.T, srv *helpers.TestServer, instanceID string) string {
	t.Helper()
	raw, ok, err := srv.Store.Get(t.Context(), "rds:instances", serviceutil.RegionKey(srv.Config.Region, instanceID))
	if err != nil || !ok {
		t.Fatalf("read stored DB instance %s: ok=%v err=%v", instanceID, ok, err)
	}
	var inst struct {
		MasterUserPassword string `json:"MasterUserPassword"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &inst))
	return inst.MasterUserPassword
}
