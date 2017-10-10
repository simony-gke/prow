/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package owners

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/sirupsen/logrus"

	"k8s.io/kubernetes/pkg/util/sets"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/git"
	"k8s.io/test-infra/prow/github"
)

const (
	ownersFileName  = "OWNERS"
	aliasesFileName = "OWNERS_ALIASES"
	// Github's api uses "" (empty) string as basedir by convention but it's clearer to use "/"
	baseDirConvention = ""
)

var dirBlacklist = sets.NewString(".git", "_output")

type ownersConfig struct {
	Assignees []string `json:"assignees,omitempty"`
	Approvers []string `json:"approvers,omitempty"`
	Reviewers []string `json:"reviewers,omitempty"`
	Labels    []string `json:"labels,omitempty"`
}

type githubClient interface {
	ListCollaborators(org, repo string) ([]github.User, error)
}

type Client struct {
	git *git.Client
	ghc githubClient
	log *logrus.Entry

	mdYAMLEnabled func(org, repo string) bool

	//cache map[string](SHA, *RepoOwners /*may be empty except for RepoAliases*/)
}

func NewClient(ca *config.Agent, gc *git.Client, ghc *github.Client, log *logrus.Entry) *Client {
	return &Client{
		git: gc,
		ghc: ghc,
		log: log,

		mdYAMLEnabled: mdYAMLEnabledFromConfig(ca),
	}
}

type RepoAliases map[string]sets.String

type RepoOwners struct {
	RepoAliases

	approvers map[string]sets.String
	reviewers map[string]sets.String
	labels    map[string]sets.String

	baseDir      string
	enableMDYAML bool

	log *logrus.Entry
}

// LoadRepoAliases returns a RepoAliases struct for the specified repo.
// If the repo does not have an aliases file then an empty alias map is returned with no error.
func (c *Client) LoadRepoAliases(org, repo string) (RepoAliases, error) {
	log := c.log.WithFields(logrus.Fields{"org": org, "repo": repo})
	// TODO: check cache first (need matching org, repo, sha)

	gitRepo, err := c.git.Clone(fmt.Sprintf("%s/%s", org, repo))
	if err != nil {
		return nil, fmt.Errorf("failed to clone %s/%s: %v", org, repo, err)
	}
	defer gitRepo.Clean()
	aliases := loadAliasesFrom(gitRepo.Dir, log)
	// TODO: save to cache

	return aliases, nil
}

func (c *Client) LoadRepoOwners(org, repo string) (*RepoOwners, error) {
	log := c.log.WithFields(logrus.Fields{"org": org, "repo": repo})

	mdYaml := c.mdYAMLEnabled(org, repo)
	// TODO: check cache first (need matching org, repo, sha, mdYaml)

	gitRepo, err := c.git.Clone(fmt.Sprintf("%s/%s", org, repo))
	if err != nil {
		return nil, fmt.Errorf("failed to clone %s/%s: %v", org, repo, err)
	}
	defer gitRepo.Clean()

	// TODO: change this to use cache value if it existed when we checked.
	aliases := loadAliasesFrom(gitRepo.Dir, log)
	o, err := loadOwnersFrom(gitRepo.Dir, mdYaml, aliases, log)
	if err != nil {
		return nil, err
	}
	// TODO: save to cache before collaborator filtering.

	// Filter collaborators. We must filter the RepoOwners struct even if it came from the cache
	// because the list of collaborators could have changed without the git SHA changing.
	collaborators, err := c.ghc.ListCollaborators(org, repo)
	if err != nil {
		log.WithError(err).Errorf("Failed to list collaborators while loading RepoOwners. Skipping collaborator filtering.")
	} else {
		o.filterCollaborators(collaborators)
	}
	return o, nil
}

func mdYAMLEnabledFromConfig(ca *config.Agent) func(org, repo string) bool {
	return func(org, repo string) bool {
		enabledRepos := ca.Config().Owners.MDYAMLRepos
		full := fmt.Sprintf("%s/%s", org, repo)
		for _, elem := range enabledRepos {
			if elem == org || elem == full {
				return true
			}
		}
		return false
	}
}

func (a RepoAliases) ExpandAlias(alias string) sets.String {
	if a == nil {
		return nil
	}
	return a[github.NormLogin(alias)]
}

func (a RepoAliases) ExpandAliases(logins sets.String) sets.String {
	if a == nil {
		return logins
	}
	// Make logins a copy of the original set to avoid modifying the original.
	logins = logins.Union(nil)
	for _, login := range logins.List() {
		if expanded := a.ExpandAlias(login); len(expanded) > 0 {
			logins.Delete(login)
			logins = logins.Union(expanded)
		}
	}
	return logins
}

func loadAliasesFrom(baseDir string, log *logrus.Entry) RepoAliases {
	path := filepath.Join(baseDir, aliasesFileName)
	b, err := ioutil.ReadFile(path)
	if err != nil {
		log.WithError(err).Warnf("Failed to read alias file %q. Using empty alias map.", path)
		return nil
	}
	config := &struct {
		Data map[string][]string `json:"aliases,omitempty"`
	}{}
	if err := yaml.Unmarshal(b, config); err != nil {
		log.WithError(err).Errorf("Failed to unmarshal aliases from %q. Using empty alias map.", path)
		return nil
	}

	result := make(RepoAliases)
	for alias, expanded := range config.Data {
		result[github.NormLogin(alias)] = normLogins(expanded)
	}
	log.Infof("Loaded %d aliases from %q.", len(result), path)
	return result
}

func loadOwnersFrom(baseDir string, mdYaml bool, aliases RepoAliases, log *logrus.Entry) (*RepoOwners, error) {
	o := &RepoOwners{
		RepoAliases:  aliases,
		baseDir:      baseDir,
		enableMDYAML: mdYaml,
		log:          log,

		approvers: make(map[string]sets.String),
		reviewers: make(map[string]sets.String),
		labels:    make(map[string]sets.String),
	}

	return o, filepath.Walk(o.baseDir, o.walkFunc)
}

// by default, github's api doesn't root the project directory at "/" and instead uses the empty string for the base dir
// of the project. And the built-in dir function returns "." for empty strings, so for consistency, we use this
// canonicalize to get the directories of files in a consistent format with NO "/" at the root (a/b/c/ -> a/b/c)
func canonicalize(path string) string {
	if path == "." {
		return baseDirConvention
	}
	return strings.TrimSuffix(path, "/")
}

func (o *RepoOwners) walkFunc(path string, info os.FileInfo, err error) error {
	log := o.log.WithField("path", path)
	if err != nil {
		log.WithError(err).Error("Error while walking OWNERS files.")
		return nil
	}
	filename := filepath.Base(path)
	if info.Mode().IsDir() && dirBlacklist.Has(filename) {
		return filepath.SkipDir
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	c := &ownersConfig{}
	// '.md' files may contain assignees at the top of the file in a yaml header
	// Note that these assignees only apply to the file itself.
	if o.enableMDYAML && filename != ownersFileName && strings.HasSuffix(filename, "md") {
		// Parse the yaml header from the file if it exists and marshal into the config
		if err := decodeOwnersMdConfig(path, c); err != nil {
			log.WithError(err).Error("Error decoding OWNERS config from '*.md' file.")
			return nil
		}

		// Set assignees for this file (not the directory) using the relative path if they were found
		relPath, err := filepath.Rel(o.baseDir, path)
		if err != nil {
			log.WithError(err).Errorf("Unable to find relative path between baseDir: %q and path.", o.baseDir)
			return err
		}
		o.applyConfigToPath(relPath, c)
		return nil
	}

	if filename != ownersFileName {
		return nil
	}

	b, err := ioutil.ReadFile(path)
	if err != nil {
		log.WithError(err).Errorf("Failed to read the OWNERS file.")
		return nil
	}

	if err := yaml.Unmarshal(b, c); err != nil {
		log.WithError(err).Errorf("Failed to unmarshal file contents.")
		return nil
	}

	relPath, err := filepath.Rel(o.baseDir, path)
	if err != nil {
		log.WithError(err).Errorf("Unable to find relative path between baseDir: %q and path.", o.baseDir)
		return err
	}
	relPathDir := canonicalize(filepath.Dir(relPath))
	o.applyConfigToPath(relPathDir, c)
	return nil
}

var mdStructuredHeaderRegex = regexp.MustCompile("^---\n(.|\n)*\n---")

// decodeOwnersMdConfig will parse the yaml header if it exists and unmarshal it into an ownersConfig.
// If no yaml header is found, do nothing
// Returns an error if the file cannot be read or the yaml header is found but cannot be unmarshalled.
func decodeOwnersMdConfig(path string, config *ownersConfig) error {
	fileBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	// Parse the yaml header from the top of the file.  Will return an empty string if regex does not match.
	meta := mdStructuredHeaderRegex.FindString(string(fileBytes))

	// Unmarshal the yaml header into the config
	return yaml.Unmarshal([]byte(meta), &config)
}

func normLogins(logins []string) sets.String {
	normed := sets.NewString()
	for _, login := range logins {
		normed.Insert(github.NormLogin(login))
	}
	return normed
}

func (o *RepoOwners) applyConfigToPath(path string, config *ownersConfig) {
	if len(config.Approvers) > 0 || len(config.Assignees) > 0 {
		o.approvers[path] = o.ExpandAliases(normLogins(config.Approvers).Union(normLogins(config.Assignees)))
	}
	if len(config.Reviewers) > 0 {
		o.reviewers[path] = o.ExpandAliases(normLogins(config.Reviewers))
	}
	if len(config.Labels) > 0 {
		o.labels[path] = sets.NewString(config.Labels...)
	}
}

func (o *RepoOwners) filterCollaborators(toKeep []github.User) {
	collabs := sets.NewString()
	for _, keeper := range toKeep {
		collabs.Insert(github.NormLogin(keeper.Login))
	}

	filter := func(ownerMap map[string]sets.String) {
		for path, unfiltered := range ownerMap {
			ownerMap[path] = unfiltered.Intersection(collabs)
		}
	}
	filter(o.approvers)
	filter(o.reviewers)
}

// findOwnersForPath returns the OWNERS file path furthest down the tree for a specified file
// By default we use the reviewers section of owners flag but this can be configured by setting approvers to true
func findOwnersForPath(path string, ownerMap map[string]sets.String) string {
	d := path

	for {
		n, ok := ownerMap[d]
		if ok && len(n) != 0 {
			return d
		}
		if d == baseDirConvention {
			break
		}
		d = filepath.Dir(d)
		d = canonicalize(d)
	}
	return ""
}

// FindApproversOwnersForPath returns the OWNERS file path furthest down the tree for a specified file
// that contains an approvers section
func (o *RepoOwners) FindApproverOwnersForPath(path string) string {
	return findOwnersForPath(path, o.approvers)
}

// FindReviewersOwnersForPath returns the OWNERS file path furthest down the tree for a specified file
// that contains a reviewers section
func (o *RepoOwners) FindReviewersOwnersForPath(path string) string {
	return findOwnersForPath(path, o.reviewers)
}

// FindLabelsForPath returns a set of labels which should be applied to PRs
// modifying files under the given path.
func (o *RepoOwners) FindLabelsForPath(path string) sets.String {
	return entriesForPath(path, o.labels, false, o.enableMDYAML)
}

// entriesForPath returns a set of users who are assignees to the
// requested file. The path variable should be a full path to a filename
// and not directory as the final directory will be discounted if enableMDYAML is true
// leafOnly indicates whether only the OWNERS deepest in the tree (closest to the file)
// should be returned or if all OWNERS in filepath should be returned
func entriesForPath(path string, people map[string]sets.String, leafOnly bool, enableMDYAML bool) sets.String {
	d := path
	if !enableMDYAML || !strings.HasSuffix(path, ".md") {
		// if path is a directory, this will remove the leaf directory, and returns "." for topmost dir
		d = filepath.Dir(d)
		d = canonicalize(path)
	}

	out := sets.NewString()
	for {
		s, ok := people[d]
		if ok {
			out = out.Union(s)
			if leafOnly && out.Len() > 0 {

				break
			}
		}
		if d == baseDirConvention {
			break
		}
		d = filepath.Dir(d)
		d = canonicalize(d)
	}
	return out
}

// LeafApprovers returns a set of users who are the closest approvers to the
// requested file. If pkg/OWNERS has user1 and pkg/util/OWNERS has user2 this
// will only return user2 for the path pkg/util/sets/file.go
func (o *RepoOwners) LeafApprovers(path string) sets.String {
	return entriesForPath(path, o.approvers, true, o.enableMDYAML)
}

// Approvers returns ALL of the users who are approvers for the
// requested file (including approvers in parent dirs' OWNERS).
// If pkg/OWNERS has user1 and pkg/util/OWNERS has user2 this
// will return both user1 and user2 for the path pkg/util/sets/file.go
func (o *RepoOwners) Approvers(path string) sets.String {
	return entriesForPath(path, o.approvers, false, o.enableMDYAML)
}

// LeafReviewers returns a set of users who are the closest reviewers to the
// requested file. If pkg/OWNERS has user1 and pkg/util/OWNERS has user2 this
// will only return user2 for the path pkg/util/sets/file.go
func (o *RepoOwners) LeafReviewers(path string) sets.String {
	return entriesForPath(path, o.reviewers, true, o.enableMDYAML)
}

// Reviewers returns ALL of the users who are reviewers for the
// requested file (including reviewers in parent dirs' OWNERS).
// If pkg/OWNERS has user1 and pkg/util/OWNERS has user2 this
// will return both user1 and user2 for the path pkg/util/sets/file.go
func (o *RepoOwners) Reviewers(path string) sets.String {
	return entriesForPath(path, o.reviewers, false, o.enableMDYAML)
}
