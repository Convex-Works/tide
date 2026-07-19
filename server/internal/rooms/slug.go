package rooms

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
)

var firstWords = []string{
	"amber", "blue", "brave", "calm", "clear", "cool", "dawn", "deep",
	"fair", "fern", "fresh", "gentle", "glad", "gold", "green", "happy",
	"hazy", "kind", "light", "lilac", "little", "lucky", "mellow", "misty",
	"neat", "new", "open", "peace", "plain", "quiet", "rapid", "red",
	"round", "silver", "soft", "still", "sunny", "swift", "tidy", "tiny",
	"true", "velvet", "warm", "white", "wild", "wise", "young", "zen",
}

var secondWords = []string{
	"badger", "bay", "bear", "birch", "bird", "brook", "cloud", "cove",
	"crane", "deer", "dove", "dune", "elm", "field", "finch", "fox",
	"grove", "harbor", "hare", "hawk", "hill", "isle", "lake", "lark",
	"leaf", "lynx", "maple", "moon", "moss", "oak", "otter", "owl",
	"pine", "pond", "rain", "reef", "river", "robin", "seal", "shore",
	"sky", "sparrow", "star", "stone", "swift", "trail", "vale", "willow",
}

func GenerateSlug() (string, error) {
	return generateSlug(rand.Reader)
}

func generateSlug(reader io.Reader) (string, error) {
	first, err := randomIndex(reader, len(firstWords))
	if err != nil {
		return "", err
	}
	second, err := randomIndex(reader, len(secondWords))
	if err != nil {
		return "", err
	}
	number, err := randomIndex(reader, 900)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%03d", firstWords[first], secondWords[second], number+100), nil
}

func randomIndex(reader io.Reader, size int) (int, error) {
	value, err := rand.Int(reader, big.NewInt(int64(size)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}
