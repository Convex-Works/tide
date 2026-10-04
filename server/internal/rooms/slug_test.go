package rooms

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"klisi/internal/store"
)

var defaultSlugPattern = regexp.MustCompile(`^[a-z]{3}-[a-z]{4}-[a-z]{3}$`)

func TestGenerateSlugIsReadable(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		slug, err := GenerateSlug()
		if err != nil {
			t.Fatal(err)
		}
		if !defaultSlugPattern.MatchString(slug) {
			t.Fatalf("GenerateSlug() = %q, want abc-defg-hij", slug)
		}
		// A default slug is one a host could type in as their own.
		if err := validateSlug(slug); err != nil {
			t.Fatalf("GenerateSlug() = %q, which validateSlug refuses: %v", slug, err)
		}
		if seen[slug] {
			t.Fatalf("GenerateSlug() repeated %q within 1000 draws", slug)
		}
		seen[slug] = true
	}
}

// Each byte below 234 (9 × 26) is a letter, its value mod 26; 234 to 255
// are drawn again rather than favoring a–v.
func TestGenerateSlugRejectsBytesThatWouldBiasIt(t *testing.T) {
	random := []byte{
		0, 255, 1, 2, // a, (rejected), b, c
		234, 25, 26, 51, 233, // (rejected), z, a, z, z
		52, 240, 77, 103, // a, (rejected), z, z
		129, 130, 131, // z, a, b
	}
	reader := bytes.NewReader(random)
	slug, err := generateSlug(reader)
	if err != nil {
		t.Fatal(err)
	}
	if slug != "abc-zazz-azz" {
		t.Fatalf("generateSlug() = %q, want abc-zazz-azz", slug)
	}
	// It reads only the bytes it needs...
	if reader.Len() != 3 {
		t.Fatalf("generateSlug() left %d bytes unread, want 3", reader.Len())
	}
	// ...however the reader hands them out.
	slug, err = generateSlug(iotest.OneByteReader(bytes.NewReader(random)))
	if err != nil || slug != "abc-zazz-azz" {
		t.Fatalf("generateSlug() one byte at a time = %q, %v", slug, err)
	}
}

func TestGenerateSlugFailsWhenRandomnessRunsOut(t *testing.T) {
	// Nine letters' worth, and then only bytes it must reject.
	random := append(bytes.Repeat([]byte{0}, 9), bytes.Repeat([]byte{250}, 100)...)
	if slug, err := generateSlug(bytes.NewReader(random)); !errors.Is(err, io.EOF) {
		t.Fatalf("generateSlug() = %q, %v, want io.EOF", slug, err)
	}
	if _, err := generateSlug(iotest.ErrReader(errors.New("no entropy"))); err == nil || !strings.Contains(err.Error(), "no entropy") {
		t.Fatalf("generateSlug() with a failing reader: %v", err)
	}
}

// Feeding every byte value equally often gives every letter equally often:
// the rejected bytes leave no remainder to favor the first letters.
func TestGenerateSlugIsUniformOverEveryByte(t *testing.T) {
	var random []byte
	for range 100 {
		for b := range 256 {
			random = append(random, byte(b))
		}
	}
	reader := bytes.NewReader(random)
	counts := make(map[rune]int)
	for reader.Len() >= 64 {
		slug, err := generateSlug(reader)
		if err != nil {
			break // the rest of the bytes run out mid-slug
		}
		for _, letter := range strings.ReplaceAll(slug, "-", "") {
			counts[letter]++
		}
	}
	if len(counts) != 26 {
		t.Fatalf("letters drawn: %d, want 26", len(counts))
	}
	least, most := math.MaxInt, 0
	for _, count := range counts {
		least, most = min(least, count), max(most, count)
	}
	// 100 rounds of 234 accepted bytes are 900 of each letter; slugs stop
	// short of the last few bytes, so allow a slug's worth of difference.
	if most-least > 10 {
		t.Fatalf("letter counts range from %d to %d: %v", least, most, counts)
	}
}

// From crypto/rand, every letter in every position is about as likely.
func TestGenerateSlugDistribution(t *testing.T) {
	const draws = 20000
	var counts [10][26]int
	for range draws {
		slug, err := GenerateSlug()
		if err != nil {
			t.Fatal(err)
		}
		for position, letter := range strings.ReplaceAll(slug, "-", "") {
			counts[position][letter-'a']++
		}
	}
	// Chi-squared with 25 degrees of freedom: the 99.99th percentile is
	// about 64.2, so a fair generator fails one position in 10,000 runs.
	expected := float64(draws) / 26
	for position, letters := range counts {
		var chi2 float64
		for _, count := range letters {
			chi2 += (float64(count) - expected) * (float64(count) - expected) / expected
		}
		if chi2 > 64.2 {
			t.Errorf("position %d: chi-squared %.1f over 25 degrees of freedom: %v", position, chi2, letters)
		}
	}
}

func TestCustomSlugValidation(t *testing.T) {
	// Rooms made before readable slugs keep their UUIDs.
	valid := []string{"abc", "team-weekly", "room-42", "abc-defg-hij", "00000000-0000-4000-8000-000000000000"}
	for _, slug := range valid {
		if err := validateSlug(slug); err != nil {
			t.Errorf("validateSlug(%q) = %v", slug, err)
		}
	}
	invalid := []string{"ab", "-team", "team-", "team--weekly", "team weekly", "team_weekly", "Team"}
	for _, slug := range invalid {
		if err := validateSlug(slug); err == nil {
			t.Errorf("validateSlug(%q) unexpectedly succeeded", slug)
		}
	}
	if got := normalizeSlug("  Team-Weekly "); got != "team-weekly" {
		t.Errorf("normalizeSlug() = %q", got)
	}
}

// collisionStore refuses the first collisions rooms it is given as taken.
type collisionStore struct {
	collisions int
	calls      int
	room       store.Room
}

func (s *collisionStore) CreateRoom(_ context.Context, room store.Room) error {
	s.calls++
	if s.calls <= s.collisions {
		return errors.New("constraint failed: UNIQUE constraint failed: rooms.slug (2067)")
	}
	s.room = room
	return nil
}

func testService(roomStore roomCreator, candidates ...string) *Service {
	service := NewService(roomStore)
	service.generateSlug = func() (string, error) {
		next := candidates[0]
		candidates = candidates[1:]
		return next, nil
	}
	service.generateID = func() (string, error) { return "room-id", nil }
	service.now = func() time.Time { return time.Unix(123, 0) }
	return service
}

func TestCreateRetriesSlugCollision(t *testing.T) {
	roomStore := &collisionStore{collisions: 1}
	service := testService(roomStore, "abc-defg-hij", "klm-nopq-rst")

	room, err := service.Create(context.Background(), "Weekly", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if roomStore.calls != 2 || room.Slug != "klm-nopq-rst" || room.Name != "Weekly" || roomStore.room != room {
		t.Fatalf("Create() = %#v after %d calls", room, roomStore.calls)
	}
}

func TestCreateWithAGivenSlugNeverReplacesIt(t *testing.T) {
	roomStore := &collisionStore{collisions: 1}
	service := testService(roomStore, "abc-defg-hij")

	room, err := service.Create(context.Background(), "Weekly", "team-weekly", "owner")
	if !errors.Is(err, ErrSlugTaken) || roomStore.calls != 1 {
		t.Fatalf("Create() = %#v, %v after %d calls, want ErrSlugTaken at once", room, err, roomStore.calls)
	}
}

func TestCreateNamesARoomWithoutANameAfterItsSlug(t *testing.T) {
	for _, test := range []struct{ slug, want string }{
		{"", "abc-defg-hij"},           // generated
		{"team-weekly", "team-weekly"}, // given
	} {
		roomStore := &collisionStore{}
		room, err := testService(roomStore, "abc-defg-hij").Create(context.Background(), "", test.slug, "owner")
		if err != nil {
			t.Fatal(err)
		}
		if room.Slug != test.want || room.Name != test.want || roomStore.room != room {
			t.Fatalf("Create(slug %q) = %#v, want slug and name %q", test.slug, room, test.want)
		}
	}
}
