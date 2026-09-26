package sts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const stsXMLNS = "https://sts.amazonaws.com/doc/2011-06-15/"

// Handler holds STS handler dependencies.
type Handler struct {
	cfg     *config.Config
	log     *serviceutil.ServiceLogger
	clk     clock.Clock
	bus     *events.Bus
	st      state.Store
	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation
}

func newHandler(cfg *config.Config, log *serviceutil.ServiceLogger, clk clock.Clock, st state.Store) *Handler {
	h := &Handler{cfg: cfg, log: log, clk: clk, st: st}
	h.initOps()
	return h
}

// initOps registers every known STS operation to its handler.
// Implemented operations point to their handler method.
// Adding a new operation: add an entry here and implement it.
func (h *Handler) initOps() {
	h.ops = map[string]http.HandlerFunc{
		"GetCallerIdentity":         h.GetCallerIdentity,
		"GetSessionToken":           h.GetSessionToken,
		"GetFederationToken":        h.GetFederationToken,
		"AssumeRole":                h.AssumeRole,
		"AssumeRoleWithWebIdentity": h.AssumeRoleWithWebIdentity,
	}
	h.typedOp = h.typedOps()
}

func (h *Handler) ownsAction(action string) bool {
	_, ok := h.ops[action]
	return ok
}

func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("Action")
	if fn, ok := h.ops[action]; ok {
		fn(w, r)
		return
	}
	// InvalidAction needs no 501-for-a-real-operation split here, unlike the
	// JSON dispatchers (#1645): the router hands a Query request to STS only
	// for an Action ownsAction claims, and answers a modeled STS action
	// nobody implements with the house-rule 501 itself (router.rootDispatch
	// via operationRegistry.ClaimQuery), so an Action reaching this arm
	// bypassed the router.
	protocol.WriteQueryXMLError(w, r, &protocol.AWSError{
		Code:       "InvalidAction",
		Message:    "The action " + action + " is not valid for STS.",
		HTTPStatus: http.StatusBadRequest,
	})
}

// ─── Handlers ────────────────────────────────────────────────────────────────

// GetCallerIdentity returns the account/user identity for the request.
func (h *Handler) GetCallerIdentity(w http.ResponseWriter, r *http.Request) {
	account := h.cfg.AccountID
	writeSTSXML(w, r, "GetCallerIdentityResponse", "GetCallerIdentityResult", struct {
		Account string `xml:"Account"`
		UserId  string `xml:"UserId"`
		Arn     string `xml:"Arn"`
	}{
		Account: account,
		UserId:  "AKIAIOSFODNN7EXAMPLE",
		Arn:     fmt.Sprintf("arn:aws:iam::%s:root", account),
	})
}

// GetSessionToken returns temporary credentials.
func (h *Handler) GetSessionToken(w http.ResponseWriter, r *http.Request) {
	dur := parseDurationSeconds(r.FormValue("DurationSeconds"), 43200)
	creds := newTempCredentials(h.clk, dur)
	writeSTSXML(w, r, "GetSessionTokenResponse", "GetSessionTokenResult", struct {
		Credentials tempCredentialsXML `xml:"Credentials"`
	}{Credentials: creds})
}

// GetFederationToken returns temporary credentials for a federated user.
func (h *Handler) GetFederationToken(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		protocol.WriteQueryXMLError(w, r, protocol.ErrMissingParameter("Name"))
		return
	}
	dur := parseDurationSeconds(r.FormValue("DurationSeconds"), 43200)
	creds := newTempCredentials(h.clk, dur)
	account := h.cfg.AccountID
	writeSTSXML(w, r, "GetFederationTokenResponse", "GetFederationTokenResult", struct {
		Credentials   tempCredentialsXML `xml:"Credentials"`
		FederatedUser struct {
			Arn             string `xml:"Arn"`
			FederatedUserId string `xml:"FederatedUserId"`
		} `xml:"FederatedUser"`
	}{
		Credentials: creds,
		FederatedUser: struct {
			Arn             string `xml:"Arn"`
			FederatedUserId string `xml:"FederatedUserId"`
		}{
			Arn:             fmt.Sprintf("arn:aws:sts::%s:federated-user/%s", account, name),
			FederatedUserId: fmt.Sprintf("%s:%s", account, name),
		},
	})
}

// AssumeRole returns temporary credentials for the assumed role.
func (h *Handler) AssumeRole(w http.ResponseWriter, r *http.Request) {
	roleArn := r.FormValue("RoleArn")
	sessionName := r.FormValue("RoleSessionName")
	if roleArn == "" {
		protocol.WriteQueryXMLError(w, r, protocol.ErrMissingParameter("RoleArn"))
		return
	}
	if sessionName == "" {
		protocol.WriteQueryXMLError(w, r, protocol.ErrMissingParameter("RoleSessionName"))
		return
	}
	dur := parseDurationSeconds(r.FormValue("DurationSeconds"), 3600)
	creds := newTempCredentials(h.clk, dur)
	account := h.cfg.AccountID
	roleID := randID("AROA", 16)
	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{
			Type: events.STSRoleAssumed, Time: h.clk.Now(), Source: "sts",
			Payload: events.STSAssumeRolePayload{RoleARN: roleArn, SessionName: sessionName},
		})
	}
	h.persistRoleSession(r.Context(), creds.AccessKeyId, roleArn, creds.SecretAccessKey)
	writeSTSXML(w, r, "AssumeRoleResponse", "AssumeRoleResult", struct {
		Credentials     tempCredentialsXML `xml:"Credentials"`
		AssumedRoleUser struct {
			Arn           string `xml:"Arn"`
			AssumedRoleId string `xml:"AssumedRoleId"`
		} `xml:"AssumedRoleUser"`
	}{
		Credentials: creds,
		AssumedRoleUser: struct {
			Arn           string `xml:"Arn"`
			AssumedRoleId string `xml:"AssumedRoleId"`
		}{
			Arn:           assumedRoleArn(account, roleArn, sessionName),
			AssumedRoleId: fmt.Sprintf("%s:%s", roleID, sessionName),
		},
	})
}

// AssumeRoleWithWebIdentity returns temporary credentials for a web identity.
func (h *Handler) AssumeRoleWithWebIdentity(w http.ResponseWriter, r *http.Request) {
	roleArn := r.FormValue("RoleArn")
	sessionName := r.FormValue("RoleSessionName")
	if roleArn == "" {
		protocol.WriteQueryXMLError(w, r, protocol.ErrMissingParameter("RoleArn"))
		return
	}
	if sessionName == "" {
		protocol.WriteQueryXMLError(w, r, protocol.ErrMissingParameter("RoleSessionName"))
		return
	}
	dur := parseDurationSeconds(r.FormValue("DurationSeconds"), 3600)
	creds := newTempCredentials(h.clk, dur)
	account := h.cfg.AccountID
	roleID := randID("AROA", 16)
	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{
			Type: events.STSRoleAssumed, Time: h.clk.Now(), Source: "sts",
			Payload: events.STSAssumeRolePayload{RoleARN: roleArn, SessionName: sessionName},
		})
	}
	h.persistRoleSession(r.Context(), creds.AccessKeyId, roleArn, creds.SecretAccessKey)
	writeSTSXML(w, r, "AssumeRoleWithWebIdentityResponse", "AssumeRoleWithWebIdentityResult", struct {
		Credentials     tempCredentialsXML `xml:"Credentials"`
		AssumedRoleUser struct {
			Arn           string `xml:"Arn"`
			AssumedRoleId string `xml:"AssumedRoleId"`
		} `xml:"AssumedRoleUser"`
		SubjectFromWebIdentityToken string `xml:"SubjectFromWebIdentityToken"`
	}{
		Credentials: creds,
		AssumedRoleUser: struct {
			Arn           string `xml:"Arn"`
			AssumedRoleId string `xml:"AssumedRoleId"`
		}{
			Arn:           assumedRoleArn(account, roleArn, sessionName),
			AssumedRoleId: fmt.Sprintf("%s:%s", roleID, sessionName),
		},
		SubjectFromWebIdentityToken: "test-user",
	})
}

// persistRoleSession stores a mapping from the temporary access key ID to the
// assumed role ARN and secret access key in iam:sessions so that the IAM
// enforcement middleware and SigV4 presigned URL validation can resolve the
// caller's identity and signing key.
func (h *Handler) persistRoleSession(ctx context.Context, accessKeyID, roleArn, secretKey string) {
	if h.st == nil || strings.TrimSpace(accessKeyID) == "" || strings.TrimSpace(roleArn) == "" {
		return
	}
	roleName := roleNameFromRoleArn(roleArn)
	type sessionRecord struct {
		RoleArn         string `json:"RoleArn"`
		RoleName        string `json:"RoleName"`
		SecretAccessKey string `json:"SecretAccessKey"`
	}
	b, err := json.Marshal(sessionRecord{RoleArn: roleArn, RoleName: roleName, SecretAccessKey: secretKey})
	if err != nil {
		return
	}
	// Ignore errors — session persistence is best-effort; missing sessions only
	// affect IAM enforcement resolution, which is opt-in.
	_ = h.st.Set(ctx, "iam:sessions", accessKeyID, string(b))
}

// roleNameFromRoleArn extracts the role name from an IAM role ARN
// (arn:aws:iam::<account>:role/<RoleName> or
// arn:aws:iam::<account>:role/<path>/<RoleName>), returning the last
// "/"-delimited path segment. Falls back to the input unchanged if it
// contains no "/".
func roleNameFromRoleArn(roleArn string) string {
	if idx := strings.LastIndex(roleArn, "/"); idx >= 0 {
		return roleArn[idx+1:]
	}
	return roleArn
}

// assumedRoleArn builds the AssumedRoleUser.Arn shape AWS returns from
// AssumeRole/AssumeRoleWithWebIdentity: arn:aws:sts::<account>:assumed-role/<RoleName>/<SessionName>,
// where RoleName is parsed out of the request's RoleArn — not the session
// name repeated in both segments. Shared by both the legacy and typed wire
// paths so they stay identical.
func assumedRoleArn(account, roleArn, sessionName string) string {
	return fmt.Sprintf("arn:aws:sts::%s:assumed-role/%s/%s", account, roleNameFromRoleArn(roleArn), sessionName)
}

// ─── Wire format helpers ──────────────────────────────────────────────────────

type tempCredentialsXML struct {
	AccessKeyId     string `xml:"AccessKeyId"`
	SecretAccessKey string `xml:"SecretAccessKey"`
	SessionToken    string `xml:"SessionToken"`
	Expiration      string `xml:"Expiration"`
}

func newTempCredentials(clk clock.Clock, durationSecs int) tempCredentialsXML {
	expiry := clk.Now().Add(time.Duration(durationSecs) * time.Second).UTC()
	return tempCredentialsXML{
		AccessKeyId:     randID("ASIA", 16),
		SecretAccessKey: randBase64(30),
		SessionToken:    randBase64(48),
		Expiration:      expiry.Format(time.RFC3339),
	}
}

func writeSTSXML(w http.ResponseWriter, r *http.Request, rootTag, resultTag string, resultBody any) {
	var inner bytes.Buffer
	enc := xml.NewEncoder(&inner)
	if err := enc.EncodeElement(resultBody, xml.StartElement{Name: xml.Name{Local: resultTag}}); err == nil {
		enc.Flush()
	}
	type response struct {
		XMLName          xml.Name                  `xml:""`
		Xmlns            string                    `xml:"xmlns,attr"`
		Inner            []byte                    `xml:",innerxml"`
		ResponseMetadata protocol.ResponseMetadata `xml:"ResponseMetadata"`
	}
	protocol.WriteQueryXML(w, r, http.StatusOK, &response{
		XMLName:          xml.Name{Local: rootTag},
		Xmlns:            stsXMLNS,
		Inner:            inner.Bytes(),
		ResponseMetadata: protocol.QueryResponseMetadata(r),
	})
}

func parseDurationSeconds(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultVal
	}
	return n
}

func randID(prefix string, n int) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[idx.Int64()]
	}
	return prefix + string(b)
}

func randBase64(n int) string {
	b := make([]byte, n)
	rand.Read(b) //nolint:errcheck
	return base64.StdEncoding.EncodeToString(b)
}
