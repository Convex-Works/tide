package api

const (
	DevTokenPath       = "/api/dev/token"
	AuthLoginPath      = "/api/auth/login"
	AuthCallbackPath   = "/api/auth/callback"
	AuthLogoutPath     = "/api/auth/logout"
	MePath             = "/api/me"
	RoomsPath          = "/api/rooms"
	RoomPath           = "/api/rooms/{slug}"
	RoomJoinPath       = "/api/rooms/{slug}/join"
	RoomLobbyPath      = "/api/rooms/{slug}/lobby"
	LobbyWaitPath      = "/api/lobby/{id}/wait"
	LobbyApprovePath   = "/api/lobby/{id}/approve"
	LobbyDenyPath      = "/api/lobby/{id}/deny"
	LiveKitWebhookPath = "/api/webhooks/livekit"
)

type TokenResponse struct {
	Token string `json:"token"`
	WSURL string `json:"ws_url"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type Me struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type RoomInfo struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	LobbyEnabled bool   `json:"lobby_enabled"`
	CreatedAt    int64  `json:"created_at"`
}

type PublicRoomInfo struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	LobbyEnabled bool   `json:"lobby_enabled"`
}

type CreateRoomRequest struct {
	Name string `json:"name"`
}

type UpdateRoomRequest struct {
	Name         *string `json:"name,omitempty"`
	LobbyEnabled *bool   `json:"lobby_enabled,omitempty"`
}

type JoinRequest struct {
	Name string `json:"name"`
}

type JoinResponse struct {
	Status    string `json:"status"`
	Token     string `json:"token,omitempty"`
	WSURL     string `json:"ws_url,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type LobbyRequestInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RequestedAt int64  `json:"requested_at"`
}

type LobbyWaitingSSE struct {
	Status string `json:"status"`
}

type LobbyAdmittedSSE struct {
	Token string `json:"token"`
	WSURL string `json:"ws_url"`
}

type LobbyDeniedSSE struct{}

type LobbyPendingSSE struct {
	Requests []LobbyRequestInfo `json:"requests"`
}
