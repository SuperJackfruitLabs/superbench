package hoststorage

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func workerPolicyFixture(t *testing.T) (Worker, HostConfig, *[]string) {
	t.Helper()
	root := t.TempDir()
	state, data := filepath.Join(root, "state"), filepath.Join(root, "data")
	for _, p := range []string{state, data} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	calls := []string{}
	worker := Worker{State: state, Data: data, Probe: func(path string) (Space, error) {
		device := uint64(1)
		if path == state {
			device = 2
		}
		return Space{Device: device, Free: 128 << 20, Total: 1 << 30}, nil
	}, Apply: func(_ HostConfig, kind, key string, requested uint64) (Assignment, error) {
		if requested != 0 {
			t.Errorf("shared quota silently requested a cap: %d", requested)
		}
		calls = append(calls, kind+":"+key)
		return Assignment{}, nil
	}}
	config := HostConfig{DataRoot: data, QuotaHelper: "/fixture-quota-helper", MinDataFreeBytes: 1024, MinStateFreeBytes: 1024, MaxLogBytes: 1024, MaxLogsBytes: 1 << 20}.Defaults()
	return worker, config, &calls
}

func TestWorkerConfigurePreservesPolicyAndAllocatesOnlySharedResources(t *testing.T) {
	worker, config, calls := workerPolicyFixture(t)
	if err := worker.Configure(config, 0); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*calls, []string{"shared:store", "shared:mirrors"}) {
		t.Fatalf("unexpected allocations: %v", *calls)
	}
	path := filepath.Join(worker.State, "host.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("host policy is not private: %v", err)
	}
	stored, err := worker.Config()
	if err != nil || stored == nil || *stored != config {
		t.Fatalf("stored policy differs: %+v %v", stored, err)
	}
	*calls = nil
	if err := worker.Configure(config, 5); err != nil {
		t.Fatal("identical existing policy did not retry", err)
	}
	if !reflect.DeepEqual(*calls, []string{"shared:store", "shared:mirrors"}) {
		t.Fatalf("retry allocation sequence: %v", *calls)
	}
	*calls = nil
	changed := config
	changed.MinStateFreeBytes++
	if err := worker.Configure(changed, 5); err == nil {
		t.Fatal("changed existing policy silently adopted")
	}
	if len(*calls) != 0 {
		t.Fatal("conflicting policy allocated quota")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("conflicting policy changed host configuration")
	}
}

func TestWorkerAdmissionAndConfigurationRefuseBeforeQuotaEffects(t *testing.T) {
	for _, kind := range []string{"existing records", "same filesystem", "low data reserve", "low state reserve"} {
		t.Run(kind, func(t *testing.T) {
			worker, config, calls := workerPolicyFixture(t)
			existing := 0
			if kind == "existing records" {
				existing = 1
			} else {
				worker.Probe = func(path string) (Space, error) {
					device, free := uint64(1), uint64(128<<20)
					if path == worker.State {
						device = 2
					}
					if kind == "same filesystem" {
						device = 1
					}
					if kind == "low data reserve" && path == worker.Data || kind == "low state reserve" && path == worker.State {
						free = 1023
					}
					return Space{Device: device, Free: free}, nil
				}
			}
			if err := worker.Configure(config, existing); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
			if len(*calls) != 0 {
				t.Fatal("refused configuration allocated quotas")
			}
			if _, err := os.Lstat(filepath.Join(worker.State, "host.json")); !os.IsNotExist(err) {
				t.Fatalf("refused policy persisted: %v", err)
			}
			if kind != "existing records" {
				if err := save(worker.State, filepath.Join(worker.State, "host.json"), config); err != nil {
					t.Fatal(err)
				}
				if err := worker.Admit(); err == nil {
					t.Fatal("unsafe host admitted worker")
				}
				if len(*calls) != 0 {
					t.Fatal("refused admission allocated quota")
				}
			}
		})
	}
}

func TestWorkerLogReservationCountsAggregateAndScalarOwners(t *testing.T) {
	worker, config, _ := workerPolicyFixture(t)
	if err := save(worker.State, filepath.Join(worker.State, "host.json"), config); err != nil {
		t.Fatal(err)
	}
	groupLog := filepath.Join(worker.State, "run-group.log")
	if err := os.WriteFile(groupLog, []byte(strings.Repeat("x", 100)), 0600); err != nil {
		t.Fatal(err)
	}
	// Both record kinds reserve their unfilled active log budgets. The group has
	// no scalar repository fields and must still contribute its held execution.
	records := map[string]*model.Bench{
		"group":  {Workspace: &model.Workspace{}, Lease: &model.Lease{Execution: &model.Execution{Log: groupLog, LogLimitBytes: 4096}}},
		"legacy": {Lease: &model.Lease{Execution: &model.Execution{Log: filepath.Join(worker.State, "run-legacy.log"), LogLimitBytes: 2048}}},
	}
	outstanding := uint64(4096 - 100 + 2048)
	free := config.MinStateFreeBytes + config.MaxLogBytes + outstanding - 1
	worker.Probe = func(path string) (Space, error) {
		if path == worker.State {
			return Space{Device: 2, Free: free}, nil
		}
		return Space{Device: 1, Free: 1 << 30}, nil
	}
	if _, err := worker.ReserveLog(records); err == nil || !strings.Contains(err.Error(), "outstanding log allocations") {
		t.Fatalf("aggregate reservation omitted: %v", err)
	}
	free++
	limit, err := worker.ReserveLog(records)
	if err != nil || limit != config.MaxLogBytes {
		t.Fatalf("exact floor rejected: %d %v", limit, err)
	}
	raw, err := os.ReadFile(groupLog)
	if err != nil || len(raw) != 100 {
		t.Fatal("reservation modified existing log evidence")
	}
	entries, err := os.ReadDir(worker.State)
	if err != nil || len(entries) != 2 {
		t.Fatal("reservation allocated an unowned log or journal")
	}
}
