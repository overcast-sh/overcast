package sts

import (
	"context"
	"fmt"
	"time"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// ---- Request types (json tags used by codec.Decode for form mapping) ----

type getSessionTokenReq struct {
	DurationSeconds int `json:"DurationSeconds"`
}

type getFederationTokenReq struct {
	Name            string `json:"Name"`
	DurationSeconds int    `json:"DurationSeconds"`
}

type assumeRoleReq struct {
	RoleArn         string `json:"RoleArn"`
	RoleSessionName string `json:"RoleSessionName"`
	DurationSeconds int    `json:"DurationSeconds"`
}

type assumeRoleWithWebIdentityReq struct {
	RoleArn          string `json:"RoleArn"`
	RoleSessionName  string `json:"RoleSessionName"`
	WebIdentityToken string `json:"WebIdentityToken"`
	DurationSeconds  int    `json:"DurationSeconds"`
}

// ---- Response types (xml tags for QueryXML codec WriteResponse) ----

type getCallerIdentityResp struct {
	XMLName struct{}                `xml:"GetCallerIdentityResponse"`
	Xmlns   string                  `xml:"xmlns,attr"`
	Result  getCallerIdentityResult `xml:"GetCallerIdentityResult"`
	Meta    respMeta                `xml:"ResponseMetadata"`
}

type getCallerIdentityResult struct {
	Account string `xml:"Account"`
	UserId  string `xml:"UserId"`
	Arn     string `xml:"Arn"`
}

type getSessionTokenResp struct {
	XMLName struct{}              `xml:"GetSessionTokenResponse"`
	Xmlns   string                `xml:"xmlns,attr"`
	Result  getSessionTokenResult `xml:"GetSessionTokenResult"`
	Meta    respMeta              `xml:"ResponseMetadata"`
}

type getSessionTokenResult struct {
	Credentials credentialsXML `xml:"Credentials"`
}

type getFederationTokenResp struct {
	XMLName struct{}                 `xml:"GetFederationTokenResponse"`
	Xmlns   string                   `xml:"xmlns,attr"`
	Result  getFederationTokenResult `xml:"GetFederationTokenResult"`
	Meta    respMeta                 `xml:"ResponseMetadata"`
}

type getFederationTokenResult struct {
	Credentials   credentialsXML   `xml:"Credentials"`
	FederatedUser federatedUserXML `xml:"FederatedUser"`
}

type assumeRoleResp struct {
	XMLName struct{}         `xml:"AssumeRoleResponse"`
	Xmlns   string           `xml:"xmlns,attr"`
	Result  assumeRoleResult `xml:"AssumeRoleResult"`
	Meta    respMeta         `xml:"ResponseMetadata"`
}

type assumeRoleResult struct {
	Credentials     credentialsXML     `xml:"Credentials"`
	AssumedRoleUser assumedRoleUserXML `xml:"AssumedRoleUser"`
}

type assumeRoleWithWebIdentityResp struct {
	XMLName struct{}                        `xml:"AssumeRoleWithWebIdentityResponse"`
	Xmlns   string                          `xml:"xmlns,attr"`
	Result  assumeRoleWithWebIdentityResult `xml:"AssumeRoleWithWebIdentityResult"`
	Meta    respMeta                        `xml:"ResponseMetadata"`
}

type assumeRoleWithWebIdentityResult struct {
	Credentials                 credentialsXML     `xml:"Credentials"`
	AssumedRoleUser             assumedRoleUserXML `xml:"AssumedRoleUser"`
	SubjectFromWebIdentityToken string             `xml:"SubjectFromWebIdentityToken"`
}

type credentialsXML struct {
	AccessKeyId     string `xml:"AccessKeyId"`
	SecretAccessKey string `xml:"SecretAccessKey"`
	SessionToken    string `xml:"SessionToken"`
	Expiration      string `xml:"Expiration"`
}

type federatedUserXML struct {
	Arn             string `xml:"Arn"`
	FederatedUserId string `xml:"FederatedUserId"`
}

type assumedRoleUserXML struct {
	Arn           string `xml:"Arn"`
	AssumedRoleId string `xml:"AssumedRoleId"`
}

// respMeta embeds RequestID as ResponseMetadata. Populate at call site.
type respMeta struct {
	RequestId string `xml:"RequestId"`
}

// ---- Typed handler functions ----

func (h *Handler) getCallerIdentityTyped(ctx context.Context, _ *struct{}) (*getCallerIdentityResp, *protocol.AWSError) {
	account := h.cfg.AccountID
	meta := metaFromCtx(ctx)
	return &getCallerIdentityResp{
		Xmlns: stsXMLNS,
		Result: getCallerIdentityResult{
			Account: account,
			UserId:  "AKIAIOSFODNN7EXAMPLE",
			Arn:     fmt.Sprintf("arn:aws:iam::%s:root", account),
		},
		Meta: meta,
	}, nil
}

func (h *Handler) getSessionTokenTyped(ctx context.Context, req *getSessionTokenReq) (*getSessionTokenResp, *protocol.AWSError) {
	dur := defaultDuration(req.DurationSeconds, 43200)
	creds := typedCredentials(h.clk, dur)
	return &getSessionTokenResp{
		Xmlns: stsXMLNS,
		Result: getSessionTokenResult{
			Credentials: creds,
		},
		Meta: metaFromCtx(ctx),
	}, nil
}

func (h *Handler) getFederationTokenTyped(ctx context.Context, req *getFederationTokenReq) (*getFederationTokenResp, *protocol.AWSError) {
	if req.Name == "" {
		return nil, protocol.ErrMissingParameter("Name")
	}
	dur := defaultDuration(req.DurationSeconds, 43200)
	creds := typedCredentials(h.clk, dur)
	account := h.cfg.AccountID
	return &getFederationTokenResp{
		Xmlns: stsXMLNS,
		Result: getFederationTokenResult{
			Credentials: creds,
			FederatedUser: federatedUserXML{
				Arn:             fmt.Sprintf("arn:aws:sts::%s:federated-user/%s", account, req.Name),
				FederatedUserId: fmt.Sprintf("%s:%s", account, req.Name),
			},
		},
		Meta: metaFromCtx(ctx),
	}, nil
}

func (h *Handler) assumeRoleTyped(ctx context.Context, req *assumeRoleReq) (*assumeRoleResp, *protocol.AWSError) {
	if req.RoleArn == "" {
		return nil, protocol.ErrMissingParameter("RoleArn")
	}
	if req.RoleSessionName == "" {
		return nil, protocol.ErrMissingParameter("RoleSessionName")
	}
	dur := defaultDuration(req.DurationSeconds, 3600)
	creds := typedCredentials(h.clk, dur)
	account := h.cfg.AccountID
	roleID := randID("AROA", 16)
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type: events.STSRoleAssumed, Time: h.clk.Now(), Source: "sts",
			Payload: events.STSAssumeRolePayload{RoleARN: req.RoleArn, SessionName: req.RoleSessionName},
		})
	}
	h.persistRoleSession(ctx, creds.AccessKeyId, req.RoleArn, creds.SecretAccessKey)
	return &assumeRoleResp{
		Xmlns: stsXMLNS,
		Result: assumeRoleResult{
			Credentials: creds,
			AssumedRoleUser: assumedRoleUserXML{
				Arn:           assumedRoleArn(account, req.RoleArn, req.RoleSessionName),
				AssumedRoleId: fmt.Sprintf("%s:%s", roleID, req.RoleSessionName),
			},
		},
		Meta: metaFromCtx(ctx),
	}, nil
}

func (h *Handler) assumeRoleWithWebIdentityTyped(ctx context.Context, req *assumeRoleWithWebIdentityReq) (*assumeRoleWithWebIdentityResp, *protocol.AWSError) {
	if req.RoleArn == "" {
		return nil, protocol.ErrMissingParameter("RoleArn")
	}
	if req.RoleSessionName == "" {
		return nil, protocol.ErrMissingParameter("RoleSessionName")
	}
	if req.WebIdentityToken == "" {
		return nil, protocol.ErrMissingParameter("WebIdentityToken")
	}
	dur := defaultDuration(req.DurationSeconds, 3600)
	creds := typedCredentials(h.clk, dur)
	account := h.cfg.AccountID
	roleID := randID("AROA", 16)
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type: events.STSRoleAssumed, Time: h.clk.Now(), Source: "sts",
			Payload: events.STSAssumeRolePayload{RoleARN: req.RoleArn, SessionName: req.RoleSessionName},
		})
	}
	h.persistRoleSession(ctx, creds.AccessKeyId, req.RoleArn, creds.SecretAccessKey)
	return &assumeRoleWithWebIdentityResp{
		Xmlns: stsXMLNS,
		Result: assumeRoleWithWebIdentityResult{
			Credentials:                 creds,
			AssumedRoleUser:             assumedRoleUserXML{Arn: assumedRoleArn(account, req.RoleArn, req.RoleSessionName), AssumedRoleId: fmt.Sprintf("%s:%s", roleID, req.RoleSessionName)},
			SubjectFromWebIdentityToken: "test-user",
		},
		Meta: metaFromCtx(ctx),
	}, nil
}

// ---- typed helpers ----

func typedCredentials(clk clock.Clock, durationSecs int) credentialsXML {
	expiry := clk.Now().Add(time.Duration(durationSecs) * time.Second).UTC()
	return credentialsXML{
		AccessKeyId:     randID("ASIA", 16),
		SecretAccessKey: randBase64(30),
		SessionToken:    randBase64(48),
		Expiration:      expiry.Format(time.RFC3339),
	}
}

func defaultDuration(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func metaFromCtx(ctx context.Context) respMeta {
	return respMeta{RequestId: protocol.RequestIDFromContext(ctx)}
}
