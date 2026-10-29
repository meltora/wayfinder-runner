#!/bin/sh
set -eu

GOVULNCHECK_VERSION=v1.8.0
GOSEC_VERSION=v2.29.0

usage() {
	echo "usage: sh scripts/check.sh [format|vet|test|vuln|sec|all]..." >&2
}

check_format() {
	unformatted=$(gofmt -l .)
	if [ -n "$unformatted" ]; then
		echo "gofmt: these files are not formatted:" >&2
		echo "$unformatted" >&2
		return 1
	fi
}

check_vet() {
	go vet ./...
	GOOS=darwin GOARCH=arm64 go vet ./...
}

check_test() {
	go test -race -count=1 ./...
}

check_vuln() {
	go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" ./...
}

check_sec() {
	go run "github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}" -quiet -exclude-generated ./...
}

if [ "$#" -eq 0 ]; then
	set -- all
fi
for check in "$@"; do
	case "$check" in
	format | vet | test | vuln | sec | all) ;;
	*)
		usage
		exit 2
		;;
	esac
done

cd "$(dirname "$0")/.."
for check in "$@"; do
	steps=$check
	if [ "$check" = all ]; then
		steps="format vet test vuln sec"
	fi
	for step in $steps; do
		echo "==> $step" >&2
		"check_$step"
	done
done
