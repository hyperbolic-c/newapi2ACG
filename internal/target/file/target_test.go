package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
	"github.com/hyperbolic-c/newapi2ACG/test/contract"
)

func TestTargetContract(t *testing.T) {
	contract.Run(t, func() target.Target { return New(filepath.Join(t.TempDir(), "report.json")) })
}
func TestFileOutputDeterministic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	adapter := New(path)
	change := contract.Change("op", contract.Group("group:a", "a"))
	if _, err := adapter.Apply(context.Background(), []model.Change{change}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Apply(context.Background(), []model.Change{change}); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("replay changed output")
	}
}

type brokenReader struct{ sent bool }

func (r *brokenReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, []byte("partial")), nil
	}
	return 0, errors.New("injected read failure")
}
func TestFileWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(context.Background(), path, &brokenReader{}); err == nil {
		t.Fatal("failure not returned")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "previous" {
		t.Fatal("failed write replaced existing file")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temporary output leaked")
	}
}
func TestPublicationFailureDoesNotCommitSimulation(t *testing.T) {
	adapter := New(filepath.Join(t.TempDir(), "report.json"))
	adapter.publish = func(context.Context, string, io.Reader) error { return errors.New("failed") }
	results, err := adapter.Apply(context.Background(), []model.Change{contract.Change("op", contract.Group("group:a", "a"))})
	if err == nil || results[0].Outcome != model.Retryable {
		t.Fatal("publication failure not retryable")
	}
	obs, _ := adapter.Observe(context.Background(), nil)
	if len(obs.Items) != 0 {
		t.Fatal("failed publication committed state")
	}
	adapter.publish = writeAtomic
	results, err = adapter.Apply(context.Background(), []model.Change{contract.Change("op", contract.Group("group:a", "a"))})
	if err != nil || results[0].Outcome != model.Succeeded {
		t.Fatal("retry did not recover")
	}
}

func TestDuplicateOperationIDsRejected(t *testing.T) {
	contract.RunDuplicateOperationCheck(t, New(filepath.Join(t.TempDir(), "report.json")))
}
func TestConcurrentCapabilitiesAndWrites(t *testing.T) {
	adapter := New(filepath.Join(t.TempDir(), "report.json"))
	start := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-start
		for i := 0; i < 1000; i++ {
			_, _ = adapter.Capabilities(context.Background())
		}
	}()
	close(start)
	for i := 0; i < 10; i++ {
		_, err := adapter.Apply(context.Background(), []model.Change{contract.Change("op", contract.Group("group:a", "a"))})
		if err != nil {
			t.Fatal(err)
		}
	}
	<-done
}
func TestOperationIDBoundAcrossBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	adapter := New(path)
	contract.RunOperationBindingCheck(t, adapter)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"source_identity": "source/Group/group:a"`)) || bytes.Contains(data, []byte("source/Group/group:b")) {
		t.Fatal("rejected intent overwrote operation log")
	}
}
