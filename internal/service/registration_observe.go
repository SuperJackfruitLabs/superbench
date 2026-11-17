package service

import (
	"net/http"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registrationobserve"
)

// The actual kernel peer and normal connection admission remain service-owned.
// Only the root-owned activation file enables this proposed observation route.
func (s *Server) registrationObservationV2(w http.ResponseWriter, r *http.Request, uid uint32) {
	registrationobserve.HTTPHandler{Context: s.ctx, Store: s.config.Store, Operators: s.config.Operators, Slots: s.slots, Admit: s.promoteNormal}.Serve(w, r, uid)
}
