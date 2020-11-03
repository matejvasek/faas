package faas

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/markbates/pkger"
)

// Updating Templates:
// See documentation in ./templates/README.md

// DefautlTemplate is the default Function signature / environmental context
// of the resultant template.  All runtimes are expected to have at least
// an HTTP Handler ("http") and Cloud Events ("events")
const DefaultTemplate = "http"

// fileAccessor encapsulates methods for accessing template files.
type fileAccessor interface {
	Stat(name string) (os.FileInfo, error)
	Open(p string) (file, error)
	Walk(p string, wf filepath.WalkFunc) error
}

type file interface {
	Readdir(int) ([]os.FileInfo, error)
	Read([]byte) (int, error)
	Close() error
}

// When pkger is run, code analysis detects this Include statement,
// triggering the serializaation of the templates directory and all
// its contents into pkged.go, which is then made available via
// a pkger fileAccessor.
// Path is relative to the go module root.
func init() {
	_ = pkger.Include("/templates")
}

type templateWriter struct {
	verbose   bool
	templates string
}

func (n templateWriter) Write(runtime, template string, dest string) error {
	if template == "" {
		template = DefaultTemplate
	}

	// TODO: Confirm the dest path is empty?  This is currently in an earlier
	// step of the create process but future calls directly to initialize would
	// be better off being made safe.

	if isEmbedded(runtime, template) {
		return copyEmbedded(runtime, template, dest)
	}
	if n.templates != "" {
		return copyFilesystem(n.templates, runtime, template, dest)
	}
	return fmt.Errorf("A template for runtime '%v' template '%v' was not found internally and no custom template path was defined.", runtime, template)
}

func copyEmbedded(runtime, template, dest string) error {
	// Copy files to the destination
	// Example embedded path:
	//   /templates/go/http
	src := filepath.Join(string(filepath.Separator), "templates", runtime, template)
	fa := chrootedFileAccessor{
		accessor: embeddedAccessor{},
		root: src,
	}
	return copy(fa, dest, string(filepath.Separator))
}

func copyFilesystem(templatesPath, runtime, templateFullName, dest string) error {
	// ensure that the templateFullName is of the format "repoName/templateName"
	cc := strings.Split(templateFullName, "/")
	if len(cc) != 2 {
		return errors.New("Template name must be in the format 'REPO/NAME'")
	}
	repo := cc[0]
	template := cc[1]

	// Example FileSystem path:
	//   /home/alice/.config/faas/templates/boson-experimental/go/json
	src := filepath.Join(templatesPath, repo, runtime, template)
	fa := filesystemAccessor{}
	return copy(fa, dest, src)
}

func isEmbedded(runtime, template string) bool {
	_, err := pkger.Stat(filepath.Join(string(filepath.Separator), "templates", runtime, template))
	return err == nil
}

func copy(fs fileAccessor, dest, src string) error {
	fs.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			err = os.MkdirAll(filepath.Join(dest,relPath), info.Mode())
			return err
		} else {
			srcF, err := fs.Open(path)
			if err != nil {
				return err
			}
			defer srcF.Close()
			destF, err := os.OpenFile(filepath.Join(dest, relPath), os.O_RDWR|os.O_CREATE|os.O_TRUNC, info.Mode())
			if err != nil {
				return err
			}
			defer destF.Close()
			_, err = io.Copy(destF, srcF)
			return err
		}
	})
	return nil
}

type embeddedAccessor struct{}

func (a embeddedAccessor) Stat(path string) (os.FileInfo, error) {
	return pkger.Stat(path)
}

func (a embeddedAccessor) Open(path string) (file, error) {
	return pkger.Open(path)
}

func (a embeddedAccessor) Walk(dir string, wf filepath.WalkFunc) error {
	return pkger.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if strings.Contains(path, ":") {
			path = strings.Join(strings.Split(path, ":")[1:], "")
		}
		return wf(path, info, err)
	})
}

type filesystemAccessor struct{}

func (a filesystemAccessor) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (a filesystemAccessor) Open(path string) (file, error) {
	return os.Open(path)
}

func (a filesystemAccessor) Walk(p string, wf filepath.WalkFunc) error {
	return filepath.Walk(p, wf)
}

type chrootedFileAccessor struct {
	accessor fileAccessor
	root     string
}

func (c chrootedFileAccessor) Stat(name string) (os.FileInfo, error) {
	name = filepath.Join(string(filepath.Separator), name)
	name = filepath.Join(c.root, name)
	return c.accessor.Stat(name)
}

func (c chrootedFileAccessor) Open(name string) (file, error) {
	name = filepath.Join(string(filepath.Separator), name)
	name = filepath.Join(c.root, name)
	return c.accessor.Open(name)
}

func (c chrootedFileAccessor) Walk(name string, wf filepath.WalkFunc) error {
	name = filepath.Join(string(filepath.Separator), name)
	name = filepath.Join(c.root, name)
	return c.accessor.Walk(name, func(path string, info os.FileInfo, err error) error {
		rel, err := filepath.Rel(c.root, path)
		if err != nil {
			return err
		}
		path = filepath.Join(string(filepath.Separator), rel)
		return wf(path, info, err)
	})
}

type fileAccessorWithRenames struct {
	accessor fileAccessor
	renames  map[string]string
}

func (f fileAccessorWithRenames) Stat(name string) (os.FileInfo, error) {
	renamed, ok := f.renames[filepath.Clean(name)]
	if ok {
		name = renamed
	}
	return f.accessor.Stat(name)
}

func (f fileAccessorWithRenames) Open(name string) (file, error) {
	renamed, ok := f.renames[filepath.Clean(name)]
	if ok {
		name = renamed
	}
	return f.accessor.Open(name)
}

func (f fileAccessorWithRenames) Walk(name string, wf filepath.WalkFunc) error {
	renamed, ok := f.renames[filepath.Clean(name)]
	if ok {
		name = renamed
	}
	
}
