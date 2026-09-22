package authz

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/example/pets/internal/auth"
)

// errForbidden is what a denied caller sees. It names no roles and no policy: a
// caller who may not invoke something does not need to learn what would have let
// them.
var errForbidden = errors.New("caller is not permitted to perform this action")

// NewInterceptor refuses any RPC the caller's roles do not permit.
//
// It runs after authentication, which supplies the claims, and before validation,
// so an unauthorized call is rejected without parsing its body.
func NewInterceptor(policy *Policy) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			procedure := req.Spec().Procedure

			claims, ok := auth.FromContext(ctx)
			if !ok || claims == nil {
				// No claims means authentication was skipped for this procedure.
				// Authorizing an unknown caller is not possible, so refuse.
				slog.WarnContext(ctx, "authorization denied: no claims on request",
					"procedure", procedure)
				return nil, connect.NewError(connect.CodePermissionDenied, errForbidden)
			}

			if !policy.Allows(procedure, claims.Roles) {
				slog.WarnContext(ctx, "authorization denied",
					"procedure", procedure,
					"subject", claims.Subject,
					"roles", claims.Roles,
				)
				return nil, connect.NewError(connect.CodePermissionDenied, errForbidden)
			}

			return next(ctx, req)
		}
	}
}
