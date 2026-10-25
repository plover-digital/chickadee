#!/bin/bash
# Offline private working image only. Preparation never launches an appliance.
set -euo pipefail
cd "$(dirname "$0")/.."
image=${1:?private working image or --prepare required}
work=${2:?private input workspace required}
family=${3:?24.04, 26.04 or rocky required}
mode=${4:?incremental or full-builder required}
bootstrap_option=
case $mode in
 incremental)
  [[ ${CHICKADEE_RUNTIME_BOOTSTRAP_SHA256:?accepted bootstrap digest required for incremental refresh} =~ ^[0-9a-f]{64}$ ]]
  bootstrap_option="--expected-bootstrap-sha256 $CHICKADEE_RUNTIME_BOOTSTRAP_SHA256"
  ;;
 full-builder) bootstrap_option=--full-builder ;;
 *) exit 2 ;;
esac
case $family in
 24.04) lock=guest/images/github-ubuntu-2404.json; extra=guest/images/github-extra-tools.json ;;
 26.04) lock=guest/images/github-ubuntu-2604.json; extra=guest/images/github-extra-ubuntu-2604.json ;;
 rocky) lock=guest/images/github-ubuntu-2404.json; extra= ;;
 *) exit 2 ;;
esac
inputs=${CHICKADEE_RUNTIME_INPUTS:-$work/downloads}
verify=(); [[ -z ${CHICKADEE_RUNTIME_INPUTS:-} ]] || verify=(--verify-only)
python3 scripts/prefetch-runtime-inputs.py "${verify[@]}" "$inputs"
mkdir -p "$work"
python3 - "$inputs" "$work/runtime-inputs.tar" <<'PY'
import importlib.util,pathlib,sys,tarfile
spec=importlib.util.spec_from_file_location('inputs','scripts/prefetch-runtime-inputs.py');module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
with tarfile.open(sys.argv[2],'w',dereference=True) as archive:
 for e in module.entries():archive.add(pathlib.Path(sys.argv[1])/e['filename'],arcname=e['filename'],recursive=False)
PY
[[ $image != --prepare ]] || exit 0
extra_args=(); [[ -z $extra ]] || extra_args=(--upload "$extra:/tmp/chickadee-runtime/extra-lock.json")
extra_option=; [[ -z $extra ]] || extra_option='--extra-lock /tmp/chickadee-runtime/extra-lock.json'
virt-customize --no-network --memsize 1024 --smp 1 -a "$image" \
 --mkdir /tmp/chickadee-runtime \
 --upload guest/refresh-runtime-security.py:/tmp/chickadee-runtime/refresh-runtime-security.py \
 --upload guest/refresh-ca-trust.py:/tmp/chickadee-runtime/refresh-ca-trust.py \
 --upload guest/refresh-nss-security.py:/tmp/chickadee-runtime/refresh-nss-security.py \
 --upload guest/install-github-extra-tools.py:/tmp/chickadee-runtime/install-github-extra-tools.py \
 --upload guest/images/runner-linux-x64.json:/tmp/chickadee-runtime/runner.json \
 --upload guest/images/ca-trust.json:/tmp/chickadee-runtime/trust.json \
 --upload guest/images/nss-security.json:/tmp/chickadee-runtime/nss.json \
 --upload guest/images/rocky-ca-security.json:/tmp/chickadee-runtime/rocky.json \
 --upload "$lock:/tmp/chickadee-runtime/lock.json" "${extra_args[@]}" \
 --upload "$work/runtime-inputs.tar":/tmp/chickadee-runtime/inputs.tar \
 --run-command 'mkdir /tmp/chickadee-runtime/inputs; tar -xf /tmp/chickadee-runtime/inputs.tar -C /tmp/chickadee-runtime/inputs' \
 --run-command "python3 /tmp/chickadee-runtime/refresh-runtime-security.py --runner /tmp/chickadee-runtime/runner.json --lock /tmp/chickadee-runtime/lock.json --trust-policy /tmp/chickadee-runtime/trust.json --nss-policy /tmp/chickadee-runtime/nss.json --rocky-policy /tmp/chickadee-runtime/rocky.json $extra_option $bootstrap_option --install /tmp/chickadee-runtime/inputs" \
 --upload guest/package-provenance.py:/tmp/chickadee-runtime/package-provenance.py \
 --run-command 'python3 /tmp/chickadee-runtime/package-provenance.py > /image-package-provenance.json; if test -e /etc/rocky-release; then rpm -qa | sort > /image-packages.txt; else dpkg-query -W > /image-packages.txt; fi' \
 --delete /tmp/chickadee-runtime --no-selinux-relabel
for report in runtime-security runtime-trust; do
 guestfish --ro -a "$image" -i download "/etc/chickadee/$report.json" "$work/$report.json"
done
