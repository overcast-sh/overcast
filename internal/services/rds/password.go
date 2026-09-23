package rds

// password.go — changing a DB instance's master password.
//
// On AWS, ModifyDBInstance's MasterUserPassword applies to the live database:
// the old password stops working and the new one starts. Here the database is
// a container, and the password it answers to was baked into its environment
// at `docker run` time — MYSQL_ROOT_PASSWORD and friends are read once, when
// the engine initialises its data directory, and never again. Restarting the
// container with a new value therefore changes nothing, and recreating it
// would apply the password by throwing the data away.
//
// So the change is made the way a DBA would make it: by running the engine's
// own password statement inside the container, authenticated with the password
// the engine still has. That leaves exactly one thing the record can say about
// a password — that the engine accepts it — and it is why a failure here fails
// the whole modification. Storing a password the container does not honour is
// the same silence that made this parameter worth fixing: the API says the
// rotation happened and every connection using the new password is refused.
//
// Two cases have no running engine to take the change, and they end
// differently:
//
//   - Docker is unavailable and the instance is metadata-only. Nothing
//     contradicts the record, so the new password is simply stored — and a
//     container built later is seeded from it.
//   - The instance is not available (stopped, starting, failed). The container
//     keeps the credentials it was initialised with, and there is no later
//     moment at which Overcast could apply a pending one, so the modification
//     is refused rather than deferred.

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// masterPasswordTimeout bounds the password statement. It is one round trip to
// a local engine that is already accepting connections, so a command still
// running after this is stuck rather than slow.
const masterPasswordTimeout = 30 * time.Second

// engineClients maps an engine to the client binary its image ships. The
// Aurora variants run the open-source images, so they use the same clients.
var engineClients = map[string]string{
	"mysql":             "mysql",
	"aurora-mysql":      "mysql",
	"mariadb":           "mariadb",
	"postgres":          "psql",
	"aurora-postgresql": "psql",
}

const minMasterPasswordLen = 8

// forbiddenPasswordChars are the characters RDS documents as not allowed in a
// master password: forward slash, double quote, at sign, and space. A single
// quote is valid and is escaped by the SQL builders below.
const forbiddenPasswordChars = `/"@ `

// validateMasterUserPassword applies RDS's constraints on a master password,
// so one real RDS would refuse is refused here too rather than working locally
// and failing on deploy — which is the whole reason to run a database against
// an emulator first.
//
// It is worth knowing that this rejects some passwords Overcast itself can
// generate. GetRandomPassword on AWS can also produce characters RDS refuses,
// which is why CDK's Credentials.fromGeneratedSecret excludes them by default.
// A template that trips this would trip it on deploy; finding out here is
// cheaper. The fix is ExcludeCharacters, the same as on AWS.
//
// Nothing downstream depends on this for safety. The statements in this file
// quote and escape their inputs (mysqlString, pgString, pgIdentifier), so a
// password containing a quote is handled correctly regardless — this is a
// fidelity rule, not the injection defence.
func validateMasterUserPassword(engine, password string) *protocol.AWSError {
	maxLen := 128
	switch engine {
	case "mysql", "mariadb", "aurora-mysql":
		maxLen = 41
	case "aurora-postgresql":
		maxLen = 99
	}
	if len(password) < minMasterPasswordLen || len(password) > maxLen {
		return errInvalidParameterValue(fmt.Sprintf(
			"The parameter MasterUserPassword must be between %d and %d characters long.",
			minMasterPasswordLen, maxLen))
	}
	for _, r := range password {
		if r < 0x21 || r > 0x7e || strings.ContainsRune(forbiddenPasswordChars, r) {
			return errInvalidParameterValue(
				"The parameter MasterUserPassword is not a valid password. " +
					"It can include any printable ASCII character except forward slash (/), " +
					"double quote (\"), at symbol (@) or space.")
		}
	}
	return nil
}

// generatedPasswordChars is the alphabet a managed master password is drawn
// from: every printable ASCII character validateMasterUserPassword accepts,
// i.e. 0x21-0x7e minus the four RDS forbids in any master password.
var generatedPasswordChars = func() string {
	var b strings.Builder
	for r := rune(0x21); r <= 0x7e; r++ {
		if !strings.ContainsRune(forbiddenPasswordChars, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}()

// generatedMasterPasswordLength matches Secrets Manager's own
// GetRandomPassword default length (see defaultPasswordLength in the
// secretsmanager package) — the API AWS documents itself using to mint a
// managed master password. It comfortably fits every supported engine's
// maximum (41, the shortest of the bunch — see validateMasterUserPassword).
const generatedMasterPasswordLength = 32

// generateManagedMasterPassword returns a random password
// validateMasterUserPassword accepts for engine, for ManageMasterUserPassword
// to hand to Secrets Manager. It draws only from characters RDS allows in a
// master password, so the generated value can never fail the same validation
// a caller-supplied one is held to.
func generateManagedMasterPassword(engine string) (string, *protocol.AWSError) {
	out := make([]byte, generatedMasterPasswordLength)
	max := big.NewInt(int64(len(generatedPasswordChars)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", protocol.Wrap(protocol.ErrInternalError, err)
		}
		out[i] = generatedPasswordChars[n.Int64()]
	}
	password := string(out)
	if aerr := validateMasterUserPassword(engine, password); aerr != nil {
		// Unreachable in practice — the alphabet and length are built to
		// satisfy every engine's rule — but a generator that produced a
		// password its own validator refuses must not be trusted silently.
		return "", aerr
	}
	return password, nil
}

// changeMasterPassword makes a new master password true of the instance, or
// says why it cannot be. It returns nil only when the caller may record the
// new password: either the engine has taken it, or there is no engine.
func (h *Handler) changeMasterPassword(ctx context.Context, inst *DBInstance, newPassword string) *protocol.AWSError {
	// Before the Docker check, not after: AWS validates the parameter whether
	// or not there is anything running to apply it to, and a metadata-only
	// instance that accepted a password no engine could ever take would just
	// move the problem to whenever a container is first built from the record.
	if aerr := validateMasterUserPassword(inst.Engine, newPassword); aerr != nil {
		return aerr
	}
	if !h.dockerReady.Load() || inst.DockerContainerID == "" {
		// Metadata-only: nothing is serving connections, so nothing can
		// disagree with the record. A container built for this instance later
		// is seeded from the record and starts life on the new password.
		return nil
	}
	if inst.DBInstanceStatus != "available" {
		return errInvalidDBInstanceState(inst.DBInstanceIdentifier,
			"the master password can only be changed while the instance is available — "+
				"the engine has to be running to accept it, and a stopped container keeps the credentials it was created with")
	}
	return h.applyMasterPassword(ctx, inst, newPassword)
}

// changeClusterMasterPassword rotates a cluster's master password across every
// member instance, and persists it on each as it goes.
//
// A DBCluster has no container of its own — the engines belong to its members —
// so this is the whole of what a cluster-level rotation can mean. Each member
// goes through changeMasterPassword, so an instance rotated through its cluster
// and one rotated directly are the same operation.
//
// It stops at the first member that refuses, and the members already rotated
// keep the new password. That is deliberate: the alternative is rolling them
// back, which would need the old password to still work on engines that have
// already stopped accepting it. The error names the member that refused, which
// is what makes the state recoverable by hand.
//
// The caller records the new password on the cluster afterwards, and only if
// this returns nil.
func (h *Handler) changeClusterMasterPassword(ctx context.Context, cluster *DBCluster, newPassword string) *protocol.AWSError {
	// Up front rather than relying on the per-member call below, which a
	// cluster with no members never reaches — that cluster would otherwise
	// record a password RDS refuses, and hand it to the first member created
	// under it.
	if aerr := validateMasterUserPassword(cluster.Engine, newPassword); aerr != nil {
		return aerr
	}

	for _, member := range cluster.DBClusterMembers {
		instanceID := member.DBInstanceIdentifier
		inst, aerr := h.store.getDBInstance(ctx, instanceID)
		if aerr != nil {
			return aerr
		}
		if inst.MasterUserPassword == newPassword {
			continue
		}
		// Against the engine first, on a snapshot: this is a command run in a
		// container, far too slow to hold a record lock across. The record is
		// written below, under the lock, once the engine has taken it.
		if aerr := h.changeMasterPassword(ctx, inst, newPassword); aerr != nil {
			return aerr
		}
		if _, aerr := h.mutateInstance(ctx, instanceID, func(inst *DBInstance) *protocol.AWSError {
			inst.MasterUserPassword = newPassword
			return nil
		}); aerr != nil {
			return aerr
		}
	}
	return nil
}

// applyMasterPassword changes the master password on the engine running in the
// instance's container. It reports an error unless the engine accepted it.
func (h *Handler) applyMasterPassword(ctx context.Context, inst *DBInstance, newPassword string) *protocol.AWSError {
	cmd, env, ok := masterPasswordCommand(inst, newPassword)
	if !ok {
		return errMasterPasswordNotApplied(inst.DBInstanceIdentifier,
			"engine "+inst.Engine+" has no password-change path in Overcast")
	}

	execCtx, cancel := context.WithTimeout(ctx, masterPasswordTimeout)
	defer cancel()

	res, err := h.docker.Exec(execCtx, inst.DockerContainerID, cmd, env)
	if err != nil {
		return errMasterPasswordNotApplied(inst.DBInstanceIdentifier, err.Error())
	}
	if res.ExitCode != 0 {
		detail := strings.TrimSpace(res.Output)
		if detail == "" {
			detail = "the engine's client exited " + strconv.Itoa(res.ExitCode) + " without saying why"
		}
		return errMasterPasswordNotApplied(inst.DBInstanceIdentifier, detail)
	}
	return nil
}

// masterPasswordCommand builds the command that sets a new master password on
// a running engine, and the environment it needs. It authenticates with the
// password the engine currently has, which is the one on the record.
func masterPasswordCommand(inst *DBInstance, newPassword string) (cmd, env []string, ok bool) {
	client, known := engineClients[inst.Engine]
	if !known {
		return nil, nil, false
	}

	switch client {
	case "psql":
		// Use the AWS-shaped rdsadmin maintenance identity so an API reset still
		// works after the master changed its password directly with SQL. The
		// second statement rotates that internal password alongside the stored
		// credential, keeping later resets recoverable too.
		// ON_ERROR_STOP makes a rejected statement a non-zero exit rather than
		// a message on a successful run.
		return []string{
				client,
				"--no-psqlrc",
				"-v", "ON_ERROR_STOP=1",
				"-U", postgresBootstrapUser,
				"-d", "postgres",
				"-c", postgresPasswordStatements(inst.MasterUsername, newPassword),
			},
			[]string{"PGPASSWORD=" + credentialBootstrapPassword(inst.MasterUserPassword)}, true

	default: // mysql, mariadb
		// Use the local-only root maintenance account so password recovery still
		// works if the requested master's grants have been damaged. Its password
		// tracks the stored master password, but root is not exposed over TCP.
		//
		// MYSQL_PWD rather than `-p<password>`, which both clients read, for
		// the same reason psql gets PGPASSWORD: argv is visible to `docker
		// inspect` and to the host process table, and an environment set for
		// one command is not. It also keeps the output usable — given `-p` on
		// the command line, both clients print "Using a password on the
		// command line interface can be insecure" to stderr on every run, and
		// with a TTY-allocated exec that warning lands in the middle of
		// whatever the engine says when a statement fails.
		return []string{
				client,
				"--protocol=socket",
				"-u", "root",
				"-e", mysqlPasswordStatements(inst.MasterUsername, newPassword),
			},
			[]string{"MYSQL_PWD=" + inst.MasterUserPassword}, true
	}
}

// mysqlPasswordStatements sets the new password on every account the container
// was seeded with. `root` is included even when it is not the master user:
// startDBContainer passes the stored password as MYSQL_ROOT_PASSWORD, so
// leaving root behind would make the record wrong about a container rebuilt
// later. IF EXISTS covers the host forms an image did not create.
func mysqlPasswordStatements(masterUser, newPassword string) string {
	users := []string{"root"}
	if masterUser != "" && masterUser != "root" {
		users = append(users, masterUser)
	}

	var b strings.Builder
	for _, user := range users {
		for _, host := range []string{"localhost", "%"} {
			b.WriteString("ALTER USER IF EXISTS ")
			b.WriteString(mysqlString(user))
			b.WriteByte('@')
			b.WriteString(mysqlString(host))
			b.WriteString(" IDENTIFIED BY ")
			b.WriteString(mysqlString(newPassword))
			b.WriteString("; ")
		}
	}
	return strings.TrimSpace(b.String())
}

// postgresPasswordStatements sets the new password on the master role and
// rotates the container-only maintenance credential used for future resets.
func postgresPasswordStatements(masterUser, newPassword string) string {
	return "ALTER USER " + pgIdentifier(masterUser) + " WITH PASSWORD " + pgString(newPassword) + "; " +
		"ALTER USER " + pgIdentifier(postgresBootstrapUser) + " WITH PASSWORD " +
		pgString(credentialBootstrapPassword(newPassword)) + ";"
}

// mysqlEscaper escapes the two characters that end a MySQL string literal
// early. MySQL treats a backslash as an escape character by default, so both
// it and the quote have to be escaped for the engine to receive the value as
// written.
var mysqlEscaper = strings.NewReplacer(`\`, `\\`, `'`, `''`)

// mysqlString renders a value as a MySQL string literal.
func mysqlString(s string) string {
	return "'" + mysqlEscaper.Replace(s) + "'"
}

// pgString renders a value as a PostgreSQL string literal. With
// standard_conforming_strings on — the default since 9.1 — a backslash is an
// ordinary character and only the quote needs doubling.
func pgString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// pgIdentifier renders a value as a quoted PostgreSQL identifier, which is
// also what preserves the case of a mixed-case role name.
func pgIdentifier(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
