GOVULNCHECK_VERSION ?= v1.9.0

.PHONY: tools-test hygiene check-all check-all-fast check-latest
# Also installs govulncheck (pinned) into your Go bin directory, then runs it.
tools-test:
	node --test tools/e2e/browser.test.mjs
	cd tools && GOWORK=off go vet ./...
	cd tools && GOWORK=off go test -race -count=1 ./...
	cd tools && GOWORK=off go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	cd tools && bin="$$(go env GOBIN)" && GOWORK=off "$${bin:-$$(go env GOPATH)/bin}/govulncheck" ./...

# Set HYGIENE_REQUIRE_PRIVATE=1 to fail when the private pattern list is absent.
hygiene:
	cd tools && GOWORK=off go run ./hygiene -root .. $(if $(filter 1,$(HYGIENE_REQUIRE_PRIVATE)),-require-private)

check-all check-all-fast:
	$(MAKE) tools-test
	@private="$${XDG_CONFIG_HOME:-$$HOME/.config}/gowebkit/hygiene-private.txt"; \
		if [ -n "$$GOWEBKIT_HYGIENE_PATTERNS" ] || [ -e "$$private" ] || [ -L "$$private" ]; then \
			$(MAKE) HYGIENE_REQUIRE_PRIVATE=1 hygiene; \
		else \
			$(MAKE) hygiene; \
		fi
	cd tools && GOWORK=off go run ./variantsync -root .. -check
	bash tools/local-check.sh $@

check-latest:
	bash tools/local-check.sh $@
