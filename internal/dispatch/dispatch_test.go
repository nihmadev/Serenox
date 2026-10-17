package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestParserLegacyEligibility(t *testing.T) {
	t.Parallel()
	parser, err := NewParser("..", 42)
	if err != nil {
		t.Fatal(err)
	}
	base := Message{SenderID: 42, Text: "..HeLp   one two", MediaKind: MediaNone}
	command, ok := parser.Parse(base)
	if !ok {
		t.Fatal("command not parsed")
	}
	if command.Name != "help" || command.Args != "one two" || command.Prefix != ".." {
		t.Fatalf("command = %#v", command)
	}
	for name, mutate := range map[string]func(*Message){
		"other sender": func(m *Message) { m.SenderID = 7 },
		"only prefix":  func(m *Message) { m.Text = "..  " },
		"no prefix":    func(m *Message) { m.Text = ".help" },
		"sticker":      func(m *Message) { m.MediaKind = MediaSticker },
		"dice":         func(m *Message) { m.MediaKind = MediaDice },
		"audio":        func(m *Message) { m.MediaKind = MediaAudio },
		"via bot":      func(m *Message) { m.ViaBot = true },
	} {
		message := base
		mutate(&message)
		if got, ok := parser.Parse(message); ok {
			t.Errorf("%s parsed as %#v", name, got)
		}
	}
}

func TestParserValidation(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", "bad prefix", "\n", string(make([]byte, 33))} {
		if _, err := NewParser(prefix, 1); err == nil {
			t.Errorf("prefix %q accepted", prefix)
		}
	}
	if _, err := NewParser(".", 0); err == nil {
		t.Fatal("zero self ID accepted")
	}
}

func TestCommandRegistryAliasPrecedenceAndCoreProtection(t *testing.T) {
	t.Parallel()
	r, err := NewCommandRegistry(RegistryLimits{MaxCommands: 3, MaxAliases: 3})
	if err != nil {
		t.Fatal(err)
	}
	core := CommandTarget{Name: "help", PluginID: "core", HandlerID: "help", Core: true}
	if err := r.Register(core, "h", "HELP"); err != nil {
		t.Fatal(err)
	}
	if target, ok := r.Resolve("H"); !ok || target.PluginID != "core" || target.Name != "help" {
		t.Fatalf("Resolve alias = %#v, %v", target, ok)
	}
	if err := r.Register(CommandTarget{Name: "help", PluginID: "external", HandlerID: "bad"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("core overwrite error = %v", err)
	}
	if err := r.Register(CommandTarget{Name: "h", PluginID: "external", HandlerID: "exact"}); err != nil {
		t.Fatal(err)
	}
	if target, _ := r.Resolve("h"); target.HandlerID != "exact" {
		t.Fatalf("exact did not precede alias: %#v", target)
	}
	if err := r.Register(CommandTarget{Name: "other", PluginID: "other", HandlerID: "other"}, "h"); !errors.Is(err, ErrConflict) {
		t.Fatalf("alias conflict error = %v", err)
	}
	r.UnregisterPlugin("external")
	if target, _ := r.Resolve("h"); target.HandlerID != "help" {
		t.Fatalf("alias not restored after exact removal: %#v", target)
	}
	commands, aliases := r.Counts()
	if commands != 1 || aliases != 1 {
		t.Fatalf("counts = %d, %d", commands, aliases)
	}
}

func TestCommandRegistryBounds(t *testing.T) {
	t.Parallel()
	r, _ := NewCommandRegistry(RegistryLimits{MaxCommands: 1, MaxAliases: 1})
	if err := r.Register(CommandTarget{Name: "one", PluginID: "p", HandlerID: "one"}, "o"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(CommandTarget{Name: "two", PluginID: "p", HandlerID: "two"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("command limit error = %v", err)
	}
	if err := r.Register(CommandTarget{Name: "one", PluginID: "p", HandlerID: "one"}, "second"); !errors.Is(err, ErrLimit) {
		t.Fatalf("alias limit error = %v", err)
	}
}

func TestCompiledWatcherAllFilters(t *testing.T) {
	t.Parallel()
	parser, _ := NewParser(".", 10)
	filter, err := CompileWatcher(WatcherSpec{
		UpdateKinds: []string{"message"},
		Direction:   DirectionOutgoing, ChatKinds: []ChatKind{ChatGroup}, ExcludeChatKinds: []ChatKind{ChatChannel},
		MediaKinds: []MediaKind{MediaPhoto}, Forwarded: MatchRejected, Reply: MatchRequired,
		Mentioned: MatchRequired, ViaBot: MatchRejected, Editable: MatchRequired,
		TextPrefix: ".go", TextSuffix: "now", TextContains: " run ", TextRegex: `^\.go.*now$`,
		SenderIDs: []int64{10}, ChatIDs: []int64{-100}, Commands: CommandsOnly,
	}, parser)
	if err != nil {
		t.Fatal(err)
	}
	matching := Message{UpdateKind: "message", ChatID: -100, SenderID: 10, Text: ".go run now", Outgoing: true, ChatKind: ChatGroup, MediaKind: MediaPhoto, Reply: true, Mentioned: true, Editable: true}
	if !filter.Match(matching) {
		t.Fatal("matching message rejected")
	}
	cases := map[string]func(*Message){
		"update kind": func(m *Message) { m.UpdateKind = "edited_message" },
		"direction":   func(m *Message) { m.Outgoing = false }, "chat": func(m *Message) { m.ChatKind = ChatPrivate },
		"media": func(m *Message) { m.MediaKind = MediaVideo }, "forward": func(m *Message) { m.Forwarded = true },
		"reply": func(m *Message) { m.Reply = false }, "mention": func(m *Message) { m.Mentioned = false },
		"inline": func(m *Message) { m.ViaBot = true }, "editable": func(m *Message) { m.Editable = false },
		"text": func(m *Message) { m.Text = ".go later" }, "sender": func(m *Message) { m.SenderID = 11 },
		"chat ID": func(m *Message) { m.ChatID = 1 },
	}
	for name, mutate := range cases {
		m := matching
		mutate(&m)
		if filter.Match(m) {
			t.Errorf("%s mismatch accepted", name)
		}
	}
}

func TestWatcherCommandModes(t *testing.T) {
	t.Parallel()
	parser, _ := NewParser(".", 1)
	only, _ := CompileWatcher(WatcherSpec{Commands: CommandsOnly}, parser)
	exclude, _ := CompileWatcher(WatcherSpec{Commands: CommandsExclude}, parser)
	command := Message{SenderID: 1, Text: ".ping", MediaKind: MediaNone}
	ordinary := Message{SenderID: 2, Text: "hello", MediaKind: MediaNone}
	if !only.Match(command) || only.Match(ordinary) || exclude.Match(command) || !exclude.Match(ordinary) {
		t.Fatal("command modes incorrect")
	}
	if _, err := CompileWatcher(WatcherSpec{Commands: CommandsOnly}, nil); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("nil parser error = %v", err)
	}
	if _, err := CompileWatcher(WatcherSpec{TextRegex: "["}, parser); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("regex error = %v", err)
	}
}

func TestRouterFiltersBeforeEnqueueAndAppliesPolicy(t *testing.T) {
	t.Parallel()
	parser, _ := NewParser(".", 1)
	incoming, _ := CompileWatcher(WatcherSpec{Direction: DirectionIncoming}, parser)
	all, _ := CompileWatcher(WatcherSpec{}, parser)
	router, _ := NewWatcherRouter(3)
	if err := router.Register(WatcherTarget{PluginID: "p1", HandlerID: "incoming", Filter: incoming}); err != nil {
		t.Fatal(err)
	}
	if err := router.Register(WatcherTarget{PluginID: "p2", HandlerID: "all", Filter: all}); err != nil {
		t.Fatal(err)
	}
	message := Message{ChatID: 5, Outgoing: true}
	var calls atomic.Int32
	result := router.Route(context.Background(), message, func(_ context.Context, target WatcherTarget, _ Message) error {
		calls.Add(1)
		if target.PluginID == "p2" {
			return ErrLimit
		}
		return nil
	})
	if calls.Load() != 1 || result.Matched != 1 || result.Enqueued != 0 || result.Rejected != 1 {
		t.Fatalf("route = %#v, calls=%d", result, calls.Load())
	}
	if err := router.SetPolicy(WatcherPolicy{BlockedChats: []int64{5}}); err != nil {
		t.Fatal(err)
	}
	result = router.Route(context.Background(), message, func(context.Context, WatcherTarget, Message) error { calls.Add(1); return nil })
	if result.Matched != 0 || calls.Load() != 1 {
		t.Fatalf("blocked route invoked enqueue: %#v", result)
	}
}

func TestRouterRegistrationBoundAndDisableRules(t *testing.T) {
	t.Parallel()
	filter, _ := CompileWatcher(WatcherSpec{}, nil)
	router, _ := NewWatcherRouter(1)
	if err := router.Register(WatcherTarget{PluginID: "p", HandlerID: "one", Filter: filter}); err != nil {
		t.Fatal(err)
	}
	if err := router.Register(WatcherTarget{PluginID: "q", HandlerID: "two", Filter: filter}); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit error = %v", err)
	}
	if err := router.SetPolicy(WatcherPolicy{Disabled: map[string]DisableRule{"p": {Incoming: true}}}); err != nil {
		t.Fatal(err)
	}
	result := router.Route(context.Background(), Message{Outgoing: false}, func(context.Context, WatcherTarget, Message) error { return nil })
	if result.Matched != 0 {
		t.Fatalf("disabled incoming watcher matched: %#v", result)
	}
}

func TestRouterEnqueuesAtMostOncePerPluginForOverlappingWatchers(t *testing.T) {
	t.Parallel()
	filter, err := CompileWatcher(WatcherSpec{TextPrefix: "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewWatcherRouter(4)
	if err != nil {
		t.Fatal(err)
	}
	for _, handlerID := range []string{"first", "second"} {
		if err := router.Register(WatcherTarget{PluginID: "plugin", HandlerID: handlerID, Filter: filter}); err != nil {
			t.Fatal(err)
		}
	}
	var calls int
	result := router.Route(context.Background(), Message{Text: "hello world"}, func(context.Context, WatcherTarget, Message) error {
		calls++
		return nil
	})
	if result.Matched != 2 || result.Enqueued != 1 || result.Rejected != 0 || calls != 1 {
		t.Fatalf("route = %#v, calls=%d", result, calls)
	}
}

func TestRegistryConcurrentReadWrite(t *testing.T) {
	r, _ := NewCommandRegistry(RegistryLimits{MaxCommands: 128, MaxAliases: 128})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("c%d", i)
			_ = r.Register(CommandTarget{Name: name, PluginID: name, HandlerID: name}, "a"+name)
			for j := 0; j < 100; j++ {
				_, _ = r.Resolve(name)
			}
		}(i)
	}
	wg.Wait()
	commands, aliases := r.Counts()
	if commands != 32 || aliases != 32 {
		t.Fatalf("counts = %d,%d", commands, aliases)
	}
}

func BenchmarkCompiledWatcherMatch(b *testing.B) {
	parser, _ := NewParser(".", 1)
	filter, _ := CompileWatcher(WatcherSpec{Direction: DirectionOutgoing, TextRegex: `^\.ping(?:\s|$)`, Commands: CommandsOnly}, parser)
	message := Message{SenderID: 1, Outgoing: true, Text: ".ping now", MediaKind: MediaNone}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !filter.Match(message) {
			b.Fatal("mismatch")
		}
	}
}

func BenchmarkWatcherRouterRoute(b *testing.B) {
	filter, _ := CompileWatcher(WatcherSpec{TextPrefix: "hello"}, nil)
	router, _ := NewWatcherRouter(16)
	for index := 0; index < 8; index++ {
		_ = router.Register(WatcherTarget{
			PluginID: fmt.Sprintf("plugin-%d", index), HandlerID: "watcher", Filter: filter,
		})
	}
	message := Message{UpdateKind: "message", Text: "hello world"}
	ctx := context.Background()
	enqueue := func(context.Context, WatcherTarget, Message) error { return nil }
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result := router.Route(ctx, message, enqueue)
		if result.Enqueued != 8 {
			b.Fatalf("route = %#v", result)
		}
	}
}
