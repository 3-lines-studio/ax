#!/bin/sh
set -eu

dir=${1:-bin}
status=0

budget() {
    case $1 in
    ax) echo 8800000 ;;
    boti) echo 8200000 ;;
    webi) echo 9500000 ;;
    slaxi) echo 8063000 ;;
    fsx) echo 2970000 ;;
    bashx) echo 3200000 ;;
    attachx) echo 8000000 ;;
    browserx) echo 9589000 ;;
    wax) echo 14369000 ;;
    sshx) echo 9000000 ;;
    skillx) echo 2934000 ;;
    memoryx) echo 3200000 ;;
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
