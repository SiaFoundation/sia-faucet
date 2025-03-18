package api

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"go.sia.tech/core/types"
	"go.sia.tech/faucet/faucet"
	"go.sia.tech/jape"
	"go.uber.org/zap"
)

type (
	reqCreateRequest struct {
		UnlockHash types.Address  `json:"unlockHash"`
		Amount     types.Currency `json:"amount"`
	}

	// An API routes requests to a faucet
	api struct {
		faucet *faucet.Faucet
		log    *zap.Logger
	}
)

func (a *api) handleGetRequest(jc jape.Context) {
	var requestID faucet.RequestID
	if err := requestID.UnmarshalText([]byte(jc.PathParam("id"))); err != nil {
		jc.Error(err, http.StatusBadRequest)
		return
	}
	request, err := a.faucet.Request(requestID)
	if errors.Is(err, faucet.ErrNotFound) {
		jc.Error(fmt.Errorf("unable to find request %v", requestID), http.StatusNotFound)
		return
	} else if err != nil {
		log.Println("[WARN] unable to get request:", err)
		jc.Error(errors.New("unable to get request"), http.StatusInternalServerError)
		return
	}
	jc.Encode(request)
}

func (a *api) handleCreateRequest(jc jape.Context) {
	log := a.log.Named("handleCreateRequest").With(zap.String("ip", jc.Request.RemoteAddr))
	var req reqCreateRequest
	if err := jc.Decode(&req); err != nil {
		jc.Error(err, http.StatusBadRequest)
		return
	} else if req.UnlockHash == (types.Address{}) {
		jc.Error(errors.New("unlock hash is required"), http.StatusBadRequest)
		return
	} else if req.Amount.IsZero() {
		jc.Error(errors.New("amount is required"), http.StatusBadRequest)
		return
	}

	ip := jc.Request.RemoteAddr
	if forwardedFor := jc.Request.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		addresses := strings.Split(forwardedFor, ",")
		ip = strings.TrimSpace(addresses[0])
	}
	if host, _, err := net.SplitHostPort(ip); err == nil { // remove the port if present
		ip = host
	}

	requestID, err := a.faucet.RequestAmount(req.UnlockHash, ip, req.Amount)
	if errors.Is(err, faucet.ErrCountExceeded) || errors.Is(err, faucet.ErrAmountExceeded) {
		jc.Error(err, http.StatusTooManyRequests)
		return
	} else if err != nil {
		log.Warn("unable to create request", zap.Error(err))
		jc.Error(errors.New("unable to create request"), http.StatusInternalServerError)
		return
	}

	request, err := a.faucet.Request(requestID)
	if err != nil { // should never fail
		log.Error("unable to get request", zap.Error(err))
		jc.Error(errors.New("unable to create request"), http.StatusInternalServerError)
		return
	}
	jc.Encode(request)
}

// New initializes an API router.
func New(f *faucet.Faucet, log *zap.Logger) http.Handler {
	a := &api{
		faucet: f,
		log:    log,
	}
	return jape.Mux(map[string]jape.Handler{
		"GET  /:id": a.handleGetRequest,
		"POST /":    a.handleCreateRequest,
	})
}
