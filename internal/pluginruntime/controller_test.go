package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestLoadRejectsNonSRNXBeforeManager(t *testing.T) {
	manager := newFakeManager()
	controller := newTestController(t, manager, newFakeFactory(nil), Config{})
	if err := controller.Load(context.Background(), "plugin.zip"); !errors.Is(err, ErrInvalidPackageExtension) {
		t.Fatalf("Load error = %v", err)
	}
	if manager.installCalls() != 0 {
		t.Fatalf("manager Install calls = %d", manager.installCalls())
	}
	closeController(t, controller)
}

func TestNewRequiresTwoRetainedVersionsForRollback(t *testing.T) {
	manager := newFakeManager()
	manager.retained = 1
	_, err := New(Config{Manager: manager, Factory: newFakeFactory(nil)})
	if err == nil || !strings.Contains(err.Error(), "retain at least 2 versions") {
		t.Fatalf("New error = %v", err)
	}
}

func TestLoadUnloadUsesInstalledEntrypointAndHooks(t *testing.T) {
	manager := newFakeManager()
	manager.addArchive("alpha.srnx", receipt("alpha", "1.0.0"))
	recorder := &eventRecorder{}
	factory := newFakeFactory(recorder)
	var ready pluginv1.PluginMetadata
	var dispatch DispatchFunc
	var stopped []string
	controller := newTestController(t, manager, factory, Config{Hooks: Hooks{
		OnReady: func(metadata pluginv1.PluginMetadata, call DispatchFunc) error {
			ready, dispatch = metadata, call
			return nil
		},
		OnStopped: func(id string) { stopped = append(stopped, id) },
	}})

	if err := controller.Load(context.Background(), "alpha.srnx"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	statuses := controller.List()
	if len(statuses) != 1 || statuses[0].ID != "alpha" || statuses[0].Version != "1.0.0" || statuses[0].State != "running" {
		t.Fatalf("statuses = %#v", statuses)
	}
	starts := factory.startSpecs()
	if len(starts) != 1 || starts[0].Entrypoint != filepath.Join(starts[0].VersionRoot, "bin", "plugin") {
		t.Fatalf("launch specs = %#v", starts)
	}
	if ready.ID != "alpha" || dispatch == nil {
		t.Fatalf("ready hook metadata=%#v dispatch=%v", ready, dispatch != nil)
	}
	if _, err := dispatch(context.Background(), validDispatch()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := controller.Unload(context.Background(), "alpha"); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if got := controller.List(); len(got) != 0 {
		t.Fatalf("statuses after unload = %#v", got)
	}
	if manager.uninstallCalls() != 0 {
		t.Fatalf("Unload deleted an installed package: calls=%d", manager.uninstallCalls())
	}
	if len(stopped) != 1 || stopped[0] != "alpha" {
		t.Fatalf("stopped hooks = %v", stopped)
	}
	closeController(t, controller)
}

func TestUpdateStopsOldProcessBeforeStartingNew(t *testing.T) {
	recorder := &eventRecorder{}
	manager := newFakeManager()
	manager.recorder = recorder
	manager.addArchive("v1.srnx", receipt("alpha", "1.0.0"))
	manager.addArchive("v2.srnx", receipt("alpha", "2.0.0"))
	factory := newFakeFactory(recorder)
	controller := newTestController(t, manager, factory, Config{})

	if err := controller.Load(context.Background(), "v1.srnx"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Load(context.Background(), "v2.srnx"); err != nil {
		t.Fatal(err)
	}
	if got := controller.List(); len(got) != 1 || got[0].Version != "2.0.0" || got[0].State != "running" {
		t.Fatalf("statuses = %#v", got)
	}
	events := recorder.snapshot()
	assertOrdered(t, events, "install:alpha@2.0.0", "stop:alpha@1.0.0", "start:alpha@2.0.0")
	closeController(t, controller)
}

func TestFailedUpdateRollsBackActiveVersionAndRestartsOldProcess(t *testing.T) {
	recorder := &eventRecorder{}
	manager := newFakeManager()
	manager.recorder = recorder
	manager.addArchive("v1.srnx", receipt("alpha", "1.0.0"))
	manager.addArchive("v2.srnx", receipt("alpha", "2.0.0"))
	factory := newFakeFactory(recorder)
	factory.failVersion("2.0.0", errors.New("startup rejected"))
	controller := newTestController(t, manager, factory, Config{})
	if err := controller.Load(context.Background(), "v1.srnx"); err != nil {
		t.Fatal(err)
	}

	err := controller.Load(context.Background(), "v2.srnx")
	if err == nil || !strings.Contains(err.Error(), "startup rejected") {
		t.Fatalf("update error = %v", err)
	}
	if active := manager.activeVersion("alpha"); active != "1.0.0" {
		t.Fatalf("active version = %q", active)
	}
	if got := controller.List(); len(got) != 1 || got[0].Version != "1.0.0" || got[0].State != "running" {
		t.Fatalf("runtime after rollback = %#v", got)
	}
	events := recorder.snapshot()
	assertOrdered(t, events,
		"stop:alpha@1.0.0", "start:alpha@2.0.0", "rollback:alpha@1.0.0", "start:alpha@1.0.0")
	closeController(t, controller)
}

func TestCapabilityMismatchStopsNewProcessAndRollsBackUpdate(t *testing.T) {
	recorder := &eventRecorder{}
	manager := newFakeManager()
	manager.recorder = recorder
	versionOne := receipt("alpha", "1.0.0")
	versionOne.Capabilities = []string{string(pluginv1.CapabilityCommands)}
	versionTwo := receipt("alpha", "2.0.0")
	versionTwo.Capabilities = []string{string(pluginv1.CapabilityCommands), string(pluginv1.CapabilityWatchers)}
	manager.addArchive("v1.srnx", versionOne)
	manager.addArchive("v2.srnx", versionTwo)
	factory := newFakeFactory(recorder)
	factory.reportCapabilities("1.0.0", pluginv1.CapabilityCommands)
	// Version two omits the watcher capability declared by its signed package.
	factory.reportCapabilities("2.0.0", pluginv1.CapabilityCommands)
	controller := newTestController(t, manager, factory, Config{})
	if err := controller.Load(context.Background(), "v1.srnx"); err != nil {
		t.Fatal(err)
	}

	err := controller.Load(context.Background(), "v2.srnx")
	if err == nil || !strings.Contains(err.Error(), "process capabilities") {
		t.Fatalf("capability mismatch error = %v", err)
	}
	if active := manager.activeVersion("alpha"); active != "1.0.0" {
		t.Fatalf("active version after mismatch = %q", active)
	}
	if got := controller.List(); len(got) != 1 || got[0].Version != "1.0.0" || got[0].State != "running" {
		t.Fatalf("runtime after capability rollback = %#v", got)
	}
	failed := factory.process("alpha", "2.0.0")
	if failed == nil || failed.stopCalls() != 1 {
		t.Fatalf("mismatched process was not stopped: %#v", failed)
	}
	assertOrdered(t, recorder.snapshot(),
		"start:alpha@2.0.0", "stop:alpha@2.0.0", "rollback:alpha@1.0.0", "start:alpha@1.0.0")
	closeController(t, controller)
}

func TestFactoryAndReadyHookCannotMutateOwnedSlices(t *testing.T) {
	manager := newFakeManager()
	installed := receipt("alpha", "1.0.0")
	installed.Capabilities = []string{string(pluginv1.CapabilityCommands)}
	installed.Permissions = []string{"storage.read"}
	manager.addArchive("alpha.srnx", installed)
	factory := newFakeFactory(nil)
	factory.reportCapabilities("1.0.0", pluginv1.CapabilityCommands)
	factory.mutateLaunchSpec(func(spec *LaunchSpec) {
		spec.Capabilities[0] = string(pluginv1.CapabilityWatchers)
		spec.Permissions[0] = "storage.write"
	})
	controller := newTestController(t, manager, factory, Config{Hooks: Hooks{
		OnReady: func(metadata pluginv1.PluginMetadata, _ DispatchFunc) error {
			metadata.Capabilities[0] = pluginv1.CapabilityWatchers
			return nil
		},
	}})

	if err := controller.Load(context.Background(), "alpha.srnx"); err != nil {
		t.Fatalf("Load after factory mutation: %v", err)
	}
	owned := controller.get("alpha")
	if owned == nil || len(owned.spec.Capabilities) != 1 || owned.spec.Capabilities[0] != string(pluginv1.CapabilityCommands) ||
		len(owned.spec.Permissions) != 1 || owned.spec.Permissions[0] != "storage.read" {
		t.Fatalf("controller-owned launch spec was mutated: %#v", owned)
	}
	process := factory.process("alpha", "1.0.0")
	if process.metadata.Capabilities[0] != pluginv1.CapabilityCommands {
		t.Fatalf("ready hook mutated process metadata: %#v", process.metadata.Capabilities)
	}
	closeController(t, controller)
}

func TestDispatchAllowsActionsDeclaredByManifestPermissions(t *testing.T) {
	manager := newFakeManager()
	installed := receipt("alpha", "1.0.0")
	installed.Permissions = []string{
		"client.block", "telegram.delete", "telegram.edit", "telegram.inline", "telegram.reply", "telegram.send",
	}
	manager.addArchive("alpha.srnx", installed)
	factory := newFakeFactory(nil)
	factory.respondWith("1.0.0",
		pluginv1.Action{ID: "reply", Kind: pluginv1.ActionReply, ChatID: 1},
		pluginv1.Action{ID: "edit", Kind: pluginv1.ActionEdit, ChatID: 1, MessageID: 2},
		pluginv1.Action{ID: "delete", Kind: pluginv1.ActionDelete, ChatID: 1, MessageID: 2},
		pluginv1.Action{ID: "send", Kind: pluginv1.ActionSend, ChatID: 1},
		pluginv1.Action{ID: "inline", Kind: pluginv1.ActionInlineAnswer, InlineQueryID: "query"},
		pluginv1.Action{ID: "client", Kind: pluginv1.ActionClient, ClientOperation: &pluginv1.ClientOperation{Name: "block"}},
	)
	var dispatch DispatchFunc
	controller := newTestController(t, manager, factory, Config{Hooks: Hooks{
		OnReady: func(_ pluginv1.PluginMetadata, call DispatchFunc) error {
			dispatch = call
			return nil
		},
	}})
	if err := controller.Load(context.Background(), "alpha.srnx"); err != nil {
		t.Fatal(err)
	}
	response, err := dispatch(context.Background(), validDispatch())
	if err != nil {
		t.Fatalf("permission-checked Dispatch: %v", err)
	}
	if len(response.Actions) != 6 {
		t.Fatalf("allowed actions = %#v", response.Actions)
	}
	closeController(t, controller)
}

func TestDispatchRejectsMissingPermissionAndMalformedResponse(t *testing.T) {
	tests := []struct {
		name       string
		permission string
		action     pluginv1.Action
		want       error
	}{
		{
			name: "missing send permission", permission: "telegram.reply",
			action: pluginv1.Action{ID: "send", Kind: pluginv1.ActionSend, ChatID: 1}, want: ErrPermissionDenied,
		},
		{
			name: "malformed action before permission check", permission: "telegram.send",
			action: pluginv1.Action{ID: "send", Kind: pluginv1.ActionSend}, want: pluginv1.ErrInvalidDTO,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newFakeManager()
			installed := receipt("alpha", "1.0.0")
			installed.Permissions = []string{test.permission}
			manager.addArchive("alpha.srnx", installed)
			factory := newFakeFactory(nil)
			factory.respondWith("1.0.0", test.action)
			var dispatch DispatchFunc
			controller := newTestController(t, manager, factory, Config{Hooks: Hooks{
				OnReady: func(_ pluginv1.PluginMetadata, call DispatchFunc) error {
					dispatch = call
					return nil
				},
			}})
			if err := controller.Load(context.Background(), "alpha.srnx"); err != nil {
				t.Fatal(err)
			}
			response, err := dispatch(context.Background(), validDispatch())
			if !errors.Is(err, test.want) {
				t.Fatalf("Dispatch error = %v, want %v", err, test.want)
			}
			if len(response.Actions) != 0 {
				t.Fatalf("rejected response leaked actions: %#v", response.Actions)
			}
			closeController(t, controller)
		})
	}
}

func TestFailedFirstStartRemovesOnlyFailedInstallation(t *testing.T) {
	manager := newFakeManager()
	manager.addArchive("alpha.srnx", receipt("alpha", "1.0.0"))
	factory := newFakeFactory(nil)
	factory.failVersion("1.0.0", errors.New("bad process"))
	controller := newTestController(t, manager, factory, Config{})

	if err := controller.Load(context.Background(), "alpha.srnx"); err == nil {
		t.Fatal("Load unexpectedly succeeded")
	}
	if manager.activeVersion("alpha") != "" || manager.uninstallCalls() != 1 {
		t.Fatalf("failed package remains active, active=%q uninstall_calls=%d",
			manager.activeVersion("alpha"), manager.uninstallCalls())
	}
	if got := controller.List(); len(got) != 0 {
		t.Fatalf("runtime state = %#v", got)
	}
	closeController(t, controller)
}

func TestCapacityIsBoundedAndFailedAdmissionRestoresDisk(t *testing.T) {
	manager := newFakeManager()
	manager.addArchive("alpha.srnx", receipt("alpha", "1.0.0"))
	manager.addArchive("beta.srnx", receipt("beta", "1.0.0"))
	controller := newTestController(t, manager, newFakeFactory(nil), Config{MaxPlugins: 1})
	if err := controller.Load(context.Background(), "alpha.srnx"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Load(context.Background(), "beta.srnx"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	if manager.activeVersion("beta") != "" {
		t.Fatalf("rejected plugin remains active: %q", manager.activeVersion("beta"))
	}
	if got := controller.List(); len(got) != 1 || got[0].ID != "alpha" {
		t.Fatalf("bounded statuses = %#v", got)
	}
	closeController(t, controller)
}

func TestListIsDeterministicAndUnexpectedExitIsReported(t *testing.T) {
	manager := newFakeManager()
	manager.addArchive("zeta.srnx", receipt("zeta", "1.0.0"))
	manager.addArchive("alpha.srnx", receipt("alpha", "1.0.0"))
	factory := newFakeFactory(nil)
	stopped := make(chan string, 2)
	controller := newTestController(t, manager, factory, Config{Hooks: Hooks{
		OnStopped: func(id string) { stopped <- id },
	}})
	for _, archive := range []string{"zeta.srnx", "alpha.srnx"} {
		if err := controller.Load(context.Background(), archive); err != nil {
			t.Fatal(err)
		}
	}
	if got := controller.List(); len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "zeta" {
		t.Fatalf("statuses = %#v", got)
	}
	factory.process("zeta", "1.0.0").crash(errors.New("boom"))
	select {
	case id := <-stopped:
		if id != "zeta" {
			t.Fatalf("stopped id = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("unexpected exit was not observed")
	}
	if got := controller.List(); len(got) != 2 || got[1].State != "failed" {
		t.Fatalf("statuses after crash = %#v", got)
	}
	closeController(t, controller)
}

func TestCloseStopsAllProcessesAndRejectsFurtherWork(t *testing.T) {
	manager := newFakeManager()
	manager.addArchive("alpha.srnx", receipt("alpha", "1.0.0"))
	manager.addArchive("beta.srnx", receipt("beta", "1.0.0"))
	factory := newFakeFactory(nil)
	controller := newTestController(t, manager, factory, Config{})
	for _, archive := range []string{"alpha.srnx", "beta.srnx"} {
		if err := controller.Load(context.Background(), archive); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := controller.List(); len(got) != 0 {
		t.Fatalf("statuses after Close = %#v", got)
	}
	for _, process := range factory.processes() {
		if process.stopCalls() != 1 {
			t.Fatalf("process %s stop calls = %d", process.key, process.stopCalls())
		}
	}
	if err := controller.Load(context.Background(), "alpha.srnx"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Load after close = %v", err)
	}
}

func TestManagerVerificationFailureNeverStartsProcess(t *testing.T) {
	manager := newFakeManager()
	manager.addArchiveError("corrupt.srnx", fmt.Errorf("%w: digest mismatch", pluginpackage.ErrIntegrity))
	factory := newFakeFactory(nil)
	controller := newTestController(t, manager, factory, Config{})
	if err := controller.Load(context.Background(), "corrupt.srnx"); !errors.Is(err, pluginpackage.ErrIntegrity) {
		t.Fatalf("Load error = %v", err)
	}
	if len(factory.startSpecs()) != 0 {
		t.Fatal("factory started an unverified package")
	}
	closeController(t, controller)
}

func TestReadyHookFailureStopsProcessAndRollsBackPackage(t *testing.T) {
	manager := newFakeManager()
	manager.addArchive("alpha.srnx", receipt("alpha", "1.0.0"))
	factory := newFakeFactory(nil)
	controller := newTestController(t, manager, factory, Config{Hooks: Hooks{
		OnReady: func(pluginv1.PluginMetadata, DispatchFunc) error { return errors.New("registration collision") },
	}})
	if err := controller.Load(context.Background(), "alpha.srnx"); err == nil || !strings.Contains(err.Error(), "registration collision") {
		t.Fatalf("Load error = %v", err)
	}
	process := factory.process("alpha", "1.0.0")
	if process == nil || process.stopCalls() != 1 {
		t.Fatalf("failed ready process was not stopped: %#v", process)
	}
	if manager.activeVersion("alpha") != "" || len(controller.List()) != 0 {
		t.Fatalf("failed ready state remains: active=%q runtime=%#v", manager.activeVersion("alpha"), controller.List())
	}
	closeController(t, controller)
}

func TestCloseCancelsInFlightStartAndJoinsLoad(t *testing.T) {
	manager := newFakeManager()
	manager.addArchive("alpha.srnx", receipt("alpha", "1.0.0"))
	factory := &blockingFactory{entered: make(chan struct{})}
	controller, err := New(Config{
		Manager: manager, Factory: factory, MaxPlugins: 1,
		StopTimeout: 250 * time.Millisecond, RollbackTimeout: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	loadResult := make(chan error, 1)
	go func() { loadResult <- controller.Load(context.Background(), "alpha.srnx") }()
	select {
	case <-factory.entered:
	case <-time.After(time.Second):
		t.Fatal("factory did not enter Start")
	}
	if got := controller.List(); len(got) != 1 || got[0].State != "starting" {
		t.Fatalf("status during start = %#v", got)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-loadResult:
		if err == nil {
			t.Fatal("cancelled Load succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Load was not joined by Close")
	}
	if got := controller.List(); len(got) != 0 {
		t.Fatalf("runtime after Close = %#v", got)
	}
	if manager.activeVersion("alpha") != "" {
		t.Fatalf("cancelled installation remains active: %q", manager.activeVersion("alpha"))
	}
}

func TestRestoreIsDeterministicContinuesAfterFailureAndDoesNotMutatePackages(t *testing.T) {
	manager := newFakeManager()
	manager.addArchive("zeta.srnx", receipt("zeta", "3.0.0"))
	manager.addArchive("beta.srnx", receipt("beta", "2.0.0"))
	manager.addArchive("alpha.srnx", receipt("alpha", "1.0.0"))
	for _, archive := range []string{"zeta.srnx", "beta.srnx", "alpha.srnx"} {
		if _, err := manager.Install(context.Background(), archive); err != nil {
			t.Fatal(err)
		}
	}
	factory := newFakeFactory(nil)
	factory.failVersion("2.0.0", errors.New("beta unavailable"))
	controller := newTestController(t, manager, factory, Config{})

	if err := controller.Restore(context.Background()); err == nil || !strings.Contains(err.Error(), "beta unavailable") {
		t.Fatalf("Restore error = %v", err)
	}
	statuses := controller.List()
	if len(statuses) != 3 || statuses[0].ID != "alpha" || statuses[0].State != "running" ||
		statuses[1].ID != "beta" || statuses[1].State != "failed" ||
		statuses[2].ID != "zeta" || statuses[2].State != "running" {
		t.Fatalf("restored statuses = %#v", statuses)
	}
	starts := factory.startSpecs()
	if got := []string{starts[0].ID, starts[1].ID, starts[2].ID}; fmt.Sprint(got) != "[alpha beta zeta]" {
		t.Fatalf("restore start order = %v", got)
	}
	if err := controller.Restore(context.Background()); err == nil {
		t.Fatal("second Restore unexpectedly hid retry failure")
	}
	starts = factory.startSpecs()
	if len(starts) != 4 || starts[3].ID != "beta" {
		t.Fatalf("idempotent restore starts = %#v", starts)
	}
	if manager.installCalls() != 3 || manager.uninstallCalls() != 0 {
		t.Fatalf("Restore mutated packages: installs=%d uninstalls=%d", manager.installCalls(), manager.uninstallCalls())
	}
	closeController(t, controller)
}

func newTestController(t *testing.T, manager *fakeManager, factory *fakeFactory, override Config) *Controller {
	t.Helper()
	override.Manager = manager
	override.Factory = factory
	if override.StopTimeout == 0 {
		override.StopTimeout = 250 * time.Millisecond
	}
	if override.RollbackTimeout == 0 {
		override.RollbackTimeout = 250 * time.Millisecond
	}
	controller, err := New(override)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func closeController(t *testing.T, controller *Controller) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func validDispatch() pluginv1.DispatchRequest {
	return pluginv1.DispatchRequest{Update: pluginv1.Update{
		ID: "update-1", Kind: pluginv1.UpdateMessage,
		Message: &pluginv1.Message{ID: 1, ChatID: 2, Text: "hello"},
	}}
}

func receipt(id, version string) pluginpackage.Receipt {
	return pluginpackage.Receipt{
		Format: pluginpackage.FormatV1, ID: id, Name: strings.ToUpper(id), Version: version,
		Category: pluginpackage.CategoryGoExecutable, ManifestDigest: "sha256:" + strings.Repeat(version[:1], 64),
		Entrypoint: pluginpackage.Entrypoint{OS: "test", Arch: "test", Path: "bin/plugin"},
	}
}

func assertOrdered(t *testing.T, events []string, expected ...string) {
	t.Helper()
	position := -1
	for _, wanted := range expected {
		found := -1
		for index := position + 1; index < len(events); index++ {
			if events[index] == wanted {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("event %q was not found after position %d in %v", wanted, position, events)
		}
		position = found
	}
}

type eventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *eventRecorder) add(event string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *eventRecorder) snapshot() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type fakeManager struct {
	mu          sync.Mutex
	archives    map[string]pluginpackage.Receipt
	archiveErrs map[string]error
	installed   map[string]map[string]pluginpackage.Receipt
	active      map[string]string
	installs    int
	uninstalls  int
	recorder    *eventRecorder
	retained    int
}

func newFakeManager() *fakeManager {
	return &fakeManager{
		archives: make(map[string]pluginpackage.Receipt), archiveErrs: make(map[string]error),
		installed: make(map[string]map[string]pluginpackage.Receipt), active: make(map[string]string), retained: 2,
	}
}

func (m *fakeManager) RetainVersions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.retained
}

func (m *fakeManager) addArchive(path string, value pluginpackage.Receipt) {
	m.mu.Lock()
	m.archives[path] = value
	m.mu.Unlock()
}

func (m *fakeManager) addArchiveError(path string, err error) {
	m.mu.Lock()
	m.archiveErrs[path] = err
	m.mu.Unlock()
}

func (m *fakeManager) Install(ctx context.Context, path string) (pluginpackage.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return pluginpackage.Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.installs++
	if err := m.archiveErrs[path]; err != nil {
		return pluginpackage.Receipt{}, err
	}
	value, exists := m.archives[path]
	if !exists {
		return pluginpackage.Receipt{}, pluginpackage.ErrInvalidPackage
	}
	if m.installed[value.ID] == nil {
		m.installed[value.ID] = make(map[string]pluginpackage.Receipt)
	}
	m.installed[value.ID][value.Version] = value
	m.active[value.ID] = value.Version
	m.recorder.add("install:" + value.ID + "@" + value.Version)
	return value, nil
}

func (m *fakeManager) List(ctx context.Context) ([]pluginpackage.InstalledPlugin, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.installed))
	for id := range m.installed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]pluginpackage.InstalledPlugin, 0, len(ids))
	for _, id := range ids {
		versions := make([]string, 0, len(m.installed[id]))
		for version := range m.installed[id] {
			versions = append(versions, version)
		}
		sort.Strings(versions)
		plugin := pluginpackage.InstalledPlugin{ID: id, ActiveVersion: m.active[id]}
		for _, version := range versions {
			value := m.installed[id][version]
			plugin.Name, plugin.Category = value.Name, value.Category
			plugin.Versions = append(plugin.Versions, pluginpackage.InstalledVersion{
				Receipt: value, Path: filepath.Join("/verified", id, version), Active: version == m.active[id],
			})
		}
		result = append(result, plugin)
	}
	return result, nil
}

func (m *fakeManager) Rollback(ctx context.Context, id, version string) (pluginpackage.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return pluginpackage.Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	value, exists := m.installed[id][version]
	if !exists {
		return pluginpackage.Receipt{}, pluginpackage.ErrNoRollback
	}
	m.active[id] = version
	m.recorder.add("rollback:" + id + "@" + version)
	return value, nil
}

func (m *fakeManager) Uninstall(ctx context.Context, id, version string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uninstalls++
	versions := m.installed[id]
	if versions == nil {
		return pluginpackage.ErrNotInstalled
	}
	delete(versions, version)
	if len(versions) == 0 {
		delete(m.installed, id)
		delete(m.active, id)
	} else if m.active[id] == version {
		keys := make([]string, 0, len(versions))
		for candidate := range versions {
			keys = append(keys, candidate)
		}
		sort.Strings(keys)
		m.active[id] = keys[len(keys)-1]
	}
	m.recorder.add("uninstall:" + id + "@" + version)
	return nil
}

func (m *fakeManager) installCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installs
}

func (m *fakeManager) uninstallCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.uninstalls
}

func (m *fakeManager) activeVersion(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[id]
}

type fakeFactory struct {
	mu                   sync.Mutex
	recorder             *eventRecorder
	failures             map[string]error
	reportedCapabilities map[string][]pluginv1.Capability
	responses            map[string]pluginv1.DispatchResponse
	mutateSpec           func(*LaunchSpec)
	starts               []LaunchSpec
	processBy            map[string]*fakeProcess
}

type blockingFactory struct {
	once    sync.Once
	entered chan struct{}
}

func (f *blockingFactory) Start(ctx context.Context, _ LaunchSpec) (Process, error) {
	f.once.Do(func() { close(f.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func newFakeFactory(recorder *eventRecorder) *fakeFactory {
	return &fakeFactory{
		recorder: recorder, failures: make(map[string]error),
		reportedCapabilities: make(map[string][]pluginv1.Capability), responses: make(map[string]pluginv1.DispatchResponse),
		processBy: make(map[string]*fakeProcess),
	}
}

func (f *fakeFactory) failVersion(version string, err error) {
	f.mu.Lock()
	f.failures[version] = err
	f.mu.Unlock()
}

func (f *fakeFactory) reportCapabilities(version string, capabilities ...pluginv1.Capability) {
	f.mu.Lock()
	f.reportedCapabilities[version] = append([]pluginv1.Capability(nil), capabilities...)
	f.mu.Unlock()
}

func (f *fakeFactory) mutateLaunchSpec(mutate func(*LaunchSpec)) {
	f.mu.Lock()
	f.mutateSpec = mutate
	f.mu.Unlock()
}

func (f *fakeFactory) respondWith(version string, actions ...pluginv1.Action) {
	f.mu.Lock()
	f.responses[version] = cloneDispatchResponse(pluginv1.DispatchResponse{Actions: actions})
	f.mu.Unlock()
}

func (f *fakeFactory) Start(ctx context.Context, spec LaunchSpec) (Process, error) {
	f.mu.Lock()
	err := f.failures[spec.Version]
	capabilities := append([]pluginv1.Capability(nil), f.reportedCapabilities[spec.Version]...)
	response := cloneDispatchResponse(f.responses[spec.Version])
	mutate := f.mutateSpec
	f.mu.Unlock()
	if mutate != nil {
		mutate(&spec)
	}
	f.mu.Lock()
	f.starts = append(f.starts, cloneLaunchSpec(spec))
	f.mu.Unlock()
	f.recorder.add("start:" + spec.ID + "@" + spec.Version)
	if err != nil {
		return nil, err
	}
	process := &fakeProcess{
		key: spec.ID + "@" + spec.Version, metadata: pluginv1.PluginMetadata{
			ID: spec.ID, Name: spec.Name, Version: spec.Version, ProtocolVersion: pluginv1.Version,
			Capabilities: capabilities,
		},
		done: make(chan error, 1), recorder: f.recorder, response: response,
	}
	f.mu.Lock()
	f.processBy[process.key] = process
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return process, err
	}
	return process, nil
}

func (f *fakeFactory) startSpecs() []LaunchSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]LaunchSpec, len(f.starts))
	for index, spec := range f.starts {
		result[index] = cloneLaunchSpec(spec)
	}
	return result
}

func (f *fakeFactory) process(id, version string) *fakeProcess {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.processBy[id+"@"+version]
}

func (f *fakeFactory) processes() []*fakeProcess {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]*fakeProcess, 0, len(f.processBy))
	for _, process := range f.processBy {
		result = append(result, process)
	}
	return result
}

type fakeProcess struct {
	key      string
	metadata pluginv1.PluginMetadata
	response pluginv1.DispatchResponse
	done     chan error
	once     sync.Once
	mu       sync.Mutex
	stops    int
	recorder *eventRecorder
}

func (p *fakeProcess) Metadata() pluginv1.PluginMetadata { return p.metadata }

func (p *fakeProcess) Dispatch(_ context.Context, request pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error) {
	_ = request
	return cloneDispatchResponse(p.response), nil
}

func (p *fakeProcess) Wait(ctx context.Context) error {
	select {
	case err := <-p.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *fakeProcess) Stop(_ context.Context) error {
	p.mu.Lock()
	p.stops++
	p.mu.Unlock()
	p.recorder.add("stop:" + p.key)
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *fakeProcess) crash(err error) {
	p.once.Do(func() {
		p.done <- err
		close(p.done)
	})
}

func (p *fakeProcess) stopCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stops
}
