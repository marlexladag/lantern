#!/bin/bash
set -euo pipefail

# Determine the repo root regardless of how this script is invoked
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WORKFLOW_FILE="$REPO_ROOT/.github/workflows/build.yml"

cd "$REPO_ROOT"

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

pass() {
    echo "PASS: $1"
}

echo "=== CI Workflow Validator ==="
echo "Repo root: $REPO_ROOT"
echo "Workflow: $WORKFLOW_FILE"
echo

# Check 1: Workflow file exists
if [[ ! -f "$WORKFLOW_FILE" ]]; then
    fail "Workflow file does not exist: $WORKFLOW_FILE"
fi
pass "Workflow file exists"

# Check 2: YAML is valid and PyYAML is available
if ! python3 -c "import yaml" 2>/dev/null; then
    fail "PyYAML not available. Install with: python3 -m pip install PyYAML"
fi

if ! python3 -c "import yaml; yaml.safe_load(open('$WORKFLOW_FILE'))" 2>&1; then
    fail "Workflow YAML parsing failed"
fi
pass "Workflow YAML parses correctly"

# Check 3: Extract and verify all shell scripts referenced
#
# Every `*.sh` token in a `run:` block is matched, not just the `./foo.sh`
# form the original regex looked for. The narrow version was a vacuous-pass
# hazard: rewriting a step as `bash scripts/foo.sh` made the script invisible
# to this check, which would then report success having verified nothing at
# all — the exact failure mode the rest of this validator is written to avoid.
#
# How a script is invoked decides what is required of it:
#   ./scripts/foo.sh  or  scripts/foo.sh   -> the kernel execs it: must exist
#                                             AND carry the executable bit
#   bash scripts/foo.sh, sh ..., source ...-> the interpreter opens and reads
#                                             it: must exist; the exec bit is
#                                             irrelevant, so requiring it here
#                                             would be a false failure
echo
echo "Checking shell scripts..."
SCRIPTS=$(python3 -c "
import yaml
import re

with open('$WORKFLOW_FILE') as f:
    workflow = yaml.safe_load(f)

# Commands that read a script rather than exec it, so the executable bit is
# not required. '.' is bash's source builtin.
READERS = {'bash', 'sh', 'zsh', 'dash', 'ksh', 'source', '.'}

# A path-like token ending in .sh, with an optional leading './'. The leading
# boundary keeps 'foo.sh' inside a longer word (e.g. 'notascript.shx' or a
# URL fragment) from matching.
TOKEN = re.compile(r'(?<![\w./\\-])(\./)?((?:[\w.-]+/)*[\w.-]+\.sh)\b')

scripts = {}
for job_name, job in workflow.get('jobs', {}).items():
    for step in job.get('steps', []):
        run_cmd = step.get('run', '') or ''
        for line in run_cmd.splitlines():
            for match in TOKEN.finditer(line):
                path = match.group(2)
                # The word immediately before the token decides the mode.
                before = line[:match.start()].split()
                prev = before[-1] if before else ''
                mode = 'read' if prev in READERS else 'exec'
                # If the same script appears both ways, the stricter
                # requirement wins.
                if scripts.get(path) != 'exec':
                    scripts[path] = mode

for path in sorted(scripts):
    print(f'{scripts[path]}:{path}')
")

if [[ -z "$SCRIPTS" ]]; then
    # A workflow that references no scripts is only legitimate when the repo
    # has none to reference. This repo does have them, and CI is the only
    # thing that runs them, so finding zero here means the extraction broke
    # (or a step was deleted) rather than that there is nothing to check.
    if compgen -G "$REPO_ROOT/scripts/*.sh" > /dev/null; then
        fail "No shell scripts referenced in workflow, but $REPO_ROOT/scripts contains .sh files; the workflow should run them (or this check has stopped detecting them)"
    fi
    pass "No shell scripts referenced in workflow"
else
    while IFS=':' read -r mode script; do
        SCRIPT_PATH="$REPO_ROOT/$script"
        if [[ ! -f "$SCRIPT_PATH" ]]; then
            fail "Referenced script does not exist: $script"
        fi
        if [[ "$mode" == "exec" ]]; then
            if [[ ! -x "$SCRIPT_PATH" ]]; then
                fail "Referenced script is not executable: $script"
            fi
            echo "  ✓ $script (exists and executable)"
        else
            echo "  ✓ $script (exists; run via an interpreter, exec bit not required)"
        fi
    done <<< "$SCRIPTS"
    pass "All referenced scripts exist and are runnable as invoked"
fi

# Check 4: Extract and verify all npm scripts
echo
echo "Checking npm scripts..."
NPM_SCRIPTS=$(python3 -c "
import yaml
import sys
import re

with open('$WORKFLOW_FILE') as f:
    workflow = yaml.safe_load(f)

scripts = set()
for job_name, job in workflow.get('jobs', {}).items():
    for step in job.get('steps', []):
        run_cmd = step.get('run', '')
        if run_cmd:
            # Extract npm run/test commands
            for match in re.finditer(r'npm\s+(run\s+)?(\S+)', run_cmd):
                # If 'run' is captured in group 1, then script is in group 2
                # Otherwise it's a built-in command or package.json script
                script_name = match.group(2)
                if match.group(1):  # 'npm run X'
                    scripts.add(script_name)
                else:  # 'npm X'
                    scripts.add(script_name)

for script in sorted(scripts):
    print(script)
")

PACKAGE_JSON="$REPO_ROOT/package.json"
if [[ ! -f "$PACKAGE_JSON" ]]; then
    fail "package.json not found"
fi

# Parse package.json to get defined scripts
DEFINED_SCRIPTS=$(python3 -c "
import json
import sys

with open('$PACKAGE_JSON') as f:
    pkg = json.load(f)

scripts = pkg.get('scripts', {})
for script in sorted(scripts.keys()):
    print(script)
")

# npm built-in commands that don't require package.json entries.
# NOTE: 'test', 'start', 'stop', and 'restart' are deliberately excluded even
# though npm has default no-op behavior for them. Every project that actually
# relies on `npm test` etc. in CI defines a real script for it (this repo
# does: "test": "vitest run") — treating them as unconditionally safe would
# let someone delete the real script and have CI keep reporting success
# while silently running nothing.
NPM_BUILTINS="ci install audit dedupe diff exec pkg pack publish run uninstall update view"

if [[ -z "$NPM_SCRIPTS" ]]; then
    pass "No npm scripts referenced in workflow"
else
    while IFS= read -r script; do
        # Check if it's a built-in npm command
        if echo " $NPM_BUILTINS " | grep -q " $script "; then
            echo "  ✓ npm $script (built-in)"
            continue
        fi

        if ! echo "$DEFINED_SCRIPTS" | grep -q "^${script}$"; then
            fail "npm script '$script' not defined in package.json"
        fi
        echo "  ✓ npm $script (defined in package.json)"
    done <<< "$NPM_SCRIPTS"
    pass "All npm scripts are defined in package.json"
fi

# Check 5: Go version compatibility across all setup-go steps
#
# Only a literal `go-version` key is treated as verifiable: its value is
# compared against the `go` directive in go.mod. Any setup-go step that
# specifies its version another way (go-version-file, or no version key at
# all) is a check this validator cannot evaluate — and an unevaluated check
# must never be reported as passing. It fails loudly instead, naming the
# job and step, rather than silently skipping or reporting success. This
# also means a workflow with zero setup-go steps fails this check (there is
# a go.mod in this repo, so CI must actually verify a Go toolchain version
# against it) instead of quietly passing because there was nothing to look at.
echo
echo "Checking Go version compatibility..."
GO_MOD_FILE="$REPO_ROOT/go.mod"
if [[ ! -f "$GO_MOD_FILE" ]]; then
    fail "go.mod not found: cannot verify workflow Go version against project requirement"
fi
GO_VERSION_MOD=$(grep '^go ' "$GO_MOD_FILE" | awk '{print $2}')
if [[ -z "$GO_VERSION_MOD" ]]; then
    fail "Could not extract a 'go' directive from go.mod"
fi

# One line per actions/setup-go step found, across every job:
#   job:step_idx:version:<value>        -- literal go-version, comparable
#   job:step_idx:unverifiable:<reason>  -- version not directly comparable
GO_STEPS=$(python3 -c "
import yaml

with open('$WORKFLOW_FILE') as f:
    workflow = yaml.safe_load(f)

for job_name, job in workflow.get('jobs', {}).items():
    for step_idx, step in enumerate(job.get('steps', [])):
        if step.get('uses', '').startswith('actions/setup-go'):
            with_config = step.get('with', {}) or {}
            if 'go-version' in with_config and str(with_config['go-version']).strip():
                version = str(with_config['go-version']).strip().lstrip('v')
                print(f'{job_name}:{step_idx}:version:{version}')
            elif 'go-version-file' in with_config:
                file_path = with_config['go-version-file']
                print(f'{job_name}:{step_idx}:unverifiable:uses go-version-file ({file_path}) instead of a literal go-version; this validator has no logic to resolve that file, so it cannot confirm the CI Go toolchain matches go.mod')
            else:
                print(f'{job_name}:{step_idx}:unverifiable:no go-version key found on this setup-go step')
")

if [[ -z "$GO_STEPS" ]]; then
    fail "No actions/setup-go steps found in the workflow; cannot verify the CI Go toolchain matches go.mod (go $GO_VERSION_MOD)"
fi

# Compare only major.minor so a patch component on either side (e.g. go.mod's
# 'go 1.23.1' vs a workflow pin of '1.23') doesn't cause a false mismatch,
# while a genuine drift like '1.23' vs '1.99' is still caught.
normalize_go_version() {
    echo "$1" | awk -F. '{print $1"."$2}'
}
NORM_MOD=$(normalize_go_version "$GO_VERSION_MOD")

while IFS=':' read -r job_name step_idx kind detail; do
    if [[ "$kind" == "version" ]]; then
        NORM_WF=$(normalize_go_version "$detail")
        if [[ "$NORM_WF" != "$NORM_MOD" ]]; then
            fail "Go version in job '$job_name' step $step_idx ($detail) does not match go.mod ($GO_VERSION_MOD)"
        fi
        echo "  ✓ $job_name step $step_idx: Go $detail (matches go.mod)"
    else
        fail "Go version in job '$job_name' step $step_idx is unverifiable: $detail"
    fi
done <<< "$GO_STEPS"

pass "Go version compatibility verified for all setup-go steps"

# Check 6: Verify paths referenced in the workflow
echo
echo "Checking paths referenced in workflow..."
PATHS=$(python3 -c '
import yaml
import re

with open("'"$WORKFLOW_FILE"'") as f:
    workflow = yaml.safe_load(f)

paths = set()

# Extract paths from hashFiles() calls
for job in workflow.get("jobs", {}).values():
    for step in job.get("steps", []):
        for key, value in step.items():
            if isinstance(value, str):
                # hashFiles with single or double quotes
                for match in re.finditer(r"hashFiles\(['\''\"](.*?)['\''\"]\)", value):
                    path = match.group(1)
                    base = path.split("/**")[0]
                    if base:
                        paths.add(base)
            elif isinstance(value, dict):
                for subkey, subvalue in value.items():
                    if isinstance(subvalue, str):
                        for match in re.finditer(r"hashFiles\(['\''\"](.*?)['\''\"]\)", subvalue):
                            path = match.group(1)
                            base = path.split("/**")[0]
                            if base:
                                paths.add(base)

# Extract paths from working-directory keys
for job in workflow.get("jobs", {}).values():
    for step in job.get("steps", []):
        if "working-directory" in step:
            wd = step["working-directory"]
            if wd:
                paths.add(wd)

for path in sorted(paths):
    print(path)
')

if [[ -z "$PATHS" ]]; then
    pass "No static paths to verify in workflow"
else
    MISSING_PATHS=""
    while IFS= read -r path; do
        # Check if path exists as either file or directory
        if [[ ! -e "$REPO_ROOT/$path" ]]; then
            MISSING_PATHS="$MISSING_PATHS $path"
        else
            echo "  ✓ $path (exists)"
        fi
    done <<< "$PATHS"

    if [[ -n "$MISSING_PATHS" ]]; then
        fail "Referenced paths do not exist:$MISSING_PATHS"
    else
        pass "All referenced paths exist"
    fi
fi

echo
echo "=== All checks passed ==="
