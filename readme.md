[![Build](https://github.com/perjahn/bitbucketreviewer/actions/workflows/build.yml/badge.svg)](https://github.com/perjahn/bitbucketreviewer/actions/workflows/build.yml)
[![Dependency Graph](https://github.com/perjahn/bitbucketreviewer/actions/workflows/dependabot/update-graph/badge.svg)](https://github.com/perjahn/bitbucketreviewer/actions/workflows/dependabot/update-graph)

# Bitbucket Reviewer

This service is a Bitbucket webhook receiver, intended to auto-approve PRs based on content in the repo, in particular yaml files with an owner field.

When all owners (non-default reviewers in Bitbucket) have approved a PR, it will not yet be able to be merged, but the account configured for this service should be a default reviewer, and as soon as this webhook receiver has been triggered it will approve the PR and it will then be able to be merged.

## What it does

This service listens for Bitbucket pull request webhook events, fetches the code by cloning the repo, derives owners from yaml files in the repository, and if all owners have approved the PR this service approves it as well, else owners, if missing, are added as additional PR reviewers.

- Parses incoming webhook payloads into a Bitbucket event model
- Retrieves the affected repository associated with the PR and inspects yaml files changed between the base branch and the PR branch
- Reads the `owner` field from those yaml files and splits comma/semicolon-separated values into reviewer usernames
- If all owners have approved the PR, it's also approved by the configured Bitbucket account automatically
- else
- Constructs a new PR payload with owners as additional reviewers, and PUTs the updated payload back to the same PR that triggered the event

## Environment variables

Set the following variables before running the app:

- `BR_BITBUCKET_USERNAME`: Bitbucket username
- `BR_BITBUCKET_PASSWORD`: Bitbucket password
  - or `BR_BITBUCKET_TOKEN`: Bitbucket access token
- `BR_WEBHOOK_USERNAME`: Bitbucket webhook username
- `BR_WEBHOOK_PASSWORD`: Bitbucket webhook password
- `BR_ALLOWED_PR_ORIGIN`: allowed protocol + host for the PR url, for example `https://bitbucket.org/`
- `BR_IGNORE_OWNERS`: optional comma-separated list of owners that will be excluded from approving the pull request
- `BR_DRY_RUN_REPOS`: optional comma-separated list of repo names that shouldn't be written to
- `BR_PORT`: optional port number that the service should listen on. Default 8080.

Example:

```bash
export BR_BITBUCKET_USERNAME="my-user"
export BR_BITBUCKET_PASSWORD="my-password"
export BR_WEBHOOK_USERNAME="my-webhook-user"
export BR_WEBHOOK_PASSWORD="my-webhook-password"
export BR_ALLOWED_PR_ORIGIN="https://bitbucket.org/"
```

## Run locally

```bash
go run .
```

The server listens on port `8080`.

## Docker

Build the image:

```bash
docker build -t bitbucketreviewer .
```

Run the container:

```bash
docker run -p 8080:8080 \
  -e BR_BITBUCKET_USERNAME="my-user" \
  -e BR_BITBUCKET_PASSWORD="my-password" \
  -e BR_WEBHOOK_USERNAME="my-webhook-user" \
  -e BR_WEBHOOK_PASSWORD="my-webhook-password" \
  -e BR_ALLOWED_PR_ORIGIN="https://bitbucket.org/" \
  bitbucketreviewer
```

## Setup

- Add the BR_BITBUCKET_USERNAME as repo admin, and as a default reviewer in the Bitbucket repo
- Add a webhook for the Bitbucket repo and configure its credentials with BR_WEBHOOK_USERNAME/BR_WEBHOOK_PASSWORD

## Notes

- The app validates that the request has an Authorization header with basic auth that matches the configured webhook credentials `BR_WEBHOOK_USERNAME` and `BR_WEBHOOK_PASSWORD`.
- The app validates that the PR url matches the configured `BR_ALLOWED_PR_ORIGIN`.
- If the required configuration is missing, the app exits with a configuration error.
- Both owners from the base commit and from the tip of the PR branch, must approve the PR before it's auto-approved by this service.
- Owners that don't have write access to the repo, cannot be added as reviewers.
