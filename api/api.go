package api

import (
	"errors"
	"net"
	"net/http"

	"go.sia.tech/faucet/faucet"
	"go.sia.tech/jape"
	"go.sia.tech/siad/types"
)

type (
	reqCreateRequest struct {
		UnlockHash types.UnlockHash `json:"unlockHash"`
		Amount     types.Currency   `json:"amount"`
	}

	// An API routes requests to a faucet
	API struct {
		faucet *faucet.Faucet
	}
)

func (a *API) handleGetRequest(jc jape.Context) {
	var requestID faucet.RequestID
	if err := requestID.UnmarshalText([]byte(jc.PathParam("id"))); err != nil {
		jc.Error(err, http.StatusBadRequest)
		return
	}
	request, err := a.faucet.Request(requestID)
	if err != nil {
		jc.Error(err, http.StatusNotFound)
		return
	}
	jc.Encode(request)
}

func (a *API) handleCreateRequest(jc jape.Context) {
	var req reqCreateRequest
	if err := jc.Decode(&req); err != nil {
		jc.Error(err, http.StatusBadRequest)
		return
	} else if req.UnlockHash == (types.UnlockHash{}) {
		jc.Error(errors.New("unlock hash is required"), http.StatusBadRequest)
	} else if req.Amount.IsZero() {
		jc.Error(errors.New("amount is required"), http.StatusBadRequest)
	}

	ip := jc.Request.RemoteAddr
	if forwardedFor := jc.Request.Header.Get("X-Forwarded-For"); len(forwardedFor) != 0 {
		ip = forwardedFor
	}

	requestID, err := a.faucet.RequestAmount(req.UnlockHash, ip, req.Amount)
	if err != nil {
		jc.Error(err, http.StatusInternalServerError)
		return
	}

	request, err := a.faucet.Request(requestID)
	if err != nil {
		jc.Error(err, http.StatusInternalServerError)
		return
	}
	jc.Encode(request)
}

// Serve serves the API on the provided listener.
func (a *API) Serve(l net.Listener) error {
	return http.Serve(l, jape.Mux(map[string]jape.Handler{
		"GET /api/:id":      a.handleGetRequest,
		"POST /api/request": a.handleCreateRequest,
	}))
}

// New initializes an API router.
func New(f *faucet.Faucet) *API {
	return &API{
		faucet: f,
	}
}
