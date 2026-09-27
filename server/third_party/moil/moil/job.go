package moil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// A Job is a unit of work for a machine: run a bundle with these params,
// these input files and these output destinations.
type Job struct {
	// ID identifies the job, ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$. It is
	// optional; one is generated if empty. Submitting a job while one
	// with its ID is unfinished returns that one's Run, so a service can
	// derive IDs from its own records (e.g. "meeting-42-transcript") and
	// resubmit safely; once it has finished, the ID runs again.
	ID string
	// Bundle is the code to run. It must have been added with AddBundle.
	Bundle *Bundle
	// Title is an optional label (at most 200 characters) machines may
	// show their owners, e.g. the meeting's name.
	Title string
	// Params is handed to the script as JSON. It is marshaled once, at
	// Submit; nil becomes null. The script gets the same JSON value, not
	// necessarily the same bytes (spec §2): keep it within I-JSON, with
	// integers beyond 2^53, such as 64-bit IDs, as strings. Submit refuses
	// params nested more than 64 levels deep or with numbers beyond the
	// range of a double, which machines can't read.
	Params any
	// Inputs are the files the runtime downloads before starting the
	// script, by file name (^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$).
	Inputs map[string]Download
	// Outputs are the files the script may produce and where the runtime
	// uploads them, by file name.
	Outputs map[string]Upload
	// Prepare, if set, makes each attempt's inputs and outputs as the
	// attempt is assigned, instead of Inputs and Outputs. Use it for
	// pre-signed URLs, which could expire while the job waits for a
	// machine or between attempts. It runs on its own goroutine before the
	// machine hears of the attempt, holding the machine for the job. ctx
	// ends if the job ends or the machine leaves; the job then waits for a
	// machine again. Server.Close waits for Prepare, so return soon after
	// ctx ends.
	//
	// A *JobError with Retryable set puts the job back in the queue, as a
	// failed attempt that counts toward MaxAttempts; any other error ends
	// the job, and Run.Wait returns it.
	Prepare func(ctx context.Context, a Assignment) (inputs map[string]Download, outputs map[string]Upload, err error)
	// Eligible is the service's policy for which machines may run the
	// job, for example OwnedBy(room.OwnerID). It is required: machine
	// owners can see a job's inputs, and only the service knows who may.
	// Use AnyMachine to allow every paired machine.
	//
	// The Server calls Eligible from its scheduler, possibly many times.
	// It must be fast and must not call the Server.
	Eligible func(Machine) bool
	// Prefer optionally ranks the machines that bid for the job: the
	// highest score wins, ties go to the earliest bid. Without it the
	// earliest bid wins. When retrying, machines that already failed or
	// refused the job rank below all others. Like Eligible, it must be
	// fast and must not call the Server.
	Prefer func(Machine) int
	// Timeout limits each attempt's running time on the machine,
	// preparation and transfers included, rounded up to whole
	// milliseconds. Zero means no limit.
	Timeout time.Duration
	// MaxAttempts overrides Config.MaxAttempts for this job when positive.
	MaxAttempts int
}

// An Assignment is an attempt about to be sent to a machine.
type Assignment struct {
	JobID   string
	Attempt int
	Machine Machine
}

// A Download is where the runtime fetches an input from, with GET. URLs are
// typically pre-signed; they must stay valid until the job runs. Machines
// take https URLs, and http ones only as Config.AllowInsecureHTTP says.
type Download struct {
	URL     string
	Headers map[string]string
}

// An Upload is where the runtime sends an output to, with the same rules
// for URLs as a Download.
type Upload struct {
	URL string
	// Method is "PUT" (the default when empty) or "POST".
	Method  string
	Headers map[string]string
}

// ErrorCode says why an attempt failed (spec §9).
type ErrorCode string

// The error codes of spec §9. Machines report all but the last two.
const (
	CodeScriptError   ErrorCode = "script_error"   // the script sent an error event
	CodeExit          ErrorCode = "exit"           // nonzero exit, or killed by a signal
	CodeNoResult      ErrorCode = "no_result"      // exited without a result
	CodeOutputMissing ErrorCode = "output_missing" // the result named an undeclared or absent file
	CodeTimeout       ErrorCode = "timeout"        // ran past Job.Timeout
	CodeEnvironment   ErrorCode = "environment"    // uv couldn't create the environment; retryable unless the machine can tell it's permanent
	CodeAssetDownload ErrorCode = "asset_download" // an asset couldn't be downloaded
	CodeAssetMismatch ErrorCode = "asset_mismatch" // an asset's hash didn't match the manifest
	CodeInputDownload ErrorCode = "input_download" // an input couldn't be downloaded
	CodeOutputUpload  ErrorCode = "output_upload"  // an output couldn't be uploaded
	CodeRejected      ErrorCode = "rejected"       // the machine couldn't read the assignment; never started
	CodeInterrupted   ErrorCode = "interrupted"    // stopped by the machine's owner, or the app quit
	CodeBusy          ErrorCode = "busy"           // assigned while not idle; never started
	CodeNotApproved   ErrorCode = "not_approved"   // bundle not approved or not stored; never started
	CodeInternal      ErrorCode = "internal"       // a bug in the machine's runtime
	CodeLeaseExpired  ErrorCode = "lease_expired"  // the machine stopped reporting and its lease ran out
	CodeLost          ErrorCode = "lost"           // the machine came back without the attempt, or was removed
)

// Retryable reports whether, by default, the spec says another attempt
// could succeed after an error with this code. The machine has the last
// word: the script decides for CodeScriptError (this reports false, the
// default when it doesn't say), and a machine reports CodeEnvironment,
// CodeInputDownload or CodeOutputUpload as final when it can tell the
// failure is permanent. Codes from newer machines are retryable.
func (c ErrorCode) Retryable() bool {
	switch c {
	case CodeScriptError, CodeExit, CodeNoResult, CodeOutputMissing, CodeTimeout, CodeAssetMismatch, CodeRejected:
		return false
	}
	return true
}

// NeverStarted reports whether the machine refused the attempt before
// starting it because it was busy or lacked the bundle (spec §7.2). Such
// attempts don't count toward a job's attempt limit.
func (c ErrorCode) NeverStarted() bool { return c == CodeBusy || c == CodeNotApproved }

// A JobError is why an attempt failed. Run.Wait returns the last attempt's
// error when a job fails.
type JobError struct {
	Code    ErrorCode
	Message string
	// Retryable says whether another attempt could succeed.
	Retryable bool
	// ExitCode is the script's exit status, if it exited normally.
	ExitCode *int
	// StderrTail is the end of the script's stderr (at most 8 KiB), if a
	// process ran.
	StderrTail string
	// Machine and Attempt say which attempt failed.
	Machine string
	Attempt int
}

// Error describes the failure with the attempt and machine it happened on.
func (e *JobError) Error() string {
	return fmt.Sprintf("moil: attempt %d on machine %s failed: %s: %s", e.Attempt, e.Machine, e.Code, e.Message)
}

// ErrCancelled is returned by Run.Wait for a job that was cancelled.
var ErrCancelled = errors.New("moil: job cancelled")

// ErrClosed is returned for work the Server can no longer do because it
// was closed.
var ErrClosed = errors.New("moil: server closed")

// ErrBundleRemoved is returned by Run.Wait for a job whose bundle was
// removed before it could run, or before it could be retried.
var ErrBundleRemoved = errors.New("moil: the job's bundle was removed")

// ErrTooMuchData is returned by Run.Wait for a job whose attempt sent more
// data than Config.MaxDataBytes. The attempt is stopped and not retried:
// another would send as much.
var ErrTooMuchData = errors.New("moil: the job sent more data than the Server keeps")

// A Result is what a succeeded job produced.
type Result struct {
	// Meta is the script's result metadata, nil if it sent none. It is
	// the JSON value the script sent, not necessarily the same bytes:
	// machines re-encode it (spec §2).
	Meta json.RawMessage
	// Files describes every uploaded output, by name. Only the job's
	// declared outputs appear.
	Files map[string]File
	// Machine and Attempt say which attempt succeeded.
	Machine string
	Attempt int
}

// A File describes one output as uploaded by the machine.
type File struct {
	SizeBytes int64
	SHA256    string
}

// EventKind says what an Event reports.
type EventKind string

// Event kinds. Queued, Assigned and Retrying come from the Server; Phase,
// Progress, Log and Data are forwarded from the attempt's machine.
const (
	EventQueued   EventKind = "queued"   // submitted; waiting for a machine
	EventAssigned EventKind = "assigned" // Attempt was assigned to Machine
	EventRetrying EventKind = "retrying" // Attempt failed with Err; the job waits for a machine again
	EventPhase    EventKind = "phase"    // the runtime entered Phase
	EventProgress EventKind = "progress" // the script reported Fraction and/or Message
	EventLog      EventKind = "log"      // the script logged Message at Level
	EventData     EventKind = "data"     // the script sent Data, service-specific JSON
)

// An Event is one step of a Run. Which fields are set depends on Kind.
type Event struct {
	Kind EventKind
	Time time.Time
	// Attempt and Machine say which attempt the event is about; they're
	// zero for EventQueued.
	Attempt int
	Machine string
	// Phase is "preparing", "downloading", "running" or "uploading", or
	// a phase from a newer machine, to be shown as it is.
	Phase string
	// Fraction is the progress from 0 to 1, when the script gave one.
	Fraction *float64
	// Message is the progress or log message.
	Message string
	// Level is the log level: "debug", "info", "warn" or "error", or a
	// level from a newer machine, to be shown as it is.
	Level string
	// Data is the payload of EventData: the JSON value the script sent,
	// not necessarily the same bytes, since machines re-encode it (spec
	// §2). Compare it as JSON.
	Data json.RawMessage
	// Err is why the attempt failed, for EventRetrying.
	Err *JobError
}

// RunState is where a Run is.
type RunState string

// Run states. A Run moves between Queued and Running as attempts come and
// go, and ends in Succeeded, Failed or Cancelled.
const (
	Queued    RunState = "queued"
	Running   RunState = "running"
	Succeeded RunState = "succeeded"
	Failed    RunState = "failed"
	Cancelled RunState = "cancelled"
)
