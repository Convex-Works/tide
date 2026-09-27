package transcripts

import "time"

// SetStopGrace sets how long the service goes on recording runs' ends once
// Run's context is done, for tests that stop it under storage or a
// database that hangs.
func (s *Service) SetStopGrace(d time.Duration) { s.stopGrace = d }
