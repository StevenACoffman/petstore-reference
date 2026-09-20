package config

import "errors"

// errNoCredentialSource is returned by Validate when authentication is enabled in a
// production posture but nothing can supply an identity: no trusted proxy headers
// and no static bearer tokens. Serving in that state would reject every request.
var errNoCredentialSource = errors.New(
	"authentication is enabled, but neither TRUST_PROXY_HEADERS nor AUTH_TOKENS is configured",
)
