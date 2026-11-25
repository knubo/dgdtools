VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test corpus vsix clean

build:
	go build $(LDFLAGS) -o bin/dgdtools ./cmd/dgdtools

test:
	go test -race ./...

# Full-corpus invariants: lex round-trip, comparator self-compare, and the
# formatter gate over every .c/.h under LPC_CORPUS.
corpus:
	@test -n "$(LPC_CORPUS)" || (echo "set LPC_CORPUS=/path/to/lpc/tree" && exit 2)
	LPC_CORPUS=$(LPC_CORPUS) go test -tags corpus -v -run 'TestCorpus' ./...

# The VS Code extension as a package for this machine, with the dgdtools
# just built inside it: editors/vscode/dgdtools-PLATFORM-VERSION.vsix
# (editors/README.md). It needs Node.js, and installs what
# editors/vscode/package-lock.json pins, inside that folder.
vsix: build
	cd editors/vscode && npm ci && node scripts/vsix.mts --binary ../../bin/dgdtools

clean:
	rm -rf bin
