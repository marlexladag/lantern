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

# Check 2: YAML is valid
if ! python3 -c "import yaml,sys; yaml.safe_load(open('$WORKFLOW_FILE'))" 2>&1; then
    fail "Workflow YAML parsing failed"
fi
pass "Workflow YAML parses correctly"

# Check 3: Extract and verify all shell scripts referenced
echo
echo "Checking shell scripts..."
SCRIPTS=$(python3 -c "
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
            # Extract script paths that start with ./
            for match in re.finditer(r'\./([\w\-./]+\.sh)', run_cmd):
                scripts.add(match.group(1))

for script in sorted(scripts):
    print(script)
")

if [[ -z "$SCRIPTS" ]]; then
    pass "No shell scripts referenced in workflow"
else
    while IFS= read -r script; do
        SCRIPT_PATH="$REPO_ROOT/$script"
        if [[ ! -f "$SCRIPT_PATH" ]]; then
            fail "Referenced script does not exist: $script"
        fi
        if [[ ! -x "$SCRIPT_PATH" ]]; then
            fail "Referenced script is not executable: $script"
        fi
        echo "  ✓ $script (exists and executable)"
    done <<< "$SCRIPTS"
    pass "All referenced scripts exist and are executable"
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
                # Otherwise it's a built-in command like 'ci' or 'test'
                if match.group(1):  # 'npm run X'
                    scripts.add(match.group(2))
                elif match.group(2) in ['test', 'ci']:
                    # These are built-in, but we still want to verify
                    scripts.add(match.group(2))
                else:
                    scripts.add(match.group(2))

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

if [[ -z "$NPM_SCRIPTS" ]]; then
    pass "No npm scripts referenced in workflow"
else
    while IFS= read -r script; do
        # 'ci' is a built-in npm command, not a package.json script
        if [[ "$script" == "ci" ]]; then
            echo "  ✓ npm ci (built-in)"
            continue
        fi

        if ! echo "$DEFINED_SCRIPTS" | grep -q "^${script}$"; then
            fail "npm script '$script' not defined in package.json"
        fi
        echo "  ✓ npm $script (defined in package.json)"
    done <<< "$NPM_SCRIPTS"
    pass "All npm scripts are defined in package.json"
fi

# Check 5: Go version compatibility
echo
echo "Checking Go version compatibility..."
GO_VERSION_WORKFLOW=$(python3 -c "
import yaml
import sys

with open('$WORKFLOW_FILE') as f:
    workflow = yaml.safe_load(f)

# Find setup-go step and extract version
for job in workflow.get('jobs', {}).values():
    for step in job.get('steps', []):
        if step.get('uses', '').startswith('actions/setup-go'):
            with_config = step.get('with', {})
            if 'go-version' in with_config:
                print(with_config['go-version'].lstrip('v'))
                sys.exit(0)
")

GO_MOD_FILE="$REPO_ROOT/go.mod"
if [[ -f "$GO_MOD_FILE" ]]; then
    GO_VERSION_MOD=$(grep '^go ' "$GO_MOD_FILE" | awk '{print $2}')

    # Compare versions (simple numeric comparison for X.Y format)
    if [[ -n "$GO_VERSION_WORKFLOW" ]] && [[ -n "$GO_VERSION_MOD" ]]; then
        WF_MAJOR=$(echo "$GO_VERSION_WORKFLOW" | cut -d. -f1)
        WF_MINOR=$(echo "$GO_VERSION_WORKFLOW" | cut -d. -f2)
        MOD_MAJOR=$(echo "$GO_VERSION_MOD" | cut -d. -f1)
        MOD_MINOR=$(echo "$GO_VERSION_MOD" | cut -d. -f2)

        WF_NUM=$((WF_MAJOR * 1000 + WF_MINOR))
        MOD_NUM=$((MOD_MAJOR * 1000 + MOD_MINOR))

        if [[ $WF_NUM -lt $MOD_NUM ]]; then
            fail "Go version in workflow ($GO_VERSION_WORKFLOW) is older than go.mod requires ($GO_VERSION_MOD)"
        fi
        pass "Go version compatibility: workflow=$GO_VERSION_WORKFLOW, go.mod=$GO_VERSION_MOD"
    fi
else
    echo "  (go.mod not found, skipping Go version check)"
fi

# Check 6: Verify referenced source directories exist
echo
echo "Checking referenced source directories..."
# For now, just verify that src-tauri directory exists (where Cargo.lock should be)
SOURCE_PATHS="src-tauri"

MISSING_PATHS=""
for path in $SOURCE_PATHS; do
    if [[ ! -d "$REPO_ROOT/$path" ]]; then
        MISSING_PATHS="$MISSING_PATHS $path"
    else
        echo "  ✓ $path (exists)"
    fi
done

if [[ -n "$MISSING_PATHS" ]]; then
    fail "Referenced source directories do not exist:$MISSING_PATHS"
fi
pass "All referenced source directories exist"

echo
echo "=== All checks passed ==="
