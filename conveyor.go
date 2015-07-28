package conveyor

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/net/context"
	"golang.org/x/oauth2"

	"github.com/google/go-github/github"
)

const (
	// Context is used for the commit status context.
	Context = "container/docker"

	// DefaultBuilderImage is the docker image used to build docker images.
	DefaultBuilderImage = "remind101/conveyor-builder"
)

type BuildOptions struct {
	// Repository is the repo to build.
	Repository string
	// Sha is the git commit to build.
	Sha string
	// Branch is the name of the branch that this build relates to.
	Branch string
	// An io.Writer where output will be written to.
	OutputStream io.Writer
}

type Conveyor struct {
	Builder
}

// NewFromEnv returns a new Conveyor instance with options configured from the
// environment variables.
func NewFromEnv() (*Conveyor, error) {
	b := &statusUpdaterBuilder{
		Builder: &dockerBuilder{},
		github:  newGitHubClient(os.Getenv("GITHUB_TOKEN")),
	}
	return &Conveyor{
		Builder: b,
	}, nil
}

// Build builds a docker image for the
func (c *Conveyor) Build(ctx context.Context, opts BuildOptions) (string, error) {
	return c.Builder.Build(ctx, opts)
}

// githubClient represents a client that can create github commit statuses.
type githubClient interface {
	CreateStatus(owner, repo, ref string, status *github.RepoStatus) (*github.RepoStatus, *github.Response, error)
}

// newGitHubClient returns a new githubClient instance. If token is an empty
// string, then a fake client will be returned.
func newGitHubClient(token string) githubClient {
	if token == "" {
		return &nullGitHubClient{}
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(oauth2.NoContext, ts)
	return github.NewClient(tc).Repositories
}

// nullGitHubClient is an implementation of the githubClient interface that does
// nothing.
type nullGitHubClient struct{}

func (c *nullGitHubClient) CreateStatus(owner, repo, ref string, status *github.RepoStatus) (*github.RepoStatus, *github.Response, error) {
	fmt.Printf("Updating status of %s on %s/%s to %s\n", ref, owner, repo, *status.State)
	return nil, nil, nil
}