package e2e

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

const updateTemplatesFolder = "update_templates"

// Update replaces the project content (source files) of the existing project in test
// by the source stored under 'update_template/<runtime>/<template>
// Once sources are update the project is built and re-deployed
func Update(t *testing.T, knFunc *TestShellCmdRunner, project *FunctionTestProject) {

	templatePath := filepath.Join(updateTemplatesFolder, project.Runtime, project.Template)
	if _, err := os.Stat(templatePath); err != nil {
		if os.IsNotExist(err) {
			// skip update test when there is no template folder
			return
		} else {
			t.Fatal(err.Error())
		}
	}

	// Template folder exists for given runtime / template.
	// Let's update the project and redeploy
	err := projectUpdaterFor(project).UpdateFolderContent(t, templatePath, project)
	if err != nil {
		t.Fatal("an error has occurred while updating project folder with new sources.", err.Error())
	}

	previousRevision := GetCurrentServiceRevision(t, project.FunctionName)

	// Redeploy function
	Deploy(t, knFunc, project)

	// Waits New Revision to become ready
	NewRevisionCheck(t, previousRevision, project.FunctionName)

	// Indicates new project (from update templates) is in use
	project.IsNewRevision = true
}

// projectUpdater offers methods to update the project source content by the
// source provided on update_templates folder
// The strategy used consists in
// 1. Create a temporary project folder with some files from original test folder (such as func.yaml, pom.xml)
// 2. Copy recursivelly all files from ./update_template/<runtime>/<template>/** to the temporary project folder
// 3. Replace current project folder by the temporary one (rm -rf <project folder> && mv <tmp folder> <project folder>
type projectUpdater struct {
	retainList []string // List of files to retain from original test project
}

func projectUpdaterFor(project *FunctionTestProject) projectUpdater {
	updater := projectUpdater{retainList: []string{"func.yaml"}}
	if project.Runtime == "springboot" {
		updater.retainList = append(updater.retainList, "pom.xml")
	}
	if project.Runtime == "quarkus" {
		updater.retainList = append(updater.retainList, "pom.xml")
	}
	return updater
}

func (p projectUpdater) UpdateFolderContent(t *testing.T, templatePath string, project *FunctionTestProject) error {
	// Create temp project folder (reuse func.yaml)
	projectTmp := NewFunctionTestProject(t, project.Runtime, project.Template)
	projectTmp.ProjectPath = projectTmp.ProjectPath + "-tmp"
	err := projectTmp.CreateProjectFolder()
	if err != nil {
		return err
	}
	defer func() {
		_ = projectTmp.RemoveProjectFolder()
	}()

	// Copy files to retain (let's reuse some such as func.yaml, pom.xml)
	for _, sourceFile := range p.retainList {
		targetDir := filepath.Join(projectTmp.ProjectPath, filepath.Dir(sourceFile))
		_, err = os.Stat(targetDir)
		if err != nil && os.IsNotExist(err) {
			err = os.MkdirAll(targetDir, os.ModePerm)
			if err != nil {
				return err
			}
		}
		err = p.copyFile(filepath.Join(project.ProjectPath, sourceFile), filepath.Join(projectTmp.ProjectPath, sourceFile))
		if err != nil {
			return err
		}
	}

	// Copy from template structure to new project folders and files
	err = p.walkThru(templatePath, func(path string, f os.FileInfo) error {
		var err error = nil
		if !f.IsDir() {
			if templatePath == path { // root path
				err = p.copyFile(filepath.Join(templatePath, f.Name()), filepath.Join(projectTmp.ProjectPath, f.Name()))
			} else {
				// Create dir if not exists
				relativePath, _ := filepath.Rel(templatePath, path)
				targetDir := filepath.Join(projectTmp.ProjectPath, relativePath)
				_, err = os.Stat(targetDir)
				if err != nil && os.IsNotExist(err) {
					err = os.MkdirAll(targetDir, os.ModePerm)
					if err != nil {
						return err
					}
				}
				err = p.copyFile(filepath.Join(templatePath, relativePath, f.Name()), filepath.Join(targetDir, f.Name()))
			}
		}
		return err
	})
	if err != nil {
		return err
	}

	// Replace project folder
	err = project.RemoveProjectFolder()
	if err != nil {
		return err
	}
	return os.Rename(projectTmp.ProjectPath, project.ProjectPath)
}

// walkThru recursive visit files in the filesystem and invokes fn for each of them
// it can be replaced in future by filepath.WalkDir when project moves to 1.16+)
func (p projectUpdater) walkThru(dir string, fn func(path string, f os.FileInfo) error) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		fileInfo, err := file.Info()
		if err != nil {
			return err
		}
		err = fn(dir, fileInfo)
		if err != nil {
			return err
		}
		if file.IsDir() {
			err := p.walkThru(filepath.Join(dir, file.Name()), fn)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (p projectUpdater) copyFile(sourceFile string, destFile string) error {
	fi, err := os.Stat(sourceFile)
	if err != nil {
		return err
	}
	src, err := os.Open(sourceFile)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}
