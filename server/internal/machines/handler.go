// Package machines lets hosts pair their own computers with klisi through
// moil, and see and unpair them (ARCHITECTURE.md §8.1). The moil SDK serves
// the machines' side of the protocol; this package is the hosts' side: the
// /machines page's API. A machine always belongs to the signed-in host who
// confirmed its pairing code, and hosts only ever see their own machines.
package machines

import (
	"errors"
	"fmt"
	"net/http"
	"sync"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/httpx"
)

const (
	machineNotFound = "Machine not found."
	unknownCode     = "No machine is waiting with this code. Check the code, or start pairing again in the moil app."
)

// MaxMachinesPerHost is how many machines one host may pair. Every machine
// keeps a connection open to klisi, so the limit bounds what one account can
// make klisi hold.
const MaxMachinesPerHost = 10

var tooManyMachines = fmt.Sprintf(
	"You have %d machines paired, the most klisi allows. Unpair one you no longer use, then confirm this code again.",
	MaxMachinesPerHost)

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
	// confirming is held while a pairing is confirmed, so that two
	// confirmations at once can't both find room for one more machine.
	confirming sync.Mutex
}

func NewHandler(server *moil.Server, bundle *moil.Bundle, moilURL string) *Handler {
	return &Handler{moil: server, bundle: bundle, moilURL: moilURL}
}

// List serves GET api.MachinesPath: the session's machines, oldest first, as
// an api.MachinesResponse.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	session, ok := requireSession(w, r)
	if !ok {
		return
	}
	machines, err := h.moil.Machines(r.Context(), session.Sub)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load your machines. Try again.")
		return
	}
	response := api.MachinesResponse{
		Machines: make([]api.MachineInfo, 0, len(machines)),
		Bundle:   api.BundleInfo{Name: h.bundle.Name(), Version: h.bundle.Version(), Hash: h.bundle.Hash()},
		MoilURL:  h.moilURL,
	}
	for _, machine := range machines {
		response.Machines = append(response.Machines, h.machineInfo(machine))
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

// Remove serves DELETE api.MachinePath: unpairs one of the session's
// machines. Another host's machine is reported as not found.
func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	session, ok := requireSession(w, r)
	if !ok {
		return
	}
	machine, err := h.moil.Machine(r.Context(), r.PathValue("id"))
	if err == nil && machine.Owner != session.Sub {
		err = moil.ErrNotFound // never reveal that another host's machine exists
	}
	if err == nil {
		err = h.moil.RemoveMachine(r.Context(), machine.ID)
	}
	switch {
	case errors.Is(err, moil.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, machineNotFound)
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "Could not unpair the machine. Try again.")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// Pairing serves GET api.PairingPath: the machine waiting with that code, as
// an api.PairingInfo, so the host can check it's theirs before confirming.
func (h *Handler) Pairing(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}
	pairing, ok := h.moil.PendingPairing(r.PathValue("code"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, unknownCode)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.PairingInfo{
		Code: pairing.Code, Name: httpx.Plain(pairing.Name), OS: httpx.Plain(pairing.OS),
		Arch: httpx.Plain(pairing.Arch), AppVersion: httpx.Plain(pairing.AppVersion),
		ExpiresAt: pairing.Expires.Unix(), MoilURL: h.moilURL,
	})
}

// Confirm serves POST api.PairingConfirmPath: pairs the waiting machine with
// the session's host as its owner, and returns it as an api.MachineInfo.
// The request has no body to read: nothing in it could name another owner.
// A host who has MaxMachinesPerHost machines is refused, and the code stays
// pending, to be confirmed once they unpair one.
func (h *Handler) Confirm(w http.ResponseWriter, r *http.Request) {
	session, ok := requireSession(w, r)
	if !ok {
		return
	}
	code := r.PathValue("code")
	if _, ok := h.moil.PendingPairing(code); !ok {
		httpx.WriteError(w, http.StatusNotFound, unknownCode)
		return
	}
	h.confirming.Lock()
	defer h.confirming.Unlock()
	paired, err := h.moil.Machines(r.Context(), session.Sub)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not pair the machine. Try again.")
		return
	}
	if len(paired) >= MaxMachinesPerHost {
		httpx.WriteError(w, http.StatusConflict, tooManyMachines)
		return
	}
	machine, err := h.moil.ConfirmPairing(r.Context(), code, session.Sub)
	switch {
	case errors.Is(err, moil.ErrUnknownCode):
		httpx.WriteError(w, http.StatusNotFound, unknownCode)
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "Could not pair the machine. Try again.")
	default:
		httpx.WriteJSON(w, http.StatusCreated, h.machineInfo(machine))
	}
}

// Deny serves POST api.PairingDenyPath: turns the waiting machine away.
func (h *Handler) Deny(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(w, r); !ok {
		return
	}
	switch err := h.moil.DenyPairing(r.PathValue("code")); {
	case errors.Is(err, moil.ErrUnknownCode):
		httpx.WriteError(w, http.StatusNotFound, unknownCode)
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "Could not turn the machine away. Try again.")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// requireSession returns the signed-in host, or answers 401. Machines and
// pairing codes are personal, so no answer may be cached.
func requireSession(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	w.Header().Set("Cache-Control", "no-store")
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Authentication required.")
	}
	return session, ok
}

// machineInfo is a machine as the page shows it. Everything the machine
// reported about itself is shown as plain text (httpx.Plain).
func (h *Handler) machineInfo(machine moil.Machine) api.MachineInfo {
	info := api.MachineInfo{
		ID: machine.ID, Name: httpx.Plain(machine.Name), OS: httpx.Plain(machine.OS),
		Arch: httpx.Plain(machine.Arch), AppVersion: httpx.Plain(machine.AppVersion),
		PairedAt: machine.PairedAt.Unix(), State: machineState(machine.State),
		Approved: machine.HasApproved(h.bundle.Hash()),
	}
	if !machine.LastSeen.IsZero() {
		lastSeen := machine.LastSeen.Unix()
		info.LastSeenAt = &lastSeen
	}
	return info
}

// machineState is a moil state as one of the api.Machine* states.
func machineState(state moil.MachineState) string {
	switch state {
	case moil.Idle:
		return api.MachineIdle
	case moil.Busy:
		return api.MachineBusy
	case moil.Paused:
		return api.MachinePaused
	case moil.Offline:
		return api.MachineOffline
	default:
		// moil refuses states it doesn't know from machines, so this is a
		// state from a newer moil SDK. moil calls every machine but an
		// offline one connected, and offers jobs only to idle ones, so the
		// machine is connected and not free to take a transcript: busy.
		return api.MachineBusy
	}
}
