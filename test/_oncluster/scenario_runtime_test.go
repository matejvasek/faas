//go:build oncluster || runtime
// +build oncluster runtime

package oncluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/func/k8s"
	common "knative.dev/func/test/_common"
	e2e "knative.dev/func/test/_e2e"
)

// TestRuntime will invoke a language runtime test against (by default) to all runtimes.
// The Environment Variable E2E_RUNTIMES can be used to select the languages/runtimes to be tested
func TestRuntime(t *testing.T) {
	var runtimeList = []string{}
	runtimes, present := os.LookupEnv("E2E_RUNTIMES")

	if present {
		if runtimes != "" {
			runtimeList = strings.Split(runtimes, " ")
		}
	} else {
		runtimeList = []string{"node", "python", "quarkus", "springboot", "typescript"} // "go" and "rust" pending support
	}
	for _, lang := range runtimeList {
		t.Run(lang+"_test", func(t *testing.T) {
			runtimeImpl(t, lang)
		})
	}

}

func runtimeImpl(t *testing.T, lang string) {
	start := time.Now()
	defer func() {
		cli, err := k8s.NewKubernetesClientset()
		if err != nil {
			panic(err)
		}
		opt := metav1.ListOptions{}
		l, err := cli.EventsV1().Events("default").List(context.Background(), opt)
		if err != nil {
			panic(err)
		}
		for _, item := range l.Items {
			if item.EventTime.After(start) {
				_, _ = fmt.Fprintf(os.Stderr, "event: %+v\n", item)
			}
		}
	}()

	var gitProjectName = "test-func-lang-" + lang
	var gitProjectPath = filepath.Join(t.TempDir(), gitProjectName)
	var funcName = gitProjectName
	var funcPath = gitProjectPath

	gitServer := common.GitTestServerProvider{}
	gitServer.Init(t)
	remoteRepo := gitServer.CreateRepository(gitProjectName)
	defer gitServer.DeleteRepository(gitProjectName)

	knFunc := common.NewKnFuncShellCli(t)
	knFunc.Exec("create", "-l", lang, funcPath)
	defer os.RemoveAll(gitProjectPath)

	GitInitialCommitAndPush(t, gitProjectPath, remoteRepo.ExternalCloneURL)

	knFunc.Exec("deploy",
		"-v",
		"-r", e2e.GetRegistry(),
		"-p", funcPath,
		"--remote",
		"--git-url", remoteRepo.ClusterCloneURL)

	defer knFunc.Exec("delete", "-p", funcPath)

	// -- Assertions --
	result := knFunc.Exec("invoke", "-p", funcPath)
	t.Log(result)
	AssertThatTektonPipelineRunSucceed(t, funcName)

}
