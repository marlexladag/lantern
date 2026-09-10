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
# Note: Only detects scripts with ./ prefix (e.g., ./scripts/foo.sh).
# Does not detect bash scripts/foo.sh or other forms.
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

# npm built-in commands that don't require package.json entries
NPM_BUILTINS="ci install audit dedupe diff exec pkg pack publish run start stop test uninstall update view"

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
echo
echo "Checking Go version compatibility..."
GO_MOD_FILE="$REPO_ROOT/go.mod"
GO_VERSION_MOD=""
if [[ -f "$GO_MOD_FILE" ]]; then
    GO_VERSION_MOD=$(grep '^go ' "$GO_MOD_FILE" | awk '{print $2}')
fi

# Collect ALL setup-go steps from all jobs
GO_VERSIONS=$(python3 -c "
import yaml
import sys

with open('$WORKFLOW_FILE') as f:
    workflow = yaml.safe_load(f)

# For each job, find all setup-go steps
for job_name, job in workflow.get('jobs', {}).items():
    for step_idx, step in enumerate(job.get('steps', [])):
        if step.get('uses', '').startswith('actions/setup-go'):
            with_config = step.get('with', {})
            # Check for different ways to specify Go version
            if 'go-version' in with_config:
                version = with_config['go-version'].lstrip('v')
                print(f'{job_name}:{step_idx}:version:{version}')
            elif 'go-version-file' in with_config:
                # go-version-file is a valid alternative
                file_path = with_config['go-version-file']
                print(f'{job_name}:{step_idx}:go-version-file:{file_path}')
            else:
                # No recognized version specification found
                print(f'{job_name}:{step_idx}:UNKNOWN')
")

if [[ -z "$GO_VERSIONS" ]]; then
    echo "  (No setup-go steps found; skipping Go version check)"
else
    # Track if we found any Go version to verify
    FOUND_ANY_VERSION=false
    GO_CHECK_FAILED=false

    while IFS=':' read -r job_name step_idx version_type version_value; do
        if [[ "$version_type" == "version" ]]; then
            FOUND_ANY_VERSION=true

            if [[ -n "$GO_VERSION_MOD" ]]; then
                # Compare versions (simple numeric comparison for X.Y format)
                WF_MAJOR=$(echo "$version_value" | cut -d. -f1)
                WF_MINOR=$(echo "$version_value" | cut -d. -f2)
                MOD_MAJOR=$(echo "$GO_VERSION_MOD" | cut -d. -f1)
                MOD_MINOR=$(echo "$GO_VERSION_MOD" | cut -d. -f2)

                WF_NUM=$((WF_MAJOR * 1000 + WF_MINOR))
                MOD_NUM=$((MOD_MAJOR * 1000 + MOD_MINOR))

                if [[ $WF_NUM -lt $MOD_NUM ]]; then
                    fail "Go version in job '$job_name' step $step_idx ($version_value) is older than go.mod requires ($GO_VERSION_MOD)"
                fi
                echo "  ✓ $job_name: Go $version_value (matches go.mod requirement)"
            else
                echo "  ✓ $job_name: Go $version_value (go.mod not found to verify)"
            fi
        elif [[ "$version_type" == "go-version-file" ]]; then
            echo "  ✓ $job_name: step $step_idx uses go-version-file ($version_value) — cannot verify without reading file"
        elif [[ "$version_type" == "UNKNOWN" ]]; then
            fail "setup-go step in job '$job_name' (step $step_idx) does not specify go-version or go-version-file. Cannot determine Go version requirement."
        fi
    done <<< "$GO_VERSIONS"

    if [[ "$FOUND_ANY_VERSION" == true ]]; then
        pass "Go version compatibility verified"
    fi
fi

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
