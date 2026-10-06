package release

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Publisher creates a GitHub release of one repository through the REST
// API, with the token Actions gives a workflow. It is plain net/http rather
// than a client library or the gh command, so publishing is a Go command
// like everything else here and adds no dependency.
type Publisher struct {
	// API is the REST API's base URL, https://api.github.com in Actions
	// (GITHUB_API_URL).
	API string
	// Repo is owner/name (GITHUB_REPOSITORY).
	Repo  string
	Token string
	HTTP  *http.Client
}

// Publish creates the release for tag and uploads every file of rel. It is
// created as a draft and made public only once every asset is uploaded, so
// no one sees a release with half its assets. A tag that already has a
// release is refused by GitHub, and that refusal is returned: a published
// release is never replaced.
//
// Every asset rel holds must be named for tag, which is what ties the
// files to the tag they are published under.
func (p *Publisher) Publish(ctx context.Context, tag string, rel *Release, log io.Writer) (string, error) {
	want := []string{SumsName}
	for _, pl := range Platforms {
		want = append(want, AssetName(tag, pl))
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(rel.Files)); !slices.Equal(got, want) {
		return "", fmt.Errorf("the release holds %q, but tag %s needs exactly %q: it was built from another commit", got, tag, want)
	}

	var created struct {
		ID        int64  `json:"id"`
		UploadURL string `json:"upload_url"`
		HTMLURL   string `json:"html_url"`
	}
	body := map[string]any{"tag_name": tag, "name": tag, "draft": true}
	if err := p.call(ctx, http.MethodPost, p.API+"/repos/"+p.Repo+"/releases", "application/json", jsonBody(body), &created); err != nil {
		return "", fmt.Errorf("creating the release for %s: %w", tag, err)
	}
	upload, _, _ := strings.Cut(created.UploadURL, "{")
	for _, name := range want {
		ctype := "application/gzip"
		if name == SumsName {
			ctype = "text/plain"
		}
		u := upload + "?name=" + url.QueryEscape(name)
		if err := p.call(ctx, http.MethodPost, u, ctype, rel.Files[name], nil); err != nil {
			return "", fmt.Errorf("uploading %s to the draft release %d: %w", name, created.ID, err)
		}
		fmt.Fprintf(log, "release: uploaded %s\n", name)
	}
	patch := fmt.Sprintf("%s/repos/%s/releases/%d", p.API, p.Repo, created.ID)
	if err := p.call(ctx, http.MethodPatch, patch, "application/json", jsonBody(map[string]any{"draft": false}), &created); err != nil {
		return "", fmt.Errorf("publishing the draft release %d: %w", created.ID, err)
	}
	return created.HTMLURL, nil
}

func jsonBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (p *Publisher) call(ctx context.Context, method, u, ctype string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", ctype)
	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, u, resp.Status, bytes.TrimSpace(b))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
