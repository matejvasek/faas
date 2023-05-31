package gitlab

import (
	"context"
	"fmt"
	"github.com/xanzy/go-gitlab"
)

func CreateGitLabWebhook(ctx context.Context, repoOwner, repoName, payloadURL, webhookSecret, personalAccessToken string) error {
	t := true
	f := false
	baseURL := "http://gitlab.127.0.0.1.sslip.io/"
	glabCli, err := gitlab.NewClient(personalAccessToken,
		gitlab.WithBaseURL(baseURL),
		gitlab.WithRequestOptions(gitlab.WithContext(ctx)))
	if err != nil {
		panic(err)
	}
	webhook := &gitlab.AddProjectHookOptions{
		EnableSSLVerification: &f,
		PushEvents:            &t,
		Token:                 &webhookSecret,
		URL:                   &payloadURL,
	}
	_, _, err = glabCli.Projects.AddProjectHook(repoOwner+"/"+repoName, webhook)
	if err != nil {
		return fmt.Errorf("cannot create gitlab webhook: %w", err)
	}
	return nil
}
