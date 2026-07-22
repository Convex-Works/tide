package api

const (
	DevTokenPath          = "/api/dev/token"
	AuthLoginPath         = "/api/auth/login"
	AuthCallbackPath      = "/api/auth/callback"
	AuthLogoutPath        = "/api/auth/logout"
	MePath                = "/api/me"
	RoomsPath             = "/api/rooms"
	RoomPath              = "/api/rooms/{slug}"
	RoomJoinPath          = "/api/rooms/{slug}/join"
	RoomLobbyPath         = "/api/rooms/{slug}/lobby"
	KickPath              = "/api/rooms/{slug}/participants/{identity}/kick"
	MutePath              = "/api/rooms/{slug}/participants/{identity}/mute"
	MeetingEndPath        = "/api/rooms/{slug}/meeting/end"
	LobbyWaitPath         = "/api/lobby/{id}/wait"
	LobbyApprovePath      = "/api/lobby/{id}/approve"
	LobbyDenyPath         = "/api/lobby/{id}/deny"
	LiveKitWebhookPath    = "/api/webhooks/livekit"
	RecordingStartPath    = "/api/rooms/{slug}/recording/start"
	RecordingStopPath     = "/api/rooms/{slug}/recording/stop"
	RoomRecordingsPath    = "/api/rooms/{slug}/recordings"
	RecordingPath         = "/api/recordings/{id}"
	RecordingDownloadPath = "/api/recordings/{id}/download"
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
	// Live state from LiveKit, populated only on the list path (the create,
	// update, and single-room paths do not query the SFU, so these stay zero
	// there). Active is true when the room currently has participants.
	Active          bool `json:"active"`
	NumParticipants int  `json:"num_participants"`
	Recording       bool `json:"recording"`
	// LastActiveAt is Unix seconds of the most recent participant join, or null
	// if the room has never been used.
	LastActiveAt *int64 `json:"last_active_at"`
}

type PublicRoomInfo struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	LobbyEnabled bool   `json:"lobby_enabled"`
	// IsOwner is true when the requesting session owns this room; the
	// capability itself is still enforced server-side on every route.
	IsOwner bool `json:"is_owner"`
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

// RecordingStartRequest selects the recording mode. Recordings are
// audio-only by default; Video opts into the full composite. The body is
// optional — an empty request means the defaults.
type RecordingStartRequest struct {
	Video bool `json:"video"`
}

type RecordingInfo struct {
	ID        string  `json:"id"`
	RoomSlug  string  `json:"room_slug"`
	EgressID  string  `json:"egress_id"`
	Status    string  `json:"status"`
	StartedBy string  `json:"started_by"`
	StartedAt int64   `json:"started_at"`
	AudioOnly bool    `json:"audio_only"`
	EndedAt   *int64  `json:"ended_at"`
	DurationS *int64  `json:"duration_s"`
	S3Key     *string `json:"s3_key"`
	SizeBytes *int64  `json:"size_bytes"`
}
