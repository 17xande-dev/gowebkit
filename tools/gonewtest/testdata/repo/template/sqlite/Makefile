.PHONY: gate
gate:
	@test "$$(go list -m)" = "example.com/generated"
	@test "$$(go run .)" = "recorded revision" || { echo "wrong template revision"; exit 1; }
	@test "$$(cat LICENSE)" = "Fixture licence from the repository root."
	@test ! -e nested
	@test ! -e sibling.txt
	@echo checked generated fixture
