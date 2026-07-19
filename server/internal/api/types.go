package api

const DevTokenPath = "/api/dev/token"

type TokenResponse struct {
	Token string `json:"token"`
	WSURL string `json:"ws_url"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
