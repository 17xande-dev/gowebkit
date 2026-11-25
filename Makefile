GOVULNCHECK_VERSION ?= v1.9.0

.PHONY: tools-test hygiene
# Also installs govulncheck (pinned) into your Go bin directory, then runs it.
tools-test:
	cd tools && GOWORK=off go vet ./...
	cd tools && GOWORK=off go test -race -count=1 ./...
	cd tools && GOWORK=off go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	cd tools && bin="$$(go env GOBIN)" && GOWORK=off "$${bin:-$$(go env GOPATH)/bin}/govulncheck" ./...

# Set HYGIENE_REQUIRE_PRIVATE=1 to fail when the private pattern list is absent.
hygiene:
	cd tools && GOWORK=off go run ./hygiene -root .. $(if $(filter 1,$(HYGIENE_REQUIRE_PRIVATE)),-require-private)
