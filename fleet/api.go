package fleet

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Handler serves the local (operator-facing) fleet API, mounted by the shelley
// server under /api/fleet/.
//
//	GET    /api/fleet              {joined:false} or id, name, addr, peers (+ StatusExtra)
//	POST   /api/fleet/init         {"name": …}            create a fleet
//	GET    /api/fleet/invite       {"invite": "tc…"}      token for others to join
//	POST   /api/fleet/join         {"name": …, "invite": "tc…"}
//	POST   /api/fleet/leave
//	GET    /api/fleet/kv?prefix=p  list entries
//	GET    /api/fleet/kv/{key...}  one entry
//	PUT    /api/fleet/kv/{key...}  write JSON body
//	DELETE /api/fleet/kv/{key...}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, err error) {
		code := http.StatusInternalServerError
		if errors.Is(err, ErrNotJoined) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
	}
	// withNode runs f against the node, or 409s if not joined.
	withNode := func(f func(http.ResponseWriter, *http.Request, *Node)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			n, err := s.Node()
			if err != nil {
				fail(w, err)
				return
			}
			f(w, r, n)
		}
	}
	decode := func(r *http.Request, v any) error {
		if r.ContentLength == 0 {
			return nil
		}
		return json.NewDecoder(r.Body).Decode(v)
	}

	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		n, err := s.Node()
		if err != nil {
			writeJSON(w, map[string]any{"joined": false})
			return
		}
		status := map[string]any{"joined": true, "id": n.ID(), "name": n.name, "addr": n.Addr(), "peers": n.Peers()}
		if s.StatusExtra != nil {
			for k, v := range s.StatusExtra() {
				status[k] = v
			}
		}
		writeJSON(w, status)
	})
	mux.HandleFunc("POST /api/fleet/init", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Init(req.Name); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/fleet/invite", func(w http.ResponseWriter, r *http.Request) {
		invite, err := s.Invite()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, map[string]string{"invite": invite})
	})
	mux.HandleFunc("POST /api/fleet/join", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name, Invite string }
		if err := decode(r, &req); err != nil || req.Invite == "" {
			http.Error(w, "body must be {\"invite\": ...}", http.StatusBadRequest)
			return
		}
		if err := s.Join(r.Context(), req.Name, req.Invite); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/fleet/leave", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Leave(r.Context()); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/fleet/kv", withNode(func(w http.ResponseWriter, r *http.Request, n *Node) {
		entries, err := n.List(r.Context(), r.URL.Query().Get("prefix"))
		if err != nil {
			fail(w, err)
			return
		}
		if entries == nil {
			entries = []Entry{}
		}
		writeJSON(w, entries)
	}))
	mux.HandleFunc("GET /api/fleet/kv/{key...}", withNode(func(w http.ResponseWriter, r *http.Request, n *Node) {
		e, ok, err := n.Get(r.Context(), r.PathValue("key"))
		if err != nil {
			fail(w, err)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, e)
	}))
	mux.HandleFunc("PUT /api/fleet/kv/{key...}", withNode(func(w http.ResponseWriter, r *http.Request, n *Node) {
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
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("DELETE /api/fleet/kv/{key...}", withNode(func(w http.ResponseWriter, r *http.Request, n *Node) {
		if err := n.Delete(r.Context(), r.PathValue("key")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
