#!/usr/bin/env bash
set -euo pipefail

RUN_STRESS_J1=1 go test ./test/integration -run TestE2EAcceptanceJ1Stress100Turns -count=1
