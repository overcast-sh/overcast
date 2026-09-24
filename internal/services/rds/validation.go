package rds

import (
	"fmt"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

func validateMasterUsername(engine, username string, cluster bool) *protocol.AWSError {
	allowUnderscore := true
	if cluster {
		allowUnderscore = false
	}
	if !validDatabaseIdentifier(username, 16, allowUnderscore) || reservedMasterUsername(engine, username) {
		return errInvalidParameterValue(fmt.Sprintf("The parameter MasterUsername is not a valid master user name: %q.", username))
	}
	return nil
}

func validateInitialDatabaseName(engine, database string) *protocol.AWSError {
	if database == "" {
		return nil
	}
	maxLen := 64
	if engine == "postgres" || engine == "aurora-postgresql" {
		maxLen = 63
	}
	if !validDatabaseIdentifier(database, maxLen, true) || reservedDatabaseName(engine, database) {
		return errInvalidParameterValue(fmt.Sprintf("The parameter DatabaseName is not a valid database name: %q.", database))
	}
	return nil
}

func validDatabaseIdentifier(value string, maxLen int, allowUnderscore bool) bool {
	if len(value) == 0 || len(value) > maxLen || !asciiLetter(value[0]) {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if asciiLetter(c) || (c >= '0' && c <= '9') || (allowUnderscore && c == '_') {
			continue
		}
		return false
	}
	return true
}

func asciiLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func reservedMasterUsername(engine, username string) bool {
	name := strings.ToLower(username)
	if name == "rdsadmin" || name == "rds_superuser_role" {
		return true
	}
	if engine == "postgres" || engine == "aurora-postgresql" {
		switch name {
		case "rds_superuser", "rds_password", "rds_replication":
			return true
		}
	}
	if engine == "aurora-mysql" {
		switch name {
		case "rdsproxyadmin", "rdsrepladmin", "rdsrepladmin_priv_checks_user",
			"aws_comprehend_access", "aws_lambda_access", "aws_load_s3_access",
			"aws_sagemaker_access", "aws_select_s3_access", "aws_bedrock_access":
			return true
		}
	}
	return false
}

func reservedDatabaseName(engine, database string) bool {
	name := strings.ToLower(database)
	switch name {
	case "select", "create", "drop", "table", "database", "user", "grant":
		return true
	}
	if engine == "mysql" || engine == "mariadb" || engine == "aurora-mysql" {
		switch name {
		case "mysql", "information_schema", "performance_schema", "sys":
			return true
		}
	}
	return false
}

// dbIdentifierMaxLen is the limit AWS documents for DBClusterIdentifier on an
// Aurora cluster and for DBInstanceIdentifier alike. Multi-AZ DB clusters cap at
// 52, which Overcast never creates.
const dbIdentifierMaxLen = 63

// normalizeDBIdentifier applies the one transformation AWS documents for both
// identifiers: "This parameter is stored as a lowercase string."
//
// It is applied on the way in and again wherever a record is keyed, so
// "MyCluster" and "mycluster" name one resource here as they do on AWS. Doing
// only the first half would be worse than doing neither: a create would answer
// with an identifier that a describe for the name the caller sent could not
// find.
func normalizeDBIdentifier(id string) string { return strings.ToLower(id) }

// validateDBIdentifier enforces the shape AWS documents in identical words for
// DBClusterIdentifier and DBInstanceIdentifier:
//
//	Must contain from 1 to 63 letters, numbers, or hyphens.
//	First character must be a letter.
//	Can't end with a hyphen or contain two consecutive hyphens.
//
// Expects an already-normalized identifier. param names the request parameter so
// the message says which one was wrong, as AWS's does.
func validateDBIdentifier(param, id string) *protocol.AWSError {
	valid := len(id) > 0 && len(id) <= dbIdentifierMaxLen &&
		asciiLetter(id[0]) &&
		!strings.HasSuffix(id, "-") &&
		!strings.Contains(id, "--")
	if valid {
		for i := 0; i < len(id); i++ {
			c := id[i]
			if asciiLetter(c) || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			valid = false
			break
		}
	}
	if valid {
		return nil
	}
	return errInvalidParameterValue(fmt.Sprintf(
		"The parameter %s is not a valid identifier: %q. Identifiers must begin with a letter; "+
			"must contain only ASCII letters, digits, and hyphens; and must not end with a hyphen "+
			"or contain two consecutive hyphens.", param, id))
}

// Cluster backup retention, from CreateDBCluster's "Must be a value from 1 to
// 35". Note the asymmetry with CreateDBInstance, which documents 0 to 35
// because 0 disables automated backups there — a cluster has no such
// setting, so 0 is not a value it can be asked for.
const (
	clusterBackupRetentionMin     = 1
	clusterBackupRetentionMax     = 35
	clusterBackupRetentionDefault = 1
)

func validateClusterBackupRetentionPeriod(days int) *protocol.AWSError {
	if days < clusterBackupRetentionMin || days > clusterBackupRetentionMax {
		return errInvalidParameterValue(fmt.Sprintf(
			"The parameter BackupRetentionPeriod must be a value from %d to %d: %d.",
			clusterBackupRetentionMin, clusterBackupRetentionMax, days))
	}
	return nil
}

// Instance backup retention, from CreateDBInstance's "Must be a value from 0
// to 35" — 0 disables automated backups, which is why the instance-side
// minimum is 0 where the cluster-side one is 1 (#2133; DBInstance did not
// model retention at all before this).
const (
	instanceBackupRetentionMin     = 0
	instanceBackupRetentionMax     = 35
	instanceBackupRetentionDefault = 1
)

func validateInstanceBackupRetentionPeriod(days int) *protocol.AWSError {
	if days < instanceBackupRetentionMin || days > instanceBackupRetentionMax {
		return errInvalidParameterValue(fmt.Sprintf(
			"The parameter BackupRetentionPeriod must be a value from %d to %d: %d.",
			instanceBackupRetentionMin, instanceBackupRetentionMax, days))
	}
	return nil
}

// Port bounds, from the "Valid Values: 1150-65535" both create operations carry.
const (
	dbPortMin = 1150
	dbPortMax = 65535
)

func validateDBPort(port int) *protocol.AWSError {
	if port < dbPortMin || port > dbPortMax {
		return errInvalidParameterValue(fmt.Sprintf(
			"The parameter Port must be a value from %d to %d: %d.", dbPortMin, dbPortMax, port))
	}
	return nil
}
