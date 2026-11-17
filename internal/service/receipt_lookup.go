package service

import (
	"net/http"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/receiptlookup"
)

// receiptLookupV2 reads only same-peer historical evidence. Public Serve keeps
// the proposed extension disabled; no request body can enable it.
func (s *Server) receiptLookupV2(w http.ResponseWriter, r *http.Request, uid uint32) {
	promote := func() error {
		if c, ok := r.Context().Value(connectionKey{}).(*peerConn); ok {
			return c.promoteRead()
		}
		return nil
	}
	receiptlookup.Handler{Operators: s.config.Operators, Ledger: s.receipts, Slots: s.receiptSlots, Promote: promote, Finite: s.proposedFiniteIO}.Serve(w, r, uid)
}
