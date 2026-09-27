// Package transcripts makes speaker-labelled transcripts of recordings on
// machines their room's owner paired, through moil (ARCHITECTURE.md §8.1).
//
// The transcripts table is the truth: one row per recording that should have
// a transcript. The reconciler projects every pending row onto a moil job,
// and the job's end back onto the row.
package transcripts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path"
	"sync"
	"time"
	"unicode/utf8"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/recording"
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
	// Stat returns the size and entity tag of the object at key, or an
	// error wrapping fs.ErrNotExist if there is none.
	Stat(ctx context.Context, key string) (size int64, etag string, err error)
	// Copy copies src to dst within storage, stored as contentType, only
	// while src's entity tag is still etag.
	Copy(ctx context.Context, src, etag, dst, contentType string) error
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
	// StorageProblem, when set, is why machines would refuse the URLs of
	// Objects (StorageWarning): every transcript fails at once with it.
	StorageProblem string
}

const (
	// urlGrace is how much longer than an attempt's time limit its URLs
	// last, for the transfers around it.
	urlGrace = 15 * time.Minute
	// maxURLValidity is the longest S3 lets a presigned URL last.
	maxURLValidity = 7 * 24 * time.Hour
	// maxTitle is the longest job title moil takes, in characters.
	maxTitle = 200
	// stagingPrefix is where machines upload transcripts, each attempt to
	// keys of its own. klisi never serves from it.
	stagingPrefix = "transcripts-staging"
	// maxTranscriptBytes is the largest transcript file klisi keeps.
	maxTranscriptBytes = 16 << 20
	// pendingFor is how long a transcript waits for a machine to make it,
	// from its request, before it fails.
	pendingFor = 14 * 24 * time.Hour
	// sweepMargin is how long after its URL expires a staging key is
	// removed: for an upload that began just before, and for storage's
	// clock running behind klisi's.
	sweepMargin = 10 * time.Minute
	// reuseGrace is how long a staging directory's URLs must outlast a
	// whole attempt for the machine it was made for to be handed them
	// again: with urlGrace, for ten minutes after klisi made it.
	reuseGrace = 5 * time.Minute
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
	// timeout is how long each attempt may take, and valid how long the
	// URLs of a staging directory last from when klisi makes it.
	timeout, valid time.Duration

	// Guarded by Service.mu: the staging directory of each attempt, by
	// attempt number, and the one klisi made last; what the current
	// attempt last reported; whether klisi cancelled the run; and whether
	// the run ended without its end recorded, for the next pass to record.
	staging    map[int]*staging
	last       *staging
	attempt    int
	progress   *float64
	message    string
	cancelled  bool
	unrecorded bool
}

// staging is a directory klisi made for a machine's uploads of a job's
// transcript, its keys queued for removal once their URLs expire.
type staging struct {
	machine string // the machine it was made for
	dir     string
	until   time.Time // when its URLs expire
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
	now := s.cfg.Now()
	wanted := make(map[string]bool, len(pending))
	for _, transcript := range pending {
		if now.Sub(time.Unix(transcript.RequestedAt, 0)) >= pendingFor && !s.busy(transcript.ID) {
			// Not wanted any more: its job, if any, is cancelled below. Only
			// the request read above expires: if the host requested it
			// again meanwhile, it is wanted.
			failed, err := s.cfg.Store.ExpireTranscript(ctx, transcript.ID, transcript.RequestedAt, expired, now.Unix())
			if err != nil {
				log.Printf("transcripts: recording %s: fail it for waiting too long: %v", transcript.ID, err)
			}
			if failed {
				continue
			}
		}
		wanted[transcript.ID] = true
		if followed[transcript.ID] {
			continue
		}
		if s.cfg.StorageProblem != "" {
			if err := s.cfg.Store.FailTranscript(ctx, transcript.ID, s.cfg.StorageProblem, now.Unix()); err != nil {
				log.Printf("transcripts: recording %s: fail it for storage machines can't use: %v", transcript.ID, err)
			}
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
			// Before Cancel: a run that ends cancelled without this, a
			// machine ended of its own accord (ended).
			j.cancelled = true
			unwanted = append(unwanted, j.run)
		}
	}
	s.mu.Unlock()
	for _, run := range unwanted {
		run.Cancel()
	}
}

// busy reports whether a machine is working on a transcript's job, or has
// finished it and its end is about to be recorded: a transcript doesn't
// fail for waiting too long then.
func (s *Service) busy(recordingID string) bool {
	s.mu.Lock()
	j := s.jobs[recordingID]
	s.mu.Unlock()
	return j != nil && j.run.State() != moil.Queued
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
	j := &job{
		recordingID: recording.ID, timeout: timeout, valid: min(timeout+urlGrace, maxURLValidity),
		staging: make(map[int]*staging),
	}
	run, err := s.cfg.Moil.Submit(ctx, moil.Job{
		ID:       "recording-" + recording.ID,
		Bundle:   s.cfg.Bundle,
		Title:    truncate(transcript.RoomName, maxTitle),
		Eligible: moil.OwnedBy(transcript.RoomOwner),
		Timeout:  timeout,
		Prepare: func(ctx context.Context, a moil.Assignment) (map[string]moil.Download, map[string]moil.Upload, error) {
			return s.files(ctx, j, a)
		},
	})
	if err != nil {
		return err
	}
	// Submit started a new run, with this Prepare: a job leaves s.jobs, and
	// its ID can be submitted again, only once its run has ended.
	j.run = run
	id := recording.ID
	s.mu.Lock()
	s.jobs[id] = j
	s.mu.Unlock()
	s.followers.Add(1)
	go s.follow(ctx, j)
	return nil
}

// attemptTimeout is how long a machine may spend on an attempt: an hour
// plus twice the recording's duration, and at least three hours, since a
// machine's first attempt also downloads 2.9 GB of models. It is three
// hours when the duration is unknown.
func attemptTimeout(durationS *int64) time.Duration {
	timeout := 3 * time.Hour
	if durationS != nil && *durationS > 0 {
		timeout = max(timeout, time.Hour+2*time.Duration(*durationS)*time.Second)
	}
	return timeout
}

// files mints an attempt's URLs as a machine takes it, rather than at
// submission: a job can wait days for a laptop to wake. They are a GET for
// the recording and a PUT for each transcript format, valid for as long as
// the attempt may take. Only a machine of the room's current owner gets
// them, and it uploads to staging keys of its own (stagingFor), which
// klisi never serves from: it checks and copies them when the job
// succeeds.
func (s *Service) files(ctx context.Context, j *job, a moil.Assignment) (map[string]moil.Download, map[string]moil.Upload, error) {
	recording, err := s.cfg.Store.RecordingByID(ctx, j.recordingID)
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
	now := s.cfg.Now()
	dir, err := s.stagingFor(ctx, j, a, now)
	if err != nil {
		return nil, nil, prepareError(err)
	}
	// Every URL expires with the directory's, when its keys are due for
	// removal.
	valid := dir.until.Sub(now)
	input, err := s.cfg.Objects.PresignedGet(ctx, *recording.S3Key, valid)
	if err != nil {
		return nil, nil, prepareError(err)
	}
	outputs := make(map[string]moil.Upload, len(store.TranscriptFormats))
	for _, format := range store.TranscriptFormats {
		output, err := s.cfg.Objects.PresignedPut(ctx, stagedKey(dir.dir, format), valid)
		if err != nil {
			return nil, nil, prepareError(err)
		}
		outputs[outputName(format)] = moil.Upload{URL: output}
	}
	s.mu.Lock()
	j.staging[a.Attempt] = dir
	s.mu.Unlock()
	return map[string]moil.Download{inputName(recording): {URL: input}}, outputs, nil
}

// stagingFor is where the machine taking attempt a of j uploads: a new
// staging directory, queued for removal once its URLs expire, or the one
// klisi made last, if it was made for the same machine and its URLs would
// outlast the whole attempt by reuseGrace. A machine that keeps taking a
// job and letting it go before it starts, which moil lets it do, costs
// klisi no more than a directory every ten minutes.
func (s *Service) stagingFor(ctx context.Context, j *job, a moil.Assignment, now time.Time) (*staging, error) {
	s.mu.Lock()
	last := j.last
	s.mu.Unlock()
	if last != nil && last.machine == a.Machine.ID && last.until.Sub(now) >= j.timeout+reuseGrace {
		return last, nil
	}
	dir, err := stagingDir(j.recordingID, a.Attempt)
	if err != nil {
		return nil, err
	}
	// The staging keys are queued before a machine can write them, to be
	// removed once their URLs expire: whatever arrives, however late.
	until := now.Add(j.valid)
	staged := make([]string, 0, len(store.TranscriptFormats))
	for _, format := range store.TranscriptFormats {
		staged = append(staged, stagedKey(dir, format))
	}
	if err := s.cfg.Store.QueueRemovals(ctx, staged, until.Add(sweepMargin).Unix()); err != nil {
		return nil, err
	}
	made := &staging{machine: a.Machine.ID, dir: dir, until: until}
	s.mu.Lock()
	j.last = made
	s.mu.Unlock()
	return made, nil
}

// stagingDir is a new directory for an attempt's uploads. Its name starts
// with the attempt's number, and ends with a random part: moil numbers the
// attempts of a job submitted again from 1 again once it has forgotten the
// last run, as it has after a restart, and a directory may serve the
// machine's next attempts too (stagingFor).
func stagingDir(recordingID string, attempt int) (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return path.Join(stagingPrefix, recordingID, fmt.Sprintf("%d-%s", attempt, hex.EncodeToString(random))), nil
}

// stagedKey is where an attempt uploads a transcript format.
func stagedKey(dir string, format store.TranscriptFormat) string {
	return path.Join(dir, outputName(format))
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
	resubmit, err := s.ended(ctx, j)
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
	// Only an end klisi brought about can leave the row wanting a new job:
	// a run that ends for a machine's reasons never makes the next pass
	// start another at once.
	if resubmit {
		s.Nudge()
	}
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
	case moil.EventRetrying:
		// Nor is the failed attempt's progress the next one's.
		j.progress, j.message = nil, ""
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
		if message := machineText(event.Message); message != "" {
			j.message = message
		}
	}
}

// ended records how a run ended on its row: completed, with its files
// beside the recording, or failed with an error the recording list shows. A
// run klisi cancelled, because its row is gone or no longer pending, or
// ended by a shutdown, leaves the row as it is; so does one Prepare ended
// because the room changed hands. It reports whether the row may want a new
// job now: only after those ends of klisi's own making. It fails only if
// storage or the database did, and then can be tried again.
func (s *Service) ended(ctx context.Context, j *job) (resubmit bool, err error) {
	result, err := j.run.Wait(ctx) // the run has ended
	now := s.cfg.Now().Unix()
	s.mu.Lock()
	cancelled := j.cancelled
	s.mu.Unlock()
	switch {
	case errors.Is(err, moil.ErrClosed):
		// klisi is shutting down; the pending row is submitted again at the
		// next start.
		return false, nil
	case errors.Is(err, moil.ErrCancelled) && cancelled:
		// The row was gone or no longer pending; it may have been
		// requested again since.
		return true, nil
	case errors.Is(err, moil.ErrCancelled):
		// A machine said it cancelled an attempt klisi never asked it to.
		// Submitting it again would go round as fast as the machine
		// answers.
		log.Printf("transcripts: recording %s: machine ended the job as cancelled unasked", j.recordingID)
		return false, s.cfg.Store.FailTranscript(ctx, j.recordingID, stoppedOnMachine, now)
	case errors.Is(err, errOwnerChanged):
		// The next pass submits it for the room's new owner.
		return true, nil
	case errors.Is(err, errRecordingGone):
		// Its row went with the recording; if it is still there, it can
		// never be made.
		return false, s.cfg.Store.FailTranscript(ctx, j.recordingID, noFile, now)
	case err != nil:
		// %q: a machine's error, whose text could otherwise forge log lines.
		log.Printf("transcripts: recording %s: %q", j.recordingID, err.Error())
		return false, s.cfg.Store.FailTranscript(ctx, j.recordingID, failure(err), now)
	}
	s.mu.Lock()
	var dir string
	if staged := j.staging[result.Attempt]; staged != nil {
		dir = staged.dir
	}
	s.mu.Unlock()
	return false, s.promote(ctx, j.recordingID, dir, result, now)
}

// promote checks the files a succeeded attempt uploaded to its staging
// directory, copies them beside the recording, where klisi serves them
// from, and completes the row. A file that is missing or larger than
// maxTranscriptBytes fails the row instead, and nothing is copied.
func (s *Service) promote(ctx context.Context, recordingID, dir string, result *moil.Result, now int64) error {
	rec, err := s.cfg.Store.RecordingByID(ctx, recordingID)
	var row store.Transcript
	if err == nil {
		row, err = s.cfg.Store.Transcript(ctx, recordingID)
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.Status != "pending") {
		// Deleted, or failed for waiting too long, while the machine worked.
		s.removeStaged(ctx, recordingID, dir)
		return nil
	}
	if err != nil {
		return err
	}
	if dir == "" {
		// Not an attempt this service prepared: nothing to check.
		return s.reject(ctx, recordingID, dir, missingOutputs, now)
	}
	etags := make([]string, len(store.TranscriptFormats))
	for i, format := range store.TranscriptFormats {
		size, etag, err := s.cfg.Objects.Stat(ctx, stagedKey(dir, format))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			log.Printf("transcripts: recording %s: machine %s succeeded without uploading %s", recordingID, result.Machine, outputName(format))
			return s.reject(ctx, recordingID, dir, missingOutputs, now)
		case err != nil:
			return err
		case size > maxTranscriptBytes:
			log.Printf("transcripts: recording %s: machine %s uploaded %d bytes of %s", recordingID, result.Machine, size, outputName(format))
			return s.reject(ctx, recordingID, dir, tooLarge, now)
		}
		etags[i] = etag
	}
	finals := make([]string, 0, len(store.TranscriptFormats))
	for i, format := range store.TranscriptFormats {
		final := rec.TranscriptKey(format)
		if err := s.cfg.Objects.Copy(ctx, stagedKey(dir, format), etags[i], final, format.ContentType); err != nil {
			return err
		}
		finals = append(finals, final)
	}
	// The recording may have been deleted while its files were copied, its
	// files removed before these were there: remove them too.
	if _, err := s.cfg.Store.RecordingByID(ctx, recordingID); errors.Is(err, sql.ErrNoRows) {
		if err := s.cfg.Store.QueueRemovals(ctx, finals, now); err != nil {
			return err
		}
		if err := recording.RemoveQueued(ctx, s.cfg.Objects, s.cfg.Store, finals, now); err != nil {
			log.Printf("transcripts: recording %s: storage kept its transcript; the reconciler will retry: %v", recordingID, err)
		}
		s.removeStaged(ctx, recordingID, dir)
		return nil
	} else if err != nil {
		return err
	}
	if err := s.cfg.Store.CompleteTranscript(ctx, recordingID, speakers(result.Meta), now); err != nil {
		return err
	}
	s.removeStaged(ctx, recordingID, dir)
	return nil
}

// reject fails a row for what its machine uploaded, and removes the
// uploads.
func (s *Service) reject(ctx context.Context, recordingID, dir, message string, now int64) error {
	if err := s.cfg.Store.FailTranscript(ctx, recordingID, message, now); err != nil {
		return err
	}
	s.removeStaged(ctx, recordingID, dir)
	return nil
}

// removeStaged removes an attempt's uploads once klisi is done with them.
// They stay queued until their URLs expire, so that storage also loses an
// upload that arrives after this.
func (s *Service) removeStaged(ctx context.Context, recordingID, dir string) {
	if dir == "" {
		return
	}
	for _, format := range store.TranscriptFormats {
		if err := s.cfg.Objects.Remove(ctx, stagedKey(dir, format)); err != nil {
			log.Printf("transcripts: recording %s: remove its staged %s; it is removed when its URL expires: %v", recordingID, outputName(format), err)
		}
	}
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
