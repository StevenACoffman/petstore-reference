package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
)

type contextKey struct{}

var claimsKey = contextKey{}

// Claims holds authenticated identity details propagated from an upstream proxy or bearer token.
type Claims struct {
	Subject  string   `json:"sub"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	Provider string   `json:"provider"` // "iap", "oauth2-proxy", "bearer", "dev"
}

// FromContext retrieves the authenticated claims from the context.
func FromContext(ctx context.Context) (*Claims, bool) {
	claims, ok := ctx.Value(claimsKey).(*Claims)
	return claims, ok
}

// WithClaims injects claims into the context.
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

const (
	DefaultDevEmail   = "developer@local.test"
	DefaultDevSubject = "dev-user-001"
)

// DefaultDevRoles is the local development identity: an admin, so a deny-by-default
// authorization policy does not lock a developer out of their own server. Narrow it
// with DEV_ROLES to feel what a non-admin caller feels.
func DefaultDevRoles() []string { return []string{"user", "admin"} }

// UserEmailFromContext retrieves the authenticated user's email.
func UserEmailFromContext(ctx context.Context) (string, bool) {
	if claims, ok := FromContext(ctx); ok && claims != nil && claims.Email != "" {
		return claims.Email, true
	}
	return "", false
}

// DevIdentityMiddleware injects local development identity headers when a request
// carries none, simulating an upstream oauth2-proxy.
//
// It simulates oauth2-proxy rather than IAP because IAP conveys no group
// membership: an IAP simulation could never exercise a role other than the one
// hardcoded for it, which would leave authorization untestable locally.
func DevIdentityMiddleware(devEmail, devSubject string, devRoles []string) func(http.Handler) http.Handler {
	if devEmail == "" {
		devEmail = DefaultDevEmail
	}
	if devSubject == "" {
		devSubject = DefaultDevSubject
	}
	if len(devRoles) == 0 {
		devRoles = DefaultDevRoles()
	}
	groups := strings.Join(devRoles, ",")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Goog-Authenticated-User-Email") == "" &&
				r.Header.Get("X-Forwarded-Email") == "" &&
				r.Header.Get("Authorization") == "" {
				r.Header.Set("X-Forwarded-Email", devEmail)
				r.Header.Set("X-Forwarded-User", devSubject)
				r.Header.Set("X-Forwarded-Groups", groups)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Config defines the authentication interceptor settings.
type Config struct {
	// Enabled determines whether authentication is enforced.
	Enabled bool

	// DevMode, when true, injects a mock identity if no upstream proxy headers or tokens are present.
	DevMode bool

	// TrustProxyHeaders permits identity headers inserted by a trusted reverse proxy.
	// It must only be enabled when clients cannot reach the service without that proxy.
	TrustProxyHeaders bool

	// StaticTokens is an optional list of valid bearer tokens (e.g. for service-to-service or CLI).
	StaticTokens []string

	// SkipProcedures is a set of RPC procedures that bypass authentication (e.g. "/pet.v2.PetService/GetPet").
	SkipProcedures map[string]bool

	// Validator is an optional custom token/JWT validator (e.g. for verifying Google IAP JWT assertion).
	Validator func(ctx context.Context, token string) (*Claims, error)
}

// NewInterceptor returns a Connect unary interceptor that authenticates incoming requests
// via upstream OAuth proxy / Google IAP headers or bearer tokens.
func NewInterceptor(cfg Config) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if !cfg.Enabled {
				return next(WithClaims(ctx, &Claims{Subject: "anonymous", Email: "anonymous", Provider: "disabled"}), req)
			}

			// Check if this procedure skips authentication
			procedure := req.Spec().Procedure
			if cfg.SkipProcedures != nil && cfg.SkipProcedures[procedure] {
				return next(ctx, req)
			}

			claims, err := authenticate(ctx, req.Header(), cfg)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}
			return next(WithClaims(ctx, claims), req)
		}
	}
}

func authenticate(ctx context.Context, header http.Header, cfg Config) (*Claims, error) {
	if cfg.TrustProxyHeaders {
		iapEmail := header.Get("X-Goog-Authenticated-User-Email")
		iapID := header.Get("X-Goog-Authenticated-User-Id")
		iapJWT := header.Get("X-Goog-IAP-JWT-Assertion")
		if iapEmail != "" || iapJWT != "" {
			if cfg.Validator != nil {
				if iapJWT == "" {
					return nil, errors.New("missing required IAP JWT assertion")
				}
				return cfg.Validator(ctx, iapJWT)
			}
			return &Claims{Subject: strings.TrimPrefix(iapID, "accounts.google.com:"), Email: strings.TrimPrefix(iapEmail, "accounts.google.com:"), Provider: "iap", Roles: []string{"user"}}, nil
		}

		forwardedEmail := header.Get("X-Forwarded-Email")
		forwardedUser := header.Get("X-Forwarded-User")
		if forwardedEmail != "" || forwardedUser != "" {
			var roles []string
			for group := range strings.SplitSeq(header.Get("X-Forwarded-Groups"), ",") {
				if group = strings.TrimSpace(group); group != "" {
					roles = append(roles, group)
				}
			}
			if len(roles) == 0 {
				roles = []string{"user"}
			}
			return &Claims{Subject: forwardedUser, Email: forwardedEmail, Provider: "oauth2-proxy", Roles: roles}, nil
		}
	}

	parts := strings.SplitN(header.Get("Authorization"), " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		token := parts[1]
		if cfg.Validator != nil {
			return cfg.Validator(ctx, token)
		}
		for _, validToken := range cfg.StaticTokens {
			if subtle.ConstantTimeCompare([]byte(token), []byte(validToken)) == 1 {
				return &Claims{Subject: "service-account", Email: "service@internal", Provider: "bearer", Roles: []string{"service"}}, nil
			}
		}
		return nil, errors.New("invalid or expired bearer token")
	}

	// Reached only when DevIdentityMiddleware is not in front of this; the roles
	// match it so the two dev identities cannot disagree.
	if cfg.DevMode {
		return &Claims{
			Subject:  DefaultDevSubject,
			Email:    DefaultDevEmail,
			Provider: "dev",
			Roles:    DefaultDevRoles(),
		}, nil
	}
	return nil, errors.New("missing authentication credentials")
}
