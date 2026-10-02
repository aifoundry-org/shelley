package fleet

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Handler serves the local (operator-facing) fleet API, mounted by the shelley
// server under /api/fleet/.
//
//	GET    /api/fleet              status: id, addr, peers
//	GET    /api/fleet/kv?prefix=p  list entries
//	GET    /api/fleet/kv/{key...}  one entry
//	PUT    /api/fleet/kv/{key...}  write JSON body
//	DELETE /api/fleet/kv/{key...}
func (n *Node) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": n.ID(), "name": n.cfg.Name, "addr": n.Addr(), "peers": n.Peers()})
	})
	mux.HandleFunc("GET /api/fleet/kv", func(w http.ResponseWriter, r *http.Request) {
		entries, err := n.List(r.Context(), r.URL.Query().Get("prefix"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if entries == nil {
			entries = []Entry{}
		}
		writeJSON(w, entries)
	})
	mux.HandleFunc("GET /api/fleet/kv/{key...}", func(w http.ResponseWriter, r *http.Request) {
		e, ok, err := n.Get(r.Context(), r.PathValue("key"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, e)
	})
	mux.HandleFunc("PUT /api/fleet/kv/{key...}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if strings.HasPrefix(key, "node/") {
			http.Error(w, "node/ keys are managed by the fleet", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !json.Valid(body) {
			http.Error(w, "body must be JSON", http.StatusBadRequest)
			return
		}
		if err := n.Put(r.Context(), key, json.RawMessage(body)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/fleet/kv/{key...}", func(w http.ResponseWriter, r *http.Request) {
		if err := n.Delete(r.Context(), r.PathValue("key")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
