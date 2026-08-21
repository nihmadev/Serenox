package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestMetricsBoundedAndCopied(t *testing.T) {
	metrics, err := NewMetrics(2)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"plugin": "alpha"}
	if err := metrics.Add("handled_total", labels, 1); err != nil {
		t.Fatal(err)
	}
	labels["plugin"] = "mutated"
	if err := metrics.Add("handled_total", map[string]string{"plugin": "alpha"}, 2); err != nil {
		t.Fatal(err)
	}
	if err := metrics.Set("depth", nil, 4); err != nil {
		t.Fatal(err)
	}
	if err := metrics.Add("third", nil, 1); !errors.Is(err, ErrSeriesLimit) {
		t.Fatalf("expected series limit, got %v", err)
	}
	points := metrics.Snapshot()
	if len(points) != 2 || points[1].Value != 3 || points[1].Labels["plugin"] != "alpha" {
		t.Fatalf("unexpected snapshot: %#v", points)
	}
	if metrics.RejectedSeries() != 1 {
		t.Fatalf("unexpected rejection count: %d", metrics.RejectedSeries())
	}
}

func TestHealthBounded(t *testing.T) {
	health, err := NewHealth(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := health.Set("telegram", HealthReady, "connected"); err != nil {
		t.Fatal(err)
	}
	if err := health.Set("plugin", HealthReady, ""); err == nil {
		t.Fatal("expected component cap")
	}
	if got := health.Snapshot(); len(got) != 1 || got[0].Component != "telegram" {
		t.Fatalf("unexpected health: %#v", got)
	}
}

func TestSamplerLifecycle(t *testing.T) {
	sampler, err := NewSampler(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := sampler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sampler.Start(context.Background()); err == nil {
		t.Fatal("second start should fail")
	}
	time.Sleep(3 * time.Millisecond)
	if snapshot := sampler.Snapshot(); snapshot.CapturedAt.IsZero() || snapshot.Goroutines == 0 {
		t.Fatalf("invalid resource snapshot: %#v", snapshot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sampler.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLoggerRedactsSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONLogger(&output, slog.LevelDebug)
	logger.Info("event", "plugin", "x", "bot_token", "sensitive", "group", slog.GroupValue(slog.String("password", "p")))
	text := output.String()
	if strings.Contains(text, "sensitive") || strings.Contains(text, `"p"`) || strings.Count(text, redacted) != 2 {
		t.Fatalf("redaction failed: %s", text)
	}
}
