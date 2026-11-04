#!/usr/bin/env bash
# Runs this module against the last release and the checkout, in turn,
# baton-spec's "repeats" times each, then prints the comparison.
#
#   internal/bench/bench.sh [OUT_DIR]
#
# The last release is the newest tag before HEAD; BENCH_AGAINST=vX.Y.Z picks
# another.
set -euo pipefail

out="${1:-$(dirname "$0")/../../bench-results}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
cd "$(dirname "$0")"
against="${BENCH_AGAINST:-$(git describe --tags --abbrev=0 HEAD^)}"
against="v${against#v}"
repeats="$(python3 -c 'import json; print(json.load(open("../../baton-spec/bench/bench.json"))["repeats"])')"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

go build -o "$work/checkout" .
mkdir "$work/published-src"
cp main.go go.mod go.sum "$work/published-src/"
(
  cd "$work/published-src"
  go mod edit -dropreplace=github.com/good-timing/baton-go -require="github.com/good-timing/baton-go@$against"
  go mod tidy
  go build -o "$work/published" .
)

for n in $(seq "$repeats"); do
  for side in published checkout; do
    echo "run $n of $repeats: $side" >&2
    "$work/$side" -source "$side" -out "$out/$side-$n.json" 2>"$out/$side-$n.log"
  done
done
python3 ../../baton-spec/scripts/compare_bench.py "$out"/published-*.json "$out"/checkout-*.json | tee "$out/compare.md"
