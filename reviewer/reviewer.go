// Copyright 2019 Palantir Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reviewer

import (
	"context"
	"math/rand"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"

	"github.com/palantir/policy-bot/policy/common"
	"github.com/palantir/policy-bot/pull"
)

func findLeafChildren(result common.Result) []common.Result {
	var r []common.Result
	if len(result.Children) == 0 {
		if result.Status == common.StatusPending && result.Error == nil {
			return []common.Result{result}
		}
	} else {
		for _, c := range result.Children {
			if c.Status == common.StatusPending {
				r = append(r, findLeafChildren(*c)...)
			}
		}
	}
	return r
}

func listAllTeamMembers(ctx context.Context, client *github.Client, team *github.Team) ([]string, error) {
	opt := &github.TeamListTeamMembersOptions{
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	// get all pages of results
	var allUsers []string

	for {
		users, resp, err := client.Teams.ListTeamMembers(ctx, team.GetID(), opt)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list team %s members page %d", team.GetName(), opt.Page)
		}
		for _, u := range users {
			allUsers = append(allUsers, u.GetLogin())
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return allUsers, nil
}

func listAllOrgMembers(ctx context.Context, client *github.Client, org string) ([]string, error) {
	opt := &github.ListMembersOptions{
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	// get all pages of results
	var allUsers []string

	for {
		users, resp, err := client.Organizations.ListMembers(ctx, org, opt)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list members of org %s page %d", org, opt.Page)
		}
		for _, u := range users {
			allUsers = append(allUsers, u.GetLogin())
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return allUsers, nil
}

func listAllCollaborators(ctx context.Context, client *github.Client, org, repo, desiredPerm string) ([]string, error) {
	opt := &github.ListCollaboratorsOptions{
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	// get all pages of results
	var allUsers []string

	for {
		users, resp, err := client.Repositories.ListCollaborators(ctx, org, repo, opt)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list members of org %s page %d", org, opt.Page)
		}
		for _, u := range users {
			perm, _, err := client.Repositories.GetPermissionLevel(ctx, org, repo, u.GetLogin())
			if err != nil {
				return nil, errors.Wrapf(err, "failed to determine permission level of %s on repo %s", u.GetLogin(), repo)
			}
			if perm.GetPermission() == desiredPerm {
				allUsers = append(allUsers, u.GetLogin())
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return allUsers, nil
}

// select n random values from the list of users
func selectRandomUsers(n int, users []string) []string {
	generated := map[int]struct{}{}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	var selections []string
	if n == 0 {
		return selections
	}
	if n >= len(users) {
		return users
	}

	for i := 0; i < n; i++ {
		for {
			i := r.Intn(len(users))
			if _, ok := generated[i]; !ok {
				generated[i] = struct{}{}
				selections = append(selections, users[i])
				break
			}
		}
	}
	return selections
}

func FindRandomRequesters(ctx context.Context, prctx pull.Context, result common.Result, client *github.Client) ([]string, error) {
	logger := zerolog.Ctx(ctx)
	pendingLeafNodes := findLeafChildren(result)
	var requestedUsers []string
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	logger.Debug().Msgf("Collecting reviewers for %d pending leaf nodes", len(pendingLeafNodes))

	for _, child := range pendingLeafNodes {
		allUsers := child.RequestedUsers

		if len(child.RequestedTeams) > 0 {
			randomTeam := child.RequestedTeams[r.Intn(len(child.RequestedTeams))]
			teamInfo := strings.Split(randomTeam, "/")
			team, _, err := client.Teams.GetTeamBySlug(ctx, teamInfo[0], teamInfo[1])
			if err != nil {
				logger.Debug().Err(err).Msgf("Unable to get member listing for team %s", randomTeam)
				//return nil, errors.Wrapf(err, "Unable to get information for team %s", randomTeam)
			} else {
				teamMembers, err := listAllTeamMembers(ctx, client, team)
				if err != nil {
					logger.Debug().Err(err).Msgf("Unable to get member listing for team %s", randomTeam)
					//return nil, errors.Wrapf(err, "Unable to get member listing for team %s", randomTeam)
				} else {
					allUsers = append(allUsers, teamMembers...)
				}
			}
		}

		if len(child.RequestedOrganizations) > 0 {
			randomOrg := child.RequestedOrganizations[r.Intn(len(child.RequestedOrganizations))]
			orgMembers, err := listAllOrgMembers(ctx, client, randomOrg)
			if err != nil {
				return nil, errors.Wrapf(err, "Unable to get member listing for org %s", randomOrg)
			}
			allUsers = append(child.RequestedUsers, orgMembers...)
		}

		if child.RequestedAdmins {
			repoAdmins, err := listAllCollaborators(ctx, client, prctx.RepositoryOwner(), prctx.RepositoryName(), "admin")
			if err != nil {
				return nil, errors.Wrapf(err, "Unable to get admin listing")
			}
			allUsers = append(child.RequestedUsers, repoAdmins...)
		}

		if child.RequestedWriteCollaborators {
			repoCollaborators, err := listAllCollaborators(ctx, client, prctx.RepositoryOwner(), prctx.RepositoryName(), "write")
			if err != nil {
				return nil, errors.Wrapf(err, "Unable to get admin listing")
			}
			allUsers = append(child.RequestedUsers, repoCollaborators...)
		}

		// Remove author before randomly selecting
		logger.Debug().Msgf("Found %q total candidates for review; randomly selecting some", allUsers)
		for i, u := range allUsers {
			if u == prctx.Author() {
				allUsers[i] = allUsers[len(allUsers)-1]
				allUsers[len(allUsers)-1] = ""
				allUsers = allUsers[:len(allUsers)-1]
			}
		}
		logger.Debug().Msgf("Found %q total candidates for review after removing author; randomly selecting some", allUsers)
		randomSelection := selectRandomUsers(child.RequiredCount, allUsers)
		logger.Debug().Msgf("Collected reviewers %q", randomSelection)

		requestedUsers = append(requestedUsers, randomSelection...)
	}
	logger.Debug().Msgf("all reviewers %q", requestedUsers)
	return requestedUsers, nil
}