package relay

const (
	personalProfile     = "personal"
	workProfile         = "work"
	modelsPath          = "/v1/models"
	messagesPath        = "/v1/messages"
	countTokensPath     = "/v1/messages/count_tokens" // #nosec G101 -- Protocol path, not a credential.
	kindUnknown         = "unknown"
	kindModels          = "models"
	invalidRequestError = "invalid_request_error"
	authOAuth           = "oauth_passthrough"
	authBearer          = "bearer"
	authAPIKey          = "x_api_key"
	authNone            = "none"
)
