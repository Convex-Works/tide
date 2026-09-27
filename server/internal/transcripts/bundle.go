package transcripts

import (
	"embed"
	"io/fs"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
)

// bundleFiles is the moil bundle klisi publishes, vendored from moil's
// examples/bundles/transcribe by scripts/vendor-moil.sh.
//
//go:embed bundle/job.py bundle/job.py.lock bundle/manifest.json
var bundleFiles embed.FS

// Bundle returns the bundle machines run to transcribe a recording: Nemotron
// 3 Diarization finds the speakers, Parakeet TDT 0.6B v3 transcribes them.
// Machine owners approve it by hash, so it only changes on purpose.
func Bundle() (*moil.Bundle, error) {
	files, err := fs.Sub(bundleFiles, "bundle")
	if err != nil {
		return nil, err
	}
	return moil.LoadBundle(files)
}
