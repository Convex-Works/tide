// Package wire holds the moil protocol v1 as it travels: the HTTP bodies
// (spec §3–§5) and the control-channel messages (spec §6).
//
// The moil package speaks the service side with these types and moiltest
// speaks the machine side, so both agree on the wire by construction, and
// the shared vectors in spec/vectors pin that agreement to the Rust side.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Protocol is the protocol version: the v1 in every path and the protocol
// field of hello and welcome.
const Protocol = 1

// Close codes for the control channel (spec §6.4).
const (
	CloseProtocolError = 4400 // the peer sent something it shouldn't have
	CloseUnauthorized  = 4401 // the machine was removed or its token revoked
	CloseReplaced      = 4409 // a newer connection for the same machine replaced this one
)

// Machine states (spec §6.2).
const (
	StateIdle   = "idle"
	StateBusy   = "busy"
	StatePaused = "paused"
)

// Done outcomes (spec §6.2).
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"
)

// Message is a control-channel message. Only the types in this package
// implement it.
type Message interface {
	// Type is the message's "type" field.
	Type() string
	validate() error
}

// Machine → service messages.

// Hello is the machine's first message on a control channel.
type Hello struct {
	Protocol   int          `json:"protocol"`
	AppVersion string       `json:"app_version"`
	Machine    MachineInfo  `json:"machine"`
	State      string       `json:"state"`
	Approved   []string     `json:"approved"`
	Attempts   []AttemptRef `json:"attempts"`
}

// MachineInfo describes a machine's hardware, as it reports it.
type MachineInfo struct {
	Name        string `json:"name"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUs        int    `json:"cpus"`
	MemoryBytes int64  `json:"memory_bytes"`
	GPUs        []GPU  `json:"gpus"`
}

// GPU is one of a machine's GPUs. MemoryBytes is 0 when unknown.
type GPU struct {
	Name        string `json:"name"`
	MemoryBytes int64  `json:"memory_bytes,omitempty"`
}

// AttemptRef names one attempt of one job.
type AttemptRef struct {
	JobID   string `json:"job_id"`
	Attempt int    `json:"attempt"`
}

// State reports a change of the machine's state.
type State struct {
	State string `json:"state"`
}

// Approved replaces the set of bundle hashes the owner approved.
type Approved struct {
	Hashes []string `json:"hashes"`
}

// Bid answers an offer: the machine will take the job.
type Bid struct {
	JobID string `json:"job_id"`
}

// Decline answers an offer: the machine won't take the job.
type Decline struct {
	JobID  string `json:"job_id"`
	Reason string `json:"reason"`
}

// Event forwards one job event with its sequence number.
type Event struct {
	JobID   string   `json:"job_id"`
	Attempt int      `json:"attempt"`
	Seq     int64    `json:"seq"`
	Event   JobEvent `json:"event"`
}

// JobEvent is an event a machine forwards: phase, progress, log or data
// (spec §8.3). Types from newer machines decode without error so that
// receivers can ignore them, and so do phases and log levels, which
// receivers show as they are (spec §11).
type JobEvent struct {
	Type     string          `json:"type"`
	Phase    string          `json:"phase,omitempty"`
	Fraction *float64        `json:"fraction,omitempty"`
	Level    string          `json:"level,omitempty"`
	Message  *string         `json:"message,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

// MarshalJSON writes the event with "v": 1, which the script contract
// version requires on every event.
func (e JobEvent) MarshalJSON() ([]byte, error) {
	type plain JobEvent
	return json.Marshal(struct {
		V int `json:"v"`
		plain
	}{1, plain(e)})
}

// Renew keeps an attempt's lease alive.
type Renew struct {
	JobID   string `json:"job_id"`
	Attempt int    `json:"attempt"`
}

// Done ends an attempt.
type Done struct {
	JobID   string    `json:"job_id"`
	Attempt int       `json:"attempt"`
	Seq     int64     `json:"seq"`
	Outcome string    `json:"outcome"`
	Result  *Result   `json:"result,omitempty"`
	Error   *JobError `json:"error,omitempty"`
}

// Result is what a succeeded attempt produced.
type Result struct {
	Meta  json.RawMessage     `json:"meta,omitempty"`
	Files map[string]FileInfo `json:"files"`
}

// FileInfo describes one uploaded output.
type FileInfo struct {
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// JobError is why an attempt failed (spec §9).
type JobError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	StderrTail string `json:"stderr_tail,omitempty"`
}

// Service → machine messages.

// Welcome answers hello.
type Welcome struct {
	Protocol  int          `json:"protocol"`
	MachineID string       `json:"machine_id"`
	Service   ServiceInfo  `json:"service"`
	Bundles   []BundleInfo `json:"bundles"`
	Resume    []Resume     `json:"resume"`
}

// ServiceInfo names the service.
type ServiceInfo struct {
	Name string `json:"name"`
}

// BundleInfo describes a bundle the service may send jobs for.
type BundleInfo struct {
	Hash        string `json:"hash"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// Resume names an attempt the service still considers assigned to the
// machine, and the last seq it processed for it.
type Resume struct {
	JobID   string `json:"job_id"`
	Attempt int    `json:"attempt"`
	Seq     int64  `json:"seq"`
}

// Bundles replaces the list of bundles the service may send jobs for.
type Bundles struct {
	Bundles []BundleInfo `json:"bundles"`
}

// Offer asks whether the machine will take a job.
type Offer struct {
	JobID      string `json:"job_id"`
	BundleHash string `json:"bundle_hash"`
	Title      string `json:"title,omitempty"`
	ExpiresMS  int64  `json:"expires_ms"`
}

// Assign gives an attempt to one machine.
type Assign struct {
	JobID      string              `json:"job_id"`
	Attempt    int                 `json:"attempt"`
	BundleHash string              `json:"bundle_hash"`
	Title      string              `json:"title,omitempty"`
	Params     json.RawMessage     `json:"params"`
	Inputs     map[string]Download `json:"inputs"`
	Outputs    map[string]Upload   `json:"outputs"`
	LeaseMS    int64               `json:"lease_ms"`
	TimeoutMS  int64               `json:"timeout_ms,omitempty"`
}

// Download is where the runtime fetches an input from, with GET.
type Download struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Upload is where the runtime sends an output to.
type Upload struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Ack acknowledges a done.
type Ack struct {
	JobID   string `json:"job_id"`
	Attempt int    `json:"attempt"`
}

// Cancel stops an attempt.
type Cancel struct {
	JobID   string `json:"job_id"`
	Attempt int    `json:"attempt"`
}

// Unknown is a message of a type this package doesn't know, from a newer
// peer. Receivers ignore it.
type Unknown struct {
	Name string
}

func (Hello) Type() string     { return "hello" }
func (State) Type() string     { return "state" }
func (Approved) Type() string  { return "approved" }
func (Bid) Type() string       { return "bid" }
func (Decline) Type() string   { return "decline" }
func (Event) Type() string     { return "event" }
func (Renew) Type() string     { return "renew" }
func (Done) Type() string      { return "done" }
func (Welcome) Type() string   { return "welcome" }
func (Bundles) Type() string   { return "bundles" }
func (Offer) Type() string     { return "offer" }
func (Assign) Type() string    { return "assign" }
func (Ack) Type() string       { return "ack" }
func (Cancel) Type() string    { return "cancel" }
func (u Unknown) Type() string { return u.Name }

// Encode serializes m as one JSON object with its "type" first. Nil
// collections the spec requires are written as empty ones, never null.
func Encode(m Message) ([]byte, error) {
	if _, ok := m.(Unknown); ok {
		return nil, errors.New("wire: can't encode an unknown message")
	}
	body, err := json.Marshal(withEmptyCollections(m))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Grow(len(body) + len(m.Type()) + 12)
	out.WriteString(`{"type":`)
	typ, _ := json.Marshal(m.Type())
	out.Write(typ)
	if len(body) > 2 {
		out.WriteByte(',')
	}
	out.Write(body[1:])
	return out.Bytes(), nil
}

func withEmptyCollections(m Message) Message {
	switch v := m.(type) {
	case Hello:
		v.Approved = orEmpty(v.Approved)
		v.Attempts = orEmpty(v.Attempts)
		v.Machine.GPUs = orEmpty(v.Machine.GPUs)
		return v
	case Approved:
		v.Hashes = orEmpty(v.Hashes)
		return v
	case Done:
		if v.Result != nil && v.Result.Files == nil {
			r := *v.Result
			r.Files = map[string]FileInfo{}
			v.Result = &r
		}
		return v
	case Welcome:
		v.Bundles = orEmpty(v.Bundles)
		v.Resume = orEmpty(v.Resume)
		return v
	case Bundles:
		v.Bundles = orEmpty(v.Bundles)
		return v
	case Assign:
		if v.Inputs == nil {
			v.Inputs = map[string]Download{}
		}
		if v.Outputs == nil {
			v.Outputs = map[string]Upload{}
		}
		return v
	}
	return m
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// A messageType says how to decode one known message type.
type messageType struct {
	// required lists the top-level fields that must be present and not null.
	required []string
	decode   func([]byte) (Message, error)
}

var messageTypes = map[string]messageType{
	"hello":    {[]string{"protocol", "app_version", "machine", "state", "approved", "attempts"}, decodeAs[Hello]},
	"state":    {[]string{"state"}, decodeAs[State]},
	"approved": {[]string{"hashes"}, decodeAs[Approved]},
	"bid":      {[]string{"job_id"}, decodeAs[Bid]},
	"decline":  {[]string{"job_id", "reason"}, decodeAs[Decline]},
	"event":    {[]string{"job_id", "attempt", "seq", "event"}, decodeAs[Event]},
	"renew":    {[]string{"job_id", "attempt"}, decodeAs[Renew]},
	"done":     {[]string{"job_id", "attempt", "seq", "outcome"}, decodeAs[Done]},
	"welcome":  {[]string{"protocol", "machine_id", "service", "bundles", "resume"}, decodeAs[Welcome]},
	"bundles":  {[]string{"bundles"}, decodeAs[Bundles]},
	"offer":    {[]string{"job_id", "bundle_hash", "expires_ms"}, decodeAs[Offer]},
	"assign":   {[]string{"job_id", "attempt", "bundle_hash", "lease_ms"}, decodeAs[Assign]},
	"ack":      {[]string{"job_id", "attempt"}, decodeAs[Ack]},
	"cancel":   {[]string{"job_id", "attempt"}, decodeAs[Cancel]},
}

func decodeAs[T Message](data []byte) (Message, error) {
	var v T
	err := json.Unmarshal(data, &v)
	return v, err
}

// Decode parses one control-channel message. A message of an unknown type
// decodes to Unknown; a known message with missing or invalid fields is an
// error. Unknown fields are ignored.
func Decode(data []byte) (Message, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, errors.New("a message must be a JSON object")
	}
	var name string
	if err := json.Unmarshal(fields["type"], &name); err != nil || name == "" {
		return nil, errors.New(`a message must have a string "type"`)
	}
	typ, ok := messageTypes[name]
	if !ok {
		return Unknown{Name: name}, nil
	}
	for _, key := range typ.required {
		if v, ok := fields[key]; !ok || isNull(v) {
			return nil, fmt.Errorf("%s: %q is required", name, key)
		}
	}
	if name == "assign" {
		// params is always present but may be null.
		if _, ok := fields["params"]; !ok {
			return nil, errors.New(`assign: "params" is required`)
		}
	}
	m, err := typ.decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return m, nil
}

func isNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// Bounds on what a machine reports about itself, where the spec gives
// none: far beyond what a real machine reports, and small enough that a
// service can keep a report for every machine.
const (
	MaxFieldChars = 64   // a machine's name (spec §4), OS, architecture and app version
	MaxGPUs       = 64   // GPUs in hello
	MaxGPUChars   = 256  // a GPU's name
	MaxApproved   = 1024 // approved bundle hashes
)

func (m Hello) validate() error {
	if m.Protocol != Protocol {
		return fmt.Errorf("protocol %d isn't supported here; this is v%d", m.Protocol, Protocol)
	}
	if err := validState(m.State); err != nil {
		return err
	}
	for _, field := range []string{m.Machine.Name, m.Machine.OS, m.Machine.Arch, m.AppVersion} {
		if Chars(field) > MaxFieldChars {
			return fmt.Errorf("the machine's name, OS, architecture and app version must be at most %d characters", MaxFieldChars)
		}
	}
	if len(m.Machine.GPUs) > MaxGPUs {
		return fmt.Errorf("%d GPUs are more than the %d a service keeps", len(m.Machine.GPUs), MaxGPUs)
	}
	for _, g := range m.Machine.GPUs {
		if Chars(g.Name) > MaxGPUChars {
			return fmt.Errorf("a GPU's name must be at most %d characters", MaxGPUChars)
		}
	}
	if err := validHashes(m.Approved); err != nil {
		return err
	}
	for _, a := range m.Attempts {
		if err := validAttempt(a.JobID, a.Attempt); err != nil {
			return err
		}
	}
	return nil
}

func (m State) validate() error    { return validState(m.State) }
func (m Approved) validate() error { return validHashes(m.Hashes) }
func (m Bid) validate() error      { return validJobID(m.JobID) }
func (m Renew) validate() error    { return validAttempt(m.JobID, m.Attempt) }
func (m Ack) validate() error      { return validAttempt(m.JobID, m.Attempt) }
func (m Cancel) validate() error   { return validAttempt(m.JobID, m.Attempt) }
func (Unknown) validate() error    { return nil }

func (m Decline) validate() error {
	if m.Reason == "" {
		return errors.New("reason is empty")
	}
	return validJobID(m.JobID)
}

func (m Event) validate() error {
	if err := validAttempt(m.JobID, m.Attempt); err != nil {
		return err
	}
	if m.Seq < 1 {
		return fmt.Errorf("seq %d is below 1", m.Seq)
	}
	return m.Event.validate()
}

func (e JobEvent) validate() error {
	switch e.Type {
	case "":
		return errors.New("event has no type")
	case "phase":
		if e.Phase == "" {
			return errors.New("phase event has no phase")
		}
	case "progress":
		if e.Fraction == nil && e.Message == nil {
			return errors.New("progress needs a fraction or a message")
		}
		if e.Fraction != nil && !(*e.Fraction >= 0 && *e.Fraction <= 1) {
			return fmt.Errorf("fraction %v is outside 0..1", *e.Fraction)
		}
	case "log":
		if e.Message == nil {
			return errors.New("log needs a message")
		}
	case "data":
		if e.Payload == nil {
			return errors.New("data needs a payload")
		}
		return checkPayload("the payload", e.Payload)
	}
	if e.Message != nil && len(*e.Message) > MaxEventMessageBytes {
		return fmt.Errorf("the message is %d bytes, over the 64 KiB limit", len(*e.Message))
	}
	// Other types come from newer machines; receivers ignore them.
	return nil
}

func (m Done) validate() error {
	if err := validAttempt(m.JobID, m.Attempt); err != nil {
		return err
	}
	if m.Seq < 1 {
		return fmt.Errorf("seq %d is below 1", m.Seq)
	}
	switch m.Outcome {
	case OutcomeSucceeded:
		if m.Result == nil {
			return errors.New("a succeeded done needs a result")
		}
		if m.Result.Files == nil {
			return errors.New("result.files is required")
		}
		for name := range m.Result.Files {
			if !IsFileName(name) {
				return fmt.Errorf("invalid output name %q", name)
			}
		}
		if m.Result.Meta != nil {
			return checkPayload("the result's meta", m.Result.Meta)
		}
	case OutcomeFailed:
		if m.Error == nil || m.Error.Code == "" {
			return errors.New("a failed done needs an error with a code")
		}
		if len(m.Error.StderrTail) > MaxStderrTailBytes {
			return fmt.Errorf("stderr_tail is %d bytes, over the 8 KiB limit", len(m.Error.StderrTail))
		}
	case OutcomeCancelled:
	default:
		return fmt.Errorf("unknown outcome %q", m.Outcome)
	}
	return nil
}

func (m Welcome) validate() error {
	if m.Protocol != Protocol {
		return fmt.Errorf("protocol %d isn't v%d", m.Protocol, Protocol)
	}
	if !IsMachineID(m.MachineID) {
		return fmt.Errorf("invalid machine ID %q", m.MachineID)
	}
	if m.Service.Name == "" {
		return errors.New("service.name is empty")
	}
	if err := validBundles(m.Bundles); err != nil {
		return err
	}
	for _, r := range m.Resume {
		if err := validAttempt(r.JobID, r.Attempt); err != nil {
			return err
		}
		if r.Seq < 0 {
			return fmt.Errorf("resume seq %d is negative", r.Seq)
		}
	}
	return nil
}

func (m Bundles) validate() error { return validBundles(m.Bundles) }

func (m Offer) validate() error {
	if err := validJobID(m.JobID); err != nil {
		return err
	}
	if !IsSHA256(m.BundleHash) {
		return fmt.Errorf("invalid bundle hash %q", m.BundleHash)
	}
	if Chars(m.Title) > 200 {
		return errors.New("title is longer than 200 characters")
	}
	if m.ExpiresMS < 1 {
		return fmt.Errorf("expires_ms %d is below 1", m.ExpiresMS)
	}
	return nil
}

func (m Assign) validate() error {
	if err := validAttempt(m.JobID, m.Attempt); err != nil {
		return err
	}
	if !IsSHA256(m.BundleHash) {
		return fmt.Errorf("invalid bundle hash %q", m.BundleHash)
	}
	if Chars(m.Title) > 200 {
		return errors.New("title is longer than 200 characters")
	}
	for name, in := range m.Inputs {
		if !IsFileName(name) {
			return fmt.Errorf("invalid input name %q", name)
		}
		if in.URL == "" {
			return fmt.Errorf("input %q has no url", name)
		}
	}
	for name, out := range m.Outputs {
		if !IsFileName(name) {
			return fmt.Errorf("invalid output name %q", name)
		}
		if out.URL == "" {
			return fmt.Errorf("output %q has no url", name)
		}
		if out.Method != "" && out.Method != "PUT" && out.Method != "POST" {
			return fmt.Errorf("output %q has method %q; only PUT and POST are allowed", name, out.Method)
		}
	}
	if m.LeaseMS < 1 {
		return fmt.Errorf("lease_ms %d is below 1", m.LeaseMS)
	}
	if m.TimeoutMS < 0 {
		return fmt.Errorf("timeout_ms %d is negative", m.TimeoutMS)
	}
	return nil
}

func validState(s string) error {
	switch s {
	case StateIdle, StateBusy, StatePaused:
		return nil
	}
	return fmt.Errorf("unknown state %q", s)
}

func validHashes(hashes []string) error {
	if len(hashes) > MaxApproved {
		return fmt.Errorf("%d approved bundles are more than the %d a service keeps", len(hashes), MaxApproved)
	}
	for _, h := range hashes {
		if !IsSHA256(h) {
			return fmt.Errorf("invalid bundle hash %q", h)
		}
	}
	return nil
}

func validBundles(bundles []BundleInfo) error {
	for _, b := range bundles {
		if !IsSHA256(b.Hash) {
			return fmt.Errorf("invalid bundle hash %q", b.Hash)
		}
	}
	return nil
}

func validJobID(id string) error {
	if !IsJobID(id) {
		return fmt.Errorf("invalid job ID %q", id)
	}
	return nil
}

func validAttempt(jobID string, attempt int) error {
	if err := validJobID(jobID); err != nil {
		return err
	}
	if attempt < 1 {
		return fmt.Errorf("attempt %d is below 1", attempt)
	}
	return nil
}

// HTTP bodies (spec §3–§5).

// InfoResponse is the body of GET /v1/info.
type InfoResponse struct {
	Protocol int    `json:"protocol"`
	Name     string `json:"name"`
}

// PairRequest is the body of POST /v1/pair.
type PairRequest struct {
	Name       string `json:"name"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	AppVersion string `json:"app_version"`
}

// PairResponse answers POST /v1/pair.
type PairResponse struct {
	DeviceCode              string      `json:"device_code"`
	UserCode                string      `json:"user_code"`
	VerificationURI         string      `json:"verification_uri"`
	VerificationURIComplete string      `json:"verification_uri_complete"`
	ExpiresIn               int64       `json:"expires_in"`
	Interval                int64       `json:"interval"`
	Service                 ServiceInfo `json:"service"`
}

// TokenRequest is the body of POST /v1/pair/token.
type TokenRequest struct {
	DeviceCode string `json:"device_code"`
}

// TokenResponse answers POST /v1/pair/token once the user confirmed.
type TokenResponse struct {
	MachineID string      `json:"machine_id"`
	Token     string      `json:"token"`
	Service   ServiceInfo `json:"service"`
}

// BundlesResponse is the body of GET /v1/bundles.
type BundlesResponse struct {
	Bundles []BundleInfo `json:"bundles"`
}

// BundleResponse is the body of GET /v1/bundles/{hash}: each file's exact
// contents by name.
type BundleResponse struct {
	Hash  string            `json:"hash"`
	Files map[string]string `json:"files"`
}

// ErrorBody is the body of every error response.
type ErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// Errors of POST /v1/pair/token (RFC 8628 §3.5), and the others the
// endpoints answer with.
const (
	ErrAuthorizationPending   = "authorization_pending"
	ErrSlowDown               = "slow_down"
	ErrAccessDenied           = "access_denied"
	ErrExpiredToken           = "expired_token"
	ErrNotFound               = "not_found"
	ErrUnauthorized           = "unauthorized"
	ErrInvalidRequest         = "invalid_request"
	ErrTemporarilyUnavailable = "temporarily_unavailable"
)
