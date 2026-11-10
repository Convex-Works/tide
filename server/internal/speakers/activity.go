package speakers

import (
	"cmp"
	"encoding/json"
	"math"
	"slices"
	"sort"
	"time"
)

// maxSpans bounds what a recording's activity holds in memory, and so the
// size of its file. LiveKit reports who is active at most every 400 ms, so
// someone flickering on and off without pause reaches it after about 11
// hours; a meeting of people taking turns never does. Spans past it are
// dropped: the names it costs stay Speaker N.
const maxSpans = 200_000

// A Speaker is a participant LiveKit reports as active.
type Speaker struct {
	// Identity is per connection; Name is the display name, which is what
	// a person keeps when they rejoin.
	Identity, Name string
}

// Activity is who was active during a recording, in wall-clock time. It
// isn't safe for concurrent use.
type Activity struct {
	open  map[string]openSpan // by identity
	spans map[string][]span   // closed, by display name
	total int
	// Dropped counts the spans lost to maxSpans.
	Dropped int
}

type openSpan struct {
	name  string
	start int64
}

// A span is a stretch of activity, in Unix nanoseconds.
type span struct{ start, end int64 }

func NewActivity() *Activity {
	return &Activity{open: map[string]openSpan{}, spans: map[string][]span{}}
}

// Update says that at this moment exactly these participants are active:
// spans of anyone else end here, and spans of anyone new start here.
func (a *Activity) Update(at time.Time, active []Speaker) {
	now := at.UnixNano()
	names := make(map[string]string, len(active))
	for _, speaker := range active {
		names[speaker.Identity] = speaker.Name
	}
	for identity, open := range a.open {
		if name, ok := names[identity]; ok && name == open.name {
			continue
		}
		a.close(open, now)
		delete(a.open, identity)
	}
	for identity, name := range names {
		if _, ok := a.open[identity]; !ok {
			a.open[identity] = openSpan{name: name, start: now}
		}
	}
}

func (a *Activity) close(open openSpan, end int64) {
	spans := a.spans[open.name]
	// Someone who stopped and started on the same moment, or two connections
	// of one name overlapping, costs no new span.
	if n := len(spans); n > 0 && open.start <= spans[n-1].end {
		spans[n-1].start = min(spans[n-1].start, open.start)
		spans[n-1].end = max(spans[n-1].end, end)
		return
	}
	if a.total >= maxSpans {
		a.Dropped++
		return
	}
	a.spans[open.name] = append(spans, span{start: open.start, end: end})
	a.total++
}

// Timeline is the activity as the file beside a recording holds it, for the
// recording file that began at start and lasts length. Anything outside the
// file is dropped, and a name left with no spans is omitted.
func (a *Activity) Timeline(start time.Time, length time.Duration) Timeline {
	names := make([]string, 0, len(a.spans))
	for name := range a.spans {
		names = append(names, name)
	}
	sort.Strings(names)
	timeline := Timeline{V: 1, Participants: []Participant{}}
	for _, name := range names {
		if spans := fileSpans(a.spans[name], start.UnixNano(), int64(length)); len(spans) > 0 {
			timeline.Participants = append(timeline.Participants, Participant{Name: name, Spans: spans})
		}
	}
	return timeline
}

// fileSpans are spans in seconds from the file's start, in order, clamped to
// the file and merged where they touch. A tenth of a second is finer than
// LiveKit reports. A span that rounds to nothing is dropped.
func fileSpans(spans []span, start, length int64) [][2]float64 {
	offsets := make([][2]float64, 0, len(spans))
	for _, s := range spans {
		from := tenths(max(s.start-start, 0))
		to := tenths(min(s.end-start, length))
		if from < to {
			offsets = append(offsets, [2]float64{from, to})
		}
	}
	slices.SortFunc(offsets, func(a, b [2]float64) int { return cmp.Compare(a[0], b[0]) })
	merged := offsets[:0]
	for _, offset := range offsets {
		if n := len(merged); n > 0 && offset[0] <= merged[n-1][1] {
			merged[n-1][1] = max(merged[n-1][1], offset[1])
			continue
		}
		merged = append(merged, offset)
	}
	return merged
}

// tenths is a time in nanoseconds as seconds, to a tenth of a second.
func tenths(nanoseconds int64) float64 {
	return math.Round(float64(nanoseconds)/1e8) / 10
}

// Timeline is speakers.json, beside a recording: who was active when, in
// seconds from the start of the file. The transcribe bundle reads it, and a
// single entry it doesn't accept costs the whole file, so it holds only
// finite spans in order.
type Timeline struct {
	V            int           `json:"v"`
	Participants []Participant `json:"participants"`
}

// A Participant is everyone who used one display name.
type Participant struct {
	Name  string       `json:"name"`
	Spans [][2]float64 `json:"spans"`
}

// JSON is the timeline as stored.
func (t Timeline) JSON() ([]byte, error) {
	return json.Marshal(t)
}
