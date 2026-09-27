package sts

import (
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func (h *Handler) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		"GetCallerIdentity":         op.NewTyped[struct{}, getCallerIdentityResp]("GetCallerIdentity", h.getCallerIdentityTyped),
		"GetSessionToken":           op.NewTyped[getSessionTokenReq, getSessionTokenResp]("GetSessionToken", h.getSessionTokenTyped),
		"GetFederationToken":        op.NewTyped[getFederationTokenReq, getFederationTokenResp]("GetFederationToken", h.getFederationTokenTyped),
		"AssumeRole":                op.NewTyped[assumeRoleReq, assumeRoleResp]("AssumeRole", h.assumeRoleTyped),
		"AssumeRoleWithWebIdentity": op.NewTyped[assumeRoleWithWebIdentityReq, assumeRoleWithWebIdentityResp]("AssumeRoleWithWebIdentity", h.assumeRoleWithWebIdentityTyped),
	}
}

// Operations implements router.ProtocolService.
func (s *Service) Operations() []op.Operation {
	ops := s.handler.typedOp
	out := make([]op.Operation, 0, len(ops))
	for _, operation := range ops {
		out = append(out, operation)
	}
	return out
}

// SupportedProtocols implements router.ProtocolService.
func (s *Service) SupportedProtocols() []codec.Codec {
	return []codec.Codec{codec.QueryXML}
}
