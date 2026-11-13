package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
)

const runtimeConnectionModelID = "catalog-fixture"

func newRuntimeConnectionManager(t *testing.T, service llm.Service, revision uint64) (*ConversationManager, *models.Manager) {
	t.Helper()
	s, database, _ := newTestServer(t)
	registry, err := models.NewManager(&models.Config{Models: []models.Built{{
		ID: runtimeConnectionModelID, Service: service, ServiceRevision: revision,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	s.llmManager = registry
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, strPtr(runtimeConnectionModelID),
		db.ConversationOptions{DisableNotifications: true})
	if err != nil {
		t.Fatal(err)
	}
	cm, err := s.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopActiveConversationLoops(s) })
	if err := cm.Hydrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := cm.ensureLoop(service, runtimeConnectionModelID); err != nil {
		t.Fatal(err)
	}
	return cm, registry
}

func refreshRuntimeConnection(t *testing.T, registry *models.Manager, service llm.Service, revision uint64) {
	t.Helper()
	if err := registry.RefreshBuiltModels([]models.Built{{
		ID: runtimeConnectionModelID, Service: service, ServiceRevision: revision,
	}}); err != nil {
		t.Fatal(err)
	}
}

func queueRuntimeMessage(t *testing.T, cm *ConversationManager, id, text string) {
	t.Helper()
	raw, err := json.Marshal(llm.UserStringMessage(text))
	if err != nil {
		t.Fatal(err)
	}
	// A queued message's old model must never replace the conversation's.
	if _, err := cm.db.AppendQueuedMessage(t.Context(), cm.conversationID, db.QueuedMessage{
		ID: id, Llm: raw, CreatedAt: time.Now(), Model: "stale-queued-model",
	}); err != nil {
		t.Fatal(err)
	}
}

func acceptRuntimeMessage(t *testing.T, cm *ConversationManager, service llm.Service, text string) {
	t.Helper()
	if _, err := cm.AcceptUserMessage(t.Context(), service, runtimeConnectionModelID, llm.UserStringMessage(text)); err != nil {
		t.Fatal(err)
	}
}

func requireRuntimeModel(t *testing.T, cm *ConversationManager) {
	t.Helper()
	conv, err := cm.db.GetConversationByID(t.Context(), cm.conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if cm.GetModel() != runtimeConnectionModelID || conv.Model == nil || *conv.Model != runtimeConnectionModelID || cm.loopSourceID != "" {
		t.Fatalf("connection refresh changed model identity: memory=%q row=%v source=%q", cm.GetModel(), conv.Model, cm.loopSourceID)
	}
}

func TestBuiltinConnectionRefreshNextTurn(t *testing.T) {
	for _, revisions := range []struct {
		name     string
		old, new uint64
	}{
		{"gateway-to-subscription", 1, 2},
		{"subscription-to-gateway", 2, 1},
		{"unchanged", 1, 1},
		// Zero is an unversioned connection: zero→zero is stable, but a
		// transition to or from a known revision must refresh the loop.
		{"unversioned", 0, 0},
		{"version-adopted", 0, 2},
		{"version-removed", 2, 0},
	} {
		for _, queued := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/queued=%t", revisions.name, queued), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					old, next := predictable.NewService(), predictable.NewService()
					cm, registry := newRuntimeConnectionManager(t, old, revisions.old)
					acceptRuntimeMessage(t, cm, old, "echo: original history")
					synctest.Wait()
					if cm.IsAgentWorking() {
						t.Fatal("initial turn did not finish")
					}
					originalLoop, originalTools, generation := cm.loop, cm.toolSet, cm.loopGeneration
					refreshRuntimeConnection(t, registry, next, revisions.new)
					if queued {
						queueRuntimeMessage(t, cm, "next", "echo: next turn")
						<-cm.drainPendingMessages()
					} else {
						// Even a caller holding the old service must get the
						// current connection from the model snapshot.
						acceptRuntimeMessage(t, cm, old, "echo: next turn")
					}
					synctest.Wait()
					requireRuntimeModel(t, cm)
					changed := revisions.old != revisions.new
					if changed {
						if cm.loop == originalLoop || cm.loopGeneration <= generation {
							t.Fatal("changed connection retained the old loop generation")
						}
					} else if cm.loop != originalLoop || cm.toolSet != originalTools || cm.loopGeneration != generation {
						t.Fatal("unchanged connection rebuilt the loop or tools")
					}
					wantOld, wantNext := 2, 0
					current := old
					if changed {
						wantOld, wantNext, current = 1, 1, next
					}
					if len(old.GetRecentRequests()) != wantOld || len(next.GetRecentRequests()) != wantNext {
						t.Fatalf("requests old=%d new=%d, want old=%d new=%d", len(old.GetRecentRequests()), len(next.GetRecentRequests()), wantOld, wantNext)
					}
					request := requestDump(current.GetLastRequest())
					if !strings.Contains(request, "original history") || !strings.Contains(request, "next turn") {
						t.Fatalf("refresh lost conversation history: %s", request)
					}
					if cm.IsAgentWorking() || len(queuedMessages(t, cm.db, cm.conversationID)) != 0 {
						t.Fatal("next turn did not settle and consume its queued work")
					}
				})
			})
		}
	}
}

func TestBuiltinConnectionRefreshDoesNotInterruptActiveTurn(t *testing.T) {
	for _, revisions := range [][2]uint64{{1, 2}, {2, 1}} {
		t.Run(fmt.Sprintf("%d-to-%d", revisions[0], revisions[1]), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				held, next := newHeldLLMService(), predictable.NewService()
				cm, registry := newRuntimeConnectionManager(t, held, revisions[0])
				acceptRuntimeMessage(t, cm, held, "echo: active turn")
				call := held.waitCall(t, "echo: active turn")
				originalLoop, generation, loopCtx := cm.loop, cm.loopGeneration, cm.loopCtx
				refreshRuntimeConnection(t, registry, next, revisions[1])
				if err := cm.ensureLoop(next, runtimeConnectionModelID); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if cm.loop != originalLoop || cm.loopGeneration != generation || loopCtx.Err() != nil || !cm.IsAgentWorking() {
					t.Fatal("connection refresh interrupted the active turn")
				}
				if len(next.GetRecentRequests()) != 0 {
					t.Fatal("active turn used the replacement connection")
				}
				call.Release()
				synctest.Wait()
				if cm.IsAgentWorking() {
					t.Fatal("original turn did not finish normally")
				}
				acceptRuntimeMessage(t, cm, held, "echo: after active turn")
				synctest.Wait()
				if len(next.GetRecentRequests()) != 1 || cm.loop == originalLoop {
					t.Fatal("next turn did not use the replacement connection")
				}
				requireRuntimeModel(t, cm)
			})
		})
	}
}

func TestBuiltinConnectionRefreshFailurePreservesQueue(t *testing.T) {
	for _, failure := range []string{"resolution", "removed", "rebuild"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				old, next := predictable.NewService(), predictable.NewService()
				cm, registry := newRuntimeConnectionManager(t, old, 1)
				acceptRuntimeMessage(t, cm, old, "echo: original history")
				synctest.Wait()
				originalLoop := cm.loop
				queueRuntimeMessage(t, cm, "first", "echo: queued first")
				queueRuntimeMessage(t, cm, "second", "echo: queued second")
				resolve := cm.resolveModelService
				fixtureErr := errors.New("connection fixture failure")
				refreshRuntimeConnection(t, registry, next, 2)
				switch failure {
				case "resolution":
					cm.resolveModelService = func(string) (llm.Service, string, uint64, error) {
						return nil, "", 0, fixtureErr
					}
				case "removed":
					if err := registry.RefreshBuiltModels(nil); err != nil {
						t.Fatal(err)
					}
				case "rebuild":
					cm.decorateService = func(llm.Service) (llm.Service, error) { return nil, fixtureErr }
				}
				cm.loopLifecycleMu.Lock()
				err := cm.feedQueued(context.Background(), queuedMessages(t, cm.db, cm.conversationID))
				cm.loopLifecycleMu.Unlock()
				if err == nil || (failure != "removed" && !errors.Is(err, fixtureErr)) {
					t.Fatalf("refresh error = %v", err)
				}
				synctest.Wait()
				requireRuntimeModel(t, cm)
				pending := queuedMessages(t, cm.db, cm.conversationID)
				if len(pending) != 2 || pending[0].ID != "first" || pending[1].ID != "second" {
					t.Fatalf("failed refresh changed the queue: %+v", pending)
				}
				if userMessageRowExists(t, cm.db, cm.conversationID, "queued first") ||
					userMessageRowExists(t, cm.db, cm.conversationID, "queued second") || cm.IsAgentWorking() {
					t.Fatal("failed refresh recorded or started queued work")
				}
				if failure == "rebuild" {
					if cm.loop != nil || cm.loopTearingDown {
						t.Fatal("failed rebuild left a loop or open teardown boundary")
					}
				} else if cm.loop != originalLoop {
					t.Fatal("failed resolution discarded the original loop")
				}
				cm.resolveModelService, cm.decorateService = resolve, nil
				refreshRuntimeConnection(t, registry, next, 2)
				<-cm.drainPendingMessages()
				synctest.Wait()
				requireRuntimeModel(t, cm)
				if len(queuedMessages(t, cm.db, cm.conversationID)) != 0 || len(next.GetRecentRequests()) != 1 {
					t.Fatal("queue could not resume on the restored connection")
				}
				request := requestDump(next.GetLastRequest())
				first, second := strings.Index(request, "queued first"), strings.Index(request, "queued second")
				if !strings.Contains(request, "original history") || first < 0 || second <= first {
					t.Fatalf("recovered queue lost history or FIFO order: %s", request)
				}
			})
		})
	}
}
