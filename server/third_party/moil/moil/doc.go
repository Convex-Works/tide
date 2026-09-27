// Package moil is the service side of moil: it lets a web service hand
// jobs to machines its users have paired, one approved bundle at a time.
//
// A Server does four things, following spec/protocol.md:
//
//   - Pairing. A machine asks to pair and shows its owner a code; the
//     owner confirms it on the service's own page, where they're signed
//     in (PendingPairing, ConfirmPairing, DenyPairing). The machine gets a
//     token; the Store keeps only its hash.
//   - The registry. Machines keep one WebSocket open to the service and
//     report their state, hardware and the bundles their owners approved
//     (Machines, Machine, RemoveMachine).
//   - Bundles. The service publishes the code machines may run
//     (LoadBundle, AddBundle, RemoveBundle); owners review and approve
//     each by hash.
//   - Jobs. Submit offers a job to every connected, idle machine that
//     approved its bundle and that the service's policy allows (Job.Eligible),
//     assigns it to the best bidder (Job.Prefer), and follows the attempt:
//     leases, retries, resumption after reconnects, cancellation. The Run
//     it returns streams events and ends with a Result or an error.
//
// Mount the Server's Handler under the service's moil base URL:
//
//	srv, err := moil.NewServer(moil.Config{
//		Name:            "klisi",
//		VerificationURL: "https://klisi.example.com/machines/pair",
//		Store:           store,
//	})
//	mux.Handle("/moil/", http.StripPrefix("/moil", srv.Handler()))
//
// Machines and their tokens persist in the Store. Jobs live in memory:
// when the service restarts, unfinished jobs are gone and machines drop
// their attempts. A service that must see a job through resubmits it by
// ID on startup.
//
// Package moiltest provides a fake machine for testing a service without
// the moil app.
package moil
