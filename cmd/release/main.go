// Command release builds, checks and publishes the shim's release (kottera
// ADR-155 D3): one gzip tar per platform whose entries are bin/<role>, and a
// SHA256SUMS over them. Run it from the repository:
//
//	go run ./cmd/release build -out <dir> [-check] [-allow-modified]
//	go run ./cmd/release compare <dir> <dir>
//	go run ./cmd/release publish -tag <tag> <dir>
//
// build -check builds the whole release a second time with an empty build
// cache and fails if any byte differs. compare fails unless two release
// directories, built on different hosts, are identical. publish creates the
// GitHub release for a tag from a built directory; it is the tag workflow's
// last step and reads GITHUB_TOKEN, GITHUB_REPOSITORY and GITHUB_API_URL.
//
// It is Go rather than a script because nothing in this repository is a
// shell script (kottera ADR-155 D3).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kottera/zig-cc-shim/internal/release"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "build":
		err = build(os.Args[2:])
	case "compare":
		err = compare(os.Args[2:])
	case "publish":
		err = publish(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  go run ./cmd/release build -out <dir> [-check] [-allow-modified]
  go run ./cmd/release compare <dir> <dir>
  go run ./cmd/release publish -tag <tag> <dir>`)
	os.Exit(2)
}

func build(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("out", "", "the directory to write the release into (required)")
	check := fs.Bool("check", false, "build everything a second time with an empty build cache and fail if any byte differs")
	allowModified := fs.Bool("allow-modified", false, "build from a checkout with uncommitted changes (never for a published release)")
	fs.Parse(args)
	if *out == "" || fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}
	first, err := release.Build(".", "", *allowModified, os.Stdout)
	if err != nil {
		return err
	}
	if *check {
		cache, err := os.MkdirTemp("", "zig-cc-shim-release-gocache-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(cache)
		fmt.Printf("release: building again with an empty build cache (%s)\n", cache)
		second, err := release.Build(".", cache, *allowModified, os.Stdout)
		if err != nil {
			return err
		}
		if diffs := release.Diff(first, second); len(diffs) > 0 {
			return fmt.Errorf("two builds of %s differ, so the release is not reproducible:\n  %s", first.Stamp.Revision, strings.Join(diffs, "\n  "))
		}
		fmt.Println("release: two builds are byte-identical")
	}
	if err := first.Write(*out); err != nil {
		return err
	}
	fmt.Printf("release: %s (%s) written to %s\n%s", first.Stamp.Version, first.Stamp.Revision, *out, first.Files[release.SumsName])
	return nil
}

func compare(args []string) error {
	if len(args) != 2 {
		usage()
	}
	a, err := release.Load(args[0])
	if err != nil {
		return err
	}
	b, err := release.Load(args[1])
	if err != nil {
		return err
	}
	if diffs := release.Diff(a, b); len(diffs) > 0 {
		return fmt.Errorf("%s and %s differ, so the release is not reproducible across hosts:\n  %s", args[0], args[1], strings.Join(diffs, "\n  "))
	}
	fmt.Printf("release: %s and %s are byte-identical\n", args[0], args[1])
	return nil
}

func publish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	tag := fs.String("tag", "", "the tag to publish the release under (required)")
	fs.Parse(args)
	if *tag == "" || fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	p := &release.Publisher{
		API:   os.Getenv("GITHUB_API_URL"),
		Repo:  os.Getenv("GITHUB_REPOSITORY"),
		Token: os.Getenv("GITHUB_TOKEN"),
	}
	if p.API == "" {
		p.API = "https://api.github.com"
	}
	if p.Repo == "" || p.Token == "" {
		return errors.New("publish needs GITHUB_REPOSITORY and GITHUB_TOKEN, which a workflow provides")
	}
	rel, err := release.Load(fs.Arg(0))
	if err != nil {
		return err
	}
	u, err := p.Publish(context.Background(), *tag, rel, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Println("release: published", u)
	return nil
}
