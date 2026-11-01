package baton

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The HTTP sink against baton-spec's fake collector, in every scenario of
// collector/scenarios.json; what each checks is the file's "about". Skipped
// where python3 is missing, which the collector needs.

type collectorScenario struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	DelayMS int    `json:"delay_ms"`
	Events  int    `json:"events"`
	Expect  struct {
		Accepted *int `json:"accepted"`
		Dropped  *int `json:"dropped"`
	} `json:"expect"`
}

type collectorSpec struct {
	Settings struct {
		RequestTimeoutMS int `json:"request_timeout_ms"`
		ShutdownBudgetMS int `json:"shutdown_budget_ms"`
		BackoffBaseMS    int `json:"backoff_base_ms"`
	} `json:"settings"`
	Limits struct {
		WriteMS       int `json:"write_ms"`
		CloseMarginMS int `json:"close_margin_ms"`
	} `json:"limits"`
	Scenarios []collectorScenario `json:"scenarios"`
}

const collectorDir = "baton-spec/collector"

// startCollector returns the URL of a collector in the scenario's mode, or of
// port 1, where nothing listens, for "down".
func startCollector(t *testing.T, sc collectorScenario) string {
	t.Helper()
	if sc.Mode == "down" {
		return "http://127.0.0.1:1"
	}
	cmd := exec.Command("python3", collectorDir+"/fake_collector.py", "--mode", sc.Mode, "--delay-ms", strconv.Itoa(sc.DelayMS))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return "http://127.0.0.1:" + line[:len(line)-1]
}

type received struct {
	Accepted []int
	Requests int
}

func receivedBy(t *testing.T, url, mode string) received {
	t.Helper()
	var body received
	if mode == "down" {
		return body
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(url + "/_received")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestTheSinkKeepsItsPromisesAgainstABrokenCollector(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is missing; the fake collector needs it")
	}
	raw, err := os.ReadFile(collectorDir + "/scenarios.json")
	if err != nil {
		t.Fatalf("is the baton-spec submodule checked out? %v", err)
	}
	var spec collectorSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	for _, sc := range spec.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			logs := captureLogs(t)
			url := startCollector(t, sc)
			s := newHTTPSink(url, testKey)
			s.client.Timeout = ms(spec.Settings.RequestTimeoutMS)
			s.budget = ms(spec.Settings.ShutdownBudgetMS)
			s.backoff = ms(spec.Settings.BackoffBaseMS)

			for n := 1; n <= sc.Events; n++ {
				started := time.Now()
				s.write(testEvent(n))
				if took := time.Since(started); took > ms(spec.Limits.WriteMS) {
					t.Errorf("write %d took %v", n, took)
				}
			}
			started := time.Now()
			s.close(context.Background())
			if took := time.Since(started); took > ms(spec.Settings.ShutdownBudgetMS+spec.Limits.CloseMarginMS) {
				t.Errorf("close took %v", took)
			}
			select { // an event still being sent is counted when its send ends
			case <-s.done:
			case <-time.After(5 * time.Second):
				t.Fatal("the sink kept sending 5s after close")
			}

			// A panic Baton recovered from is one that raised somewhere unseen.
			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("logs: %s", logs)
			}
			r := receivedBy(t, url, sc.Mode)
			got := r.Accepted
			if sc.Mode != "down" && r.Requests < 1 {
				t.Error("the collector got no request")
			}
			dropped := 0
			for _, n := range processDrops.totals() {
				dropped += n
			}
			for _, n := range got {
				if n < 1 || n > sc.Events {
					t.Errorf("accepted %d, which was never sent", n)
				}
			}
			if len(got)+dropped < sc.Events {
				t.Errorf("%d accepted + %d counted < %d sent", len(got), dropped, sc.Events)
			}
			if want := sc.Expect.Accepted; want != nil && len(got) != *want {
				t.Errorf("accepted %v, want %d", got, *want)
			}
			if want := sc.Expect.Dropped; want != nil && dropped != *want {
				t.Errorf("counted %v, want %d", processDrops.totals(), *want)
			}
		})
	}
}
