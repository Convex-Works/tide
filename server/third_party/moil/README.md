# moil Go SDK

The service side of [moil](../../README.md): let the people who use your
service lend it their machines, one approved job at a time. The SDK pairs
machines, keeps a WebSocket to each, publishes your job bundles and
schedules jobs onto the machines your policy allows. It implements
[spec/protocol.md](../../spec/protocol.md), version 1.

```
go get git.convex.works/ConvexWorks/moil/sdk/go
```

- `moil`: the SDK.
- `moiltest`: a fake machine for testing your service without the moil
  app.

It needs Go 1.24 and depends only on `github.com/coder/websocket` and
`github.com/BurntSushi/toml`.

## Mount the server

```go
store, err := moil.NewFileStore("data/moil-machines.json") // or your own moil.Store
srv, err := moil.NewServer(moil.Config{
	Name:            "klisi",
	VerificationURL: "https://klisi.example.com/machines/pair",
	Store:           store,
	Logger:          slog.Default(),
})
defer srv.Close()

mux.Handle("/moil/", http.StripPrefix("/moil", srv.Handler()))
```

Users add your service in the moil app by its moil base URL, here
`https://klisi.example.com/moil`. `VerificationURL` must be on the same
origin (scheme, host and port): machines refuse to pair with a service
whose confirmation page is anywhere else, since that's how one service
would relay another's pairing, having the user confirm, on the other
service's page, a machine of its choosing.

Machines need https, except on loopback addresses. Jobs' input and output
URLs need https too: machines accept plain http only to loopback hosts,
and only from a service they reach on loopback, as in local development,
so `Submit` refuses other http URLs.
For development against storage without TLS, `Config.AllowInsecureHTTP`
lifts that; machines then need their owners to allow insecure http in
their settings too.

`Store` keeps paired machines and the SHA-256 of their tokens, never the
tokens. `FileStore` suits one process and a few hundred machines; for more,
implement `moil.Store` over your database and check it with
`moiltest.TestStore`. Timing knobs (`BidWindow`, `LeaseTTL`, `MaxAttempts`,
`PingInterval`, …) default to the spec's values.

## Confirm pairings

Pairing is a device-code flow: the app shows a code like `WDJB-MJHT` and
opens `VerificationURL?code=WDJB-MJHT` in the browser, where the user is
signed in to your service. That page is yours:

```go
func (s *Service) pairPage(w http.ResponseWriter, r *http.Request) {
	user := s.mustSignIn(w, r)
	p, ok := s.moil.PendingPairing(r.URL.Query().Get("code"))
	if !ok { /* unknown or expired code: let them type it again */ }
	// Show p.Name, p.OS, p.Arch and p.Code, with Confirm and Deny.
}

func (s *Service) confirmPairing(w http.ResponseWriter, r *http.Request) {
	user := s.mustSignIn(w, r)
	code := r.FormValue("code")
	if r.FormValue("action") == "deny" {
		s.moil.DenyPairing(code)
		return
	}
	m, err := s.moil.ConfirmPairing(r.Context(), code, user.ID) // user.ID becomes m.Owner
	...
}
```

Codes are compared case-insensitively, with or without the hyphen. The
app picks up its token on its next poll.

The confirmation page hands a machine the user's jobs, so guard it:

- **Warn against phishing** (RFC 8628 §5.4). An attacker can start
  pairing their own machine and send a victim the link; confirming it
  would send the victim's jobs, and their data, to the attacker. Say
  plainly on the page: only confirm a code your own moil app shows you.
  Show your moil base URL there too, and ask users to check that their
  app is pairing with it: a relay can have the app show your code, then
  send the user on to your page from one of its own.
- **Protect the confirmation from CSRF**, as any state-changing form: a
  CSRF token tied to the session, or refusing cross-site requests by
  their `Sec-Fetch-Site` or `Origin` header (`http.CrossOriginProtection`
  from Go 1.25). Otherwise another site could confirm an attacker's code
  from the user's browser.
- **Rate-limit `POST /v1/pair`** per client in front of the Handler. It
  needs no credentials; the Server bounds the pairings it holds
  (`MaxPendingPairings`, replacing the oldest unconfirmed one when full),
  but only a per-client limit keeps one client from crowding out others.

`srv.Machines(ctx, user.ID)` lists a user's machines: online or not, their
state (idle, busy, paused, offline), hardware, and the bundle hashes the
owner approved. `srv.RemoveMachine(ctx, id)` unpairs one; check it's the
user's own first.

## Publish bundles

A bundle is `job.py`, `job.py.lock` and `manifest.json`
([spec §5](../../spec/protocol.md#5-job-bundles)). Load and add every
bundle you'll send jobs for; machines see the list and their owners review
and approve each one by hash.

```go
//go:embed bundles
var bundles embed.FS

sub, _ := fs.Sub(bundles, "bundles/transcribe")
transcribe, err := moil.LoadBundle(sub) // validates and hashes
srv.AddBundle(transcribe)
```

`LoadBundle` refuses what machines refuse: a manifest outside spec §5.3,
or a lockfile that doesn't pin everything it installs (every artifact
hashed, sources over https, git pinned to a commit). `Packages` lists
what the lockfile installs, marking the packages machines build from
source, should your service want to show it as the app does.

A new version of a bundle has a new hash and needs a new approval, so
keep publishing the old version until owners have approved the new one.
`srv.RemoveBundle(old)` then retires it, or retires at once a version
found to be vulnerable: machines are offered none of its jobs any more,
an attempt already running finishes without being retried, and jobs
waiting for a machine fail with `moil.ErrBundleRemoved`.

## Submit jobs

```go
run, err := srv.Submit(ctx, moil.Job{
	ID:       "meeting-42-transcript", // optional; same ID while unfinished, same Run
	Bundle:   transcribe,
	Title:    "Weekly sync",
	Params:   map[string]any{"language": "en"},
	Inputs:   map[string]moil.Download{"audio.ogg": {URL: presignedGet}},
	Outputs:  map[string]moil.Upload{"transcript.json": {URL: presignedPut}},
	Eligible: moil.OwnedBy(room.OwnerID),               // who may see the inputs
	Prefer:   func(m moil.Machine) int { return len(m.GPUs) }, // optional
	Timeout:  time.Hour,
})

for ev := range run.Events(ctx) {
	switch ev.Kind {
	case moil.EventProgress:
		// ev.Fraction, ev.Message
	case moil.EventData:
		// ev.Data: the JSON value your script sent
	}
}
res, err := run.Wait(ctx)
```

- **Eligibility is required.** Whoever runs a machine can see the job's
  inputs, so say whose machines may take it: `moil.OwnedBy(owner)`, your
  own `func(moil.Machine) bool`, or `moil.AnyMachine` when anyone may.
  Policies run on the scheduler: keep them fast and don't call the Server
  from them. The Server asks them again whenever machines change; when
  your own data changes what they answer, as when a user shares a
  meeting, call `srv.Reschedule()`.
- **Jobs wait** without a time limit until an eligible, connected, idle
  machine that approved the bundle bids. Among bidders, `Prefer` ranks
  (highest score wins, ties to the earliest bid); retries rank machines that
  already failed the job last.
- **Pre-signed URLs expire.** If a job may wait a while or be retried, set
  `Prepare` instead of `Inputs` and `Outputs`: it makes each attempt's URLs
  as the attempt is assigned. A `*moil.JobError` with `Retryable` set
  tries again later, counting toward `MaxAttempts`; any other error ends
  the job. If the machine leaves, pauses, goes busy or withdraws its
  approval while `Prepare` runs, the attempt is given up unsent, like one
  the machine refused.
- **JSON values, not bytes.** `Params`, `ev.Data` and `Result.Meta` reach
  the other side as the same JSON value, but machines re-encode them, so
  key order, whitespace and the spelling of numbers may change. Keep them
  within I-JSON: send integers beyond 2^53, such as 64-bit IDs, as
  strings.
- **Results.** `Wait` returns a `*moil.Result` (the script's `Meta`, and the
  size and SHA-256 of each uploaded output), or an error: a
  `*moil.JobError` with the spec's error code, the error `Prepare`
  returned, `moil.ErrTooMuchData`, `moil.ErrCancelled` (only after
  `run.Cancel()`) or `moil.ErrClosed`.
- **Events** replay from the first for every reader and end with the run;
  the Server never waits for slow readers. Besides the script's `phase`,
  `progress`, `log` and `data`, you get `queued`, `assigned` (with the
  machine) and `retrying` (with the error). A run keeps its events within
  bounds a machine can't push past: consecutive progress events collapse
  into the latest, logs stop after 1 MiB, and an attempt sending more
  data than `Config.MaxDataBytes` (16 MiB) is stopped and its job fails
  with `moil.ErrTooMuchData`.
- **Leases and retries.** An attempt whose machine goes quiet for
  `LeaseTTL` fails with `lease_expired`; a machine that restarts without its
  attempt loses it (`lost`). Retryable failures are retried up to
  `MaxAttempts`, not counting attempts that never started, because
  machines refused them or backed out while `Prepare` ran; a job
  tolerates ten of those.
- `run.Cancel()` stops a job: at once if it's waiting or its machine is
  offline, otherwise when the machine confirms. It ends as cancelled
  unless its attempt succeeded before the machine saw the request. A
  machine that reports an attempt cancelled when you didn't cancel it has
  failed the attempt, as `interrupted`, and the job is retried.

Jobs live in memory. If the service restarts, unfinished jobs are gone and
machines drop their attempts; resubmit what must finish, by the same ID,
on startup. Submitting an ID whose job hasn't finished returns its `Run`;
once it has finished, the same ID runs again, so a retry button can
simply resubmit. `srv.Run(id)` finds a job while it runs and for
`KeepFinished` (an hour) after.

## Test with moiltest

`moiltest.Pair` pairs a fake machine with your Server over real HTTP; the
fake speaks the machine side of the protocol over a real WebSocket and
behaves like the app by default. Your test plays the script:

```go
func TestTranscriptionReachesTheRoom(t *testing.T) {
	svc := newTestService(t) // your service, with its *moil.Server
	owner := moiltest.Pair(t, svc.moil, "alice", moiltest.WithGPU("RTX 4090", 24<<30))
	stranger := moiltest.Pair(t, svc.moil, "bob")
	for _, m := range []*moiltest.Machine{owner, stranger} {
		m.Approve(svc.transcribeBundle)
		m.Connect()
	}

	svc.endMeeting(t, "alice") // submits the job

	a := owner.NextAttempt()
	audio := a.Input("audio.ogg") // downloads from your storage
	a.Progress(0.5, "transcribing")
	a.Output("transcript.json", fakeTranscript(audio)) // uploads to your storage
	a.Succeed(map[string]any{"speakers": 2})

	// ...assert your service stored the transcript...
	stranger.Sync()
	if len(stranger.Offers()) != 0 {
		t.Fatal("bob's machine was offered alice's meeting")
	}
}
```

Attempts can also fail (`a.Fail`, `a.ScriptError`), go silent until the
lease runs out (`a.GoSilent`), end cancelled unasked as a machine breaking
the protocol would (`a.CancelUnasked`), or carry on across
`m.Disconnect()` and `m.Connect()`; `m.Crash()` restarts the app without
its attempts. Use `m.OnOffer` to answer offers yourself, `m.Sync()` before
asserting that something didn't happen, and `moiltest.At(url)` to reach
the Server through your own router. Every message your service sends is
checked against the protocol.

The fake holds your service to what real machines do: it refuses to pair
unless your confirmation page is on the origin it reaches the Server at
(through `moiltest.At`, or `moiltest.New` and `StartPairing`); it
re-encodes params, data and meta as the app does, so compare them as JSON
values, not bytes; it refuses input and output URLs machines refuse
(`moiltest.WithInsecureHTTP()` for a service using
`Config.AllowInsecureHTTP`); and it drops data and refuses meta that a
machine wouldn't forward.

## Try it with the app

[examples/service](../../examples/service) is a small runnable service:

```
cd examples/service
go run . -bundles ../bundles/echo -data .demo
```

Then add `http://127.0.0.1:8787/moil` in the moil app.
