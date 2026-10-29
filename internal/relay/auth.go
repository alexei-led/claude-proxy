package relay

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

const localKeyHeader = "X-Claude-Proxy-Key"

func localAccess(header http.Header, token string) bool {
	values := header.Values(localKeyHeader)
	if len(values) != 1 {
		return false
	}
	supplied := sha256.Sum256([]byte(values[0]))
	expected := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(supplied[:], expected[:]) == 1
}

func oauthAuthorization(header http.Header) (string, bool) {
	values := header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	scheme, value, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || value == "" || strings.ContainsAny(value, " \t\r\n,") {
		return "", false
	}
	return values[0], true
}

func outboundHeaders(in http.Header, route *upstream, oauth string) http.Header {
	var out http.Header
	if route.mode == authOAuth {
		out = in.Clone()
		stripCredentials(out)
		out.Set("Authorization", oauth)
	} else {
		out = make(http.Header)
		for _, key := range []string{"Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta", "User-Agent"} {
			if values, ok := in[key]; ok {
				out[key] = append([]string(nil), values...)
			}
		}
		switch route.mode {
		case authBearer:
			out.Set("Authorization", "Bearer "+route.key)
		case authAPIKey:
			out.Set("X-Api-Key", route.key)
		}
	}
	return out
}

func stripCredentials(header http.Header) {
	for _, key := range []string{localKeyHeader, "Authorization", "Proxy-Authorization", "X-Api-Key", "Cookie", "Set-Cookie"} {
		header.Del(key)
	}
}
