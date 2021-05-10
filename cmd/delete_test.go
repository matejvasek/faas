package cmd

import (
	"context"
	fn "github.com/boson-project/func"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

type testRemover struct {
	invokedWith *string
}

func (t *testRemover) Remove(ctx context.Context, name string) error {
	t.invokedWith = &name
	return nil
}

// test delete outside project just using function name
func TestDeleteCmdWithoutProject(t *testing.T) {
	tr := &testRemover{}
	cmd := NewDeleteCmd(func(ns string, verbose bool) (fn.Remover, error) {
		return tr, nil
	})

	cmd.SetArgs([]string{"foo"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	if tr.invokedWith == nil {
		t.Fatal("fn.Remover has not been invoked")
	}

	if *tr.invokedWith != "foo" {
		t.Fatalf("expected fn.Remover to be called with 'foo', but was called with '%s'", *tr.invokedWith)
	}
}

// test delete from inside project directory (reading func name from func.yaml)
func TestDeleteCmdWithProject(t *testing.T) {
	funcYaml := `name: bar
namespace: ""
runtime: go
image: ""
imageDigest: ""
trigger: http
builder: quay.io/boson/faas-go-builder
builderMap:
  default: quay.io/boson/faas-go-builder
env: {}
annotations: {}
`
	tmpDir, err := ioutil.TempDir("", "bar")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	f, err := os.Create(filepath.Join(tmpDir, "func.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	f.WriteString(funcYaml)
	f.Close()


	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	err = 	os.Chdir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	tr := &testRemover{}
	cmd := NewDeleteCmd(func(ns string, verbose bool) (fn.Remover, error) {
		return tr, nil
	})

	cmd.SetArgs([]string{"-p", "."})
	err = cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	if tr.invokedWith == nil {
		t.Fatal("fn.Remover has not been invoked")
	}

	if *tr.invokedWith != "bar" {
		t.Fatalf("expected fn.Remover to be called with 'bar', but was called with '%s'", *tr.invokedWith)
	}
}