package speakers

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	ada   = Speaker{Identity: "guest:1", Name: "Ada"}
	ada2  = Speaker{Identity: "guest:2", Name: "Ada"}
	grace = Speaker{Identity: "guest:3", Name: "Grace"}
)

// epoch is a moment of the meeting's, well before any of these spans.
var epoch = time.Unix(1_800_000_000, 0)

// at is seconds after epoch.
func at(seconds float64) time.Time {
	return epoch.Add(time.Duration(seconds * float64(time.Second)))
}

func timelineOf(a *Activity, start float64, length time.Duration) map[string][][2]float64 {
	found := map[string][][2]float64{}
	for _, participant := range a.Timeline(at(start), length).Participants {
		found[participant.Name] = participant.Spans
	}
	return found
}

func wantTimeline(t *testing.T, got, want map[string][][2]float64) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("timeline = %v, want %v", got, want)
	}
}

// Times count from the start of the file, not from when the observer heard
// anything, and each name holds its own spans.
func TestTimelineIsInSecondsFromTheFilesStart(t *testing.T) {
	a := NewActivity()
	a.Update(at(10), []Speaker{ada})
	a.Update(at(12), []Speaker{ada, grace})
	a.Update(at(13.5), []Speaker{grace})
	a.Update(at(14), nil)
	wantTimeline(t, timelineOf(a, 8, time.Hour), map[string][][2]float64{
		"Ada":   {{2, 5.5}},
		"Grace": {{4, 6}},
	})
}

func TestUpdatesThatChangeNothingKeepASpanGoing(t *testing.T) {
	a := NewActivity()
	a.Update(at(1), []Speaker{ada})
	a.Update(at(2), []Speaker{ada})
	a.Update(at(3), []Speaker{ada})
	a.Update(at(4), nil)
	a.Update(at(5), nil)
	wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{"Ada": {{1, 4}}})
}

func TestTouchingSpansMerge(t *testing.T) {
	t.Run("stopping and starting on one moment", func(t *testing.T) {
		a := NewActivity()
		a.Update(at(1), []Speaker{ada})
		a.Update(at(2), nil)
		a.Update(at(2), []Speaker{ada})
		a.Update(at(3), nil)
		wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{"Ada": {{1, 3}}})
		if a.total != 1 {
			t.Fatalf("holding %d spans for what merged into one", a.total)
		}
	})
	t.Run("within what the file can tell apart", func(t *testing.T) {
		a := NewActivity()
		a.Update(at(1), []Speaker{ada})
		a.Update(at(2.01), nil)
		a.Update(at(2.04), []Speaker{ada})
		a.Update(at(3), nil)
		wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{"Ada": {{1, 3}}})
	})
	t.Run("a gap stays a gap", func(t *testing.T) {
		a := NewActivity()
		a.Update(at(1), []Speaker{ada})
		a.Update(at(2), nil)
		a.Update(at(2.5), []Speaker{ada})
		a.Update(at(3), nil)
		wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{"Ada": {{1, 2}, {2.5, 3}}})
	})
}

// Identities are per connection: someone who rejoins has a new one and the
// same name, and spans go by the name, overlapping or not.
func TestOneNameAcrossTwoIdentities(t *testing.T) {
	a := NewActivity()
	a.Update(at(1), []Speaker{ada})
	a.Update(at(2), nil)
	a.Update(at(5), []Speaker{ada2})
	a.Update(at(6), []Speaker{ada, ada2})
	a.Update(at(7), []Speaker{ada})
	a.Update(at(8), nil)
	wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{"Ada": {{1, 2}, {5, 8}}})
}

// Connections can overlap so that a span ends before an older one does, and
// so reach the timeline out of order.
func TestOverlappingConnectionsComeOutInOrder(t *testing.T) {
	a := NewActivity()
	a.Update(at(10), []Speaker{ada})
	a.Update(at(11), []Speaker{ada, ada2})
	a.Update(at(12), []Speaker{ada})
	a.Update(at(13), nil)
	a.close(openSpan{name: "Ada", start: at(1).UnixNano()}, at(2).UnixNano())
	wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{"Ada": {{1, 13}}})
}

func TestRenamedMidSentenceIsANewSpan(t *testing.T) {
	a := NewActivity()
	a.Update(at(1), []Speaker{ada})
	a.Update(at(2), []Speaker{{Identity: ada.Identity, Name: "Ada L."}})
	a.Update(at(3), nil)
	wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{"Ada": {{1, 2}}, "Ada L.": {{2, 3}}})
}

// The observer joins before the file begins, and may stop after it ended.
func TestSpansAreClampedToTheFile(t *testing.T) {
	a := NewActivity()
	a.Update(at(1), []Speaker{ada}) // before the file
	a.Update(at(2), nil)
	a.Update(at(9), []Speaker{ada}) // across its start
	a.Update(at(11), nil)
	a.Update(at(12), []Speaker{ada}) // inside
	a.Update(at(13), nil)
	a.Update(at(19), []Speaker{ada}) // across its end
	a.Update(at(21), nil)
	a.Update(at(22), []Speaker{ada, grace}) // after the file
	a.Update(at(23), nil)
	wantTimeline(t, timelineOf(a, 10, 10*time.Second), map[string][][2]float64{"Ada": {{0, 1}, {2, 3}, {9, 10}}})
}

func TestANameWithNoSpansInTheFileIsOmitted(t *testing.T) {
	a := NewActivity()
	a.Update(at(1), []Speaker{grace})
	a.Update(at(2), []Speaker{ada})
	a.Update(at(30), nil)
	wantTimeline(t, timelineOf(a, 10, time.Minute), map[string][][2]float64{"Ada": {{0, 20}}})
}

func TestTooShortToShowIsDropped(t *testing.T) {
	a := NewActivity()
	a.Update(at(1.01), []Speaker{ada})
	a.Update(at(1.02), nil)
	wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{})
}

// A span still open when the recording ends must not be lost: Stop ends
// them all.
func TestAnOpenSpanEndsWithNoOneActive(t *testing.T) {
	a := NewActivity()
	a.Update(at(1), []Speaker{ada})
	wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{})
	a.Update(at(2), nil)
	wantTimeline(t, timelineOf(a, 0, time.Hour), map[string][][2]float64{"Ada": {{1, 2}}})
}

// Memory is bounded however long a meeting flickers, and what's kept is the
// file's start, in good order.
func TestSpansAreBounded(t *testing.T) {
	a := NewActivity()
	moment := 0.0
	for range maxSpans + 10 {
		a.Update(at(moment), []Speaker{ada})
		a.Update(at(moment+1), nil)
		moment += 2
	}
	if a.total != maxSpans || a.Dropped != 10 {
		t.Fatalf("holding %d spans, dropped %d", a.total, a.Dropped)
	}
	spans := a.Timeline(epoch, 1000*time.Hour).Participants[0].Spans
	if len(spans) != maxSpans || spans[1] != [2]float64{2, 3} {
		t.Fatalf("%d spans, the second %v", len(spans), spans[1])
	}
}

// The shape the transcribe bundle reads, to the letter. One entry it doesn't
// take costs the whole file.
func TestTimelineJSON(t *testing.T) {
	a := NewActivity()
	a.Update(at(112.4), []Speaker{ada})
	a.Update(at(118), nil)
	a.Update(at(131.2), []Speaker{ada})
	a.Update(at(133.9), nil)
	a.Update(at(140), []Speaker{{Identity: "guest:9", Name: `<b>"Grace"</b>`}})
	a.Update(at(141), nil)
	body, err := a.Timeline(at(100), time.Hour).JSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		V            int `json:"v"`
		Participants []struct {
			Name  string      `json:"name"`
			Spans [][]float64 `json:"spans"`
		} `json:"participants"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	if !strings.Contains(string(body), `{"name":"Ada","spans":[[12.4,18],[31.2,33.9]]}`) || !strings.HasPrefix(string(body), `{"v":1,"participants":[`) {
		t.Fatalf("timeline = %s", body)
	}
	if decoded.Participants[0].Name != `<b>"Grace"</b>` {
		t.Fatalf("first name = %q", decoded.Participants[0].Name)
	}
	empty, err := NewActivity().Timeline(epoch, time.Hour).JSON()
	if err != nil || string(empty) != `{"v":1,"participants":[]}` {
		t.Fatalf("empty timeline = %s, %v", empty, err)
	}
}
