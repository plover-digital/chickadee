#!/bin/bash
# Only a coordinator-authorized offline disposable working copy, never a sealed/live image.
set -euo pipefail
cd "$(dirname "$0")/.."
image=${1:?fresh private working image required}
report=${2:?output Java audit JSON required}
[[ -f $image && ! -e $report ]]
virt-customize --no-network --memsize 1024 --smp 1 -a "$image" \
 --upload guest/refresh-ca-trust.py:/tmp/chickadee-refresh-java.py \
 --upload guest/images/ca-trust.json:/tmp/chickadee-java-policy.json \
 --run-command 'python3 /tmp/chickadee-refresh-java.py --policy /tmp/chickadee-java-policy.json --refresh-java-report /etc/chickadee/java-trust.json' \
 --delete /tmp/chickadee-refresh-java.py --delete /tmp/chickadee-java-policy.json \
 --no-selinux-relabel
guestfish --ro -a "$image" -i download /etc/chickadee/java-trust.json "$report"
