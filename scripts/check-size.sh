#!/bin/sh
# Enforce the per-binary size budgets. Called with the directory holding the
# built programs (default: bin). Keeping every binary small is part of the
# ecosystem's design: each release artifact must stay easy to download and
# audit. Raise a budget deliberately, in the same change that grows a binary.
set -eu

dir=${1:-bin}
status=0

budget() {
    case $1 in
    # Context compaction, the MCP catalog and the OAuth client live in ax;
    # the extra megabyte keeps room for the transport in cmd/webi.
    ax) echo 8400000 ;;
    boti) echo 7000000 ;;
    webi) echo 9500000 ;;
    slaxi) echo 8063000 ;;
    fsx) echo 2970000 ;;
    bashx) echo 3200000 ;;
    attachx) echo 8000000 ;;
    browserx) echo 9589000 ;;
    wax) echo 14369000 ;;
    # SSH transport client (x/crypto/ssh + sftp) for remote compute.
    sshx) echo 9000000 ;;
    skillx) echo 2934000 ;;
    slackx) echo 7543000 ;;
    bqx) echo 24789000 ;;
    pgx) echo 10593000 ;;
    *)
        echo "size: no budget for $1; add one to scripts/check-size.sh" >&2
        return 1
        ;;
    esac
}

for path in "$dir"/*; do
    [ -f "$path" ] || continue
    program=$(basename "$path")
    case $program in *.sha256) continue ;; esac
    if ! limit=$(budget "$program"); then
        status=1
        continue
    fi
    size=$(wc -c <"$path" | tr -d ' ')
    if [ "$size" -gt "$limit" ]; then
        printf 'size: %-10s %8d bytes exceeds budget %d\n' "$program" "$size" "$limit" >&2
        status=1
    else
        printf 'size: %-10s %8d / %d bytes\n' "$program" "$size" "$limit"
    fi
done

exit $status
