// Package transcripts makes speaker-labelled transcripts of recordings on
// machines their room's owner paired, through moil (ARCHITECTURE.md §8.1).
//
// The transcripts table is the truth: one row per recording that should have
// a transcript. The reconciler projects every pending row onto a moil job,
// and the job's end back onto the row.
package transcripts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"
	"unicode/utf8"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/store"
)

// ObjectStore is where recordings and their transcript sidecars live. URLs
// are presigned for the endpoint machines and browsers reach.
type ObjectStore interface {
	Remove(ctx context.Context, key string) error
	PresignedGet(ctx context.Context, key string, expiry time.Duration) (string, error)
	PresignedPut(ctx context.Context, key string, expiry time.Duration) (string, error)
	// PresignedDownload is PresignedGet for a browser to save the object as
	// a file called filename, served as contentType.
	PresignedDownload(ctx context.Context, key string, expiry time.Duration, filename, contentType string) (string, error)
}

type Config struct {
	// Moil is the server machines connect to; Bundle must have been added
	// to it.
	Moil    *moil.Server
	Bundle  *moil.Bundle
	Store   *store.Store
	Objects ObjectStore
	// Now is the time; nil means time.Now.
	Now func() time.Time
}

const (
	// urlGrace is how much longer than an attempt's time limit its URLs
	// last, for the transfers around it.
	urlGrace = 15 * time.Minute
	// maxURLValidity is the longest S3 lets a presigned URL last.
	maxURLValidity = 7 * 24 * time.Hour
	// maxTitle is the longest job title moil takes, in characters.
	maxTitle = 200
)

var (
	// errRecordingGone ends a job whose recording was deleted before a
	// machine took it.
	errRecordingGone = errors.New("the recording was deleted")
	// errOwnerChanged ends a job whose room changed hands after it was
	// submitted, before one of the old owner's machines took it. Its row
	// stays pending, and the next pass submits it for the new owner.
	errOwnerChanged = errors.New("the room's owner changed")
)

// settleTimeout bounds how long recording a run's end may take, storage
// and database included: it goes on while klisi stops.
const settleTimeout = 2 * time.Minute

// Service runs transcript jobs and serves the transcript routes.
type Service struct {
	cfg       Config
	nudge     chan struct{}
	followers sync.WaitGroup

	mu sync.Mutex
	// jobs are the runs followed, by recording ID. A job leaves only once
	// its run's end is recorded on its row, and a pass takes stock of the
	// jobs before it reads the pending rows: so a pass never finds a row
	// pending whose run ended unrecorded, and never runs a job twice.
	jobs map[string]*job
}

// A job is a transcript's moil run, as the service follows it.
type job struct {
	recordingID string
	run         *moil.Run
	// keys are the sidecars the job may upload, kept from submission: the
	// recording can be gone by the time the job ends.
	keys []string

	// Guarded by Service.mu: what the current attempt last reported, and
	// whether the run ended without its end recorded, for the next pass to
	// record.
	attempt    int
	progress   *float64
	message    string
	unrecorded bool
}

func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, nudge: make(chan struct{}, 1), jobs: make(map[string]*job)}
}

// Run reconciles transcript rows with moil jobs at once, then every
// interval and whenever nudged, until ctx is done. It returns once it has
// stopped following jobs, and has tried once more to record the ends that
// failed to be. A job still running then leaves its row pending, and the
// next Run submits it again.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	defer func() {
		s.followers.Wait()
		s.recordUnrecorded(ctx)
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.nudge:
		}
	}
}

// Nudge asks the reconciler to run soon. It never blocks: nudges that come
// while a pass is due anyway make no extra pass.
func (s *Service) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// reconcile records the ends that failed to be recorded, adds the
// transcripts that pairing a machine opted into, submits a job for every
// pending transcript it isn't following yet, and cancels the jobs whose
// transcripts are gone or no longer pending.
func (s *Service) reconcile(ctx context.Context) {
	s.recordUnrecorded(ctx)
	// Before reading the rows: see Service.jobs.
	followed := s.followed()
	if _, err := s.cfg.Store.CreateTranscripts(ctx, s.cfg.Now().Unix()); err != nil {
		log.Printf("transcripts: add transcripts: %v", err)
	}
	pending, err := s.cfg.Store.PendingTranscripts(ctx)
	if err != nil {
		log.Printf("transcripts: list pending transcripts: %v", err)
		return
	}
	wanted := make(map[string]bool, len(pending))
	for _, transcript := range pending {
		wanted[transcript.ID] = true
		if followed[transcript.ID] {
			continue
		}
		if err := s.submit(ctx, transcript); err != nil {
			log.Printf("transcripts: recording %s: submit its job: %v", transcript.ID, err)
		}
	}
	var unwanted []*moil.Run
	s.mu.Lock()
	for id, j := range s.jobs {
		if !wanted[id] {
			unwanted = append(unwanted, j.run)
		}
	}
	s.mu.Unlock()
	for _, run := range unwanted {
		run.Cancel()
	}
}

// followed is the recording IDs of the jobs followed now.
func (s *Service) followed() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make(map[string]bool, len(s.jobs))
	for id := range s.jobs {
		ids[id] = true
	}
	return ids
}

// submit hands a pending transcript's job to moil, which offers it to the
// machines of the room's owner only, and follows it. The room is the
// recording's by ID: its slug may change meanwhile, and name another room.
func (s *Service) submit(ctx context.Context, transcript store.PendingTranscript) error {
	recording := transcript.Recording
	timeout := attemptTimeout(recording.DurationS)
	valid := min(timeout+urlGrace, maxURLValidity)
	id := recording.ID
	run, err := s.cfg.Moil.Submit(ctx, moil.Job{
		ID:       "recording-" + id,
		Bundle:   s.cfg.Bundle,
		Title:    truncate(transcript.RoomName, maxTitle),
		Eligible: moil.OwnedBy(transcript.RoomOwner),
		Timeout:  timeout,
		Prepare: func(ctx context.Context, a moil.Assignment) (map[string]moil.Download, map[string]moil.Upload, error) {
			return s.files(ctx, id, a, valid)
		},
	})
	if err != nil {
		return err
	}
	j := &job{recordingID: id, run: run}
	for _, format := range store.TranscriptFormats {
		j.keys = append(j.keys, recording.TranscriptKey(format))
	}
	s.mu.Lock()
	s.jobs[id] = j
	s.mu.Unlock()
	s.followers.Add(1)
	go s.follow(ctx, j)
	return nil
}

// attemptTimeout is how long a machine may spend on an attempt: an hour,
// plus twice the recording's duration, or three hours when that's unknown.
func attemptTimeout(durationS *int64) time.Duration {
	if durationS == nil || *durationS <= 0 {
		return 3 * time.Hour
	}
	return time.Hour + 2*time.Duration(*durationS)*time.Second
}

// files mints an attempt's URLs as a machine takes it, rather than at
// submission: a job can wait days for a laptop to wake. They are a GET for
// the recording and a PUT for each sidecar, valid for as long as the
// attempt may take. Only a machine of the room's current owner gets them.
func (s *Service) files(ctx context.Context, recordingID string, a moil.Assignment, valid time.Duration) (map[string]moil.Download, map[string]moil.Upload, error) {
	recording, err := s.cfg.Store.RecordingByID(ctx, recordingID)
	var room store.Room
	if err == nil {
		room, err = s.cfg.Store.RoomByID(ctx, recording.RoomID)
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !transcribable(recording)) {
		return nil, nil, errRecordingGone
	}
	if err != nil {
		return nil, nil, prepareError(err)
	}
	if a.Machine.Owner != room.OwnerSub {
		return nil, nil, errOwnerChanged
	}
	input, err := s.cfg.Objects.PresignedGet(ctx, *recording.S3Key, valid)
	if err != nil {
		return nil, nil, prepareError(err)
	}
	outputs := make(map[string]moil.Upload, len(store.TranscriptFormats))
	for _, format := range store.TranscriptFormats {
		output, err := s.cfg.Objects.PresignedPut(ctx, recording.TranscriptKey(format), valid)
		if err != nil {
			return nil, nil, prepareError(err)
		}
		outputs[outputName(format)] = moil.Upload{URL: output}
	}
	return map[string]moil.Download{inputName(recording): {URL: input}}, outputs, nil
}

// prepareError puts the job back in the queue, as a failed attempt: klisi
// couldn't make its URLs this time, but could the next.
func prepareError(err error) error {
	return &moil.JobError{Code: moil.CodeInternal, Message: "klisi couldn't prepare the recording's URLs: " + err.Error(), Retryable: true}
}

// inputName is the recording's file name on the machine. Its extension is
// the container egress wrote for the recording's mode.
func inputName(recording store.Recording) string {
	if recording.AudioOnly {
		return "recording.ogg"
	}
	return "recording.mp4"
}

// outputName is the file the bundle writes a transcript format to: it goes
// by the extension.
func outputName(format store.TranscriptFormat) string { return "transcript." + format.Extension }

func transcribable(recording store.Recording) bool {
	return recording.Status == "completed" && recording.HasFile()
}

// follow keeps what a run reports, for the recording list, until the run
// ends, and then records its end. If ctx ends first, klisi is stopping: moil
// ends the run with ErrClosed, and its row stays pending for the next start.
func (s *Service) follow(ctx context.Context, j *job) {
	defer s.followers.Done()
	for event := range j.run.Events(ctx) {
		s.mu.Lock()
		j.observe(event)
		s.mu.Unlock()
	}
	select {
	case <-j.run.Done():
		s.settle(ctx, j)
	default:
	}
}

// settle records how j's run ended, even while klisi stops, and forgets the
// job. If it can't, it keeps the job for the next pass to record: its row
// stays pending meanwhile, and the job isn't submitted again, which would
// run hours of work again.
func (s *Service) settle(ctx context.Context, j *job) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	err := s.ended(ctx, j)
	s.mu.Lock()
	j.unrecorded = err != nil
	if err == nil && s.jobs[j.recordingID] == j {
		delete(s.jobs, j.recordingID)
	}
	s.mu.Unlock()
	if err != nil {
		log.Printf("transcripts: recording %s: record how its job ended, again next pass: %v", j.recordingID, err)
		return
	}
	// A row the run left pending gets its next job without waiting a tick.
	s.Nudge()
}

// recordUnrecorded tries again to record the ends that failed to be.
func (s *Service) recordUnrecorded(ctx context.Context) {
	var unrecorded []*job
	s.mu.Lock()
	for _, j := range s.jobs {
		if j.unrecorded {
			unrecorded = append(unrecorded, j)
		}
	}
	s.mu.Unlock()
	for _, j := range unrecorded {
		s.settle(ctx, j)
	}
}

func (j *job) observe(event moil.Event) {
	switch event.Kind {
	case moil.EventAssigned:
		// A new attempt starts from scratch, whatever the last one reached.
		j.attempt, j.progress, j.message = event.Attempt, nil, "Starting"
	case moil.EventPhase:
		if event.Attempt == j.attempt {
			j.message = phaseMessage(event.Phase)
		}
	case moil.EventProgress:
		if event.Attempt != j.attempt {
			return
		}
		if event.Fraction != nil {
			fraction := min(max(*event.Fraction, 0), 1)
			j.progress = &fraction
		}
		if event.Message != "" {
			j.message = capitalize(truncate(event.Message, maxMessage))
		}
	}
}

// ended records how a run ended on its row: completed, or failed with an
// error the recording list shows. A run cancelled because its row is gone or
// no longer pending, or ended by a shutdown, leaves the row as it is. It
// fails only if the row couldn't be written.
func (s *Service) ended(ctx context.Context, j *job) error {
	result, err := j.run.Wait(ctx) // the run has ended
	now := s.cfg.Now().Unix()
	switch {
	case errors.Is(err, moil.ErrClosed):
		// klisi is shutting down; the pending row is submitted again at the
		// next start.
		return nil
	case errors.Is(err, moil.ErrCancelled), errors.Is(err, errRecordingGone), errors.Is(err, errOwnerChanged):
	case err != nil:
		log.Printf("transcripts: recording %s: %v", j.recordingID, err)
		if err := s.cfg.Store.FailTranscript(ctx, j.recordingID, failure(err), now); err != nil {
			return err
		}
	case !uploaded(result):
		log.Printf("transcripts: recording %s: machine %s succeeded without uploading every sidecar", j.recordingID, result.Machine)
		if err := s.cfg.Store.FailTranscript(ctx, j.recordingID, missingOutputs, now); err != nil {
			return err
		}
	default:
		if err := s.cfg.Store.CompleteTranscript(ctx, j.recordingID, speakers(result.Meta), now); err != nil {
			return err
		}
	}
	// A job can end after its recording was deleted, with sidecars uploaded
	// after the deletion removed the recording's files. Nothing else would
	// remove them.
	if _, err := s.cfg.Store.RecordingByID(ctx, j.recordingID); errors.Is(err, sql.ErrNoRows) {
		for _, key := range j.keys {
			if err := s.cfg.Objects.Remove(ctx, key); err != nil {
				log.Printf("transcripts: recording %s: remove %s: %v", j.recordingID, key, err)
			}
		}
	} else if err != nil {
		log.Printf("transcripts: recording %s: check it still exists: %v", j.recordingID, err)
	}
	return nil
}

// uploaded reports whether a succeeded job uploaded every sidecar: a script
// may succeed without writing a declared output.
func uploaded(result *moil.Result) bool {
	for _, format := range store.TranscriptFormats {
		if _, ok := result.Files[outputName(format)]; !ok {
			return false
		}
	}
	return true
}

// speakers is the speaker count in the bundle's result meta, if it's there.
func speakers(meta json.RawMessage) *int {
	var result struct {
		Speakers *int `json:"speakers"`
	}
	if json.Unmarshal(meta, &result) != nil || result.Speakers == nil || *result.Speakers < 0 {
		return nil
	}
	return result.Speakers
}

// truncate shortens s to at most n characters.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
