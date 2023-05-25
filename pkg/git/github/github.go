package github

import (
	"context"
	"fmt"
	"github.com/xanzy/go-gitlab"
	"io"
	"net/http"

	"github.com/google/go-github/v49/github"
	"golang.org/x/oauth2"
)

func CreateGitHubWebhook(ctx context.Context, repoOwner, repoName, payloadURL, webhookSecret, personalAccessToken string) error {
	if true {
		t := true
		f := false
		baseURL := "http://gitlab.example.com/"
		glabCli, err := gitlab.NewClient(personalAccessToken, gitlab.WithBaseURL(baseURL))
		if err != nil {
			panic(err)
		}
		aho := &gitlab.AddProjectHookOptions{
			EnableSSLVerification: &f,
			PushEvents:            &t,
			Token:                 &webhookSecret,
			URL:                   &payloadURL,
		}
		_, _, err = glabCli.Projects.AddProjectHook(repoOwner+"/"+repoName, aho)
		if err != nil {
			panic(err)
		}
		return nil
	}
	hook := &github.Hook{
		Name:   github.String("web"),
		Active: github.Bool(true),
		Events: []string{
			"issue_comment",
			"pull_request",
			"push",
		},
		Config: map[string]interface{}{
			"url":          payloadURL,
			"content_type": "json",
			"insecure_ssl": "1", // TODO fix insecure (default should be 0)
			"secret":       webhookSecret,
		},
	}

	ghClient, err := newGHClientByToken(ctx, personalAccessToken, "")
	if err != nil {
		return err
	}

	_, res, err := ghClient.Repositories.CreateHook(ctx, repoOwner, repoName, hook)
	if err != nil {
		return err
	}

	if res.Response.StatusCode != http.StatusCreated {
		payload, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		return fmt.Errorf("failed to create webhook on repository %v/%v, status code: %v, error : %v",
			repoOwner, repoName, res.Response.StatusCode, payload)
	}

	return nil
}

func newGHClientByToken(ctx context.Context, personalAccessToken, ghApiURL string) (*github.Client, error) {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: personalAccessToken},
	)

	if ghApiURL == "" || ghApiURL == "https://api.github.com/" {
		return github.NewClient(oauth2.NewClient(ctx, ts)), nil
	}

	// GitHub Enterprise
	gprovider, err := github.NewEnterpriseClient(ghApiURL, "", oauth2.NewClient(ctx, ts))
	if err != nil {
		return nil, err
	}
	return gprovider, nil
}
