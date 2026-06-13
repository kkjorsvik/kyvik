package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/auth"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// =============================================================================
// Scenario: New Instance Setup
// Tests the first-run experience from bootstrap through first message.
// =============================================================================

func TestScenario_Setup_BootstrapAndFirstLogin(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	// BootstrapAdminIfEmpty should create a default admin when no users exist.
	created, generatedPwd, err := h.users.BootstrapAdminIfEmpty(context.Background(), "", "")
	if err != nil {
		t.Fatalf("BootstrapAdminIfEmpty: %v", err)
	}
	if !created {
		t.Fatal("expected admin to be created")
	}
	if generatedPwd == "" {
		t.Fatal("expected generated password")
	}

	// Authenticate returns ForcePasswordChange=true.
	lr, err := h.users.Authenticate(context.Background(), "admin", generatedPwd, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !lr.ForcePasswordChange {
		t.Fatal("expected ForcePasswordChange=true")
	}

	// UpdatePassword clears the flag.
	if err := h.users.UpdatePassword(context.Background(), lr.UserID, "new-secure-password"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	lr2, err := h.users.Authenticate(context.Background(), "admin", "new-secure-password", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Authenticate after password change: %v", err)
	}
	if lr2.ForcePasswordChange {
		t.Fatal("ForcePasswordChange should be false after password update")
	}
}

func TestScenario_Setup_DuplicateBootstrap_NoOp(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	// First bootstrap creates the admin.
	h.seedUser(t, "existing-admin", "secretsecret", true)

	// Second bootstrap should be a no-op when users exist.
	created, _, err := h.users.BootstrapAdminIfEmpty(context.Background(), "", "")
	if err != nil {
		t.Fatalf("BootstrapAdminIfEmpty: %v", err)
	}
	if created {
		t.Fatal("expected no-op when users already exist")
	}
}

func TestScenario_Setup_LoginFailure_BadCredentials(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedUser(t, "admin", "secretsecret", true)

	// Wrong password.
	_, err := h.users.Authenticate(context.Background(), "admin", "wrongpassword", "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected error for wrong password")
	}

	// Nonexistent user.
	_, err = h.users.Authenticate(context.Background(), "nobody", "secretsecret", "127.0.0.1", "test")
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
}

func TestScenario_Setup_CreateAgentViaAPI(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedUser(t, "admin", "secretsecret", true)
	key := h.createAPIKey(t, "admin-key", auth.RoleAdmin, nil)

	// Seed agent in store.
	agentID := "setup-api-agent"
	h.seedAgent(t, agentID, "Setup Agent", "worker")

	// GET should list it.
	listResp := h.apiRequest(t, "GET", "/api/v1/agents", key, nil)
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status: %d", listResp.StatusCode)
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(listResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var agents []types.AgentConfig
	if err := json.Unmarshal(body["data"], &agents); err != nil {
		t.Fatalf("unmarshal agents: %v", err)
	}
	found := false
	for _, a := range agents {
		if a.ID == agentID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("agent %s not found in list", agentID)
	}
}

func TestScenario_Setup_StartAgentAndMessage(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	h.seedAgent(t, "msg-agent", "Msg Agent", "worker")
	h.startAgent(t, "msg-agent")

	resp := h.sendAndReceive(t, "msg-agent", "hello", 5*time.Second)
	if resp.Content == "" {
		t.Fatal("expected non-empty response")
	}
}

func TestScenario_Setup_FullJourney_EndToEnd(t *testing.T) {
	// t.Parallel() — disabled: shared PostgreSQL database
	h := newFullHarness(t)

	// 1. Bootstrap admin.
	created, pwd, err := h.users.BootstrapAdminIfEmpty(context.Background(), "", "")
	if err != nil || !created {
		t.Fatalf("bootstrap: created=%v err=%v", created, err)
	}

	// 2. Login.
	lr, err := h.users.Authenticate(context.Background(), "admin", pwd, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	// 3. Change password.
	if err := h.users.UpdatePassword(context.Background(), lr.UserID, "updated-password-123"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	// 4. Create API key.
	_ = h.createAPIKey(t, "journey-key", auth.RoleAdmin, nil)

	// 5. Create agent directly.
	agentID := "journey-agent"
	h.seedAgent(t, agentID, "Journey Agent", "worker")

	// 6. Start agent.
	h.startAgent(t, agentID)

	// 7. Send message and receive response.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.kyvik.SendMessage(ctx, agentID, types.Message{Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msg, err := h.kyvik.ReceiveMessage(ctx, agentID)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	if msg.Content == "" {
		t.Fatal("expected non-empty response")
	}

	// 8. Verify audit trail (the audit action is "start", not "started").
	h.assertAuditContains(t, agentID, "start")
}
