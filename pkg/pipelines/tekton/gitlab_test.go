package tekton_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/xanzy/go-gitlab"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/html"
	"k8s.io/apimachinery/pkg/util/uuid"
	"knative.dev/func/pkg/docker"
	fn "knative.dev/func/pkg/functions"
	"knative.dev/func/pkg/pipelines"
	"knative.dev/func/pkg/pipelines/tekton"
	"knative.dev/func/pkg/random"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func getAPIToken(host, username, password string) (string, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", fmt.Errorf("cannot create a cookie jar: %w", err)
	}

	c := http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	signInURL := fmt.Sprintf("http://%s/users/sign_in", host)

	resp, err := c.Get(signInURL)
	if err != nil {
		return "", fmt.Errorf("cannot get sign in page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("cannot get sign in page, unexpected status: %d", resp.StatusCode)
	}

	node, err := html.Parse(resp.Body)
	if err != nil {
		return "", fmt.Errorf("cannot parse sign in page: %w", err)
	}

	csrfToken := getCSRFToken(node)

	form := url.Values{}
	form.Add("authenticity_token", csrfToken)
	form.Add("user[login]", username)
	form.Add("user[password]", password)
	form.Add("user[remember_me]", "0")

	req, err := http.NewRequest("POST", signInURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("cannot create sign in request: %w", err)
	}

	req.Header.Add("Origin", fmt.Sprintf("http://%s", host))
	req.Header.Add("Referer", signInURL)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err = c.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot sign in: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 302 {
		return "", fmt.Errorf("cannot sign in, unexpected status: %d", resp.StatusCode)
	}

	personalAccessTokensURL := fmt.Sprintf("http://%s/-/profile/personal_access_tokens", host)

	resp, err = c.Get(personalAccessTokensURL)
	if err != nil {
		return "", fmt.Errorf("cannot get personal access tokens: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("cannot get personal access tokens, unexpected status: %d", resp.StatusCode)
	}

	node, err = html.Parse(resp.Body)
	if err != nil {
		return "", fmt.Errorf("cannot parse personal access tokens page: %w", err)
	}
	csrfToken = getCSRFToken(node)

	form = url.Values{
		"personal_access_token[name]":       {"test-2"},
		"personal_access_token[expires_at]": {time.Now().Add(time.Hour * 25).Format("2006-01-02")},
		"personal_access_token[scopes][]":   {"api", "read_api", "read_user", "read_repository", "write_repository", "sudo"},
	}

	req, err = http.NewRequest("POST", personalAccessTokensURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("cannot create new personal access token request: %w", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Add("X-CSRF-Token", csrfToken)

	resp, err = c.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot create new personal access token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("cannot create new personal access token, unexpected status: %d", resp.StatusCode)
	}

	data := struct {
		NewToken string `json:"new_token,omitempty"`
	}{}
	e := json.NewDecoder(resp.Body)
	err = e.Decode(&data)
	if err != nil {
		return "", fmt.Errorf("cannot parse token form a response: %w", err)
	}

	return data.NewToken, nil
}

func getCSRFToken(n *html.Node) string {
	var match bool
	var token string
	for _, a := range n.Attr {
		if a.Key == "name" && (a.Val == "authenticity_token" || a.Val == "csrf-token") {
			match = true
		}
		if a.Key == "value" || a.Key == "content" {
			token = a.Val
		}
	}
	if match {
		return token
	}

	for n = n.FirstChild; n != nil; n = n.NextSibling {
		token = getCSRFToken(n)
		if token != "" {
			return token
		}
	}

	return ""
}

func p[T any](t T) *T {
	return &t
}

func generateSSHKeys(t *testing.T) string {
	tmp := t.TempDir()

	privateKeyPath := filepath.Join(tmp, "id_ecdsa")
	publicKeyPath := privateKeyPath + ".pub"

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyBlock := &pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: privateKeyBytes,
	}
	privateKeyFile, err := os.OpenFile(privateKeyPath, os.O_CREATE|os.O_WRONLY, 0400)
	if err != nil {
		t.Fatal(err)
	}
	err = pem.Encode(privateKeyFile, privateKeyBlock)
	if err != nil {
		t.Fatal(err)
	}

	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKeyBytes := ssh.MarshalAuthorizedKey(publicKey)

	err = os.WriteFile(publicKeyPath, publicKeyBytes, 0444)
	if err != nil {
		t.Fatal(err)
	}

	return privateKeyPath
}

func setup(t *testing.T, host, username, password string) {
	randStr := strings.ToLower(random.AlphaString(5))
	funcUser := "func_user_" + randStr
	funcUserPwd := "1ddqd1dkf@"
	funcGrp := "func-grp-" + randStr
	funcProj := "func-proj-" + randStr
	funcImg := fmt.Sprintf("ttl.sh/func/fn-%s:5m", uuid.NewUUID())

	rootToken, err := getAPIToken(host, username, password)
	if err != nil {
		t.Fatal(err)
	}

	baseURL := fmt.Sprintf("http://%s", host)
	glabCli, err := gitlab.NewClient(rootToken, gitlab.WithBaseURL(baseURL))
	if err != nil {
		t.Fatal(err)
	}

	pat, _, err := glabCli.PersonalAccessTokens.GetSinglePersonalAccessToken()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = glabCli.PersonalAccessTokens.RevokePersonalAccessToken(pat.ID)
	})

	userOpts := &gitlab.CreateUserOptions{
		Name:                p("John Doe"),
		Username:            p(funcUser),
		Password:            p(funcUserPwd),
		ForceRandomPassword: p(false),
		Email:               p(fmt.Sprintf("%s@example.com", funcUser)),
	}

	u, _, err := glabCli.Users.CreateUser(userOpts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("created user")
	t.Cleanup(func() {
		_, _ = glabCli.Users.DeleteUser(u.ID)
	})

	groupOpts := &gitlab.CreateGroupOptions{
		Name:                 p(funcGrp),
		Path:                 p(funcGrp),
		Description:          p("group for `func` testing"),
		Visibility:           p(gitlab.PublicVisibility),
		RequireTwoFactorAuth: p(false),
		ProjectCreationLevel: p(gitlab.DeveloperProjectCreation),
		LFSEnabled:           p(true),
		RequestAccessEnabled: p(true),
	}
	g, _, err := glabCli.Groups.CreateGroup(groupOpts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("created group")
	t.Cleanup(func() {
		_, _ = glabCli.Groups.DeleteGroup(g.ID)
	})

	grpMemOpts := &gitlab.AddGroupMemberOptions{
		UserID:      p(u.ID),
		AccessLevel: p(gitlab.MaintainerPermissions),
	}
	_, _, err = glabCli.GroupMembers.AddGroupMember(g.ID, grpMemOpts)
	if err != nil {
		t.Fatal(err)
	}

	creatProjOpts := &gitlab.CreateProjectOptions{
		Name:                 p(funcProj),
		NamespaceID:          p(g.ID),
		Path:                 p(funcProj),
		Visibility:           p(gitlab.PublicVisibility),
		InitializeWithReadme: p(true),
		DefaultBranch:        p("main"),
	}
	proj, _, err := glabCli.Projects.CreateProject(creatProjOpts)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("created project")
	t.Cleanup(func() {
		_, _ = glabCli.Projects.DeleteProject(proj.ID)
	})

	sshPrivateKeyPath := generateSSHKeys(t)
	sshPublicKeyBytes, err := os.ReadFile(sshPrivateKeyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	userToken, err := getAPIToken(host, funcUser, funcUserPwd)
	if err != nil {
		t.Fatal(err)
	}

	glabCliUser, err := gitlab.NewClient(userToken, gitlab.WithBaseURL(baseURL))
	if err != nil {
		t.Fatal(err)
	}

	addSshKeyOpts := &gitlab.AddSSHKeyOptions{
		Title: p("func-ssh-key"),
		Key:   p(string(sshPublicKeyBytes)),
	}
	_, _, err = glabCliUser.Users.AddSSHKey(addSshKeyOpts)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("URL: ", proj.SSHURLToRepo)
	t.Log("URL: ", proj.HTTPURLToRepo)
	t.Log("USR: ", funcUser)
	t.Log("PWD: ", funcUserPwd)

	tempHome := t.TempDir()
	projDir := filepath.Join(t.TempDir(), "fn")
	err = os.MkdirAll(projDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	f := fn.Function{
		Root:     projDir,
		Name:     funcProj,
		Runtime:  "test-runtime",
		Template: "test-template",
		Image:    funcImg,
		Created:  time.Now(),
		Invoke:   "none",
		Build: fn.BuildSpec{
			Git: fn.Git{
				URL:      strings.TrimSuffix(proj.HTTPURLToRepo, ".git"),
				Revision: "devel",
			},
			BuilderImages: map[string]string{"pack": "docker.io/paketobuildpacks/builder:tiny"},
			Builder:       "pack",
			PVCSize:       "256Mi",
		},
		Deploy: fn.DeploySpec{
			Remote: true,
		},
	}
	f = fn.NewFunctionWith(f)
	err = f.Write()
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(projDir, "Procfile"), []byte("web: non-existent-app\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	credentialsProvider := func(ctx context.Context, image string) (docker.Credentials, error) {
		return docker.Credentials{
			Username: "",
			Password: "",
		}, nil
	}
	pp := tekton.NewPipelinesProvider(tekton.WithCredentialsProvider(credentialsProvider))

	metadata := pipelines.PacMetadata{
		RegistryUsername:          "",
		RegistryPassword:          "",
		RegistryServer:            "",
		GitProvider:               "", // "gitlab",
		PersonalAccessToken:       userToken,
		WebhookSecret:             "",
		ConfigureLocalResources:   true,
		ConfigureClusterResources: true,
		ConfigureRemoteResources:  true,
	}
	err = pp.ConfigurePAC(context.Background(), f, metadata)
	if err != nil {
		t.Fatal(err)
	}

	gitCommands := `export GIT_TERMINAL_PROMPT=0 && \
  cd "${PROJECT_DIR}" && \
  git config --global user.name "John Doe" && \
  git config --global user.email "jdoe@example.com" && \
  git config --global core.sshCommand "ssh -i ${SSH_PK} -o UserKnownHostsFile=${HOME}/known_hosts -o StrictHostKeyChecking=no" && \
  git init --initial-branch=devel && \
  git remote add origin "${REPO_URL}" && \
  git add . && \
  git commit -m "commit message" && \
  git push -u origin devel
`
	cmd := exec.Command("sh", "-c", gitCommands)
	cmd.Env = []string{
		"PROJECT_DIR=" + projDir,
		"SSH_PK=" + sshPrivateKeyPath,
		"REPO_URL=" + proj.SSHURLToRepo,
		"HOME=" + tempHome,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Log(string(out))
		t.Fatal(err)
	}
}

func TestGitlab(t *testing.T) {
	setup(t, "gitlab.example.com", "root", "TZj4JFHsxy3jF7F5P5kzQbUByF4SSM6b5F1S58vFGGQ=")
	t.Log("Setup done!")
}
