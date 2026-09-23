package eventbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// maxBusPolicyBytes is AWS's documented PutPermission limit — "The permission
// policy on the event bus cannot exceed 10 KB in size" — applied to the
// marshaled policy document.
const maxBusPolicyBytes = 10 * 1024

// busPolicyDocument is the IAM-shaped resource policy DescribeEventBus
// reports back as a JSON string in Policy. Overcast stores the decoded
// statement list rather than the marshaled string, so PutPermission can
// upsert by Sid and RemovePermission can remove by Sid without re-parsing
// JSON on every call.
type busPolicyDocument struct {
	Version   string           `json:"Version"`
	Statement []map[string]any `json:"Statement"`
}

// loadBusPolicyStatements returns the bus's current permission statements,
// or nil if none have ever been granted.
func (s *Service) loadBusPolicyStatements(ctx context.Context, busName string) ([]map[string]any, *protocol.AWSError) {
	raw, found, err := s.store.Get(ctx, nsPermissions, serviceutil.RegionKey(s.region(ctx), busName))
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	if !found {
		return nil, nil
	}
	var doc busPolicyDocument
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return nil, nil
	}
	return doc.Statement, nil
}

// saveBusPolicyStatements persists the bus's statement list, or removes the
// record entirely once the last statement is gone — so a bus that never had
// a permission granted, and one whose last permission was just removed, read
// back identically: DescribeEventBus omits Policy for both, the same way AWS
// omits it for a bus nobody has been given cross-account access to.
func (s *Service) saveBusPolicyStatements(ctx context.Context, busName string, statements []map[string]any) *protocol.AWSError {
	key := serviceutil.RegionKey(s.region(ctx), busName)
	if len(statements) == 0 {
		if err := s.store.Delete(ctx, nsPermissions, key); err != nil {
			return protocol.ErrInternalError
		}
		return nil
	}
	b, err := json.Marshal(busPolicyDocument{Version: "2012-10-17", Statement: statements})
	if err != nil {
		return protocol.ErrInternalError
	}
	if len(b) > maxBusPolicyBytes {
		return &protocol.AWSError{
			Code:       "PolicyLengthExceededException",
			Message:    "Event bus policy size limit exceeded.",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if err := s.store.Set(ctx, nsPermissions, key, string(b)); err != nil {
		return protocol.ErrInternalError
	}
	return nil
}

// busPolicyJSON renders the bus's current policy as the JSON string
// DescribeEventBus.Policy carries, or "" once no permission has ever been
// granted — the describeEventBusResponse.Policy field is tagged omitempty so
// this comes out exactly as AWS's absent Policy does.
func (s *Service) busPolicyJSON(ctx context.Context, busName string) (string, *protocol.AWSError) {
	statements, aerr := s.loadBusPolicyStatements(ctx, busName)
	if aerr != nil {
		return "", aerr
	}
	if len(statements) == 0 {
		return "", nil
	}
	b, err := json.Marshal(busPolicyDocument{Version: "2012-10-17", Statement: statements})
	if err != nil {
		return "", protocol.ErrInternalError
	}
	return string(b), nil
}

// deleteBusPolicyStatements drops a bus's stored permission policy, called
// when the bus itself is deleted so a later bus created under the same name
// does not inherit a stranger's permissions.
func (s *Service) deleteBusPolicyStatements(ctx context.Context, busName string) *protocol.AWSError {
	if err := s.store.Delete(ctx, nsPermissions, serviceutil.RegionKey(s.region(ctx), busName)); err != nil {
		return protocol.ErrInternalError
	}
	return nil
}

// principalElement renders PutPermission's Principal member the way AWS's
// own generated resource policies do: "*" stays bare, and anything else is
// wrapped as {"AWS": "<arn>"} — an already-ARN-shaped principal is kept as
// is, and a bare account ID is expanded to its root-user ARN.
func principalElement(principal string) any {
	if principal == "*" {
		return "*"
	}
	return map[string]any{"AWS": principalARN(principal)}
}

func principalARN(principal string) string {
	if strings.HasPrefix(principal, "arn:") {
		return principal
	}
	return fmt.Sprintf("arn:aws:iam::%s:root", principal)
}

// upsertStatement replaces the statement with a matching Sid, or appends a
// new one — PutPermission's own documentation describes exactly this
// "run it again to update" idiom for revising a granted permission.
func upsertStatement(statements []map[string]any, sid string, statement map[string]any) []map[string]any {
	for i, st := range statements {
		if existing, _ := st["Sid"].(string); existing == sid {
			out := make([]map[string]any, len(statements))
			copy(out, statements)
			out[i] = statement
			return out
		}
	}
	out := make([]map[string]any, len(statements), len(statements)+1)
	copy(out, statements)
	return append(out, statement)
}

// parsePolicyStatements decodes PutPermission's whole-document Policy
// parameter, which replaces the bus's entire permission policy rather than
// merging into it — AWS's documentation describes Policy as used "instead
// of" StatementId/Action/Principal/Condition, not alongside them.
func parsePolicyStatements(raw string) ([]map[string]any, error) {
	var doc busPolicyDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, err
	}
	return doc.Statement, nil
}
