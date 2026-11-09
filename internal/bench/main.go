// Command bench runs baton-spec's bench/bench.json against the baton-go this
// module requires and writes one result file. Only the public API is used, so
// the same program measures the last release and the checkout; bench.sh runs
// both.
//
//	go run . -source published|checkout -out RESULT.json [-only MEASURE]
package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	baton "github.com/good-timing/baton-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const key = "baton_pk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type measure struct {
	Name        string `json:"name"`
	Collector   string `json:"collector"`
	Warmup      int    `json:"warmup"`
	Calls       int    `json:"calls"`
	Concurrency int    `json:"concurrency"`
	ResultBytes int    `json:"result_bytes"`
}

var spec = filepath.Join("..", "..", "baton-spec")

// collectors are killed by fatal too: os.Exit skips withCollector's defer,
// and a hang-mode collector never exits on its own.
var collectors sync.Map

func fatal(v ...any) {
	collectors.Range(func(cmd, _ any) bool {
		_ = cmd.(*exec.Cmd).Process.Kill()
		return true
	})
	log.Fatal(v...)
}

// withCollector runs use with the port of a fake collector in mode.
func withCollector(mode string, use func(port int) map[string]float64) map[string]float64 {
	if mode == "down" {
		return use(1)
	}
	cmd := exec.Command("python3", filepath.Join(spec, "collector", "fake_collector.py"), "--mode", mode)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fatal(err)
	}
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	collectors.Store(cmd, nil)
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); collectors.Delete(cmd) }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		fatal(fmt.Sprintf("the fake collector printed no port: %v", err))
	}
	port, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		fatal(err)
	}
	return use(port)
}

// losses is emitted, delivered and lost for m's calls, a tool_call_start and a
// tool_call_end each.
func losses(port int, m measure) map[string]float64 {
	emitted := float64(2 * (m.Warmup + m.Calls))
	got := delivered(port, m.Collector)
	return map[string]float64{"emitted": emitted, "delivered": got, "lost": emitted - got}
}

func delivered(port int, mode string) float64 {
	if mode == "down" {
		return 0
	}
	client := http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/_received?lists=0", port))
	if err != nil {
		fatal(err)
	}
	defer response.Body.Close()
	var got struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		fatal(err)
	}
	return float64(got.Counts["tool_call_start"] + got.Counts["tool_call_end"])
}

type echoIn struct {
	Text string `json:"text"`
}

func echo(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
}

func large(size int) mcp.ToolHandlerFor[struct{}, any] {
	text := strings.Repeat("hello ", size/6+1)[:size]
	return func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	}
}

// echoClient is a session with the bench server, with Baton sending to port
// unless it is 0, and the large tool when resultBytes is set. Close the
// session, then the handle.
func echoClient(ctx context.Context, port, resultBytes int) (*mcp.ClientSession, *baton.Handle) {
	impl := &mcp.Implementation{Name: "bench", Version: "1.0.0"}
	server := mcp.NewServer(impl, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, echo)
	if resultBytes > 0 {
		mcp.AddTool(server, &mcp.Tool{Name: "large"}, large(resultBytes))
	}
	var handle *baton.Handle
	if port != 0 {
		dsn := fmt.Sprintf("http://%s@127.0.0.1:%d/ten_655b084e118b43f88992ee6357fcc23c/bench", key, port)
		var err error
		if handle, err = baton.Install(server, impl, nil, baton.Config{DSN: dsn}); err != nil {
			fatal(err)
		}
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "bench", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		fatal(err)
	}
	return session, handle
}

var echoCall = &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hello"}}

// timedCalls returns each call's ms, with concurrency calls in flight at once.
func timedCalls(ctx context.Context, session *mcp.ClientSession, call *mcp.CallToolParams, calls, concurrency int) []float64 {
	times := make([]float64, calls)
	var next atomic.Int64
	var workers sync.WaitGroup
	for range concurrency {
		workers.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= calls {
					return
				}
				started := time.Now()
				result, err := session.CallTool(ctx, call)
				times[i] = ms(time.Since(started))
				if err != nil || result.IsError {
					fatal(fmt.Sprintf("%s failed: %v %v", call.Name, err, result))
				}
			}
		})
	}
	workers.Wait()
	return times
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func pct(samples []float64, p float64) float64 {
	ordered := append([]float64(nil), samples...)
	sort.Float64s(ordered)
	return ordered[min(len(ordered)-1, int(p/100*float64(len(ordered))))]
}

var percentiles = []int{50, 99}

func overhead(ctx context.Context, m measure) map[string]float64 {
	return withCollector(m.Collector, func(port int) map[string]float64 {
		return added(ctx, m, port, echoCall)
	})
}

var largeCall = &mcp.CallToolParams{Name: "large", Arguments: map[string]any{}}

func largeResult(ctx context.Context, m measure) map[string]float64 {
	return withCollector(m.Collector, func(port int) map[string]float64 {
		out := added(ctx, m, port, largeCall)
		maps.Copy(out, losses(port, m))
		// Read on a second Baton server, untimed, so the full GCs stay out
		// of the timed calls. Its events come after the count above.
		session, handle := echoClient(ctx, port, m.ResultBytes)
		timedCalls(ctx, session, largeCall, m.Warmup, 1)
		before := liveHeap()
		timedCalls(ctx, session, largeCall, m.Calls, m.Concurrency)
		out["heap_growth_bytes"] = liveHeap() - before
		_ = session.Close()
		handle.Close(ctx)
		return out
	})
}

// added runs m's calls on a server without Baton, then on one with it, and
// returns the call times and what Baton adds to them.
func added(ctx context.Context, m measure, port int, call *mcp.CallToolParams) map[string]float64 {
	out := map[string]float64{}
	for _, side := range []struct {
		name string
		port int
	}{{"plain", 0}, {"baton", port}} {
		session, handle := echoClient(ctx, side.port, m.ResultBytes)
		timedCalls(ctx, session, call, m.Warmup, 1)
		times := timedCalls(ctx, session, call, m.Calls, m.Concurrency)
		_ = session.Close()
		handle.Close(ctx)
		for _, p := range percentiles {
			out[fmt.Sprintf("%s_p%d_ms", side.name, p)] = pct(times, float64(p))
		}
	}
	for _, p := range percentiles {
		out[fmt.Sprintf("added_p%d_ms", p)] = out[fmt.Sprintf("baton_p%d_ms", p)] - out[fmt.Sprintf("plain_p%d_ms", p)]
	}
	return out
}

func load(ctx context.Context, m measure) map[string]float64 {
	return withCollector(m.Collector, func(port int) map[string]float64 {
		session, handle := echoClient(ctx, port, 0)
		timedCalls(ctx, session, echoCall, m.Warmup, 1)
		started := time.Now()
		times := timedCalls(ctx, session, echoCall, m.Calls, m.Concurrency)
		wall := time.Since(started)
		_ = session.Close()
		started = time.Now()
		handle.Close(ctx)
		closing := time.Since(started)
		out := losses(port, m)
		out["wall_ms"] = ms(wall)
		out["call_p50_ms"] = pct(times, 50)
		out["call_p99_ms"] = pct(times, 99)
		out["close_ms"] = ms(closing)
		return out
	})
}

func liveHeap() float64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return float64(stats.HeapAlloc)
}

func memory(ctx context.Context, m measure) map[string]float64 {
	half := m.Calls / 2
	return withCollector(m.Collector, func(port int) map[string]float64 {
		session, handle := echoClient(ctx, port, 0)
		timedCalls(ctx, session, echoCall, m.Warmup, 1)
		heaps := []float64{liveHeap()}
		for range 2 {
			timedCalls(ctx, session, echoCall, half, m.Concurrency)
			heaps = append(heaps, liveHeap())
		}
		_ = session.Close()
		handle.Close(ctx)
		return map[string]float64{
			"heap_after_warmup_bytes": heaps[0],
			"heap_at_half_bytes":      heaps[1],
			"heap_at_end_bytes":       heaps[2],
			"growth_per_call_bytes":   (heaps[2] - heaps[1]) / float64(half),
		}
	})
}

var runners = map[string]func(context.Context, measure) map[string]float64{
	"overhead":      overhead,
	"large_result":  largeResult,
	"burst":         load,
	"outage_hang":   load,
	"outage_down":   load,
	"memory":        memory,
	"memory_outage": memory,
}

func maxRSSBytes() int64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		fatal(err)
	}
	if runtime.GOOS == "darwin" {
		return usage.Maxrss
	}
	return usage.Maxrss * 1024
}

func batonVersion() string {
	info, _ := debug.ReadBuildInfo()
	for _, dep := range info.Deps {
		if dep.Path == "github.com/good-timing/baton-go" && dep.Replace == nil {
			return strings.TrimPrefix(dep.Version, "v")
		}
	}
	return baton.Version
}

func cpuModel() string {
	if runtime.GOOS == "darwin" {
		raw, _ := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
		return cmp.Or(strings.TrimSpace(string(raw)), "unknown")
	}
	raw, _ := os.ReadFile("/proc/cpuinfo")
	for line := range strings.Lines(string(raw)) {
		if _, model, ok := strings.Cut(line, ":"); ok && strings.HasPrefix(line, "model name") {
			return strings.TrimSpace(model)
		}
	}
	return "unknown"
}

func main() {
	source := flag.String("source", "", "published or checkout")
	out := flag.String("out", "", "the result file")
	only := flag.String("only", "", "run this measure alone")
	flag.Parse()
	if (*source != "published" && *source != "checkout") || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	raw, err := os.ReadFile(filepath.Join(spec, "bench", "bench.json"))
	if err != nil {
		fatal(err)
	}
	var bench struct {
		Measures []measure `json:"measures"`
	}
	if err := json.Unmarshal(raw, &bench); err != nil {
		fatal(err)
	}
	ctx := context.Background()
	measures := map[string]map[string]float64{}
	var peak int64
	for _, m := range bench.Measures {
		run, ok := runners[m.Name]
		if !ok {
			fatal(fmt.Sprintf("bench.json has a measure this runner lacks: %s", m.Name))
		}
		if *only != "" && m.Name != *only {
			continue
		}
		if strings.HasPrefix(m.Name, "memory") && peak == 0 {
			peak = maxRSSBytes()
		}
		measures[m.Name] = run(ctx, m)
	}
	if *only != "" && len(measures) == 0 {
		fatal(fmt.Sprintf("bench.json has no measure %s", *only))
	}
	if peak == 0 {
		peak = maxRSSBytes()
	}
	result, err := json.MarshalIndent(map[string]any{
		"sdk":           "go",
		"version":       batonVersion(),
		"source":        *source,
		"runtime":       runtime.Version(),
		"cpu":           cpuModel(),
		"max_rss_bytes": peak,
		"measures":      measures,
	}, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, append(result, '\n'), 0o644); err != nil {
		fatal(err)
	}
}
