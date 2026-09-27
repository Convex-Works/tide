// Package machines lets hosts pair their own computers with klisi through
// moil, and see and unpair them (ARCHITECTURE.md §8.1). The moil SDK serves
// the machines' side of the protocol; this package is the hosts' side: the
// /machines page's API. A machine always belongs to the signed-in host who
// confirmed its pairing code, and hosts only ever see their own machines.
package machines

import (
	"net/http"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/httpx"
)

// Handler serves the machine and pairing routes of internal/api. Every route
// runs behind the session middleware; mutating ones also behind the CSRF
// check.
type Handler struct {
	moil *moil.Server
	// bundle is the transcription bundle klisi publishes: MachineInfo.Approved
	// says whether a machine's owner approved exactly this hash.
	bundle *moil.Bundle
	// moilURL is the moil base URL machines pair with: base URL + /moil.
	moilURL string
}

func NewHandler(server *moil.Server, bundle *moil.Bundle, moilURL string) *Handler {
	return &Handler{moil: server, bundle: bundle, moilURL: moilURL}
}

// List serves GET api.MachinesPath: the session's machines, oldest first, as
// an api.MachinesResponse.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotImplemented, "Not implemented yet.")
}

// Remove serves DELETE api.MachinePath: unpairs one of the session's
// machines. Another host's machine is reported as not found.
func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotImplemented, "Not implemented yet.")
}

// Pairing serves GET api.PairingPath: the machine waiting with that code, as
// an api.PairingInfo, so the host can check it's theirs before confirming.
func (h *Handler) Pairing(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotImplemented, "Not implemented yet.")
}

// Confirm serves POST api.PairingConfirmPath: pairs the waiting machine with
// the session's host as its owner, and returns it as an api.MachineInfo.
func (h *Handler) Confirm(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotImplemented, "Not implemented yet.")
}

// Deny serves POST api.PairingDenyPath: turns the waiting machine away.
func (h *Handler) Deny(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotImplemented, "Not implemented yet.")
}
