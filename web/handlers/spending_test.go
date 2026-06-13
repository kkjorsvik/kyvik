package handlers_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSpendingPage_RequiresAuth(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.client.Get(ts.server.URL + "/spending")
	if err != nil {
		t.Fatalf("GET /spending failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}
}

func TestSpendingPage_RendersForViewer(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/spending", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	// Check key page elements are present.
	for _, needle := range []string{
		"Spending Dashboard",
		"Spend Today",
		"Spend This Month",
		"Tokens Today",
		"Active Agents",
		"dailyChart",
		"agentChart",
	} {
		if !strings.Contains(html, needle) {
			t.Errorf("expected page to contain %q", needle)
		}
	}
}

func TestSpendingChartData_JSON(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/spending/charts?range=7d", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	// Verify JSON has expected top-level keys.
	for _, key := range []string{`"daily"`, `"agents"`, `"providers"`} {
		if !strings.Contains(html, key) {
			t.Errorf("expected JSON to contain key %s", key)
		}
	}
}

func TestSpendingChartData_TimeRange(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// Both ranges should return valid JSON.
	for _, rng := range []string{"today", "7d", "30d", "month"} {
		resp := ts.authedGet(t, "/spending/charts?range="+rng, cookie)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("range=%s: expected 200, got %d", rng, resp.StatusCode)
		}
	}
}

func TestSpendingUpdateLimit_RequiresAuth(t *testing.T) {
	ts := newTestServer(t)

	form := url.Values{}
	form.Set("max_spend_per_day", "5.00")

	req, _ := http.NewRequest("POST", ts.server.URL+"/spending/limits/test-agent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("POST /spending/limits/test-agent failed: %v", err)
	}
	defer resp.Body.Close()

	// Should redirect to login since not authenticated.
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", resp.StatusCode)
	}
}

func TestSpendingUpdateLimit_Authenticated(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	form := url.Values{}
	form.Set("max_spend_per_day", "5.00")
	form.Set("max_spend_per_month", "50.00")

	resp := ts.authedPost(t, "/spending/limits/test-agent", cookie, form, true)
	defer resp.Body.Close()

	// Admin user should be able to update limits (200 OK with fragment).
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSpendingExportCSV(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/spending/export?range=7d", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("expected text/csv content type, got %q", ct)
	}

	disp := resp.Header.Get("Content-Disposition")
	if !strings.Contains(disp, "attachment") || !strings.Contains(disp, ".csv") {
		t.Errorf("expected CSV attachment disposition, got %q", disp)
	}

	body, _ := io.ReadAll(resp.Body)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")

	// Should have at least the header row.
	if len(lines) < 1 {
		t.Fatal("expected at least header row in CSV")
	}

	header := lines[0]
	for _, col := range []string{"agent_id", "agent_name", "total_tokens", "total_cost", "request_count", "period"} {
		if !strings.Contains(header, col) {
			t.Errorf("expected CSV header to contain %q, got %q", col, header)
		}
	}
}

func TestSpendingExportCSV_TimeRange(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	// All ranges should return valid CSV.
	for _, rng := range []string{"today", "7d", "30d", "month"} {
		resp := ts.authedGet(t, "/spending/export?range="+rng, cookie)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("range=%s: expected 200, got %d", rng, resp.StatusCode)
		}

		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/csv") {
			t.Errorf("range=%s: expected text/csv, got %q", rng, ct)
		}
	}
}

func TestSpendingProviderDrillDown(t *testing.T) {
	ts := newTestServer(t)
	cookie := ts.login(t)

	resp := ts.authedGet(t, "/spending/providers/openrouter?range=7d", cookie)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html, got %q", ct)
	}
}
