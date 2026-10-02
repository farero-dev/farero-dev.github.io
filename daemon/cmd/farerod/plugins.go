package main

// githubClientID is the farero-dev GitHub OAuth App's client id. It is
// public (device flow needs no secret) and set at release build time with
// -ldflags "-X main.githubClientID=...". FARERO_GITHUB_CLIENT_ID overrides it.
var githubClientID = ""
