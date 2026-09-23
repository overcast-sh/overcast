// Package rds_test contains integration tests for the RDS emulator.
//
// Run: go test ./tests/integration/rds/...
package rds_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// rdsQuery sends an RDS Query protocol request.
func rdsQuery(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", "2014-10-31")
	body := strings.NewReader(params.Encode())
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", body)
	if err != nil {
		t.Fatalf("rdsQuery: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("rdsQuery: do: %v", err)
	}
	return resp
}

func ec2QueryForRDS(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", "2016-11-15")
	body := strings.NewReader(params.Encode())
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", body)
	if err != nil {
		t.Fatalf("ec2Query: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ec2Query: do: %v", err)
	}
	return resp
}

func createVpcForRDS(t *testing.T, srv *helpers.TestServer, cidr string) string {
	t.Helper()
	resp := ec2QueryForRDS(t, srv, "CreateVpc", url.Values{"CidrBlock": []string{cidr}})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out struct {
		Vpc struct {
			VpcID string `xml:"vpcId"`
		} `xml:"vpc"`
	}
	require.NoError(t, xml.Unmarshal(b, &out), "body: %s", b)
	return out.Vpc.VpcID
}

func createSubnetForRDS(t *testing.T, srv *helpers.TestServer, vpcID, cidr string) string {
	t.Helper()
	resp := ec2QueryForRDS(t, srv, "CreateSubnet", url.Values{
		"VpcId":     []string{vpcID},
		"CidrBlock": []string{cidr},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out struct {
		Subnet struct {
			SubnetID string `xml:"subnetId"`
		} `xml:"subnet"`
	}
	require.NoError(t, xml.Unmarshal(b, &out), "body: %s", b)
	return out.Subnet.SubnetID
}

func decodeXML(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("decodeXML: read: %v", err)
	}
	if err := xml.Unmarshal(b, dst); err != nil {
		t.Fatalf("decodeXML: unmarshal %T: %v\nbody: %s", dst, err, b)
	}
}

// assertQueryXMLError decodes a Query-protocol XML error and checks the code.
func assertQueryXMLError(t *testing.T, resp *http.Response, expectedCode string) {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var errResp struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Error   struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	require.NoError(t, xml.Unmarshal(b, &errResp), "body: %s", b)
	assert.Equal(t, expectedCode, errResp.Error.Code)
}

// ─── CreateDBInstance ─────────────────────────────────────────────────────────

func TestCreateDBInstance_success(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBInstance is called with valid MySQL params
	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"test-db"},
		"DBInstanceClass":      []string{"db.t3.micro"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
		"AllocatedStorage":     []string{"20"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with instance in "creating" state
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"CreateDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
				DBInstanceClass      string `xml:"DBInstanceClass"`
				Engine               string `xml:"Engine"`
				EngineVersion        string `xml:"EngineVersion"`
				DBInstanceStatus     string `xml:"DBInstanceStatus"`
				MasterUsername       string `xml:"MasterUsername"`
				MasterUserPassword   string `xml:"MasterUserPassword"`
				AllocatedStorage     int    `xml:"AllocatedStorage"`
				StorageType          string `xml:"StorageType"`
				MultiAZ              bool   `xml:"MultiAZ"`
				PubliclyAccessible   bool   `xml:"PubliclyAccessible"`
				DBInstanceArn        string `xml:"DBInstanceArn"`
				Endpoint             struct {
					Address string `xml:"Address"`
					Port    int    `xml:"Port"`
				} `xml:"Endpoint"`
			} `xml:"DBInstance"`
		} `xml:"CreateDBInstanceResult"`
	}
	decodeXML(t, resp, &result)

	inst := result.Result.DBInstance
	assert.Equal(t, "test-db", inst.DBInstanceIdentifier)
	assert.Equal(t, "db.t3.micro", inst.DBInstanceClass)
	assert.Equal(t, "mysql", inst.Engine)
	assert.Equal(t, "8.0", inst.EngineVersion)
	assert.Equal(t, "creating", inst.DBInstanceStatus)
	assert.Equal(t, "admin", inst.MasterUsername)
	assert.Empty(t, inst.MasterUserPassword, "MasterUserPassword must not be returned")
	assert.Equal(t, 20, inst.AllocatedStorage)
	assert.Equal(t, "gp2", inst.StorageType)
	assert.Equal(t, false, inst.MultiAZ)
	// No DB subnet group: the default VPC, which has an internet gateway, so
	// AWS calls this public and so does Overcast.
	assert.True(t, inst.PubliclyAccessible)
	assert.Equal(t, 3306, inst.Endpoint.Port)
	assert.Contains(t, inst.Endpoint.Address, "test-db")
	assert.NotEmpty(t, inst.DBInstanceArn)
	assert.Contains(t, inst.DBInstanceArn, "rds")
}

func TestCreateDBInstance_postgres(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBInstance is called with PostgreSQL engine
	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"pg-db"},
		"Engine":               []string{"postgres"},
		"MasterUsername":       []string{"pgadmin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with postgres defaults (port 5432, version 16.1)
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"CreateDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				Engine        string `xml:"Engine"`
				EngineVersion string `xml:"EngineVersion"`
				Endpoint      struct {
					Port int `xml:"Port"`
				} `xml:"Endpoint"`
				DBInstanceClass  string `xml:"DBInstanceClass"`
				AllocatedStorage int    `xml:"AllocatedStorage"`
				StorageType      string `xml:"StorageType"`
			} `xml:"DBInstance"`
		} `xml:"CreateDBInstanceResult"`
	}
	decodeXML(t, resp, &result)

	inst := result.Result.DBInstance
	assert.Equal(t, "postgres", inst.Engine)
	assert.Equal(t, "16.1", inst.EngineVersion)
	assert.Equal(t, 5432, inst.Endpoint.Port)
	assert.Equal(t, "db.t3.micro", inst.DBInstanceClass)
	assert.Equal(t, 20, inst.AllocatedStorage)
	assert.Equal(t, "gp2", inst.StorageType)
}

func TestCreateDBInstance_missingEngine(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBInstance is called without Engine
	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"no-engine"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	defer resp.Body.Close()

	// Then: 400 error with InvalidParameterValue
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

func TestCreateDBInstance_invalidMasterUsernameAndDatabaseName(t *testing.T) {
	tests := []struct {
		name   string
		values url.Values
	}{
		{
			name: "master username",
			values: url.Values{
				"DBInstanceIdentifier": {"invalid-user"},
				"Engine":               {"mysql"},
				"MasterUsername":       {"1admin"},
				"MasterUserPassword":   {"Password1!"},
			},
		},
		{
			name: "database name",
			values: url.Values{
				"DBInstanceIdentifier": {"invalid-database"},
				"Engine":               {"postgres"},
				"MasterUsername":       {"admin"},
				"MasterUserPassword":   {"Password1!"},
				"DBName":               {"app-database"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a fresh RDS endpoint.
			srv := helpers.NewTestServer(t)

			// When: CreateDBInstance receives an AWS-invalid account or database name.
			resp := rdsQuery(t, srv, "CreateDBInstance", tc.values)
			defer resp.Body.Close()

			// Then: the Query API rejects it synchronously with an AWS error envelope.
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			assertQueryXMLError(t, resp, "InvalidParameterValue")
			helpers.AssertRequestID(t, resp)
		})
	}
}

func TestCreateDBInstance_duplicate(t *testing.T) {
	// Given: an existing DB instance
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"dup-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)

	// When: CreateDBInstance is called with the same identifier
	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"dup-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	defer resp.Body.Close()

	// Then: 400 error with DBInstanceAlreadyExists
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBInstanceAlreadyExists")
}

// TestCreateDBInstance_additionalStorageVolumesRejected covers #2041.
//
// AWS's CreateDBInstanceMessage.AdditionalStorageVolumes documents itself as
// "supported for RDS for Oracle and RDS for SQL Server DB instances only" —
// see the pinned rds-2014-10-31.json model. Overcast never emulates either
// engine (docs/services/rds/limitations.md), so every engine it can actually
// create is one AWS itself would refuse the parameter for. Rejecting it here
// is the AWS-faithful behaviour; silently accepting and echoing it back would
// invent support Overcast cannot provide (no Oracle/SQL Server container ever
// runs).
func TestCreateDBInstance_additionalStorageVolumesRejected(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBInstance is called with AdditionalStorageVolumes on a
	// supported (non-Oracle/SQL Server) engine
	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"asv-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
		"AdditionalStorageVolumes.member.1.VolumeName":       []string{"RDSDBDATA2"},
		"AdditionalStorageVolumes.member.1.AllocatedStorage": []string{"100"},
	})
	defer resp.Body.Close()

	// Then: 400 error with InvalidParameterCombination, and no instance was
	// created
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterCombination")

	describeResp := rdsQuery(t, srv, "DescribeDBInstances", url.Values{
		"DBInstanceIdentifier": []string{"asv-db"},
	})
	defer describeResp.Body.Close()
	helpers.AssertStatus(t, describeResp, http.StatusBadRequest)
	assertQueryXMLError(t, describeResp, "DBInstanceNotFound")
}

func TestCreateDBInstance_unbackedSubnetGroupRejected(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithEC2VPCStrategy("shared"))
	vpcID := createVpcForRDS(t, srv, "10.55.0.0/16")
	subnetID := createSubnetForRDS(t, srv, vpcID, "10.55.1.0/24")

	sgResp := rdsQuery(t, srv, "CreateDBSubnetGroup", url.Values{
		"DBSubnetGroupName":        []string{"db-subnets"},
		"DBSubnetGroupDescription": []string{"test subnet group"},
		"SubnetIds.member.1":       []string{subnetID},
	})
	defer sgResp.Body.Close()
	assert.Equal(t, http.StatusOK, sgResp.StatusCode)

	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"test-db"},
		"DBInstanceClass":      []string{"db.t3.micro"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"password123"},
		"DBSubnetGroupName":    []string{"db-subnets"},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var errResp struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Error   struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	require.NoError(t, xml.Unmarshal(b, &errResp), "body: %s", b)
	assert.Equal(t, "InvalidVPCNetworkStateFault", errResp.Error.Code)
	assert.Contains(t, errResp.Error.Message, "unbacked")
}

// ─── DescribeDBInstances ──────────────────────────────────────────────────────

func TestDescribeDBInstances_success(t *testing.T) {
	// Given: a DB instance exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"desc-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	resp1.Body.Close()

	// When: DescribeDBInstances is called without filters
	resp := rdsQuery(t, srv, "DescribeDBInstances", nil)
	defer resp.Body.Close()

	// Then: 200 OK with the instance in the list
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBInstancesResponse"`
		Result  struct {
			DBInstances struct {
				Items []struct {
					DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
					Engine               string `xml:"Engine"`
					MasterUserPassword   string `xml:"MasterUserPassword"`
				} `xml:"DBInstance"`
			} `xml:"DBInstances"`
		} `xml:"DescribeDBInstancesResult"`
	}
	decodeXML(t, resp, &result)

	require.Len(t, result.Result.DBInstances.Items, 1)
	assert.Equal(t, "desc-db", result.Result.DBInstances.Items[0].DBInstanceIdentifier)
	assert.Equal(t, "mysql", result.Result.DBInstances.Items[0].Engine)
	assert.Empty(t, result.Result.DBInstances.Items[0].MasterUserPassword, "MasterUserPassword must not be returned")
}

// TestDescribeDBInstances_storageOperationStatusOmittedAtRest covers #2041.
//
// AWS's DBInstance.StorageOperationStatus and .StorageOperationPercentProgress
// document themselves as appearing "only while a storage operation is in
// progress" and absent otherwise — see the pinned rds-2014-10-31.json model.
// Overcast performs no asynchronous storage operation (PITR/snapshot restore,
// read-replica creation, blue/green deployment, Single-AZ->Multi-AZ
// conversion, storage scaling) at all, so a resting instance's AWS-faithful
// response omits both elements entirely rather than inventing an
// always-"completed"/100 state.
func TestDescribeDBInstances_storageOperationStatusOmittedAtRest(t *testing.T) {
	// Given: a DB instance exists
	srv := helpers.NewTestServer(t)
	createResp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"storage-op-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	createBody, err := io.ReadAll(createResp.Body)
	require.NoError(t, err)
	assert.NotContains(t, string(createBody), "StorageOperationStatus")
	assert.NotContains(t, string(createBody), "StorageOperationPercentProgress")

	// When: DescribeDBInstances is called for it
	resp := rdsQuery(t, srv, "DescribeDBInstances", url.Values{
		"DBInstanceIdentifier": []string{"storage-op-db"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Then: neither element is present on the wire — AWS's own resting value
	assert.NotContains(t, string(body), "StorageOperationStatus")
	assert.NotContains(t, string(body), "StorageOperationPercentProgress")
}

func TestDescribeDBInstances_byId(t *testing.T) {
	// Given: two DB instances exist
	srv := helpers.NewTestServer(t)
	for _, id := range []string{"db-one", "db-two"} {
		resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
			"DBInstanceIdentifier": []string{id},
			"Engine":               []string{"mysql"},
			"MasterUsername":       []string{"admin"},
			"MasterUserPassword":   []string{"Password1!"},
		})
		resp.Body.Close()
	}

	// When: DescribeDBInstances is called with a specific ID
	resp := rdsQuery(t, srv, "DescribeDBInstances", url.Values{
		"DBInstanceIdentifier": []string{"db-one"},
	})
	defer resp.Body.Close()

	// Then: only the requested instance is returned
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBInstancesResponse"`
		Result  struct {
			DBInstances struct {
				Items []struct {
					DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
				} `xml:"DBInstance"`
			} `xml:"DBInstances"`
		} `xml:"DescribeDBInstancesResult"`
	}
	decodeXML(t, resp, &result)

	require.Len(t, result.Result.DBInstances.Items, 1)
	assert.Equal(t, "db-one", result.Result.DBInstances.Items[0].DBInstanceIdentifier)
}

func TestDescribeDBInstances_notFound(t *testing.T) {
	// Given: no DB instances exist
	srv := helpers.NewTestServer(t)

	// When: DescribeDBInstances is called with a non-existent ID
	resp := rdsQuery(t, srv, "DescribeDBInstances", url.Values{
		"DBInstanceIdentifier": []string{"nope"},
	})
	defer resp.Body.Close()

	// Then: 400 error with DBInstanceNotFound
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBInstanceNotFound")
}

// ─── DeleteDBInstance ─────────────────────────────────────────────────────────

func TestDeleteDBInstance_success(t *testing.T) {
	// Given: a DB instance exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"del-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	resp1.Body.Close()

	// When: DeleteDBInstance is called
	resp := rdsQuery(t, srv, "DeleteDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"del-db"},
		"SkipFinalSnapshot":    []string{"true"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with instance in "deleting" state
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DeleteDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
				DBInstanceStatus     string `xml:"DBInstanceStatus"`
			} `xml:"DBInstance"`
		} `xml:"DeleteDBInstanceResult"`
	}
	decodeXML(t, resp, &result)

	assert.Equal(t, "del-db", result.Result.DBInstance.DBInstanceIdentifier)
	assert.Equal(t, "deleting", result.Result.DBInstance.DBInstanceStatus)
}

func TestDeleteDBInstance_notFound(t *testing.T) {
	// Given: no DB instances exist
	srv := helpers.NewTestServer(t)

	// When: DeleteDBInstance is called for a non-existent ID
	resp := rdsQuery(t, srv, "DeleteDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"nope"},
		"SkipFinalSnapshot":    []string{"true"},
	})
	defer resp.Body.Close()

	// Then: 400 error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBInstanceNotFound")
}

// ─── DescribeDBEngineVersions ─────────────────────────────────────────────────

func TestDescribeDBEngineVersions_success(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: DescribeDBEngineVersions is called without filters
	resp := rdsQuery(t, srv, "DescribeDBEngineVersions", nil)
	defer resp.Body.Close()

	// Then: 200 OK with multiple engine versions
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBEngineVersionsResponse"`
		Result  struct {
			DBEngineVersions struct {
				Items []struct {
					Engine        string `xml:"Engine"`
					EngineVersion string `xml:"EngineVersion"`
				} `xml:"DBEngineVersion"`
			} `xml:"DBEngineVersions"`
		} `xml:"DescribeDBEngineVersionsResult"`
	}
	decodeXML(t, resp, &result)

	versions := result.Result.DBEngineVersions.Items
	assert.GreaterOrEqual(t, len(versions), 3, "should have at least 3 engine versions")

	// Verify multiple engines are present.
	engines := make(map[string]bool)
	for _, v := range versions {
		engines[v.Engine] = true
	}
	assert.True(t, engines["mysql"], "should include mysql")
	assert.True(t, engines["postgres"], "should include postgres")
	assert.True(t, engines["mariadb"], "should include mariadb")
}

func TestDescribeDBEngineVersions_filterByEngine(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: DescribeDBEngineVersions is called with Engine=mysql
	resp := rdsQuery(t, srv, "DescribeDBEngineVersions", url.Values{
		"Engine": []string{"mysql"},
	})
	defer resp.Body.Close()

	// Then: only MySQL versions are returned
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBEngineVersionsResponse"`
		Result  struct {
			DBEngineVersions struct {
				Items []struct {
					Engine        string `xml:"Engine"`
					EngineVersion string `xml:"EngineVersion"`
				} `xml:"DBEngineVersion"`
			} `xml:"DBEngineVersions"`
		} `xml:"DescribeDBEngineVersionsResult"`
	}
	decodeXML(t, resp, &result)

	versions := result.Result.DBEngineVersions.Items
	assert.GreaterOrEqual(t, len(versions), 1)
	for _, v := range versions {
		assert.Equal(t, "mysql", v.Engine, "all returned versions should be mysql")
	}
}

// ─── CreateDBSubnetGroup ──────────────────────────────────────────────────────

func TestCreateDBSubnetGroup_success(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBSubnetGroup is called with valid params
	resp := rdsQuery(t, srv, "CreateDBSubnetGroup", url.Values{
		"DBSubnetGroupName":        []string{"test-subnet-group"},
		"DBSubnetGroupDescription": []string{"Test subnet group"},
		"SubnetIds.member.1":       []string{"subnet-12345678"},
		"SubnetIds.member.2":       []string{"subnet-87654321"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with subnet group details
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"CreateDBSubnetGroupResponse"`
		Result  struct {
			DBSubnetGroup struct {
				DBSubnetGroupName        string `xml:"DBSubnetGroupName"`
				DBSubnetGroupDescription string `xml:"DBSubnetGroupDescription"`
				DBSubnetGroupArn         string `xml:"DBSubnetGroupArn"`
				SubnetGroupStatus        string `xml:"SubnetGroupStatus"`
				Subnets                  struct {
					Items []struct {
						SubnetIdentifier string `xml:"SubnetIdentifier"`
					} `xml:"Subnet"`
				} `xml:"Subnets"`
			} `xml:"DBSubnetGroup"`
		} `xml:"CreateDBSubnetGroupResult"`
	}
	decodeXML(t, resp, &result)

	sg := result.Result.DBSubnetGroup
	assert.Equal(t, "test-subnet-group", sg.DBSubnetGroupName)
	assert.Equal(t, "Test subnet group", sg.DBSubnetGroupDescription)
	assert.Equal(t, "Complete", sg.SubnetGroupStatus)
	assert.NotEmpty(t, sg.DBSubnetGroupArn)
	require.Len(t, sg.Subnets.Items, 2)
	assert.Equal(t, "subnet-12345678", sg.Subnets.Items[0].SubnetIdentifier)
	assert.Equal(t, "subnet-87654321", sg.Subnets.Items[1].SubnetIdentifier)
}

func TestCreateDBSubnetGroup_duplicate(t *testing.T) {
	// Given: a subnet group already exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBSubnetGroup", url.Values{
		"DBSubnetGroupName":        []string{"dup-group"},
		"DBSubnetGroupDescription": []string{"First"},
		"SubnetIds.member.1":       []string{"subnet-111"},
	})
	resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)

	// When: CreateDBSubnetGroup is called with the same name
	resp := rdsQuery(t, srv, "CreateDBSubnetGroup", url.Values{
		"DBSubnetGroupName":        []string{"dup-group"},
		"DBSubnetGroupDescription": []string{"Second"},
		"SubnetIds.member.1":       []string{"subnet-222"},
	})
	defer resp.Body.Close()

	// Then: 400 error with DBSubnetGroupAlreadyExistsFault
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBSubnetGroupAlreadyExistsFault")
}

// ─── DescribeDBSubnetGroups ───────────────────────────────────────────────────

func TestDescribeDBSubnetGroups_success(t *testing.T) {
	// Given: two subnet groups exist
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"group-a", "group-b"} {
		resp := rdsQuery(t, srv, "CreateDBSubnetGroup", url.Values{
			"DBSubnetGroupName":        []string{name},
			"DBSubnetGroupDescription": []string{"desc-" + name},
			"SubnetIds.member.1":       []string{"subnet-aaa"},
		})
		resp.Body.Close()
	}

	// When: DescribeDBSubnetGroups is called without filters
	resp := rdsQuery(t, srv, "DescribeDBSubnetGroups", nil)
	defer resp.Body.Close()

	// Then: 200 OK with both groups
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBSubnetGroupsResponse"`
		Result  struct {
			DBSubnetGroups struct {
				Items []struct {
					DBSubnetGroupName string `xml:"DBSubnetGroupName"`
				} `xml:"DBSubnetGroup"`
			} `xml:"DBSubnetGroups"`
		} `xml:"DescribeDBSubnetGroupsResult"`
	}
	decodeXML(t, resp, &result)

	assert.Len(t, result.Result.DBSubnetGroups.Items, 2)
}

func TestDescribeDBSubnetGroups_byName(t *testing.T) {
	// Given: a subnet group exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBSubnetGroup", url.Values{
		"DBSubnetGroupName":        []string{"specific-group"},
		"DBSubnetGroupDescription": []string{"Specific"},
		"SubnetIds.member.1":       []string{"subnet-abc"},
	})
	resp1.Body.Close()

	// When: DescribeDBSubnetGroups is called with a specific name
	resp := rdsQuery(t, srv, "DescribeDBSubnetGroups", url.Values{
		"DBSubnetGroupName": []string{"specific-group"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with only that group
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBSubnetGroupsResponse"`
		Result  struct {
			DBSubnetGroups struct {
				Items []struct {
					DBSubnetGroupName string `xml:"DBSubnetGroupName"`
				} `xml:"DBSubnetGroup"`
			} `xml:"DBSubnetGroups"`
		} `xml:"DescribeDBSubnetGroupsResult"`
	}
	decodeXML(t, resp, &result)

	require.Len(t, result.Result.DBSubnetGroups.Items, 1)
	assert.Equal(t, "specific-group", result.Result.DBSubnetGroups.Items[0].DBSubnetGroupName)
}

func TestDescribeDBSubnetGroups_notFound(t *testing.T) {
	// Given: no subnet groups exist
	srv := helpers.NewTestServer(t)

	// When: DescribeDBSubnetGroups is called with a non-existent name
	resp := rdsQuery(t, srv, "DescribeDBSubnetGroups", url.Values{
		"DBSubnetGroupName": []string{"nope"},
	})
	defer resp.Body.Close()

	// Then: 404 with DBSubnetGroupNotFoundFault, the status AWS documents for
	// this fault on every operation that raises it.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "DBSubnetGroupNotFoundFault")
}

// ─── DeleteDBSubnetGroup ──────────────────────────────────────────────────────

func TestDeleteDBSubnetGroup_success(t *testing.T) {
	// Given: a subnet group exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBSubnetGroup", url.Values{
		"DBSubnetGroupName":        []string{"del-group"},
		"DBSubnetGroupDescription": []string{"To delete"},
		"SubnetIds.member.1":       []string{"subnet-del"},
	})
	resp1.Body.Close()

	// When: DeleteDBSubnetGroup is called
	resp := rdsQuery(t, srv, "DeleteDBSubnetGroup", url.Values{
		"DBSubnetGroupName": []string{"del-group"},
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: the group no longer exists
	resp2 := rdsQuery(t, srv, "DescribeDBSubnetGroups", url.Values{
		"DBSubnetGroupName": []string{"del-group"},
	})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

func TestDeleteDBSubnetGroup_notFound(t *testing.T) {
	// Given: no subnet groups exist
	srv := helpers.NewTestServer(t)

	// When: DeleteDBSubnetGroup is called for a non-existent name
	resp := rdsQuery(t, srv, "DeleteDBSubnetGroup", url.Values{
		"DBSubnetGroupName": []string{"nope"},
	})
	defer resp.Body.Close()

	// Then: 404, per AWS's documented status for DBSubnetGroupNotFoundFault.
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	assertQueryXMLError(t, resp, "DBSubnetGroupNotFoundFault")
}

// ─── StopDBInstance ───────────────────────────────────────────────────────────

func TestStopDBInstance_success(t *testing.T) {
	// Given: an available DB instance
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"stop-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	resp1.Body.Close()

	// Advance time to transition creating → available
	srv.AdvanceClock(1 * time.Second)

	// When: StopDBInstance is called
	resp := rdsQuery(t, srv, "StopDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"stop-db"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with instance in "stopping" state
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"StopDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
				DBInstanceStatus     string `xml:"DBInstanceStatus"`
			} `xml:"DBInstance"`
		} `xml:"StopDBInstanceResult"`
	}
	decodeXML(t, resp, &result)

	assert.Equal(t, "stop-db", result.Result.DBInstance.DBInstanceIdentifier)
	assert.Equal(t, "stopping", result.Result.DBInstance.DBInstanceStatus)

	// Advance time for stopping → stopped transition
	srv.AdvanceClock(1 * time.Second)

	// Verify final state is "stopped"
	descResp := rdsQuery(t, srv, "DescribeDBInstances", url.Values{
		"DBInstanceIdentifier": []string{"stop-db"},
	})
	defer descResp.Body.Close()

	var descResult struct {
		XMLName xml.Name `xml:"DescribeDBInstancesResponse"`
		Result  struct {
			DBInstances struct {
				Items []struct {
					DBInstanceStatus string `xml:"DBInstanceStatus"`
				} `xml:"DBInstance"`
			} `xml:"DBInstances"`
		} `xml:"DescribeDBInstancesResult"`
	}
	decodeXML(t, descResp, &descResult)
	require.Len(t, descResult.Result.DBInstances.Items, 1)
	assert.Equal(t, "stopped", descResult.Result.DBInstances.Items[0].DBInstanceStatus)
}

func TestStopDBInstance_notAvailable(t *testing.T) {
	// Given: a DB instance in "creating" state
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"not-avail-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	resp1.Body.Close()

	// When: StopDBInstance is called while in "creating" state (mock clock not
	// advanced, so the creating→available transition has not fired yet)
	resp := rdsQuery(t, srv, "StopDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"not-avail-db"},
	})
	defer resp.Body.Close()

	// Then: 400 error with InvalidDBInstanceState
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidDBInstanceState")
}

// ─── StartDBInstance ──────────────────────────────────────────────────────────

func TestStartDBInstance_success(t *testing.T) {
	// Given: a stopped DB instance
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"start-db"},
		"Engine":               []string{"postgres"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	resp1.Body.Close()

	// creating → available
	srv.AdvanceClock(1 * time.Second)

	// Stop the instance
	resp2 := rdsQuery(t, srv, "StopDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"start-db"},
	})
	resp2.Body.Close()

	// stopping → stopped
	srv.AdvanceClock(1 * time.Second)

	// When: StartDBInstance is called
	resp := rdsQuery(t, srv, "StartDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"start-db"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with instance in "starting" state
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"StartDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
				DBInstanceStatus     string `xml:"DBInstanceStatus"`
			} `xml:"DBInstance"`
		} `xml:"StartDBInstanceResult"`
	}
	decodeXML(t, resp, &result)

	assert.Equal(t, "start-db", result.Result.DBInstance.DBInstanceIdentifier)
	assert.Equal(t, "starting", result.Result.DBInstance.DBInstanceStatus)

	// Advance time for starting → available transition
	srv.AdvanceClock(1 * time.Second)

	// Verify final state is "available"
	descResp := rdsQuery(t, srv, "DescribeDBInstances", url.Values{
		"DBInstanceIdentifier": []string{"start-db"},
	})
	defer descResp.Body.Close()

	var descResult struct {
		XMLName xml.Name `xml:"DescribeDBInstancesResponse"`
		Result  struct {
			DBInstances struct {
				Items []struct {
					DBInstanceStatus string `xml:"DBInstanceStatus"`
				} `xml:"DBInstance"`
			} `xml:"DBInstances"`
		} `xml:"DescribeDBInstancesResult"`
	}
	decodeXML(t, descResp, &descResult)
	require.Len(t, descResult.Result.DBInstances.Items, 1)
	assert.Equal(t, "available", descResult.Result.DBInstances.Items[0].DBInstanceStatus)
}

func TestStartDBInstance_notStopped(t *testing.T) {
	// Given: an available DB instance (not stopped)
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"not-stopped-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	resp1.Body.Close()
	srv.AdvanceClock(1 * time.Second)

	// When: StartDBInstance is called on an available instance
	resp := rdsQuery(t, srv, "StartDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"not-stopped-db"},
	})
	defer resp.Body.Close()

	// Then: 400 error with InvalidDBInstanceState
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidDBInstanceState")
}

// ─── ModifyDBInstance ─────────────────────────────────────────────────────────

func TestModifyDBInstance_success(t *testing.T) {
	// Given: an available DB instance
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"mod-db"},
		"Engine":               []string{"mysql"},
		"DBInstanceClass":      []string{"db.t3.micro"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
		"AllocatedStorage":     []string{"20"},
	})
	resp1.Body.Close()
	srv.AdvanceClock(1 * time.Second)

	// When: ModifyDBInstance is called with new class and storage
	resp := rdsQuery(t, srv, "ModifyDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"mod-db"},
		"DBInstanceClass":      []string{"db.r5.large"},
		"AllocatedStorage":     []string{"100"},
		"StorageType":          []string{"io1"},
		"MultiAZ":              []string{"true"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with updated values
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"ModifyDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
				DBInstanceClass      string `xml:"DBInstanceClass"`
				DBInstanceStatus     string `xml:"DBInstanceStatus"`
				AllocatedStorage     int    `xml:"AllocatedStorage"`
				StorageType          string `xml:"StorageType"`
				MultiAZ              bool   `xml:"MultiAZ"`
			} `xml:"DBInstance"`
		} `xml:"ModifyDBInstanceResult"`
	}
	decodeXML(t, resp, &result)

	inst := result.Result.DBInstance
	assert.Equal(t, "mod-db", inst.DBInstanceIdentifier)
	assert.Equal(t, "db.r5.large", inst.DBInstanceClass)
	assert.Equal(t, "modifying", inst.DBInstanceStatus)
	assert.Equal(t, 100, inst.AllocatedStorage)
	assert.Equal(t, "io1", inst.StorageType)
	assert.True(t, inst.MultiAZ)

	// Advance time for modifying → available transition
	srv.AdvanceClock(1 * time.Second)

	// Verify final state is "available"
	descResp := rdsQuery(t, srv, "DescribeDBInstances", url.Values{
		"DBInstanceIdentifier": []string{"mod-db"},
	})
	defer descResp.Body.Close()

	var descResult struct {
		XMLName xml.Name `xml:"DescribeDBInstancesResponse"`
		Result  struct {
			DBInstances struct {
				Items []struct {
					DBInstanceStatus string `xml:"DBInstanceStatus"`
					DBInstanceClass  string `xml:"DBInstanceClass"`
				} `xml:"DBInstance"`
			} `xml:"DBInstances"`
		} `xml:"DescribeDBInstancesResult"`
	}
	decodeXML(t, descResp, &descResult)
	require.Len(t, descResult.Result.DBInstances.Items, 1)
	assert.Equal(t, "available", descResult.Result.DBInstances.Items[0].DBInstanceStatus)
	assert.Equal(t, "db.r5.large", descResult.Result.DBInstances.Items[0].DBInstanceClass)
}

// PubliclyAccessible is what a user reaches for when a database inside a VPC
// still has to be dialable from outside it, so it has to be settable at create,
// reported on describe, and changeable afterwards — over the wire, through the
// whole server, exactly as an SDK would drive it.
func TestPubliclyAccessible_defaultsAndRoundTripsOverTheWire(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithMockClock())

	sgResp := rdsQuery(t, srv, "CreateDBSubnetGroup", url.Values{
		"DBSubnetGroupName":        []string{"private-subnets"},
		"DBSubnetGroupDescription": []string{"a chosen placement"},
		"SubnetIds.member.1":       []string{"subnet-11111111"},
	})
	sgResp.Body.Close()
	helpers.AssertStatus(t, sgResp, http.StatusOK)

	// A named subnet group is a chosen placement, and AWS makes that private
	// unless asked otherwise.
	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"pa-private"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
		"DBSubnetGroupName":    []string{"private-subnets"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var created struct {
		XMLName xml.Name `xml:"CreateDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				PubliclyAccessible bool `xml:"PubliclyAccessible"`
			} `xml:"DBInstance"`
		} `xml:"CreateDBInstanceResult"`
	}
	decodeXML(t, resp, &created)
	assert.False(t, created.Result.DBInstance.PubliclyAccessible,
		"an instance in a named DB subnet group defaulted to public")

	srv.AdvanceClock(1 * time.Second)

	// And the escape hatch: turn it back on, and it stays on.
	modResp := rdsQuery(t, srv, "ModifyDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"pa-private"},
		"PubliclyAccessible":   []string{"true"},
	})
	defer modResp.Body.Close()
	helpers.AssertStatus(t, modResp, http.StatusOK)

	var modified struct {
		XMLName xml.Name `xml:"ModifyDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				PubliclyAccessible bool `xml:"PubliclyAccessible"`
			} `xml:"DBInstance"`
		} `xml:"ModifyDBInstanceResult"`
	}
	decodeXML(t, modResp, &modified)
	assert.True(t, modified.Result.DBInstance.PubliclyAccessible,
		"ModifyDBInstance did not report the instance as publicly accessible")

	srv.AdvanceClock(1 * time.Second)

	descResp := rdsQuery(t, srv, "DescribeDBInstances", url.Values{
		"DBInstanceIdentifier": []string{"pa-private"},
	})
	defer descResp.Body.Close()
	helpers.AssertStatus(t, descResp, http.StatusOK)

	var described struct {
		XMLName xml.Name `xml:"DescribeDBInstancesResponse"`
		Result  struct {
			DBInstances struct {
				Items []struct {
					PubliclyAccessible bool `xml:"PubliclyAccessible"`
				} `xml:"DBInstance"`
			} `xml:"DBInstances"`
		} `xml:"DescribeDBInstancesResult"`
	}
	decodeXML(t, descResp, &described)
	require.Len(t, described.Result.DBInstances.Items, 1)
	assert.True(t, described.Result.DBInstances.Items[0].PubliclyAccessible,
		"the modification did not survive to the next describe")
}

func TestModifyDBInstance_notFound(t *testing.T) {
	// Given: no DB instances exist
	srv := helpers.NewTestServer(t)

	// When: ModifyDBInstance is called for a non-existent instance
	resp := rdsQuery(t, srv, "ModifyDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"nope"},
		"DBInstanceClass":      []string{"db.m5.large"},
	})
	defer resp.Body.Close()

	// Then: 400 error with DBInstanceNotFound
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBInstanceNotFound")
}

// ─── CreateDBParameterGroup ───────────────────────────────────────────────────

func TestCreateDBParameterGroup_success(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBParameterGroup is called with valid params
	resp := rdsQuery(t, srv, "CreateDBParameterGroup", url.Values{
		"DBParameterGroupName":   []string{"my-pg-params"},
		"DBParameterGroupFamily": []string{"postgres16"},
		"Description":            []string{"Custom PostgreSQL 16 parameters"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with parameter group details
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"CreateDBParameterGroupResponse"`
		Result  struct {
			DBParameterGroup struct {
				DBParameterGroupName   string `xml:"DBParameterGroupName"`
				DBParameterGroupFamily string `xml:"DBParameterGroupFamily"`
				Description            string `xml:"Description"`
				DBParameterGroupArn    string `xml:"DBParameterGroupArn"`
			} `xml:"DBParameterGroup"`
		} `xml:"CreateDBParameterGroupResult"`
	}
	decodeXML(t, resp, &result)

	pg := result.Result.DBParameterGroup
	assert.Equal(t, "my-pg-params", pg.DBParameterGroupName)
	assert.Equal(t, "postgres16", pg.DBParameterGroupFamily)
	assert.Equal(t, "Custom PostgreSQL 16 parameters", pg.Description)
	assert.NotEmpty(t, pg.DBParameterGroupArn)
	assert.Contains(t, pg.DBParameterGroupArn, "rds")
}

func TestCreateDBParameterGroup_auroraMySQL8Family(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBParameterGroup is called with Aurora MySQL 3's parameter family
	resp := rdsQuery(t, srv, "CreateDBParameterGroup", url.Values{
		"DBParameterGroupName":   []string{"aurora-mysql8-params"},
		"DBParameterGroupFamily": []string{"aurora-mysql8.0"},
		"Description":            []string{"Aurora MySQL 3 instance parameters"},
	})
	defer resp.Body.Close()

	// Then: the parameter group is created with the requested family
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)

	var result struct {
		Result struct {
			DBParameterGroup struct {
				DBParameterGroupFamily string `xml:"DBParameterGroupFamily"`
			} `xml:"DBParameterGroup"`
		} `xml:"CreateDBParameterGroupResult"`
	}
	decodeXML(t, resp, &result)
	assert.Equal(t, "aurora-mysql8.0", result.Result.DBParameterGroup.DBParameterGroupFamily)
}

func TestCreateDBParameterGroup_unknownFamily(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBParameterGroup is called with a family RDS does not advertise
	resp := rdsQuery(t, srv, "CreateDBParameterGroup", url.Values{
		"DBParameterGroupName":   []string{"unknown-family-params"},
		"DBParameterGroupFamily": []string{"aurora-mysql99.0"},
		"Description":            []string{"Unknown family"},
	})
	defer resp.Body.Close()

	// Then: the request is rejected with an AWS Query error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
	helpers.AssertRequestID(t, resp)
}

func TestCreateDBParameterGroup_duplicate(t *testing.T) {
	// Given: a parameter group already exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBParameterGroup", url.Values{
		"DBParameterGroupName":   []string{"dup-params"},
		"DBParameterGroupFamily": []string{"mysql8.0"},
		"Description":            []string{"First"},
	})
	resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)

	// When: CreateDBParameterGroup is called with the same name
	resp := rdsQuery(t, srv, "CreateDBParameterGroup", url.Values{
		"DBParameterGroupName":   []string{"dup-params"},
		"DBParameterGroupFamily": []string{"mysql8.0"},
		"Description":            []string{"Second"},
	})
	defer resp.Body.Close()

	// Then: 400 error with DBParameterGroupAlreadyExists
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBParameterGroupAlreadyExists")
}

// ─── DescribeDBParameterGroups ────────────────────────────────────────────────

func TestDescribeDBParameterGroups_all(t *testing.T) {
	// Given: two parameter groups exist
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"pg-group-a", "pg-group-b"} {
		resp := rdsQuery(t, srv, "CreateDBParameterGroup", url.Values{
			"DBParameterGroupName":   []string{name},
			"DBParameterGroupFamily": []string{"postgres16"},
			"Description":            []string{"desc-" + name},
		})
		resp.Body.Close()
	}

	// When: DescribeDBParameterGroups is called without filters
	resp := rdsQuery(t, srv, "DescribeDBParameterGroups", nil)
	defer resp.Body.Close()

	// Then: 200 OK with both groups
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBParameterGroupsResponse"`
		Result  struct {
			DBParameterGroups struct {
				Items []struct {
					DBParameterGroupName string `xml:"DBParameterGroupName"`
				} `xml:"DBParameterGroup"`
			} `xml:"DBParameterGroups"`
		} `xml:"DescribeDBParameterGroupsResult"`
	}
	decodeXML(t, resp, &result)

	assert.Len(t, result.Result.DBParameterGroups.Items, 2)
}

func TestDescribeDBParameterGroups_byName(t *testing.T) {
	// Given: two parameter groups exist
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"find-me", "ignore-me"} {
		resp := rdsQuery(t, srv, "CreateDBParameterGroup", url.Values{
			"DBParameterGroupName":   []string{name},
			"DBParameterGroupFamily": []string{"mysql8.0"},
			"Description":            []string{"desc"},
		})
		resp.Body.Close()
	}

	// When: DescribeDBParameterGroups is called with a specific name
	resp := rdsQuery(t, srv, "DescribeDBParameterGroups", url.Values{
		"DBParameterGroupName": []string{"find-me"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with only that group
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBParameterGroupsResponse"`
		Result  struct {
			DBParameterGroups struct {
				Items []struct {
					DBParameterGroupName   string `xml:"DBParameterGroupName"`
					DBParameterGroupFamily string `xml:"DBParameterGroupFamily"`
					Description            string `xml:"Description"`
					DBParameterGroupArn    string `xml:"DBParameterGroupArn"`
				} `xml:"DBParameterGroup"`
			} `xml:"DBParameterGroups"`
		} `xml:"DescribeDBParameterGroupsResult"`
	}
	decodeXML(t, resp, &result)

	require.Len(t, result.Result.DBParameterGroups.Items, 1)
	pg := result.Result.DBParameterGroups.Items[0]
	assert.Equal(t, "find-me", pg.DBParameterGroupName)
	assert.Equal(t, "mysql8.0", pg.DBParameterGroupFamily)
	assert.NotEmpty(t, pg.DBParameterGroupArn)
}

// ─── DeleteDBParameterGroup ──────────────────────────────────────────────────

func TestDeleteDBParameterGroup_success(t *testing.T) {
	// Given: a parameter group exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBParameterGroup", url.Values{
		"DBParameterGroupName":   []string{"del-params"},
		"DBParameterGroupFamily": []string{"postgres16"},
		"Description":            []string{"To delete"},
	})
	resp1.Body.Close()

	// When: DeleteDBParameterGroup is called
	resp := rdsQuery(t, srv, "DeleteDBParameterGroup", url.Values{
		"DBParameterGroupName": []string{"del-params"},
	})
	defer resp.Body.Close()

	// Then: 200 OK
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: the group no longer exists
	resp2 := rdsQuery(t, srv, "DescribeDBParameterGroups", url.Values{
		"DBParameterGroupName": []string{"del-params"},
	})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusBadRequest)
}

func TestDeleteDBParameterGroup_notFound(t *testing.T) {
	// Given: no parameter groups exist
	srv := helpers.NewTestServer(t)

	// When: DeleteDBParameterGroup is called for a non-existent name
	resp := rdsQuery(t, srv, "DeleteDBParameterGroup", url.Values{
		"DBParameterGroupName": []string{"nope"},
	})
	defer resp.Body.Close()

	// Then: 400 error with DBParameterGroupNotFound
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBParameterGroupNotFound")
}

// ─── DescribeOrderableDBInstanceOptions ───────────────────────────────────────

func TestDescribeOrderableDBInstanceOptions_postgres(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: DescribeOrderableDBInstanceOptions is called for postgres
	resp := rdsQuery(t, srv, "DescribeOrderableDBInstanceOptions", url.Values{
		"Engine": []string{"postgres"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with orderable options for postgres
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeOrderableDBInstanceOptionsResponse"`
		Result  struct {
			OrderableDBInstanceOptions struct {
				Items []struct {
					Engine          string `xml:"Engine"`
					EngineVersion   string `xml:"EngineVersion"`
					DBInstanceClass string `xml:"DBInstanceClass"`
					LicenseModel    string `xml:"LicenseModel"`
					StorageType     string `xml:"StorageType"`
					MultiAZCapable  bool   `xml:"MultiAZCapable"`
				} `xml:"OrderableDBInstanceOption"`
			} `xml:"OrderableDBInstanceOptions"`
		} `xml:"DescribeOrderableDBInstanceOptionsResult"`
	}
	decodeXML(t, resp, &result)

	options := result.Result.OrderableDBInstanceOptions.Items
	assert.GreaterOrEqual(t, len(options), 4, "should have at least 4 options for postgres")

	for _, opt := range options {
		assert.Equal(t, "postgres", opt.Engine)
		assert.NotEmpty(t, opt.DBInstanceClass)
		assert.NotEmpty(t, opt.EngineVersion)
		assert.NotEmpty(t, opt.StorageType)
	}
}

func TestDescribeOrderableDBInstanceOptions_mysql(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: DescribeOrderableDBInstanceOptions is called for mysql
	resp := rdsQuery(t, srv, "DescribeOrderableDBInstanceOptions", url.Values{
		"Engine": []string{"mysql"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with orderable options for mysql
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeOrderableDBInstanceOptionsResponse"`
		Result  struct {
			OrderableDBInstanceOptions struct {
				Items []struct {
					Engine          string `xml:"Engine"`
					DBInstanceClass string `xml:"DBInstanceClass"`
				} `xml:"OrderableDBInstanceOption"`
			} `xml:"OrderableDBInstanceOptions"`
		} `xml:"DescribeOrderableDBInstanceOptionsResult"`
	}
	decodeXML(t, resp, &result)

	options := result.Result.OrderableDBInstanceOptions.Items
	assert.GreaterOrEqual(t, len(options), 4, "should have at least 4 options for mysql")

	for _, opt := range options {
		assert.Equal(t, "mysql", opt.Engine)
		assert.NotEmpty(t, opt.DBInstanceClass)
	}
}

// ─── Aurora engines on CreateDBInstance ──────────────────────────────────────

func TestCreateDBInstance_aurora_mysql(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBInstance is called with aurora-mysql engine
	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"aurora-mysql-db"},
		"DBInstanceClass":      []string{"db.r5.large"},
		"Engine":               []string{"aurora-mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with instance in "creating" state and aurora-mysql engine
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"CreateDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
				Engine               string `xml:"Engine"`
				EngineVersion        string `xml:"EngineVersion"`
				DBInstanceStatus     string `xml:"DBInstanceStatus"`
				Endpoint             struct {
					Port int `xml:"Port"`
				} `xml:"Endpoint"`
			} `xml:"DBInstance"`
		} `xml:"CreateDBInstanceResult"`
	}
	decodeXML(t, resp, &result)

	inst := result.Result.DBInstance
	assert.Equal(t, "aurora-mysql-db", inst.DBInstanceIdentifier)
	assert.Equal(t, "aurora-mysql", inst.Engine)
	assert.NotEmpty(t, inst.EngineVersion)
	assert.Equal(t, "creating", inst.DBInstanceStatus)
	assert.Equal(t, 3306, inst.Endpoint.Port)
}

func TestCreateDBInstance_aurora_postgresql(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBInstance is called with aurora-postgresql engine
	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"aurora-pg-db"},
		"DBInstanceClass":      []string{"db.r5.large"},
		"Engine":               []string{"aurora-postgresql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with instance in "creating" state and aurora-postgresql engine
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"CreateDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
				Engine               string `xml:"Engine"`
				EngineVersion        string `xml:"EngineVersion"`
				DBInstanceStatus     string `xml:"DBInstanceStatus"`
				Endpoint             struct {
					Port int `xml:"Port"`
				} `xml:"Endpoint"`
			} `xml:"DBInstance"`
		} `xml:"CreateDBInstanceResult"`
	}
	decodeXML(t, resp, &result)

	inst := result.Result.DBInstance
	assert.Equal(t, "aurora-pg-db", inst.DBInstanceIdentifier)
	assert.Equal(t, "aurora-postgresql", inst.Engine)
	assert.NotEmpty(t, inst.EngineVersion)
	assert.Equal(t, "creating", inst.DBInstanceStatus)
	assert.Equal(t, 5432, inst.Endpoint.Port)
}

// ─── CreateDBCluster ──────────────────────────────────────────────────────────

func TestCreateDBCluster_aurora_mysql(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBCluster is called with aurora-mysql engine
	resp := rdsQuery(t, srv, "CreateDBCluster", url.Values{
		"DBClusterIdentifier": []string{"aurora-mysql-cluster"},
		"Engine":              []string{"aurora-mysql"},
		"MasterUsername":      []string{"admin"},
		"MasterUserPassword":  []string{"Password1!"},
		"DatabaseName":        []string{"mydb"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with cluster details
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"CreateDBClusterResponse"`
		Result  struct {
			DBCluster struct {
				DBClusterIdentifier string `xml:"DBClusterIdentifier"`
				Engine              string `xml:"Engine"`
				EngineVersion       string `xml:"EngineVersion"`
				Status              string `xml:"Status"`
				MasterUsername      string `xml:"MasterUsername"`
				DatabaseName        string `xml:"DatabaseName"`
				Port                int    `xml:"Port"`
				DBClusterArn        string `xml:"DBClusterArn"`
			} `xml:"DBCluster"`
		} `xml:"CreateDBClusterResult"`
	}
	decodeXML(t, resp, &result)

	c := result.Result.DBCluster
	assert.Equal(t, "aurora-mysql-cluster", c.DBClusterIdentifier)
	assert.Equal(t, "aurora-mysql", c.Engine)
	assert.NotEmpty(t, c.EngineVersion)
	assert.Equal(t, "creating", c.Status)
	assert.Equal(t, "admin", c.MasterUsername)
	assert.Equal(t, "mydb", c.DatabaseName)
	assert.Equal(t, 3306, c.Port)
	assert.NotEmpty(t, c.DBClusterArn)
}

func TestCreateDBCluster_aurora_postgresql(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBCluster is called with aurora-postgresql engine
	resp := rdsQuery(t, srv, "CreateDBCluster", url.Values{
		"DBClusterIdentifier": []string{"aurora-pg-cluster"},
		"Engine":              []string{"aurora-postgresql"},
		"MasterUsername":      []string{"pgadmin"},
		"MasterUserPassword":  []string{"Password1!"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with cluster details
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"CreateDBClusterResponse"`
		Result  struct {
			DBCluster struct {
				DBClusterIdentifier string `xml:"DBClusterIdentifier"`
				Engine              string `xml:"Engine"`
				Port                int    `xml:"Port"`
				Status              string `xml:"Status"`
				DBClusterArn        string `xml:"DBClusterArn"`
			} `xml:"DBCluster"`
		} `xml:"CreateDBClusterResult"`
	}
	decodeXML(t, resp, &result)

	c := result.Result.DBCluster
	assert.Equal(t, "aurora-pg-cluster", c.DBClusterIdentifier)
	assert.Equal(t, "aurora-postgresql", c.Engine)
	assert.Equal(t, "creating", c.Status)
	assert.Equal(t, 5432, c.Port)
	assert.NotEmpty(t, c.DBClusterArn)
}

func TestCreateDBCluster_invalidEngine(t *testing.T) {
	// Given: the RDS service
	srv := helpers.NewTestServer(t)

	// When: CreateDBCluster is called with a non-Aurora engine
	resp := rdsQuery(t, srv, "CreateDBCluster", url.Values{
		"DBClusterIdentifier": []string{"bad-cluster"},
		"Engine":              []string{"mysql"},
		"MasterUsername":      []string{"admin"},
		"MasterUserPassword":  []string{"Password1!"},
	})
	defer resp.Body.Close()

	// Then: 400 error with InvalidParameterValue
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}

func TestCreateDBCluster_invalidMasterUsernameAndDatabaseName(t *testing.T) {
	tests := []struct {
		name   string
		values url.Values
	}{
		{
			name: "Aurora MySQL master username underscore",
			values: url.Values{
				"DBClusterIdentifier": {"invalid-user"},
				"Engine":              {"aurora-mysql"},
				"MasterUsername":      {"cluster_admin"},
				"MasterUserPassword":  {"Password1!"},
			},
		},
		{
			name: "Aurora PostgreSQL database punctuation",
			values: url.Values{
				"DBClusterIdentifier": {"invalid-database"},
				"Engine":              {"aurora-postgresql"},
				"MasterUsername":      {"clusteradmin"},
				"MasterUserPassword":  {"Password1!"},
				"DatabaseName":        {"app-database"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a fresh RDS endpoint.
			srv := helpers.NewTestServer(t)

			// When: CreateDBCluster receives an AWS-invalid account or database name.
			resp := rdsQuery(t, srv, "CreateDBCluster", tc.values)
			defer resp.Body.Close()

			// Then: the Query API rejects it synchronously with an AWS error envelope.
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			assertQueryXMLError(t, resp, "InvalidParameterValue")
			helpers.AssertRequestID(t, resp)
		})
	}
}

func TestCreateDBCluster_duplicate(t *testing.T) {
	// Given: a cluster already exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBCluster", url.Values{
		"DBClusterIdentifier": []string{"dup-cluster"},
		"Engine":              []string{"aurora-mysql"},
		"MasterUsername":      []string{"admin"},
		"MasterUserPassword":  []string{"Password1!"},
	})
	resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)

	// When: CreateDBCluster is called with the same identifier
	resp := rdsQuery(t, srv, "CreateDBCluster", url.Values{
		"DBClusterIdentifier": []string{"dup-cluster"},
		"Engine":              []string{"aurora-mysql"},
		"MasterUsername":      []string{"admin"},
		"MasterUserPassword":  []string{"Password1!"},
	})
	defer resp.Body.Close()

	// Then: 400 error with DBClusterAlreadyExistsFault
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBClusterAlreadyExistsFault")
}

// ─── DescribeDBClusters ───────────────────────────────────────────────────────

func TestDescribeDBClusters_success(t *testing.T) {
	// Given: two clusters exist
	srv := helpers.NewTestServer(t)
	for _, id := range []string{"cluster-a", "cluster-b"} {
		resp := rdsQuery(t, srv, "CreateDBCluster", url.Values{
			"DBClusterIdentifier": []string{id},
			"Engine":              []string{"aurora-mysql"},
			"MasterUsername":      []string{"admin"},
			"MasterUserPassword":  []string{"Password1!"},
		})
		resp.Body.Close()
	}

	// When: DescribeDBClusters is called without filters
	resp := rdsQuery(t, srv, "DescribeDBClusters", nil)
	defer resp.Body.Close()

	// Then: 200 OK with both clusters
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBClustersResponse"`
		Result  struct {
			DBClusters struct {
				Items []struct {
					DBClusterIdentifier string `xml:"DBClusterIdentifier"`
				} `xml:"DBCluster"`
			} `xml:"DBClusters"`
		} `xml:"DescribeDBClustersResult"`
	}
	decodeXML(t, resp, &result)

	assert.Len(t, result.Result.DBClusters.Items, 2)
}

func TestDescribeDBClusters_byId(t *testing.T) {
	// Given: two clusters exist
	srv := helpers.NewTestServer(t)
	for _, id := range []string{"find-me", "not-me"} {
		resp := rdsQuery(t, srv, "CreateDBCluster", url.Values{
			"DBClusterIdentifier": []string{id},
			"Engine":              []string{"aurora-postgresql"},
			"MasterUsername":      []string{"admin"},
			"MasterUserPassword":  []string{"Password1!"},
		})
		resp.Body.Close()
	}

	// When: DescribeDBClusters is called with a specific ID
	resp := rdsQuery(t, srv, "DescribeDBClusters", url.Values{
		"DBClusterIdentifier": []string{"find-me"},
	})
	defer resp.Body.Close()

	// Then: only the requested cluster is returned
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DescribeDBClustersResponse"`
		Result  struct {
			DBClusters struct {
				Items []struct {
					DBClusterIdentifier string `xml:"DBClusterIdentifier"`
					Engine              string `xml:"Engine"`
				} `xml:"DBCluster"`
			} `xml:"DBClusters"`
		} `xml:"DescribeDBClustersResult"`
	}
	decodeXML(t, resp, &result)

	require.Len(t, result.Result.DBClusters.Items, 1)
	assert.Equal(t, "find-me", result.Result.DBClusters.Items[0].DBClusterIdentifier)
	assert.Equal(t, "aurora-postgresql", result.Result.DBClusters.Items[0].Engine)
}

func TestDescribeDBClusters_notFound(t *testing.T) {
	// Given: no clusters exist
	srv := helpers.NewTestServer(t)

	// When: DescribeDBClusters is called with a non-existent ID
	resp := rdsQuery(t, srv, "DescribeDBClusters", url.Values{
		"DBClusterIdentifier": []string{"nope"},
	})
	defer resp.Body.Close()

	// Then: 400 error with DBClusterNotFoundFault
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBClusterNotFoundFault")
}

// ─── DeleteDBCluster ──────────────────────────────────────────────────────────

func TestDeleteDBCluster_success(t *testing.T) {
	// Given: a cluster exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBCluster", url.Values{
		"DBClusterIdentifier": []string{"del-cluster"},
		"Engine":              []string{"aurora-mysql"},
		"MasterUsername":      []string{"admin"},
		"MasterUserPassword":  []string{"Password1!"},
	})
	resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)

	// When: DeleteDBCluster is called
	resp := rdsQuery(t, srv, "DeleteDBCluster", url.Values{
		"DBClusterIdentifier": []string{"del-cluster"},
		"SkipFinalSnapshot":   []string{"true"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with cluster in "deleting" state
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"DeleteDBClusterResponse"`
		Result  struct {
			DBCluster struct {
				DBClusterIdentifier string `xml:"DBClusterIdentifier"`
				Status              string `xml:"Status"`
			} `xml:"DBCluster"`
		} `xml:"DeleteDBClusterResult"`
	}
	decodeXML(t, resp, &result)

	assert.Equal(t, "del-cluster", result.Result.DBCluster.DBClusterIdentifier)
	assert.Equal(t, "deleting", result.Result.DBCluster.Status)
}

func TestDeleteDBCluster_notFound(t *testing.T) {
	// Given: no clusters exist
	srv := helpers.NewTestServer(t)

	// When: DeleteDBCluster is called for a non-existent cluster
	resp := rdsQuery(t, srv, "DeleteDBCluster", url.Values{
		"DBClusterIdentifier": []string{"nope"},
		"SkipFinalSnapshot":   []string{"true"},
	})
	defer resp.Body.Close()

	// Then: 400 error with DBClusterNotFoundFault
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "DBClusterNotFoundFault")
}

// ─── CreateDBInstance with DBClusterIdentifier ────────────────────────────────

func TestCreateDBInstance_withClusterIdentifier(t *testing.T) {
	// Given: an aurora-postgresql cluster exists
	srv := helpers.NewTestServer(t)
	resp1 := rdsQuery(t, srv, "CreateDBCluster", url.Values{
		"DBClusterIdentifier": []string{"my-cluster"},
		"Engine":              []string{"aurora-postgresql"},
		"MasterUsername":      []string{"admin"},
		"MasterUserPassword":  []string{"Password1!"},
	})
	resp1.Body.Close()
	helpers.AssertStatus(t, resp1, http.StatusOK)

	// When: CreateDBInstance is called with DBClusterIdentifier
	resp := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"my-cluster-instance"},
		"DBInstanceClass":      []string{"db.r5.large"},
		"Engine":               []string{"aurora-postgresql"},
		"DBClusterIdentifier":  []string{"my-cluster"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	defer resp.Body.Close()

	// Then: 200 OK with instance linked to the cluster
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		XMLName xml.Name `xml:"CreateDBInstanceResponse"`
		Result  struct {
			DBInstance struct {
				DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
				Engine               string `xml:"Engine"`
				DBClusterIdentifier  string `xml:"DBClusterIdentifier"`
				DBInstanceStatus     string `xml:"DBInstanceStatus"`
			} `xml:"DBInstance"`
		} `xml:"CreateDBInstanceResult"`
	}
	decodeXML(t, resp, &result)

	inst := result.Result.DBInstance
	assert.Equal(t, "my-cluster-instance", inst.DBInstanceIdentifier)
	assert.Equal(t, "aurora-postgresql", inst.Engine)
	assert.Equal(t, "my-cluster", inst.DBClusterIdentifier)
	assert.Equal(t, "creating", inst.DBInstanceStatus)

	// And: the cluster should include the instance as a member
	descResp := rdsQuery(t, srv, "DescribeDBClusters", url.Values{
		"DBClusterIdentifier": []string{"my-cluster"},
	})
	defer descResp.Body.Close()
	helpers.AssertStatus(t, descResp, http.StatusOK)

	var clusterResult struct {
		XMLName xml.Name `xml:"DescribeDBClustersResponse"`
		Result  struct {
			DBClusters struct {
				Items []struct {
					DBClusterIdentifier string `xml:"DBClusterIdentifier"`
					DBClusterMembers    struct {
						Items []struct {
							DBInstanceIdentifier string `xml:"DBInstanceIdentifier"`
							IsClusterWriter      bool   `xml:"IsClusterWriter"`
						} `xml:"DBClusterMember"`
					} `xml:"DBClusterMembers"`
				} `xml:"DBCluster"`
			} `xml:"DBClusters"`
		} `xml:"DescribeDBClustersResult"`
	}
	decodeXML(t, descResp, &clusterResult)

	require.Len(t, clusterResult.Result.DBClusters.Items, 1)
	cluster := clusterResult.Result.DBClusters.Items[0]
	require.Len(t, cluster.DBClusterMembers.Items, 1)
	assert.Equal(t, "my-cluster-instance", cluster.DBClusterMembers.Items[0].DBInstanceIdentifier)
	assert.True(t, cluster.DBClusterMembers.Items[0].IsClusterWriter)
}

// ─── DescribeEvents ───────────────────────────────────────────────────────────

type rdsEventsResponse struct {
	XMLName xml.Name `xml:"DescribeEventsResponse"`
	Result  struct {
		Marker string `xml:"Marker"`
		Events struct {
			Items []struct {
				SourceIdentifier string `xml:"SourceIdentifier"`
				SourceType       string `xml:"SourceType"`
				Message          string `xml:"Message"`
				Date             string `xml:"Date"`
				SourceArn        string `xml:"SourceArn"`
				EventCategories  struct {
					Items []string `xml:"EventCategory"`
				} `xml:"EventCategories"`
			} `xml:"Event"`
		} `xml:"Events"`
	} `xml:"DescribeEventsResult"`
}

// A DB instance's lifecycle shows up in DescribeEvents, which is the channel
// AWS provides for "what happened to my database" — and the only one a failure
// reason can travel on, since DBInstance has no StatusReason field.
func TestDescribeEvents_instanceLifecycleOnTheWire(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithMockClock())

	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"events-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	resp1.Body.Close()

	resp := rdsQuery(t, srv, "DescribeEvents", url.Values{
		"SourceIdentifier": []string{"events-db"},
		"SourceType":       []string{"db-instance"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result rdsEventsResponse
	decodeXML(t, resp, &result)

	require.Len(t, result.Result.Events.Items, 1)
	e := result.Result.Events.Items[0]
	assert.Equal(t, "events-db", e.SourceIdentifier)
	assert.Equal(t, "db-instance", e.SourceType)
	assert.Equal(t, "DB instance created.", e.Message)
	assert.Equal(t, []string{"creation"}, e.EventCategories.Items)
	assert.NotEmpty(t, e.Date)
	assert.Contains(t, e.SourceArn, ":rds:")
}

// The EventCategories filter arrives as an indexed list on the wire, and the
// AWS SDKs spell that list two different ways. A filter that fails to decode
// silently returns everything, which reads as the filter being ignored.
func TestDescribeEvents_categoryFilterOnTheWire(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithMockClock())

	resp1 := rdsQuery(t, srv, "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"events-filter-db"},
		"Engine":               []string{"mysql"},
		"MasterUsername":       []string{"admin"},
		"MasterUserPassword":   []string{"Password1!"},
	})
	resp1.Body.Close()
	srv.AdvanceClock(1 * time.Second) // creating → available

	resp2 := rdsQuery(t, srv, "StopDBInstance", url.Values{
		"DBInstanceIdentifier": []string{"events-filter-db"},
	})
	resp2.Body.Close()
	srv.AdvanceClock(time.Nanosecond) // stopping → stopped after the engine has stopped

	resp := rdsQuery(t, srv, "DescribeEvents", url.Values{
		"EventCategories.EventCategory.1": []string{"notification"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result rdsEventsResponse
	decodeXML(t, resp, &result)
	require.Len(t, result.Result.Events.Items, 1)
	assert.Equal(t, "DB instance stopped.", result.Result.Events.Items[0].Message)

	resp3 := rdsQuery(t, srv, "DescribeEvents", url.Values{
		"EventCategories.member.1": []string{"creation"},
	})
	defer resp3.Body.Close()
	var byMember rdsEventsResponse
	decodeXML(t, resp3, &byMember)
	require.Len(t, byMember.Result.Events.Items, 1)
	assert.Equal(t, "DB instance created.", byMember.Result.Events.Items[0].Message)
}

func TestDescribeEvents_invalidSourceType(t *testing.T) {
	srv := helpers.NewTestServer(t, helpers.WithMockClock())

	resp := rdsQuery(t, srv, "DescribeEvents", url.Values{
		"SourceType": []string{"not-a-source-type"},
	})
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertQueryXMLError(t, resp, "InvalidParameterValue")
}
