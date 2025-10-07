package githubapp

import (
	"net/http"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v75/github"
)

type Clients struct {
	REST *github.Client
	HTTP *http.Client // also used by githubv4
}

// NewClients creates an installation-scoped client for a given installation ID.
func NewClients(appID int64, installationID int64, pem []byte) (*Clients, error) {
	tr := http.DefaultTransport
	itr, err := ghinstallation.New(tr, appID, installationID, pem)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Transport: itr}
	return &Clients{
		REST: github.NewClient(httpClient),
		HTTP: httpClient,
	}, nil
}
