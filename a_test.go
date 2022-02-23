package function

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func Test_AAA(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	uri := fmt.Sprint(`file://` + filepath.ToSlash(filepath.Join(wd, "testdata", "repository.git")))

	t.Log(uri)

	gitFS, err := filesystemFromRepo(uri)
	if err != nil {
		t.Fatal(err)
	}
	if gitFS == nil {
		t.Fatal("FS is nil")
	}
	des, err := gitFS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		t.Log(de.Name())
	}
}
